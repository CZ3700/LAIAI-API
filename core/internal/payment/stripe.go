package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Stripe（国际信用卡）：Checkout Session + webhook 验签。
// 直接走 REST API（api.stripe.com），不引 SDK，保持依赖轻量。
type Stripe struct {
	SecretKey     string // sk_live_... / sk_test_...
	WebhookSecret string // whsec_...
	Client        *http.Client
}

func (s *Stripe) Name() string { return "stripe" }

func (s *Stripe) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// CreatePayment：建 Checkout Session（一次性付款），返回 session.url。
// 金额按 currency 的最小货币单位（美元=分）传 Stripe。
func (s *Stripe) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	cur := strings.ToLower(req.Currency)
	if cur == "" {
		cur = "usd"
	}
	minorUnits := req.Amount.Mul(decimal.NewFromInt(100)).Round(0).IntPart() // 美元→分
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", req.ReturnURL)
	form.Set("cancel_url", req.ReturnURL)
	form.Set("client_reference_id", req.OrderNo)
	form.Set("metadata[order_no]", req.OrderNo)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", cur)
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(minorUnits, 10))
	form.Set("line_items[0][price_data][product_data][name]", req.Description)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.SetBasicAuth(s.SecretKey, "")
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripe create session: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("stripe error %d: %s", resp.StatusCode, excerpt(body))
	}
	var session struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, err
	}
	return &CreateResult{PayURL: session.URL, ProviderRef: session.ID}, nil
}

// VerifyCallback：验 Stripe-Signature 头（HMAC-SHA256 over "t.payload"），
// 仅 checkout.session.completed 事件视为成功。
func (s *Stripe) VerifyCallback(rawBody []byte, headers, _ map[string]string) (*CallbackResult, error) {
	sig := headers["Stripe-Signature"]
	if sig == "" {
		sig = headers["stripe-signature"]
	}
	if !s.verifySignature(rawBody, sig) {
		return nil, fmt.Errorf("stripe signature verification failed")
	}
	var event struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				ClientReferenceID string `json:"client_reference_id"`
				PaymentIntent     string `json:"payment_intent"`
				AmountTotal       int64  `json:"amount_total"`
				Currency          string `json:"currency"`
				PaymentStatus     string `json:"payment_status"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return nil, err
	}
	obj := event.Data.Object
	success := event.Type == "checkout.session.completed" && obj.PaymentStatus == "paid"
	return &CallbackResult{
		OrderNo:  obj.ClientReferenceID,
		TradeNo:  obj.PaymentIntent,
		Amount:   decimal.NewFromInt(obj.AmountTotal).Div(decimal.NewFromInt(100)), // 分→元
		Currency: strings.ToUpper(obj.Currency),
		Success:  success,
	}, nil
}

func (s *Stripe) AckBody() string { return "" } // Stripe 只看 2xx 状态码

// verifySignature：Stripe webhook 签名校验（防伪造回调入金）。
// 头格式：t=<ts>,v1=<hex hmac>；签名串 = "<ts>.<rawBody>"，密钥 = WebhookSecret。
func (s *Stripe) verifySignature(payload []byte, header string) bool {
	if s.WebhookSecret == "" || header == "" {
		return false
	}
	var ts, v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			v1 = kv[1]
		}
	}
	if ts == "" || v1 == "" {
		return false
	}
	// 时间戳容差 5 分钟，防重放
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		if d := time.Now().Unix() - n; d > 300 || d < -300 {
			return false
		}
	}
	mac := hmac.New(sha256.New, []byte(s.WebhookSecret))
	mac.Write([]byte(ts + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(v1))
}

func excerpt(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
