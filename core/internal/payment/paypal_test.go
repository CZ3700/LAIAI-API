package payment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 注意：下面的 PayPal API/webhook 负载是按 Orders v2 + verify-webhook-signature
// **官方文档**推导的 fixture，用于验证 OAuth 流程 + 建单请求形态 + 验签 FAIL-CLOSED
// + 成功态映射「逻辑」。上线动真金前必须接真实 PayPal（sandbox）账号抓 raw
// response 替换核对（webhook envelope 字段层级、capture 嵌套、order status 枚举）。
//
// 移植自旧平台 engine_paypal.go 的行为契约：
//   - CreatePayment：OAuth → POST /v2/checkout/orders（intent=CAPTURE）→ 取 approve link
//   - VerifyCallback：FAIL-CLOSED（旧引擎缺失，本移植按资金红线补 verify-webhook-signature）
//   - CHECKOUT.ORDER.APPROVED → 自动 capture；PAYMENT.CAPTURE.COMPLETED → 直接入金
//   - QueryOrder：GET /v2/checkout/orders/{id} 映射 Paid/Amount/TradeNo

const ppToken = "A21AA-fake-access-token"

// ppMux 构造一个模拟 PayPal API 的 httptest server。
//   verifyStatus："SUCCESS" / "FAILURE"（verify-webhook-signature 返回值）
//   captureStatus：capture 端点返回的 order status（如 "COMPLETED"）
func ppMux(t *testing.T, verifyStatus, captureStatus string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// OAuth2 token 端点
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "grant_type=client_credentials") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": ppToken, "token_type": "Bearer"})
	})

	// 建单端点
	mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+ppToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "ORDER123",
			"status": "CREATED",
			"links": []map[string]string{
				{"rel": "self", "href": "https://api/v2/checkout/orders/ORDER123"},
				{"rel": "approve", "href": "https://www.paypal.com/checkoutnow?token=ORDER123"},
				{"rel": "capture", "href": "https://api/v2/checkout/orders/ORDER123/capture"},
			},
		})
	})

	// verify-webhook-signature 端点
	mux.HandleFunc("/v1/notifications/verify-webhook-signature", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"verification_status": verifyStatus})
	})

	// capture 端点（CHECKOUT.ORDER.APPROVED 自动 capture 用）
	mux.HandleFunc("/v2/checkout/orders/ORDER123/capture", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "ORDER123",
			"status": captureStatus,
			"purchase_units": []map[string]any{{
				"reference_id": "USR1NOdeadbeef",
				"custom_id":    "USR1NOdeadbeef",
				"payments": map[string]any{
					"captures": []map[string]any{{
						"id":     "CAP999",
						"status": "COMPLETED",
						"amount": map[string]string{"value": "10.00", "currency_code": "USD"},
					}},
				},
			}},
		})
	})

	return httptest.NewServer(mux)
}

// 全部 PAYPAL-* 验签头（FAIL-CLOSED 要求 5 个齐全）。
func ppHeaders() map[string]string {
	return map[string]string{
		"PAYPAL-AUTH-ALGO":         "SHA256withRSA",
		"PAYPAL-CERT-URL":          "https://api.paypal.com/cert.pem",
		"PAYPAL-TRANSMISSION-ID":   "txn-id-123",
		"PAYPAL-TRANSMISSION-SIG":  "base64sig==",
		"PAYPAL-TRANSMISSION-TIME": "2026-06-17T00:00:00Z",
	}
}

