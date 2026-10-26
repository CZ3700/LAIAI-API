package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	errQueryObservationInvalid = errors.New("video query observation requires review")
	errQueryRecoveryReview     = errors.New("video query recovery requires review")
)

type queryJSONFailure uint8

const (
	queryJSONSyntax queryJSONFailure = iota + 1
	queryJSONType
)

// Only decodeQueryObservation constructs this type. Do not retain the decoder
// error: its message can contain a field name or a value from the observation.
type queryObservationDecodeError struct{ kind queryJSONFailure }

func (*queryObservationDecodeError) Error() string { return errQueryObservationInvalid.Error() }
func (*queryObservationDecodeError) Unwrap() error { return errQueryObservationInvalid }

func decodeQueryObservation(raw string) (queryObservation, error) {
	var value queryObservation
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		var syntax *json.SyntaxError
		var mismatch *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntax):
			return queryObservation{}, &queryObservationDecodeError{kind: queryJSONSyntax}
		case errors.As(err, &mismatch):
			return queryObservation{}, &queryObservationDecodeError{kind: queryJSONType}
		default:
			return queryObservation{}, safeRecoveryErrors(err)
		}
	}
	return value, nil
}

type queryRecoveryCause uint8

const (
	queryCauseTemporary queryRecoveryCause = iota
	queryCauseObservation
	queryCauseSettlement
	queryCauseCreateReview
)

// A positive classification requires a closed error tree. A matching JSON or
// conflict sentinel joined with ANY unrecognized/authority/SQL/context cause
// is temporary. External bare JSON errors and the sentinel alone are not proof.
func classifyQueryRecoveryCause(err error) queryRecoveryCause {
	remaining := 64
	var visit func(error) queryRecoveryCause
	visit = func(current error) queryRecoveryCause {
		remaining--
		if current == nil || remaining < 0 {
			return queryCauseTemporary
		}
		if decoded, ok := current.(*queryObservationDecodeError); ok {
			if decoded != nil && (decoded.kind == queryJSONSyntax || decoded.kind == queryJSONType) {
				return queryCauseObservation
			}
			return queryCauseTemporary
		}
		if current == videobilling.ErrConflict {
			return queryCauseSettlement
		}
		if current == errCreateInboxReview {
			return queryCauseCreateReview
		}
		// Only our fixed-message wrapper has a known, transparent meaning.
		// Generic Unwrap, fmt.Errorf, errors.Join and custom wrappers may add
		// unknown semantics; never erase that uncertainty by following them.
		if joined, ok := current.(*persistedRecoveryErrors); ok && joined != nil {
			children := joined.causes
			if len(children) == 0 {
				return queryCauseTemporary
			}
			kind := visit(children[0])
			for _, child := range children[1:] {
				if visit(child) != kind {
					return queryCauseTemporary
				}
			}
			return kind
		}
		return queryCauseTemporary
	}
	return visit(err)
}

type queryRecoveryDisposition uint8

const (
	queryRecoveryNoop queryRecoveryDisposition = iota
	queryRecoveryApplied
	queryRecoveryDeferred
	queryRecoveryReview
	queryRecoveryFailed
)

type queryRecoveryResult struct {
	disposition queryRecoveryDisposition
	code        string
}

type queryRecoveryAttempt struct {
	orgID             uint64
	taskID, attemptID string
	version           uint64
	evidence          [32]byte
	valid             bool
}

func queryEvidenceDigest(row QueryReceipt) ([32]byte, error) {
	// Exclude only mutable application/progress fields. All durable evidence,
	// including TaskRowVersion and both artifact-presence flags, remains bound.
	row.AppliedAt, row.RecoveryDueAt = nil, nil
	row.ApplyErrorCode, row.RecoveryVersion = "", 0
	row.ReceivedAt = row.ReceivedAt.UTC()
	raw, err := json.Marshal(row)
	if err != nil {
		return [32]byte{}, safeRecoveryErrors(err)
	}
	return sha256.Sum256(raw), nil
}

