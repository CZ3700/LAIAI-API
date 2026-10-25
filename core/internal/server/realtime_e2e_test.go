package server

// Realtime WS relay e2e（§3.9）：mock 上游 WS + 真实 listener（WS 需 hijack，不能用 ResponseRecorder）。
// 覆盖：握手鉴权（Bearer + Sec-WebSocket-Protocol 两式）、response.done 权威 usage 逐轮计费、
// 余额预检 402、会话中余额不足→error 事件+拆连（不透支）、上游断连、心跳 idle 超时。
// 真机形态验证走 env 门控 smoke（真实 OpenAI Realtime key 待补）。

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

// httptest.Server.Close does not wait for hijacked WebSocket handlers. Track
// the outer handler until pumpRealtime has joined both relay directions. A
// failed drain fails the test and deliberately does not run afterDrain (which
// may restore a package variable still being read by the unfinished pump).
func newRealtimeTestServer(t *testing.T, handler http.Handler, afterDrain ...func()) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var handlers sync.WaitGroup
	sealed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if sealed {
			mu.Unlock()
			http.Error(w, "test server closing", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		mu.Unlock()
		defer handlers.Done()
		// Preserve the original ResponseWriter's Hijacker/Flusher interfaces.
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		mu.Lock()
		sealed = true
		mu.Unlock()
		done := make(chan struct{})
		go func() { srv.Close(); handlers.Wait(); close(done) }()
		select {
		case <-done:
			for _, restore := range afterDrain {
				restore()
			}
		case <-time.After(5 * time.Second):
			t.Error("realtime test handler did not finish; package settings were not restored")
		}
	})
	return srv
}

// newMockRealtimeUpstream：模拟 OpenAI Realtime 上游 WS。onRecv 决定每收到一帧后回什么。
func newMockRealtimeUpstream(t *testing.T, onRecv func(recv []byte) (reply string, closeAfter bool)) *httptest.Server {
	t.Helper()
	up := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return newRealtimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			reply, closeAfter := onRecv(msg)
			if reply != "" {
				_ = c.WriteMessage(gws.TextMessage, []byte(reply))
			}
			if closeAfter {
				_ = c.WriteMessage(gws.CloseMessage, gws.FormatCloseMessage(gws.CloseNormalClosure, ""))
				return
			}
		}
	}))
}

// setupRealtimeChannel：openai 渠道指向 mock 上游 + realtime 模型定价（含音频价）。
func setupRealtimeChannel(t *testing.T, srv *gin999, upURL, model string) {
	t.Helper()
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "rt", "provider": "openai", "base_url": upURL,
		"api_key": "sk-upstream", "models": []string{model},
	}); w.Code != 201 {
		t.Fatalf("create rt channel = %d %v", w.Code, out)
	}
	// 价：text in=10/out=30，audio in=100/out=200（每 1M）
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": model, "input_usd_per_1m": "10", "output_usd_per_1m": "30",
		"audio_input_usd_per_1m": "100", "audio_output_usd_per_1m": "200",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
}

func wsURL(httpURL, path string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + path
}

// 一轮 response.done（含权威 usage）：text_in=1000/out=1000, audio_in=10000/out=10000。
const rtDoneFixture = `{"type":"response.done","response":{"usage":{` +
	`"input_token_details":{"text_tokens":1000,"audio_tokens":10000,"cached_tokens":0},` +
	`"output_token_details":{"text_tokens":1000,"audio_tokens":10000}}}}`

// 期望 cost = 1000*10/1M + 1000*30/1M + 10000*100/1M + 10000*200/1M = 0.01+0.03+1+2 = 3.04

func TestRealtimeHappyPathBilling(t *testing.T) {
	srv, db := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return rtDoneFixture, false })
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	apiKey, orgID := seedUserWithCredit(t, srv, "rt@example.com", "10")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	cli, resp, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err != nil {
		t.Fatalf("dial = %v (resp %v)", err, resp)
	}
	defer cli.Close()

	// 客户端发一帧 → LAIAI 转发上游 → 上游回 response.done → LAIAI 计费后转发给客户端
	if err := cli.WriteMessage(gws.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := cli.ReadMessage()
	if err != nil {
		t.Fatalf("client read = %v", err)
	}
	if !strings.Contains(string(msg), "response.done") {
		t.Fatalf("client got %q, want response.done", msg)
	}
	// 计费在转发给客户端前已提交（同 goroutine 顺序）：读到 done 即已扣费
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("6.96")) {
		t.Fatalf("balance after 1 turn = %s, want 6.96 (10 - 3.04)", acc.PostedBalance)
	}

	// 再来一轮：累计再扣 3.04 → 3.92
	_ = cli.WriteMessage(gws.TextMessage, []byte(`{"type":"response.create"}`))
	_, _, _ = cli.ReadMessage()
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("3.92")) {
		t.Fatalf("balance after 2 turns = %s, want 3.92", acc.PostedBalance)
	}
}

