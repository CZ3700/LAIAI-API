package payment

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 合成 fixture：验证可编程 HTTP 模板引擎的逻辑（模板渲染 + 字段提取 + HMAC 验签 fail-closed）。
// 对接某真实处理商前仍需抓其真实 raw 创建响应/回调校正字段点路径与签名口径。

func customHMAC(algo, secret string, payload []byte) string {
	if algo == "hmac-md5" || algo == "md5" {
		mac := hmac.New(md5.New, []byte(secret))
		mac.Write(payload)
		return hex.EncodeToString(mac.Sum(nil))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// CreatePayment：模板渲染（URL/请求体/请求头 {{占位符}}）+ 点路径提取支付 URL/引用。
func TestCustomCreatePayment(t *testing.T) {
	var gotPath, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":{"pay_url":"https://pay.example/abc","trade_id":"TX99"}}`))
	}))
	defer srv.Close()

	c := &Custom{
		DisplayName:   "mygate",
		APIKey:        "KEY123",
		Currency:      "USD",
		CreateMethod:  "POST",
		CreateURL:     srv.URL + "/create?order={{order_no}}",
		CreateHeaders: map[string]string{"Authorization": "Bearer {{api_key}}"},
		CreateBody:    `{"out_trade_no":"{{order_no}}","amount":"{{amount}}","cur":"{{currency}}","notify":"{{notify_url}}"}`,
		PayURLPath:    "data.pay_url",
		RefPath:       "data.trade_id",
	}
	res, err := c.CreatePayment(context.Background(), &CreateRequest{
		OrderNo:   "ORD-1",
		Amount:    decimal.RequireFromString("10.5"),
		Currency:  "USD",
		NotifyURL: "https://me/notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PayURL != "https://pay.example/abc" || res.ProviderRef != "TX99" {
		t.Fatalf("extract = %+v", res)
	}
	if c.Name() != "mygate" {
		t.Fatalf("Name = %s, want mygate", c.Name())
	}
	// URL 占位符渲染
	if gotPath != "/create?order=ORD-1" {
		t.Fatalf("url render = %q", gotPath)
	}
	// 请求头占位符渲染（{{api_key}}）
	if gotAuth != "Bearer KEY123" {
		t.Fatalf("header render = %q", gotAuth)
	}
	// 请求体占位符渲染 + 金额规整为币种小数位（USD→"10.50"）
	want := `{"out_trade_no":"ORD-1","amount":"10.50","cur":"USD","notify":"https://me/notify"}`
	if gotBody != want {
		t.Fatalf("body render =\n %q\nwant\n %q", gotBody, want)
	}
}

// 金额按币种最小单位规整：USD 2 位、JPY 0 位、BHD 3 位（复刻 AmountToMinor→AmountFromMinor）。
func TestCustomAmountCanonicalization(t *testing.T) {
	cases := []struct {
		amount, currency, want string
	}{
		{"10", "USD", "10.00"},
		{"10.5", "USD", "10.50"},
		{"7.299", "CNY", "7.30"}, // 四舍五入到分
		{"100", "JPY", "100"},    // 零小数位
		{"100.9", "JPY", "101"},  // 取整
		{"0.5", "BHD", "0.500"},  // 三位
	}
	for _, tc := range cases {
		got := canonicalAmount(decimal.RequireFromString(tc.amount), tc.currency)
		if got != tc.want {
			t.Errorf("canonicalAmount(%s,%s) = %q, want %q", tc.amount, tc.currency, got, tc.want)
		}
	}
}

