package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A real GET is committed before the first consumer lock read is interrupted.
// This keeps receipt bytes/progress intact and exercises G with zero network.
func generationPendingHTTP(t *testing.T, f *fixture, id string, status ark.Status, tokens *int64) QueryReceipt {
	t.Helper()
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, st.QuerySequence, st.QueryLeaseUntil != nil)
	settlementRatesDue(t, f, id)
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/contents/generations/tasks/up_"+id {
			return nil, errors.New("unexpected synthetic generation request")
		}
		body := map[string]any{"id": "up_" + id, "status": status}
		if tokens != nil {
			body["usage"] = map[string]any{"completion_tokens": *tokens, "total_tokens": *tokens}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return reply(200, string(raw)), nil
	}
	interrupted := errors.New("synthetic generation consumer boundary")
	name, hits := "test:g-persisted-"+id, 0
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
		if q.Statement.Table == "video_query_receipts" && ok && lock.Strength == "UPDATE" {
			if _, ok := q.Statement.Dest.(*QueryReceipt); ok && hits == 0 {
				hits++
				q.AddError(interrupted)
			}
		}
	}))
	gets := f.gets.Load()
	err := f.s.pollTask(context.Background(), f.key.OrgID, id)
	must(t, f.s.DB.Callback().Query().Remove(name))
	if !errors.Is(err, interrupted) || hits != 1 || f.gets.Load() != gets+1 {
		t.Fatal("real GET did not stop at the committed receipt consumer boundary")
	}
	var row QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("sequence DESC").Take(&row).Error)
	if row.PayloadRef == "" || row.GenerationAppliedAt != nil || row.AppliedAt != nil || row.RecoveryVersion != 0 || row.RecoveryDueAt == nil || row.ObservationJSON == "" {
		t.Fatal("consumer fixture changed evidence or advanced the new receipt")
	}
	return row
}

// Includes hidden physical money projections; generation fields intentionally
// stay outside this oracle because G is required to update those fields.
func generationFinancialProjection(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var row task.Task
	var st State
	var usage []videobilling.UsageEvidence
	var logs []invocation.Invocation
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&usage).Error)
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&logs).Error)
	stored := []any{authorityFinancialFacts(t, f, id), row.ActualTokens, row.CostUSD, st.ObservedCompletionTokens, st.TotalTokens}
	for _, evidence := range usage {
		stored = append(stored, executionRecoveryStorageFields(evidence))
	}
	for _, invocation := range logs {
		stored = append(stored, executionRecoveryStorageFields(invocation))
	}
	raw, err := json.Marshal(stored)
	must(t, err)
	return string(raw)
}

