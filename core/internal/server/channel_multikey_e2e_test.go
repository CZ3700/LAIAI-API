package server

// Multi-key Channel e2e（指令 §3.5）：
// 一个渠道挂多把 key；random/polling 轮换；401 只禁当前 key 不误杀渠道；全 key 禁用才禁渠道；
// legacy 单 key 向后兼容；admin 增/禁/删 key + 明文/密文永不回显。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

// waitFor：轮询等待条件成立（async key/channel 禁用是 goroutine）。mustHold=true 时未达即 Fatal。
func waitFor(t *testing.T, cond func() bool, mustHold bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if mustHold {
		t.Fatal("condition not met within timeout")
	}
}

// multiKeyUpstream：按 Authorization 里的 key 记录命中次数；badKeys 集合里的 key 返回 401。
type multiKeyUpstream struct {
	srv     *httptest.Server
	mu      sync.Mutex
	hits    map[string]int
	badKeys map[string]bool
}

func newMultiKeyUpstream(t *testing.T) *multiKeyUpstream {
	t.Helper()
	m := &multiKeyUpstream{hits: map[string]int{}, badKeys: map[string]bool{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		m.mu.Lock()
		m.hits[key]++
		bad := m.badKeys[key]
		m.mu.Unlock()
		if bad {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"invalid api key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *multiKeyUpstream) hitsFor(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[key]
}
func (m *multiKeyUpstream) markBad(key string) {
	m.mu.Lock()
	m.badKeys[key] = true
	m.mu.Unlock()
}

// seedMultiKeyChannel：建渠道（首 key）+ 追加 keys + 定价，返回 channelID。
func seedMultiKeyChannel(t *testing.T, srv *gin999, baseURL, mode string, keys []string) uint64 {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "mk", "provider": "openai", "base_url": baseURL,
		"api_key": keys[0], "models": []string{"test-model"}, "credential_mode": mode,
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	chID := uint64(out["id"].(float64))
	for _, k := range keys[1:] {
		w, out = srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%d/credentials", chID), map[string]any{"api_key": k})
		if w.Code != 201 {
			t.Fatalf("add key = %d %v", w.Code, out)
		}
	}
	w, out = srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "test-model", "input_usd_per_1m": "2", "output_usd_per_1m": "6",
	})
	if w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	return chID
}

func activeKeyCount(t *testing.T, db *gorm.DB, chID uint64) int64 {
	t.Helper()
	var n int64
	db.Model(&relay.ChannelCredential{}).Where("channel_id = ? AND status = ?", chID, relay.CredentialActive).Count(&n)
	return n
}

// polling 轮换：3 key 均匀命中。
func TestMultiKeyPollingRotation(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	keys := []string{"sk-a", "sk-b", "sk-c"}
	seedMultiKeyChannel(t, srv, up.srv.URL, "polling", keys)
	apiKey, _ := seedUserWithCredit(t, srv, "mk-poll@example.com", "100")

	for i := 0; i < 9; i++ {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "max_tokens": 10,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
		if w.Code != 200 {
			t.Fatalf("req %d = %d", i, w.Code)
		}
	}
	for _, k := range keys {
		if h := up.hitsFor(k); h != 3 {
			t.Fatalf("polling: key %s hit %d, want 3", k, h)
		}
	}
}

// 401 只禁当前 key，不禁渠道；其余 key 继续服务。
func TestMultiKey401DisablesKeyNotChannel(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	// 开启自动禁用 + 阈值 1（一次 401 即禁该 key）
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('channel_auto_disable', 'true')")
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('channel_disable_threshold', '1')")
	keys := []string{"sk-good", "sk-bad"}
	chID := seedMultiKeyChannel(t, srv, up.srv.URL, "polling", keys)
	apiKey, _ := seedUserWithCredit(t, srv, "mk-401@example.com", "100")
	up.markBad("sk-bad")

	// 反复请求：坏 key 命中会 401（该请求内换渠道无其它渠道 → 透传失败），但坏 key 被禁后
	// 后续只命中好 key，全部 200。跑足够多次让轮换必然踩到坏 key 并触发禁用。
	got200 := 0
	for i := 0; i < 12; i++ {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "max_tokens": 10,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
		if w.Code == 200 {
			got200++
		}
		waitFor(t, func() bool { return activeKeyCount(t, db, chID) == 1 }, false) // 坏 key 迟早被禁
	}
	// 坏 key 被禁 → 渠道仍有 1 个 active key，渠道未被禁
	if n := activeKeyCount(t, db, chID); n != 1 {
		t.Fatalf("active keys = %d, want 1 (bad key disabled)", n)
	}
	var chStatus int8
	db.Raw("SELECT status FROM channels WHERE id = ?", chID).Scan(&chStatus)
	if chStatus != 1 {
		t.Fatalf("channel status = %d, want 1 (must NOT be disabled — good key remains)", chStatus)
	}
	if got200 == 0 {
		t.Fatal("no request succeeded — good key never served")
	}
	// 坏 key 禁用后，再来一批必然全绿（只剩好 key）
	for i := 0; i < 5; i++ {
		w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "max_tokens": 10,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
		if w.Code != 200 {
			t.Fatalf("post-disable req %d = %d (should route around bad key)", i, w.Code)
		}
	}
}

// 全部 key 被禁 → 渠道降级为禁用。
func TestMultiKeyAllKeysDownDisablesChannel(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('channel_auto_disable', 'true')")
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('channel_disable_threshold', '1')")
	keys := []string{"sk-x", "sk-y"}
	chID := seedMultiKeyChannel(t, srv, up.srv.URL, "polling", keys)
	apiKey, _ := seedUserWithCredit(t, srv, "mk-all@example.com", "100")
	up.markBad("sk-x")
	up.markBad("sk-y")

	chDisabled := func() bool {
		var s int8
		db.Raw("SELECT status FROM channels WHERE id = ?", chID).Scan(&s)
		return s == 2
	}
	for i := 0; i < 20 && !chDisabled(); i++ {
		srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
			"model": "test-model", "max_tokens": 10,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
	}
	// 直接等渠道状态（渠道禁用是双重异步：先禁 key 再禁 channel，等 key 计数=0 早于 channel 状态落库）。
	waitFor(t, chDisabled, true)
}

// 向后兼容：老渠道只有 channels.credential_enc（无子表行）仍正常服务（legacy fallback）。
func TestMultiKeyLegacyFallback(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	chID := seedMultiKeyChannel(t, srv, up.srv.URL, "random", []string{"sk-legacy"})
	// 模拟老数据：删掉子表行，只留 channels.credential_enc
	db.Exec("DELETE FROM channel_credentials WHERE channel_id = ?", chID)
	apiKey, _ := seedUserWithCredit(t, srv, "mk-legacy@example.com", "10")

	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 10,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("legacy fallback = %d %v", w.Code, out)
	}
	if up.hitsFor("sk-legacy") == 0 {
		t.Fatal("legacy credential_enc not used")
	}
}

