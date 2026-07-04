package server

// Task/video 准入统一回归（指令 P0-1/P0-2）：
// /v1/video/generations 必须与 chat 同一套 Key 精细管控（IP 白名单/模型白名单/额度）、
// (org,model) 模型级限流、key quota 原子预留→settle 对账、请求体上限、分组倍率计费、
// 终态落 Invocation Store。每条都是上线门禁（曾全部缺失，绕过 key 管控直达上游）。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// seedUserSession：注册+登录，返回 (sessionToken, orgID)——供签发带精细管控的 key。
func seedUserSession(t *testing.T, srv *gin999, email string) (string, float64) {
	t.Helper()
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": email, "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": email, "password": "longenough1"})
	return login["token"].(string), login["org_id"].(float64)
}

// issueControlledKey：签发带 quota/model/IP 管控的 API Key。
func issueControlledKey(t *testing.T, srv *gin999, token string, controls map[string]any) string {
	t.Helper()
	body := map[string]any{"name": "task-controlled"}
	for k, v := range controls {
		body[k] = v
	}
	w, out := srv.do(t, "POST", "/api/keys", token, body)
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("issue key = %d %v", w.Code, out)
	}
	return out["key"].(string)
}

func grantCredit(t *testing.T, srv *gin999, orgID float64, amount, idem string) {
	t.Helper()
	w, out := srv.doAdmin(t, "POST", "/api/admin/credits", map[string]any{
		"org_id": orgID, "amount_usd": amount, "reason": "test grant", "idempotency_key": idem,
	})
	if w.Code != 200 {
		t.Fatalf("grant = %d %v", w.Code, out)
	}
}

func keyUsedAmount(t *testing.T, db *gorm.DB, orgID float64) decimal.Decimal {
	t.Helper()
	var used decimal.Decimal
	if err := db.Raw("SELECT used_amount FROM api_keys WHERE org_id = ? ORDER BY id DESC LIMIT 1",
		uint64(orgID)).Scan(&used).Error; err != nil {
		t.Fatalf("read used_amount: %v", err)
	}
	return used
}

// IP 白名单对 video 生效（keyGate 第一项）：httptest 请求源 IP 是 192.0.2.1，白名单填别的 → 403。
func TestTaskKeyGateIPWhitelist(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "video-ip@example.com")
	grantCredit(t, srv, orgID, "10", "seed-video-ip")
	apiKey := issueControlledKey(t, srv, token, map[string]any{"allow_ips": "203.0.113.7"})

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "x"}},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("ip-blocked submit = %d %v, want 403", w.Code, out)
	}
	if ark.submits.Load() != 0 {
		t.Fatal("blocked request must not reach upstream")
	}
}

// 模型白名单对 video 生效（keyGate 第三项）。
func TestTaskKeyGateModelWhitelist(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "video-model@example.com")
	grantCredit(t, srv, orgID, "10", "seed-video-model")
	apiKey := issueControlledKey(t, srv, token, map[string]any{"model_limits": []string{"some-other-model"}})

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "x"}},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("model-blocked submit = %d %v, want 403", w.Code, out)
	}
	if ark.submits.Load() != 0 {
		t.Fatal("blocked request must not reach upstream")
	}
}

// key quota：提交预留（软上限语义与 chat 一致）→ 触顶拒绝 → 失败终态全额退还后可再提交。
func TestTaskKeyQuotaReserveAndRefund(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "video-quota@example.com")
	grantCredit(t, srv, orgID, "100", "seed-video-quota")
	// quota=$1 < 单次 hold $3.159（315,900 est × $10/1M，真机标定+保守基准）：软上限——used<quota 即放行首笔，之后拦截
	apiKey := issueControlledKey(t, srv, token, map[string]any{"quota_limit": "1"})

	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "first"}},
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("first submit = %d %v", w.Code, out)
	}
	if used := keyUsedAmount(t, db, orgID); !used.Equal(decimal.RequireFromString("3.159")) {
		t.Fatalf("used after reserve = %s, want 3.159", used)
	}

	// 第二笔：used(3.159) ≥ quota(1) → 402，不触上游
	before := ark.submits.Load()
	w, out = srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "second"}},
	})
	if w.Code != http.StatusPaymentRequired || !strings.Contains(out["error"].(map[string]any)["message"].(string), "quota") {
		t.Fatalf("over-quota submit = %d %v, want 402 quota", w.Code, out)
	}
	if ark.submits.Load() != before {
		t.Fatal("over-quota request must not reach upstream")
	}

	// 任务失败 → ledger 全退 + key 预留全退 → 可再提交
	ark.queryState.Store(`{"id":"cgt-1","status":"failed","error":{"message":"boom"}}`)
	svc.PollOnce(context.Background())
	if used := keyUsedAmount(t, db, orgID); !used.IsZero() {
		t.Fatalf("used after failure refund = %s, want 0", used)
	}
	w, out = srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "third"}},
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("post-refund submit = %d %v", w.Code, out)
	}
}

