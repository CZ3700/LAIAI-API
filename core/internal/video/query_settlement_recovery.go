package video

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const querySettlementTimeout = 2 * time.Second

// ProcessSettlementOnce is a separate zero-network job. It does not append a
// fifth lane to S1's four-lane recovery cycle or expand either batch limit.
func (s *Service) ProcessSettlementOnce(ctx context.Context) (err error) {
	finish := s.metrics().startCycleKind(workerCycleSettlement)
	defer func() { finish(err) }()
	round, cancel := context.WithTimeout(context.WithValue(ctx, recoveryClockBudgetKey{}, true), querySettlementTimeout)
	defer cancel()
	result, err := s.recoverQuerySettlementsProgress(round)
	if cause := round.Err(); cause != nil && !errors.Is(err, cause) {
		err = safeRecoveryErrors(err, cause)
	}
	s.metrics().observeRecoveryLane(recoveryQuerySettlement, result, err)
	return err
}

func (s *Service) recoverySettlementCandidates(ctx context.Context) *gorm.DB {
	q := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	if q.Dialector == nil || q.Dialector.Name() != "mysql" {
		q.AddError(ErrUnavailable)
	}
	q = q.Table("video_query_receipts AS r").Select("r.*").Joins("JOIN video_task_states v ON v.org_id = r.org_id AND v.task_id = r.task_id")
	return querySettlementEligible(q)
}

func (s *Service) recoverQuerySettlementsProgress(ctx context.Context) (recoveryLaneResult, error) {
	var result recoveryLaneResult
	var rows []QueryReceipt
	if err := s.recoverySettlementCandidates(ctx).Order("r.recovery_due_at, r.received_at, r.attempt_id").Limit(40).Find(&rows).Error; err != nil {
		result.Failed++
		return result, safeRecoveryErrors(err, ctx.Err())
	}
	result.Selected = int64(len(rows))
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, safeRecoveryErrors(append(failures, err)...)
		}
		applied, err := s.applyQuerySettlement(ctx, row)
		applied.countIn(&result, err)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return result, safeRecoveryErrors(append(failures, ctx.Err())...)
}