// 对抗 review：被禁用的 key 绝不被 legacy credential_enc 镜像复活。
// 场景：单 key 渠道禁掉唯一 key → 渠道级联禁用；管理员企图重启用渠道应被拒（无 active key）。
func TestMultiKeyDisabledKeyNotResurrected(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	chID := seedMultiKeyChannel(t, srv, up.srv.URL, "random", []string{"sk-only"})
	// 取该 key id
	var creds []relay.ChannelCredential
	db.Where("channel_id = ?", chID).Find(&creds)
	cid := creds[0].ID

	// 禁用唯一 key → 渠道级联禁用（adminSetChannelKeyStatus）
	if w, _ := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%d/credentials/%d/disable", chID, cid), nil); w.Code != 200 {
		t.Fatalf("disable key = %d", w.Code)
	}
	var chStatus int8
	db.Raw("SELECT status FROM channels WHERE id = ?", chID).Scan(&chStatus)
	if chStatus != 2 {
		t.Fatalf("channel status = %d, want 2 (cascade-disabled)", chStatus)
	}
	// 企图重启用渠道 → 被拒（无 active key，否则会回退 credential_enc 复活被禁 key）
	if w, out := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%d/enable", chID), nil); w.Code != 400 {
		t.Fatalf("re-enable with no active key = %d %v, want 400", w.Code, out)
	}
	// 即便强改渠道 status=1（模拟旁路），选 key 也不回退到被禁 key（有子表行但全禁用）
	db.Exec("UPDATE channels SET status = 1 WHERE id = ?", chID)
	apiKey, _ := seedUserWithCredit(t, srv, "mk-resurrect@example.com", "10")
	before := up.hitsFor("sk-only")
	w, _ := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_tokens": 10,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if w.Code == 200 || up.hitsFor("sk-only") != before {
		t.Fatalf("disabled key resurrected via legacy fallback (code=%d, hits changed)", w.Code)
	}
}

// admin API：列 key 只回 fingerprint 不回明文/密文；删最后一把 active key 被拒。
func TestMultiKeyAdminAPI(t *testing.T) {
	srv, _ := setupRelayServer(t)
	up := newMultiKeyUpstream(t)
	chID := seedMultiKeyChannel(t, srv, up.srv.URL, "random", []string{"sk-first", "sk-second"})

	_, out := srv.doAdmin(t, "GET", fmt.Sprintf("/api/admin/channels/%d/credentials", chID), nil)
	raw, _ := json.Marshal(out)
	s := string(raw)
	if strings.Contains(s, "sk-first") || strings.Contains(s, "sk-second") {
		t.Fatalf("plaintext key leaked: %s", s)
	}
	if strings.Contains(s, "credential_enc") {
		t.Fatalf("ciphertext leaked: %s", s)
	}
	creds := out["credentials"].([]any)
	if len(creds) != 2 {
		t.Fatalf("credentials = %d, want 2", len(creds))
	}
	if creds[0].(map[string]any)["fingerprint"] != relay.CredentialFingerprint("sk-first") {
		t.Fatalf("fingerprint mismatch: %v", creds[0])
	}
	// 禁一把 → 剩 1 active；删最后一把 active 被拒
	cid1 := uint64(creds[0].(map[string]any)["id"].(float64))
	cid2 := uint64(creds[1].(map[string]any)["id"].(float64))
	if w, _ := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%d/credentials/%d/disable", chID, cid1), nil); w.Code != 200 {
		t.Fatalf("disable key = %d", w.Code)
	}
	if w, out := srv.doAdmin(t, "DELETE", fmt.Sprintf("/api/admin/channels/%d/credentials/%d", chID, cid2), nil); w.Code != 400 {
		t.Fatalf("delete last active key = %d %v, want 400", w.Code, out)
	}
	// 禁用的那把可以删
	if w, _ := srv.doAdmin(t, "DELETE", fmt.Sprintf("/api/admin/channels/%d/credentials/%d", chID, cid1), nil); w.Code != 200 {
		t.Fatalf("delete disabled key = %d", w.Code)
	}
}