// 成功结算：used_amount 从预留对账到实收（chat capture 同款）；分组倍率参与 hold 与结算；
// 终态落 Invocation Store（请求日志可见视频消费）。
func TestTaskGroupRatioBillingAndSettleAdjust(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)
	svc.Rec = rec // 生产装配下 poller 与路由共享同一 Service（server.New 注入）；测试的独立 poller 手动补
	apiKey, orgID := seedUserWithCredit(t, srv, "video-ratio@example.com", "20")

	// org 计费倍率 ×2
	if w, out := srv.doAdmin(t, "POST", "/api/admin/group-ratios", map[string]any{"name": "vip2x", "ratio": "2"}); w.Code != 200 {
		t.Fatalf("group ratio = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "PUT", "/api/admin/orgs/"+trimFloat(orgID)+"/group", map[string]any{"group": "vip2x"}); w.Code != 200 {
		t.Fatalf("set org group = %d %v", w.Code, out)
	}

	taskID := submitVideoTask(t, srv, apiKey)

	// hold = 315,900 × $10/1M × 2 = $6.318；key 预留同额
	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.HeldAmount.Equal(decimal.RequireFromString("6.318")) {
		t.Fatalf("held with ratio = %s, want 6.318", acc.HeldAmount)
	}
	if used := keyUsedAmount(t, db, orgID); !used.Equal(decimal.RequireFromString("6.318")) {
		t.Fatalf("reserved with ratio = %s, want 6.318", used)
	}

	// 成功 100k tokens → 实收 $1×2=$2；余额 18；used 对账到 2
	ark.queryState.Store(`{"id":"cgt-1","status":"succeeded",
		"content":{"video_url":"https://cdn.example.com/v.mp4"},
		"usage":{"completion_tokens":100000}}`)
	svc.PollOnce(context.Background())

	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("18")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("after ratio settle: posted=%s held=%s, want 18/0", acc.PostedBalance, acc.HeldAmount)
	}
	// 容差断言：SQLite 测试库 used_amount 是 REAL，2.808-0.808 有二进制浮点尾差；
	// 生产 MySQL DECIMAL(20,8) 精确（AddKeyUsageSigned 以 decimal 字符串参数运算）。
	if used := keyUsedAmount(t, db, orgID); used.Sub(decimal.RequireFromString("2")).Abs().GreaterThan(decimal.RequireFromString("0.000001")) {
		t.Fatalf("used after settle adjust = %s, want ≈2", used)
	}

	// Invocation Store：request_id=task_id，费用/模型/归属齐全
	inv := waitInvocation(t, db, "request_id = ?", taskID)
	if inv.Status != invocation.StatusSuccess || inv.Model != "seedance-test" ||
		!inv.CostUSD.Equal(decimal.RequireFromString("2")) || inv.CompletionTokens != 100000 || inv.APIKeyID == 0 {
		t.Fatalf("task invocation = %+v", inv)
	}
}

// estimate 分支的倍率（对抗 review：此前零覆盖）：上游 succeeded 但缺 usage → 按预估收，
// 倍率必须与 hold 同值参与（否则 ratio=2 的 org 在上游漏报 usage 时静默按 1 倍少收）。
func TestTaskEstimateBranchAppliesRatio(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)
	svc.Rec = rec
	apiKey, orgID := seedUserWithCredit(t, srv, "video-est@example.com", "20")

	if w, out := srv.doAdmin(t, "POST", "/api/admin/group-ratios", map[string]any{"name": "est2x", "ratio": "2"}); w.Code != 200 {
		t.Fatalf("group ratio = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "PUT", "/api/admin/orgs/"+trimFloat(orgID)+"/group", map[string]any{"group": "est2x"}); w.Code != 200 {
		t.Fatalf("set org group = %d %v", w.Code, out)
	}

	taskID := submitVideoTask(t, srv, apiKey)
	// succeeded 但不带 usage：按 est 315,900 × $10/1M × ratio 2 = $6.318 全额收
	ark.queryState.Store(`{"id":"cgt-1","status":"succeeded",
		"content":{"video_url":"https://cdn.example.com/v.mp4"}}`)
	svc.PollOnce(context.Background())

	acc := &ledger.Account{}
	_ = db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("13.682")) || !acc.HeldAmount.IsZero() {
		t.Fatalf("estimate settle with ratio: posted=%s held=%s, want 13.682/0", acc.PostedBalance, acc.HeldAmount)
	}
	if used := keyUsedAmount(t, db, orgID); !used.Equal(decimal.RequireFromString("6.318")) {
		t.Fatalf("used after estimate settle = %s, want 6.318", used)
	}
	inv := waitInvocation(t, db, "request_id = ?", taskID)
	if inv.UsageSource != invocation.UsageSourceEstimated || !inv.CostUSD.Equal(decimal.RequireFromString("6.318")) {
		t.Fatalf("estimate invocation = source %q cost %s, want estimated/6.318", inv.UsageSource, inv.CostUSD)
	}
}