func generationEvents(t *testing.T, f *fixture, id string) (int64, int64) {
	t.Helper()
	var events, outbox int64
	must(t, f.s.DB.Model(&Event{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Count(&events).Error)
	must(t, f.s.DB.Model(&Outbox{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Count(&outbox).Error)
	return events, outbox
}

func generationTaskStorage(t *testing.T, f *fixture, id string, retryBaseline ...State) string {
	t.Helper()
	var row task.Task
	var st State
	var events []Event
	var outbox []Outbox
	var ops []Operation
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&events).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("event_id").Find(&outbox).Error)
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ?", f.key.OrgID, id).Order("id").Find(&ops).Error)
	// Only the separate confirmed retry diagnostic may change these two fields.
	if len(retryBaseline) == 1 {
		st.SyncState, st.UpdatedAt = retryBaseline[0].SyncState, retryBaseline[0].UpdatedAt
	}
	values := []any{executionRecoveryStorageFields(row), executionRecoveryStorageFields(st)}
	for _, event := range events {
		values = append(values, executionRecoveryStorageFields(event))
	}
	for _, event := range outbox {
		values = append(values, executionRecoveryStorageFields(event))
	}
	for _, op := range ops {
		values = append(values, executionRecoveryStorageFields(op))
	}
	raw, err := json.Marshal(values)
	must(t, err)
	return string(raw)
}

func TestMySQLQueryGenerationPhaseAndZeroFinancialWrites(t *testing.T) {
	for _, name := range []string{"positive", "zero", "missing", "nocharge", "failed_usage"} {
		t.Run(name, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "generation-"+name)
			status, n := ark.StatusSucceeded, int64(100)
			usage, kind := &n, querySettlementUsage
			if name == "zero" {
				n = 0
			}
			if name == "missing" {
				usage, kind = nil, querySettlementNone
			}
			if name == "nocharge" {
				status, usage, kind = ark.StatusCancelled, nil, querySettlementProviderNoCharge
			}
			if name == "failed_usage" {
				status, kind = ark.StatusFailed, querySettlementNone
			}
			pending := generationPendingHTTP(t, f, v.ID, status, usage)
			before := generationFinancialProjection(t, f, v.ID)
			events, outbox := generationEvents(t, f, v.ID)
			settlementForbidNetwork(t, f)
			posts, gets := f.posts.Load(), f.gets.Load()
			result, err := f.s.applyQueryReceiptProgress(context.Background(), pending)
			must(t, err)
			got := settlementReadReceipt(t, f, pending)
			var row task.Task
			var st State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&row).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
			if result.disposition != queryRecoveryApplied || row.Status != string(status) || row.FinishedAt == nil || got.GenerationAppliedAt == nil || got.SettlementKind != kind || got.SettlementEvidenceSHA256 == "" || got.RecoveryVersion != 1 || st.AppliedQuerySequence != pending.Sequence || st.QueryLeaseUntil != nil {
				t.Fatal("G did not atomically publish generation and phase")
			}
			if name == "missing" || name == "failed_usage" {
				if got.AppliedAt == nil || st.SettlementReceiptID != nil || st.BillingState != "pending_usage" {
					t.Fatal("missing usage was mistaken for zero")
				}
			} else if got.AppliedAt != nil || st.SettlementReceiptID == nil || *st.SettlementReceiptID != got.AttemptID || st.BillingState != "reserved" {
				t.Fatal("G did not leave the first financial source for B")
			}
			if name == "failed_usage" && st.SyncState != "needs_review" {
				t.Fatal("unproved no-charge observation lost billing review")
			}
			e, o := generationEvents(t, f, v.ID)
			if e != events+1 || o != outbox+1 || before != generationFinancialProjection(t, f, v.ID) {
				t.Fatal("G changed financial projections or lost its single generation event")
			}
			stable := generationTaskStorage(t, f, v.ID)
			result, err = f.s.applyQueryReceiptProgress(context.Background(), pending)
			must(t, err)
			if result.disposition != queryRecoveryNoop || stable != generationTaskStorage(t, f, v.ID) || !reflect.DeepEqual(got, settlementReadReceipt(t, f, got)) || posts != f.posts.Load() || gets != f.gets.Load() {
				t.Fatal("G replay performed B or changed committed facts")
			}
		})
	}
}

func TestMySQLQueryGenerationConfirmsMatchingCancellation(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "generation-cancel")
	op, replay, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("generation-cancel"), "cancel-g")
	must(t, err)
	if replay || op.Status != "pending" {
		t.Fatal("cancel acceptance fixture failed")
	}
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
	ids := []string{op.ID}
	for _, status := range []string{"unknown", "in_flight", "confirmed"} {
		id, err := randomID("op_")
		must(t, err)
		row := Operation{ID: id, OrgID: f.key.OrgID, ResourceID: v.ID, Kind: "cancel_task", Status: status, Version: 1, CreatedAt: f.now, UpdatedAt: f.now}
		inheritOperationRuntime(st, &row)
		must(t, f.s.DB.Create(&row).Error)
		ids = append(ids, id)
	}
	other := op
	other.ID, err = randomID("op_")
	must(t, err)
	other.RuntimeVersion = videoruntime.RuntimeV1
	other.PollURL = ""
	must(t, f.s.DB.Create(&other).Error)
	must(t, f.s.DB.Where("id = ?", other.ID).Take(&other).Error)
	untouchedBefore := []Operation{other}
	// Existing resource/kind/status columns use PAD SPACE collation. Each
	// near-match must stay untouched; pending+two spaces is length 9, which
	// must not borrow the in_flight literal's allowed length.
	for _, pad := range []string{"resource", "kind", "pending_one", "pending_two", "unknown", "in_flight"} {
		id, err := randomID("op_")
		must(t, err)
		row := Operation{ID: id, OrgID: f.key.OrgID, ResourceID: v.ID, Kind: "cancel_task", Status: "pending", Version: 1, CreatedAt: f.now, UpdatedAt: f.now}
		inheritOperationRuntime(st, &row)
		switch pad {
		case "resource":
			row.ResourceID += " "
		case "kind":
			row.Kind += " "
		case "pending_one":
			row.Status += " "
		case "pending_two":
			row.Status += "  "
		case "unknown":
			row.Status = "unknown "
		case "in_flight":
			row.Status = "in_flight "
		}
		must(t, f.s.DB.Create(&row).Error)
		must(t, f.s.DB.Where("id = ?", row.ID).Take(&row).Error)
		untouchedBefore = append(untouchedBefore, row)
	}
	pending := generationPendingHTTP(t, f, v.ID, ark.StatusCancelled, nil)
	funds := generationFinancialProjection(t, f, v.ID)
	must(t, f.s.applyQueryReceipt(context.Background(), pending))
	var rows []Operation
	must(t, f.s.DB.Where("org_id = ? AND id IN ?", f.key.OrgID, ids).Find(&rows).Error)
	if len(rows) != len(ids) {
		t.Fatal("cancel operations missing")
	}
	for _, r := range rows {
		if r.Status != "confirmed" {
			t.Fatal("provider cancelled generation left matching cancel operation unfinished")
		}
	}
	for _, before := range untouchedBefore {
		var untouched Operation
		must(t, f.s.DB.Where("id = ?", before.ID).Take(&untouched).Error)
		if !reflect.DeepEqual(untouched, before) {
			t.Fatal("G confirmed another runtime or padded operation identity")
		}
	}
	if funds != generationFinancialProjection(t, f, v.ID) {
		t.Fatal("G cancellation confirmation changed money")
	}
}

