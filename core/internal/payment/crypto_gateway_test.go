package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 合成 fixture：验证处理商无关的框架逻辑（HMAC 验签 + 成功态 + 实付金额 + 包裹解析）。
// 上线某真实处理商（EBPay/OxaPay…）前必须接其真实 raw webhook/响应替换、校正签名口径与字段名。

func cryptoSign(algo, secret string, body []byte) string {
	var mac hash.Hash
	if algo == "sha512" {
		mac = hmac.New(sha512.New, []byte(secret))
	} else {
		mac = hmac.New(sha256.New, []byte(secret))
	}
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCryptoGatewayVerify(t *testing.T) {
	g := &CryptoGateway{Processor: "ebpay", WebhookSecret: "s", SignAlgo: "sha256"}
	body := []byte(`{"order_id":"O1","payment_id":"P1","status":"confirmed","amount_paid":"50.00","amount":"50.00","currency":"USD"}`)
	res, err := g.VerifyCallback(body, map[string]string{"X-Signature": cryptoSign("sha256", "s", body)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.OrderNo != "O1" || res.TradeNo != "P1" || !res.Amount.Equal(decimal.RequireFromString("50.00")) {
		t.Fatalf("res = %+v", res)
	}
	if g.Name() != "ebpay" {
		t.Fatalf("Name = %s, want ebpay", g.Name())
	}
	// 伪造签名必须拒（fail-closed）
	if _, err := g.VerifyCallback(body, map[string]string{"X-Signature": "deadbeef"}, nil); err == nil {
		t.Fatal("bad signature must be rejected")
	}
}

func TestCryptoGatewayUnderpaidAndStatus(t *testing.T) {
	g := &CryptoGateway{WebhookSecret: "s"}
	// underpaid：实付 5 < 应付 100，金额必须返回实付 5（由 creditPaidOrder「实付不得少于应付」拦）
	body := []byte(`{"order_id":"O","status":"paid","amount_paid":"5.00","amount":"100.00","currency":"USDT"}`)
	res, _ := g.VerifyCallback(body, map[string]string{"X-Signature": cryptoSign("sha256", "s", body)}, nil)
	if !res.Amount.Equal(decimal.RequireFromString("5.00")) {
		t.Fatalf("underpaid amount = %s, want 5.00 (实付，非应付 100)", res.Amount)
	}
	// pending 绝不入金
	pb := []byte(`{"order_id":"O","status":"pending","amount_paid":"0"}`)
	pr, _ := g.VerifyCallback(pb, map[string]string{"X-Signature": cryptoSign("sha256", "s", pb)}, nil)
	if pr.Success {
		t.Fatal("pending must not be success")
	}

	// sha512 算法 + 备用 header 名
	g2 := &CryptoGateway{WebhookSecret: "k", SignAlgo: "sha512"}
	b2 := []byte(`{"order_id":"O2","status":"completed","amount_paid":"1.00"}`)
	r2, err := g2.VerifyCallback(b2, map[string]string{"signature": cryptoSign("sha512", "k", b2)}, nil)
	if err != nil || !r2.Success {
		t.Fatalf("sha512 = %+v err=%v", r2, err)
	}
}

func TestCryptoGatewayDataWrapped(t *testing.T) {
	body := []byte(`{"data":{"order_id":"O3","status":"success","amount_paid":"20.00"}}`)
	g := &CryptoGateway{WebhookSecret: "s"}
	res, _ := g.VerifyCallback(body, map[string]string{"X-Signature": cryptoSign("sha256", "s", body)}, nil)
	if !res.Success || res.OrderNo != "O3" || !res.Amount.Equal(decimal.RequireFromString("20.00")) {
		t.Fatalf("data-wrapped parse = %+v", res)
	}
}

func TestCryptoGatewayCreatePayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"payment_url":"https://pay.example/abc","payment_id":"PX"}`))
	}))
	defer srv.Close()
	g := &CryptoGateway{APIKey: "k", APIBase: srv.URL}
	res, err := g.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "O", Amount: decimal.RequireFromString("10"), Currency: "USD",
	})
	if err != nil || res.PayURL == "" || res.ProviderRef != "PX" {
		t.Fatalf("create = %+v err=%v", res, err)
	}
	// 无 payment_url/pay_address → 报 schema mismatch
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"payment_id":"X"}`))
	}))
	defer srv2.Close()
	g2 := &CryptoGateway{APIKey: "k", APIBase: srv2.URL}
	if _, err := g2.CreatePayment(context.Background(), &CreateRequest{OrderNo: "O", Amount: decimal.RequireFromString("1"), Currency: "USD"}); err == nil {
		t.Fatal("missing pay target must error")
	}
	// G4：注入类 payment_url（javascript:）必须拒绝（与 custom/webhook 同口径）
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"payment_url":"javascript:alert(1)","payment_id":"EVIL"}`))
	}))
	defer srv3.Close()
	g3 := &CryptoGateway{APIKey: "k", APIBase: srv3.URL}
	if _, err := g3.CreatePayment(context.Background(), &CreateRequest{OrderNo: "O", Amount: decimal.RequireFromString("1"), Currency: "USD"}); err == nil {
		t.Fatal("javascript: payment_url must be rejected")
	}
	// 链上地址回退（非 URL）不受 URL 校验误伤
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pay_address":"TXk9qwerty123","payment_id":"ADDR"}`))
	}))
	defer srv4.Close()
	g4 := &CryptoGateway{APIKey: "k", APIBase: srv4.URL}
	if res, err := g4.CreatePayment(context.Background(), &CreateRequest{OrderNo: "O", Amount: decimal.RequireFromString("1"), Currency: "USD"}); err != nil || res.PayURL != "TXk9qwerty123" {
		t.Fatalf("chain address fallback must survive: %+v err=%v", res, err)
	}
}
