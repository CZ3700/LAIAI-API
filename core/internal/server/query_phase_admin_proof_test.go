package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Reused narrow primitive: the S1 handoff's information_schema column/PK
// enumeration. No model JSON tag can omit a persisted field. Only table hashes
// leave this method; encrypted bytes and API keys are not serialized to stdout.
func (h *phaseAdminHarness) projection(tx *gorm.DB, table, where string, args ...any) ([]map[string]any, error) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	if !valid.MatchString(table) {
		return nil, errPhaseHarness
	}
	if h.columns[table] == nil {
		var columns, keys []string
		if e := tx.Raw("SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", table).Scan(&columns).Error; e != nil {
			return nil, e
		}
		if e := tx.Raw("SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY seq_in_index", table).Scan(&keys).Error; e != nil {
			return nil, e
		}
		if len(columns) == 0 || len(keys) == 0 {
			return nil, errPhaseHarness
		}
		for _, c := range append(append([]string{}, columns...), keys...) {
			if !valid.MatchString(c) {
				return nil, errPhaseHarness
			}
		}
		h.columns[table], h.primary[table] = columns, keys
	}
	columns := h.columns[table]
	query := "SELECT `" + strings.Join(columns, "`,`") + "` FROM `" + table + "`"
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY `" + strings.Join(h.primary[table], "`,`") + "`"
	rows, e := tx.Raw(query, args...).Rows()
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		if len(result) >= 30000 {
			return nil, errPhaseHarness
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if e = rows.Scan(ptrs...); e != nil {
			return nil, e
		}
		row := map[string]any{}
		for i, v := range values {
			switch x := v.(type) {
			case nil:
				row[columns[i]] = nil
			case []byte:
				row[columns[i]] = map[string]any{"bytes_hex": hex.EncodeToString(x)}
			case time.Time:
				row[columns[i]] = map[string]any{"utc": x.UTC().Format(time.RFC3339Nano)}
			case float64:
				return nil, errPhaseHarness
			default:
				row[columns[i]] = map[string]any{"scalar": fmt.Sprint(x)}
			}
		}
		result = append(result, row)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	return result, nil
}
func (h *phaseAdminHarness) tableProof(tx *gorm.DB, table, where string, args ...any) (phaseTableProof, error) {
	rows, e := h.projection(tx, table, where, args...)
	if e != nil {
		return phaseTableProof{}, e
	}
	sha, e := phaseHash(rows)
	return phaseTableProof{SHA: sha, Rows: len(rows)}, e
}

var phaseFinanceTables = []string{"ledger_accounts", "ledger_holds", "ledger_journal", "ledger_operations", "ledger_operation_events", "ledger_movements", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations", "video_quota_buckets", "api_keys", "invocations", "video_adjustment_proposals", "video_adjustment_reviews", "video_adjustment_applications"}
var phaseStorageTables = []string{"tasks", "video_task_states", "video_operations", "video_attempts", "video_execution_attempts", "video_response_inbox", "video_receipt_applications", "video_query_receipts", "video_upstream_bindings", "video_credential_snapshots", "video_events", "video_outbox", "invocation_payloads", "invocation_payload_staging", "invocation_payload_accesses", "video_request_contexts", "api_request_logs", "video_idempotency_records", "video_provider_bill_imports", "video_provider_bill_lines", "video_provider_bill_bindings", "video_provider_bill_import_rows", "video_provider_bill_heads"}

func phaseTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}
func phaseInt(v *int64) any {
	if v == nil {
		return nil
	}
	return fmt.Sprint(*v)
}
func phaseUint(v *uint64) any {
	if v == nil {
		return nil
	}
	return phaseNum(*v)
}

// Exactly the historical 21 exported fields, in original order with original
// JSON names and zeroed progress. We deliberately do not marshal the new ORM.
type phaseReceiptCanonical struct {
	AttemptID                string
	OrgID                    uint64
	TaskID                   string
	Sequence                 uint64
	TaskRowVersion           uint64
	RuntimeVersion           string
	AdapterVersion           string
	ProtocolContractVersion  string
	ObservationSchemaVersion uint32
	HTTPStatus               int
	Outcome                  string
	ProviderRequestID        string
	ObservationJSON          string
	HasVideo                 bool
	HasLastFrame             bool
	PayloadRef               string
	ReceivedAt               time.Time
	AppliedAt                *time.Time
	ApplyErrorCode           string
	RecoveryDueAt            *time.Time
	RecoveryVersion          uint64
}

