package relay

// Diagnostics CheckKey 单测（§4.8）：核心探测 + 红线 7（key 绝不入日志/结果）+ provider 白名单 + 截断。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const diagTestKey = "sk-diag-super-secret-9f3a"

func newDiagRelay(buf *bytes.Buffer) *Relay {
	return &Relay{Now: time.Now, Log: slog.New(slog.NewTextHandler(buf, nil))}
}

// mockProvider：/models 端点，校验 Authorization 带正确 key；nModels 控制返回模型数、status 非 200 则拒。
func mockProvider(t *testing.T, wantKey string, nModels, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		ids := make([]string, nModels)
		for i := range ids {
			ids[i] = fmt.Sprintf(`{"id":"m-%d"}`, i)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(ids, ","))
	}))
}

// assertNoKeyLeak：结果任何字符串字段 + 全部日志都不得含被测 key（红线 7）。
func assertNoKeyLeak(t *testing.T, res *DiagResult, logs string, key string) {
	t.Helper()
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), key) {
		t.Fatalf("RED LINE 7: key leaked into DiagResult: %s", blob)
	}
	if strings.Contains(logs, key) {
		t.Fatalf("RED LINE 7: key leaked into logs: %s", logs)
	}
}

func TestCheckKeyValid(t *testing.T) {
	ByokAllowPrivateForTest = true // mock 在 127.0.0.1
	t.Cleanup(func() { ByokAllowPrivateForTest = false })
	var buf bytes.Buffer
	r := newDiagRelay(&buf)
	up := mockProvider(t, diagTestKey, 3, 200)
	defer up.Close()

	res := r.CheckKey(context.Background(), diagTestKey, up.URL, "openai")
	if !res.OK || res.Status != "ok" || res.ModelCount != 3 {
		t.Fatalf("valid check = %+v", res)
	}
	if res.LatencyMs < 0 {
		t.Fatalf("latency = %d", res.LatencyMs)
	}
	assertNoKeyLeak(t, res, buf.String(), diagTestKey)
}

func TestCheckKeyInvalid(t *testing.T) {
	ByokAllowPrivateForTest = true
	t.Cleanup(func() { ByokAllowPrivateForTest = false })
	var buf bytes.Buffer
	r := newDiagRelay(&buf)
	up := mockProvider(t, "the-real-key", 0, 200) // 校验 key 不匹配 → 401
	defer up.Close()

	res := r.CheckKey(context.Background(), diagTestKey, up.URL, "openai")
	if res.OK || res.Status != "invalid_key" {
		t.Fatalf("invalid check = %+v, want status invalid_key", res)
	}
	assertNoKeyLeak(t, res, buf.String(), diagTestKey) // 即便被拒，key 也绝不入日志/结果
}

func TestCheckKeyUnreachable(t *testing.T) {
	ByokAllowPrivateForTest = true
	t.Cleanup(func() { ByokAllowPrivateForTest = false })
	var buf bytes.Buffer
	r := newDiagRelay(&buf)
	// 立即关闭的 server → 拨号失败
	up := mockProvider(t, diagTestKey, 1, 200)
	up.Close()

	res := r.CheckKey(context.Background(), diagTestKey, up.URL, "openai")
	if res.OK || res.Status != "unreachable" {
		t.Fatalf("unreachable check = %+v, want status unreachable", res)
	}
	assertNoKeyLeak(t, res, buf.String(), diagTestKey)
}

func TestCheckKeyProviderWhitelist(t *testing.T) {
	var buf bytes.Buffer
	r := newDiagRelay(&buf)
	// advanced_custom 不在白名单 → 立即拒，不拨号
	res := r.CheckKey(context.Background(), diagTestKey, "https://x.example/v1", "advanced_custom")
	if res.OK || res.Status != "error" || !strings.Contains(res.Message, "not supported") {
		t.Fatalf("whitelist check = %+v, want error not-supported", res)
	}
	assertNoKeyLeak(t, res, buf.String(), diagTestKey)
}

func TestCheckKeyModelsSampleTruncation(t *testing.T) {
	ByokAllowPrivateForTest = true
	t.Cleanup(func() { ByokAllowPrivateForTest = false })
	var buf bytes.Buffer
	r := newDiagRelay(&buf)
	up := mockProvider(t, diagTestKey, 50, 200) // 50 个模型
	defer up.Close()

	res := r.CheckKey(context.Background(), diagTestKey, up.URL, "openai")
	if res.ModelCount != 50 {
		t.Fatalf("model count = %d, want 50", res.ModelCount)
	}
	if len(res.ModelsSample) != diagMaxModelsSample {
		t.Fatalf("sample len = %d, want %d (truncated)", len(res.ModelsSample), diagMaxModelsSample)
	}
}
