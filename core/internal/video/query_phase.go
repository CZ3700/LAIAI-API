package video

import (
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

type queryApplyPhase uint8

const (
	queryPhaseAtomicV1 queryApplyPhase = iota + 1
	queryPhaseGenerationV2
	queryPhaseSettlementV2
	queryPhaseComplete
	queryPhaseReview
)

const (
	querySettlementNone             = "none"
	querySettlementUsage            = "usage"
	querySettlementProviderNoCharge = "provider_nocharge"
)

// A phase is structure, not permission to settle. In particular a damaged
// stored digest must reach the B validator instead of vanishing from its queue.
func queryPhaseOf(st State, current QueryReceipt) (queryApplyPhase, error) {
	if err := requireQueryReceiptRuntime(st, current); err != nil {
		return 0, err
	}
	if current.AppliedAt != nil {
		return queryPhaseComplete, nil
	}
	if current.RecoveryDueAt == nil {
		return queryPhaseReview, nil
	}
	mode, err := videoruntime.ResolveQueryApplication(stateRuntime(st), current.ObservationSchemaVersion)
	if err != nil {
		return 0, err
	}
	if mode == videoruntime.QueryApplicationAtomicV1 {
		return queryPhaseAtomicV1, nil
	}
	if current.GenerationAppliedAt == nil {
		if current.SettlementKind == "" && current.SettlementEvidenceSHA256 == "" {
			return queryPhaseGenerationV2, nil
		}
		return 0, ErrState
	}
	if current.GenerationAppliedAt.Year() < 1000 || (current.SettlementKind != querySettlementUsage && current.SettlementKind != querySettlementProviderNoCharge) {
		return 0, ErrState
	}
	if st.SettlementReceiptID == nil || *st.SettlementReceiptID != current.AttemptID || current.AttemptID == "" {
		return 0, ErrConflict
	}
	return queryPhaseSettlementV2, nil
}

// Fixed r/v aliases belong to the video candidate builders. These scopes add
// eligibility only; callers retain ownership of ordering, one LIMIT and tx.
func queryGenerationEligible(q *gorm.DB) *gorm.DB {
	return supportedQueryReceiptRuntime(q).Where(noCreateInboxReview).Where(noTaskSettlementConflict).
		Where("r.applied_at IS NULL AND r.recovery_due_at IS NOT NULL AND r.recovery_due_at <= UTC_TIMESTAMP(3)").
		Where("((v.runtime_version = ? AND r.apply_error_code <> ?) OR (v.runtime_version = ? AND r.generation_applied_at IS NULL AND r.settlement_kind = '' AND r.settlement_evidence_sha256 = ''))", videoruntime.RuntimeV1, "settlement_conflict", videoruntime.RuntimeV2)
}

func querySettlementEligible(q *gorm.DB) *gorm.DB {
	return supportedQueryReceiptRuntime(q).Where(noCreateInboxReview).
		// 0135 derives this index key from G time and the two exact kinds only.
		// Every original eligibility and locked settlement check remains required.
		Where("r.settlement_shape_ready = 1").
		Where("v.runtime_version = ? AND r.applied_at IS NULL AND r.recovery_due_at IS NOT NULL AND r.recovery_due_at <= UTC_TIMESTAMP(3) AND r.generation_applied_at >= CAST('1000-01-01 00:00:00' AS DATETIME) AND r.settlement_kind IN ? AND v.settlement_receipt_id = r.attempt_id AND OCTET_LENGTH(v.settlement_receipt_id) = OCTET_LENGTH(r.attempt_id)", videoruntime.RuntimeV2, []string{querySettlementUsage, querySettlementProviderNoCharge}).
		Where(noTaskSettlementConflict)
}

// Value copies prevent callers from mutating a captured token through pointers.
// The original stored digest is deliberately retained even when invalid.
type queryRecoveryPhaseState struct {
	pinPresent         bool
	pin                string
	generated          bool
	generatedAt        time.Time
	kind, storedDigest string
}

func queryPhaseState(st State, row QueryReceipt) queryRecoveryPhaseState {
	v := queryRecoveryPhaseState{kind: row.SettlementKind, storedDigest: row.SettlementEvidenceSHA256}
	if st.SettlementReceiptID != nil {
		v.pinPresent = true
		v.pin = *st.SettlementReceiptID
	}
	if row.GenerationAppliedAt != nil {
		v.generated = true
		v.generatedAt = row.GenerationAppliedAt.UTC()
	}
	return v
}

// Called only with the real current Task/State/receipt under the caller locks.
// This captures facts for a later diagnostic; it is never financial authority.
func observeQueryPhaseRecovery(t task.Task, st State, row QueryReceipt) (queryRecoveryAttempt, error) {
	phase, err := queryPhaseOf(st, row)
	if err != nil {
		return queryRecoveryAttempt{}, err
	}
	if phase == queryPhaseAtomicV1 {
		return observeQueryRecovery(row)
	}
	if phase != queryPhaseGenerationV2 && phase != queryPhaseSettlementV2 {
		return queryRecoveryAttempt{}, ErrState
	}
	if t.OrgID != st.OrgID || t.TaskID != st.TaskID || t.Provider != Provider {
		return queryRecoveryAttempt{}, ErrConflict
	}
	token, err := observeQueryRecovery(row)
	if err != nil {
		return queryRecoveryAttempt{}, err
	}
	token.phase = phase
	token.progress = queryPhaseState(st, row)
	token.fixed = querySettlementCanonical(t, st, row, row.SettlementKind, token.evidence)
	token.raw = queryReceiptDigestProjection(row)
	return token, nil
}

func queryPhaseTokenMatches(observed, current queryRecoveryAttempt) bool {
	return observed.valid && current.valid && observed == current
}

// Locks already stabilize State.pin. These explicit phase terms also make the
// receipt UPDATE a CAS, not a second chance to diagnose a later G/B phase.
func queryPhaseCAS(q *gorm.DB, observed queryRecoveryAttempt) *gorm.DB {
	q = q.Where("settlement_kind = ? AND settlement_evidence_sha256 = ?", observed.progress.kind, observed.progress.storedDigest)
	if observed.progress.generated {
		return q.Where("generation_applied_at = ?", observed.progress.generatedAt)
	}
	return q.Where("generation_applied_at IS NULL")
}
