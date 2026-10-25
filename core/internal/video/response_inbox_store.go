package video

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/dberr"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// No plaintext or credential survives preparation. The seal also prevents an
// in-process caller from substituting an identity after it was authenticated.
type preparedResponseReceipt struct {
	attempt ExecutionAttempt
	receipt ResponseInbox
	payload *invocation.PreparedVideoPayload
	seal    string
}

func responseTransaction(tx *gorm.DB) error {
	if tx == nil || tx.Statement == nil {
		return ErrUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrUnavailable
	}
	return nil
}

func responseDatabaseTimeTx(tx *gorm.DB) (time.Time, error) {
	if err := responseTransaction(tx); err != nil {
		return time.Time{}, err
	}
	var result struct{ DatabaseTime time.Time }
	if tx.Dialector.Name() != "mysql" {
		return time.Time{}, ErrUnavailable
	}
	if err := tx.Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&result).Error; err != nil {
		return time.Time{}, err
	}
	if result.DatabaseTime.IsZero() {
		return time.Time{}, ErrUnavailable
	}
	return result.DatabaseTime.UTC().Truncate(time.Millisecond), nil
}

func (s *Service) validateResponseHashVersionTx(tx *gorm.DB, version string) error {
	if version != "v1" || len(s.Secret) < 32 {
		return ErrUnavailable
	}
	var row HashVersion
	if err := tx.Where("version = ?", version).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUnavailable
		}
		return responseStorageFailure(err)
	}
	if !hmac.Equal([]byte(row.Fingerprint), []byte(digest(s.Secret, "version-fingerprint:v1"))) {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) executionIdentityHMAC(identity invocation.VideoExecutionIdentity) string {
	encoded, _ := json.Marshal(identity)
	return digest(s.Secret, "execution-metadata-identity:v1", string(encoded))
}

