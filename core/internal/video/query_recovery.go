package video

import (
	"context"
)

func (s *Service) recoverQueryReceipts(ctx context.Context) error {
	_, err := s.recoverQueryReceiptsProgress(ctx)
	return err
}

func (s *Service) recoverQueryReceiptsProgress(ctx context.Context) (recoveryLaneResult, error) {
	var result recoveryLaneResult
	var rows []QueryReceipt
	if err := s.recoveryQueryCandidates(ctx).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&rows).Error; err != nil {
		result.Failed++
		return result, safeRecoveryErrors(err, ctx.Err())
	}
	result.Selected = int64(len(rows))
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, safeRecoveryErrors(append(failures, err)...)
		}
		applied, err := s.applyQueryReceiptProgress(ctx, row)
		applied.countIn(&result, err)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return result, safeRecoveryErrors(append(failures, ctx.Err())...)
}

func (r queryRecoveryResult) countIn(result *recoveryLaneResult, err error) {
	switch r.disposition {
	case queryRecoveryApplied:
		result.Applied++
	case queryRecoveryDeferred:
		result.Deferred++
	case queryRecoveryReview:
		result.Review++
	case queryRecoveryNoop:
		if err == nil {
			result.Noop++
		}
	}
	if err != nil {
		result.Failed++
	}
}
