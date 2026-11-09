package video

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Ark callbacks are durable query triggers, never status or billing facts.
// The normal bound path schedules an authoritative GET immediately. A per-task
// watchdog still queries after about two minutes without a usable callback.

const (
	arkCallbackTokenDomain    = "ark-callback-token:v1"
	arkSafetyIdentifierDomain = "ark-safety-identifier:v1"
	arkCallbackHintMaxBytes   = 64 << 10
	stalePendingUsageHorizon  = 7 * 24 * time.Hour
	callbackHintBatch         = 100
	callbackHintWorkers       = 16
	callbackVerifyMaxAttempts = 12
)

func (s *Service) arkCallbackToken(taskID string) string {
	return digest(s.Secret, arkCallbackTokenDomain, taskID)[:32]
}

func (s *Service) arkCallbackURL(taskID string) string {
	// The route is registered under /v1/video (routes.go RegisterRoutes) and
	// Caddy only forwards /v1/* to core — everything else falls through to the
	// SPA file_server, which rejects POST with 405 empty body. The full
	// /v1/video prefix is required; Volcano pushed to a bare /ark-callbacks
	// path and received exactly that 405 until this fix.
	return strings.TrimSuffix(s.ArkCallbackBase, "/") + "/v1/video/ark-callbacks/" + taskID + "/" + s.arkCallbackToken(taskID)
}

func (s *Service) arkSafetyIdentifier(taskID string) string {
	return digest(s.Secret, arkSafetyIdentifierDomain, taskID)[:32]
}

// HandleArkCallback is the public, unauthenticated hint receiver. Nothing in
// the hint touches billing: it only schedules an authoritative query through
// the normal pipeline. The task-unique row and revision are updated atomically.
func (s *Service) HandleArkCallback(c *gin.Context) {
	if s.ArkCallbackBase == "" {
		c.Status(http.StatusNotFound)
		return
	}
	taskID, token := c.Param("task_id"), c.Param("token")
	if len(taskID) > 64 || len(token) != 32 || subtle.ConstantTimeCompare([]byte(token), []byte(s.arkCallbackToken(taskID))) != 1 {
		c.Status(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, arkCallbackHintMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > arkCallbackHintMaxBytes || !json.Valid(body) {
		// 提供方侧的畸形体：直接 ACK 丢弃，避免无意义的 ×3 重发。
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	sum := digest(s.Secret, "ark-callback-body:v1", string(body))
	excerpt := string(body)
	if len(excerpt) > 4096 {
		// Keep the recovery fields intact even when the provider sends a large
		// notification whose id appears after the audit excerpt boundary.
		var identity struct {
			ID               string `json:"id"`
			SafetyIdentifier string `json:"safety_identifier"`
		}
		if json.Unmarshal(body, &identity) == nil {
			if compact, marshalErr := json.Marshal(identity); marshalErr == nil {
				excerpt = string(compact)
			}
		}
		if len(excerpt) > 4096 {
			excerpt = excerpt[:4096]
		}
	}
	// A single upsert keeps the latest notification without a delete/insert
	// gap. A concurrent consumer can acknowledge only the revision it read.
	now := s.Now().UTC()
	hint := CallbackHint{TaskID: taskID, BodyHash: sum, BodyExcerpt: excerpt, ReceivedAt: now, Revision: 1}
	err = s.DB.WithContext(c.Request.Context()).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "task_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"body_hash": sum, "body_excerpt": excerpt, "received_at": now,
			"processed_at": nil, "result": "", "retry_at": nil, "attempts": 0,
			"revision": gorm.Expr("revision + 1"),
		}),
	}).Create(&hint).Error
	if err != nil {
		// Ark can retry terminal callbacks only when delivery did not succeed.
		// The two-minute watchdog still protects tasks after those retries end.
		s.Log.Warn("ark callback hint store unavailable", "task", taskID)
		c.JSON(http.StatusServiceUnavailable, gin.H{"received": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": true})
}

type callbackHintOutcome struct {
	result       string
	retryAfter   time.Duration
	countAttempt bool
}

func callbackRetry(result string, delay time.Duration, count bool) callbackHintOutcome {
	return callbackHintOutcome{result: result, retryAfter: delay, countAttempt: count}
}

// ProcessCallbackHints turns notifications into due authoritative queries.
// Revision-fenced acknowledgment leaves a newer callback pending even if it
// arrives while this batch is processing. Slow unknown-ID verification cannot
// hold up bound tasks or the separate create dispatcher.
func (s *Service) ProcessCallbackHints(ctx context.Context) error {
	if s.ArkCallbackBase == "" {
		return nil
	}
	var hints []CallbackHint
	if err := s.DB.WithContext(ctx).Where("processed_at IS NULL AND (retry_at IS NULL OR retry_at <= UTC_TIMESTAMP(3))").Order("retry_at, id").Limit(callbackHintBatch).Find(&hints).Error; err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, callbackHintWorkers)
	var mu sync.Mutex
	var failures []error
	for _, hint := range hints {
		if err := ctx.Err(); err != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(hint CallbackHint) {
			defer wg.Done()
			defer func() { <-sem }()
			outcome, err := s.processCallbackHint(ctx, hint)
			if finishErr := s.finishCallbackHint(ctx, hint, outcome); finishErr != nil {
				err = errors.Join(err, finishErr)
			}
			if err != nil {
				s.Log.Warn("ark callback hint deferred", "task", hint.TaskID, "result", outcome.result)
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(hint)
	}
	wg.Wait()
	return errors.Join(append(failures, ctx.Err())...)
}

func (s *Service) finishCallbackHint(ctx context.Context, hint CallbackHint, outcome callbackHintOutcome) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := authorityTimeTx(tx)
		if err != nil {
			return err
		}
		updates := map[string]any{"result": outcome.result}
		if outcome.retryAfter > 0 {
			updates["retry_at"] = now.Add(outcome.retryAfter)
			if outcome.countAttempt {
				updates["attempts"] = gorm.Expr("attempts + 1")
			}
		} else {
			updates["processed_at"] = now
			updates["retry_at"] = nil
		}
		// RowsAffected=0 means a newer revision won and still needs processing.
		return tx.Model(&CallbackHint{}).Where("id = ? AND revision = ? AND processed_at IS NULL", hint.ID, hint.Revision).Updates(updates).Error
	})
}

