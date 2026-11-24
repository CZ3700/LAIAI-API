package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupStage2Server(t *testing.T) (*gin999, *gorm.DB, *task.Service, string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	for _, schema := range []string{testSchema, relayTestSchema, rulesAdminTestSchema} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('email_verification_enabled', 'false')")

	kr := testKeyring(t)
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)

	ts := task.NewService(db, kr, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	engine, srv := NewWithServer(db, Options{
		AuthSecret:    testSecret,
		AdminToken:    []byte(testAdminToken),
		Keyring:       kr,
		Recorder:      rec,
		TaskService:   ts,
		AuthRateBurst: 100000,
	})
	// 测试允许本地 mock httptest.Server 站点出网
	ts.Client = &http.Client{Timeout: 10 * time.Second}

	f := &gin999{engine: engine, srv: srv}
	f.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "stage2@test.com", "password": "longenough1"})
	_, login := f.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "stage2@test.com", "password": "longenough1"})
	sessionToken := login["token"].(string)
	orgID := login["org_id"].(float64)

	_, keyOut := f.do(t, "POST", "/api/keys", sessionToken, map[string]any{"name": "stage2-key"})
	apiKey := keyOut["key"].(string)

	wCredit, outCredit := f.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": orgID, "amount_usd": "100", "reason": "stage2 seed", "idempotency_key": "stage2-credit-1",
	})
	if wCredit.Code != http.StatusOK {
		t.Fatalf("seed credit: %d %v", wCredit.Code, outCredit)
	}
	return f, db, ts, sessionToken, apiKey
}