// registerExecutionAttemptTx joins the existing claim transaction. The row is
// immutable and is written before HTTP; it never grants permission by itself.
func (s *Service) registerExecutionAttemptTx(tx *gorm.DB, t task.Task, st State, op Operation) (ExecutionAttempt, error) {
	var out ExecutionAttempt
	if err := responseTransaction(tx); err != nil {
		return out, err
	}
	if t.OrgID == 0 || t.OrgID != st.OrgID || t.OrgID != op.OrgID || t.TaskID != st.TaskID || t.TaskID != op.ResourceID || t.Provider != Provider || op.Kind != "create_task" || op.AttemptID == "" || op.Version == 0 || st.EffectivePayloadRef == "" || op.PayloadRef != st.EffectivePayloadRef {
		return out, ErrConflict
	}
	if err := requireTaskRuntimeTx(tx, st); err != nil {
		return out, err
	}
	if err := requireOperationRuntime(st, op); err != nil {
		return out, err
	}
	// The producer's value can retain its pre-claim Status. Validate the exact
	// persisted claim, not that stale projection, inside the caller transaction.
	var current Operation
	if err := locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", op.OrgID, op.ID, op.ResourceID).Take(&current).Error; err != nil {
		return out, err
	}
	if current.Status != "in_flight" || current.Version != op.Version || current.AttemptID != op.AttemptID || current.Kind != op.Kind || current.PayloadRef != op.PayloadRef {
		return out, ErrConflict
	}
	if err := requireOperationRuntime(st, current); err != nil {
		return out, err
	}
	var fixed State
	if err := tx.Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Take(&fixed).Error; err != nil {
		return out, err
	}
	if stateRuntime(fixed) != stateRuntime(st) || fixed.ProviderScopeID != st.ProviderScopeID || fixed.CredentialSnapshotID != st.CredentialSnapshotID || fixed.BillingSnapshotID != st.BillingSnapshotID || fixed.EffectivePayloadRef != st.EffectivePayloadRef || fixed.CaptureHistory != st.CaptureHistory || fixed.RetentionDays != st.RetentionDays || st.RetentionDays < 1 || st.RetentionDays > 30 {
		return out, ErrConflict
	}
	var credential CredentialSnapshot
	if err := tx.Select("id", "org_id", "task_id", "provider_scope_id").Where("id = ? AND org_id = ? AND task_id = ? AND provider_scope_id = ?", st.CredentialSnapshotID, st.OrgID, st.TaskID, st.ProviderScopeID).Take(&credential).Error; err != nil {
		return out, err
	}
	descriptor, err := videoruntime.Resolve(stateRuntime(st))
	if err != nil {
		return out, err
	}
	identity, err := invocation.ReadVideoExecutionIdentityTx(tx, st.OrgID, st.EffectivePayloadRef, descriptor.ExecutionInputSchemaVersion)
	if err != nil {
		return out, err
	}
	if err := s.validateResponseHashVersionTx(tx, "v1"); err != nil {
		return out, err
	}
	now, err := responseDatabaseTimeTx(tx)
	if err != nil {
		return out, err
	}
	out = ExecutionAttempt{AttemptID: op.AttemptID, OrgID: st.OrgID, TaskID: st.TaskID, OperationID: op.ID, Action: op.Kind,
		StorageProtocol: responseInboxProtocol, ProviderScopeID: st.ProviderScopeID, CredentialSnapshotID: st.CredentialSnapshotID,
		BillingSnapshotID: st.BillingSnapshotID, InputPayloadRef: st.EffectivePayloadRef,
		InputIdentityHMAC: s.executionIdentityHMAC(identity), InputHMACVersion: "v1", EvidenceHMACVersion: "v1",
		RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion,
		ExecutionSchemaVersion: descriptor.ExecutionInputSchemaVersion, ObservationSchemaVersion: descriptor.ObservationSchemaVersion,
		OperationClaimVersion: op.Version, CaptureHistory: st.CaptureHistory, RetentionDays: st.RetentionDays, RegisteredAt: now}
	if err := tx.Create(&out).Error; err != nil {
		if !dberr.IsDuplicate(err) && !errors.Is(err, gorm.ErrDuplicatedKey) {
			return ExecutionAttempt{}, err
		}
		var previous ExecutionAttempt
		if err := locked(tx).Where("org_id = ? AND attempt_id = ?", out.OrgID, out.AttemptID).Take(&previous).Error; err != nil {
			return ExecutionAttempt{}, ErrConflict
		}
		out.RegisteredAt = previous.RegisteredAt // Registration retry does not change its original time.
		if !sameExecutionAttempt(previous, out) {
			return ExecutionAttempt{}, ErrConflict
		}
		return previous, nil
	}
	return out, nil
}

func sameExecutionAttempt(a, b ExecutionAttempt) bool {
	if !a.RegisteredAt.Equal(b.RegisteredAt) {
		return false
	}
	if (a.TargetUpstreamTaskID == nil) != (b.TargetUpstreamTaskID == nil) || a.TargetUpstreamTaskID != nil && *a.TargetUpstreamTaskID != *b.TargetUpstreamTaskID {
		return false
	}
	a.TargetUpstreamTaskID, b.TargetUpstreamTaskID = nil, nil
	a.RegisteredAt, b.RegisteredAt = time.Time{}, time.Time{}
	return a == b
}

func (s *Service) validateExecutionAttemptInputTx(tx *gorm.DB, attempt ExecutionAttempt) error {
	if attempt.StorageProtocol == videoruntime.ActionStorageProtocolV1 {
		return s.validateMutationAttemptInputTx(tx, attempt)
	}
	if attempt.TargetUpstreamTaskID != nil {
		return ErrConflict
	}
	if err := responseTransaction(tx); err != nil {
		return err
	}
	if err := s.validateResponseHashVersionTx(tx, attempt.InputHMACVersion); err != nil {
		return err
	}
	identity, err := invocation.ReadVideoExecutionIdentityTx(tx, attempt.OrgID, attempt.InputPayloadRef, attempt.ExecutionSchemaVersion)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(attempt.InputIdentityHMAC), []byte(s.executionIdentityHMAC(identity))) {
		return ErrConflict
	}
	return nil
}

var responseSafeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func responseIdentifier(value, credential string, maxLength int) string {
	if len(value) > maxLength || !responseSafeID.MatchString(value) || credential != "" && strings.Contains(value, credential) {
		return ""
	}
	return value
}

