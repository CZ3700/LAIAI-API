package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	body := []byte(`{"event":{"type":"charge:confirmed","data":{` +
		`"code":"ABC123","metadata":{"order_no":"USR1NOdeadbeef"},` +
		`"pricing":{"local":{"amount":"100.00","currency":"USD"}}}}}`)
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