func phaseCanonical(r video.QueryReceipt) (string, string, error) {
	v := phaseReceiptCanonical{AttemptID: r.AttemptID, OrgID: r.OrgID, TaskID: r.TaskID, Sequence: r.Sequence, TaskRowVersion: r.TaskRowVersion, RuntimeVersion: r.RuntimeVersion, AdapterVersion: r.AdapterVersion, ProtocolContractVersion: r.ProtocolContractVersion, ObservationSchemaVersion: r.ObservationSchemaVersion, HTTPStatus: r.HTTPStatus, Outcome: r.Outcome, ProviderRequestID: r.ProviderRequestID, ObservationJSON: r.ObservationJSON, HasVideo: r.HasVideo, HasLastFrame: r.HasLastFrame, PayloadRef: r.PayloadRef, ReceivedAt: r.ReceivedAt.UTC()}
	raw, e := json.Marshal(v)
	if e != nil {
		return "", "", e
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

type phaseOuterCanonical struct {
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
type phaseProgressColumns struct {
	RecoveryDueAt            *time.Time
	RecoveryVersion          uint64
	GenerationAppliedAt      *time.Time
	SettlementKind           string
	SettlementEvidenceSHA256 string
}

func (h *phaseAdminHarness) inspect() (phaseSnapshot, error) {
	out := phaseSnapshot{SchemaVersion: h.schema, Tables: map[string]phaseTableProof{}, Tasks: map[string]map[string]any{}, Money: map[string]string{}}
	h.mu.Lock()
	out.Network = h.http
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e := h.verify.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var now time.Time
		if e := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error; e != nil {
			return e
		}
		out.DatabaseUTC = now.UTC().Format(time.RFC3339Nano)
		financial := map[string]any{}
		for _, table := range append(append([]string{}, phaseFinanceTables...), phaseStorageTables...) {
			p, e := h.tableProof(tx, table, "")
			if e != nil {
				return e
			}
			out.Tables[table] = p
		}
		for _, table := range phaseFinanceTables {
			financial[table] = out.Tables[table]
		}
		var e error
		out.FinancialSHA, e = phaseHash(financial)
		if e != nil {
			return e
		}
		if h.seed.Protocol == "" {
			return nil
		}
		org, e := phaseID(h.seed.OrgID)
		if e != nil {
			return e
		}
		keyID, e := phaseID(h.seed.KeyID)
		if e != nil {
			return e
		}
		var account ledger.Account
		var key identity.APIKey
		if e = tx.Where("org_id=? AND type=? AND shard=0", org, ledger.AccountUserCredit).Take(&account).Error; e != nil {
			return e
		}
		if e = tx.Where("org_id=? AND id=?", org, keyID).Take(&key).Error; e != nil {
			return e
		}
		out.Money = map[string]string{"posted_usd": account.PostedBalance.StringFixed(8), "held_usd": account.HeldAmount.StringFixed(8), "available_usd": account.Available().StringFixed(8), "key_used_usd": key.UsedAmount.StringFixed(8)}
		for _, scope := range []struct {
			label string
			org   uint64
		}{{"customer", org}, {"provider", 0}} {
			var sums struct{ Reserved, Consumed decimal.Decimal }
			if e = tx.Raw("SELECT COALESCE(SUM(reserved_amount),0) reserved,COALESCE(SUM(consumed_amount),0) consumed FROM video_quota_buckets WHERE org_id=?", scope.org).Scan(&sums).Error; e != nil {
				return e
			}
			out.Money[scope.label+"_reserved"] = sums.Reserved.StringFixed(8)
			out.Money[scope.label+"_consumed"] = sums.Consumed.StringFixed(8)
		}
		for handle, item := range h.seed.Tasks {
			p, e := h.taskProof(tx, org, keyID, item)
			if e != nil {
				return e
			}
			out.Tasks[handle] = p
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, e
}
func (h *phaseAdminHarness) taskProof(tx *gorm.DB, org, keyID uint64, item phaseSeedTask) (map[string]any, error) {
	var t task.Task
	var st video.State
	var snapshot videobilling.Snapshot
	var hold ledger.Hold
	for _, q := range []*gorm.DB{tx.Where("org_id=? AND task_id=?", org, item.TaskID).Take(&t), tx.Where("org_id=? AND task_id=?", org, item.TaskID).Take(&st), tx.Where("org_id=? AND task_id=?", org, item.TaskID).Take(&snapshot)} {
		if q.Error != nil {
			return nil, q.Error
		}
	}
	if e := tx.Where("id=?", snapshot.HoldID).Take(&hold).Error; e != nil {
		return nil, e
	}
	var pin *string
	if h.schema == "0135_video_query_settlement_shape" {
		var row struct{ SettlementReceiptID *string }
		if e := tx.Table("video_task_states").Select("settlement_receipt_id").Where("org_id=? AND task_id=?", org, item.TaskID).Take(&row).Error; e != nil {
			return nil, e
		}
		pin = row.SettlementReceiptID
	}
	if t.APIKeyID != keyID || st.OrgID != t.OrgID || st.TaskID != t.TaskID {
		return nil, errPhaseHarness
	}
	id := map[string]any{"org_id": phaseNum(t.OrgID), "key_id": phaseNum(t.APIKeyID), "user_id": phaseUint(t.UserID), "task_id": t.TaskID, "create_operation_id": item.OperationID, "create_request_id": st.CreateRequestID, "upstream_task_id": t.UpstreamTaskID, "channel_id": phaseNum(t.ChannelID), "provider_scope_id": phaseNum(st.ProviderScopeID), "credential_snapshot_id": phaseNum(st.CredentialSnapshotID), "billing_snapshot_id": phaseNum(st.BillingSnapshotID), "model": t.Model, "upstream_model": st.UpstreamModel, "runtime_version": st.RuntimeVersion, "adapter_version": st.AdapterVersion, "protocol_contract_version": st.ProtocolContractVersion, "billing_schema_version": st.BillingSchemaVersion, "no_charge_statuses": st.NoChargeStatuses}
	out := map[string]any{"identity": id, "request_body_sha256": item.BodySHA, "task_status": t.Status, "billing_state": st.BillingState, "sync_state": st.SyncState, "artifact_status": st.ArtifactStatus, "row_version": phaseNum(st.RowVersion), "query_sequence": phaseNum(st.QuerySequence), "applied_query_sequence": phaseNum(st.AppliedQuerySequence), "query_lease_until": phaseTime(st.QueryLeaseUntil), "next_poll_at": st.NextPollAt.UTC().Format(time.RFC3339Nano), "deleted_at": phaseTime(st.DeletedAt), "settlement_receipt_id": pin}
	out["provider_request_body_sha256"] = item.ProviderBodySHA
	out["price"] = map[string]string{"rule_id": snapshot.RuleID, "rule_version": snapshot.RuleVersion, "retail_usd_per_1m": snapshot.RetailUSDPer1M.StringFixed(8), "supplier_cny_per_1m": snapshot.SupplierCNYPer1M.StringFixed(8), "effective_ratio": snapshot.EffectiveRatio.StringFixed(8), "fx_usd_per_cny": snapshot.FXUSDPerCNY.StringFixed(8), "fx_version": snapshot.FXVersion, "hold_usd": snapshot.HoldUSD.StringFixed(8)}
	money := map[string]any{"hold_status": fmt.Sprint(hold.Status), "hold_reserved_usd": hold.Amount.StringFixed(8), "hold_captured_usd": hold.CapturedAmount.StringFixed(8), "task_cost_usd": t.CostUSD.StringFixed(8), "task_actual_tokens": fmt.Sprint(t.ActualTokens), "observed_tokens": phaseInt(st.ObservedCompletionTokens), "total_tokens": phaseInt(st.TotalTokens)}
	var entries []videobilling.BillingEntry
	if e := tx.Where("org_id=? AND task_id=?", org, item.TaskID).Order("id").Find(&entries).Error; e != nil {
		return nil, e
	}
	entryProof := []map[string]any{}
	for _, v := range entries {
		entryProof = append(entryProof, map[string]any{"id": phaseNum(v.ID), "settlement_key": v.SettlementKey, "evidence_id": v.EvidenceID, "state": v.State, "reason": v.Reason, "journal_id": phaseUint(v.JournalID), "snapshot_id": phaseNum(v.SnapshotID), "hold_id": phaseNum(v.HoldID), "completion_tokens": phaseInt(v.CompletionTokens), "calculated_usd": v.CalculatedUSD.StringFixed(8), "captured_usd": v.CapturedUSD.StringFixed(8), "released_usd": v.ReleasedUSD.StringFixed(8), "absorbed_usd": v.AbsorbedUSD.StringFixed(8), "supplier_cny": v.SupplierCNY.StringFixed(8), "supplier_usd": v.SupplierUSD.StringFixed(8)})
	}
	money["entries"] = entryProof
	out["money"] = money
	var usage []videobilling.UsageEvidence
	if e := tx.Where("org_id=? AND task_id=?", org, item.TaskID).Order("id").Find(&usage).Error; e != nil {
		return nil, e
	}
	usageProof := []map[string]any{}
	for _, v := range usage {
		usageProof = append(usageProof, map[string]any{"id": phaseNum(v.ID), "evidence_id": v.EvidenceID, "source": v.Source, "source_record_id": v.SourceRecordID, "source_version": v.SourceVersion, "completion_tokens": phaseInt(v.CompletionTokens), "payload_ref": v.PayloadRef, "observed_at": v.ObservedAt.UTC().Format(time.RFC3339Nano)})
	}
	out["usage_evidence"] = usageProof
	var invocations []invocation.Invocation
	if e := tx.Where("org_id=? AND request_id=?", org, st.CreateRequestID).Order("id").Find(&invocations).Error; e != nil {
		return nil, e
	}
	invocationProof := []map[string]any{}
	for _, v := range invocations {
		invocationProof = append(invocationProof, map[string]any{"id": phaseNum(v.ID), "org_id": phaseNum(v.OrgID), "user_id": phaseUint(v.UserID), "key_id": phaseNum(v.APIKeyID), "request_id": v.RequestID, "channel_id": phaseNum(v.ChannelID), "model": v.Model, "provider": v.Provider, "modality": v.Modality, "status": v.Status, "status_code": v.StatusCode, "attempts": v.Attempts, "prompt_tokens": fmt.Sprint(v.PromptTokens), "completion_tokens": fmt.Sprint(v.CompletionTokens), "cost_usd": v.CostUSD.StringFixed(8), "usage_source": v.UsageSource, "protocol": v.Protocol})
	}
	out["invocations"] = invocationProof
	var events []video.Event
	if e := tx.Where("org_id=? AND task_id=?", org, item.TaskID).Order("sequence").Find(&events).Error; e != nil {
		return nil, e
	}
	eventProof := []map[string]any{}
	for _, v := range events {
		eventProof = append(eventProof, map[string]any{"event_id": v.ID, "sequence": phaseNum(v.Sequence), "type": v.Type, "request_id": v.RequestID, "evidence_ref": v.EvidenceRef})
	}
	out["events"] = eventProof
	storage := map[string]phaseTableProof{}
	for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_attempts", "video_execution_attempts", "video_response_inbox", "video_receipt_applications", "video_query_receipts", "video_upstream_bindings", "video_credential_snapshots", "video_events", "video_outbox", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations"} {
		column := "task_id"
		if table == "video_operations" {
			column = "resource_id"
		}
		p, e := h.tableProof(tx, table, "org_id=? AND "+column+"=?", org, item.TaskID)
		if e != nil {
			return nil, e
		}
		storage[table] = p
	}
	for _, table := range []string{"invocation_payloads", "invocation_payload_staging"} {
		p, e := h.tableProof(tx, table, "org_id=? AND payload_id=?", org, st.EffectivePayloadRef)
		if e != nil {
			return nil, e
		}
		storage[table] = p
	}
	out["storage"] = storage
	var ops []video.Operation
	if e := tx.Where("org_id=? AND resource_id=?", org, item.TaskID).Order("id").Find(&ops).Error; e != nil {
		return nil, e
	}
	opProof := []map[string]any{}
	for _, op := range ops {
		opProof = append(opProof, map[string]any{"operation_id": op.ID, "kind": op.Kind, "status": op.Status, "version": phaseNum(op.Version), "attempt_id": op.AttemptID, "payload_ref": op.PayloadRef, "receipt_ref": op.ReceiptRef, "runtime_version": op.RuntimeVersion, "adapter_version": op.AdapterVersion, "protocol_contract_version": op.ProtocolContractVersion, "lease_until": phaseTime(op.LeaseUntil), "receipt_applied_at": phaseTime(op.ReceiptAppliedAt), "error_code": op.ErrorCode})
	}
	out["operations"] = opProof
	var attempts []video.ExecutionAttempt
	if e := tx.Where("org_id=? AND task_id=?", org, item.TaskID).Order("attempt_id").Find(&attempts).Error; e != nil {
		return nil, e
	}
	attemptProof := []map[string]any{}
	for _, v := range attempts {
		attemptProof = append(attemptProof, map[string]any{"attempt_id": v.AttemptID, "operation_id": v.OperationID, "action": v.Action, "storage_protocol": v.StorageProtocol, "target_upstream_task_id": v.TargetUpstreamTaskID, "runtime_version": v.RuntimeVersion, "adapter_version": v.AdapterVersion, "protocol_contract_version": v.ProtocolContractVersion, "billing_schema_version": v.BillingSchemaVersion, "execution_schema_version": v.ExecutionSchemaVersion, "observation_schema_version": v.ObservationSchemaVersion, "input_identity_hmac": v.InputIdentityHMAC, "input_hmac_version": v.InputHMACVersion})
	}
	out["execution_attempts"] = attemptProof
	var receipts []video.QueryReceipt
	if e := tx.Where("org_id=? AND task_id=?", org, item.TaskID).Order("sequence,attempt_id").Find(&receipts).Error; e != nil {
		return nil, e
	}
	rp := []map[string]any{}
	for _, r := range receipts {
		var p phaseProgressColumns
		columns := "recovery_due_at,recovery_version"
		if h.schema == "0135_video_query_settlement_shape" {
			columns += ",generation_applied_at,settlement_kind,settlement_evidence_sha256"
		}
		if e := tx.Table("video_query_receipts").Select(columns).Where("org_id=? AND task_id=? AND attempt_id=?", org, item.TaskID, r.AttemptID).Take(&p).Error; e != nil {
			return nil, e
		}
		canonical, inner, e := phaseCanonical(r)
		if e != nil {
			return nil, e
		}
		outer := phaseOuterCanonical{Domain: "query-settlement-evidence-v1", OrgID: org, TaskID: item.TaskID, AttemptID: r.AttemptID, RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion, BillingSnapshotID: st.BillingSnapshotID, ProviderScopeID: st.ProviderScopeID, CredentialSnapshotID: st.CredentialSnapshotID, ChannelID: t.ChannelID, Model: t.Model, UpstreamTaskID: t.UpstreamTaskID, UpstreamModel: st.UpstreamModel, CreateRequestID: st.CreateRequestID, NoChargeStatuses: st.NoChargeStatuses, SettlementKind: p.SettlementKind, ReceiptEvidenceSHA256: inner}
		raw, e := json.Marshal(outer)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		full, e := h.tableProof(tx, "video_query_receipts", "org_id=? AND task_id=? AND attempt_id=?", org, item.TaskID, r.AttemptID)
		if e != nil {
			return nil, e
		}
		rp = append(rp, map[string]any{"attempt_id": r.AttemptID, "sequence": phaseNum(r.Sequence), "http_status": r.HTTPStatus, "outcome": r.Outcome, "applied_at": phaseTime(r.AppliedAt), "recovery_due_at": phaseTime(p.RecoveryDueAt), "recovery_version": phaseNum(p.RecoveryVersion), "error_code": r.ApplyErrorCode, "generation_applied_at": phaseTime(p.GenerationAppliedAt), "settlement_kind": p.SettlementKind, "stored_outer_sha256": p.SettlementEvidenceSHA256, "receipt_evidence_json": canonical, "receipt_evidence_sha256": inner, "outer_evidence_json": string(raw), "outer_evidence_sha256": hex.EncodeToString(sum[:]), "full_sha256": full.SHA})
	}
	out["receipts"] = rp
	return out, nil
}
