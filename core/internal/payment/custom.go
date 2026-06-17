package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Custom（可编程 HTTP 模板引擎）：移植旧平台 CustomEngine（v9.1），用 decimal 重实现。
//
// 非标准支付 API 的终极兜底——一切逻辑都由模板/字段路径配置，引擎本身不含任何硬编码协议。
// 管理员在后台填写「创建请求的 URL/方法/请求体模板/请求头」与「响应/回调里各字段的点路径」
// 以及一个签名方案，即可对接任意 HTTP 支付处理商。
//
// 本文件自带一套 custom* 前缀的私有工具（模板渲染/点路径/验签/币种规整），不依赖其它引擎文件，
// 以便独立编译、避免跨引擎符号耦合。
//
// 🚨 红线（资金）：
//   - VerifyCallback 一律 fail-closed：签名口径不符 / 缺签名 / 解析失败 → 返回 error（Success=false），
//     绝不在未验真的数据上入金。入金/幂等/实付下限校验在 server 层，本引擎只创建+验签+解析。
//   - 模板渲染与签名口径**复刻旧引擎**（占位符 {{order_no}} 等、payload = body 或 timestamp+body、
//     HMAC-SHA256/HMAC-MD5），不臆造。具体处理商上线前仍需抓真实 raw 校正字段点路径与签名口径。
//
// 模板可用变量（customRenderTemplate 把 {{key}} 整串替换）：
//
//	{{order_no}} {{amount}} {{currency}} {{notify_url}} {{return_url}} {{desc}} {{client_ip}} {{api_key}}
//
// 其中 {{amount}} 经币种最小单位规整（customCanonicalAmount，沿用旧 AmountToMinor→AmountFromMinor 往返）：
// USD/CNY 等两位小数、JPY 等零位、BHD 等三位，输出与币种小数位一致。
type Custom struct {
	// ── 渠道标识与密钥 ──
	DisplayName   string // 渠道展示名（Name() 返回）
	APIKey        string // 商户 API key，供 {{api_key}} 模板变量与请求头使用
	WebhookSecret string // 回调验签密钥；为空则跳过验签（仅当 SigAlgo 也为空/none 时安全）

	// ── 默认币种（无显式 Currency 时用，回调币种 tag 兜底）──
	Currency string // 主币种，如 "CNY"/"USD"；默认 "CNY"

	// ── 创建支付（CreatePayment）模板 ──
	CreateMethod  string            // HTTP 方法 "GET"/"POST"；默认 POST
	CreateURL     string            // 完整 URL 模板，含 {{...}} 占位符（必填）
	CreateHeaders map[string]string // 请求头，值同样支持 {{...}} 渲染
	CreateBody    string            // 请求体模板（JSON），含 {{...}}；GET 时忽略

	// ── 创建响应字段提取（点路径，如 "data.url"）──
	PayURLPath string // 提取支付跳转 URL 的点路径；默认 "pay_url"
	RefPath    string // 提取上游订单引用的点路径；默认 "id"

	// ── 回调验签方案 ──
	SigHeader  string // 签名所在请求头名（大小写不敏感）；默认 "x-signature"
	SigAlgo    string // "hmac-sha256" / "hmac-md5"(或 "md5") / "none"；默认 none=不验签
	SigPayload string // 签名原文构造："body" 或 "timestamp+body"；默认 body

	// ── 回调字段提取（点路径）──
	SuccessCondition string // 成功判定 "field_path=expected_value"；为空则「有 order_no 即成功」
	OrderPath        string // 回调里我方单号的点路径；默认 "order_no"
	AmountPath       string // 回调里金额的点路径；默认 "amount"
	TradeNoPath      string // 回调里上游交易号的点路径；默认 "trade_no"
	CurrencyPath     string // 回调里币种的点路径；为空则用 Currency 默认值

	Client *http.Client
}

func (c *Custom) Name() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return "custom"
}

func (c *Custom) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// templateVars：构造模板变量表。{{amount}} 经币种最小单位规整（customCanonicalAmount）。
func (c *Custom) templateVars(req *CreateRequest) map[string]string {
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	}
	if currency == "" {
		currency = "CNY"
	}
	return map[string]string{
		"order_no":   req.OrderNo,
		"amount":     customCanonicalAmount(req.Amount, currency),
		"currency":   currency,
		"notify_url": req.NotifyURL,
		"return_url": req.ReturnURL,
		"desc":       req.Description,
		"client_ip":  req.ClientIP,
		"api_key":    c.APIKey,
	}
}