func responseSafeError(err *ark.Error) string {
	if err == nil {
		return ""
	}
	switch err.Code {
	case "invalid_request", "invalid_configuration", "request_cancelled", "upstream_transport_error", "invalid_upstream_response", "upstream_response_too_large", "incomplete_upstream_response", "upstream_rejected", "upstream_not_found", "upstream_outcome_unknown", "upstream_identity_mismatch", "invalid_upstream_usage", "invalid_action":
		return err.Code
	default:
		return "upstream_error"
	}
}

func (s *Service) responsePreparationSeal(p *preparedResponseReceipt) string {
	encoded, _ := json.Marshal(struct {
		Attempt ExecutionAttempt
		Receipt ResponseInbox
	}{p.attempt, p.receipt})
	return digest(s.Secret, "prepared-response:v1", string(encoded), p.payload.PayloadID())
}

// prepareResponseReceipt captures the producer's contract, including a future
// contract this consumer cannot interpret. Append validates registration, not
// the current task/operation or the current runtime registry.
func (s *Service) prepareResponseReceipt(attempt ExecutionAttempt, result ark.Result, credential string) (*preparedResponseReceipt, error) {
	if result.Action != ark.ActionSubmit || attempt.TargetUpstreamTaskID != nil {
		return nil, ErrInvalid
	}
	return s.prepareCapturedResponse(attempt, result, credential, false)
}

func (s *Service) prepareCapturedResponse(attempt ExecutionAttempt, result ark.Result, credential string, mutation bool) (*preparedResponseReceipt, error) {
	if s == nil || s.Evidence == nil || s.Now == nil || len(s.Secret) < 32 || attempt.OrgID == 0 || attempt.AttemptID == "" || attempt.TaskID == "" || attempt.OperationID == "" || attempt.RegisteredAt.IsZero() || attempt.EvidenceHMACVersion != "v1" || attempt.InputHMACVersion != "v1" || attempt.RetentionDays < 1 || attempt.RetentionDays > 30 {
		return nil, ErrInvalid
	}
	if attempt.TargetUpstreamTaskID != nil {
		target := *attempt.TargetUpstreamTaskID
		attempt.TargetUpstreamTaskID = &target
	}
	row := ResponseInbox{OrgID: attempt.OrgID, TaskID: attempt.TaskID, OperationID: attempt.OperationID, AttemptID: attempt.AttemptID,
		Action: attempt.Action, StorageProtocol: attempt.StorageProtocol, ProviderScopeID: attempt.ProviderScopeID,
		CredentialSnapshotID: attempt.CredentialSnapshotID, BillingSnapshotID: attempt.BillingSnapshotID,
		RuntimeVersion: attempt.RuntimeVersion, AdapterVersion: attempt.AdapterVersion, ProtocolContractVersion: attempt.ProtocolContractVersion,
		BillingSchemaVersion: attempt.BillingSchemaVersion, ExecutionSchemaVersion: attempt.ExecutionSchemaVersion,
		ObservationSchemaVersion: attempt.ObservationSchemaVersion, EvidenceHMACVersion: attempt.EvidenceHMACVersion,
		HTTPStatus: result.HTTPStatus, Outcome: string(result.Outcome), SafeErrorCode: responseSafeError(result.Error), Truncated: result.Truncated}
	// Known credential reflection is unsafe even when it satisfies ID syntax.
	// Body redaction alone cannot protect these separately stored typed fields.
	row.UpstreamTaskID = responseIdentifier(result.TaskID, credential, 128)
	row.ProviderRequestID = responseIdentifier(result.RequestID, credential, 256)
	switch result.Outcome {
	case ark.OutcomeAccepted, ark.OutcomeRejected, ark.OutcomeUnknown, ark.OutcomeNotSent:
	default:
		row.Outcome, row.SafeErrorCode = string(ark.OutcomeUnknown), "invalid_upstream_response"
	}
	body := result.Body
	if int64(len(body)) > ark.DefaultMaxResponseBytes {
		body = body[:ark.DefaultMaxResponseBytes]
		row.Truncated = true
	}
	row.CapturedBodyBytes = int64(len(body))
	if row.Truncated || !mutation && row.Outcome == string(ark.OutcomeAccepted) && row.UpstreamTaskID == "" || mutation && result.TaskID != "" && row.UpstreamTaskID == "" {
		row.Outcome, row.SafeErrorCode = string(ark.OutcomeUnknown), "invalid_upstream_response"
	}
	// Capture identity and bounded raw evidence BEFORE random envelope values.
	identity, _ := json.Marshal(struct {
		Attempt ExecutionAttempt
		Result  ResponseInbox
	}{attempt, row})
	row.EvidenceHMAC = digest(s.Secret, "response-evidence:v1", string(identity), string(body))
	var err error
	row.ReceiptID, err = randomID("rcpt_")
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	payload, err := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: attempt.OrgID, RequestID: row.ReceiptID, AttemptID: attempt.AttemptID,
		Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body,
		ExpiresAt: s.Now().UTC().Add(time.Duration(attempt.RetentionDays) * 24 * time.Hour), CaptureEnabled: attempt.CaptureHistory,
		SecretValues: []string{credential}, CaptureRepresentation: "response_json_redacted"})
	if err != nil {
		return nil, err
	}
	row.PayloadRef = payload.PayloadID()
	p := &preparedResponseReceipt{attempt: attempt, receipt: row, payload: payload}
	p.seal = s.responsePreparationSeal(p)
	return p, nil
}

