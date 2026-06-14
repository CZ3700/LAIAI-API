package payment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// EPay 签名 + 验签往返。
func TestEPaySignRoundtrip(t *testing.T) {
	e := &EPay{PID: "1001", Key: "secret", APIURL: "https://pay.test"}
	// 构造一个回调（含正确签名）
	params := map[string]string{
		"pid": "1001", "out_trade_no": "USR1NOabc", "trade_no": "T123",
		"money": "73.00", "trade_status": "TRADE_SUCCESS",
	}
	sign := epaySign(params, "secret")
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	v.Set("sign", sign)
	res, err := e.VerifyCallback([]byte(v.Encode()), nil, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Success || res.OrderNo != "USR1NOabc" || !res.Amount.Equal(decimal.RequireFromString("73")) {
		t.Fatalf("callback result = %+v", res)
	}
	// 篡改签名 → 拒
	v.Set("sign", "deadbeef")
	if _, err := e.VerifyCallback([]byte(v.Encode()), nil, nil); err == nil {
		t.Fatal("tampered signature should fail")
	}
	// 非 CNY 创建被拒
	if _, err := e.CreatePayment(nil, &CreateRequest{Currency: "USD", Amount: decimal.RequireFromString("10")}); err == nil {
		t.Fatal("epay should reject non-CNY")
	}
}

// EPay QueryOrder 字段方言：查询用 `status`（1=已支付），不是回调的 `trade_status`。
// fixture 用彩虹 api.php?act=order 的真实 JSON 形态。
func TestEPayQueryOrderDialect(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		// 已支付订单的彩虹查询响应（status=1 是方言关键）
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"msg":"查询成功","trade_no":"2024X","out_trade_no":"USR1NOabc","type":"alipay","pid":1001,"addtime":"2026-06-14 10:00:00","endtime":"2026-06-14 10:01:00","name":"topup","money":"73.00","status":1}`))
	}))
	defer srv.Close()

	e := &EPay{PID: "1001", Key: "secret", APIURL: srv.URL, Client: srv.Client()}
	qr, err := e.QueryOrder(context.Background(), QueryRequest{OrderNo: "USR1NOabc"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !qr.Found || !qr.Paid || qr.TradeNo != "2024X" || !qr.Amount.Equal(decimal.RequireFromString("73")) {
		t.Fatalf("query result = %+v", qr)
	}
	// 请求形态：act=order + pid/key/out_trade_no（key 明文鉴权，查询无需另签）
	if !strings.Contains(gotPath, "api.php") || !strings.Contains(gotQuery, "act=order") ||
		!strings.Contains(gotQuery, "out_trade_no=USR1NOabc") || !strings.Contains(gotQuery, "key=secret") {
		t.Fatalf("query request = %s?%s", gotPath, gotQuery)
	}

	// 未支付：status=0 → Found=true, Paid=false
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":1,"trade_no":"","out_trade_no":"X","money":"73.00","status":0}`))
	}))
	defer srv2.Close()
	e2 := &EPay{PID: "1001", Key: "k", APIURL: srv2.URL, Client: srv2.Client()}
	qr2, _ := e2.QueryOrder(context.Background(), QueryRequest{OrderNo: "X"})
	if !qr2.Found || qr2.Paid {
		t.Fatalf("unpaid query = %+v, want found+unpaid", qr2)
	}

	// 订单不存在：code!=1 → Found=false
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"订单不存在"}`))
	}))
	defer srv3.Close()
	e3 := &EPay{PID: "1001", Key: "k", APIURL: srv3.URL, Client: srv3.Client()}
	qr3, _ := e3.QueryOrder(context.Background(), QueryRequest{OrderNo: "nope"})
	if qr3.Found {
		t.Fatalf("missing order query = %+v, want not found", qr3)
	}
}

// Stripe QueryOrder：按 Checkout Session id 查 payment_status；404 → 查不到。
func TestStripeQueryOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/cs_test_1") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_1","payment_status":"paid","payment_intent":"pi_9","amount_total":5000,"currency":"usd"}`))
	}))
	defer srv.Close()

	s := &Stripe{SecretKey: "sk_test", APIBase: srv.URL, Client: srv.Client()}
	qr, err := s.QueryOrder(context.Background(), QueryRequest{ProviderRef: "cs_test_1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !qr.Found || !qr.Paid || qr.TradeNo != "pi_9" || !qr.Amount.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("stripe query = %+v", qr)
	}
	// 未知 session → 404 → Found=false
	qr2, _ := s.QueryOrder(context.Background(), QueryRequest{ProviderRef: "cs_missing"})
	if qr2.Found {
		t.Fatalf("missing session = %+v, want not found", qr2)
	}
	// 空 ProviderRef → 直接 Found=false（不打网络）
	qr3, _ := s.QueryOrder(context.Background(), QueryRequest{ProviderRef: ""})
	if qr3.Found {
		t.Fatalf("empty ref = %+v, want not found", qr3)
	}
}

// Stripe webhook 验签：无密钥/无签名一律拒。
func TestStripeSignatureRejects(t *testing.T) {
	s := &Stripe{WebhookSecret: ""}
	if s.verifySignature([]byte("{}"), "t=1,v1=x") {
		t.Fatal("empty webhook secret must reject")
	}
	s.WebhookSecret = "whsec_test"
	if s.verifySignature([]byte("{}"), "") {
		t.Fatal("missing signature must reject")
	}
	if s.verifySignature([]byte("{}"), "t=1,v1=bogus") {
		t.Fatal("bogus signature must reject")
	}
}