func (s *Service) processCallbackHint(ctx context.Context, hint CallbackHint) (callbackHintOutcome, error) {
	// token 已在端点验证过 task 身份；这里先按 task_id 定位 org（token 校验
	// 已隐含成员关系），再走带租户的加载。
	var located task.Task
	if err := s.DB.WithContext(ctx).Select("org_id", "task_id", "provider").Where("task_id = ?", hint.TaskID).Take(&located).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return callbackHintOutcome{result: "task_missing"}, nil
		}
		return callbackRetry("lookup_unavailable", 10*time.Second, false), err
	}
	t, st, err := loadTask(s.DB.WithContext(ctx), located.OrgID, hint.TaskID, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return callbackHintOutcome{result: "task_missing"}, nil
		}
		return callbackRetry("lookup_unavailable", 10*time.Second, false), err
	}
	if t.Provider != Provider {
		return callbackHintOutcome{result: "task_missing"}, nil
	}
	if t.UpstreamTaskID != "" {
		result, err := s.scheduleCallbackQuery(ctx, t.OrgID, t.TaskID)
		if err != nil {
			return callbackRetry("schedule_unavailable", 10*time.Second, false), err
		}
		if result == "await_binding" || result == "query_inflight" {
			return callbackRetry(result, 5*time.Second, false), nil
		}
		return callbackHintOutcome{result: result}, nil
	}
	if terminal(t.Status) && financialFinal(st.BillingState) {
		return callbackHintOutcome{result: "ack_final"}, nil
	}
	if t.Status != "submission_unknown" {
		if t.Status == "accepted" || t.Status == "submitting" {
			// A callback may win the race with the create receipt. Preserve it
			// until either the upstream ID is bound or the create is unknown.
			return callbackRetry("await_binding", 5*time.Second, false), nil
		}
		return callbackHintOutcome{result: "ack_state"}, nil
	}
	// body 必须携带任务 id；safety_identifier 出现时必须与我们派发的派生值一致。
	var body struct {
		ID               string `json:"id"`
		SafetyIdentifier string `json:"safety_identifier"`
	}
	if json.Unmarshal([]byte(hint.BodyExcerpt), &body) != nil {
		return callbackHintOutcome{result: "body_unreadable"}, nil
	}
	if !ark.UpstreamIDMatch(body.ID) {
		return callbackHintOutcome{result: "no_id"}, nil
	}
	want := s.arkSafetyIdentifier(hint.TaskID)
	if body.SafetyIdentifier != "" && subtle.ConstantTimeCompare([]byte(body.SafetyIdentifier), []byte(want)) != 1 {
		s.Log.Warn("ark callback safety_identifier mismatch", "task", hint.TaskID)
		return callbackHintOutcome{result: "forged_si"}, nil
	}
	// 证明 ③：先用我们的凭据对回显 id 做一次权威查询——查不到/身份不符不绑定。
	client, err := s.clientFor(ctx, t, st)
	if err != nil {
		return callbackRetry("verify_unavailable", 10*time.Second, false), err
	}
	allowed := false
	if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var gateErr error
		allowed, gateErr = s.rateTx(tx, st.ProviderScopeID, "query")
		return gateErr
	}); err != nil {
		return callbackRetry("query_gate_unavailable", 10*time.Second, false), err
	}
	if !allowed {
		return callbackRetry("query_gate_wait", 3*time.Second, false), nil
	}
	started := time.Now()
	response := client.Query(ctx, body.ID)
	s.observeAdapterCall("query", response, started)
	if response.Outcome != ark.OutcomeAccepted || response.Observation == nil {
		if hint.Attempts+1 >= callbackVerifyMaxAttempts {
			return callbackHintOutcome{result: "verify_exhausted"}, nil
		}
		return callbackRetry("verify_retry", callbackVerifyDelay(hint.Attempts), true), nil
	}
	if response.Observation.TaskID != body.ID {
		return callbackHintOutcome{result: "verify_mismatch"}, nil
	}
	if err := s.bindUnknownUpstreamTx(ctx, t, st, body.ID); err != nil {
		if err == ErrConflict {
			return callbackRetry("binding_conflict", 5*time.Second, false), nil
		}
		s.Log.Warn("ark callback bind failed", "task", hint.TaskID, "err", err)
		return callbackRetry("bind_failed", 10*time.Second, false), err
	}
	s.Log.Info("ark callback bound unknown task", "task", hint.TaskID, "upstream", body.ID)
	return callbackHintOutcome{result: "bound"}, nil
}