func responseMatchesAttempt(r ResponseInbox, a ExecutionAttempt) bool {
	return r.OrgID == a.OrgID && r.TaskID == a.TaskID && r.OperationID == a.OperationID && r.AttemptID == a.AttemptID &&
		r.Action == a.Action && r.StorageProtocol == a.StorageProtocol && r.ProviderScopeID == a.ProviderScopeID &&
		r.CredentialSnapshotID == a.CredentialSnapshotID && r.BillingSnapshotID == a.BillingSnapshotID &&
		r.RuntimeVersion == a.RuntimeVersion && r.AdapterVersion == a.AdapterVersion && r.ProtocolContractVersion == a.ProtocolContractVersion &&
		r.BillingSchemaVersion == a.BillingSchemaVersion && r.ExecutionSchemaVersion == a.ExecutionSchemaVersion &&
		r.ObservationSchemaVersion == a.ObservationSchemaVersion && r.EvidenceHMACVersion == a.EvidenceHMACVersion
}

// validateResponseInboxTx checks durable associations without payload access,
// current runtime checks or mutable task/claim reads. Used on duplicate append
// and by the reducer before it performs its separate interpretation checks.
func (s *Service) validateResponseInboxTx(tx *gorm.DB, attempt ExecutionAttempt, receipt ResponseInbox) error {
	if err := responseTransaction(tx); err != nil {
		return err
	}
	if !responseMatchesAttempt(receipt, attempt) || receipt.ReceiptID == "" || receipt.PayloadRef == "" || len(receipt.EvidenceHMAC) != 64 {
		return ErrConflict
	}
	if err := s.validateResponseHashVersionTx(tx, receipt.EvidenceHMACVersion); err != nil {
		return err
	}
	if err := invocation.ValidateVideoPayloadReferenceTx(tx, invocation.VideoPayloadReference{OrgID: receipt.OrgID, PayloadID: receipt.PayloadRef,
		RequestID: receipt.ReceiptID, AttemptID: receipt.AttemptID, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1}); err != nil {
		return err
	}
	var audits int64
	if err := tx.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND operation_id = ? AND request_id = ? AND phase = ? AND evidence_ref = ? AND http_status = ? AND upstream_request_id = ?", receipt.OrgID, receipt.TaskID, receipt.AttemptID, receipt.OperationID, receipt.ReceiptID, "response_received", receipt.PayloadRef, receipt.HTTPStatus, receipt.ProviderRequestID).Count(&audits).Error; err != nil {
		return err
	}
	if audits != 1 {
		return ErrConflict
	}
	return nil
}

func sameResponseEvidence(a, b ResponseInbox) bool {
	a.ReceiptID, b.ReceiptID, a.PayloadRef, b.PayloadRef = "", "", "", ""
	a.ReceivedAt, b.ReceivedAt = time.Time{}, time.Time{}
	return a == b
}

