package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// Anonymous physical DTO, so the common server adapter also compiles against N
// without pretending N's Go model knows the four expanded schema columns.
type phaseAdminReceipt struct {
	AttemptID, TaskID, PayloadRef, RuntimeVersion, SettlementKind, SettlementEvidenceSHA256, ApplyErrorCode, ObservationJSON string
	OrgID, Sequence, RecoveryVersion                                                                                         uint64
	CompletionTokens, TotalTokens                                                                                            *int64 `gorm:"-"`
	ReceivedAt                                                                                                               time.Time
	GenerationAppliedAt, AppliedAt, RecoveryDueAt                                                                            *time.Time
}

type phaseAdminOracle struct {
	DatabaseUTC   time.Time
	Seed          phaseAdminSeed
	Task          task.Task
	State         video.State
	Pin           *string
	Receipt       phaseAdminReceipt
	Snapshot      videobilling.Snapshot
	Hold          ledger.Hold
	Key           identity.APIKey
	Posted, Held  decimal.Decimal
	Entries       []videobilling.BillingEntry
	Evidence      []videobilling.UsageEvidence
	Invocations   []invocation.Invocation
	Applications  []videobilling.AdjustmentApplication
	Journals      []ledger.JournalEntry
	Reservations  []videobilling.QuotaReservation
	Buckets       []videobilling.QuotaBucket
	Events        []video.Event
	Outbox        []video.Outbox
	MoneyHashes   map[string]string
	StorageHashes map[string]string
}