func TestMySQLQueryGenerationTransactionRollback(t *testing.T) {
	for _, boundary := range []string{"state_cas", "event", "phase"} {
		t.Run(boundary, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "generation-rollback-"+boundary)
			n := int64(100)
			row := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
			must(t, f.s.ConfigureAssets(AssetIntegrationOptions{}))
			var beforeState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeState).Error)
			var scope ProviderScope
			must(t, f.s.DB.Where("id = ?", beforeState.ProviderScopeID).Take(&scope).Error)
			group, asset := assetTestSeed(t, f, scope, videoasset.KindVirtual)
			// Keep the real group/asset foreign keys. Asset admission itself is
			// covered separately; the real release UPDATE must roll back with G.
			binding := videoasset.TaskReference{OrgID: f.key.OrgID, TaskID: v.ID, AssetID: asset.ID, GroupID: group.ID, ContentIndex: 0, Role: "reference_image", AssetMetadataVersion: 1, AuthorizationVersion: 1, CreatedAt: f.now}
			must(t, f.s.DB.Create(&binding).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&binding).Error)
			before, financial := generationTaskStorage(t, f, v.ID), generationFinancialProjection(t, f, v.ID)
			name, hits := "test:g-rollback-"+v.ID, 0
			inject := func(q *gorm.DB) {
				if hits != 0 {
					return
				}
				updates, _ := q.Statement.Dest.(map[string]any)
				_, stateWrite := updates["row_version"]
				_, phaseWrite := updates["generation_applied_at"]
				if boundary == "state_cas" && q.Statement.Table == "video_task_states" && stateWrite {
					hits++
					q.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}}) // Real zero-row UPDATE.
				} else if (boundary == "event" && q.Statement.Table == "video_events") || (boundary == "phase" && q.Statement.Table == "video_query_receipts" && phaseWrite) {
					hits++
					q.AddError(q.Session(&gorm.Session{NewDB: true}).Exec("SELECT generation_missing_column FROM video_query_receipts LIMIT 1").Error)
				}
			}
			must(t, f.s.DB.Callback().Update().Before("gorm:update").Register(name, inject))
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, inject))
			result, err := f.s.applyQueryReceiptProgress(context.Background(), row)
			must(t, f.s.DB.Callback().Update().Remove(name))
			must(t, f.s.DB.Callback().Create().Remove(name))
			if hits != 1 || result.disposition != queryRecoveryDeferred {
				t.Fatal("G failure did not reach the intended transactional boundary")
			}
			if boundary == "state_cas" {
				if !errors.Is(err, ErrConflict) {
					t.Fatal("zero-row CAS was accepted as generation success")
				}
			} else {
				var sqlErr *drivermysql.MySQLError
				if !errors.As(err, &sqlErr) || sqlErr.Number != 1054 || strings.Contains(err.Error(), "generation_missing_column") {
					t.Fatal("real storage failure was not preserved safely")
				}
			}
			var afterState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterState).Error)
			if afterState.SyncState != "needs_review" || before != generationTaskStorage(t, f, v.ID, beforeState) || financial != generationFinancialProjection(t, f, v.ID) {
				t.Fatal("G failure committed a partial generation or finance change")
			}
			var bindingAfter videoasset.TaskReference
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&bindingAfter).Error)
			if !reflect.DeepEqual(binding, bindingAfter) {
				t.Fatal("G rollback left asset release committed")
			}
			got := settlementReadReceipt(t, f, row)
			copy := got
			copy.RecoveryDueAt, copy.RecoveryVersion, copy.ApplyErrorCode = row.RecoveryDueAt, row.RecoveryVersion, row.ApplyErrorCode
			if !reflect.DeepEqual(copy, row) || got.GenerationAppliedAt != nil || got.AppliedAt != nil || got.RecoveryVersion != 1 || got.ApplyErrorCode != "query_apply_failed" {
				t.Fatal("G rollback changed evidence or published an uncommitted phase")
			}
			must(t, f.s.applyQueryReceipt(context.Background(), got))
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&bindingAfter).Error)
			if bindingAfter.ReleasedAt == nil {
				t.Fatal("successful G omitted the real asset release")
			}
			if settlementReadReceipt(t, f, row).GenerationAppliedAt == nil {
				t.Fatal("G could not recover after the real storage failure")
			}
		})
	}
}