func doAPIKeyReq(t *testing.T, f *gin999, method, path, headerName, headerVal string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if headerName != "" {
		req.Header.Set(headerName, headerVal)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	return w
}

func TestStage2NativeRoutesAndRulesAndUnifiedLogs(t *testing.T) {
	f, db, ts, sessionToken, apiKey := setupStage2Server(t)

	// 记录 mock 上游接收到的请求路径与头
	var mu sync.Mutex
	var upstreamPaths []string
	var upstreamHeaders []http.Header
	taskPollCount := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamPaths = append(upstreamPaths, r.Method+" "+r.URL.Path)
		upstreamHeaders = append(upstreamHeaders, r.Header.Clone())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/custom/ark/chat"):
			_, _ = w.Write([]byte(`{
				"id":"chatcmpl-ark-1",
				"object":"chat.completion",
				"model":"doubao-pro-rule",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ark ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":100000,"completion_tokens":50000,"total_tokens":150000}
			}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents/generations/tasks"):
			_, _ = w.Write([]byte(`{"id":"up_seedance_001"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/generations/tasks/up_seedance_001"):
			mu.Lock()
			taskPollCount++
			n := taskPollCount
			mu.Unlock()
			if n == 1 {
				_, _ = w.Write([]byte(`{"status":"running"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"status":"succeeded",
				"content":{"video_url":"https://cdn.example.com/seedance.mp4"},
				"usage":{"completion_tokens":100000}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"unexpected mock route: ` + r.URL.Path + `"}}`))
		}
	}))
	defer upstream.Close()

	// 1. 配置渠道：openai 兼容 + upstream_cost_ratio=0.75
	wCh, outCh := f.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name":                "stage2-mock",
		"provider":            "openai",
		"base_url":            upstream.URL,
		"api_key":             "sk-upstream-secret",
		"models":              []string{"doubao-pro-rule", "gemini-rule", "claude-rule", "seedance-2-native"},
		"upstream_cost_ratio": "0.75",
	})
	if wCh.Code != http.StatusCreated {
		t.Fatalf("create channel: %d %v", wCh.Code, outCh)
	}

	// 2. 配置转发规则与计费规则（Chat: 全天候 0.5x 分时倍率; Seedance2: 720p with_video=28/1M）
	wChatFR, outChatFR := f.doAdmin(t, "POST", "/api/admin/forward-rules", map[string]any{
		"name":      "Ark Custom Chat Forward",
		"rule_type": "openai",
		"category":  "聊天",
		"config_json": `{
			"target_type": "openai",
			"auth_type": "x-api-key",
			"path_rewrites": [
				{"old": "/v1/chat/completions", "new": "/custom/ark/chat"},
				{"old": "/v1beta/models/${model}:generateContent", "new": "/custom/ark/chat"}
			],
			"path_accept": ["/api/v3/chat/completions", "/v1/messages"]
		}`,
	})
	if wChatFR.Code != http.StatusCreated {
		t.Fatalf("create chat forward rule: %d %v", wChatFR.Code, outChatFR)
	}
	chatFRID := uint64(outChatFR["rule"].(map[string]any)["id"].(float64))
	chatFREID := outChatFR["rule"].(map[string]any)["eid"].(string)

	wChatBR, outChatBR := f.doAdmin(t, "POST", "/api/admin/billing-rules", map[string]any{
		"name":            "Chat Half Off Rule",
		"billing_type":    "tokens",
		"billing_rule":    "standard",
		"prompt_rate":     "10",
		"completion_rate": "20",
		"extended_config": `{
			"enable_time_multipliers": true,
			"time_multipliers": [{"all_day": true, "multiplier": "0.5"}]
		}`,
	})
	if wChatBR.Code != http.StatusCreated {
		t.Fatalf("create chat billing rule: %d %v", wChatBR.Code, outChatBR)
	}
	chatBRID := uint64(outChatBR["rule"].(map[string]any)["id"].(float64))
	chatBRPID := outChatBR["rule"].(map[string]any)["pid"].(string)

	for _, m := range []string{"doubao-pro-rule", "gemini-rule", "claude-rule"} {
		wb, ob := f.doAdmin(t, "POST", "/api/admin/model-rule-bindings", map[string]any{
			"model":            m,
			"display_name":     m,
			"billing_rule_id":  chatBRID,
			"forward_rule_ids": []uint64{chatFRID},
			"is_listed":        true,
		})
		if wb.Code != http.StatusOK {
			t.Fatalf("bind %s: %d %v", m, wb.Code, ob)
		}
	}

	// Seedance 2.0 规则
	wVidFR, outVidFR := f.doAdmin(t, "POST", "/api/admin/forward-rules", map[string]any{
		"name":      "Volc Seedance Native Forward",
		"rule_type": "volcengine",
		"category":  "视频",
		"config_json": `{
			"target_type": "volcengine",
			"auth_type": "bearer",
			"path_rewrite": {"old": "/v1/videos", "new": "/api/v3/contents/generations/tasks"},
			"path_passthrough": ["/api/v3/contents/generations/tasks"],
			"poll_path": "/api/v3/contents/generations/tasks/${task_id}"
		}`,
	})
	if wVidFR.Code != http.StatusCreated {
		t.Fatalf("create video forward rule: %d %v", wVidFR.Code, outVidFR)
	}
	vidFRID := uint64(outVidFR["rule"].(map[string]any)["id"].(float64))
	vidFREID := outVidFR["rule"].(map[string]any)["eid"].(string)

	wVidBR, outVidBR := f.doAdmin(t, "POST", "/api/admin/billing-rules", map[string]any{
		"name":         "Seedance 2.0 Native Rule",
		"billing_type": "tokens",
		"billing_rule": "seedance2.0",
		"pricing_type": "tiered",
		"prompt_rate":  "46",
		"extended_config": `{
			"resolution_rates": {
				"720p": {"with_video": "28", "without_video": "46"}
			}
		}`,
	})
	if wVidBR.Code != http.StatusCreated {
		t.Fatalf("create video billing rule: %d %v", wVidBR.Code, outVidBR)
	}
	vidBRID := uint64(outVidBR["rule"].(map[string]any)["id"].(float64))
	vidBRPID := outVidBR["rule"].(map[string]any)["pid"].(string)

	wbv, obv := f.doAdmin(t, "POST", "/api/admin/model-rule-bindings", map[string]any{
		"model":            "seedance-2-native",
		"display_name":     "Seedance 2.0",
		"billing_rule_id":  vidBRID,
		"forward_rule_ids": []uint64{vidFRID},
		"is_listed":        true,
	})
	if wbv.Code != http.StatusOK {
		t.Fatalf("bind seedance-2-native: %d %v", wbv.Code, obv)
	}

	// 3. 测试 /api/v3/chat/completions（命中 forward_rule /custom/ark/chat + x-api-key + billing_rule 0.5x）
	// 计算：(100k*10 + 50k*20)/1M * 0.5 = (1.0 + 1.0)*0.5 = 1.0
	wArkChat := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/chat/completions", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":    "doubao-pro-rule",
		"messages": []map[string]any{{"role": "user", "content": "hi ark"}},
	})
	if wArkChat.Code != http.StatusOK || !strings.Contains(wArkChat.Body.String(), "ark ok") {
		t.Fatalf("/api/v3/chat/completions failed: %d %s", wArkChat.Code, wArkChat.Body.String())
	}
	mu.Lock()
	lastIdx := len(upstreamPaths) - 1
	gotPath := upstreamPaths[lastIdx]
	gotAuth := upstreamHeaders[lastIdx].Get("x-api-key")
	mu.Unlock()
	if gotPath != "POST /custom/ark/chat" || gotAuth != "sk-upstream-secret" {
		t.Fatalf("expected forward rule override path+auth, got path=%q x-api-key=%q", gotPath, gotAuth)
	}

	arkReqID := wArkChat.Header().Get("X-Request-Id")
	arkInv := waitInvocation(t, db, "request_id = ?", arkReqID)
	if !arkInv.CostUSD.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("ark chat cost = %s, want 1", arkInv.CostUSD.String())
	}
	if arkInv.ForwardRuleEID != chatFREID || arkInv.BillingRulePID != chatBRPID || !arkInv.UpstreamCostRatio.Equal(decimal.RequireFromString("0.75")) {
		t.Fatalf("expected rule tracking on invocation, got eid=%q pid=%q ratio=%s", arkInv.ForwardRuleEID, arkInv.BillingRulePID, arkInv.UpstreamCostRatio.String())
	}

	// 4. 测试 /v1/messages（Anthropic 直连入站）
	wCl := doAPIKeyReq(t, f, http.MethodPost, "/v1/messages", "x-api-key", apiKey, map[string]any{
		"model":      "claude-rule",
		"max_tokens": 128,
		"messages":   []map[string]any{{"role": "user", "content": "hi claude"}},
	})
	if wCl.Code != http.StatusOK || !strings.Contains(wCl.Body.String(), `"type":"message"`) {
		t.Fatalf("/v1/messages failed: %d %s", wCl.Code, wCl.Body.String())
	}

	// 5. 测试 /v1beta/models/gemini-rule:generateContent（Gemini 原生入站）
	wGem := doAPIKeyReq(t, f, http.MethodPost, "/v1beta/models/gemini-rule:generateContent", "x-goog-api-key", apiKey, map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "hi gemini"}}}},
	})
	if wGem.Code != http.StatusOK || !strings.Contains(wGem.Body.String(), `"candidates"`) || !strings.Contains(wGem.Body.String(), `"ark ok"`) {
		t.Fatalf("gemini native generateContent failed: %d %s", wGem.Code, wGem.Body.String())
	}
	gemInv := waitInvocation(t, db, "request_id = ?", wGem.Header().Get("X-Request-Id"))
	if gemInv.Protocol != "gemini" {
		t.Fatalf("expected protocol=gemini, got %q", gemInv.Protocol)
	}

	// 6. 测试 /api/v3/contents/generations/tasks（Seedance 2.0 原生提交 -> 轮询 -> 结算）
	// 100k tokens * 28/1M (720p with_video) = 2.8 USD
	wTask := doAPIKeyReq(t, f, http.MethodPost, "/api/v3/contents/generations/tasks", "Authorization", "Bearer "+apiKey, map[string]any{
		"model":      "seedance-2-native",
		"resolution": "720p",
		"duration":   4,
		"content": []map[string]any{
			{"type": "text", "text": "cat running"},
			{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example.com/ref.mp4"}},
		},
	})
	if wTask.Code != http.StatusOK {
		t.Fatalf("seedance native create failed: %d %s", wTask.Code, wTask.Body.String())
	}
	var taskCreateResp struct {
		ID             string `json:"id"`
		Status         string `json:"status"`
		ForwardRuleEID string `json:"forward_rule_eid"`
		BillingRulePID string `json:"billing_rule_pid"`
	}
	_ = json.Unmarshal(wTask.Body.Bytes(), &taskCreateResp)
	if taskCreateResp.ID == "" || taskCreateResp.ForwardRuleEID != vidFREID || taskCreateResp.BillingRulePID != vidBRPID {
		t.Fatalf("unexpected task create response: %s", wTask.Body.String())
	}

	// 第 1 次 PollOnce -> running；此时尝试 DELETE 取消应返回 409 Conflict
	ts.PollOnce(context.Background())
	wCancelRunning := doAPIKeyReq(t, f, http.MethodDelete, "/api/v3/contents/generations/tasks/"+taskCreateResp.ID, "Authorization", "Bearer "+apiKey, nil)
	if wCancelRunning.Code != http.StatusConflict {
		t.Fatalf("cancel running task: got %d, want 409", wCancelRunning.Code)
	}

	// 第 2 次 PollOnce -> succeeded + seedance2.0 按 720p with_video=28/1M 结算 100k = 2.8
	ts.PollOnce(context.Background())
	wTaskGet := doAPIKeyReq(t, f, http.MethodGet, "/api/v3/contents/generations/tasks/"+taskCreateResp.ID, "Authorization", "Bearer "+apiKey, nil)
	if wTaskGet.Code != http.StatusOK || !strings.Contains(wTaskGet.Body.String(), "https://cdn.example.com/seedance.mp4") {
		t.Fatalf("seedance native get failed: %d %s", wTaskGet.Code, wTaskGet.Body.String())
	}
	var getResp struct {
		Status        string          `json:"status"`
		CostUSD       decimal.Decimal `json:"cost_usd"`
		BillingDetail string          `json:"billing_detail"`
	}
	_ = json.Unmarshal(wTaskGet.Body.Bytes(), &getResp)
	if getResp.Status != "succeeded" || !getResp.CostUSD.Equal(decimal.RequireFromString("2.8")) {
		t.Fatalf("unexpected seedance settled state: status=%q cost=%s detail=%q", getResp.Status, getResp.CostUSD.String(), getResp.BillingDetail)
	}
	// 等待 task recorder 写入 invocations
	_ = waitInvocation(t, db, "request_id = ?", taskCreateResp.ID)

	// 7. 验证统一日志视图（租户视角隐藏渠道敏感字段；Admin 视角含渠道与进货成本倍率）
	_, orgLogs := f.do(t, "GET", "/api/logs/unified?page=1&page_size=20", sessionToken, nil)
	items, _ := orgLogs["items"].([]any)
	if len(items) < 4 {
		t.Fatalf("expected >=4 unified log items for org, got %d (%v)", len(items), orgLogs)
	}
	rawOrgLogs, _ := json.Marshal(orgLogs)
	if strings.Contains(string(rawOrgLogs), `"channel_id"`) || strings.Contains(string(rawOrgLogs), `"upstream_cost_ratio"`) {
		t.Fatalf("tenant unified logs must redact channel_id and upstream_cost_ratio: %s", string(rawOrgLogs))
	}

	wAdmLogs, admLogs := f.doAdmin(t, "GET", "/api/admin/logs/unified?kind=task", nil)
	if wAdmLogs.Code != http.StatusOK {
		t.Fatalf("admin unified logs failed: %d %v", wAdmLogs.Code, admLogs)
	}
	rawAdmLogs, _ := json.Marshal(admLogs)
	if !strings.Contains(string(rawAdmLogs), taskCreateResp.ID) || !strings.Contains(string(rawAdmLogs), `"channel_id"`) {
		t.Fatalf("admin unified logs missing expected task/channel info: %s", string(rawAdmLogs))
	}
}