func callbackVerifyDelay(attempts uint32) time.Duration {
	if attempts > 6 {
		attempts = 6
	}
	return min(time.Duration(5<<attempts)*time.Second, 5*time.Minute)
}

func (s *Service) scheduleCallbackQuery(ctx context.Context, org uint64, id string) (string, error) {
	result := "ack_state"
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t, st, err := loadTask(tx, org, id, true)
		if err != nil {
			return err
		}
		if t.Provider != Provider {
			result = "task_missing"
			return nil
		}
		if t.UpstreamTaskID == "" {
			result = "await_binding"
			return nil
		}
		if terminal(t.Status) && financialFinal(st.BillingState) {
			result = "ack_final"
			return nil
		}
		if t.Status != "queued" && t.Status != "running" && st.BillingState != "pending_usage" {
			return nil
		}
		now, err := authorityTimeTx(tx)
		if err != nil {
			return err
		}
		if st.QueryLeaseUntil != nil && st.QueryLeaseUntil.After(now) {
			// The in-flight GET may have started before this callback. Wait for
			// its receipt, then schedule another GET if the task still needs it.
			result = "query_inflight"
			return nil
		}
		if st.NextPollAt.IsZero() || st.NextPollAt.After(now) {
			if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", org, id).Update("next_poll_at", now).Error; err != nil {
				return err
			}
		}
		result = "query_due"
		return nil
	})
	return result, err
}