func TestMySQLQueryGenerationLateEvidenceAndPinReview(t *testing.T) {
	for _, mode := range []string{"late_usage", "deleted_initial", "compatible_pin", "review_missing", "review_same", "conflicting_usage", "conflicting_terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "generation-late-"+mode)
			n := int64(100)
			first := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
			var current QueryReceipt
			if mode == "late_usage" || mode == "deleted_initial" {
				// The higher real GET wins generation with omitted usage. Only then
				// is the lower sequence applied as the first financial evidence.
				second := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, nil)
				must(t, f.s.applyQueryReceipt(context.Background(), second))
				if mode == "deleted_initial" {
					settlementAdminInitial(t, f, v.ID, &n, "")
					op, _, err := f.s.Delete(context.Background(), f.key.OrgID, v.ID, "g-delete")
					must(t, err)
					f.response = func(req *http.Request) (*http.Response, error) {
						if req.Method != http.MethodDelete {
							return nil, errors.New("unexpected synthetic delete")
						}
						return reply(204, ""), nil
					}
					settlementRatesDue(t, f, v.ID)
					must(t, f.s.processMutation(context.Background(), op))
				}
				current = first
			} else {
				must(t, f.s.applyQueryReceipt(context.Background(), first))
				status, usage := ark.StatusSucceeded, &n
				if mode == "review_missing" || mode == "review_same" {
					// A durable review state is a scheduling fixture, not authority
					// to alter the original pin's immutable bytes or generated time.
					must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, first.AttemptID).Updates(map[string]any{"recovery_due_at": nil, "apply_error_code": "query_settlement_evidence_invalid", "recovery_version": 2}).Error)
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).UpdateColumn("sync_state", "needs_review").Error)
					if mode == "review_missing" {
						usage = nil
					}
				}
				if mode == "conflicting_usage" {
					n++
				}
				if mode == "conflicting_terminal" {
					status, usage = ark.StatusFailed, nil
				}
				current = generationPendingHTTP(t, f, v.ID, status, usage)
			}
			pinBefore := settlementReadReceipt(t, f, first)
			var beforeTask task.Task
			var beforeState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeTask).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeState).Error)
			funds := generationFinancialProjection(t, f, v.ID)
			events, outbox := generationEvents(t, f, v.ID)
			settlementForbidNetwork(t, f)
			result, err := f.s.applyQueryReceiptProgress(context.Background(), current)
			got := settlementReadReceipt(t, f, current)
			var afterTask task.Task
			var afterState State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterTask).Error)
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterState).Error)
			if strings.HasPrefix(mode, "conflicting_") {
				if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryReview || got.GenerationAppliedAt != nil || got.ApplyErrorCode != queryFinancialConflictCode || got.RecoveryDueAt != nil {
					t.Fatal("trusted financial contradiction did not stop before generation")
				}
				pin := settlementReadReceipt(t, f, first)
				if pin.ApplyErrorCode != queryFinancialConflictCode || pin.RecoveryDueAt != nil || pin.SettlementEvidenceSHA256 != pinBefore.SettlementEvidenceSHA256 || !reflect.DeepEqual(afterTask, beforeTask) || !reflect.DeepEqual(afterState, beforeState) {
					t.Fatal("financial conflict changed generation or first pin evidence")
				}
			} else {
				must(t, err)
				if result.disposition != queryRecoveryApplied || got.GenerationAppliedAt == nil || !reflect.DeepEqual(beforeTask, afterTask) {
					t.Fatal("late evidence regressed or resurrected generation")
				}
				if mode == "late_usage" || mode == "deleted_initial" {
					if got.AppliedAt != nil || got.SettlementKind != querySettlementUsage || afterState.SettlementReceiptID == nil || *afterState.SettlementReceiptID != first.AttemptID || afterState.AppliedQuerySequence != beforeState.AppliedQuerySequence {
						t.Fatal("stale/tombstone short circuit discarded financial evidence")
					}
					if mode == "deleted_initial" && (afterState.DeletedAt == nil || afterState.BillingState != "settled") {
						t.Fatal("late G erased tombstone or initial settlement")
					}
				} else if got.AppliedAt == nil || got.SettlementKind != querySettlementNone || !reflect.DeepEqual(pinBefore, settlementReadReceipt(t, f, first)) {
					t.Fatal("later observation replaced or reopened the first pin")
				}
				if (mode == "review_missing" || mode == "review_same") && (afterState.SyncState != "needs_review" || afterState.SettlementReceiptID == nil || *afterState.SettlementReceiptID != first.AttemptID) {
					t.Fatal("later usage cleared review or changed the original pin")
				}
			}
			e, o := generationEvents(t, f, v.ID)
			if e != events || o != outbox || funds != generationFinancialProjection(t, f, v.ID) {
				t.Fatal("late G appended generation events or financial writes")
			}
		})
	}
}