// 生产装配守卫（对抗 review 变异实测曾全绿）：NewWithServer 必须把 Gate 与 Recorder
// 装配到 TaskService 上——装配线被删则视频调用静默丢准入/丢请求日志，此测试变红。
func TestTaskServiceProductionAssembly(t *testing.T) {
	srv, db := setupRelayServer(t)
	_ = srv
	rec := invocation.NewRecorder(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Cleanup(rec.Close)
	ts := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	NewWithServer(db, Options{
		AuthSecret: testSecret, AdminToken: []byte(testAdminToken),
		Keyring: testKeyring(t), Recorder: rec, TaskService: ts,
		AuthRateBurst: 100000,
	})
	if ts.Gate == nil {
		t.Fatal("NewWithServer must wire task admission Gate (P0-1 fail-closed depends on it)")
	}
	if ts.Rec != rec {
		t.Fatal("NewWithServer must wire Options.Recorder into TaskService.Rec (video invocations silently vanish otherwise)")
	}
}

// GET /v1/video/generations/:id 的 IP 白名单（对抗 review）：响应含 result_url 等租户数据，
// 泄露的受限 Key 不得从白名单外 IP 读取。
func TestTaskGetHonorsIPWhitelist(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	token, orgID := seedUserSession(t, srv, "video-get-ip@example.com")
	grantCredit(t, srv, orgID, "10", "seed-video-get-ip")
	// 白名单含测试请求源 192.0.2.1 → 提交成功；随后收窄白名单 → GET 被拒
	apiKey := issueControlledKey(t, srv, token, map[string]any{"allow_ips": "192.0.2.1"})
	taskID := submitVideoTask(t, srv, apiKey)

	db.Exec("UPDATE api_keys SET allow_ips = '203.0.113.7' WHERE org_id = ?", uint64(orgID))
	w, out := srv.do(t, "GET", "/v1/video/generations/"+taskID, apiKey, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("ip-blocked GET = %d %v, want 403", w.Code, out)
	}
}

// (org,model) 模型级限流对 video 生效（与 chat 同一限流器实例/窗口）。
func TestTaskModelRateLimit(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "video-rate@example.com", "100")
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('model_rate_limit_enabled', 'true')")
	db.Exec("INSERT INTO ext_settings (name, value) VALUES ('model_rate_limit_rpm', '1')")

	submitVideoTask(t, srv, apiKey)
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": "again"}},
	})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited submit = %d %v, want 429", w.Code, out)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 missing Retry-After")
	}
}

// 请求体上限（P0-2）：超过 relay.MaxRequestBodySize 的 JSON → 413，不做无界读。
func TestTaskBodyLimit(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	_ = setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, _ := seedUserWithCredit(t, srv, "video-body@example.com", "10")

	huge := strings.Repeat("a", 10<<20) // 10MiB 文本字段，加上 JSON 包装必超限
	w, out := srv.do(t, "POST", "/v1/video/generations", apiKey, map[string]any{
		"model": "seedance-test", "content": []map[string]any{{"type": "text", "text": huge}},
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized submit = %d %v, want 413", w.Code, out)
	}
	if ark.submits.Load() != 0 {
		t.Fatal("oversized request must not reach upstream")
	}
}

// 升级兼容：0032 前的在途旧行（hold_amount_usd=0，提交时未做 key 预留）结算时不得动 used_amount。
func TestTaskLegacyRowSettleNoQuotaAdjust(t *testing.T) {
	srv, db := setupRelayServer(t)
	ark := newMockArk(t)
	svc := setupTaskEnv(t, srv, db, ark.srv.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "video-legacy@example.com", "10")
	_ = apiKey

	// 手工造一条"旧版"任务行：真实 ledger hold + hold_amount_usd=0
	var hold *ledger.Hold
	if err := db.Transaction(func(tx *gorm.DB) error {
		h, _, err := ledger.CreateHoldSharded(tx, uint64(orgID), ledger.AccountUserCredit, "legacy-1", ledger.HoldInput{
			IdempotencyKey: "task:task_legacy1", Amount: decimal.RequireFromString("5"),
			RefType: "task", RefID: "task_legacy1",
		})
		hold = h
		return err
	}); err != nil {
		t.Fatalf("legacy hold: %v", err)
	}
	legacy := &task.Task{
		TaskID: "task_legacy1", OrgID: uint64(orgID), APIKeyID: 1,
		Model: "seedance-test", ChannelID: 1, Provider: "volcengine",
		Status: task.StatusRunning, UpstreamTaskID: "cgt-1", HoldID: hold.ID,
		EstTokens: 500000, PriceOutUSDPer1M: decimal.RequireFromString("10"),
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("legacy row: %v", err)
	}

	ark.queryState.Store(`{"id":"cgt-1","status":"succeeded",
		"content":{"video_url":"https://cdn.example.com/v.mp4"},
		"usage":{"completion_tokens":100000}}`)
	svc.PollOnce(context.Background())

	got := &task.Task{}
	_ = db.Where("task_id = ?", "task_legacy1").First(got)
	if got.Status != task.StatusSucceeded || !got.CostUSD.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("legacy settle: status=%s cost=%s, want succeeded/1", got.Status, got.CostUSD)
	}
	if used := keyUsedAmount(t, db, orgID); !used.IsZero() {
		t.Fatalf("legacy settle touched used_amount = %s, want 0", used)
	}
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(decimal.NewFromFloat(f).String(), ".0"), ".00")
}