// VerifyCallback：合法签名 → Success true + 字段提取正确。
func TestCustomVerifyCallbackValid(t *testing.T) {
	body := []byte(`{"data":{"order_no":"ORD-1","trade_no":"T1","amount":"10.50","state":"OK"}}`)
	c := &Custom{
		WebhookSecret:    "secret",
		SigAlgo:          "hmac-sha256",
		SigHeader:        "X-Sign",
		Currency:         "USD",
		OrderPath:        "data.order_no",
		TradeNoPath:      "data.trade_no",
		AmountPath:       "data.amount",
		SuccessCondition: "data.state=OK",
	}
	sig := customHMAC("hmac-sha256", "secret", body)
	res, err := c.VerifyCallback(body, map[string]string{"X-Sign": sig}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatal("valid sig + state=OK must be Success")
	}
	if res.OrderNo != "ORD-1" || res.TradeNo != "T1" || res.Currency != "USD" {
		t.Fatalf("extract = %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("10.50")) {
		t.Fatalf("amount = %s, want 10.50", res.Amount)
	}
}

// 🚨 fail-closed：签名不符 / 缺签名 → error + 不入金（绝不在未验真数据上 Success=true）。
func TestCustomVerifyCallbackBadSig(t *testing.T) {
	body := []byte(`{"order_no":"ORD-1","amount":"10.00"}`)
	c := &Custom{WebhookSecret: "secret", SigAlgo: "hmac-sha256"}

	// 伪造签名
	if res, err := c.VerifyCallback(body, map[string]string{"x-signature": "deadbeef"}, nil); err == nil || (res != nil && res.Success) {
		t.Fatalf("bad sig must be rejected (fail-closed): res=%+v err=%v", res, err)
	}
	// 缺签名头
	if res, err := c.VerifyCallback(body, map[string]string{}, nil); err == nil || (res != nil && res.Success) {
		t.Fatalf("missing sig must be rejected (fail-closed): res=%+v err=%v", res, err)
	}
}

// timestamp+body 签名原文 + hmac-md5 算法（大小写不敏感 hex 比对）。
func TestCustomVerifyCallbackTimestampMD5(t *testing.T) {
	body := []byte(`{"order_no":"ORD-9","amount":"5.00"}`)
	ts := "1700000000"
	payload := append([]byte(ts), body...)
	sig := customHMAC("hmac-md5", "k", payload)

	c := &Custom{
		WebhookSecret: "k",
		SigAlgo:       "hmac-md5",
		SigPayload:    "timestamp+body",
		Currency:      "CNY",
	}
	res, err := c.VerifyCallback(body, map[string]string{
		"x-signature": sig, // 默认头名
		"X-Timestamp": ts,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 无 success_condition → 有 order_no 即成功
	if !res.Success || res.OrderNo != "ORD-9" {
		t.Fatalf("res = %+v", res)
	}
	// 错误时间戳 → 原文不符 → fail-closed
	if _, err := c.VerifyCallback(body, map[string]string{"x-signature": sig, "X-Timestamp": "9999"}, nil); err == nil {
		t.Fatal("wrong timestamp must fail-closed")
	}
}

// success_condition 不满足 → Success false（即便签名合法、字段齐全）。
func TestCustomVerifyCallbackConditionFalse(t *testing.T) {
	body := []byte(`{"order_no":"ORD-2","state":"FAILED"}`)
	c := &Custom{
		WebhookSecret:    "s",
		SigAlgo:          "hmac-sha256",
		SuccessCondition: "state=SUCCESS",
	}
	sig := customHMAC("hmac-sha256", "s", body)
	res, err := c.VerifyCallback(body, map[string]string{"x-signature": sig}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("state=FAILED must not be Success (condition mismatch)")
	}
}

// 表单回调（非 JSON）：JSON 解析失败退化为表单 + query 扁平 map，无签名方案时直接解析。
func TestCustomVerifyCallbackFormFallback(t *testing.T) {
	c := &Custom{} // 无 WebhookSecret/SigAlgo → 跳过验签
	res, err := c.VerifyCallback([]byte("order_no=ORD-3&trade_no=T3&amount=8.00"), nil, map[string]string{"extra": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.OrderNo != "ORD-3" || res.TradeNo != "T3" {
		t.Fatalf("form fallback = %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("8.00")) {
		t.Fatalf("amount = %s", res.Amount)
	}
}

func TestCustomAckAndQuery(t *testing.T) {
	c := &Custom{}
	if c.AckBody() != "" {
		t.Fatalf("AckBody = %q, want empty", c.AckBody())
	}
	q, err := c.QueryOrder(context.Background(), QueryRequest{OrderNo: "X"})
	if err != nil || q.Found {
		t.Fatalf("QueryOrder = %+v err=%v (want Found=false, no error)", q, err)
	}
}
