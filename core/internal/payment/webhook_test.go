package payment

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 合成 fixture：验证通用 webhook 引擎的处理商无关逻辑（HMAC 验签 fail-closed + 成功事件映射
// + 点路径字段提取 + minor→major 金额换算 + 建单模板/默认 body）。上线某真实处理商
// （Airwallex/UseePay/Creem…）前必须接其真实环境抓 raw webhook/响应替换、校正签名口径与字段名。

func whSignSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func whSignMD5(secret string, body []byte) string {
	mac := hmac.New(md5.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyCallback：合法 HMAC-SHA256 签名 → Success=true，正确解析 order/trade/amount/currency。
func TestWebhookVerifyCallback_Valid(t *testing.T) {
	w := &Webhook{
		WebhookSecret: "whsec",
		SuccessEvent:  "payment_intent.succeeded",
		// 默认提取路径即可（type / data.object.metadata.order_no / data.object.amount / data.object.id）
	}
	body := []byte(`{"type":"payment_intent.succeeded","data":{"object":{` +
		`"id":"pi_123","amount":"73.00","currency":"CNY",` +
		`"metadata":{"order_no":"USR1NOabc"}}}}`)
	headers := map[string]string{"X-Signature": whSignSHA256("whsec", body)}

	res, err := w.VerifyCallback(body, headers, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Success || res.OrderNo != "USR1NOabc" || res.TradeNo != "pi_123" {
		t.Fatalf("callback result = %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("73.00")) {
		t.Fatalf("amount = %s, want 73.00", res.Amount)
	}
}

// 资金红线：fail-closed。伪造签名 / body 篡改 / 缺签名头 一律返回 error，绝不入金。
func TestWebhookVerifyCallback_FailClosed(t *testing.T) {
	w := &Webhook{WebhookSecret: "whsec", SuccessEvent: "payment_intent.succeeded"}
	body := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"id":"X","metadata":{"order_no":"O"}}}}`)

	// 伪造签名 → 拒
	if _, err := w.VerifyCallback(body, map[string]string{"X-Signature": "deadbeef"}, nil); err == nil {
		t.Fatal("bogus signature must be rejected (fail-closed)")
	}
	// 用错误密钥签名 → 拒
	if _, err := w.VerifyCallback(body, map[string]string{"X-Signature": whSignSHA256("wrong", body)}, nil); err == nil {
		t.Fatal("wrong-secret signature must be rejected")
	}
	// body 篡改但用旧签名 → 拒
	good := whSignSHA256("whsec", body)
	tampered := []byte(`{"type":"payment_intent.succeeded","data":{"object":{"id":"Y","metadata":{"order_no":"O2"}}}}`)
	if _, err := w.VerifyCallback(tampered, map[string]string{"X-Signature": good}, nil); err == nil {
		t.Fatal("body tamper with stale signature must be rejected")
	}
	// 缺签名头 → 拒
	if _, err := w.VerifyCallback(body, map[string]string{}, nil); err == nil {
		t.Fatal("missing signature header must be rejected")
	}
	// 非法 JSON（验签通过后解析失败）→ 拒
	bad := []byte(`{not json`)
	if _, err := w.VerifyCallback(bad, map[string]string{"X-Signature": whSignSHA256("whsec", bad)}, nil); err == nil {
		t.Fatal("unparseable body must be rejected")
	}
}

// 非成功事件 → Success=false（非错误，只是不入金）。
func TestWebhookVerifyCallback_NonSuccessEvent(t *testing.T) {
	w := &Webhook{WebhookSecret: "whsec", SuccessEvent: "payment_intent.succeeded"}
	body := []byte(`{"type":"payment_intent.created","data":{"object":{"id":"X"}}}`)
	res, err := w.VerifyCallback(body, map[string]string{"X-Signature": whSignSHA256("whsec", body)}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Success {
		t.Fatal("non-success event must map to Success=false")
	}
}

// minor 单位换算 + 自定义提取路径 + timestamp+body 签名口径 + hmac-md5 算法。
func TestWebhookVerifyCallback_MinorAmountAndTimestamp(t *testing.T) {
	w := &Webhook{
		WebhookSecret:    "k",
		SigHeader:        "X-Custom-Sig",
		SigAlgo:          "hmac-md5",
		SigPayloadFormat: "timestamp+body",
		SuccessEvent:     "charge.paid",
		EventTypeExtract: "event",
		OrderExtract:     "order_no",
		AmountExtract:    "amount_minor",
		TradeNoExtract:   "txn_id",
		CurrencyExtract:  "currency",
		AmountUnit:       "minor",
	}
	body := []byte(`{"event":"charge.paid","order_no":"O9","txn_id":"T9","amount_minor":1495,"currency":"USD"}`)
	ts := "1718600000"
	sig := whSignMD5("k", []byte(ts+string(body)))

	res, err := w.VerifyCallback(body, map[string]string{"X-Custom-Sig": sig, "X-Timestamp": ts}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Success || res.OrderNo != "O9" || res.TradeNo != "T9" || res.Currency != "USD" {
		t.Fatalf("result = %+v", res)
	}
	// 1495 minor cents → 14.95 USD（币种感知除数）
	if !res.Amount.Equal(decimal.RequireFromString("14.95")) {
		t.Fatalf("minor amount = %s, want 14.95", res.Amount)
	}

	// JPY 0 小数位：1495 minor → 1495（除数=1）
	wj := &Webhook{WebhookSecret: "k", SigAlgo: "none", SuccessEvent: "charge.paid",
		EventTypeExtract: "event", OrderExtract: "order_no", AmountExtract: "amount_minor",
		CurrencyExtract: "currency", AmountUnit: "minor"}
	jb := []byte(`{"event":"charge.paid","order_no":"OJ","amount_minor":1495,"currency":"JPY"}`)
	rj, err := wj.VerifyCallback(jb, nil, nil)
	if err != nil {
		t.Fatalf("jpy verify: %v", err)
	}
	if !rj.Amount.Equal(decimal.RequireFromString("1495")) {
		t.Fatalf("JPY minor amount = %s, want 1495", rj.Amount)
	}
}

// CreatePayment：默认 body 结构 + 默认提取路径，构造正确上游请求并提取 PayURL/ProviderRef。
func TestWebhookCreatePayment_DefaultBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pi_42","next_action":{"redirect_url":"https://pay.example/checkout/42"}}`))
	}))
	defer srv.Close()

	wh := &Webhook{
		APIBaseURL: srv.URL,
		AuthCreds:  map[string]string{"token": "static_tok"}, // 静态 token，免去 auth 端点
		Client:     srv.Client(),
		Currency:   "USD",
	}
	res, err := wh.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "ORD1", Amount: decimal.RequireFromString("10"), Currency: "USD",
		Description: "topup", ReturnURL: "https://app/return",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.PayURL != "https://pay.example/checkout/42" || res.ProviderRef != "pi_42" {
		t.Fatalf("create result = %+v", res)
	}
	// 上游请求形态：POST /v1/payment_intents + Bearer token
	if gotMethod != http.MethodPost || gotPath != "/v1/payment_intents" || gotAuth != "Bearer static_tok" {
		t.Fatalf("upstream req = %s %s auth=%q", gotMethod, gotPath, gotAuth)
	}
	// 默认 body 携带规整后金额(10.00)、币种、我方单号
	if gotBody["amount"] != "10.00" || gotBody["currency"] != "USD" || gotBody["merchant_order_id"] != "ORD1" {
		t.Fatalf("upstream body = %+v", gotBody)
	}
}

