package server

import (
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"
)

// decimal 字段经 JSON 回来是字符串（shopspring 默认带引号），解析成 float64 便于断言
func decf(t *testing.T, v any) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(v.(string), 64)
	if err != nil {
		t.Fatalf("parse decimal %v: %v", v, err)
	}
	return f
}

// 直插一条 invocation（绕开异步 recorder，精确控制 created_at）
func insertInvocation(t *testing.T, db *gorm.DB, orgID float64, reqID, status, provider string, cost float64, createdAt time.Time) {
	t.Helper()
	if err := db.Exec(`INSERT INTO invocations
		(org_id, api_key_id, request_id, model, provider, status, status_code,
		 prompt_tokens, completion_tokens, cost_usd, frt_ms, duration_ms, created_at)
		VALUES (?, 0, ?, 'gpt-4o', ?, ?, 200, 100, 50, ?, 120, 800, ?)`,
		orgID, reqID, provider, status, cost, createdAt.UTC().Format("2006-01-02 15:04:05")).Error; err != nil {
		t.Fatalf("insert invocation: %v", err)
	}
}

func TestAdminAnalytics(t *testing.T) {
	srv, db := setupRelayServer(t)

	_, orgID := seedUserWithCredit(t, srv, "ana@gmail.com", "100") // → ledger float = 100
	now := time.Now()

	// 三条调用：两成功（各 $0.02）一失败（$0），同 org → consumption=0.04
	insertInvocation(t, db, orgID, "r1", "success", "openai", 0.02, now)
	insertInvocation(t, db, orgID, "r2", "success", "openai", 0.02, now)
	insertInvocation(t, db, orgID, "r3", "error", "anthropic", 0, now)

	// 两笔充值：stripe 已支付 $100、epay 待支付 $50 → 转化率 0.5
	nowUnix := now.Unix()
	if err := db.Exec(`INSERT INTO topup_orders
		(order_no, org_id, provider, amount_usd, pay_amount, pay_currency, status, created_at, paid_at)
		VALUES ('o1', ?, 'stripe', 100, 100, 'USD', 1, ?, ?)`, orgID, nowUnix, nowUnix).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO topup_orders
		(order_no, org_id, provider, amount_usd, pay_amount, pay_currency, status, created_at, paid_at)
		VALUES ('o2', ?, 'epay', 50, 365, 'CNY', 0, ?, 0)`, orgID, nowUnix).Error; err != nil {
		t.Fatal(err)
	}

	w, out := srv.doAdmin(t, "GET", "/api/admin/analytics?days=14", nil)
	if w.Code != 200 {
		t.Fatalf("analytics = %d %v", w.Code, out)
	}

	fin := out["finance"].(map[string]any)
	if got := decf(t, fin["cash_in"]); got != 100 {
		t.Errorf("cash_in = %v, want 100", got)
	}
	if got := decf(t, fin["consumption"]); got != 0.04 {
		t.Errorf("consumption = %v, want 0.04", got)
	}
	if got := decf(t, fin["float"]); got != 100 {
		t.Errorf("float = %v, want 100 (credit liability)", got)
	}
	if got := fin["topup_paid"].(float64); got != 1 {
		t.Errorf("topup_paid = %v, want 1", got)
	}
	if got := fin["topup_pending"].(float64); got != 1 {
		t.Errorf("topup_pending = %v, want 1", got)
	}
	if got := fin["topup_conversion"].(float64); got != 0.5 {
		t.Errorf("conversion = %v, want 0.5", got)
	}

	usr := out["users"].(map[string]any)
	if got := usr["total_users"].(float64); got < 1 {
		t.Errorf("total_users = %v, want >=1", got)
	}
	if got := usr["active_orgs"].(float64); got != 1 {
		t.Errorf("active_orgs = %v, want 1", got)
	}
	// ARPU = consumption / active_orgs = 0.04
	if got := decf(t, usr["arpu"]); got != 0.04 {
		t.Errorf("arpu = %v, want 0.04", got)
	}

	// 质量：错误分布应含 success=2 / error=1
	qual := out["quality"].(map[string]any)
	errs := qual["errors"].([]any)
	counts := map[string]float64{}
	for _, e := range errs {
		m := e.(map[string]any)
		counts[m["status"].(string)] = m["n"].(float64)
	}
	if counts["success"] != 2 || counts["error"] != 1 {
		t.Errorf("error breakdown = %v, want success:2 error:1", counts)
	}

	// 日序列：长度 = days，cash_in 之和 = 100，cost 之和 = 0.04
	series := out["series"].([]any)
	if len(series) != 14 {
		t.Fatalf("series len = %d, want 14", len(series))
	}
	var sumCash, sumCost float64
	for _, p := range series {
		m := p.(map[string]any)
		sumCash += decf(t, m["cash_in"])
		sumCost += decf(t, m["cost"])
	}
	if sumCash != 100 {
		t.Errorf("series cash_in sum = %v, want 100", sumCash)
	}
	if sumCost != 0.04 {
		t.Errorf("series cost sum = %v, want 0.04", sumCost)
	}
}

// TestAdminOrgsEnriched：组织列表的活跃度聚合（calls/spend/last_active）不被
// members 子查询交叉相乘——JOIN 正确性守卫。
func TestAdminOrgsEnriched(t *testing.T) {
	srv, db := setupRelayServer(t)
	_, orgID := seedUserWithCredit(t, srv, "orgs@gmail.com", "50")

	now := time.Now()
	insertInvocation(t, db, orgID, "e1", "success", "openai", 0.02, now)
	insertInvocation(t, db, orgID, "e2", "success", "openai", 0.01, now)

	w, out := srv.doAdmin(t, "GET", "/api/admin/orgs", nil)
	if w.Code != 200 {
		t.Fatalf("orgs = %d %v", w.Code, out)
	}
	rows := out["orgs"].([]any)
	var found map[string]any
	for _, r := range rows {
		m := r.(map[string]any)
		if m["id"].(float64) == orgID {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatalf("org %v not in list", orgID)
	}
	if got := found["calls"].(float64); got != 2 {
		t.Errorf("calls = %v, want 2 (not multiplied by members)", got)
	}
	if got := decf(t, found["spend"]); got != 0.03 {
		t.Errorf("spend = %v, want 0.03", got)
	}
	if got := decf(t, found["balance"]); got != 50 {
		t.Errorf("balance = %v, want 50", got)
	}
	if got := found["members"].(float64); got != 1 {
		t.Errorf("members = %v, want 1", got)
	}
	if found["last_active"].(string) == "" {
		t.Error("last_active empty, want a timestamp")
	}
}

// TestUsagePerfWindow：验证 /api/usage/perf 只统计近 60 秒（修复 epoch-vs-datetime bug）。
func TestUsagePerfWindow(t *testing.T) {
	srv, db := setupRelayServer(t)

	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "perf@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "perf@gmail.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := login["org_id"].(float64)

	now := time.Now()
	insertInvocation(t, db, orgID, "recent", "success", "openai", 0.01, now.Add(-10*time.Second)) // 窗口内
	insertInvocation(t, db, orgID, "old", "success", "openai", 0.01, now.Add(-5*time.Minute))     // 窗口外

	w, out := srv.do(t, "GET", "/api/usage/perf", token, nil)
	if w.Code != 200 {
		t.Fatalf("perf = %d %v", w.Code, out)
	}
	if got := out["rpm"].(float64); got != 1 {
		t.Errorf("rpm = %v, want 1 (only last-60s row, not the 5-min-old one)", got)
	}
}