// Full database-column bytes, including json:"-" fields. Only hashes leave the
// private adapter. Explicit fixed queries below define the authorized scope.
func phaseAdminRowsHash(tx *gorm.DB, query string, args ...any) (string, error) {
	rows, err := tx.Raw(query, args...).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	all := make([][]*string, 0)
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return "", err
		}
		line := make([]*string, len(columns))
		for i, value := range values {
			if value != nil {
				x := base64.StdEncoding.EncodeToString(value)
				line[i] = &x
			}
		}
		all = append(all, line)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if err = rows.Close(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Columns []string
		Rows    [][]*string
	}{columns, all})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (a *phaseAdminAdapter) readOracle(ctx context.Context) (phaseAdminOracle, error) {
	o := phaseAdminOracle{Seed: a.seed, MoneyHashes: map[string]string{}, StorageHashes: map[string]string{}}
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&o.DatabaseUTC).Error; err != nil || o.DatabaseUTC.IsZero() {
			return errors.New("admin_oracle_database_time")
		}
		org, id := a.seed.OrgID, a.seed.TaskID
		if err := tx.Where("org_id = ? AND task_id = ?", org, id).Take(&o.Task).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND task_id = ?", org, id).Take(&o.State).Error; err != nil {
			return err
		}
		var p struct{ SettlementReceiptID *string }
		if err := tx.Table("video_task_states").Select("settlement_receipt_id").Where("org_id = ? AND task_id = ?", org, id).Take(&p).Error; err != nil {
			return err
		}
		o.Pin = p.SettlementReceiptID
		if err := tx.Table("video_query_receipts").Where("org_id = ? AND task_id = ? AND attempt_id = ?", org, id, a.seed.QueryAttemptID).Take(&o.Receipt).Error; err != nil {
			return err
		}
		var observation struct {
			TaskID           string `json:"task_id"`
			Model            string `json:"model"`
			Status           string `json:"status"`
			CompletionTokens *int64 `json:"completion_tokens"`
			TotalTokens      *int64 `json:"total_tokens"`
		}
		if err := phaseAdminDecode([]byte(o.Receipt.ObservationJSON), &observation); err != nil {
			return err
		}
		if observation.TaskID != a.seed.UpstreamID || (observation.Model != "" && observation.Model != o.State.UpstreamModel) || observation.Status != "succeeded" {
			return errors.New("admin_query_observation_identity")
		}
		o.Receipt.CompletionTokens, o.Receipt.TotalTokens = observation.CompletionTokens, observation.TotalTokens
		if err := tx.Where("id = ? AND org_id = ? AND task_id = ?", o.State.BillingSnapshotID, org, id).Take(&o.Snapshot).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", o.Snapshot.HoldID).Take(&o.Hold).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ? AND org_id = ?", a.seed.KeyID, org).Take(&o.Key).Error; err != nil {
			return err
		}
		var err error
		o.Posted, o.Held, err = ledger.OrgBalances(tx, org, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		for _, dest := range []any{&o.Entries, &o.Evidence, &o.Applications, &o.Reservations} {
			if err = tx.Where("org_id = ? AND task_id = ?", org, id).Order("id").Find(dest).Error; err != nil {
				return err
			}
		}
		if err = tx.Where("org_id = ? AND request_id = ?", org, a.seed.CreateRequestID).Order("id").Find(&o.Invocations).Error; err != nil {
			return err
		}
		if err = tx.Where("ref_id = ? AND ref_type IN ?", id, []string{"video", "video_adjustment"}).Order("id").Find(&o.Journals).Error; err != nil {
			return err
		}
		if err = tx.Table("video_quota_buckets").Where("id IN (SELECT bucket_id FROM video_quota_reservations WHERE org_id = ? AND task_id = ?)", org, id).Order("id").Find(&o.Buckets).Error; err != nil {
			return err
		}
		if err = tx.Where("org_id = ? AND task_id = ?", org, id).Order("sequence").Find(&o.Events).Error; err != nil {
			return err
		}
		if err = tx.Where("org_id = ? AND task_id = ?", org, id).Order("event_id").Find(&o.Outbox).Error; err != nil {
			return err
		}
		reads := []struct {
			name, query string
			args        []any
			money       bool
		}{
			{"snapshot", "SELECT * FROM video_billing_snapshots WHERE org_id=? AND task_id=? ORDER BY id", []any{org, id}, true},
			{"hold", "SELECT * FROM ledger_holds WHERE id=?", []any{o.Snapshot.HoldID}, true},
			{"accounts", "SELECT * FROM ledger_accounts WHERE org_id=? ORDER BY id", []any{org}, true},
			{"key", "SELECT * FROM api_keys WHERE org_id=? AND id=?", []any{org, a.seed.KeyID}, true},
			{"entries", "SELECT * FROM video_billing_entries WHERE org_id=? AND task_id=? ORDER BY id", []any{org, id}, true},
			{"evidence", "SELECT * FROM video_usage_evidence WHERE org_id=? AND task_id=? ORDER BY id", []any{org, id}, true},
			{"applications", "SELECT * FROM video_adjustment_applications WHERE org_id=? AND task_id=? ORDER BY id", []any{org, id}, true},
			{"reservations", "SELECT * FROM video_quota_reservations WHERE org_id=? AND task_id=? ORDER BY id", []any{org, id}, true},
			{"buckets", "SELECT b.* FROM video_quota_buckets b WHERE b.id IN (SELECT bucket_id FROM video_quota_reservations WHERE org_id=? AND task_id=?) ORDER BY b.id", []any{org, id}, true},
			{"journals", "SELECT * FROM ledger_journal WHERE ref_id=? AND ref_type IN ('video','video_adjustment') ORDER BY id", []any{id}, true},
			{"invocations", "SELECT * FROM invocations WHERE org_id=? AND request_id=? ORDER BY id", []any{org, a.seed.CreateRequestID}, true},
			{"task", "SELECT * FROM tasks WHERE org_id=? AND task_id=?", []any{org, id}, false},
			{"state", "SELECT * FROM video_task_states WHERE org_id=? AND task_id=?", []any{org, id}, false},
			{"receipt", "SELECT * FROM video_query_receipts WHERE org_id=? AND task_id=? AND attempt_id=?", []any{org, id, a.seed.QueryAttemptID}, false},
			{"events", "SELECT * FROM video_events WHERE org_id=? AND task_id=? ORDER BY sequence", []any{org, id}, false},
			{"outbox", "SELECT * FROM video_outbox WHERE org_id=? AND task_id=? ORDER BY event_id", []any{org, id}, false},
			{"operations", "SELECT * FROM video_operations WHERE org_id=? AND resource_id=? ORDER BY id", []any{org, id}, false},
		}
		for _, read := range reads {
			h, e := phaseAdminRowsHash(tx, read.query, read.args...)
			if e != nil {
				return e
			}
			o.StorageHashes[read.name] = h
			if read.money {
				o.MoneyHashes[read.name] = h
			}
		}
		return ctx.Err()
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return o, err
}