// CreatePayment：body 模板渲染 + 自定义 create_path/method + 自定义 pay_url 提取路径。
func TestWebhookCreatePayment_Template(t *testing.T) {
	var gotBodyRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBodyRaw = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"checkout_url":"https://gw/pay/abc","id":"sess_1"}}`))
	}))
	defer srv.Close()

	wh := &Webhook{
		APIBaseURL:         srv.URL,
		AuthCreds:          map[string]string{"token": "tok"},
		Client:             srv.Client(),
		CreatePath:         "/v2/sessions",
		CreateBodyTemplate: `{"amt":"{{amount}}","cur":"{{currency}}","ref":"{{order_no}}","cb":"{{notify_url}}"}`,
		PayURLExtract:      "data.checkout_url",
		RefExtract:         "data.id",
		Currency:           "USD",
	}
	res, err := wh.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "REF7", Amount: decimal.RequireFromString("25.5"), Currency: "USD",
		NotifyURL: "https://app/notify",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.PayURL != "https://gw/pay/abc" || res.ProviderRef != "sess_1" {
		t.Fatalf("create result = %+v", res)
	}
	// 模板渲染：占位符全部替换，金额规整为 25.50
	want := `{"amt":"25.50","cur":"USD","ref":"REF7","cb":"https://app/notify"}`
	if gotBodyRaw != want {
		t.Fatalf("rendered body = %s, want %s", gotBodyRaw, want)
	}
}

// CreatePayment：响应里无任何支付 URL（schema 不符）→ 报错，绝不返回空跳转 URL。
func TestWebhookCreatePayment_NoPayURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()
	wh := &Webhook{APIBaseURL: srv.URL, AuthCreds: map[string]string{"token": "t"}, Client: srv.Client()}
	if _, err := wh.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "O", Amount: decimal.RequireFromString("1"), Currency: "USD",
	}); err == nil {
		t.Fatal("missing pay URL must error (schema mismatch)")
	}
}

// AckBody：通用 webhook 只看 2xx，无 ack 文本。
func TestWebhookAckBody(t *testing.T) {
	w := &Webhook{}
	if w.AckBody() != "" {
		t.Fatalf("AckBody = %q, want empty", w.AckBody())
	}
	if w.Name() != "webhook" {
		t.Fatalf("Name = %q, want webhook", w.Name())
	}
}

// QueryOrder：旧 WebhookChannel 未实现 OrderQuerier，通用引擎不支持主动查询 → 始终 Found=false。
func TestWebhookQueryOrder_NotSupported(t *testing.T) {
	w := &Webhook{}
	qr, err := w.QueryOrder(context.Background(), QueryRequest{OrderNo: "O", ProviderRef: "R"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qr.Found {
		t.Fatal("generic webhook engine has no query endpoint → Found must be false")
	}
}
