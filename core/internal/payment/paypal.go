package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PayPal（国际钱包/信用卡）：Orders API v2，无 SDK 依赖。移植自旧平台
// engine_paypal.go（已在生产实战）。流程与旧引擎逐字对齐：
//
//	OAuth2 client_credentials 拿 Bearer token →
//	POST /v2/checkout/orders（intent=CAPTURE）建单 → 取 links[rel=approve] 跳转 →
//	用户在 PayPal 托管页批准 → webhook（CHECKOUT.ORDER.APPROVED 自动 capture，
//	或 PAYMENT.CAPTURE.COMPLETED 直接到账）。
//
// 🚨 资金红线 —— 与旧引擎的关键差异：
//
//	旧 engine_paypal.go 的 VerifyCallback **完全不验签**，只解析 JSON 后信任
//	resource.status=="COMPLETED" 即入金。这是伪造回调入金漏洞。本移植版按红线
//	FAIL-CLOSED 补上 PayPal 官方 webhook 验签：调
//	POST /v1/notifications/verify-webhook-signature（需 WebhookID + 5 个
//	PAYPAL-* 头）→ 仅当上游回 verification_status=="SUCCESS" 才继续解析；
//	验签失败/缺头/缺 WebhookID/网络错误一律 Success=false（绝不在未验真前入金）。
//
// API 基址（旧引擎的 sandbox 开关原样保留）：
//
//	Sandbox=true  → https://api-m.sandbox.paypal.com
//	Sandbox=false → https://api-m.paypal.com
type PayPal struct {
	ClientID     string // PayPal App Client ID（OAuth basic auth 用户名）
	ClientSecret string // PayPal App Secret（OAuth basic auth 密码）
	WebhookID    string // PayPal 控制台 webhook 配置的 ID（验签必需；空=验签恒失败 FAIL-CLOSED）
	Sandbox      bool   // true 走 sandbox 基址，false 走生产基址（与旧引擎一致）
	Currency     string // 默认币种（callback/capture 未带币种时回退用），空回退 USD
	APIBase      string // 覆盖基址（测试注入 httptest）；非空时优先于 Sandbox 开关
	Client       *http.Client
}

func (p *PayPal) Name() string { return "paypal" }

// baseURL：sandbox/live 切换（与旧 engine_paypal.go baseURL() 一致）。
// APIBase 非空（测试注入）时优先返回，绕开真实 PayPal。
func (p *PayPal) baseURL() string {
	if p.APIBase != "" {
		return strings.TrimRight(p.APIBase, "/")
	}
	if p.Sandbox {
		return "https://api-m.sandbox.paypal.com"
	}
	return "https://api-m.paypal.com"
}

func (p *PayPal) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (p *PayPal) defaultCurrency() string {
	if c := strings.ToUpper(strings.TrimSpace(p.Currency)); c != "" {
		return c
	}
	return "USD"
}

// getAccessToken：OAuth2 client_credentials 换 Bearer token。
// POST /v1/oauth2/token，Basic(client_id:client_secret)，
// body=grant_type=client_credentials（与旧引擎逐字一致）。
func (p *PayPal) getAccessToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL()+"/v1/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+
		base64.StdEncoding.EncodeToString([]byte(p.ClientID+":"+p.ClientSecret)))

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(body, &result)
	if result.AccessToken == "" {
		return "", fmt.Errorf("paypal auth failed: %s", result.Error)
	}
	return result.AccessToken, nil
}

// CreatePayment：建 Orders v2 订单（intent=CAPTURE），返回 links[rel=approve] 跳转 URL。
// 金额按 currency 的原生主单位字符串传给 PayPal（USD="10.00"、JPY="100"）；
// 经 amountToMinor/amountFromMinor 规整小数位（与旧引擎一致）。
func (p *PayPal) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("paypal auth: %w", err)
	}

	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = p.defaultCurrency()
	}
	// 规整到该币种正确的小数位（USD 2 位、JPY 0 位）。负数/零金额直接拒绝。
	minor, err := amountToMinor(req.Amount, currency)
	if err != nil || minor <= 0 {
		return nil, fmt.Errorf("paypal: invalid amount %q for %s: %v", req.Amount.String(), currency, err)
	}
	amount := amountFromMinor(minor, currency)

	order := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{
			{
				"reference_id": req.OrderNo,
				"description":  req.Description,
				"amount": map[string]any{
					"currency_code": currency,
					"value":         amount,
				},
				"custom_id": req.OrderNo,
			},
		},
		"application_context": map[string]any{
			"return_url": req.ReturnURL + "?status=success&order_no=" + req.OrderNo,
			"cancel_url": req.ReturnURL + "?status=cancel",
		},
	}

	payload, _ := json.Marshal(order)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL()+"/v2/checkout/orders", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("paypal create order: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("paypal create order %d: %s", resp.StatusCode, excerpt(body))
	}

	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Links  []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("paypal create order decode: %w", err)
	}

	var approveURL string
	for _, link := range result.Links {
		if link.Rel == "approve" {
			approveURL = link.Href
			break
		}
	}
	if approveURL == "" {
		return nil, fmt.Errorf("paypal: no approve URL (status=%s, body=%s)", result.Status, excerpt(body))
	}

	return &CreateResult{PayURL: approveURL, ProviderRef: result.ID}, nil
}

