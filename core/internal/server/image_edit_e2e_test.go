package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// D1-b：/v1/images/edits multipart 图生图——路由命中 /images/edits、按返回张数 ImageUSD 计价、
// 模型字段透传，与生成同款 hold/对账。
func TestImageEditsBillingByCount(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotPath, gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseMultipartForm(1 << 20)
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"url": "a"}, {"url": "b"}}})
	}))
	defer up.Close()
	setupImagesChannel(t, srv, up.URL) // model img-test @ $0.04/张
	apiKey, orgID := seedUserWithCredit(t, srv, "imgedit@example.com", "10")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "img-test")
	_ = mw.WriteField("n", "2")
	_ = mw.WriteField("prompt", "add a hat")
	fw, _ := mw.CreateFormFile("image", "x.png")
	_, _ = fw.Write([]byte("PNGDATA"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("edits = %d %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/images/edits" {
		t.Fatalf("upstream path = %s, want /images/edits", gotPath)
	}
	if gotModel != "img-test" {
		t.Fatalf("upstream model = %s, want img-test", gotModel)
	}
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.92")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after 2-image edit: posted=%s held=%s, want 9.92/0", acc.PostedBalance, acc.HeldAmount)
	}
}
