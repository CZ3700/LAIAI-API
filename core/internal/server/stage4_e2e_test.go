package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

func TestStage4ReasoningSuffixThinkingToContentAndZstdE2E(t *testing.T) {
	srv, _ := setupRelayServer(t)

	var seenModel string
	var seenEffort string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModel, _ = body["model"].(string)
		seenEffort, _ = body["reasoning_effort"].(string)

		respJSON := `{"id":"cmpl-zstd","choices":[{"message":{"role":"assistant","reasoning_content":"deliberate","content":"done"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
		var zbuf bytes.Buffer
		zw, _ := zstd.NewWriter(&zbuf)
		_, _ = zw.Write([]byte(respJSON))
		_ = zw.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = w.Write(zbuf.Bytes())
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "mock-zstd-ttc", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream-secret", "models": []string{"test-model"},
		"thinking_to_content": true,
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("upsert price = %d %v", w.Code, out)
	}

	apiKey, _ := seedUserWithCredit(t, srv, "stage4-zstd@example.com", "10")
	w, out = srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model":    "test-model-high",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("chat = %d %v raw=%s", w.Code, out, w.Body.String())
	}
	if seenModel != "test-model" || seenEffort != "high" {
		t.Fatalf("upstream got model=%q effort=%q, want test-model/high", seenModel, seenEffort)
	}
	choices, _ := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if _, hasRC := msg["reasoning_content"]; hasRC {
		t.Fatalf("reasoning_content should be folded by thinking_to_content: %v", msg)
	}
	if got, _ := msg["content"].(string); got != "<think>deliberate</think>\n\ndone" {
		t.Fatalf("folded content = %q", got)
	}
}

func TestStage4AnthropicMultiTurnSignatureRoundtripE2E(t *testing.T) {
	srv, _ := setupRelayServer(t)

	var turn int32
	var turn2HadSig, turn2HadRedacted bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&turn, 1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n == 2 {
			msgs, _ := body["messages"].([]any)
			if len(msgs) >= 2 {
				assist := msgs[1].(map[string]any)
				if blocks, ok := assist["content"].([]any); ok {
					for _, rb := range blocks {
						b, _ := rb.(map[string]any)
						if b["type"] == "thinking" && b["signature"] == "sig_turn1" {
							turn2HadSig = true
						}
						if b["type"] == "redacted_thinking" && b["data"] == "cipher_turn1" {
							turn2HadRedacted = true
						}
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_ant_1","model":"claude-sonnet-4","stop_reason":"end_turn",
			"content":[
				{"type":"thinking","thinking":"internal reasoning","signature":"sig_turn1"},
				{"type":"redacted_thinking","data":"cipher_turn1"},
				{"type":"text","text":"final answer"}
			],
			"usage":{"input_tokens":15,"output_tokens":9}
		}`))
	}))
	defer up.Close()

	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ant-roundtrip", "provider": "anthropic", "base_url": up.URL,
		"api_key": "sk-ant-secret", "models": []string{"claude-sonnet-4"},
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "claude-sonnet-4", "input_usd_per_1m": "3", "output_usd_per_1m": "15",
	})
	if w.Code != 200 {
		t.Fatalf("upsert price = %d %v", w.Code, out)
	}

	apiKey, _ := seedUserWithCredit(t, srv, "stage4-ant@example.com", "10")
	w, out = srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "claude-sonnet-4", "max_tokens": 1024,
		"messages": []map[string]any{{"role": "user", "content": "q1"}},
	})
	if w.Code != 200 {
		t.Fatalf("turn 1 = %d %v", w.Code, out)
	}
	blocks, _ := out["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("turn 1 content blocks = %d, want 3: %v", len(blocks), blocks)
	}

	// Replay the assistant's thinking + signature + redacted_thinking back on turn 2.
	w, out = srv.do(t, "POST", "/anthropic/v1/messages", apiKey, map[string]any{
		"model": "claude-sonnet-4", "max_tokens": 1024,
		"messages": []map[string]any{
			{"role": "user", "content": "q1"},
			{"role": "assistant", "content": blocks},
			{"role": "user", "content": "q2"},
		},
	})
	if w.Code != 200 {
		t.Fatalf("turn 2 = %d %v", w.Code, out)
	}
	if !turn2HadSig || !turn2HadRedacted {
		t.Fatalf("turn 2 upstream missing blocks: sig=%v redacted=%v", turn2HadSig, turn2HadRedacted)
	}
}

