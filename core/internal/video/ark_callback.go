package video

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Ark 回调通道（2026-11-06 起量准备）：火山状态回调只是“通知”，不是事实源。
// 端点做四步廉价校验（特性开关、task_id 形态、HMAC token、幂等落库）后立即
// ACK；消费端对未绑定任务执行三重证明恢复（token ① + safety_identifier 回显
// ② + 权威 GET ③），绑定后交给轮询管道做权威结算。已绑定任务的 hint 直接
// ACK 丢弃——轮询是地面真相层，回调故障等于回到纯轮询现状，零堆积风险。

const (
	arkCallbackTokenDomain    = "ark-callback-token:v1"
	arkSafetyIdentifierDomain = "ark-safety-identifier:v1"
	arkCallbackHintMaxBytes   = 64 << 10
	stalePendingUsageHorizon  = 7 * 24 * time.Hour
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
// the normal pipeline. Invalid tokens cost one row lookup; duplicate hints
// collapse on the (task_id, body_hash) unique key.
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
		excerpt = excerpt[:4096]
	}
	// One row per task, always the latest callback: delete-then-insert keeps
	// the table O(tasks) instead of O(callbacks) without a DDL change. The
	// unique key (task_id, body_hash) still collapses exact duplicates.
	res := s.DB.WithContext(c.Request.Context()).
		Where("task_id = ?", taskID).
		Delete(&CallbackHint{})
	if res.Error == nil {
		hint := CallbackHint{TaskID: taskID, BodyHash: sum, BodyExcerpt: excerpt, ReceivedAt: s.Now().UTC()}
		res = s.DB.WithContext(c.Request.Context()).Create(&hint)
	}
	if res.Error == nil {
		s.Log.Info("ark callback hint updated", "task", taskID)
	}
	// 存储抖动也 ACK：正确性归轮询管道（回调只是通知，不是事实源）。
	c.JSON(http.StatusOK, gin.H{"received": true})
}

// ProcessCallbackHints consumes received hints. Bound/terminal tasks are
// acknowledged and dropped; submission_unknown tasks run the three-proof
// recovery and bind the upstream id for the normal poll pipeline to settle.
func (s *Service) ProcessCallbackHints(ctx context.Context) error {
	if s.ArkCallbackBase == "" {
		return nil
	}
	var hints []CallbackHint
	if err := s.DB.WithContext(ctx).Where("processed_at IS NULL").Order("id").Limit(10).Find(&hints).Error; err != nil {
		return err
	}
	for _, hint := range hints {
		result := s.processCallbackHint(ctx, hint)
		s.DB.WithContext(ctx).Model(&CallbackHint{}).Where("id = ? AND processed_at IS NULL", hint.ID).
			Updates(map[string]any{"processed_at": s.Now().UTC(), "result": result})
	}
	return nil
}

func (s *Service) processCallbackHint(ctx context.Context, hint CallbackHint) string {
	// token 已在端点验证过 task 身份；这里先按 task_id 定位 org（token 校验
	// 已隐含成员关系），再走带租户的加载。
	var located task.Task
	if err := s.DB.WithContext(ctx).Select("org_id", "task_id", "provider").Where("task_id = ?", hint.TaskID).Take(&located).Error; err != nil {
		return "task_missing"
	}
	t, st, err := loadTask(s.DB.WithContext(ctx), located.OrgID, hint.TaskID, false)
	if err != nil {
		return "task_missing"
	}
	if t.Provider != Provider {
		return "task_missing"
	}
	if t.UpstreamTaskID != "" {
		return "ack_bound" // 正常任务：轮询拥有它，hint 仅 ACK。
	}
	if terminal(t.Status) && financialFinal(st.BillingState) {
		return "ack_final"
	}
	if t.Status != "submission_unknown" {
		return "ack_state"
	}
	// body 必须携带任务 id；safety_identifier 出现时必须与我们派发的派生值一致。
	var body struct {
		ID               string `json:"id"`
		SafetyIdentifier string `json:"safety_identifier"`
	}
	if json.Unmarshal([]byte(hint.BodyExcerpt), &body) != nil {
		return "body_unreadable"
	}
	if !ark.UpstreamIDMatch(body.ID) {
		return "no_id"
	}
	want := s.arkSafetyIdentifier(hint.TaskID)
	if body.SafetyIdentifier != "" && subtle.ConstantTimeCompare([]byte(body.SafetyIdentifier), []byte(want)) != 1 {
		s.Log.Warn("ark callback safety_identifier mismatch", "task", hint.TaskID)
		return "forged_si"
	}
	// 证明 ③：先用我们的凭据对回显 id 做一次权威查询——查不到/身份不符不绑定。
	client, err := s.clientFor(ctx, t, st)
	if err != nil {
		return "verify_unavailable"
	}
	response := client.Query(ctx, body.ID)
	if response.Outcome != ark.OutcomeAccepted || response.Observation == nil || response.Observation.TaskID != body.ID {
		return "verify_failed"
	}
	if err := s.bindUnknownUpstreamTx(ctx, t, st, body.ID); err != nil {
		if err == ErrConflict {
			return "binding_conflict"
		}
		s.Log.Warn("ark callback bind failed", "task", hint.TaskID, "err", err)
		return "bind_failed"
	}
	s.Log.Info("ark callback bound unknown task", "task", hint.TaskID, "upstream", body.ID)
	return "bound"
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
