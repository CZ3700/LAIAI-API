package video

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMySQLOfficialVideoResponseFormats 走真实 HTTP 栈断言任务接口的火山官方原生形状：
// 提交与幂等重放都是 {"id": ...}（重放不得退回平台全量 DTO），单查为官方 query 形状，
// 取消返回 id/status/operation_id。镜像 handler 的平行 helper 测不出重放路径的分歧。
func TestMySQLOfficialVideoResponseFormats(t *testing.T) {
	f := mysqlFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	r.GET("/v1/video/generations/:id", f.s.Middleware(), f.s.HandleGet)
	r.POST("/v1/video/generations/:id/cancel", f.s.Middleware(), f.s.HandleCancel)

	body := `{"model":"seedance-2.0","content":[{"type":"text","text":"official shape check"}]}`
	call := func(method, path, key string) *httptest.ResponseRecorder {
		var payload string
		if method == "POST" && path == "/v1/video/generations" {
			payload = body
		}
		req := httptest.NewRequest(method, path, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+f.plain)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(t *testing.T, raw string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("non-JSON body: %s", raw)
		}
		return out
	}

	// 1. 首次提交：官方 Submit 形状，仅 {"id": "task_..."}。
	first := call("POST", "/v1/video/generations", "official-shape-1")
	if first.Code != 202 {
		t.Fatalf("create %d: %s", first.Code, first.Body.String())
	}
	submit := decode(t, first.Body.String())
	if len(submit) != 1 || !strings.HasPrefix(submit["id"].(string), "task_") {
		t.Fatalf("submit must be official {id} only: %s", first.Body.String())
	}

	// 2. 同键重放：同一形状、同一 ID，不允许退回平台全量 DTO。
	replay := call("POST", "/v1/video/generations", "official-shape-1")
	if replay.Code != 202 {
		t.Fatalf("replay %d: %s", replay.Code, replay.Body.String())
	}
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay header missing")
	}
	replayBody := decode(t, replay.Body.String())
	if len(replayBody) != 1 || replayBody["id"] != submit["id"] {
		t.Fatalf("replay diverged from official submit shape: %s", replay.Body.String())
	}

	// 3. 单查：官方 Query 形状（id/model/status/created_at/updated_at，时间戳为 Unix 秒）。
	detail := call("GET", "/v1/video/generations/"+submit["id"].(string), "")
	if detail.Code != 200 {
		t.Fatalf("get %d: %s", detail.Code, detail.Body.String())
	}
	query := decode(t, detail.Body.String())
	for _, field := range []string{"id", "model", "status", "created_at", "updated_at"} {
		if _, ok := query[field]; !ok {
			t.Fatalf("official query missing %s: %s", field, detail.Body.String())
		}
	}
	if query["model"] != "seedance-2.0" {
		t.Fatalf("model mismatch: %s", detail.Body.String())
	}
	if _, isNumber := query["created_at"].(float64); !isNumber {
		t.Fatalf("created_at must be unix seconds: %s", detail.Body.String())
	}
	for _, legacy := range []string{"billing_state", "sync_state", "artifact_status", "deletion_state", "last_synced_at"} {
		if _, leaked := query[legacy]; leaked {
			t.Fatalf("platform DTO field %s leaked into official query: %s", legacy, detail.Body.String())
		}
	}

	// 4. 取消：官方形状 + operation_id，完成与否以 operations 资源为准。
	cancel := call("POST", "/v1/video/generations/"+submit["id"].(string)+"/cancel", "official-cancel-1")
	if cancel.Code != 202 {
		t.Fatalf("cancel %d: %s", cancel.Code, cancel.Body.String())
	}
	cancelled := decode(t, cancel.Body.String())
	if len(cancelled) != 3 || cancelled["status"] != "cancelling" || !strings.HasPrefix(cancelled["operation_id"].(string), "op_") {
		t.Fatalf("cancel must be {id,status:cancelling,operation_id}: %s", cancel.Body.String())
	}
}