// ppWebhookEvent：webhook envelope 的统一解析形态（CHECKOUT.ORDER.* 与
// PAYMENT.CAPTURE.* 字段层级不同，这里取并集）。
type ppWebhookEvent struct {
	EventType string `json:"event_type"`
	Resource  struct {
		ID            string  `json:"id"`
		Status        string  `json:"status"`
		CustomID      string  `json:"custom_id"` // PAYMENT.CAPTURE.COMPLETED 把 custom_id 提到顶层
		Amount        ppMoney `json:"amount"`    // PAYMENT.CAPTURE.COMPLETED 的实付金额
		PurchaseUnits []struct {
			ReferenceID string  `json:"reference_id"`
			CustomID    string  `json:"custom_id"`
			Amount      ppMoney `json:"amount"`
		} `json:"purchase_units"`
	} `json:"resource"`
}

type ppMoney struct {
	Value        string `json:"value"`
	CurrencyCode string `json:"currency_code"`
}

// VerifyCallback：FAIL-CLOSED。先调 PayPal 官方 verify-webhook-signature API
// 验真（防伪造回调入金），通过后再解析事件。
//
// 验签需 5 个入站 PAYPAL-* 头 + 配置的 WebhookID + 原始 body：
//
//	POST /v1/notifications/verify-webhook-signature
//	{ auth_algo, cert_url, transmission_id, transmission_sig, transmission_time,
//	  webhook_id, webhook_event:<原始事件 JSON> }
//	仅当响应 verification_status=="SUCCESS" 才放行（任何其它值/错误 → 验签失败）。
//
// Engine.VerifyCallback 无 context 形参（与 stripe/epay 一致），而 PayPal 验签
// 是一次内部基础设施 HTTP 调用——这里用带 15s 超时的 background context。
//
// 成功态映射（与旧引擎一致）：
//
//	CHECKOUT.ORDER.APPROVED   → 自动 capture（POST .../capture），按 capture 结果入金
//	PAYMENT.CAPTURE.COMPLETED → 直接据事件入金（resource.status=="COMPLETED"）
//	其它事件 → Success=false（非成功通知，不入金）
func (p *PayPal) VerifyCallback(rawBody []byte, headers, _ map[string]string) (*CallbackResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := p.verifyWebhookSignature(ctx, rawBody, headers); err != nil {
		return nil, err // FAIL-CLOSED：验签未过绝不入金（资金红线）
	}

	var event ppWebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return nil, fmt.Errorf("paypal parse webhook: %w", err)
	}

	switch event.EventType {
	case "CHECKOUT.ORDER.APPROVED":
		// 订单已批准但尚未扣款 → 主动 capture（与旧引擎一致）。
		return p.captureOrder(ctx, event.Resource.ID)

	case "PAYMENT.CAPTURE.COMPLETED":
		orderNo := event.Resource.CustomID
		amount := event.Resource.Amount.Value
		currency := strings.ToUpper(event.Resource.Amount.CurrencyCode)
		if len(event.Resource.PurchaseUnits) > 0 {
			pu := event.Resource.PurchaseUnits[0]
			if orderNo == "" {
				orderNo = pu.CustomID
				if orderNo == "" {
					orderNo = pu.ReferenceID
				}
			}
			if amount == "" {
				amount = pu.Amount.Value
				currency = strings.ToUpper(pu.Amount.CurrencyCode)
			}
		}
		if currency == "" {
			currency = p.defaultCurrency()
		}
		amt, _ := decimal.NewFromString(amount)
		return &CallbackResult{
			OrderNo:  orderNo,
			TradeNo:  event.Resource.ID,
			Amount:   amt,
			Currency: currency,
			Success:  event.Resource.Status == "COMPLETED",
		}, nil

	default:
		return &CallbackResult{Success: false}, nil
	}
}