func TestPayPalCreatePayment(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "cid", ClientSecret: "sec", APIBase: srv.URL}

	res, err := p.CreatePayment(context.Background(), &CreateRequest{
		OrderNo:     "USR1NOdeadbeef",
		Amount:      decimal.RequireFromString("10.00"),
		Currency:    "USD",
		Description: "topup",
		ReturnURL:   "https://laiai.app/pay/return",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.ProviderRef != "ORDER123" {
		t.Fatalf("ProviderRef=%q, want ORDER123 (order id)", res.ProviderRef)
	}
	if !strings.Contains(res.PayURL, "checkoutnow") {
		t.Fatalf("PayURL should be the approve link, got %q", res.PayURL)
	}
}

// 建单请求形态：intent=CAPTURE、purchase_units[0] 带 currency_code/value/custom_id=order_no、
// application_context.return_url 拼了 order_no。验证发往 PayPal 的请求体（移植自旧引擎形态）。
func TestPayPalCreatePaymentRequestShape(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": ppToken})
	})
	mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "O1",
			"links": []map[string]string{{"rel": "approve", "href": "https://approve"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &PayPal{ClientID: "cid", ClientSecret: "sec", APIBase: srv.URL}
	_, err := p.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "ORD-7", Amount: decimal.RequireFromString("12.5"), Currency: "USD",
		Description: "买点数", ReturnURL: "https://r",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent["intent"] != "CAPTURE" {
		t.Fatalf("intent=%v, want CAPTURE", sent["intent"])
	}
	pu := sent["purchase_units"].([]any)[0].(map[string]any)
	if pu["custom_id"] != "ORD-7" || pu["reference_id"] != "ORD-7" {
		t.Fatalf("purchase_unit ids wrong: %+v", pu)
	}
	amt := pu["amount"].(map[string]any)
	if amt["currency_code"] != "USD" || amt["value"] != "12.50" {
		t.Fatalf("amount wrong: %+v (value should be canonicalised to 2dp)", amt)
	}
	ac := sent["application_context"].(map[string]any)
	if !strings.Contains(ac["return_url"].(string), "order_no=ORD-7") {
		t.Fatalf("return_url should carry order_no: %v", ac["return_url"])
	}
}

// JPY（0 位小数币种）：value 不应带小数点（移植旧引擎多币种最小单位规整）。
func TestPayPalCreatePaymentJPY(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": ppToken})
	})
	mux.HandleFunc("/v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "O", "links": []map[string]string{{"rel": "approve", "href": "https://a"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &PayPal{ClientID: "c", ClientSecret: "s", APIBase: srv.URL}
	if _, err := p.CreatePayment(context.Background(), &CreateRequest{
		OrderNo: "J1", Amount: decimal.RequireFromString("100"), Currency: "JPY", ReturnURL: "https://r",
	}); err != nil {
		t.Fatalf("create JPY: %v", err)
	}
	var sent map[string]any
	_ = json.Unmarshal(gotBody, &sent)
	amt := sent["purchase_units"].([]any)[0].(map[string]any)["amount"].(map[string]any)
	if amt["value"] != "100" {
		t.Fatalf("JPY value=%v, want \"100\" (no decimals)", amt["value"])
	}
}

// VerifyCallback 成功：PAYMENT.CAPTURE.COMPLETED + verify-webhook 返回 SUCCESS → 入金。
func TestPayPalVerifyCallbackCaptureCompleted(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", WebhookID: "WH-1", APIBase: srv.URL}

	body := []byte(`{"event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{` +
		`"id":"CAP999","status":"COMPLETED","custom_id":"USR1NOdeadbeef",` +
		`"amount":{"value":"10.00","currency_code":"USD"}}}`)

	res, err := p.VerifyCallback(body, ppHeaders(), nil)
	if err != nil {
		t.Fatalf("verify capture completed: %v", err)
	}
	if !res.Success {
		t.Fatal("PAYMENT.CAPTURE.COMPLETED with verified sig must be Success=true")
	}
	if res.OrderNo != "USR1NOdeadbeef" || res.TradeNo != "CAP999" {
		t.Fatalf("parsed order/trade wrong: %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("10.00")) || res.Currency != "USD" {
		t.Fatalf("parsed amount/currency wrong: %+v", res)
	}
}

// VerifyCallback 自动 capture：CHECKOUT.ORDER.APPROVED → 调 capture 端点 → 按 capture 结果入金。
func TestPayPalVerifyCallbackOrderApprovedAutoCapture(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", WebhookID: "WH-1", APIBase: srv.URL}

	body := []byte(`{"event_type":"CHECKOUT.ORDER.APPROVED","resource":{"id":"ORDER123","status":"APPROVED"}}`)
	res, err := p.VerifyCallback(body, ppHeaders(), nil)
	if err != nil {
		t.Fatalf("verify approved: %v", err)
	}
	if !res.Success {
		t.Fatal("APPROVED → capture COMPLETED must be Success=true")
	}
	if res.TradeNo != "CAP999" || res.OrderNo != "USR1NOdeadbeef" {
		t.Fatalf("capture result mapping wrong: %+v", res)
	}
	if !res.Amount.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("capture amount wrong: %+v", res)
	}
}

