package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	r.GET("/v1/video/generations", f.s.Middleware(), f.s.HandleList)
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

	// 3. 让真实上游 Query 回执携带官方可选参数，再经 HTTP 单查验证透传。
	f.dispatch(t, submit["id"].(string))
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return reply(200, `{"id":"`+f.upstream(t, submit["id"].(string))+`","model":"doubao-seedance-2-0-260128","status":"running","created_at":1779348818,"updated_at":1779348874,"seed":78674,"resolution":"720p","ratio":"16:9","duration":5,"framespersecond":24,"service_tier":"default","execution_expires_after":172800,"generate_audio":true,"draft":false,"priority":0}`), nil
		}
		return nil, errors.New("unexpected fixture request")
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, submit["id"].(string)))

	// 4. 单查：官方 Query 形状含可选字段，时间戳为 Unix 秒整数。
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
	if query["created_at"] != float64(1779348818) || query["updated_at"] != float64(1779348874) {
		t.Fatalf("upstream Unix-second timestamps were not preserved: %s", detail.Body.String())
	}
	want := map[string]any{"seed": float64(78674), "resolution": "720p", "ratio": "16:9", "duration": float64(5), "framespersecond": float64(24), "service_tier": "default", "execution_expires_after": float64(172800), "generate_audio": true, "draft": false, "priority": float64(0)}
	for field, value := range want {
		if query[field] != value {
			t.Fatalf("official field %s = %#v, want %#v: %s", field, query[field], value, detail.Body.String())
		}
	}
	for _, legacy := range []string{"billing_state", "sync_state", "artifact_status", "deletion_state", "last_synced_at"} {
		if _, leaked := query[legacy]; leaked {
			t.Fatalf("platform DTO field %s leaked into official query: %s", legacy, detail.Body.String())
		}
	}
	listed := decode(t, call("GET", "/v1/video/generations", "").Body.String())
	listTask := listed["data"].([]any)[0].(map[string]any)
	for _, field := range []string{"seed", "resolution", "ratio", "duration", "frames", "framespersecond", "service_tier", "execution_expires_after", "generate_audio", "draft", "priority"} {
		if _, leaked := listTask[field]; leaked {
			t.Fatalf("official-only field %s leaked into platform list DTO: %#v", field, listTask)
		}
	}

	// A later applied observation without optional values must not retain values
	// from the earlier receipt.
	makeFixtureTaskQueryDue(t, f, submit["id"].(string))
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodGet {
			return reply(200, `{"id":"`+f.upstream(t, submit["id"].(string))+`","model":"doubao-seedance-2-0-260128","status":"running"}`), nil
		}
		return nil, errors.New("unexpected fixture request")
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, submit["id"].(string)))
	detail = call("GET", "/v1/video/generations/"+submit["id"].(string), "")
	query = decode(t, detail.Body.String())
	for _, field := range []string{"seed", "resolution", "ratio", "duration", "frames", "framespersecond", "service_tier", "execution_expires_after", "generate_audio", "draft", "priority"} {
		if _, present := query[field]; present {
			t.Fatalf("omitted optional field %s remained in query response: %s", field, detail.Body.String())
		}
	}

	// 5. 取消：另建仍处于 queued 的任务验证官方回执形状。
	f.response = nil
	cancelledCreate := call("POST", "/v1/video/generations", "official-cancel-task")
	cancelTask := decode(t, cancelledCreate.Body.String())
	f.dispatch(t, cancelTask["id"].(string))
	cancel := call("POST", "/v1/video/generations/"+cancelTask["id"].(string)+"/cancel", "official-cancel-1")
	if cancel.Code != 202 {
		t.Fatalf("cancel %d: %s", cancel.Code, cancel.Body.String())
	}
	cancelled := decode(t, cancel.Body.String())
	if len(cancelled) != 3 || cancelled["status"] != "cancelling" || !strings.HasPrefix(cancelled["operation_id"].(string), "op_") {
		t.Fatalf("cancel must be {id,status:cancelling,operation_id}: %s", cancel.Body.String())
	}
}
