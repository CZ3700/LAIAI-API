package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// 资金红线回归（对抗评审 H-1/H-2）：webhook/custom 即便配了 webhook_secret，若 sig_algo=none/未知
// 则回调不验签=伪造入金漏洞，buildPaymentEngine 必须拒绝构建（返回 nil，渠道不可用）。
func TestBuildEngineRejectsUnverifiedWebhookCustom(t *testing.T) {
	base := map[string]string{
		"api_base_url": "https://api.x.com", "create_url": "https://api.x.com/pay", "webhook_secret": "s3cr3t",
	}
	clone := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, proto := range []string{"webhook", "custom"} {
		if buildPaymentEngine(proto, clone(nil)) == nil { // 留空 sig_algo → 默认 hmac-sha256 → 建得起
			t.Fatalf("%s with default sig_algo should build", proto)
		}
		if buildPaymentEngine(proto, clone(map[string]string{"sig_algo": "none"})) != nil {
			t.Fatalf("%s sig_algo=none MUST NOT build (forgery hole)", proto)
		}
		if buildPaymentEngine(proto, clone(map[string]string{"sig_algo": "rot13"})) != nil {
			t.Fatalf("%s unknown sig_algo MUST NOT build", proto)
		}
		if buildPaymentEngine(proto, clone(map[string]string{"webhook_secret": ""})) != nil {
			t.Fatalf("%s without webhook_secret MUST NOT build", proto)
		}
	}
	// paypal：webhook_id 是验签必需，缺则不建（否则回调全 fail-closed，渠道无用且误导）。
	if buildPaymentEngine("paypal", map[string]string{"client_id": "a", "client_secret": "b"}) != nil {
		t.Fatal("paypal without webhook_id MUST NOT build")
	}
	if buildPaymentEngine("paypal", map[string]string{"client_id": "a", "client_secret": "b", "webhook_id": "wh"}) == nil {
		t.Fatal("paypal with id+secret+webhook_id should build")
	}
}

func TestPaymentChannelCRUD(t *testing.T) {
	srv, _ := setupRelayServer(t)

	// 建渠道（带密钥）
	w, out := srv.doAdmin(t, "POST", "/api/admin/payment-channels", map[string]any{
		"code": "stripe_main", "display_name": "Stripe Main", "protocol": "stripe",
		"config":      map[string]string{"secret_key": "sk_test_SECRET", "webhook_secret": "whsec_x"},
		"pay_methods": "card", "currency": "USD",
	})
	if w.Code != 200 {
		t.Fatalf("create = %d %v", w.Code, out)
	}

	// 列表：密钥绝不回显
	w, out = srv.doAdmin(t, "GET", "/api/admin/payment-channels", nil)
	if body, _ := json.Marshal(out); bytes.Contains(body, []byte("sk_test_SECRET")) {
		t.Fatal("SECURITY: secret key leaked in channel list")
	}
	channels := out["channels"].([]any)
	if len(channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(channels))
	}
	id := int(channels[0].(map[string]any)["id"].(float64))

	// 连通性测试：配置完整 → ok
	w, out = srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/payment-channels/%d/test", id), nil)
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("test = %d %v", w.Code, out)
	}

	// 路由预览：card → stripe_main
	_, out = srv.doAdmin(t, "GET", "/api/admin/payment-channels/route-preview", nil)
	cardCh := ""
	for _, r := range out["routes"].([]any) {
		m := r.(map[string]any)
		if m["method"] == "card" {
			cardCh, _ = m["channel_code"].(string)
		}
	}
	if cardCh != "stripe_main" {
		t.Errorf("card route = %q, want stripe_main", cardCh)
	}

	// 用户侧支付方式：表渠道生效
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "pc@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "pc@gmail.com", "password": "longenough1"})
	token := login["token"].(string)
	_, mo := srv.do(t, "GET", "/api/payment/methods", token, nil)
	methods := mo["methods"].([]any)
	if len(methods) != 1 || methods[0].(map[string]any)["provider"] != "stripe_main" {
		t.Fatalf("payment methods = %v, want [stripe_main]", methods)
	}

	// 编辑（省略 config 保留原密钥）：改显示名
	w, _ = srv.doAdmin(t, "POST", "/api/admin/payment-channels", map[string]any{
		"code": "stripe_main", "display_name": "Renamed", "protocol": "stripe", "pay_methods": "card",
	})
	if w.Code != 200 {
		t.Fatalf("update = %d", w.Code)
	}
	_, mo = srv.do(t, "GET", "/api/payment/methods", token, nil)
	if mo["methods"].([]any)[0].(map[string]any)["label"] != "Renamed" {
		t.Error("rename not reflected")
	}

	// 删除
	w, _ = srv.doAdmin(t, "DELETE", fmt.Sprintf("/api/admin/payment-channels/%d", id), nil)
	if w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
}