// 🚨 FAIL-CLOSED #1：verify-webhook-signature 返回 FAILURE → 必须拒绝（不入金）。
func TestPayPalVerifyCallbackFailClosedOnVerifyFailure(t *testing.T) {
	srv := ppMux(t, "FAILURE", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", WebhookID: "WH-1", APIBase: srv.URL}

	body := []byte(`{"event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"X","status":"COMPLETED","custom_id":"O","amount":{"value":"99.00","currency_code":"USD"}}}`)
	if _, err := p.VerifyCallback(body, ppHeaders(), nil); err == nil {
		t.Fatal("verification_status=FAILURE must be rejected (fail-closed, never credit unverified)")
	}
}

// 🚨 FAIL-CLOSED #2：缺验签头 → 不发起验签即拒绝（即便 verify 端点会返回 SUCCESS）。
func TestPayPalVerifyCallbackFailClosedOnMissingHeaders(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", WebhookID: "WH-1", APIBase: srv.URL}

	body := []byte(`{"event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"X","status":"COMPLETED","custom_id":"O","amount":{"value":"99.00","currency_code":"USD"}}}`)
	// 只给 4 个头（缺 PAYPAL-TRANSMISSION-SIG）
	hdrs := ppHeaders()
	delete(hdrs, "PAYPAL-TRANSMISSION-SIG")
	if _, err := p.VerifyCallback(body, hdrs, nil); err == nil {
		t.Fatal("missing PAYPAL-* signature header must be rejected (fail-closed)")
	}
}

// 🚨 FAIL-CLOSED #3：未配置 WebhookID → 验签恒失败（绝不裸信任 JSON 入金，堵旧引擎的漏洞）。
func TestPayPalVerifyCallbackFailClosedOnMissingWebhookID(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", APIBase: srv.URL} // 无 WebhookID

	body := []byte(`{"event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"X","status":"COMPLETED","custom_id":"O","amount":{"value":"99.00","currency_code":"USD"}}}`)
	if _, err := p.VerifyCallback(body, ppHeaders(), nil); err == nil {
		t.Fatal("missing WebhookID must fail-closed (no verification possible)")
	}
}

// 非成功事件（验签通过但事件类型非成功态）→ Success=false（不入金）。
func TestPayPalVerifyCallbackNonSuccessEvent(t *testing.T) {
	srv := ppMux(t, "SUCCESS", "COMPLETED")
	defer srv.Close()
	p := &PayPal{ClientID: "c", ClientSecret: "s", WebhookID: "WH-1", APIBase: srv.URL}

	body := []byte(`{"event_type":"PAYMENT.CAPTURE.DENIED","resource":{"id":"X","status":"DECLINED"}}`)
	res, err := p.VerifyCallback(body, ppHeaders(), nil)
	if err != nil {
		t.Fatalf("verify denied: %v", err)
	}
	if res.Success {
		t.Fatal("PAYMENT.CAPTURE.DENIED must map to Success=false")
	}
}

// QueryOrder（reconcile 兜底腿）：COMPLETED→Paid、CREATED→未付、404→Found=false、空 ref→Found=false。
// ⚠ 合成 fixture，上线前换真实 raw。
func TestPayPalQueryOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": ppToken})
	})
	mux.HandleFunc("/v2/checkout/orders/PAID", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "PAID", "status": "COMPLETED",
			"purchase_units": []map[string]any{{
				"amount": map[string]string{"value": "25.00", "currency_code": "USD"},
				"payments": map[string]any{"captures": []map[string]any{{
					"id": "CAPX", "status": "COMPLETED",
					"amount": map[string]string{"value": "25.00", "currency_code": "USD"},
				}}},
			}},
		})
	})
	mux.HandleFunc("/v2/checkout/orders/UNPAID", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "UNPAID", "status": "CREATED",
			"purchase_units": []map[string]any{{"amount": map[string]string{"value": "25.00", "currency_code": "USD"}}},
		})
	})
	// 其余路径 → 404
	mux.HandleFunc("/v2/checkout/orders/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &PayPal{ClientID: "c", ClientSecret: "s", APIBase: srv.URL}

	if qr, err := p.QueryOrder(context.Background(), QueryRequest{ProviderRef: "PAID"}); err != nil ||
		!qr.Found || !qr.Paid || qr.TradeNo != "CAPX" || !qr.Amount.Equal(decimal.RequireFromString("25.00")) {
		t.Fatalf("PAID query = %+v err=%v", qr, err)
	}
	if qr, _ := p.QueryOrder(context.Background(), QueryRequest{ProviderRef: "UNPAID"}); !qr.Found || qr.Paid {
		t.Fatalf("UNPAID query = %+v, want Found && !Paid", qr)
	}
	if qr, _ := p.QueryOrder(context.Background(), QueryRequest{ProviderRef: "NOPE"}); qr.Found {
		t.Fatalf("404 must be Found=false: %+v", qr)
	}
	if qr, _ := p.QueryOrder(context.Background(), QueryRequest{ProviderRef: ""}); qr.Found {
		t.Fatal("empty ProviderRef must be Found=false")
	}
}