func TestMySQLQueryGenerationTaskBarrierSurvivesVersionDamage(t *testing.T) {
	for _, damage := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "corrupt_to_v1"}[damage], func(t *testing.T) {
			f := newSettlementFixture(t)
			v := submitSettlementV2(t, f, "generation-task-barrier")
			// Required no-pin chain: succeeded/missing -> cancelled contradiction
			// -> later succeeded/usage. Persist both later GETs before diagnosis.
			first := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, nil)
			must(t, f.s.applyQueryReceipt(context.Background(), first))
			conflicting := generationPendingHTTP(t, f, v.ID, ark.StatusCancelled, nil)
			n := int64(100)
			later := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
			funds := generationFinancialProjection(t, f, v.ID)
			result, err := f.s.applyQueryReceiptProgress(context.Background(), conflicting)
			if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryReview {
				t.Fatal("no-pin terminal contradiction did not create the durable task barrier")
			}
			if damage {
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).UpdateColumn("runtime_version", videoruntime.RuntimeV1).Error)
				must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, later.AttemptID).UpdateColumn("runtime_version", videoruntime.RuntimeV1).Error)
				must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND attempt_id = ?", f.key.OrgID, conflicting.AttemptID).UpdateColumn("runtime_version", "future-corrupted-marker").Error)
			}
			current := settlementReadReceipt(t, f, later)
			marker := settlementReadReceipt(t, f, conflicting)
			storage := generationTaskStorage(t, f, v.ID)
			settlementForbidNetwork(t, f)
			result, err = f.s.applyQueryReceiptProgress(context.Background(), later)
			if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryNoop {
				t.Fatal("later receipt bypassed the task barrier")
			}
			var g, b []QueryReceipt
			must(t, f.s.recoveryQueryCandidates(context.Background()).Limit(40).Find(&g).Error)
			must(t, f.s.recoverySettlementCandidates(context.Background()).Limit(40).Find(&b).Error)
			if len(g) != 0 || len(b) != 0 || !reflect.DeepEqual(current, settlementReadReceipt(t, f, later)) || !reflect.DeepEqual(marker, settlementReadReceipt(t, f, conflicting)) || storage != generationTaskStorage(t, f, v.ID) || funds != generationFinancialProjection(t, f, v.ID) {
				t.Fatal("barrier changed evidence/money or kept occupying the automatic batch")
			}
		})
	}
}