// appendResponseReceipt is an independent evidence transaction. A plain INSERT
// chooses the unique winner even with CLIENT_FOUND_ROWS; duplicate-key errors
// do not abort MySQL transactions. Only the winner stages encrypted history.
func (s *Service) appendResponseReceipt(ctx context.Context, p *preparedResponseReceipt) (ResponseInbox, error) {
	var out ResponseInbox
	if s == nil || s.DB == nil || s.Evidence == nil || ctx == nil || p == nil || p.payload == nil || p.receipt.PayloadRef != p.payload.PayloadID() || !hmac.Equal([]byte(p.seal), []byte(s.responsePreparationSeal(p))) {
		return out, ErrInvalid
	}
	if s.responseDB == nil || s.responseDB.Statement == nil {
		return out, ErrUnavailable
	}
	if _, inTransaction := s.responseDB.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
		return out, ErrUnavailable
	}
	err := s.responseDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var registered ExecutionAttempt
		// This is the first read: serialize responses for this immutable
		// registration before establishing the RR snapshot. A duplicate then
		// sees the complete committed winner, including Invocation metadata.
		if err := locked(tx).Where("org_id = ? AND attempt_id = ?", p.attempt.OrgID, p.attempt.AttemptID).Take(&registered).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return responseStorageFailure(err)
		}
		if err := s.validateResponseHashVersionTx(tx, p.attempt.EvidenceHMACVersion); err != nil {
			return err
		}
		if err := s.validateResponseHashVersionTx(tx, p.attempt.InputHMACVersion); err != nil {
			return err
		}
		if !sameExecutionAttempt(registered, p.attempt) || !responseMatchesAttempt(p.receipt, registered) {
			return ErrConflict
		}
		now, err := responseDatabaseTimeTx(tx)
		if err != nil {
			return err
		}
		candidate := p.receipt
		candidate.ReceivedAt = now
		if err := tx.Create(&candidate).Error; err != nil {
			if !dberr.IsDuplicate(err) && !errors.Is(err, gorm.ErrDuplicatedKey) {
				return err
			}
			// Current locking read sees a concurrent winner after the unique-key
			// wait, rather than this transaction's earlier registration snapshot.
			if err := locked(tx).Where("org_id = ? AND attempt_id = ? AND evidence_hmac_version = ? AND evidence_hmac = ?", candidate.OrgID, candidate.AttemptID, candidate.EvidenceHMACVersion, candidate.EvidenceHMAC).Take(&out).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrConflict
				}
				return responseStorageFailure(err)
			}
			if !sameResponseEvidence(out, candidate) {
				return ErrConflict
			}
			var app ReceiptApplication
			if err := locked(tx).Where("receipt_id = ? AND org_id = ? AND task_id = ? AND attempt_id = ?", out.ReceiptID, out.OrgID, out.TaskID, out.AttemptID).Take(&app).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrConflict
				}
				return responseStorageFailure(err)
			}
			if app.Version == 0 {
				return ErrConflict
			}
			switch app.State {
			case "pending", "blocked", "review", "applied":
			default:
				return ErrConflict
			}
			return s.validateResponseInboxTx(tx, registered, out)
		}
		ref, err := s.Evidence.StageTx(tx, p.payload)
		if err != nil {
			return err
		}
		if ref != candidate.PayloadRef {
			return ErrConflict
		}
		app := ReceiptApplication{ReceiptID: candidate.ReceiptID, OrgID: candidate.OrgID, TaskID: candidate.TaskID, AttemptID: candidate.AttemptID,
			State: "pending", Version: 1, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&app).Error; err != nil {
			return err
		}
		audit := Attempt{OrgID: candidate.OrgID, TaskID: candidate.TaskID, AttemptID: candidate.AttemptID, RequestID: candidate.ReceiptID,
			OperationID: candidate.OperationID, Phase: "response_received", HTTPStatus: candidate.HTTPStatus, UpstreamRequestID: candidate.ProviderRequestID,
			EvidenceRef: candidate.PayloadRef, ErrorCode: candidate.SafeErrorCode, ObservedAt: now}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		out = candidate
		return nil
	})
	if err != nil {
		return ResponseInbox{}, responseStorageFailure(err)
	}
	return out, nil
}
