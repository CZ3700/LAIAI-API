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
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Webhook：通用「REST 建单 + Webhook 回调」协议引擎（移植旧平台 engine_webhook.go）。
//
// 一个万能适配器，覆盖任何遵循「调 REST API 建单 + 收 Webhook 回调」模式的处理商：
// Airwallex / UseePay / Creem / Adyen / Paddle / LemonSqueezy / Razorpay /
// MercadoPago / EBANX / Flutterwave / Paystack / Xendit 等。
//
// 与同包 Custom（engine_custom.go 的移植）的区别：Webhook 走「鉴权换 token + 固定 create 端点 +
// 事件类型枚举」三段式（getAuthToken：静态 token 或 auth_endpoint 换取；按 SuccessEvent 判成功；
// 支持 minor 单位金额）。本文件唯一定义并导出二者共用的工具：nonEmpty / extractDotPath /
// renderTemplate / verifyWebhookSig / getHeaderCI / canonicalAmount（custom.go 复用）。
//
// 🚨 红线（同 crypto/stripe/custom 纪律，默认禁用、验真后才 enable）：
//   - VerifyCallback **fail-closed**：缺签名头/签名不符/解析失败一律返回 error，
//     绝不在未验签数据上入金（资金红线）。
//   - HMAC 验签口径按各处理商不同（签名串 = body 或 timestamp+body；算法 hmac-sha256/hmac-md5），
//     上线某处理商前**必须接其真实环境抓 raw webhook/响应写 fixture 验真**，确认：签名口径、
//     create 响应里支付 URL 字段、webhook 里 order_no/实付金额/成功事件枚举。绝不凭文档臆造。
//   - 入金/订单状态机在 server 层（幂等 + 实付不得少于应付 + 入金），本引擎只验签与解析。
type Webhook struct {
	// ── 鉴权（旧 config，AES 加密存）──
	APIBaseURL   string            // 处理商 API 基址，如 https://api.airwallex.com/api/v1
	AuthType     string            // "bearer" / "basic" / "header"（默认 bearer）
	AuthCreds    map[string]string // 鉴权凭证，键名随处理商而异（如 {"client_id":..,"api_key":..} 或 {"token":..}）
	AuthEndpoint string            // 可选：换取 Bearer token 的路径（如 /authentication/login）

	// ── 验签 + 字段提取（旧 protocol_config，明文模板）──
	WebhookSecret    string // 验签密钥
	SigHeader        string // 签名所在 header 名（默认 x-signature）
	SigAlgo          string // "hmac-sha256" / "hmac-md5" / "none"（默认 hmac-sha256）
	SigPayloadFormat string // "body" / "timestamp+body"（默认 body）

	CreateMethod       string // 建单 HTTP 方法（默认 POST）
	CreatePath         string // 建单端点（默认 /v1/payment_intents）
	CreateBodyTemplate string // 建单 body 模板（含 {{amount}} {{currency}} {{order_no}} 等占位符）；空则用默认结构
	PayURLExtract      string // 从建单响应提取支付 URL 的点路径（默认 next_action.redirect_url）
	RefExtract         string // 从建单响应提取上游引用的点路径（默认 id）

	SuccessEvent     string // 成功事件名（默认 payment_intent.succeeded）
	EventTypeExtract string // 提取事件类型的点路径（默认 type；fallback name/event_type）
	OrderExtract     string // 提取我方单号的点路径（默认 data.object.metadata.order_no）
	AmountExtract    string // 提取金额的点路径（默认 data.object.amount）
	TradeNoExtract   string // 提取上游交易号的点路径（默认 data.object.id）
	CurrencyExtract  string // 提取币种的点路径（空则用 Currency）

	// ── 金额/币种 ──
	Currency   string // 渠道默认币种（回调未带币种时的兜底，默认 CNY）
	AmountUnit string // "major"（元/美元）/ "minor"（分/cents）；默认 major

	Client *http.Client
}

func (w *Webhook) Name() string { return "webhook" }

func (w *Webhook) apiBase() string { return strings.TrimRight(w.APIBaseURL, "/") }

