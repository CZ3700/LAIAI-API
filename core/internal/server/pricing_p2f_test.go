package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

// doRawBody：发原始字节请求（自定义 Content-Type + Bearer），返回 recorder。
func (g *gin999) doRawBody(t *testing.T, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	g.engine.ServeHTTP(w, req)
	return w
}

// buildMultipart：构造含 file + model 字段的 multipart body，返回 (body, content-type)。
func buildMultipart(t *testing.T, model string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", model)
	fw, _ := mw.CreateFormFile("file", "audio.mp3")
	_, _ = fw.Write([]byte("FAKEAUDIO"))
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

// 二层分组倍率：EffectiveRatio = 分组倍率 × (分组,模型) 倍率，各层缺省回退 1。
func TestEffectiveRatioTwoLayer(t *testing.T) {
	srv, db := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "tl@gmail.com", "10")
	oid := uint64(orgID)
	db.Exec(`UPDATE organizations SET group_name='vip' WHERE id=?`, oid)
	db.Exec(`INSERT INTO group_ratios (name,ratio) VALUES ('vip','0.5')`)

	// 无二层覆盖 → 0.5 × 1 = 0.5
	if r := relay.EffectiveRatio(db, oid, "gpt-4"); !r.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("ratio without model override = %s, want 0.5", r)
	}
	// 加 (vip, gpt-4)=0.5 → 0.5 × 0.5 = 0.25
	db.Exec(`INSERT INTO model_group_ratios (group_name,model,ratio) VALUES ('vip','gpt-4','0.5')`)
	if r := relay.EffectiveRatio(db, oid, "gpt-4"); !r.Equal(decimal.RequireFromString("0.25")) {
		t.Fatalf("two-layer ratio = %s, want 0.25", r)
	}
	// 其他模型仍只受一层影响 → 0.5
	if r := relay.EffectiveRatio(db, oid, "gpt-5"); !r.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("other model ratio = %s, want 0.5", r)
	}
}

// 音频 TTS 端到端：按字符计价，余额按 chars×price/1000 × ratio 扣减。
func TestAudioSpeechBilling(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio/speech") {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("FAKEAUDIObytes"))
			return
		}
		w.WriteHeader(404)
	}))
	defer up.Close()

	cred, _ := testKeyring(t).Encrypt("sk-x")
	db.Exec(`INSERT INTO channels (name,provider,base_url,credential_enc,models,status) VALUES ('c','openai',?,?,'tts-1',1)`, up.URL, cred)
	db.Exec(`INSERT INTO model_prices (model,input_usd_per_1m,output_usd_per_1m,tts_usd_per_1k_chars) VALUES ('tts-1','0','0','0.015')`)
	apiKey, orgID := seedUserWithCredit(t, srv, "tts@gmail.com", "10")

	// input "hello world" = 11 chars → 11×0.015/1000 = 0.000165
	w, _ := srv.do(t, "POST", "/v1/audio/speech", apiKey, map[string]any{"model": "tts-1", "input": "hello world"})
	if w.Code != 200 {
		t.Fatalf("tts = %d", w.Code)
	}
	if w.Body.String() != "FAKEAUDIObytes" {
		t.Fatalf("audio bytes not passed through: %q", w.Body.String())
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	want := decimal.RequireFromString("10").Sub(decimal.RequireFromString("0.000165"))
	if !acc.PostedBalance.Equal(want) {
		t.Fatalf("balance after tts = %s, want %s", acc.PostedBalance, want)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0", acc.HeldAmount)
	}
}

// 音频转录端到端：multipart(file+model)，按次固定价。
func TestAudioTranscriptionBilling(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer up.Close()

	cred, _ := testKeyring(t).Encrypt("sk-x")
	db.Exec(`INSERT INTO channels (name,provider,base_url,credential_enc,models,status) VALUES ('c','openai',?,?,'whisper-1',1)`, up.URL, cred)
	db.Exec(`INSERT INTO model_prices (model,input_usd_per_1m,output_usd_per_1m,transcription_usd_per_call) VALUES ('whisper-1','0','0','0.006')`)
	apiKey, orgID := seedUserWithCredit(t, srv, "stt@gmail.com", "10")

	// 构造 multipart：file + model 字段
	body, ct := buildMultipart(t, "whisper-1")
	w := srv.doRawBody(t, "POST", "/v1/audio/transcriptions", apiKey, ct, body)
	if w.Code != 200 {
		t.Fatalf("transcription = %d %s", w.Code, w.Body.String())
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	want := decimal.RequireFromString("9.994") // 10 - 0.006 固定
	if !acc.PostedBalance.Equal(want) {
		t.Fatalf("balance after transcription = %s, want %s", acc.PostedBalance, want)
	}
}

// 二层分组倍率 admin CRUD。
func TestAdminModelGroupRatioCRUD(t *testing.T) {
	srv, _ := setupRelayServer(t)
	if w, _ := srv.doAdmin(t, "POST", "/api/admin/model-group-ratios", map[string]any{
		"group_name": "vip", "model": "gpt-4", "ratio": "0.5",
	}); w.Code != 200 {
		t.Fatalf("upsert mgr = %d", w.Code)
	}
	_, list := srv.doAdmin(t, "GET", "/api/admin/model-group-ratios", nil)
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("mgr list = %v, want 1", items)
	}
	if w, _ := srv.doAdmin(t, "DELETE", "/api/admin/model-group-ratios?group_name=vip&model=gpt-4", nil); w.Code != 200 {
		t.Fatalf("delete mgr = %d", w.Code)
	}
	_, list = srv.doAdmin(t, "GET", "/api/admin/model-group-ratios", nil)
	if items := list["items"].([]any); len(items) != 0 {
		t.Fatalf("mgr list after delete = %v, want 0", items)
	}
}
