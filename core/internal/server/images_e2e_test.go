package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// 可编程张数的图像上游（真实 OpenAI images 响应形态）
func mockImagesUpstream(t *testing.T, imagesToReturn int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		data := make([]map[string]any, imagesToReturn)
		for i := range data {
			data[i] = map[string]any{"url": fmt.Sprintf("https://cdn.example.com/img-%d.png", i)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"created": 1700000000, "data": data})
	}))
}

func setupImagesChannel(t *testing.T, srv *gin999, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "img-mock", "provider": "openai", "base_url": baseURL,
		"api_key": "sk-upstream-secret", "models": []string{"img-test"},
	})
	if w.Code != 201 {
		t.Fatalf("channel = %d %v", w.Code, out)
	}
	// $0.04/张
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "img-test", "input_usd_per_1m": "0", "output_usd_per_1m": "0", "image_usd": "0.04",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
}

func TestImagesBillingByCount(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockImagesUpstream(t, 2)
	defer up.Close()
	setupImagesChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "img@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/images/generations", apiKey, map[string]any{
		"model": "img-test", "prompt": "a lighthouse at dawn", "n": 2,
	})
	if w.Code != 200 {
		t.Fatalf("images = %d %v", w.Code, out)
	}
	if data := out["data"].([]any); len(data) != 2 {
		t.Fatalf("response data = %d items", len(data))
	}
	// 2 张 × $0.04 = $0.08
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.92")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after 2 images: posted=%s held=%s, want 9.92/0", acc.PostedBalance, acc.HeldAmount)
	}
}

func TestImagesPartialDeliveryChargesActual(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockImagesUpstream(t, 1) // 请求 4 张，上游只出 1 张
	defer up.Close()
	setupImagesChannel(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "img-partial@example.com", "10")

	w, _ := srv.do(t, "POST", "/v1/images/generations", apiKey, map[string]any{
		"model": "img-test", "prompt": "four cats", "n": 4,
	})
	if w.Code != 200 {
		t.Fatalf("images = %d", w.Code)
	}
	// hold 是 4×0.04=0.16，但只收实际 1 张：$0.04，差额自动解冻
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.96")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("partial delivery: posted=%s held=%s, want 9.96/0", acc.PostedBalance, acc.HeldAmount)
	}
}

func TestImagesGuards(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := mockImagesUpstream(t, 1)
	defer up.Close()
	setupImagesChannel(t, srv, up.URL)

	// 余额不足 → 402 无残渣
	poorKey, _ := seedUserWithCredit(t, srv, "img-poor@example.com", "")
	w, _ := srv.do(t, "POST", "/v1/images/generations", poorKey, map[string]any{
		"model": "img-test", "prompt": "x",
	})
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("no balance = %d, want 402", w.Code)
	}

	richKey, _ := seedUserWithCredit(t, srv, "img-rich@example.com", "10")
	// n 上限
	w, _ = srv.do(t, "POST", "/v1/images/generations", richKey, map[string]any{
		"model": "img-test", "prompt": "x", "n": 11,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("n=11 = %d, want 400", w.Code)
	}
	// 未配图像单价的模型 → 明确 400
	srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "img-test", "input_usd_per_1m": "1", "output_usd_per_1m": "1", // image_usd 归零
	})
	w, out := srv.do(t, "POST", "/v1/images/generations", richKey, map[string]any{
		"model": "img-test", "prompt": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unpriced image model = %d %v, want 400", w.Code, out)
	}
}