func (w *Webhook) httpClient() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// getAuthToken：经 auth_endpoint 换取 Bearer token；若 AuthCreds 含静态 "token" 则直接用。
func (w *Webhook) getAuthToken(ctx context.Context) (string, error) {
	if tok := w.AuthCreds["token"]; tok != "" {
		return tok, nil
	}
	if w.AuthEndpoint == "" {
		return "", fmt.Errorf("no auth_endpoint configured and no static token")
	}

	payload, _ := json.Marshal(w.AuthCreds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.apiBase()+w.AuthEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// 部分处理商需把凭证放 header 而非 body
	if w.AuthType == "header" {
		for k, v := range w.AuthCreds {
			req.Header.Set(k, v)
		}
	}

	resp, err := w.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tokenData map[string]interface{}
	_ = json.Unmarshal(body, &tokenData)
	for _, key := range []string{"token", "access_token", "data.token", "bearer_token"} {
		if v := extractDotPath(tokenData, key); v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("auth failed: no token in response: %s", excerpt(body))
}

// CreatePayment：建单。先取 token，再渲染 body 模板（或默认结构），POST 建单端点，
// 从响应点路径提取支付 URL 与上游引用。⚠ 请求/响应字段按处理商验真。
//
// 金额：CreateRequest.Amount 是 Currency 单位的 decimal。canonicalAmount 按币种有效小数位规整
// （沿用旧 AmountToMinor→AmountFromMinor 往返语义，防 JPY 被当 100.00、BHD 截断）。
func (w *Webhook) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	token, err := w.getAuthToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook auth: %w", err)
	}

	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = strings.ToUpper(nonEmpty(w.Currency, "CNY"))
	}
	amount := canonicalAmount(req.Amount, currency)

	var bodyPayload []byte
	if tmpl := w.CreateBodyTemplate; tmpl != "" {
		rendered := renderTemplate(tmpl, map[string]string{
			"amount":     amount,
			"currency":   currency,
			"order_no":   req.OrderNo,
			"notify_url": req.NotifyURL,
			"return_url": req.ReturnURL,
			"desc":       req.Description,
			"client_ip":  req.ClientIP,
		})
		bodyPayload = []byte(rendered)
	} else {
		// 默认 body 结构（多数处理商可用）
		payload := map[string]interface{}{
			"amount":            amount,
			"currency":          currency,
			"merchant_order_id": req.OrderNo,
			"description":       req.Description,
			"request_id":        req.OrderNo,
			"return_url":        req.ReturnURL,
			"metadata": map[string]string{
				"order_no": req.OrderNo,
			},
		}
		bodyPayload, _ = json.Marshal(payload)
	}

	createPath := nonEmpty(w.CreatePath, "/v1/payment_intents")
	createMethod := nonEmpty(w.CreateMethod, http.MethodPost)

	httpReq, err := http.NewRequestWithContext(ctx, createMethod, w.apiBase()+createPath, bytes.NewReader(bodyPayload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := w.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("create payment request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("webhook create HTTP %d: %s", resp.StatusCode, excerpt(respBody))
	}

	var respData map[string]interface{}
	_ = json.Unmarshal(respBody, &respData)

	payURLPath := nonEmpty(w.PayURLExtract, "next_action.redirect_url")
	payURL := extractDotPath(respData, payURLPath)
	if payURL == "" {
		// 同旧引擎：再试常见别名
		for _, alt := range []string{"url", "data.url", "checkout_url", "payment_url", "redirect_url", "data.next_action.redirect_url"} {
			if v := extractDotPath(respData, alt); v != "" {
				payURL = v
				break
			}
		}
	}
	if payURL == "" {
		return nil, fmt.Errorf("no pay URL found in response (tried %s): %s", payURLPath, excerpt(respBody))
	}

	providerRef := extractDotPath(respData, nonEmpty(w.RefExtract, "id"))
	return &CreateResult{PayURL: payURL, ProviderRef: providerRef}, nil
}

// VerifyCallback：**fail-closed** 验签 + 解析。
//  1. 验签（WebhookSecret 非空且算法非 none 时强制）：缺签名头/不符 → error；
//  2. 解析事件，事件类型 != SuccessEvent → Success=false（非成功通知，不报错）；
//  3. 提取 order_no/amount/trade_no/currency；minor 单位按币种除数换算回 major。
//
// 红线：任一验签/解析失败返回 error（caller 视为拒绝），绝不在未验签数据上返回 Success=true。
func (w *Webhook) VerifyCallback(rawBody []byte, headers, _ map[string]string) (*CallbackResult, error) {
	sigAlgo := strings.ToLower(nonEmpty(w.SigAlgo, "hmac-sha256"))

	// 1. 验签（fail-closed）
	if w.WebhookSecret != "" && sigAlgo != "none" {
		sigHeader := nonEmpty(w.SigHeader, "x-signature")
		sig := getHeaderCI(headers, sigHeader)
		if sig == "" {
			return nil, fmt.Errorf("missing webhook signature header: %s", sigHeader)
		}

		var signedPayload []byte
		switch nonEmpty(w.SigPayloadFormat, "body") {
		case "timestamp+body":
			ts := getHeaderCI(headers, "x-timestamp")
			if ts == "" {
				ts = getHeaderCI(headers, "t")
			}
			signedPayload = []byte(ts + string(rawBody))
		default:
			signedPayload = rawBody
		}

		if !verifyWebhookSig(signedPayload, sig, w.WebhookSecret, sigAlgo) {
			return nil, fmt.Errorf("invalid webhook signature")
		}
	}

	// 2. 解析事件
	var eventData map[string]interface{}
	if err := json.Unmarshal(rawBody, &eventData); err != nil {
		return nil, fmt.Errorf("parse webhook body: %w", err)
	}

	successEvent := nonEmpty(w.SuccessEvent, "payment_intent.succeeded")
	eventType := extractDotPath(eventData, nonEmpty(w.EventTypeExtract, "type"))
	if eventType == "" {
		eventType = extractDotPath(eventData, "name") // Airwallex 用 name
	}
	if eventType == "" {
		eventType = extractDotPath(eventData, "event_type")
	}
	if eventType != successEvent {
		return &CallbackResult{Success: false}, nil
	}

	// 3. 提取字段
	orderNo := extractDotPath(eventData, nonEmpty(w.OrderExtract, "data.object.metadata.order_no"))
	if orderNo == "" {
		orderNo = extractDotPath(eventData, "data.object.merchant_order_id")
	}
	amountStr := extractDotPath(eventData, nonEmpty(w.AmountExtract, "data.object.amount"))
	tradeNo := extractDotPath(eventData, nonEmpty(w.TradeNoExtract, "data.object.id"))

	// 币种：先看回调带的（CurrencyExtract），否则用渠道默认。决定 minor→major 的除数。
	cbCurrency := strings.ToUpper(nonEmpty(w.Currency, "CNY"))
	if w.CurrencyExtract != "" {
		if v := extractDotPath(eventData, w.CurrencyExtract); v != "" {
			cbCurrency = strings.ToUpper(v)
		}
	}

	amount := decimal.Zero
	if amountStr != "" {
		amount = parseWebhookAmount(amountStr, cbCurrency, w.AmountUnit)
	}

	return &CallbackResult{
		OrderNo:  orderNo,
		TradeNo:  tradeNo,
		Amount:   amount,
		Currency: cbCurrency,
		Success:  true,
	}, nil
}

// AckBody：通用 webhook 处理商多数只看 2xx 状态码，无需回特定 body（同旧引擎无 ack 文本）。
func (w *Webhook) AckBody() string { return "" }

// QueryOrder：旧 WebhookChannel **未实现** OrderQuerier（无 query 端点配置），
// 因此通用 webhook 引擎不支持主动对账，依赖 callback。返回 Found=false（reconcile 跳过）。
// ⚠ 待真实验证：如某处理商有查询 API，需新增 QueryPath + 响应字段映射后再实现，绝不臆造端点。
func (w *Webhook) QueryOrder(_ context.Context, _ QueryRequest) (*QueryResult, error) {
	return &QueryResult{Found: false}, nil
}

// parseWebhookAmount：回调金额转 decimal。minor 单位（分/cents）按币种小数位除数还原为 major。
// 复刻旧 engine_webhook.go：取整数部分 → AmountFromMinor（除以 10^decimals）；
// 非整数则当作已是 major 单位 fallback（兼容忽略 amount_unit 语义的模板）。
func parseWebhookAmount(amountStr, currency, amountUnit string) decimal.Decimal {
	if amountUnit == "minor" {
		intPart := strings.SplitN(amountStr, ".", 2)[0]
		if minor, err := strconv.ParseInt(intPart, 10, 64); err == nil {
			divisor := decimal.New(1, currencyDecimalPlaces(currency)) // 10^decimals
			return decimal.NewFromInt(minor).Div(divisor)
		}
	}
	d, _ := decimal.NewFromString(strings.TrimSpace(amountStr))
	return d
}

// ── 同包共享 helper（移植旧 gateway 包；本文件唯一定义，custom.go 复用）──

// nonEmpty：v 非空白则取 v，否则取 fallback（移植旧 protoStr「带默认值读取」语义）。
func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// canonicalAmount：按币种有效小数位规整金额（沿用旧 AmountToMinor→AmountFromMinor 往返）。
// USD/CNY=2 位、JPY/KRW=0 位、BHD/KWD=3 位。
func canonicalAmount(amount decimal.Decimal, currency string) string {
	places := currencyDecimalPlaces(currency)
	return amount.Round(places).StringFixed(places)
}

// currencyDecimalPlaces：币种最小单位的小数位（移植旧 CurrencyMinorUnits：100→2、1→0、1000→3）。
func currencyDecimalPlaces(code string) int32 {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "JPY", "KRW", "VND", "IDR", "CLP", "PYG", "RWF", "UGX",
		"VUV", "XAF", "XOF", "XPF", "ISK":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2 // USD, EUR, GBP, CNY, ...
	}
}

// extractDotPath：用点路径从嵌套 map 取值（移植旧实现，逐字对齐）。
// 如 "data.object.metadata.order_no" → data["object"]["metadata"]["order_no"]。
func extractDotPath(data map[string]interface{}, path string) string {
	parts := strings.Split(path, ".")
	var current interface{} = data
	for _, part := range parts {
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

// renderTemplate：替换模板里的 {{key}} 占位符（移植旧实现）。
func renderTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// verifyWebhookSig：验 webhook 签名（移植旧实现，逐字对齐算法口径）。
//   - hmac-sha256：HMAC-SHA256(payload, secret) → hex，常数时间比较；
//   - hmac-md5 / md5：HMAC-MD5 → hex，大小写不敏感比较；
//   - 其他（含 none）：跳过验证（返回 true）——caller 仅在 SigAlgo!="none" 时调用此函数。
func verifyWebhookSig(payload []byte, signature, secret, algo string) bool {
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
		return true // "none" 或未知 = 跳过验证
	}
}

// getHeaderCI：大小写不敏感取 header（移植旧实现）。
func getHeaderCI(headers map[string]string, key string) string {
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