func TestStage4StreamDisconnectGraceSettlementE2E(t *testing.T) {
	srv, db := setupRelayServer(t)

	oldGrace := relay.StreamDisconnectGrace
	relay.StreamDisconnectGrace = 250 * time.Millisecond
	defer func() { relay.StreamDisconnectGrace = oldGrace }()

	chunk1Sent := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		close(chunk1Sent)
		time.Sleep(45 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500}}\n\ndata: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer up.Close()

	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "stage4-grace@example.com", "10")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-chunk1Sent
		time.Sleep(10 * time.Millisecond)
		cancel() // simulate client disconnecting right after first chunk
	}()

	reqBody := bytes.NewBufferString(`{"model":"test-model","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBody).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)

	// Cost of 1000 in ($2/1M) + 500 out ($6/1M) = 0.005 -> remaining balance = 9.995, held_amount = 0.
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatalf("load account: %v", err)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held_amount = %s, want 0 (grace settlement must not leak hold)", acc.HeldAmount)
	}
	wantBal := decimal.RequireFromString("9.995")
	if !acc.PostedBalance.Equal(wantBal) {
		t.Fatalf("posted_balance = %s, want %s", acc.PostedBalance, wantBal)
	}
	inv := waitInvocation(t, db, "org_id = ? AND model = ?", uint64(orgID), "test-model")
	if inv.PromptTokens != 1000 || inv.CompletionTokens != 500 {
		t.Fatalf("invocation tokens = %d/%d, want 1000/500", inv.PromptTokens, inv.CompletionTokens)
	}
}

func TestStage4StickySessionAffinityAndTPMLimitE2E(t *testing.T) {
	srv, _ := setupRelayServer(t)

	var hitsA, hitsB int32
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsA, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer upA.Close()
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsB, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"b","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer upB.Close()

	for _, c := range []struct {
		name string
		url  string
	}{{"chA", upA.URL}, {"chB", upB.URL}} {
		w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
			"name": c.name, "provider": "openai", "base_url": c.url,
			"api_key": "sk-upstream", "models": []string{"test-model"},
			"priority": 10, "weight": 1,
		})
		if w.Code != 201 {
			t.Fatalf("create %s: %d %v", c.name, w.Code, out)
		}
	}
	w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price: %d %v", w.Code, out)
	}

	_, orgID := seedUserWithCredit(t, srv, "stage4-sticky@example.com", "10")
	// Login as that user to mint a key with a strict tpm_limit.
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{
		"email": "stage4-sticky@example.com", "password": "longenough1",
	})
	jwt := login["token"].(string)

	w, keyOut := srv.do(t, "POST", "/api/keys", jwt, map[string]any{"name": "unlim"})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create unlimited key: %d %v", w.Code, keyOut)
	}
	stickyKey := keyOut["key"].(string)

	for i := range 6 {
		body, _ := json.Marshal(map[string]any{
			"model":    "test-model",
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+stickyKey)
		req.Header.Set("X-Session-Id", "session-fixed-1")
		rec := httptest.NewRecorder()
		srv.engine.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("sticky run %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	a, b := atomic.LoadInt32(&hitsA), atomic.LoadInt32(&hitsB)
	if !((a == 6 && b == 0) || (a == 0 && b == 6)) {
		t.Fatalf("expected all 6 sticky requests on 1 channel for org %v, got A=%d B=%d", orgID, a, b)
	}

	// TPM limit key test.
	w, tpmKeyOut := srv.do(t, "POST", "/api/keys", jwt, map[string]any{"name": "tight-tpm", "tpm_limit": 20})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create tpm key: %d %v", w.Code, tpmKeyOut)
	}
	tightKey := tpmKeyOut["key"].(string)
	w1, out1 := srv.do(t, "POST", "/v1/chat/completions", tightKey, map[string]any{
		"model":    "test-model",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w1.Code != 200 {
		t.Fatalf("first tpm request = %d %v", w1.Code, out1)
	}
	w2, out2 := srv.do(t, "POST", "/v1/chat/completions", tightKey, map[string]any{
		"model":    "test-model",
		"messages": []map[string]string{{"role": "user", "content": strings.Repeat("token ", 20)}},
	})
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second tpm request = %d %v, want 429", w2.Code, out2)
	}
}