func phaseAdminMoney(v decimal.Decimal, literal string) bool {
	return v.Equal(decimal.RequireFromString(literal))
}
func phaseAdminTokens(v *int64, n int64) bool { return v != nil && *v == n }

func (o phaseAdminOracle) requirePendingSeed() error {
	r := o.Receipt
	if r.RecoveryDueAt == nil || r.RecoveryDueAt.After(o.DatabaseUTC) {
		return errors.New("admin_seed_B_not_due")
	}
	if o.Task.OrgID != o.Seed.OrgID || o.Task.TaskID != o.Seed.TaskID || o.Task.APIKeyID != o.Seed.KeyID || o.Task.UpstreamTaskID != o.Seed.UpstreamID || o.Task.Status != "succeeded" || o.State.CreateRequestID != o.Seed.CreateRequestID || o.State.ProviderScopeID != o.Seed.ScopeID || o.State.BillingState != "reserved" || o.State.DeletedAt != nil || o.State.TotalTokens != nil || o.Pin == nil || *o.Pin != o.Seed.QueryAttemptID || r.GenerationAppliedAt == nil || r.AppliedAt != nil || r.RecoveryDueAt == nil || r.RecoveryVersion != 1 || r.SettlementKind != "usage" || r.RuntimeVersion != "ark-official-generation-settlement-v2" || r.ApplyErrorCode != "" || len(r.SettlementEvidenceSHA256) != 64 || !phaseAdminTokens(r.CompletionTokens, 100) || !phaseAdminTokens(r.TotalTokens, 100) {
		return errors.New("admin_seed_not_committed_G_pending_B")
	}
	if len(o.Entries) != 0 || len(o.Evidence) != 0 || len(o.Invocations) != 0 || len(o.Applications) != 0 || len(o.Journals) != 0 || !phaseAdminMoney(o.Snapshot.HoldUSD, "0.864") || !phaseAdminMoney(o.Hold.Amount, "0.864") || !o.Hold.CapturedAmount.IsZero() || o.Hold.Status != ledger.HoldActive || !phaseAdminMoney(o.Key.UsedAmount, "0.864") || !phaseAdminMoney(o.Posted, "100") || !phaseAdminMoney(o.Held, "0.864") {
		return errors.New("admin_seed_funds_invalid")
	}
	return nil
}

func phaseAdminSameMoney(a, b phaseAdminOracle) error {
	if !reflect.DeepEqual(a.MoneyHashes, b.MoneyHashes) {
		return errors.New("admin_unexpected_money_change")
	}
	return nil
}

