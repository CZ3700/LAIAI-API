package video

import (
	"bytes"
	"context"
	"errors"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQueryPhaseClosedCombinations(t *testing.T) {
	_, base, row := querySettlementPending(t)
	for name, tc := range map[string]struct {
		change func(*State, *QueryReceipt)
		want   queryApplyPhase
	}{
		"b":            {func(*State, *QueryReceipt) {}, queryPhaseSettlementV2},
		"b_bad_digest": {func(_ *State, r *QueryReceipt) { r.SettlementEvidenceSHA256 = "bad" }, queryPhaseSettlementV2},
		"g": {func(_ *State, r *QueryReceipt) {
			r.GenerationAppliedAt = nil
			r.SettlementKind = ""
			r.SettlementEvidenceSHA256 = ""
		}, queryPhaseGenerationV2},
		"complete": {func(_ *State, r *QueryReceipt) { r.AppliedAt = r.GenerationAppliedAt }, queryPhaseComplete},
		"review":   {func(_ *State, r *QueryReceipt) { r.RecoveryDueAt = nil }, queryPhaseReview},
		"v1_ignores_s2": {func(s *State, r *QueryReceipt) {
			s.RuntimeVersion = videoruntime.RuntimeV1
			r.RuntimeVersion = videoruntime.RuntimeV1
			r.SettlementKind = "future"
			r.SettlementEvidenceSHA256 = "bad"
		}, queryPhaseAtomicV1},
		"mixed":           {func(_ *State, r *QueryReceipt) { r.RuntimeVersion = videoruntime.RuntimeV1 }, 0},
		"unknown":         {func(s *State, r *QueryReceipt) { s.RuntimeVersion = "future"; r.RuntimeVersion = "future" }, 0},
		"schema":          {func(_ *State, r *QueryReceipt) { r.ObservationSchemaVersion = 2 }, 0},
		"snapshot_schema": {func(s *State, _ *QueryReceipt) { s.BillingSchemaVersion = "future" }, 0},
		"g_partial":       {func(_ *State, r *QueryReceipt) { r.GenerationAppliedAt = nil }, 0},
		"none_unfinished": {func(_ *State, r *QueryReceipt) { r.SettlementKind = querySettlementNone }, 0},
		"pin_missing":     {func(s *State, _ *QueryReceipt) { s.SettlementReceiptID = nil }, 0},
		"pin_padded":      {func(s *State, r *QueryReceipt) { v := r.AttemptID + " "; s.SettlementReceiptID = &v }, 0},
		"generation_year_one": {func(_ *State, r *QueryReceipt) {
			v := time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC)
			r.GenerationAppliedAt = &v
		}, 0},
		"generation_zero": {func(_ *State, r *QueryReceipt) { v := time.Time{}; r.GenerationAppliedAt = &v }, 0},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := base, row
			tc.change(&s, &r)
			p, e := queryPhaseOf(s, r)
			if p != tc.want || (e != nil) != (tc.want == 0) {
				t.Fatal("phase accepted an unsupported or incomplete combination", p, e)
			}
		})
	}
}