// sandbox/live 基址切换（移植旧引擎 baseURL()）。
func TestPayPalBaseURLSwitch(t *testing.T) {
	live := &PayPal{}
	if live.baseURL() != "https://api-m.paypal.com" {
		t.Fatalf("live baseURL=%q", live.baseURL())
	}
	sandbox := &PayPal{Sandbox: true}
	if sandbox.baseURL() != "https://api-m.sandbox.paypal.com" {
		t.Fatalf("sandbox baseURL=%q", sandbox.baseURL())
	}
	override := &PayPal{Sandbox: true, APIBase: "http://127.0.0.1:1234/"}
	if override.baseURL() != "http://127.0.0.1:1234" {
		t.Fatalf("APIBase override should win and trim slash, got %q", override.baseURL())
	}
}

// 资金红线回归（对抗评审 C-1）：TWD/HUF 必须 2 位小数（与旧平台 minor_units 真值一致），
// 绝不被凭文档臆造重分类为整数币种——否则 100.50 TWD 会被四舍五入成 101 误扣。
func TestPayPalCurrencyMinorUnitsGroundTruth(t *testing.T) {
	for _, c := range []string{"TWD", "HUF", "USD", "EUR", "CNY", "GBP"} {
		if got := currencyMinorUnits(c); got != 100 {
			t.Fatalf("%s minor units = %d, want 100 (2-decimal, ground-truth)", c, got)
		}
	}
	if currencyMinorUnits("JPY") != 1 {
		t.Fatalf("JPY minor units = %d, want 1", currencyMinorUnits("JPY"))
	}
	if currencyMinorUnits("BHD") != 1000 {
		t.Fatalf("BHD minor units = %d, want 1000", currencyMinorUnits("BHD"))
	}
	// 100.50 TWD 往返保留两位小数（不被截断为整数）
	m, err := amountToMinor(decimal.RequireFromString("100.50"), "TWD")
	if err != nil {
		t.Fatalf("amountToMinor TWD: %v", err)
	}
	if got := amountFromMinor(m, "TWD"); got != "100.50" {
		t.Fatalf("TWD 100.50 round-trip = %q, want 100.50", got)
	}
}
