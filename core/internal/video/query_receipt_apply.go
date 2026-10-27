package video

import (
	"context"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type queryObservation struct {
	TaskID             string     `json:"task_id"`
	Model              string     `json:"model"`
	Status             ark.Status `json:"status"`
	CompletionTokens   *int64     `json:"completion_tokens"`
	TotalTokens        *int64     `json:"total_tokens"`
	HasError           bool       `json:"has_error"`
	DeliveryPayloadRef string     `json:"delivery_payload_ref,omitempty"`
}

func (s *Service) recoveryQueryCandidates(ctx context.Context) *gorm.DB {
	return queryGenerationEligible(s.authorityQuery(ctx).Table("video_query_receipts AS r").Select("r.*").Joins("JOIN video_task_states v ON v.org_id = r.org_id AND v.task_id = r.task_id"))
}

func (s *Service) applyQueryReceipt(ctx context.Context, receipt QueryReceipt) error {
	_, err := s.applyQueryReceiptProgress(ctx, receipt)
	return err
}

func (s *Service) applyQueryReceiptProgress(ctx context.Context, receipt QueryReceipt) (queryRecoveryResult, error) {
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	var observed queryRecoveryAttempt
	var compatibility queryCompatibilityAttempt
	requested := receipt
	now := s.Now().UTC()
	err := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		t, st, e := LockBillingTaskTx(tx, receipt.OrgID, receipt.TaskID)
		if e != nil {
			if recoveryOnlyErrors(safeRecoveryErrors(e, ctx.Err()), videoruntime.ErrUnsupported) {
				var tokenErr error
				compatibility, tokenErr = observeQueryCompatibilityTx(tx, t, st, requested)
				return safeRecoveryErrors(e, tokenErr, ctx.Err())
			}
			return e
		}
		var current QueryReceipt
		if e = locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", requested.OrgID, requested.TaskID, requested.AttemptID).First(&current).Error; e != nil {
			return e
		}
		receipt = current
		if t.OrgID != requested.OrgID || t.TaskID != requested.TaskID || receipt.OrgID != requested.OrgID || receipt.TaskID != requested.TaskID || receipt.AttemptID != requested.AttemptID {
			return ErrConflict
		}
		// An existing task barrier survives later corruption, including a V1
		// rewrite. It must not be erased by either reducer or a late diagnostic.
		if receipt.ApplyErrorCode == queryFinancialConflictCode {
			return errQueryFinancialConflict
		}
		if e = requireNoTaskSettlementConflictTx(tx, st); e != nil {
			return e
		}
		if e = requireQueryReceiptRuntime(st, receipt); e != nil {
			return e
		}
		if receipt.AppliedAt != nil {
			return nil
		}
		if receipt.RecoveryDueAt == nil {
			return errQueryRecoveryReview
		}
		phase, e := queryPhaseOf(st, receipt)
		if e != nil {
			return e
		}
		if phase == queryPhaseSettlementV2 {
			// A second G call cannot take B's token, touch progress or perform money.
			return nil
		}
		if phase != queryPhaseAtomicV1 && phase != queryPhaseGenerationV2 {
			return ErrState
		}
		if observed, e = observeQueryPhaseRecovery(t, st, receipt); e != nil {
			return e
		}
		if e = requireNoCreateInboxReviewTx(tx, receipt.OrgID, receipt.TaskID); e != nil {
			return e
		}
		if phase == queryPhaseAtomicV1 {
			result, e = s.applyQueryAtomicTx(tx, &t, &st, receipt, now)
		} else {
			result, e = s.applyQueryGenerationTx(tx, &t, &st, receipt)
		}
		return e
	})
	if err == nil {
		if result.code == queryFinancialConflictCode {
			return result, safeRecoveryErrors(errQueryFinancialConflict)
		}
		return result, nil
	}
	// Even a successful reducer is not committed evidence if COMMIT failed.
	result = queryRecoveryResult{disposition: queryRecoveryNoop}
	cause := safeRecoveryErrors(err, ctx.Err())
	if compatibility.identity.valid {
		result, progressErr := s.recordQueryCompatibilityFailure(ctx, compatibility, cause)
		return result, safeRecoveryErrors(cause, progressErr)
	}
	if !observed.valid {
		return result, cause
	}
	result, progressErr := s.recordQueryRecoveryFailure(ctx, observed, cause)
	return result, safeRecoveryErrors(cause, progressErr)
}