func (o phaseAdminOracle) require100(winner string, line phaseAdminLine) error {
	if len(o.Entries) != 1 || len(o.Evidence) != 1 || len(o.Applications) != 0 || len(o.Journals) != 1 {
		return errors.New("admin_initial_cardinality")
	}
	e, u := o.Entries[0], o.Evidence[0]
	j := o.Journals[0]
	if j.Kind != ledger.KindCapture || j.HoldID == nil || *j.HoldID != o.Hold.ID || j.AccountID != o.Hold.AccountID || j.IdempotencyKey != fmt.Sprintf("capture:%d", o.Hold.ID) || j.RefType != "video" || j.RefID != o.Seed.TaskID || !phaseAdminMoney(j.Delta, "-0.0008") {
		return errors.New("admin_capture_journal_literal")
	}
	if e.SettlementKey != "initial" || e.State != "settled" || e.OrgID != o.Seed.OrgID || e.TaskID != o.Seed.TaskID || e.SnapshotID != o.Snapshot.ID || e.HoldID != o.Hold.ID || e.Reason != "actual_usage_settled" || e.JournalID == nil || *e.JournalID != o.Journals[0].ID || !phaseAdminTokens(e.CompletionTokens, 100) || !phaseAdminMoney(e.CalculatedUSD, "0.0008") || !phaseAdminMoney(e.CapturedUSD, "0.0008") || !phaseAdminMoney(e.ReleasedUSD, "0.8632") || !e.AbsorbedUSD.IsZero() || !phaseAdminMoney(e.SupplierCNY, "0.0046") || !phaseAdminMoney(e.SupplierUSD, "0.000644") {
		return errors.New("admin_initial_literal")
	}
	if o.Task.ActualTokens != 100 || !phaseAdminMoney(o.Task.CostUSD, "0.0008") || !phaseAdminTokens(o.State.ObservedCompletionTokens, 100) || o.State.BillingState != "settled" || !phaseAdminMoney(o.Key.UsedAmount, "0.0008") || !phaseAdminMoney(o.Posted, "99.9992") || !o.Held.IsZero() || !phaseAdminMoney(o.Hold.CapturedAmount, "0.0008") {
		return errors.New("admin_initial_projection")
	}
	if u.OrgID != o.Seed.OrgID || u.TaskID != o.Seed.TaskID || !phaseAdminTokens(u.CompletionTokens, 100) || u.EvidenceID != e.EvidenceID {
		return errors.New("admin_initial_evidence_identity")
	}
	if winner == "admin" {
		if u.Source != "bill_exact" || u.EvidenceID != fmt.Sprintf("bill-line-%d", line.LineID) || u.SourceRecordID != line.RecordID || u.SourceVersion != line.Version || u.PayloadRef != line.PayloadRef || !u.ObservedAt.Equal(line.ObservedAt) || len(o.Invocations) != 0 || o.State.TotalTokens != nil {
			return errors.New("admin_first_source_or_invocation")
		}
	} else if winner == "settlement" {
		if u.Source != "task_get" || u.EvidenceID != o.Seed.QueryAttemptID || u.SourceRecordID != o.Seed.UpstreamID || u.SourceVersion != o.Seed.QueryAttemptID || u.PayloadRef != o.Receipt.PayloadRef || !u.ObservedAt.Equal(o.Receipt.ReceivedAt.UTC()) || len(o.Invocations) != 1 || !phaseAdminTokens(o.State.TotalTokens, 100) {
			return errors.New("B_first_source_or_invocation")
		}
		v := o.Invocations[0]
		if !reflect.DeepEqual(v.UserID, o.Task.UserID) || v.ChannelID != o.Task.ChannelID || v.Model != o.Task.Model {
			return errors.New("B_invocation_subject_identity")
		}
		if v.OrgID != o.Seed.OrgID || v.RequestID != o.Seed.CreateRequestID || v.APIKeyID != o.Seed.KeyID || v.CompletionTokens != 100 || !phaseAdminMoney(v.CostUSD, "0.0008") || v.Status != "success" || v.StatusCode != 200 || v.Attempts != 1 || v.UsageSource != "upstream" || v.Protocol != "ark_v3" {
			return errors.New("B_invocation_literal")
		}
	} else {
		return errors.New("admin_winner_invalid")
	}
	return o.requireQuota("0.0008", "0.0046", "0.000644", 100)
}

func (o phaseAdminOracle) requireQuota(usd, cny, supplierUSD string, tokens int64) error {
	buckets := map[uint64]videobilling.QuotaBucket{}
	for _, b := range o.Buckets {
		buckets[b.ID] = b
	}
	seenCustomer, seenSupplier := false, false
	for _, r := range o.Reservations {
		b, ok := buckets[r.BucketID]
		if !ok || !b.ReservedAmount.IsZero() || b.ReservedUnits != 0 || r.State == "active" {
			return errors.New("admin_quota_not_settled")
		}
		switch r.Metric {
		case "usd":
			amount := usd
			if r.Supplier {
				amount = supplierUSD
			} else {
				seenCustomer = true
			}
			if !phaseAdminMoney(b.ConsumedAmount, amount) {
				return errors.New("admin_quota_usd")
			}
		case "cny":
			if !r.Supplier || b.OrgID != 0 || !phaseAdminMoney(b.ConsumedAmount, cny) {
				return errors.New("admin_quota_provider_cny")
			}
			seenSupplier = true
		case "tokens":
			if b.ConsumedUnits != tokens {
				return errors.New("admin_quota_tokens")
			}
		case "tasks":
			if b.ConsumedUnits != 1 {
				return errors.New("admin_quota_tasks")
			}
		default:
			return errors.New("admin_quota_metric_unknown")
		}
	}
	if !seenCustomer || !seenSupplier {
		return errors.New("admin_quota_fixture_missing")
	}
	return nil
}

