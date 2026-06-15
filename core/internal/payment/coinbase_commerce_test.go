package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 注意：下面的 webhook 负载是**合成** fixture（按官方文档/SDK 推导的字段层级），用于验证
// 验签算法 + 成功态映射 + 金额/单号解析「逻辑」。上线前必须用接真实 Coinbase 账号抓的 raw
// envelope 替换（确认 event.data 嵌套、pricing.local、metadata.order_no、timeline 枚举），
// 见 coinbase_commerce.go 红线（绝不凭文档臆造定稿——epay 方言教训）。

func ccSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCoinbaseVerifyCallback(t *testing.T) {
	c := &CoinbaseCommerce{WebhookSecret: "shh"}
	// 实收金额取 payments[]（非 pricing.local 应付）。此处实收==应付==100。
	body := []byte(`{"event":{"type":"charge:confirmed","data":{` +
		`"code":"ABC123","metadata":{"order_no":"USR1NOdeadbeef"},` +
		`"pricing":{"local":{"amount":"100.00","currency":"USD"}},` +
		`"payments":[{"value":{"local":{"amount":"100.00","currency":"USD"}}}]}}}`)
	sig := ccSign("shh", body)

	res, err := c.VerifyCallback(body, map[string]string{"X-Cc-Webhook-Signature": sig}, nil)
	if err != nil {
		t.Fatalf("verify confirmed: %v", err)
	}
	if !res.Success {
		t.Fatal("charge:confirmed must map to Success=true")
	}
	if res.OrderNo != "USR1NOdeadbeef" || res.TradeNo != "ABC123" {
		t.Fatalf("parsed order/trade wrong: %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("100.00")) || res.Currency != "USD" {
		t.Fatalf("parsed amount/currency wrong: %+v", res)
	}
}

// 资金红线（对抗评审 critical）：underpaid 的 resolved——金额必须返回 payments[] 实收（5）而非
// pricing.local 应付（100），否则上层 creditPaidOrder 的「实付不得少于应付」退化为恒等式被绕过。
func TestCoinbaseUnderpaidReturnsActualPaid(t *testing.T) {
	c := &CoinbaseCommerce{WebhookSecret: "shh"}
	body := []byte(`{"event":{"type":"charge:resolved","data":{` +
		`"code":"UP1","metadata":{"order_no":"O1"},` +
		`"pricing":{"local":{"amount":"100.00","currency":"USD"}},` +
		`"payments":[{"value":{"local":{"amount":"5.00","currency":"USD"}}}]}}}`)
	res, err := c.VerifyCallback(body, map[string]string{"X-Cc-Webhook-Signature": ccSign("shh", body)}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Amount.Equal(decimal.RequireFromString("5.00")) {
		t.Fatalf("underpaid amount = %s, want 5.00 (实收 payments[]，非应付 pricing.local 100)", res.Amount)
	}
}

func TestCoinbaseTamperedSignatureRejected(t *testing.T) {
	c := &CoinbaseCommerce{WebhookSecret: "shh"}
	body := []byte(`{"event":{"type":"charge:confirmed","data":{"code":"X"}}}`)
	// 用错误密钥签名（伪造回调）→ 必须拒绝（防伪造入金）
	bad := ccSign("wrong-secret", body)
	if _, err := c.VerifyCallback(body, map[string]string{"X-Cc-Webhook-Signature": bad}, nil); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
	// 篡改 body 但用旧签名 → 必须拒绝
	good := ccSign("shh", body)
	tampered := []byte(`{"event":{"type":"charge:confirmed","data":{"code":"Y"}}}`)
	if _, err := c.VerifyCallback(tampered, map[string]string{"X-Cc-Webhook-Signature": good}, nil); err == nil {
		t.Fatal("body tamper with stale signature must be rejected")
	}
}

// QueryOrder（reconcile 兜底腿）：timeline 含 COMPLETED→Paid=true（金额取 payments[] 实收）、
// 只含 PENDING→未付、404→Found=false、空 ref→Found=false。⚠ 合成 fixture，上线前换真实 raw。
func TestCoinbaseQueryOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/charges/PAID"):
			w.Write([]byte(`{"data":{"code":"PAID","pricing":{"local":{"amount":"50.00","currency":"USD"}},"payments":[{"value":{"local":{"amount":"50.00","currency":"USD"}}}],"timeline":[{"status":"NEW"},{"status":"COMPLETED"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/charges/UNPAID"):
			w.Write([]byte(`{"data":{"code":"UNPAID","pricing":{"local":{"amount":"50.00","currency":"USD"}},"timeline":[{"status":"NEW"},{"status":"PENDING"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &CoinbaseCommerce{APIKey: "k", APIBase: srv.URL}

	if qr, err := c.QueryOrder(context.Background(), QueryRequest{ProviderRef: "PAID"}); err != nil ||
		!qr.Found || !qr.Paid || !qr.Amount.Equal(decimal.RequireFromString("50.00")) {
		t.Fatalf("PAID query = %+v err=%v", qr, err)
	}
	if qr, _ := c.QueryOrder(context.Background(), QueryRequest{ProviderRef: "UNPAID"}); !qr.Found || qr.Paid {
		t.Fatalf("UNPAID query = %+v, want Found && !Paid", qr)
	}
	if qr, _ := c.QueryOrder(context.Background(), QueryRequest{ProviderRef: "NOPE"}); qr.Found {
		t.Fatalf("404 must be Found=false: %+v", qr)
	}
	if qr, _ := c.QueryOrder(context.Background(), QueryRequest{ProviderRef: ""}); qr.Found {
		t.Fatal("empty ProviderRef must be Found=false")
	}
}

func TestCoinbaseCreatePayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CC-Api-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"code":"CODE1","hosted_url":"https://commerce.coinbase.com/charges/CODE1"}}`))
	}))
	defer srv.Close()
	c := &CoinbaseCommerce{APIKey: "k", APIBase: srv.URL}
	res, err := c.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "O1", Amount: decimal.RequireFromString("20"), Currency: "USD", Description: "topup",
	})
	if err != nil || res.PayURL == "" || res.ProviderRef != "CODE1" {
		t.Fatalf("create = %+v err=%v", res, err)
	}

	// 空 hosted_url（schema 不符）必须报错，绝不返回空跳转 URL
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"code":"X"}}`))
	}))
	defer srv2.Close()
	c2 := &CoinbaseCommerce{APIKey: "k", APIBase: srv2.URL}
	if _, err := c2.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "O", Amount: decimal.RequireFromString("1"), Currency: "USD",
	}); err == nil {
		t.Fatal("empty hosted_url must error (schema mismatch)")
	}
}

func TestCoinbaseSuccessStateMapping(t *testing.T) {
	c := &CoinbaseCommerce{WebhookSecret: "shh"}
	cases := map[string]bool{
		"charge:confirmed": true,  // 最终成功态
		"charge:resolved":  true,  // under/overpaid 已解决（underpaid 由 creditPaidOrder 金额校验拦）
		"charge:pending":   false, // 链上检测未达确认 → 绝不入金
		"charge:delayed":   false,
		"charge:failed":    false,
		"charge:created":   false,
	}
	for typ, wantSuccess := range cases {
		body := []byte(`{"event":{"type":"` + typ + `","data":{"code":"C","metadata":{"order_no":"O"},"pricing":{"local":{"amount":"1.00","currency":"USD"}}}}}`)
		res, err := c.VerifyCallback(body, map[string]string{"X-Cc-Webhook-Signature": ccSign("shh", body)}, nil)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if res.Success != wantSuccess {
			t.Fatalf("%s: Success=%v, want %v", typ, res.Success, wantSuccess)
		}
	}
}
