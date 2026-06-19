package server

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

// insertInvocationPerf：可指定 model/status/frt_ms/duration_ms/created_at 的调用插入（perf-stats 与窗口/过滤测试用）。
func insertInvocationPerf(t *testing.T, db *gorm.DB, orgID float64, reqID, model, status string, frtMs, durMs int, createdAt time.Time) {
	t.Helper()
	if err := db.Exec(`INSERT INTO invocations
		(org_id, api_key_id, request_id, model, provider, status, status_code,
		 prompt_tokens, completion_tokens, cost_usd, frt_ms, duration_ms, created_at)
		VALUES (?, 0, ?, ?, 'openai', ?, 200, 100, 50, 0, ?, ?, ?)`,
		orgID, reqID, model, status, frtMs, durMs, createdAt.UTC().Format("2006-01-02 15:04:05")).Error; err != nil {
		t.Fatalf("insert invocation perf: %v", err)
	}
}

func num(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return -1
}

// 性能分位/成功率/时长分布 服务端聚合：窗口全量计算（取代旧前端 ≤100 行本地算），与前端分位口径一致，org 隔离。
func TestUsagePerfStats(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "perf@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	now := time.Now().UTC()

	// 5 条在窗内：frt=[100,200,300,400,500]，duration=[500,1500,2000,5000,12000]，4 成功 1 失败（500/12000 那条失败）
	insertInvocationPerf(t, db, orgID, "p1", "gpt-4o", "success", 100, 500, now)
	insertInvocationPerf(t, db, orgID, "p2", "gpt-4o", "success", 200, 1500, now)
	insertInvocationPerf(t, db, orgID, "p3", "gpt-4o", "success", 300, 2000, now)
	insertInvocationPerf(t, db, orgID, "p4", "gpt-4o", "success", 400, 5000, now)
	insertInvocationPerf(t, db, orgID, "p5", "gpt-4o", "upstream_error", 500, 12000, now)
	// 1 条窗外（2 天前），mins=1440(24h) 应排除
	insertInvocationPerf(t, db, orgID, "pold", "gpt-4o", "success", 999, 999, now.AddDate(0, 0, -2))

	w, out := srv.do(t, "GET", "/api/usage/perf-stats?mins=1440", tok, nil)
	if w.Code != 200 {
		t.Fatalf("perf-stats = %d %v", w.Code, out)
	}
	if num(out["calls"]) != 5 {
		t.Fatalf("calls = %v, want 5 (窗外行须排除)", out["calls"])
	}
	if num(out["succeeded"]) != 4 {
		t.Fatalf("succeeded = %v, want 4", out["succeeded"])
	}
	if num(out["success_rate"]) != 0.8 {
		t.Fatalf("success_rate = %v, want 0.8", out["success_rate"])
	}
	// frt 分位（含失败行，与前端 frts 口径一致）：sorted=[100,200,300,400,500]
	frt := out["frt"].(map[string]any)
	if num(frt["p50"]) != 300 || num(frt["p95"]) != 500 || num(frt["p99"]) != 500 {
		t.Fatalf("frt = %v, want p50=300 p95=500 p99=500", frt)
	}
	// duration 分位：sorted=[500,1500,2000,5000,12000]
	dur := out["dur"].(map[string]any)
	if num(dur["p50"]) != 2000 || num(dur["p95"]) != 12000 || num(dur["p99"]) != 12000 {
		t.Fatalf("dur = %v, want p50=2000 p95=12000 p99=12000", dur)
	}
	// 时长桶：<1s=1(500), 1-3s=2(1500,2000), 3-10s=1(5000), >10s=1(12000)
	buckets := out["buckets"].([]any)
	wantBuckets := []float64{1, 2, 1, 1}
	if len(buckets) != 4 {
		t.Fatalf("buckets len = %d, want 4", len(buckets))
	}
	for i, b := range buckets {
		if got := num(b.(map[string]any)["count"]); got != wantBuckets[i] {
			t.Fatalf("bucket[%d] count = %v, want %v", i, got, wantBuckets[i])
		}
	}
	// 按模型：单模型 gpt-4o
	models := out["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	m0 := models[0].(map[string]any)
	if num(m0["calls"]) != 5 || num(m0["succeeded"]) != 4 || num(m0["success_rate"]) != 0.8 || num(m0["p50"]) != 300 {
		t.Fatalf("model[0] = %v, want calls=5 succeeded=4 sr=0.8 p50=300", m0)
	}
	if out["sampled"].(bool) {
		t.Fatalf("sampled = true, want false (5 行远未触顶)")
	}

	// org 隔离（红线 5）
	tok2 := registerAndLogin(t, srv, "perf2@example.com")
	_, out2 := srv.do(t, "GET", "/api/usage/perf-stats?mins=1440", tok2, nil)
	if num(out2["calls"]) != 0 {
		t.Fatalf("org isolation breach: calls = %v, want 0", out2["calls"])
	}
}

// getUsage：status=failed 在服务端等价 status<>'success'（覆盖全部失败枚举），分页/total 反映过滤结果。
func TestUsageFailedFilter(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "uf@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	now := time.Now().UTC()
	insertInvocationPerf(t, db, orgID, "f1", "gpt-4o", "success", 100, 500, now)
	insertInvocationPerf(t, db, orgID, "f2", "gpt-4o", "upstream_error", 100, 500, now)
	insertInvocationPerf(t, db, orgID, "f3", "gpt-4o", "no_channel", 100, 500, now)

	_, all := srv.do(t, "GET", "/api/usage", tok, nil)
	if num(all["total"]) != 3 {
		t.Fatalf("all total = %v, want 3", all["total"])
	}
	_, failed := srv.do(t, "GET", "/api/usage?status=failed", tok, nil)
	if num(failed["total"]) != 2 {
		t.Fatalf("failed total = %v, want 2 (upstream_error+no_channel)", failed["total"])
	}
	_, success := srv.do(t, "GET", "/api/usage?status=success", tok, nil)
	if num(success["total"]) != 1 {
		t.Fatalf("success total = %v, want 1", success["total"])
	}
}

// getUsage：since/until 服务端 UTC 时间窗过滤（左闭右开），分页/total 反映窗口。
func TestUsageTimeWindow(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "uw@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	insertInvocationPerf(t, db, orgID, "w_today", "gpt-4o", "success", 100, 500, now)
	insertInvocationPerf(t, db, orgID, "w_old", "gpt-4o", "success", 100, 500, now.AddDate(0, 0, -10))

	_, sinceOut := srv.do(t, "GET", "/api/usage?since="+today, tok, nil)
	if num(sinceOut["total"]) != 1 {
		t.Fatalf("since total = %v, want 1 (仅今天)", sinceOut["total"])
	}
	_, untilOut := srv.do(t, "GET", "/api/usage?until="+today, tok, nil)
	if num(untilOut["total"]) != 1 {
		t.Fatalf("until total = %v, want 1 (今天之前)", untilOut["total"])
	}
	items := untilOut["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["request_id"].(string) != "w_old" {
		t.Fatalf("until items = %v, want [w_old]", untilOut["items"])
	}
}

// 请求日志 CLIENT 栏：调用记录采集并通过 /api/usage 返回访客 IP / UA / Referer（org 隔离不变）。
func TestUsageClientMeta(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "cm@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := me["org_id"].(float64)
	if err := db.Exec(`INSERT INTO invocations
		(org_id, api_key_id, request_id, model, status, status_code, client_ip, user_agent, referer, created_at)
		VALUES (?, 0, 'cm1', 'gpt-4o', 'success', 200, '203.0.113.7', 'curl/8.5', 'https://app.example/', ?)`,
		orgID, time.Now().UTC().Format("2006-01-02 15:04:05")).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, out := srv.do(t, "GET", "/api/usage", tok, nil)
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", out["items"])
	}
	row := items[0].(map[string]any)
	if row["client_ip"] != "203.0.113.7" || row["user_agent"] != "curl/8.5" || row["referer"] != "https://app.example/" {
		t.Fatalf("client meta = %v / %v / %v", row["client_ip"], row["user_agent"], row["referer"])
	}
}

// listTopups：返回权威 total（不被 Limit(50) 截断），供前端计数显示真实总量。
func TestListTopupsTotal(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "lt@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := uint64(me["org_id"].(float64))
	now := time.Now().UTC()
	insertTopup(t, db, orgID, "t1", 30, 1, now.Unix())
	insertTopup(t, db, orgID, "t2", 50, 0, now.Unix())

	_, out := srv.do(t, "GET", "/api/payment/orders", tok, nil)
	if num(out["total"]) != 2 {
		t.Fatalf("topups total = %v, want 2", out["total"])
	}
	if orders, _ := out["orders"].([]any); len(orders) != 2 {
		t.Fatalf("topups orders len = %d, want 2", len(orders))
	}
}
