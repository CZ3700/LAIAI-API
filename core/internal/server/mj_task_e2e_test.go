package server

// 丝滑度批 C-4 Midjourney e2e：/mj/* 方言全链路——
// mj-api-secret 鉴权提交 → 上游 mjproxy mock → PollOnce 终态 → PerCallUSD 按次结算（0048 快照）
// → fetch 透传改写 id → change 动作 taskId 平台↔上游翻译 → 失败全退。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func setupMJEnv(t *testing.T, srv *gin999, db *gorm.DB, baseURL string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "mj", "provider": "midjourney", "base_url": baseURL,
		"api_key": "gw-secret", "models": []string{"mj_imagine", "mj_change", "mj_describe"},
	})
	if w.Code != 201 {
		t.Fatalf("create mj channel = %d %v", w.Code, out)
	}
	for _, m := range []string{"mj_imagine", "mj_change", "mj_describe"} {
		if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
			"model": m, "input_usd_per_1m": "0", "output_usd_per_1m": "0", "per_call_usd": "0.30",
		}); w.Code != 200 {
			t.Fatalf("price %s = %d %v", m, w.Code, out)
		}
	}
}

func TestMJTaskE2E(t *testing.T) {
	srv, db := setupRelayServer(t)
	var gotSecret, submitBody, changeTaskID atomic.Value
	gotSecret.Store("")
	submitBody.Store("")
	changeTaskID.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret.Store(r.Header.Get("mj-api-secret"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mj/submit/imagine"):
			b, _ := json.Marshal(map[string]any{})
			_ = b
			raw := make([]byte, 4096)
			n, _ := r.Body.Read(raw)
			submitBody.Store(string(raw[:n]))
			_, _ = w.Write([]byte(`{"code":1,"description":"提交成功","result":"mj-up-1"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mj/submit/change"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			changeTaskID.Store(b["taskId"])
			_, _ = w.Write([]byte(`{"code":1,"description":"提交成功","result":"mj-up-2"}`))
		case strings.HasSuffix(r.URL.Path, "/mj/task/mj-up-1/fetch"), strings.HasSuffix(r.URL.Path, "/mj/task/mj-up-2/fetch"):
			_, _ = w.Write([]byte(`{"id":"mj-up-1","status":"SUCCESS","progress":"100%",` +
				`"imageUrl":"https://cdn.discordapp.com/grid.png","buttons":[{"customId":"MJ::U1","label":"U1"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()

	setupMJEnv(t, srv, db, up.URL)
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, orgID := seedUserWithCredit(t, srv, "mj-e2e@example.com", "10")

	// 1. 提交 imagine（mj-api-secret 头鉴权，方言标准）
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mj/submit/imagine",
		strings.NewReader(`{"prompt":"a cat --v 6"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("mj submit = %d %s", w.Code, w.Body.String())
	}
	var sub struct {
		Code   int    `json:"code"`
		Result string `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &sub)
	if sub.Code != 1 || !strings.HasPrefix(sub.Result, "task_") {
		t.Fatalf("submit resp = %s (want code=1 + 平台 task id)", w.Body.String())
	}
	if gotSecret.Load() != "gw-secret" {
		t.Fatalf("upstream mj-api-secret = %q, want channel credential", gotSecret.Load())
	}
	if !strings.Contains(submitBody.Load().(string), "a cat --v 6") {
		t.Fatalf("prompt 未透传: %s", submitBody.Load())
	}

	// 2. 轮询 → SUCCESS → PerCallUSD 按次结算（0048 快照回归：此前会结算 0）
	svc.PollOnce(context.Background())
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	want := decimal.RequireFromString("9.70") // 10 - 0.30
	if !acc.PostedBalance.Equal(want) {
		t.Fatalf("balance = %s, want %s (PerCallUSD 按次结算)", acc.PostedBalance, want)
	}

	// 3. fetch 透传：id 必须改写为平台 id，buttons 等全量字段保留
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/mj/task/"+sub.Result+"/fetch", nil)
	req2.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w2, req2)
	var fetched map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &fetched)
	if fetched["id"] != sub.Result {
		t.Fatalf("fetch id = %v, want 平台 id %s（不泄上游 id）", fetched["id"], sub.Result)
	}
	if fetched["status"] != "SUCCESS" || fetched["buttons"] == nil {
		t.Fatalf("fetch 透传字段缺失: %s", w2.Body.String())
	}

	// 4. change 动作：客户端带平台 taskId → 上游收到的是上游 id（org 隔离翻译）
	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/mj/submit/change",
		strings.NewReader(`{"taskId":"`+sub.Result+`","action":"UPSCALE","index":1}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w3, req3)
	var sub3 struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(w3.Body.Bytes(), &sub3)
	if sub3.Code != 1 {
		t.Fatalf("change submit = %s", w3.Body.String())
	}
	if changeTaskID.Load() != "mj-up-1" {
		t.Fatalf("上游收到 taskId = %v, want mj-up-1（平台→上游翻译）", changeTaskID.Load())
	}

	// 5. list-by-condition：DB 合成 + org 隔离
	w4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("POST", "/mj/task/list-by-condition",
		strings.NewReader(`{"ids":["`+sub.Result+`","task_not_exist"]}`))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w4, req4)
	var list []map[string]any
	_ = json.Unmarshal(w4.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["status"] != "SUCCESS" {
		t.Fatalf("list = %s", w4.Body.String())
	}
}

// describe（无图动作）：SUCCESS 但 imageUrl 空 → 仍按次结算（对抗 review HIGH 回归——
// 此前空 URL 守卫会把它卡到 30 分钟超时全退，客户端却已透传拿走文本产物）。
func TestMJDescribeSettlesWithoutImage(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":1,"result":"mj-desc-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"mj-desc-1","status":"SUCCESS","progress":"100%",` +
			`"properties":{"finalPrompt":"1️⃣ a cat in the rain"}}`)) // 无 imageUrl
	}))
	defer up.Close()

	setupMJEnv(t, srv, db, up.URL)
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, orgID := seedUserWithCredit(t, srv, "mj-desc@example.com", "10")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mj/submit/describe", strings.NewReader(`{"base64":"data:image/png;base64,xx"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":1`) {
		t.Fatalf("describe submit = %d %s", w.Code, w.Body.String())
	}

	svc.PollOnce(context.Background())
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	if !acc.PostedBalance.Equal(decimal.RequireFromString("9.70")) {
		t.Fatalf("balance = %s, want 9.70（无图动作必须按次结算，不得卡守卫超时全退）", acc.PostedBalance)
	}
	if !acc.HeldAmount.IsZero() {
		t.Fatalf("held = %s, want 0", acc.HeldAmount)
	}
}

// code 21（任务已存在）：上游返回旧任务 id → 去重复用，绝不对同一生成收两次 hold/结算。
func TestMJDuplicateSubmitDedup(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost { // 两次提交都回同一个上游 id（第二次 code 21）
			_, _ = w.Write([]byte(`{"code":21,"description":"任务已存在","result":"mj-dup-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"mj-dup-1","status":"IN_PROGRESS"}`))
	}))
	defer up.Close()

	setupMJEnv(t, srv, db, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "mj-dup@example.com", "10")

	submit := func() (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/mj/submit/imagine", strings.NewReader(`{"prompt":"same"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("mj-api-secret", apiKey)
		srv.engine.ServeHTTP(w, req)
		var r struct {
			Code   int    `json:"code"`
			Result string `json:"result"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		return r.Code, r.Result
	}
	c1, id1 := submit()
	c2, id2 := submit()
	if c1 != 1 || c2 != 1 || id1 != id2 {
		t.Fatalf("dedup 必须复用同一平台任务: (%d,%s) vs (%d,%s)", c1, id1, c2, id2)
	}
	// 只允许一份 hold 在冻结（第二次的已释放）
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	if !acc.HeldAmount.Equal(decimal.RequireFromString("0.30")) {
		t.Fatalf("held = %s, want 0.30（重复提交的 hold 必须释放）", acc.HeldAmount)
	}
}

// code 24（敏感词）：确定性客户端错误——不跨渠道重试，上游 description 透给客户端（丝滑哲学：
// 平台不自设违禁词，上游说什么给什么）。
func TestMJSensitiveWordSurfacesDescription(t *testing.T) {
	srv, db := setupRelayServer(t)
	var submits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		submits.Add(1)
		_, _ = w.Write([]byte(`{"code":24,"description":"May contain sensitive words: xxx"}`))
	}))
	defer up.Close()

	setupMJEnv(t, srv, db, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "mj-sens@example.com", "10")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mj/submit/imagine", strings.NewReader(`{"prompt":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "sensitive words") {
		t.Fatalf("上游 description 必须透给客户端: %s", w.Body.String())
	}
	if submits.Load() != 1 {
		t.Fatalf("确定性客户端错误不得跨渠道重试: submits=%d", submits.Load())
	}
	// 敏感词拒绝 = 无任务：hold 必须已全退
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	if !acc.HeldAmount.IsZero() || !acc.PostedBalance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("rejected submit 必须全退: posted=%s held=%s", acc.PostedBalance, acc.HeldAmount)
	}
}

// MJ 失败路径：上游 FAILURE → 全额退款（按次任务失败收 0）。
func TestMJTaskFailureRefund(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":1,"result":"mj-fail-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"mj-fail-1","status":"FAILURE","failReason":"upstream error"}`))
	}))
	defer up.Close()

	setupMJEnv(t, srv, db, up.URL)
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, orgID := seedUserWithCredit(t, srv, "mj-fail@example.com", "5")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mj/submit/imagine", strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("mj-api-secret", apiKey)
	srv.engine.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":1`) {
		t.Fatalf("submit = %d %s", w.Code, w.Body.String())
	}

	svc.PollOnce(context.Background())
	acc := &ledger.Account{}
	if err := db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc).Error; err != nil {
		t.Fatal(err)
	}
	if !acc.PostedBalance.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("balance = %s, want 5（失败全退）", acc.PostedBalance)
	}
}