// CreatePayment：按模板渲染 URL/请求体/请求头，发起创建请求，再按点路径提取支付 URL 与引用。
func (c *Custom) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	if strings.TrimSpace(c.CreateURL) == "" {
		return nil, fmt.Errorf("custom: create_url is required")
	}
	vars := c.templateVars(req)

	createURL := customRenderTemplate(c.CreateURL, vars)
	method := strings.ToUpper(customDefault(c.CreateMethod, "POST"))

	var bodyReader io.Reader
	if c.CreateBody != "" && method != "GET" {
		bodyReader = bytes.NewReader([]byte(customRenderTemplate(c.CreateBody, vars)))
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, createURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("custom build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.CreateHeaders {
		httpReq.Header.Set(k, customRenderTemplate(v, vars))
	}

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("custom request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("custom HTTP %d: %s", resp.StatusCode, excerpt(respBody))
	}

	var respData map[string]interface{}
	_ = json.Unmarshal(respBody, &respData)

	payURLPath := customDefault(c.PayURLPath, "pay_url")
	payURL := customDotPath(respData, payURLPath)
	if payURL == "" {
		// GET-redirect 风格：响应本身就是 3xx 重定向，取 Location（复刻旧引擎）。
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			payURL = resp.Header.Get("Location")
		}
	}
	if payURL == "" {
		return nil, fmt.Errorf("custom: no pay URL found (path=%s): %s", payURLPath, excerpt(respBody))
	}

	providerRef := customDotPath(respData, customDefault(c.RefPath, "id"))

	return &CreateResult{
		PayURL:      payURL,
		ProviderRef: providerRef,
	}, nil
}

// VerifyCallback：先验签（fail-closed），再解析字段、判定成功。
// 🚨 任何签名不符/缺签名/解析失败 → 返回 error（Success=false），绝不在未验真数据上入金。
func (c *Custom) VerifyCallback(rawBody []byte, headers, query map[string]string) (*CallbackResult, error) {
	// ── 验签（复刻旧逻辑：仅当 WebhookSecret 非空且 SigAlgo != none 时校验）──
	if c.WebhookSecret != "" {
		algo := strings.ToLower(customDefault(c.SigAlgo, "none"))
		if algo != "none" {
			sigHeader := customDefault(c.SigHeader, "x-signature")
			sig := customHeaderCI(headers, sigHeader)
			// fail-closed：缺签名头一律拒（红线——有签名方案就必须带有效签名）。
			if sig == "" {
				return nil, fmt.Errorf("custom: missing webhook signature header: %s", sigHeader)
			}
			var payload []byte
			if customDefault(c.SigPayload, "body") == "timestamp+body" {
				ts := customHeaderCI(headers, "x-timestamp")
				if ts == "" {
					ts = customHeaderCI(headers, "t")
				}
				payload = []byte(ts + string(rawBody))
			} else {
				payload = rawBody
			}
			if !customVerifySig(payload, sig, c.WebhookSecret, algo) {
				return nil, fmt.Errorf("custom: invalid webhook signature")
			}
		}
	}

	// ── 解析回调体：先试 JSON，失败再退化为表单（body + query 扁平 map），复刻旧引擎 ──
	var data map[string]interface{}
	if err := json.Unmarshal(rawBody, &data); err != nil {
		data = customFormToMap(rawBody, query)
	}

	orderNo := customDotPath(data, customDefault(c.OrderPath, "order_no"))
	amountStr := customDotPath(data, customDefault(c.AmountPath, "amount"))
	tradeNo := customDotPath(data, customDefault(c.TradeNoPath, "trade_no"))

	// ── 成功判定（复刻旧引擎）──
	success := false
	if cond := strings.TrimSpace(c.SuccessCondition); cond != "" {
		// 格式 "field_path=expected_value"
		if parts := strings.SplitN(cond, "=", 2); len(parts) == 2 {
			success = customDotPath(data, parts[0]) == parts[1]
		}
	} else {
		// 默认：能取到 order_no 即视为成功
		success = orderNo != ""
	}

	// ── 回调币种 tag（CurrencyPath 可覆盖，否则用默认 Currency）──
	cbCurrency := strings.ToUpper(strings.TrimSpace(c.Currency))
	if cbCurrency == "" {
		cbCurrency = "CNY"
	}
	if c.CurrencyPath != "" {
		if v := customDotPath(data, c.CurrencyPath); v != "" {
			cbCurrency = strings.ToUpper(v)
		}
	}

	// 金额：旧引擎回调金额按 major 单位直传（无 minor 换算）；用 decimal 解析。
	amount, _ := decimal.NewFromString(strings.TrimSpace(amountStr))

	return &CallbackResult{
		OrderNo:  orderNo,
		TradeNo:  tradeNo,
		Amount:   amount,
		Currency: cbCurrency,
		Success:  success,
	}, nil
}

