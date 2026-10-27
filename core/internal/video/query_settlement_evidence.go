package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

const querySettlementEvidenceInvalidCode = "query_settlement_evidence_invalid"

var errQuerySettlementEvidenceInvalid = errors.New("video query settlement evidence requires review")

type querySettlementEvidenceFailure uint8

const (
	querySettlementIdentityInvalid querySettlementEvidenceFailure = iota + 1
	querySettlementDigestInvalid
	querySettlementObservationInvalid
)

// Only readQuerySettlementEvidence constructs this type. An arbitrary conflict
// or JSON error supplied by a caller is not a proof of permanent B corruption.
type querySettlementEvidenceError struct {
	kind querySettlementEvidenceFailure
}

func (*querySettlementEvidenceError) Error() string { return errQuerySettlementEvidenceInvalid.Error() }
func (*querySettlementEvidenceError) Unwrap() error { return errQuerySettlementEvidenceInvalid }

// Field order and tags are the separately frozen nineteen-field canonical.
// This is also a comparable value snapshot for the diagnostic token; no
// mutable financial projection, phase, pin or stored outer hash is encoded.
type querySettlementEvidenceCanonical struct {
	Domain                  string `json:"domain"`
	OrgID                   uint64 `json:"org_id"`
	TaskID                  string `json:"task_id"`
	AttemptID               string `json:"attempt_id"`
	RuntimeVersion          string `json:"runtime_version"`
	AdapterVersion          string `json:"adapter_version"`
	ProtocolContractVersion string `json:"protocol_contract_version"`
	BillingSchemaVersion    string `json:"billing_schema_version"`
	BillingSnapshotID       uint64 `json:"billing_snapshot_id"`
	ProviderScopeID         uint64 `json:"provider_scope_id"`
	CredentialSnapshotID    uint64 `json:"credential_snapshot_id"`
	ChannelID               uint64 `json:"channel_id"`
	Model                   string `json:"model"`
	UpstreamTaskID          string `json:"upstream_task_id"`
	UpstreamModel           string `json:"upstream_model"`
	CreateRequestID         string `json:"create_request_id"`
	NoChargeStatuses        string `json:"no_charge_statuses"`
	SettlementKind          string `json:"settlement_kind"`
	ReceiptEvidenceSHA256   string `json:"receipt_evidence_sha256"`
}

func querySettlementCanonical(t task.Task, st State, row QueryReceipt, kind string, evidence [32]byte) querySettlementEvidenceCanonical {
	return querySettlementEvidenceCanonical{
		Domain: "query-settlement-evidence-v1", OrgID: row.OrgID, TaskID: row.TaskID, AttemptID: row.AttemptID,
		RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion,
		BillingSnapshotID: st.BillingSnapshotID, ProviderScopeID: st.ProviderScopeID, CredentialSnapshotID: st.CredentialSnapshotID,
		ChannelID: t.ChannelID, Model: t.Model, UpstreamTaskID: t.UpstreamTaskID, UpstreamModel: st.UpstreamModel,
		CreateRequestID: st.CreateRequestID, NoChargeStatuses: st.NoChargeStatuses, SettlementKind: kind, ReceiptEvidenceSHA256: hex.EncodeToString(evidence[:]),
	}
}

func settlementEvidenceDigest(t task.Task, st State, row QueryReceipt, kind string) ([32]byte, error) {
	if err := requireQueryReceiptRuntime(st, row); err != nil {
		return [32]byte{}, err
	}
	if stateRuntime(st) != videoruntime.GenerationSettlementV2() {
		return [32]byte{}, videoruntime.ErrUnsupported
	}
	if row.OrgID == 0 || t.OrgID != row.OrgID || t.TaskID != row.TaskID || t.Provider != Provider || row.TaskID == "" || row.AttemptID == "" || st.BillingSnapshotID == 0 || st.ProviderScopeID == 0 || st.CredentialSnapshotID == 0 || t.ChannelID == 0 || t.Model == "" || t.UpstreamTaskID == "" || st.UpstreamModel == "" || st.CreateRequestID == "" {
		return [32]byte{}, ErrInvalid
	}
	if kind != querySettlementNone && kind != querySettlementUsage && kind != querySettlementProviderNoCharge {
		return [32]byte{}, ErrInvalid
	}
	// Go's legacy JSON replacement of invalid UTF-8 must stay unchanged for
	// V1. V2 rejects it instead of allowing two distinct inputs to alias.
	for _, value := range []string{row.AttemptID, row.TaskID, row.RuntimeVersion, row.AdapterVersion, row.ProtocolContractVersion, row.Outcome, row.ProviderRequestID, row.ObservationJSON, row.PayloadRef, t.Model, t.UpstreamTaskID, st.UpstreamModel, st.CreateRequestID, st.NoChargeStatuses} {
		if !utf8.ValidString(value) {
			return [32]byte{}, ErrInvalid
		}
	}
	inner, err := queryEvidenceDigest(row)
	if err != nil {
		return [32]byte{}, err
	}
	raw, err := json.Marshal(querySettlementCanonical(t, st, row, kind, inner))
	if err != nil {
		return [32]byte{}, safeRecoveryErrors(err)
	}
	return sha256.Sum256(raw), nil
}

func readQuerySettlementEvidence(t task.Task, st State, current QueryReceipt) (queryObservation, error) {
	if t.OrgID != st.OrgID || t.TaskID != st.TaskID || t.Provider != Provider {
		return queryObservation{}, ErrConflict
	}
	phase, err := queryPhaseOf(st, current)
	if err != nil {
		return queryObservation{}, err
	}
	if phase != queryPhaseSettlementV2 {
		return queryObservation{}, ErrState
	}
	digest, err := settlementEvidenceDigest(t, st, current, current.SettlementKind)
	if err != nil {
		if err == ErrInvalid {
			return queryObservation{}, &querySettlementEvidenceError{kind: querySettlementIdentityInvalid}
		}
		return queryObservation{}, err
	}
	if current.SettlementEvidenceSHA256 != hex.EncodeToString(digest[:]) {
		return queryObservation{}, &querySettlementEvidenceError{kind: querySettlementDigestInvalid}
	}
	o, err := decodeQueryObservation(current.ObservationJSON)
	if err != nil {
		return queryObservation{}, err
	}
	if current.ReceivedAt.IsZero() || current.HTTPStatus < 200 || current.HTTPStatus >= 300 || current.Outcome != string(ark.OutcomeAccepted) || o.TaskID != t.UpstreamTaskID || (o.Model != "" && o.Model != st.UpstreamModel) || o.HasError || (o.CompletionTokens != nil && *o.CompletionTokens < 0) || (o.TotalTokens != nil && *o.TotalTokens < 0) {
		return queryObservation{}, &querySettlementEvidenceError{kind: querySettlementObservationInvalid}
	}
	switch current.SettlementKind {
	case querySettlementUsage:
		if o.Status != ark.StatusSucceeded || o.CompletionTokens == nil {
			return queryObservation{}, &querySettlementEvidenceError{kind: querySettlementObservationInvalid}
		}
	case querySettlementProviderNoCharge:
		if (o.Status != ark.StatusFailed && o.Status != ark.StatusCancelled && o.Status != ark.StatusExpired) || !strings.Contains(","+st.NoChargeStatuses+",", ","+string(o.Status)+",") || (o.CompletionTokens != nil && *o.CompletionTokens != 0) || current.PayloadRef == "" {
			return queryObservation{}, &querySettlementEvidenceError{kind: querySettlementObservationInvalid}
		}
	}
	return o, nil
}