func (o phaseAdminOracle) require90(before phaseAdminOracle, pid, appID uint64, line phaseAdminLine) error {
	for _, k := range []string{"snapshot", "hold", "entries", "evidence", "reservations", "invocations"} {
		if o.MoneyHashes[k] != before.MoneyHashes[k] {
			return errors.New("admin_adjustment_changed_original_" + k)
		}
	}
	if len(o.Applications) != 1 || len(o.Journals) != 2 || len(o.Invocations) != 0 || o.State.TotalTokens != nil || o.State.BillingState != "adjusted" || o.Task.ActualTokens != 90 || !phaseAdminMoney(o.Task.CostUSD, "0.00072") || !phaseAdminTokens(o.State.ObservedCompletionTokens, 90) || !phaseAdminMoney(o.Key.UsedAmount, "0.00072") || !phaseAdminMoney(o.Posted, "99.99928") || !o.Held.IsZero() || !reflect.DeepEqual(before.Receipt, o.Receipt) || !reflect.DeepEqual(before.Pin, o.Pin) {
		return errors.New("admin_adjustment_projection")
	}
	a := o.Applications[0]
	if a.ID != appID || a.ProposalID != pid || a.LineID != line.LineID || a.ReviewID == 0 || a.JournalID == nil || a.OrgID != o.Seed.OrgID || a.TaskID != o.Seed.TaskID || a.TokensDelta != -10 || !phaseAdminTokens(a.CompletionTokens, 90) || !phaseAdminMoney(a.CustomerDeltaUSD, "-0.00008") || !phaseAdminMoney(a.SupplierDeltaCNY, "-0.00046") || !phaseAdminMoney(a.SupplierDeltaUSD, "-0.0000644") || !phaseAdminMoney(a.NetUSD, "0.00072") || !phaseAdminMoney(a.CalculatedUSD, "0.00072") || !a.AbsorbedUSD.IsZero() || !phaseAdminMoney(a.SupplierCNY, "0.00414") || !phaseAdminMoney(a.SupplierUSD, "0.0005796") {
		return errors.New("admin_adjustment_literal")
	}
	found := false
	for _, j := range o.Journals {
		if j.ID == *a.JournalID {
			found = j.IdempotencyKey == fmt.Sprintf("video-adjustment:%d", pid) && j.RefType == "video_adjustment" && j.RefID == o.Seed.TaskID && j.Kind == ledger.KindCredit && phaseAdminMoney(j.Delta, "0.00008")
		}
	}
	if !found {
		return errors.New("admin_adjustment_journal")
	}
	return o.requireQuota("0.00072", "0.00414", "0.0005796", 90)
}

func phaseAdminEventDelta(before, after phaseAdminOracle, types []string, requests []string, refs []string) error {
	if len(types) != len(requests) || len(types) != len(refs) || len(after.Events) != len(before.Events)+len(types) || len(after.Outbox) != len(before.Outbox)+len(types) {
		return errors.New("admin_event_cardinality")
	}
	if !reflect.DeepEqual(before.Events, after.Events[:len(before.Events)]) {
		return errors.New("admin_old_event_changed")
	}
	for _, original := range before.Outbox {
		found := false
		for _, current := range after.Outbox {
			if current.EventID == original.EventID {
				if !reflect.DeepEqual(current, original) {
					return errors.New("admin_old_outbox_changed")
				}
				found = true
			}
		}
		if !found {
			return errors.New("admin_old_outbox_missing")
		}
	}
	for i, typ := range types {
		e := after.Events[len(before.Events)+i]
		if e.Type != typ || e.RequestID != requests[i] || e.EvidenceRef != refs[i] || e.OrgID != before.Seed.OrgID || e.TaskID != before.Seed.TaskID || e.FromStatus != "succeeded" || e.ToStatus != "succeeded" {
			return errors.New("admin_event_source")
		}
		n := 0
		for _, b := range after.Outbox {
			if b.EventID == e.ID {
				if b.OrgID != e.OrgID || b.TaskID != e.TaskID || b.Topic != e.Type {
					return errors.New("admin_outbox_identity")
				}
				n++
			}
		}
		if n != 1 {
			return errors.New("admin_outbox_cardinality")
		}
	}
	return nil
}