// AckBody：自定义协议无统一确认体，多数处理商只看 2xx 状态码（同旧引擎无 ack 文本）。
func (c *Custom) AckBody() string { return "" }

// QueryOrder：旧平台 CustomChannel **未实现** OrderQuerier（无查询端点配置），仅靠回调入金。
// 因此返回 Found=false（reconcile 跳过，依赖 callback）。
// ⚠ 待真实验证：若某处理商有查询 API，需新增一组查询模板/字段路径配置后再实现，绝不臆造端点。
func (c *Custom) QueryOrder(_ context.Context, _ QueryRequest) (*QueryResult, error) {
	return &QueryResult{Found: false}, nil
}

// ── 私有工具（复刻旧 gateway 包 helpers.go/engine_webhook.go/engine_crypto.go，自带以独立编译）──

// customDefault：空白则回落 fallback（复刻旧 protoStr 的「带默认值读取」语义）。
func customDefault(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// customCanonicalAmount：把金额规整到币种最小单位的小数位（复刻旧 AmountToMinor→AmountFromMinor）。
// USD/CNY/EUR... 2 位；JPY/KRW... 0 位；BHD/KWD... 3 位。
func customCanonicalAmount(amount decimal.Decimal, currency string) string {
	p := customCurrencyDecimals(currency)
	return amount.Round(p).StringFixed(p)
}

// customCurrencyDecimals：币种最小单位的有效小数位（对应旧 CurrencyMinorUnits：1→0、100→2、1000→3）。
func customCurrencyDecimals(currency string) int32 {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "JPY", "KRW", "VND", "IDR", "CLP", "PYG", "RWF", "UGX",
		"VUV", "XAF", "XOF", "XPF", "ISK":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2 // USD, EUR, GBP, CNY, AUD, CAD, HKD, SGD, ...
	}
}

// customRenderTemplate：把模板里的 {{key}} 整串替换为 vars[key]（复刻旧 renderTemplate）。
func customRenderTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// customDotPath：按点路径从嵌套 map 取值（复刻旧 extractDotPath）。
// 如 "data.object.order_no" → data["object"]["order_no"]；数字以最简字符串返回。
func customDotPath(data map[string]interface{}, path string) string {
	var current interface{} = data
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = m[part]
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// customVerifySig：复刻旧 verifyWebhookSig 的签名口径。
// hmac-sha256 → hex 严格比对（hmac.Equal）；hmac-md5/md5 → hex 大小写不敏感比对；
// 其它（含 none/未知）→ 旧实现返回 true（跳过校验），此处保持一致。
func customVerifySig(payload []byte, signature, secret, algo string) bool {
	switch algo {
	case "hmac-sha256":
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(expected), []byte(signature))
	case "hmac-md5", "md5":
		mac := hmac.New(md5.New, []byte(secret))
		mac.Write(payload)
		expected := hex.EncodeToString(mac.Sum(nil))
		return strings.EqualFold(expected, signature)
	default:
		return true // "none" 或未知算法 = 跳过校验（与旧引擎一致）
	}
}

// customHeaderCI：大小写不敏感取 header（复刻旧 getHeaderCI）。
func customHeaderCI(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok {
		return v
	}
	lk := strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == lk {
			return v
		}
	}
	return ""
}

// customFormToMap：URL-encoded 表单 + query 扁平化为 map（复刻旧 parseFormToMap）。
// JSON 解析失败时的回调体兜底解析。
func customFormToMap(rawBody []byte, queryParams map[string]string) map[string]interface{} {
	result := map[string]interface{}{}
	for k, v := range queryParams {
		result[k] = v
	}
	for _, pair := range strings.Split(string(rawBody), "&") {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}
	return result
}