func TestMySQLQueryGenerationMixedCandidatesAndDefaultV1(t *testing.T) {
	f := newSettlementFixture(t)
	legacy := f.create(t, "generation-default-v1")
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			return nil, errors.New("unexpected legacy create request")
		}
		return reply(200, `{"id":"up_`+legacy.ID+`"}`), nil
	}
	settlementRatesDue(t, f, legacy.ID)
	f.dispatch(t, legacy.ID)
	n := int64(100)
	old := generationPendingHTTP(t, f, legacy.ID, ark.StatusSucceeded, &n)
	if old.RuntimeVersion != videoruntime.RuntimeV1 {
		t.Fatal("consumer implementation enabled V2 production Create")
	}
	newTask := submitSettlementV2(t, f, "generation-mixed-g")
	g := generationPendingHTTP(t, f, newTask.ID, ark.StatusSucceeded, &n)
	bTask := submitSettlementV2(t, f, "generation-mixed-b")
	b := settlementPoll(t, f, bTask.ID, ark.StatusSucceeded, &n)
	var gRows, bRows []QueryReceipt
	must(t, f.s.recoveryQueryCandidates(context.Background()).Order("r.recovery_due_at,r.received_at,r.attempt_id").Limit(40).Find(&gRows).Error)
	must(t, f.s.recoverySettlementCandidates(context.Background()).Limit(40).Find(&bRows).Error)
	if len(gRows) != 2 || len(bRows) != 1 || bRows[0].AttemptID != b.AttemptID {
		t.Fatal("G/B candidate populations overlap or omit a supported version")
	}
	seen := map[string]bool{}
	for _, r := range gRows {
		seen[r.AttemptID] = true
	}
	if !seen[old.AttemptID] || !seen[g.AttemptID] {
		t.Fatal("joint G batch does not include both legacy atomic and V2 generation")
	}
	settlementForbidNetwork(t, f)
	stats, err := f.s.recoverQueryReceiptsProgress(context.Background())
	must(t, err)
	if stats.Selected != 2 || stats.Applied != 2 || stats.Failed != 0 {
		t.Fatal("joint G recovery applied a B receipt or dropped a supported generation")
	}
	oldDone := settlementReadReceipt(t, f, old)
	newDone := settlementReadReceipt(t, f, g)
	if oldDone.AppliedAt == nil || oldDone.GenerationAppliedAt != nil || oldDone.SettlementKind != "" || newDone.AppliedAt != nil || newDone.GenerationAppliedAt == nil || !reflect.DeepEqual(b, settlementReadReceipt(t, f, b)) {
		t.Fatal("V1 lost atomic semantics or G consumed pending B")
	}
}