// Additional server proof contains only selected actual provenance and review
// fields. It complements the identical common task/financial snapshot; no
// encrypted source body, API key, session token or driver error is exported.
func (a *phaseAdminAdapter) managementProof(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sources := []ArkBillEvidenceSource{}
		if err := tx.Where("source_key IN ?", []string{a.prefix + "-r1", a.prefix + "-r2"}).Order("id").Find(&sources).Error; err != nil {
			return err
		}
		sp := []map[string]any{}
		for _, s := range sources {
			sp = append(sp, map[string]any{"id": phaseNum(s.ID), "scope_id": phaseNum(s.ProviderScopeID), "state": s.State, "source_kind": s.SourceKind, "version": s.SourceVersion, "file_sha256": s.SourceFileSHA256, "payload_ref": s.PayloadRef, "uploaded_by": phaseNum(s.UploadedBy), "reviewed_by": phaseNum(s.ReviewedBy), "reviewed_at": phaseTime(s.ReviewedAt)})
		}
		out["sources"] = sp
		lines := []videobilling.BillLine{}
		if err := tx.Where("provider_scope_id=? AND upstream_task_id=? AND source_record_id=?", a.seed.ScopeID, a.seed.UpstreamID, "row-"+a.prefix).Order("id").Find(&lines).Error; err != nil {
			return err
		}
		lp := []map[string]any{}
		for _, l := range lines {
			lp = append(lp, map[string]any{"id": phaseNum(l.ID), "source": l.Source, "source_record_id": l.SourceRecordID, "source_version": l.SourceVersion, "source_revision": fmt.Sprint(l.SourceRevision), "upstream_task_id": l.UpstreamTaskID, "model": l.Model, "completion_tokens": phaseInt(l.CompletionTokens), "amount_cny": l.AmountCNY.StringFixed(8), "payload_ref": l.PayloadRef, "observed_at": l.ObservedAt.UTC().Format(time.RFC3339Nano), "content_sha256": l.ContentHash, "evidence_sha256": l.EvidenceHash})
		}
		out["bill_lines"] = lp
		proposals := []videobilling.AdjustmentProposal{}
		applications := []videobilling.AdjustmentApplication{}
		for _, dest := range []any{&proposals, &applications} {
			if err := tx.Where("org_id=? AND task_id=?", a.seed.OrgID, a.seed.TaskID).Order("id").Find(dest).Error; err != nil {
				return err
			}
		}
		pp, rp, ap := []map[string]any{}, []map[string]any{}, []map[string]any{}
		for _, p := range proposals {
			pp = append(pp, map[string]any{"id": phaseNum(p.ID), "line_id": phaseNum(p.LineID), "proposer_id": phaseNum(p.ProposerID), "supplier_only": p.SupplierOnly, "reason": p.Reason, "base_application_id": phaseNum(p.BaseApplicationID), "base_net_usd": p.BaseNetUSD.StringFixed(8), "target_net_usd": p.TargetNetUSD.StringFixed(8), "target_supplier_cny": p.TargetCNY.StringFixed(8), "target_tokens": phaseInt(p.TargetTokens)})
			var reviews []videobilling.AdjustmentReview
			if err := tx.Where("org_id=? AND proposal_id=?", a.seed.OrgID, p.ID).Order("id").Find(&reviews).Error; err != nil {
				return err
			}
			for _, r := range reviews {
				rp = append(rp, map[string]any{"id": phaseNum(r.ID), "proposal_id": phaseNum(r.ProposalID), "reviewer_id": phaseNum(r.ReviewerID), "approved": r.Approved, "reason": r.Reason})
			}
		}
		for _, app := range applications {
			ap = append(ap, map[string]any{"id": phaseNum(app.ID), "proposal_id": phaseNum(app.ProposalID), "review_id": phaseNum(app.ReviewID), "line_id": phaseNum(app.LineID), "journal_id": phaseUint(app.JournalID), "customer_delta_usd": app.CustomerDeltaUSD.StringFixed(8), "supplier_delta_cny": app.SupplierDeltaCNY.StringFixed(8), "supplier_delta_usd": app.SupplierDeltaUSD.StringFixed(8), "tokens_delta": fmt.Sprint(app.TokensDelta), "net_usd": app.NetUSD.StringFixed(8), "calculated_usd": app.CalculatedUSD.StringFixed(8), "absorbed_usd": app.AbsorbedUSD.StringFixed(8), "supplier_cny": app.SupplierCNY.StringFixed(8), "supplier_usd": app.SupplierUSD.StringFixed(8), "completion_tokens": phaseInt(app.CompletionTokens)})
			var p videobilling.AdjustmentProposal
			var r videobilling.AdjustmentReview
			if err := tx.Where("org_id=? AND id=?", a.seed.OrgID, app.ProposalID).Take(&p).Error; err != nil {
				return err
			}
			if err := tx.Where("org_id=? AND id=?", a.seed.OrgID, app.ReviewID).Take(&r).Error; err != nil {
				return err
			}
			if p.TaskID != a.seed.TaskID || p.ProposerID != a.seed.ActorIDs[0] || p.SupplierOnly || p.BaseApplicationID != 0 || p.LineID != app.LineID || r.ProposalID != p.ID || r.ReviewerID != a.seed.ActorIDs[1] || !r.Approved {
				return errors.New("admin_review_chain_identity")
			}
		}
		out["proposals"], out["reviews"], out["applications"] = pp, rp, ap
		var journals []ledger.JournalEntry
		var movements []ledger.Movement
		if err := tx.Where("ref_id=? AND ref_type IN ?", a.seed.TaskID, []string{"video", "video_adjustment"}).Order("id").Find(&journals).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id=? AND ref_id=?", a.seed.OrgID, a.seed.TaskID).Order("id").Find(&movements).Error; err != nil {
			return err
		}
		jp, mp := []map[string]any{}, []map[string]any{}
		for _, j := range journals {
			jp = append(jp, map[string]any{"id": phaseNum(j.ID), "account_id": phaseNum(j.AccountID), "kind": j.Kind, "delta": j.Delta.StringFixed(8), "hold_id": phaseUint(j.HoldID), "idempotency_key": j.IdempotencyKey, "ref_type": j.RefType, "ref_id": j.RefID})
		}
		for _, m := range movements {
			mp = append(mp, map[string]any{"id": phaseNum(m.ID), "operation_id": phaseNum(m.OperationID), "type": m.Type, "direction": m.Direction, "amount": m.Amount.StringFixed(8), "posted_delta": m.PostedDelta.StringFixed(8), "held_delta": m.HeldDelta.StringFixed(8), "journal_id": phaseUint(m.JournalID), "hold_id": phaseUint(m.HoldID), "original_movement_id": phaseUint(m.OriginalMovementID), "original_journal_id": phaseUint(m.OriginalJournalID), "original_hold_id": phaseUint(m.OriginalHoldID), "correction_proposal_id": phaseUint(m.CorrectionProposalID), "correction_review_id": phaseUint(m.CorrectionReviewID)})
		}
		out["journals"], out["movements"] = jp, mp
		for _, app := range applications {
			n := 0
			for _, m := range movements {
				if m.JournalID != nil && app.JournalID != nil && *m.JournalID == *app.JournalID {
					if m.CorrectionProposalID == nil || *m.CorrectionProposalID != app.ProposalID || m.CorrectionReviewID == nil || *m.CorrectionReviewID != app.ReviewID || m.OriginalHoldID == nil || m.OriginalJournalID == nil || m.OriginalMovementID == nil || !phaseAdminMoney(m.PostedDelta, "0.00008") {
						return errors.New("admin_correction_link")
					}
					n++
				}
			}
			if n != 1 {
				return errors.New("admin_correction_link_count")
			}
		}
		audits := []map[string]any{}
		for _, request := range a.requestSlice(0) {
			var rows []AuditLog
			if err := tx.Where("JSON_UNQUOTE(JSON_EXTRACT(detail,'$.request_id'))=?", request.RequestID).Order("id").Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				var detail struct {
					Replay *bool `json:"replay"`
				}
				if err := json.Unmarshal([]byte(row.Detail), &detail); err != nil {
					return err
				}
				sum := sha256.Sum256([]byte(row.Detail))
				audits = append(audits, map[string]any{"id": phaseNum(row.ID), "actor": row.Actor, "action": row.Action, "request_id": request.RequestID, "detail_sha256": hex.EncodeToString(sum[:]), "replay": detail.Replay})
			}
		}
		out["audits"] = audits
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}
