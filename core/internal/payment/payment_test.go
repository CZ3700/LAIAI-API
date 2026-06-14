package payment

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// x402 引擎：requirements 构造（USDC 原子单位）+ Settle 经 mock facilitator verify→settle。
func TestX402Engine(t *testing.T) {
	var verifyHit, settleHit int
	fac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/verify":
			verifyHit++
			_, _ = w.Write([]byte(`{"isValid":true,"payer":"0xPayer"}`))
		case "/settle":
			settleHit++
			_, _ = w.Write([]byte(`{"success":true,"transaction":"0xTxHash","payer":"0xPayer","network":"base"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fac.Close()

	x := &X402{FacilitatorURL: fac.URL, PayTo: "0xMerchant", Asset: "0xUSDC", Client: fac.Client()}

	// CreatePayment：requirements JSON 含 payTo/asset/原子金额（$50 → 50000000）
	cr, err := x.CreatePayment(context.Background(), &CreateRequest{OrderNo: "ORD1", Amount: decimal.RequireFromString("50")})
	if err != nil || cr.Requirements == "" || cr.PayURL != "" {
		t.Fatalf("create = %+v err=%v", cr, err)
	}
	var reqDoc struct {
		Accepts []paymentRequirements `json:"accepts"`
	}
	if err := json.Unmarshal([]byte(cr.Requirements), &reqDoc); err != nil || len(reqDoc.Accepts) != 1 {
		t.Fatalf("requirements json = %s err=%v", cr.Requirements, err)
	}
	a := reqDoc.Accepts[0]
	if a.MaxAmountRequired != "50000000" || a.PayTo != "0xMerchant" || a.Asset != "0xUSDC" || a.Network != "base" {
		t.Fatalf("requirements = %+v", a)
	}

	// Settle：X-PAYMENT 是 base64 的 paymentPayload JSON
	xpay := base64.StdEncoding.EncodeToString([]byte(`{"x402Version":1,"scheme":"exact","network":"base","payload":{}}`))
	res, err := x.Settle(context.Background(), xpay, "ORD1", decimal.RequireFromString("50"), "topup")
	if err != nil || res == nil || !res.Success || res.TxHash != "0xTxHash" {
		t.Fatalf("settle = %+v err=%v", res, err)
	}
	if verifyHit != 1 || settleHit != 1 {
		t.Fatalf("facilitator hits: verify=%d settle=%d", verifyHit, settleHit)
	}

	// verify 拒绝 → Settle 失败且不 settle
	facReject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/verify" {
			_, _ = w.Write([]byte(`{"isValid":false,"invalidReason":"insufficient_funds"}`))
			return
		}
		t.Error("settle must not be called when verify rejects")
	}))
	defer facReject.Close()
	xr := &X402{FacilitatorURL: facReject.URL, PayTo: "0xM", Asset: "0xU", Client: facReject.Client()}
	if res, err := xr.Settle(context.Background(), xpay, "ORD2", decimal.RequireFromString("10"), ""); err == nil || (res != nil && res.Success) {
		t.Fatalf("rejected verify should fail settle: res=%+v err=%v", res, err)
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
