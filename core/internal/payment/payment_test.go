package payment

import (
	"net/url"
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
