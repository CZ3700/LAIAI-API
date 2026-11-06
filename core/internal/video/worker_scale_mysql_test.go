package video

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// 回归（2026-11-06 起量准备，260 任务/分钟）：一轮 tick 必须把同 scope 的积压
// 一批派发（此前每 2s 只派 1 单 = 30 单/分钟，是起量的第一瓶颈）。
func TestMySQLDispatchDrainsBacklogInOneTick(t *testing.T) {
	f := mysqlFixture(t)
	const n = 5
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		v := f.create(t, fmt.Sprintf("dispatch-batch-%d", i))
		ids = append(ids, v.ID)
	}
	// 每次提交返回唯一上游 id，避免共享绑定的既有语义。
	// 共享测试库里有大量历史遗留的 pending 操作：先把它们的候选资格移出
	// （非本测试 org 的 pending create 一律置为 cancelled，共享库不做清理），
	// 再把 create 闸间隔降到 10ms 消除边界竞争，然后断言本测试的 5 个任务
	// 在一轮 tick 内全部离开 accepted。
	must(t, f.s.DB.Exec("UPDATE video_operations SET status = 'cancelled' WHERE kind = 'create_task' AND status = 'pending' AND org_id <> ?", f.key.OrgID).Error)
	var st0 State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, ids[0]).First(&st0).Error)
	must(t, f.s.DB.Exec("INSERT INTO video_provider_rates (scope_id, action, next_allowed_at, interval_ms) VALUES (?, 'create', UTC_TIMESTAMP(3), 10) ON DUPLICATE KEY UPDATE interval_ms = 10, next_allowed_at = UTC_TIMESTAMP(3)", st0.ProviderScopeID).Error)
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			seq := f.posts.Add(1)
			return reply(200, fmt.Sprintf(`{"id":"up_unique_%d"}`, seq)), nil
		}
		return reply(200, `{}`), nil
	}
	before := f.posts.Load()
	must(t, f.s.ProcessDispatchOnce(context.Background()))
	if delta := f.posts.Load() - before; delta < n {
		t.Fatalf("one tick dispatched only %d of %d backlog creates", delta, n)
	}
	for _, id := range ids {
		var row task.Task
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&row).Error)
		if row.Status == "accepted" {
			t.Fatalf("task %s still queued behind the backlog after one tick", id)
		}
	}
}

// 回归：上游 429（限流）不是客户侧失败——任务回 accepted、旧操作记
// upstream_rate_limited、新 pending 操作重派；账务 hold 保持冻结；最终正常
// 提交成功后任务照常入队。
func TestMySQLRateLimitedSubmitRequeuesInsteadOfFailing(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "rate-limited-requeue")
	first := true
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && first {
			first = false
			return reply(429, `{"error":{"code":"ModelAccountConcurrentRequestsLimitExceeded","message":"concurrency full"}}`), nil
		}
		if r.Method == http.MethodPost {
			return reply(200, `{"id":"up_retry_ok"}`), nil
		}
		return reply(200, `{"id":"up_retry_ok","status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999}}`), nil
	}
	f.dispatch(t, v.ID)

	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if row.Status != "accepted" {
		t.Fatalf("rate-limited task settled as %s instead of requeueing to accepted", row.Status)
	}
	if h := f.hold(t, v.ID); h.Status != ledger.HoldActive {
		t.Fatal("rate-limited submit released or captured the hold")
	}
	var rejectedCount, pendingCount int64
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = 'create_task' AND status = 'rejected' AND error_code = 'upstream_rate_limited'", f.key.OrgID, v.ID).Count(&rejectedCount).Error)
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND kind = 'create_task' AND status = 'pending'", f.key.OrgID, v.ID).Count(&pendingCount).Error)
	if rejectedCount != 1 || pendingCount != 1 {
		t.Fatalf("requeue operations wrong: rejected=%d pending=%d", rejectedCount, pendingCount)
	}

	// 闸后重派：直接处理重派出的 pending 操作（共享库的历史积压会占据批量
	// 候选的名额，这里点名本任务的新操作）。
	var op2 Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = 'create_task' AND status = 'pending'", f.key.OrgID, v.ID).First(&op2).Error)
	beforeRetry := f.posts.Load()
	// create 闸间隔 100ms（0147）：首次派发刚消耗一个时隙，测试把间隔降到
	// 1ms 并立即放行，重派零等待（生产由 2s tick 自然间隔）。
	var st0 State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st0).Error)
	must(t, f.s.DB.Exec("UPDATE video_provider_rates SET interval_ms = 1, next_allowed_at = UTC_TIMESTAMP(3) WHERE scope_id = ? AND action = 'create'", st0.ProviderScopeID).Error)
	time.Sleep(5 * time.Millisecond)
	must(t, f.s.processOperation(context.Background(), op2))
	if f.posts.Load() <= beforeRetry {
		var op2After Operation
		must(t, f.s.DB.Where("org_id = ? AND id = ?", op2.OrgID, op2.ID).First(&op2After).Error)
		var rowAfter task.Task
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&rowAfter).Error)
		t.Fatalf("requeued operation was not dispatched: op(status=%s ver=%d err=%s lease=%v payload=%s) task=%s", op2After.Status, op2After.Version, op2After.ErrorCode, op2After.LeaseUntil, op2After.PayloadRef, rowAfter.Status)
	}
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if row.Status != "queued" {
		t.Fatalf("retry did not queue the task: %s", row.Status)
	}
}

// 闸间隔可配置（interval_ms）：100ms 行第一次放行、紧随的第二次拒绝、
// 间隔过后再放行——这是 260 任务/分钟容量设计的闸基础。
func TestMySQLProviderRateIntervalConfigurable(t *testing.T) {
	f := mysqlFixture(t)
	var st State
	v := f.create(t, "rate-interval-config")
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	must(t, f.s.DB.Exec("INSERT INTO video_provider_rates (scope_id, action, next_allowed_at, interval_ms) VALUES (?, 'create', UTC_TIMESTAMP(3), 100) ON DUPLICATE KEY UPDATE interval_ms = 100, next_allowed_at = UTC_TIMESTAMP(3)", st.ProviderScopeID).Error)
	err := f.s.DB.Transaction(func(tx *gorm.DB) error { _, e := f.s.rateTx(tx, st.ProviderScopeID, "create"); return e })
	must(t, err)
	err = f.s.DB.Transaction(func(tx *gorm.DB) error {
		allowed, e := f.s.rateTx(tx, st.ProviderScopeID, "create")
		if e != nil {
			return e
		}
		if allowed {
			return fmt.Errorf("interval not enforced")
		}
		return nil
	})
	must(t, err)
	time.Sleep(120 * time.Millisecond)
	err = f.s.DB.Transaction(func(tx *gorm.DB) error {
		allowed, e := f.s.rateTx(tx, st.ProviderScopeID, "create")
		if e != nil {
			return e
		}
		if !allowed {
			return fmt.Errorf("interval never opened")
		}
		return nil
	})
	must(t, err)
}
