package video

import (
	"context"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A disposition is published only after the enclosing transaction confirms
// commit. A nil return from a replay/claim check is not an application.
type recoveryDisposition uint8

const (
	recoveryUnchanged recoveryDisposition = iota
	recoveryApplied
	recoveryDeferred
	recoveryReview
)

func (r *recoveryLaneResult) record(disposition recoveryDisposition, err error) {
	switch disposition {
	case recoveryApplied:
		r.Applied++
	case recoveryDeferred:
		r.Deferred++
	case recoveryReview:
		r.Review++
	default:
		if err == nil {
			r.Noop++
		}
	}
	if err != nil {
		r.Failed++
	}
}

// Only these private safe aggregates may be traversed. An arbitrary wrapper
// can carry storage/authority meaning of its own, even if its leaf is known.
func recoveryOnlyErrors(err error, allowed ...error) bool {
	remaining := 64
	var visit func(error) bool
	visit = func(current error) bool {
		remaining--
		if current == nil || remaining < 0 {
			return false
		}
		if wrapped, ok := current.(*persistedRecoveryErrors); ok {
			if wrapped == nil || len(wrapped.causes) == 0 {
				return false
			}
			for _, cause := range wrapped.causes {
				if !visit(cause) {
					return false
				}
			}
			return true
		}
		for _, target := range allowed {
			if current == target {
				return true
			}
		}
		return false
	}
	return visit(err)
}

func recoveryPermanentCreateError(err error) bool {
	return recoveryOnlyErrors(err, ErrConflict, videoruntime.ErrUnsupported,
		invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoAccessDenied,
		invocation.ErrVideoNotFound, invocation.ErrVideoInvalid,
		invocation.ErrVideoCorrupt, invocation.ErrVideoConflict)
}

func (s *Service) recoveryDB(ctx context.Context) *gorm.DB {
	return s.DB.Session(&gorm.Session{Context: ctx, Logger: logger.Discard})
}

// This CAS cannot rewrite a successful consumer or a newer diagnostic. The
// single UPDATE is atomic; only its confirmed affected row counts as deferred.
func (s *Service) deferInboxRecovery(ctx context.Context, row ReceiptApplication) (recoveryDisposition, error) {
	if err := ctx.Err(); err != nil {
		return recoveryUnchanged, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	result := s.recoveryDB(ctx).Model(&ReceiptApplication{}).
		Where("org_id = ? AND receipt_id = ? AND version = ? AND state IN ?", row.OrgID, row.ReceiptID, row.Version, []string{"pending", "blocked"}).
		Updates(map[string]any{"next_attempt_at": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 30 SECOND)"), "version": gorm.Expr("version + 1")})
	if result.Error != nil {
		return recoveryUnchanged, result.Error
	}
	if result.RowsAffected == 1 {
		return recoveryDeferred, nil
	}
	return recoveryUnchanged, nil
}

func (s *Service) recoverInboxApplications(ctx context.Context, rows []ReceiptApplication, result recoveryLaneResult, prior error, apply func(context.Context, uint64, string, *Operation) (recoveryDisposition, error)) (recoveryLaneResult, error) {
	failures := []error{prior}
	result.Selected += int64(len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, safeRecoveryErrors(append(failures, err)...)
		}
		disposition, err := apply(ctx, row.OrgID, row.ReceiptID, nil)
		if err != nil {
			failures = append(failures, err)
			// A committed mark already advances progress. On rollback, retain
			// the original error even if this separate backoff also fails.
			deferred, backoffErr := s.deferInboxRecovery(ctx, row)
			if disposition == recoveryUnchanged {
				disposition = deferred
			}
			failures = append(failures, backoffErr)
		}
		result.record(disposition, err)
	}
	return result, safeRecoveryErrors(failures...)
}