func TestQueryPhaseTokenBindsCurrentFacts(t *testing.T) {
	row, st, r := querySettlementPending(t)
	observed, err := observeQueryPhaseRecovery(row, st, r)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*State, *QueryReceipt){
		"version": func(_ *State, r *QueryReceipt) { r.RecoveryVersion++ },
		"pin":     func(s *State, _ *QueryReceipt) { v := "other"; s.SettlementReceiptID = &v },
		"generated": func(_ *State, r *QueryReceipt) {
			v := r.GenerationAppliedAt.Add(time.Millisecond)
			r.GenerationAppliedAt = &v
		},
		"stored_hash": func(_ *State, r *QueryReceipt) { r.SettlementEvidenceSHA256 = "bad" },
		"kind":        func(_ *State, r *QueryReceipt) { r.SettlementKind = querySettlementProviderNoCharge },
		"fixed_scope": func(s *State, _ *QueryReceipt) { s.ProviderScopeID++ },
		"raw":         func(_ *State, r *QueryReceipt) { r.ObservationJSON += " " },
		"review":      func(_ *State, r *QueryReceipt) { r.RecoveryDueAt = nil },
		"applied":     func(_ *State, r *QueryReceipt) { r.AppliedAt = r.GenerationAppliedAt },
	} {
		t.Run(name, func(t *testing.T) {
			s, c := st, r
			change(&s, &c)
			newToken, _ := observeQueryPhaseRecovery(row, s, c)
			if queryPhaseTokenMatches(observed, newToken) {
				t.Fatal("late diagnostic accepted different current facts")
			}
		})
	}
	// The copied token never aliases pin/time pointers, nor normalizes bad UTF8.
	bad := r
	bad.ProviderRequestID = string([]byte{255})
	a, e := observeQueryPhaseRecovery(row, st, bad)
	if e != nil {
		t.Fatal(e)
	}
	bad.ProviderRequestID = string([]byte{254})
	b, e := observeQueryPhaseRecovery(row, st, bad)
	if e != nil || a.evidence != b.evidence || queryPhaseTokenMatches(a, b) {
		t.Fatal("raw invalid UTF8 diagnostic alias")
	}
	changed := *st.SettlementReceiptID + "x"
	*st.SettlementReceiptID = changed
	if observed.progress.pin == changed {
		t.Fatal("token aliases caller pin")
	}
	// A G token cannot diagnose the later B phase even with the same receipt.
	_, s, g := querySettlementFixture(t, 0)
	gToken, e := observeQueryPhaseRecovery(row, s, g)
	if e != nil {
		t.Fatal(e)
	}
	if queryPhaseTokenMatches(gToken, observed) {
		t.Fatal("G token crossed B commit")
	}
}

func TestQueryPhaseSQLKeepsOneBoundedClosedQueue(t *testing.T) {
	var out bytes.Buffer
	db := authorityClockUnitDB(t, &out).Session(&gorm.Session{DryRun: true})
	base := func() *gorm.DB {
		return db.Table("video_query_receipts AS r").Select("r.*").Joins("JOIN video_task_states v ON v.org_id=r.org_id AND v.task_id=r.task_id")
	}
	for _, b := range []bool{false, true} {
		q := base()
		if b {
			q = querySettlementEligible(q)
		} else {
			q = queryGenerationEligible(q)
		}
		var rows []QueryReceipt
		q = q.Order("r.recovery_due_at,r.received_at,r.attempt_id").Limit(40).Find(&rows)
		sql := q.Statement.SQL.String()
		if q.Error != nil || strings.Count(sql, "LIMIT") != 1 || !strings.Contains(sql, "LIMIT 40") || !strings.Contains(sql, "UTC_TIMESTAMP(3)") || !strings.Contains(sql, "bs.billing_schema_version = v.billing_schema_version") {
			t.Fatal("candidate lost DB due, fixed contract, or one caller limit")
		}
		if !strings.Contains(sql, noTaskSettlementConflict) {
			t.Fatal("closed task marker was not excluded before the shared LIMIT")
		}
		if !strings.Contains(sql, "v.runtime_version = ? AND v.adapter_version = ? AND v.protocol_contract_version = ? AND v.billing_schema_version = ?") {
			t.Fatal("runtime Cartesian set replaced closed tuples")
		}
		if b {
			if strings.Contains(sql, "settlement_evidence_sha256") || !strings.Contains(sql, "OCTET_LENGTH(v.settlement_receipt_id)") || !strings.Contains(sql, noTaskSettlementConflict) {
				t.Fatal("bad digest hidden or task barrier/pin lost")
			}
		} else if !strings.Contains(sql, "r.generation_applied_at IS NULL") || !strings.Contains(sql, "r.apply_error_code <> ?") {
			t.Fatal("G scope lost V1 compatibility or V2 phase")
		}
	}
	if strings.Contains(noTaskSettlementConflict, "runtime") || strings.Contains(noTaskSettlementConflict, "digest") || strings.Contains(noTaskSettlementConflict, "applied_at") {
		t.Fatal("damaged marker can disappear before limit")
	}
}

