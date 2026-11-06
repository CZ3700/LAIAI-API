package video

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
)

// 回归（2026-11-06 CPU 审计）：终态且账务完结的任务必须把 next_poll_at 置 NULL。
// 旧数据把时间戳留在过去，轮询候选查询的扫描范围随历史只增不减（8,000+ 行/2s）。
func TestMySQLFinanciallyFinalTaskStopsPollScheduling(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "poll-done-settled")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	if st.BillingState != "settled" {
		t.Fatalf("billing %s", st.BillingState)
	}
	var n int64
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ? AND next_poll_at IS NOT NULL", f.key.OrgID, v.ID).Count(&n).Error)
	if n != 0 {
		t.Fatal("settled task still carries a poll schedule")
	}
	// 终态行必须从候选查询里消失（全表扫描范围随之收缩）。
	var rows []State
	must(t, f.s.pendingQueryCandidates(context.Background()).Where("v.task_id = ?", v.ID).Scan(&rows).Error)
	if len(rows) != 0 {
		t.Fatal("financially final task is still a poll candidate")
	}
}

// 派发被上游 400 拒绝：failed+released 同样必须停排（这是线上量最大的失败路径）。
func TestMySQLRejectedTaskStopsPollScheduling(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "poll-done-rejected")
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			return reply(400, `{"error":{"code":"InvalidParameter","message":"bad"}}`), nil
		}
		return reply(200, `{}`), nil
	}
	f.dispatch(t, v.ID)
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	if st.BillingState != "released" {
		t.Fatalf("billing %s", st.BillingState)
	}
	var n int64
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ? AND next_poll_at IS NOT NULL", f.key.OrgID, v.ID).Count(&n).Error)
	if n != 0 {
		t.Fatal("rejected task still carries a poll schedule")
	}
}

// 被 1 req/s 上游闸拒绝的候选必须顺延 next_poll_at，而不是留在过去让同一批
// 40 行每个 tick 原地重选（线上 20 事务/秒的空转来源）。
func TestMySQLRateRefusedPollReschedules(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "poll-refused-reschedule")
	f.dispatch(t, v.ID)
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	// 占住 query 闸：next_allowed_at 推到 1 小时后，pollTask 必被拒。
	must(t, f.s.DB.Exec("INSERT INTO video_provider_rates (scope_id, action, next_allowed_at) VALUES (?, 'query', UTC_TIMESTAMP(3) + INTERVAL 1 HOUR) ON DUPLICATE KEY UPDATE next_allowed_at = UTC_TIMESTAMP(3) + INTERVAL 1 HOUR", st.ProviderScopeID).Error)
	gets := f.gets.Load()
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	if f.gets.Load() != gets {
		t.Fatal("refused poll still queried upstream")
	}
	var after State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&after).Error)
	if !after.NextPollAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("refused poll did not reschedule: %s", after.NextPollAt)
	}
}

// 终态任务的补账轮询（pending_usage）用慢节拍（~60s），把上游 1 req/s 闸让给
// 在途任务；且它不算"账务完结"，schedule 必须保留。
func TestMySQLTerminalPendingUsagePollBacksOff(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "poll-terminal-backoff")
	f.dispatch(t, v.ID)
	var dispatched task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&dispatched).Error)
	up := dispatched.UpstreamTaskID
	if up == "" {
		t.Fatal("dispatch did not bind an upstream id")
	}
	// 直接构造线上真实存量形态：failed + pending_usage（等待补账证据）。
	must(t, f.s.DB.Exec("UPDATE tasks SET status = 'failed' WHERE org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Error)
	must(t, f.s.DB.Exec("UPDATE video_task_states SET billing_state = 'pending_usage' WHERE org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Error)
	// 上游仍报 running：观察不落账，只验证重排节拍。
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			body, _ := json.Marshal(map[string]any{"id": up, "status": "running"})
			return reply(200, string(body)), nil
		}
		return reply(200, `{"id":"`+up+`"}`), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if row.Status != "failed" {
		t.Fatalf("status %s", row.Status)
	}
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	if st.BillingState != "pending_usage" {
		t.Fatalf("billing %s", st.BillingState)
	}
	if st.NextPollAt.IsZero() {
		t.Fatal("pending_usage task lost its poll schedule")
	}
	if wait := time.Until(st.NextPollAt); wait < 30*time.Second || wait > 2*time.Minute {
		t.Fatalf("terminal pending_usage poll interval = %s, want ~60s", wait)
	}
}