func TestMySQLQueryGenerationOldTokenCannotOverwriteSettlementPhaseOrReview(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "generation-old-token")
	n := int64(100)
	pending := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
	// Capture through the actual current Task -> State -> receipt lock order.
	// No token version/phase is rewritten to manufacture a race.
	capture := func() queryRecoveryAttempt {
		var token queryRecoveryAttempt
		must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
			taskRow, st, err := LockBillingTaskTx(tx, f.key.OrgID, v.ID)
			if err != nil {
				return err
			}
			var current QueryReceipt
			if err = locked(tx).Where("org_id = ? AND task_id = ? AND attempt_id = ?", f.key.OrgID, v.ID, pending.AttemptID).Take(&current).Error; err != nil {
				return err
			}
			token, err = observeQueryPhaseRecovery(taskRow, st, current)
			return err
		}))
		return token
	}
	oldG := capture()
	if !oldG.valid || oldG.phase != queryPhaseGenerationV2 || oldG.version != 0 {
		t.Fatal("old G token was not captured from the real pending receipt")
	}
	funds := generationFinancialProjection(t, f, v.ID)
	posts, gets := f.posts.Load(), f.gets.Load()
	settlementForbidNetwork(t, f)
	result, err := f.s.applyQueryReceiptProgress(context.Background(), pending)
	must(t, err)
	generated := settlementReadReceipt(t, f, pending)
	currentB := capture()
	if result.disposition != queryRecoveryApplied || generated.GenerationAppliedAt == nil || generated.AppliedAt != nil || generated.RecoveryDueAt == nil || generated.SettlementKind != querySettlementUsage || generated.RecoveryVersion != oldG.version+1 || !currentB.valid || currentB.phase != queryPhaseSettlementV2 || currentB.version != generated.RecoveryVersion || queryPhaseTokenMatches(oldG, currentB) {
		t.Fatal("actual G commit did not produce a distinct current B token")
	}
	stable := generationTaskStorage(t, f, v.ID)
	lateCause := errors.New("synthetic late G diagnostic")
	result, err = f.s.recordQueryRecoveryFailure(context.Background(), oldG, lateCause)
	must(t, err)
	if result.disposition != queryRecoveryNoop || !reflect.DeepEqual(generated, settlementReadReceipt(t, f, pending)) || stable != generationTaskStorage(t, f, v.ID) || funds != generationFinancialProjection(t, f, v.ID) || posts != f.posts.Load() || gets != f.gets.Load() {
		t.Fatal("old G diagnostic overwrote the committed B phase")
	}
	// Corrupt only the stored outer digest after real G commit. B must perform
	// its own typed integrity check and same-token diagnostic; this fixture does
	// not synthesize generation/phase/pin, or directly write NULL review.
	damagedDigest := strings.Repeat("0", 64)
	if generated.SettlementEvidenceSHA256 == damagedDigest {
		t.Fatal("digest corruption fixture unexpectedly equals the true digest")
	}
	write := f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND recovery_version = ? AND settlement_evidence_sha256 = ?", f.key.OrgID, v.ID, pending.AttemptID, generated.RecoveryVersion, generated.SettlementEvidenceSHA256).UpdateColumn("settlement_evidence_sha256", damagedDigest)
	must(t, write.Error)
	if write.RowsAffected != 1 {
		t.Fatal("digest fault did not change exactly the intended B receipt")
	}
	damaged := settlementReadReceipt(t, f, pending)
	restored := damaged
	restored.SettlementEvidenceSHA256 = generated.SettlementEvidenceSHA256
	if !reflect.DeepEqual(restored, generated) || stable != generationTaskStorage(t, f, v.ID) || funds != generationFinancialProjection(t, f, v.ID) {
		t.Fatal("digest fault changed another persisted field")
	}
	currentDamagedB := capture()
	if currentDamagedB.phase != queryPhaseSettlementV2 || currentDamagedB.version != currentB.version || queryPhaseTokenMatches(currentB, currentDamagedB) || queryPhaseTokenMatches(oldG, currentDamagedB) {
		t.Fatal("current B token did not retain the actual stored bad digest")
	}
	var beforeDiagnostic State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&beforeDiagnostic).Error)
	result, typedErr := f.s.applyQuerySettlement(context.Background(), damaged)
	if classifyQueryRecoveryCause(typedErr) != queryCauseSettlementEvidence || result.disposition != queryRecoveryReview || result.code != querySettlementEvidenceInvalidCode {
		t.Fatal("real B did not commit its same-evidence typed review")
	}
	reviewed := settlementReadReceipt(t, f, pending)
	normalized := reviewed
	normalized.RecoveryDueAt, normalized.RecoveryVersion, normalized.ApplyErrorCode = damaged.RecoveryDueAt, damaged.RecoveryVersion, damaged.ApplyErrorCode
	var afterDiagnostic State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&afterDiagnostic).Error)
	if reviewed.RecoveryDueAt != nil || reviewed.AppliedAt != nil || reviewed.RecoveryVersion != damaged.RecoveryVersion+1 || reviewed.ApplyErrorCode != querySettlementEvidenceInvalidCode || !reflect.DeepEqual(normalized, damaged) || afterDiagnostic.SyncState != "needs_review" || stable != generationTaskStorage(t, f, v.ID, beforeDiagnostic) || funds != generationFinancialProjection(t, f, v.ID) || posts != f.posts.Load() || gets != f.gets.Load() {
		t.Fatal("B diagnostic changed evidence, generation, money or upstream calls")
	}
	reviewStorage := generationTaskStorage(t, f, v.ID)
	for _, late := range []struct {
		token queryRecoveryAttempt
		cause error
	}{{oldG, lateCause}, {currentB, lateCause}, {currentDamagedB, typedErr}} {
		result, err = f.s.recordQueryRecoveryFailure(context.Background(), late.token, late.cause)
		must(t, err)
		if result.disposition != queryRecoveryNoop || !reflect.DeepEqual(reviewed, settlementReadReceipt(t, f, pending)) || reviewStorage != generationTaskStorage(t, f, v.ID) || funds != generationFinancialProjection(t, f, v.ID) || posts != f.posts.Load() || gets != f.gets.Load() {
			t.Fatal("late G or B token reopened the committed NULL review")
		}
	}
}