func TestQuerySettlementBarrierRetainsDamagedMarker(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	if err := db.Exec("CREATE TABLE video_query_receipts (attempt_id TEXT PRIMARY KEY,org_id INTEGER,task_id TEXT,apply_error_code TEXT,runtime_version TEXT,observation_schema_version INTEGER,settlement_evidence_sha256 TEXT,recovery_due_at DATETIME)").Error; err != nil {
		t.Fatal(err)
	}
	_, st, r := querySettlementPending(t)
	r.RuntimeVersion = "unknown"
	r.ObservationSchemaVersion = 99
	r.ApplyErrorCode = queryFinancialConflictCode
	r.SettlementEvidenceSHA256 = "bad"
	r.RecoveryDueAt = nil
	if err := db.Table("video_query_receipts").Create(map[string]any{"attempt_id": r.AttemptID, "org_id": r.OrgID, "task_id": r.TaskID, "apply_error_code": r.ApplyErrorCode, "runtime_version": r.RuntimeVersion, "observation_schema_version": r.ObservationSchemaVersion, "settlement_evidence_sha256": r.SettlementEvidenceSHA256, "recovery_due_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	defer tx.Rollback()
	if err := requireNoTaskSettlementConflictTx(tx, st); err != errQueryFinancialConflict {
		t.Fatal("damaged closed marker vanished", err)
	}
	other := st
	other.OrgID++
	if err := requireNoTaskSettlementConflictTx(tx, other); err != nil {
		t.Fatal("marker crossed org", err)
	}
	if err := tx.Model(&QueryReceipt{}).Where("attempt_id = ?", r.AttemptID).Update("apply_error_code", "settlement_conflict").Error; err != nil {
		t.Fatal(err)
	}
	if err := requireNoTaskSettlementConflictTx(tx, st); err != nil {
		t.Fatal("old conflict became task barrier", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := requireNoTaskSettlementConflictTx(tx.WithContext(ctx), st); !errors.Is(err, context.Canceled) {
		t.Fatal("barrier read swallowed cancellation")
	}
}

func TestQueryPhaseCapturedRuntimeCannotChange(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	for _, sql := range []string{
		"CREATE TABLE video_task_states (org_id INTEGER,task_id TEXT,billing_snapshot_id INTEGER,runtime_version TEXT,adapter_version TEXT,protocol_contract_version TEXT,billing_schema_version TEXT)",
		"CREATE TABLE video_billing_snapshots (id INTEGER,org_id INTEGER,task_id TEXT,billing_schema_version TEXT)",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, st, _ := querySettlementFixture(t, 0)
	st.RuntimeVersion = videoruntime.RuntimeV1
	if err := db.Table("video_task_states").Create(map[string]any{"org_id": st.OrgID, "task_id": st.TaskID, "billing_snapshot_id": st.BillingSnapshotID, "runtime_version": st.RuntimeVersion, "adapter_version": st.AdapterVersion, "protocol_contract_version": st.ProtocolContractVersion, "billing_schema_version": st.BillingSchemaVersion}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("video_billing_snapshots").Create(map[string]any{"id": st.BillingSnapshotID, "org_id": st.OrgID, "task_id": st.TaskID, "billing_schema_version": st.BillingSchemaVersion}).Error; err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: db}
	if err := service.requireCurrentTaskRuntime(context.Background(), st); err != nil {
		t.Fatal("V1 positive", err)
	}
	// This is a guard-only fixture, not a fabricated V2 admission or HMAC.
	if err := db.Table("video_task_states").Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Update("runtime_version", videoruntime.RuntimeV2).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.requireCurrentTaskRuntime(context.Background(), st); err != videoruntime.ErrUnsupported {
		t.Fatal("captured V1 accepted current V2", err)
	}
	st.RuntimeVersion = videoruntime.RuntimeV2
	if err := service.requireCurrentTaskRuntime(context.Background(), st); err != nil {
		t.Fatal("V2 positive", err)
	}
	if err := db.Table("video_task_states").Where("org_id = ? AND task_id = ?", st.OrgID, st.TaskID).Update("runtime_version", videoruntime.RuntimeV1).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.requireCurrentTaskRuntime(context.Background(), st); err != videoruntime.ErrUnsupported {
		t.Fatal("captured V2 accepted current V1", err)
	}
}

func TestQuerySettlementConflictSurvivesCompatibilityDiagnostics(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_v1", true: "unknown_state_known_v2"}[unsupported], func(t *testing.T) {
			var output bytes.Buffer
			db := authorityClockUnitDB(t, &output)
			// 红线 5 禁用 AutoMigrate；列集与 task.Task/State/QueryReceipt 模型一一对应。
			for _, ddl := range []string{
				"CREATE TABLE tasks (id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT,org_id INTEGER,user_id INTEGER,api_key_id INTEGER,model TEXT,channel_id INTEGER,provider TEXT,status TEXT,fail_reason TEXT,upstream_task_id TEXT,result_url TEXT,result_ref TEXT,hold_id INTEGER,credential_id INTEGER,est_tokens INTEGER,actual_tokens INTEGER,cost_usd NUMERIC,price_out_usd_per_1m NUMERIC,price_per_call_usd NUMERIC,hold_amount_usd NUMERIC,group_ratio NUMERIC,submitted_at DATETIME,started_at DATETIME,finished_at DATETIME)",
				"CREATE TABLE video_task_states (task_id TEXT PRIMARY KEY,org_id INTEGER,create_request_id TEXT,provider_scope_id INTEGER,credential_snapshot_id INTEGER,provider_base_url TEXT,config_version TEXT,capability_version TEXT,runtime_version TEXT,adapter_version TEXT,protocol_contract_version TEXT,billing_schema_version TEXT,upstream_model TEXT,no_charge_statuses TEXT,billing_state TEXT,capture_history INTEGER,retention_days INTEGER,sync_state TEXT,artifact_status TEXT,effective_payload_ref TEXT,response_payload_ref TEXT,result_payload_ref TEXT,total_tokens INTEGER,observed_completion_tokens INTEGER,billing_snapshot_id INTEGER,idempotency_record_id INTEGER,parent_draft_task_id TEXT,row_version INTEGER,query_sequence INTEGER,applied_query_sequence INTEGER,query_lease_until DATETIME,event_sequence INTEGER,last_synced_at DATETIME,next_poll_at DATETIME,review_due_at DATETIME,deleted_at DATETIME,deletion_operation_id TEXT,created_at DATETIME,updated_at DATETIME,settlement_receipt_id TEXT)",
				"CREATE TABLE video_query_receipts (attempt_id TEXT PRIMARY KEY,org_id INTEGER,task_id TEXT,sequence INTEGER,task_row_version INTEGER,runtime_version TEXT,adapter_version TEXT,protocol_contract_version TEXT,observation_schema_version INTEGER,http_status INTEGER,outcome TEXT,provider_request_id TEXT,observation_json TEXT,has_video INTEGER,has_last_frame INTEGER,payload_ref TEXT,received_at DATETIME,applied_at DATETIME,apply_error_code TEXT,recovery_due_at DATETIME,recovery_version INTEGER,generation_applied_at DATETIME,settlement_kind TEXT,settlement_evidence_sha256 TEXT)",
			} {
				if err := db.Exec(ddl).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Exec("CREATE TABLE video_billing_snapshots (id INTEGER,org_id INTEGER,task_id TEXT,billing_schema_version TEXT)").Error; err != nil {
				t.Fatal(err)
			}
			row, st, r := querySettlementPending(t)
			if unsupported {
				st.RuntimeVersion = "unknown"
			} else {
				st.RuntimeVersion = videoruntime.RuntimeV1
				r.RuntimeVersion = videoruntime.RuntimeV1
			}
			r.ApplyErrorCode = queryFinancialConflictCode
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&st).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Table("video_billing_snapshots").Create(map[string]any{"id": st.BillingSnapshotID, "org_id": st.OrgID, "task_id": st.TaskID, "billing_schema_version": st.BillingSchemaVersion}).Error; err != nil {
				t.Fatal(err)
			}
			var before QueryReceipt
			if err := db.Where("attempt_id = ?", r.AttemptID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			service := &Service{DB: db}
			var result queryRecoveryResult
			var err error
			if unsupported {
				var token queryCompatibilityAttempt
				if e := db.Transaction(func(tx *gorm.DB) error { var e error; token, e = observeQueryCompatibilityTx(tx, row, st, r); return e }); e != nil {
					t.Fatal(e)
				}
				if !token.identity.valid {
					t.Fatal("compatibility token not captured")
				}
				result, err = service.recordQueryCompatibilityFailure(context.Background(), token, videoruntime.ErrUnsupported)
			} else {
				token, e := observeQueryRecovery(r)
				if e != nil {
					t.Fatal(e)
				}
				result, err = service.recordQueryRecoveryFailure(context.Background(), token, errors.New("temporary"))
			}
			if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryFailed {
				t.Fatal("closed financial marker was not explicitly rejected", err)
			}
			var after QueryReceipt
			if e := db.Where("attempt_id = ?", r.AttemptID).Take(&after).Error; e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("diagnostic changed closed conflict receipt")
			}
		})
	}
}