// bindUnknownUpstreamTx binds the provider-echoed upstream id onto a
// submission_unknown task and hands control back to the poll pipeline (the
// next authoritative poll applies the real observation through the reducer).
func (s *Service) bindUnknownUpstreamTx(ctx context.Context, t task.Task, st State, upstreamID string) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, freshState, err := loadTask(tx, t.OrgID, t.TaskID, true)
		if err != nil {
			return err
		}
		if fresh.UpstreamTaskID != "" || freshState.ProviderScopeID != st.ProviderScopeID {
			return ErrConflict
		}
		binding := UpstreamBinding{ProviderScopeID: st.ProviderScopeID, UpstreamTaskID: upstreamID, OrgID: t.OrgID, TaskID: t.TaskID, CreatedAt: s.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
			return err
		}
		var existing UpstreamBinding
		if err := locked(tx).Where("provider_scope_id = ? AND upstream_task_id = ?", st.ProviderScopeID, upstreamID).First(&existing).Error; err != nil {
			return err
		}
		if existing.OrgID != t.OrgID || existing.TaskID != t.TaskID {
			return ErrConflict
		}
		if err := tx.Model(&task.Task{}).Where("org_id = ? AND task_id = ? AND upstream_task_id = ''", t.OrgID, t.TaskID).Update("upstream_task_id", upstreamID).Error; err != nil {
			return err
		}
		now := s.Now().UTC()
		return tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"next_poll_at": now, "updated_at": now}).Error
	})
}

// ReleaseStalePendingUsage is the P0 floor: terminal tasks whose billing
// never resolved within the horizon are refunded. Provider usage evidence
// never arrived (no upstream id, or usage never seen); failed generations
// bill only in rare partial-generation cases (2026-10 reconciliation), so
// customer-safe release wins over an eternal freeze. The monthly
// reconciliation still sees every release in the journal.
func (s *Service) ReleaseStalePendingUsage(ctx context.Context) error {
	cutoff := s.Now().UTC().Add(-stalePendingUsageHorizon)
	var rows []task.Task
	err := s.DB.WithContext(ctx).Table("tasks").
		Joins("JOIN video_task_states v ON v.org_id = tasks.org_id AND v.task_id = tasks.task_id").
		Where("tasks.status IN ? AND v.billing_state = 'pending_usage' AND v.deleted_at IS NULL AND COALESCE(tasks.finished_at, tasks.created_at) < ?", []string{"failed", "submission_unknown", "expired"}, cutoff).
		Order("tasks.created_at, tasks.id").Limit(100).Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, stale := range rows {
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			t, st, err := loadTask(tx, stale.OrgID, stale.TaskID, true)
			if err != nil {
				return err
			}
			if t.Status != stale.Status || st.BillingState != "pending_usage" {
				return nil // 并发变化：留给下一轮。
			}
			now := s.Now().UTC()
			if err := s.afterAssetsTerminalTx(tx, t.OrgID, t.TaskID); err != nil {
				return err
			}
			evidence := digest(s.Secret, "stale-pending-usage:v1", t.TaskID, now.Format(time.RFC3339Nano))
			if _, err := videobilling.ReleaseConfirmedTx(tx, videobilling.ReleaseInput{OrgID: t.OrgID, TaskID: t.TaskID, EvidenceID: evidence, Reason: videobilling.ReleaseProviderNoCharge, Confirmed: true, Now: now}); err != nil {
				return err
			}
			if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"billing_state": "released", "next_poll_at": nil, "updated_at": now}).Error; err != nil {
				return err
			}
			st.BillingState = "released"
			return appendEventTx(tx, &t, &st, "task.billing_released", t.Status, t.Status, st.CreateRequestID, evidence, now)
		})
		if err != nil {
			s.Log.Warn("stale pending_usage release failed", "task", stale.TaskID, "err", err)
			continue
		}
		s.Log.Info("stale pending_usage released", "task", stale.TaskID)
	}
	return nil
}