func TestMySQLQueryGenerationFinancialConflictRejectsLaterOriginalUsage(t *testing.T) {
	f := newSettlementFixture(t)
	v := submitSettlementV2(t, f, "generation-conflict-original")
	n := int64(100)
	first := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
	must(t, f.s.applyQueryReceipt(context.Background(), first))
	n = 101
	conflicting := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
	funds := generationFinancialProjection(t, f, v.ID)
	result, err := f.s.applyQueryReceiptProgress(context.Background(), conflicting)
	if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryReview {
		t.Fatal("different usage did not commit the task financial barrier")
	}
	pin, marker := settlementReadReceipt(t, f, first), settlementReadReceipt(t, f, conflicting)
	if pin.ApplyErrorCode != queryFinancialConflictCode || marker.ApplyErrorCode != queryFinancialConflictCode || pin.RecoveryDueAt != nil || marker.RecoveryDueAt != nil || pin.AppliedAt != nil || marker.AppliedAt != nil {
		t.Fatal("conflict did not preserve both pending receipts behind review")
	}
	var taskBefore task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&taskBefore).Error)
	// This third GET happens AFTER the durable conflict, unlike the separate
	// late-response fixture which stores its later receipt before diagnosis.
	posts, gets := f.posts.Load(), f.gets.Load()
	n = 100
	later := generationPendingHTTP(t, f, v.ID, ark.StatusSucceeded, &n)
	if f.posts.Load() != posts || f.gets.Load() != gets+1 || later.Sequence <= conflicting.Sequence || !reflect.DeepEqual(pin, settlementReadReceipt(t, f, first)) || !reflect.DeepEqual(marker, settlementReadReceipt(t, f, conflicting)) || funds != generationFinancialProjection(t, f, v.ID) {
		t.Fatal("post-conflict GET did not preserve the original pin and financial facts")
	}
	var taskAfter task.Task
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&taskAfter).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
	if !reflect.DeepEqual(taskBefore, taskAfter) || st.SettlementReceiptID == nil || *st.SettlementReceiptID != first.AttemptID {
		t.Fatal("post-conflict GET changed generation or the first pin")
	}
	storage := generationTaskStorage(t, f, v.ID)
	posts, gets = f.posts.Load(), f.gets.Load()
	settlementForbidNetwork(t, f)
	for i := 0; i < 2; i++ {
		result, err = f.s.applyQueryReceiptProgress(context.Background(), later)
		if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryNoop {
			t.Fatal("original usage reopened a closed task through G")
		}
	}
	for _, receipt := range []QueryReceipt{pin, later} {
		result, err = f.s.applyQuerySettlement(context.Background(), receipt)
		if !errors.Is(err, errQueryFinancialConflict) || result.disposition != queryRecoveryFailed {
			t.Fatal("original usage reopened a closed task through B")
		}
	}
	var g, b []QueryReceipt
	must(t, f.s.recoveryQueryCandidates(context.Background()).Limit(40).Find(&g).Error)
	must(t, f.s.recoverySettlementCandidates(context.Background()).Limit(40).Find(&b).Error)
	if len(g) != 0 || len(b) != 0 || !reflect.DeepEqual(pin, settlementReadReceipt(t, f, first)) || !reflect.DeepEqual(marker, settlementReadReceipt(t, f, conflicting)) || !reflect.DeepEqual(later, settlementReadReceipt(t, f, later)) || storage != generationTaskStorage(t, f, v.ID) || funds != generationFinancialProjection(t, f, v.ID) || posts != f.posts.Load() || gets != f.gets.Load() {
		t.Fatal("closed task changed evidence, money, scheduling or upstream calls")
	}
}