// 对抗 review：多 key 任务渠道，提交选定的 key 必须钉在任务行，轮询固定用同一把 key
// （否则提交/轮询 key 不一致，上游查不到任务 → 超时全退 + 上游已计费）。
// 上游 mock：submit 记住每个 task→提交 key；query 校验 Authorization 与提交 key 一致，
// 不一致返回 failed（模拟"任务 scope 到创建它的 key/账号"的真实上游）。
func TestTaskCredentialPinnedAcrossPoll(t *testing.T) {
	srv, db := setupRelayServer(t)
	var mu sync.Mutex
	submitKeyByTask := map[string]string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			id := "cgt-" + key // 任务 id 编码提交 key
			mu.Lock()
			submitKeyByTask[id] = key
			mu.Unlock()
			fmt.Fprintf(w, `{"id":%q}`, id)
			return
		}
		// query：路径尾是 upstream task id
		parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
		id := parts[len(parts)-1]
		mu.Lock()
		want := submitKeyByTask[id]
		mu.Unlock()
		if key != want { // 轮询用了与提交不同的 key → 上游查不到（scope 到提交 key）
			fmt.Fprintf(w, `{"id":%q,"status":"failed","error":{"message":"task not found for this key"}}`, id)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"status":"succeeded","content":{"video_url":"https://x/v.mp4"},"usage":{"completion_tokens":100000}}`, id)
	}))
	t.Cleanup(up.Close)

	// volcengine 渠道 + seedance 定价 + 两把有效 key（polling）
	w, out := srv.doAdmin(t, "POST", "/api/admin/channels", map[string]any{
		"name": "ark-mk", "provider": "volcengine", "base_url": up.URL,
		"api_key": "sk-k1", "models": []string{"seedance-test"}, "credential_mode": "polling",
		"max_concurrent_tasks": 10, // 提交多个任务不撞并发闸
	})
	if w.Code != 201 {
		t.Fatalf("create channel = %d %v", w.Code, out)
	}
	chID := uint64(out["id"].(float64))
	if w, out := srv.doAdmin(t, "POST", fmt.Sprintf("/api/admin/channels/%d/credentials", chID), map[string]any{"api_key": "sk-k2"}); w.Code != 201 {
		t.Fatalf("add key = %d %v", w.Code, out)
	}
	if w, out := srv.doAdmin(t, "POST", "/api/admin/model-prices", map[string]any{
		"model": "seedance-test", "input_usd_per_1m": "0", "output_usd_per_1m": "10",
	}); w.Code != 200 {
		t.Fatalf("price = %d %v", w.Code, out)
	}
	svc := task.NewService(db, testKeyring(t), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apiKey, _ := seedUserWithCredit(t, srv, "task-pin@example.com", "50")

	// 提交 4 个任务（polling 会在两把 key 间交替）；每个 credential_id 落库非 0；
	// 轮询固定用钉住的 key → 全部成功（若 pollOne re-select，polling 漂移会命中错 key → failed）。
	var taskIDs []string
	for i := 0; i < 4; i++ {
		taskIDs = append(taskIDs, submitVideoTask(t, srv, apiKey))
	}
	for _, tid := range taskIDs {
		var credID uint64
		db.Raw("SELECT credential_id FROM tasks WHERE task_id = ?", tid).Scan(&credID)
		if credID == 0 {
			t.Fatalf("task %s credential_id not pinned", tid)
		}
	}
	svc.PollOnce(context.Background())
	for _, tid := range taskIDs {
		var status string
		db.Raw("SELECT status FROM tasks WHERE task_id = ?", tid).Scan(&status)
		if status != task.StatusSucceeded {
			t.Fatalf("task %s status = %s, want succeeded (poll must use pinned submit key)", tid, status)
		}
	}
}