// captureOrder：POST /v2/checkout/orders/{id}/capture，解析 capture 结果。
// 取 purchase_units[0].payments.captures[0] 的金额/币种/状态与交易号（与旧引擎一致）。
func (p *PayPal) captureOrder(ctx context.Context, orderID string) (*CallbackResult, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL()+"/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture",
		strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("paypal capture: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var result struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		PurchaseUnits []struct {
			ReferenceID string `json:"reference_id"`
			CustomID    string `json:"custom_id"`
			Payments    struct {
				Captures []struct {
					ID     string  `json:"id"`
					Amount ppMoney `json:"amount"`
					Status string  `json:"status"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	_ = json.Unmarshal(body, &result)

	var orderNo, amount, currency, tradeNo string
	if len(result.PurchaseUnits) > 0 {
		pu := result.PurchaseUnits[0]
		orderNo = pu.CustomID
		if orderNo == "" {
			orderNo = pu.ReferenceID
		}
		if len(pu.Payments.Captures) > 0 {
			cap0 := pu.Payments.Captures[0]
			tradeNo = cap0.ID
			amount = cap0.Amount.Value
			currency = strings.ToUpper(cap0.Amount.CurrencyCode)
		}
	}
	if currency == "" {
		currency = p.defaultCurrency()
	}
	amt, _ := decimal.NewFromString(amount)

	return &CallbackResult{
		OrderNo:  orderNo,
		TradeNo:  tradeNo,
		Amount:   amt,
		Currency: currency,
		Success:  result.Status == "COMPLETED",
	}, nil
}

func (p *PayPal) AckBody() string { return "" } // PayPal 只看 2xx 状态码（同 Stripe）

// QueryOrder：GET /v2/checkout/orders/{id}，按 status 判已支付（reconcile 兜底 webhook 漏发）。
// ProviderRef = CreatePayment 返回的 order id。404 → Found=false；
// status=="COMPLETED" → 已支付（取 capture 金额/交易号），其余视为未付。
//
// ⚠ 待真实验证：旧引擎只有 capture 端点、无 GET 查单实现，此对账腿系本移植按
// PayPal Orders v2 公开文档「Show order details」补；上线动真金前需接真实账号抓
// raw response 核对字段层级（intent=CAPTURE 已 capture 的订单是否携 payments.captures、
// status 枚举 COMPLETED/APPROVED/VOIDED）后再启用。
func (p *PayPal) QueryOrder(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.ProviderRef == "" {
		return &QueryResult{Found: false}, nil // 无 order id 无从查
	}
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("paypal query auth: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.baseURL()+"/v2/checkout/orders/"+url.PathEscape(req.ProviderRef), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("paypal query order: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &QueryResult{Found: false}, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("paypal query http %d: %s", resp.StatusCode, excerpt(body))
	}

	var result struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		PurchaseUnits []struct {
			ReferenceID string  `json:"reference_id"`
			CustomID    string  `json:"custom_id"`
			Amount      ppMoney `json:"amount"`
			Payments    struct {
				Captures []struct {
					ID     string  `json:"id"`
					Amount ppMoney `json:"amount"`
					Status string  `json:"status"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("paypal query decode: %w", err)
	}

	var tradeNo, amount, currency string
	captured := false // 仅凭「已 COMPLETED 的 capture」判已支付，不信订单级 status（对抗评审 M-1）
	if len(result.PurchaseUnits) > 0 {
		pu := result.PurchaseUnits[0]
		amount = pu.Amount.Value
		currency = strings.ToUpper(pu.Amount.CurrencyCode)
		if len(pu.Payments.Captures) > 0 {
			cap0 := pu.Payments.Captures[0]
			tradeNo = cap0.ID
			amount = cap0.Amount.Value // capture 实付优先于应付
			currency = strings.ToUpper(cap0.Amount.CurrencyCode)
			captured = cap0.Status == "COMPLETED"
		}
	}
	if currency == "" {
		currency = p.defaultCurrency()
	}
	amt, _ := decimal.NewFromString(amount)

	// Paid 仅当确有 COMPLETED capture（钱真到账）才为 true——APPROVED/未扣款订单 reconcile 不入金。
	// ⚠ 仍属 doc-derived 对账腿，上线动真金前须接真实 sandbox 抓 raw 核对 capture 字段层级。
	return &QueryResult{
		Found:    true,
		Paid:     captured,
		TradeNo:  tradeNo,
		Amount:   amt,
		Currency: currency,
	}, nil
}

// verifyWebhookSignature：调 PayPal 官方 verify-webhook-signature API（防伪造回调入金）。
// FAIL-CLOSED：缺 WebhookID / 缺任一 PAYPAL-* 头 / 网络错误 / 非 2xx /
// verification_status!="SUCCESS" 一律返回 error（上层据此 Success=false 拒收）。
//
// 入站头（PayPal webhook 投递时随附，大小写不敏感容错）：
//
//	PAYPAL-AUTH-ALGO / PAYPAL-CERT-URL / PAYPAL-TRANSMISSION-ID /
//	PAYPAL-TRANSMISSION-SIG / PAYPAL-TRANSMISSION-TIME
func (p *PayPal) verifyWebhookSignature(ctx context.Context, rawBody []byte, headers map[string]string) error {
	if p.WebhookID == "" {
		return fmt.Errorf("paypal: webhook_id not configured (fail-closed)")
	}
	authAlgo := headerGet(headers, "PAYPAL-AUTH-ALGO")
	certURL := headerGet(headers, "PAYPAL-CERT-URL")
	transmissionID := headerGet(headers, "PAYPAL-TRANSMISSION-ID")
	transmissionSig := headerGet(headers, "PAYPAL-TRANSMISSION-SIG")
	transmissionTime := headerGet(headers, "PAYPAL-TRANSMISSION-TIME")
	if authAlgo == "" || certURL == "" || transmissionID == "" || transmissionSig == "" || transmissionTime == "" {
		return fmt.Errorf("paypal: missing webhook signature headers (fail-closed)")
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("paypal verify-webhook auth: %w", err)
	}

	// webhook_event 必须是收到的**原始**事件对象（用 RawMessage 原样回放，
	// 避免重新 Marshal 改变字节导致验签失败）。
	verifyReq := map[string]any{
		"auth_algo":         authAlgo,
		"cert_url":          certURL,
		"transmission_id":   transmissionID,
		"transmission_sig":  transmissionSig,
		"transmission_time": transmissionTime,
		"webhook_id":        p.WebhookID,
		"webhook_event":     json.RawMessage(rawBody),
	}
	payload, err := json.Marshal(verifyReq)
	if err != nil {
		return fmt.Errorf("paypal verify-webhook marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL()+"/v1/notifications/verify-webhook-signature", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("paypal verify-webhook request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("paypal verify-webhook http %d: %s", resp.StatusCode, excerpt(body))
	}
	var vr struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(body, &vr); err != nil {
		return fmt.Errorf("paypal verify-webhook decode: %w", err)
	}
	if vr.VerificationStatus != "SUCCESS" {
		return fmt.Errorf("paypal webhook signature verification failed: %s", vr.VerificationStatus)
	}
	return nil
}

// headerGet：大小写不敏感取 header（PayPal 投递头多为大写连字符，net/http
// 会规范化为 Canonical-Form，但回调透传的 map 来源不一，这里做容错）。
func headerGet(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok && v != "" {
		return v
	}
	if v, ok := headers[http.CanonicalHeaderKey(key)]; ok && v != "" {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == lower && v != "" {
			return v
		}
	}
	return ""
}

// amountToMinor / amountFromMinor：移植自旧平台 helpers.go 的多币种最小单位换算。
// PayPal 接受币种原生主单位的小数字符串（USD="10.00"、JPY="100"），这里据
// 币种小数位规整。本地化版本（不引旧包），只覆盖 PayPal 支持币种的小数位规则。
func currencyMinorUnits(code string) int64 {
	// ⚠ 与旧平台 helpers.go CurrencyMinorUnits 逐字对齐（battle-tested 真值，含 minor_units_test 钉死 TWD=100）。
	// 绝不在金额路径上凭文档臆造重分类（红线，见 epay 方言事故）：PayPal 文档称 HUF/TWD 为整数币种，
	// 但未经真实账号验真，故保持与真值一致（TWD/HUF 走默认 100=2 位）。若 PayPal 真要求整数 TWD/HUF，
	// 它会在 create 时报错（fail-safe，不会误扣），届时接真实 sandbox 验真后再改此表。
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "JPY", "KRW", "VND", "IDR", "CLP", "PYG", "RWF", "UGX",
		"VUV", "XAF", "XOF", "XPF", "ISK":
		return 1
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 1000
	default:
		return 100 // USD, EUR, GBP, CNY, AUD, CAD, HKD, SGD, TWD, HUF, ...
	}
}

func amountToMinor(amount decimal.Decimal, currency string) (int64, error) {
	units := currencyMinorUnits(currency)
	scaled := amount.Mul(decimal.NewFromInt(units)).Round(0)
	if scaled.Sign() < 0 {
		return 0, fmt.Errorf("negative amount")
	}
	return scaled.IntPart(), nil
}

func amountFromMinor(minor int64, currency string) string {
	units := currencyMinorUnits(currency)
	d := decimal.NewFromInt(minor).Div(decimal.NewFromInt(units))
	switch units {
	case 1:
		return d.StringFixed(0)
	case 1000:
		return d.StringFixed(3)
	default:
		return d.StringFixed(2)
	}
}