// 浏览器式握手：key 走 Sec-WebSocket-Protocol 的 openai-insecure-api-key. 段。
func TestRealtimeSubprotocolAuth(t *testing.T) {
	srv, _ := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return rtDoneFixture, false })
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	apiKey, _ := seedUserWithCredit(t, srv, "rt-sub@example.com", "10")

	d := gws.Dialer{Subprotocols: []string{"realtime", "openai-insecure-api-key." + apiKey, "openai-beta.realtime-v1"}}
	cli, resp, err := d.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), nil)
	if err != nil {
		t.Fatalf("subprotocol dial = %v (resp %v)", err, resp)
	}
	defer cli.Close()
	// 回的子协议只能是 realtime（绝不回带 key 的 openai-insecure-api-key.*）
	if resp.Header.Get("Sec-WebSocket-Protocol") != "realtime" {
		t.Fatalf("negotiated subprotocol = %q, want realtime only", resp.Header.Get("Sec-WebSocket-Protocol"))
	}
	if strings.Contains(resp.Header.Get("Sec-WebSocket-Protocol"), apiKey) {
		t.Fatal("api key leaked back in Sec-WebSocket-Protocol")
	}
}

// 建连预检：余额为 0 → 402，不 upgrade。
func TestRealtimeInsufficientBalancePreCheck(t *testing.T) {
	srv, _ := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return "", false })
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	apiKey, _ := seedUserWithCredit(t, srv, "rt-broke@example.com", "") // 无授信 → 零余额

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	_, resp, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err == nil {
		t.Fatal("dial should fail on zero balance")
	}
	if resp == nil || resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("pre-check status = %v, want 402", resp)
	}
}

// fail-closed：realtime 模型未配音频价 → 建连 400 拒绝（防音频按文本价漏收 10-20×，对抗 review）。
func TestRealtimeRequiresAudioPricing(t *testing.T) {
	srv, _ := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return "", false })
	defer up.Close()
	// 渠道 + 只配文本价（无 audio_*）
	if w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "rt-noaudio", "provider": "openai", "base_url": up.URL,
		"api_key": "sk-upstream", "models": []string{"rt-text-only"},
	}); w.Code != 201 {
		t.Fatalf("channel = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "rt-text-only", "input_usd_per_1m": "10", "output_usd_per_1m": "30", // 无音频价
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	apiKey, _ := seedUserWithCredit(t, srv, "rt-noaudio@example.com", "10")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	_, resp, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=rt-text-only"), h)
	if err == nil {
		t.Fatal("connect should be rejected when audio price is unconfigured")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want 400 (fail-closed audio pricing)", resp)
	}
}

// 会话中余额不足：一轮 cost > 余额 → 扣费失败 → error 事件 + 拆连；余额不透支（不为负）。
func TestRealtimeMidSessionInsufficientNoOverdraft(t *testing.T) {
	srv, db := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return rtDoneFixture, false })
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	// 只充 1（正余额过预检），但一轮 cost=3.04 > 1 → 扣费失败
	apiKey, orgID := seedUserWithCredit(t, srv, "rt-thin@example.com", "1")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	cli, _, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	defer cli.Close()
	_ = cli.WriteMessage(gws.TextMessage, []byte(`{"type":"response.create"}`))
	// 读到的应是 error 事件（扣费失败回写），随后连接被关
	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := cli.ReadMessage()
	if err == nil && !strings.Contains(string(msg), `"error"`) {
		t.Fatalf("expected error event, got %q", msg)
	}
	// 关键：余额不透支——扣费失败即不扣，仍为 1（绝不为负）
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("balance = %s, want 1 (charge failed → no debit, no overdraft)", acc.PostedBalance)
	}
	if acc.PostedBalance.IsNegative() {
		t.Fatal("balance went negative — overdraft red line")
	}
}