func observeQueryRecovery(row QueryReceipt) (queryRecoveryAttempt, error) {
	if row.OrgID == 0 || row.TaskID == "" || row.AttemptID == "" || row.Sequence == 0 || row.RecoveryVersion == math.MaxUint64 {
		return queryRecoveryAttempt{}, ErrConflict
	}
	digest, err := queryEvidenceDigest(row)
	if err != nil {
		return queryRecoveryAttempt{}, err
	}
	return queryRecoveryAttempt{orgID: row.OrgID, taskID: row.TaskID, attemptID: row.AttemptID, version: row.RecoveryVersion, evidence: digest, valid: true}, nil
}

func queryRecoveryTimeTx(tx *gorm.DB) (time.Time, error) {
	ctx, cancel := context.WithTimeout(tx.Statement.Context, 200*time.Millisecond)
	defer cancel()
	return authorityTimeTx(tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}))
}

func (s *Service) recordQueryRecoveryFailure(ctx context.Context, observed queryRecoveryAttempt, cause error) (queryRecoveryResult, error) {
	result := queryRecoveryResult{disposition: queryRecoveryNoop}
	if err := ctx.Err(); err != nil {
		return result, safeRecoveryErrors(err)
	}
	if !observed.valid || cause == nil {
		return result, nil
	}
	// A cancelled/expired application cannot authorize permanent review, even
	// if its original decoding failure also remains in the error tree.
	kind := classifyQueryRecoveryCause(cause)
	phase, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := s.DB.Session(&gorm.Session{NewDB: true, Context: phase, Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		task, state, err := LockBillingTaskTx(tx, observed.orgID, observed.taskID)
		if err != nil {
			return err
		}
		var current QueryReceipt
		if err = locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", observed.orgID, observed.taskID, observed.attemptID).Take(&current).Error; err != nil {
			return err
		}
		if task.OrgID != observed.orgID || task.TaskID != observed.taskID || current.OrgID != observed.orgID || current.TaskID != observed.taskID || current.AttemptID != observed.attemptID {
			return ErrConflict
		}
		if err = requireQueryReceiptRuntime(state, current); err != nil {
			return err
		}
		if current.AppliedAt != nil || current.RecoveryDueAt == nil || current.RecoveryVersion != observed.version {
			return nil
		}
		digest, err := queryEvidenceDigest(current)
		if err != nil {
			return err
		}
		if digest != observed.evidence {
			return ErrConflict
		}
		quarantineErr := requireNoCreateInboxReviewTx(tx, current.OrgID, current.TaskID)
		if kind == queryCauseCreateReview {
			if quarantineErr == nil {
				return nil // The current quarantine no longer supports this diagnosis.
			}
			if quarantineErr != errCreateInboxReview {
				return quarantineErr
			}
		} else if quarantineErr != nil {
			return quarantineErr
		}
		code := "query_apply_failed"
		if kind == queryCauseObservation {
			// Only a current, identical, supported piece of evidence can justify
			// review. The original caller's error alone never authorizes it.
			_, decodeErr := decodeQueryObservation(current.ObservationJSON)
			if classifyQueryRecoveryCause(decodeErr) != queryCauseObservation {
				return ErrConflict
			}
			code = "query_observation_invalid"
		} else if kind == queryCauseSettlement {
			code = "settlement_conflict"
		} else if kind == queryCauseCreateReview {
			code = "create_identity_review"
		}
		now, err := queryRecoveryTimeTx(tx)
		if err != nil {
			return err
		}
		if err = phase.Err(); err != nil {
			return err
		}
		var due *time.Time
		if kind == queryCauseTemporary || kind == queryCauseCreateReview {
			next := now.Add(30 * time.Second)
			due = &next
		}
		write := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", observed.orgID, observed.taskID, observed.attemptID, observed.version).
			Updates(map[string]any{"apply_error_code": code, "recovery_due_at": due, "recovery_version": observed.version + 1})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return ErrConflict
		}
		if state.DeletedAt == nil && state.SyncState != "needs_review" {
			if err = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", observed.orgID, observed.taskID).Update("sync_state", "needs_review").Error; err != nil {
				return err
			}
		}
		result.code = code
		result.disposition = queryRecoveryReview
		if due != nil {
			result.disposition = queryRecoveryDeferred
		}
		return nil
	})
	if err != nil {
		return queryRecoveryResult{disposition: queryRecoveryFailed}, safeRecoveryErrors(err, phase.Err())
	}
	return result, nil
}