// 上游主动断连 → 客户端连接联动关闭。
func TestRealtimeUpstreamClose(t *testing.T) {
	srv, _ := setupRelayServer(t)
	laiai := newRealtimeTestServer(t, srv.engine)
	up := newMockRealtimeUpstream(t, func([]byte) (string, bool) { return rtDoneFixture, true /* closeAfter */ })
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	apiKey, _ := seedUserWithCredit(t, srv, "rt-upclose@example.com", "10")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	cli, _, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	defer cli.Close()
	_ = cli.WriteMessage(gws.TextMessage, []byte(`{"type":"response.create"}`))
	// 上游回一帧后关闭 → LAIAI 联动关客户端。读到 done 后下一次读应报关闭错误。
	_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := cli.ReadMessage(); err != nil {
		t.Fatalf("first read = %v", err)
	}
	if _, _, err := cli.ReadMessage(); err == nil {
		t.Fatal("client should see connection close after upstream close")
	}
}

// 写截止防 DoS（对抗 review CONFIRMED HIGH）：客户端只写不读把 TCP 缓冲填满时，
// LAIAI 的转发写不得永久阻塞——写截止到期即拆连、goroutine 收敛（非 idle/ctx 路径）。
func TestRealtimeWriteDeadlineDoS(t *testing.T) {
	srv, _ := setupRelayServer(t)
	oldW := relay.RealtimeWriteWait
	relay.RealtimeWriteWait = 200 * time.Millisecond
	laiai := newRealtimeTestServer(t, srv.engine, func() { relay.RealtimeWriteWait = oldW })
	// 上游收到触发帧后猛灌大帧；LAIAI 转发给"只写不读"的客户端 → 客户端缓冲填满 → LAIAI 写阻塞。
	// LAIAI 写截止（200ms）到期 → closeBoth → upConn 关 → 上游写报错 → close(broke)。
	broke := make(chan struct{})
	up := newRealtimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upg := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := upg.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
		big := make([]byte, 256<<10) // 256KB/帧
		for i := range big {
			big[i] = 'x'
		}
		for {
			if err := c.WriteMessage(gws.TextMessage, big); err != nil {
				close(broke) // LAIAI 拆连后写报错
				return
			}
		}
	}))
	defer up.Close()
	setupRealtimeChannel(t, srv, up.URL, "gpt-realtime")
	apiKey, _ := seedUserWithCredit(t, srv, "rt-dos@example.com", "10")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	cli, _, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	defer cli.Close()
	_ = cli.WriteMessage(gws.TextMessage, []byte(`{"type":"response.create"}`))
	// 客户端故意不读；写阻塞应在写截止（200ms）内被打破 → 会话拆除（远早于 idle 60s / ping 20s）。
	select {
	case <-broke:
		// ok：写阻塞被写截止打破，会话收敛
	case <-time.After(4 * time.Second):
		t.Fatal("write-blocked session was NOT torn down by write deadline (DoS goroutine leak)")
	}
}

// 心跳 idle 超时：上游假死（upgrade 后不读不写不 pong）→ idle 超时强制关闭（补 New API 缺口）。
func TestRealtimeIdleTimeout(t *testing.T) {
	srv, _ := setupRelayServer(t)
	old := relay.RealtimeIdleTimeout
	relay.RealtimeIdleTimeout = 300 * time.Millisecond
	laiai := newRealtimeTestServer(t, srv.engine, func() { relay.RealtimeIdleTimeout = old })
	// 假死上游：upgrade 后阻塞，永不读/写/pong
	stopDead := make(chan struct{})
	defer close(stopDead)
	deadUp := newRealtimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		<-stopDead // Remains completely idle until test cleanup, without leaking a goroutine.
	}))
	setupRealtimeChannel(t, srv, deadUp.URL, "gpt-realtime")
	apiKey, _ := seedUserWithCredit(t, srv, "rt-idle@example.com", "10")

	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	cli, _, err := gws.DefaultDialer.Dial(wsURL(laiai.URL, "/v1/realtime?model=gpt-realtime"), h)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	defer cli.Close()
	// 双方都不发帧；LAIAI 的上游读超时（300ms）应在 ~2s 内触发→联动关客户端。
	_ = cli.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := cli.ReadMessage(); err == nil {
		t.Fatal("idle session should be closed by idle-timeout")
	} else {
		var deadline net.Error
		if errors.As(err, &deadline) && deadline.Timeout() {
			t.Fatal("client's own read deadline expired; server idle teardown was not observed")
		}
	}
}
