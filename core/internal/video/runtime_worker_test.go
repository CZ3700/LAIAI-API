package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func runtimeWorkerCreateOperation(t *testing.T, f *fixture, id string) Operation {
	t.Helper()
	var op Operation
	must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, id, "create_task").First(&op).Error)
	return op
}

func assertRuntimeWorkerDenied(t *testing.T, f *fixture, id string, run func() error) {
	t.Helper()
	before := mutationRuntimeSnapshot(t, f, id)
	opBefore := runtimeWorkerCreateOperation(t, f, id)
	posts, gets := f.posts.Load(), f.gets.Load()
	err := run()
	opAfter := runtimeWorkerCreateOperation(t, f, id)
	if !errors.Is(err, videoruntime.ErrUnsupported) || f.posts.Load() != posts || f.gets.Load() != gets || mutationRuntimeSnapshot(t, f, id) != before || opAfter.AttemptID != opBefore.AttemptID || opAfter.Version != opBefore.Version || opAfter.ReceiptAppliedAt != nil {
		t.Fatal("unknown contract advanced a task, attempted HTTP, or released funds", err)
	}
}

func TestMySQLRuntimeWorkerRejectsUnknownBeforeClaimAndRefund(t *testing.T) {
	for _, target := range []struct{ table, column string }{
		{"state", "runtime_version"}, {"state", "adapter_version"}, {"state", "protocol_contract_version"}, {"state", "billing_schema_version"},
		{"operation", "runtime_version"}, {"operation", "adapter_version"}, {"operation", "protocol_contract_version"},
		{"snapshot", "billing_schema_version"},
	} {
		for _, value := range []string{"", "future-contract"} {
			t.Run(fmt.Sprintf("%s/%s/%q", target.table, target.column, value), func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "worker-contract-denied")
				captured := runtimeWorkerCreateOperation(t, f, v.ID)
				q := f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
				switch target.table {
				case "state":
					q = q.Model(&State{})
				case "snapshot":
					q = q.Model(&videobilling.Snapshot{})
				case "operation":
					q = f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ?", f.key.OrgID, v.ID)
				}
				must(t, q.Update(target.column, value).Error)
				// A candidate captured before a stored contract changed is refused,
				// as is a freshly read unsupported candidate.
				assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.processOperation(context.Background(), captured) })
				current := runtimeWorkerCreateOperation(t, f, v.ID)
				assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.processOperation(context.Background(), current) })
				var attempts int64
				must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&attempts).Error)
				if attempts != 0 {
					t.Fatal("unsupported claim wrote an attempt")
				}
				// Exercise the refund branch with a matching in-flight CAS, so the
				// absence of a refund is caused by the contract gate, not status.
				must(t, f.s.DB.Model(&Operation{}).Where("id = ? AND org_id = ?", captured.ID, f.key.OrgID).Updates(map[string]any{"status": "in_flight", "attempt_id": "att_" + v.ID}).Error)
				must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", "submitting").Error)
				var row task.Task
				var state State
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
				assertRuntimeWorkerDenied(t, f, v.ID, func() error {
					return f.s.knownUnsent(context.Background(), row, state, captured, "credential_unavailable")
				})
				if target.table != "operation" {
					must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Updates(map[string]any{"status": "queued", "upstream_task_id": "up_" + v.ID}).Error)
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("next_poll_at", f.now.Add(-time.Minute)).Error)
					assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.pollTask(context.Background(), f.key.OrgID, v.ID) })
				}
				if f.hold(t, v.ID).Status != ledger.HoldActive {
					t.Fatal("unknown contract released or captured the reservation")
				}
			})
		}
	}
}

func TestMySQLRuntimeCreateResponseSurvivesUnsupportedTask(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "runtime-response-survives")
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return nil, errors.New("unexpected provider method")
		}
		// The HTTP call has already started with v1. A later registry/task
		// incompatibility may stop applying its evidence but cannot erase it.
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
		return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+v.ID)), nil
	}
	op := runtimeWorkerCreateOperation(t, f, v.ID)
	if err := f.s.processOperation(context.Background(), op); !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("unsupported application was not distinguished from unsent", err)
	}
	op = runtimeWorkerCreateOperation(t, f, v.ID)
	var payload invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, op.ReceiptRef).First(&payload).Error)
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if op.ReceiptRef == "" || op.RuntimeVersion != videoruntime.RuntimeV1 || op.AdapterVersion != videoruntime.AdapterV1 || op.ProtocolContractVersion != videoruntime.ProtocolV1 || op.ReceiptAppliedAt != nil || op.Status != "in_flight" || row.Status != "submitting" || row.UpstreamTaskID != "" || f.hold(t, v.ID).Status != ledger.HoldActive || f.posts.Load() != 1 {
		t.Fatal("in-flight response was lost, reinterpreted, applied or refunded")
	}
	assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.applyCreateReceipt(context.Background(), op) })
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", videoruntime.RuntimeV1).Error)
	must(t, f.s.applyCreateReceipt(context.Background(), op))
	must(t, f.s.applyCreateReceipt(context.Background(), op))
	must(t, f.s.processOperation(context.Background(), op))
	op = runtimeWorkerCreateOperation(t, f, v.ID)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
	if op.Status != "confirmed" || op.ReceiptAppliedAt == nil || row.Status != "queued" || row.UpstreamTaskID != "up_"+v.ID || f.posts.Load() != 1 || f.gets.Load() != 0 || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("restoring v1 repeated HTTP or did not replay the original receipt")
	}
}

func TestMySQLRuntimeExecutionSchemaPrecedesKnownUnsentFailures(t *testing.T) {
	for _, schema := range []uint32{0, 2} {
		for _, failure := range []string{"none", "expired", "assets", "quota"} {
			t.Run(fmt.Sprintf("schema=%d/%s", schema, failure), func(t *testing.T) {
				f := mysqlFixture(t)
				v := f.create(t, "unknown-execution-schema")
				op := runtimeWorkerCreateOperation(t, f, v.ID)
				changes := map[string]any{"schema_version": schema}
				if failure == "expired" {
					changes["expires_at"] = f.now.Add(-time.Second)
				}
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", f.key.OrgID, op.PayloadRef).Updates(changes).Error)
				if failure == "assets" {
					name := fmt.Sprintf("test:unknown-execution-assets-%d", f.key.OrgID)
					must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
						if tx.Statement.Table == "video_task_asset_refs" {
							tx.AddError(errors.New("synthetic asset validation unavailable"))
						}
					}))
					t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
				}
				if failure == "quota" {
					owned := f.s.DB.Model(&videobilling.QuotaReservation{}).Select("bucket_id").Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID)
					must(t, f.s.DB.Model(&videobilling.QuotaBucket{}).Where("id IN (?)", owned).Update("blocked", true).Error)
				}
				assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.processOperation(context.Background(), op) })
				var attempts int64
				must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&attempts).Error)
				if attempts != 0 {
					t.Fatal("unknown execution schema was claimed before the failure branch")
				}
				must(t, f.s.DB.Model(&Operation{}).Where("id = ? AND org_id = ?", op.ID, f.key.OrgID).Updates(map[string]any{"status": "in_flight", "attempt_id": "att_" + v.ID}).Error)
				must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("status", "submitting").Error)
				var row task.Task
				var state State
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&row).Error)
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
				assertRuntimeWorkerDenied(t, f, v.ID, func() error { return f.s.knownUnsent(context.Background(), row, state, op, "execution_unavailable") })
			})
		}
	}
}

func TestMySQLRuntimeQueryResponseSurvivesUnsupportedTask(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "runtime-query-survives")
	f.dispatch(t, v.ID)
	upstream := f.upstream(t, v.ID)
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			return nil, errors.New("unexpected provider method")
		}
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
		return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100},"content":{"video_url":"https://cdn.example.com/runtime.mp4"}}`, upstream)), nil
	}
	if err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID); !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("query receipt did not stop at the contract application gate", err)
	}
	var receipt QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
	var payload invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, receipt.PayloadRef).First(&payload).Error)
	if receipt.PayloadRef == "" || receipt.RuntimeVersion != videoruntime.RuntimeV1 || receipt.AdapterVersion != videoruntime.AdapterV1 || receipt.ProtocolContractVersion != videoruntime.ProtocolV1 || receipt.ObservationSchemaVersion != videoruntime.ObservationSchemaV1 || receipt.AppliedAt != nil || receipt.ApplyErrorCode != "runtime_contract_unsupported" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("query producer contract/evidence was lost or funds changed")
	}
	// Query application checks State/Snapshot independently of the create op.
	before := mutationRuntimeSnapshot(t, f, v.ID)
	if err := f.s.applyQueryReceipt(context.Background(), receipt); !errors.Is(err, videoruntime.ErrUnsupported) || mutationRuntimeSnapshot(t, f, v.ID) != before {
		t.Fatal("unsupported saved query receipt advanced state", err)
	}
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", videoruntime.RuntimeV1).Error)
	must(t, f.s.applyQueryReceipt(context.Background(), receipt))
	must(t, f.s.applyQueryReceipt(context.Background(), receipt))
	must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, receipt.AttemptID).First(&receipt).Error)
	var entries, evidence, responses int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	must(t, f.s.DB.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&evidence).Error)
	must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND phase = ?", f.key.OrgID, v.ID, "query_response").Count(&responses).Error)
	hold := f.hold(t, v.ID)
	if receipt.AppliedAt == nil || entries != 1 || evidence != 1 || responses != 1 || f.posts.Load() != 1 || f.gets.Load() != 1 || hold.Status != ledger.HoldCaptured || hold.CapturedAmount.String() != "0.0008" {
		t.Fatal("saved query recovery repeated HTTP or settled more than once")
	}
}

func TestMySQLRuntimeCandidateFiltersPrecedeAllBatchLimits(t *testing.T) {
	f := mysqlFixture(t)
	ctx := context.Background()
	const unsupportedCount = 45 // Larger than all four production batch limits.
	var ids []string
	for i := 0; i <= unsupportedCount; i++ {
		f.now = f.now.Add(time.Millisecond)
		v := f.create(t, fmt.Sprintf("runtime-candidates-%d", i))
		ids = append(ids, v.ID)
		attempt := fmt.Sprintf("att_runtime_%d_%d", f.key.OrgID, i)
		r := QueryReceipt{OrgID: f.key.OrgID, TaskID: v.ID, AttemptID: attempt, Sequence: 1, RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, ObservationSchemaVersion: 1, Outcome: string(ark.OutcomeAccepted), ObservationJSON: `{}`, ReceivedAt: f.now}
		must(t, f.s.DB.Create(&r).Error)
	}
	validID := ids[len(ids)-1]
	invalidIDs := ids[:unsupportedCount]
	// Candidate fixtures are never executed. Leave synthetic unfinished rows
	// incompatible so later global worker tests cannot dispatch their payloads.
	t.Cleanup(func() {
		_ = f.s.DB.Model(&State{}).Where("org_id = ?", f.key.OrgID).Update("runtime_version", "candidate-fixture-only").Error
	})
	for _, blocked := range []string{"state", "snapshot", "operation", "receipt"} {
		t.Run(blocked, func(t *testing.T) {
			must(t, f.s.DB.Model(&State{}).Where("org_id = ?", f.key.OrgID).Updates(map[string]any{"runtime_version": videoruntime.RuntimeV1, "adapter_version": videoruntime.AdapterV1, "protocol_contract_version": videoruntime.ProtocolV1, "billing_schema_version": videoruntime.BillingV1, "next_poll_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"), "query_lease_until": nil}).Error)
			must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ?", f.key.OrgID).Update("billing_schema_version", videoruntime.BillingV1).Error)
			must(t, f.s.DB.Model(&Operation{}).Where("org_id = ?", f.key.OrgID).Updates(map[string]any{"runtime_version": videoruntime.RuntimeV1, "adapter_version": videoruntime.AdapterV1, "protocol_contract_version": videoruntime.ProtocolV1}).Error)
			must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ?", f.key.OrgID).Update("observation_schema_version", 1).Error)
			for i, id := range invalidIDs {
				value := "future-contract"
				if i%2 == 0 {
					value = ""
				}
				switch blocked {
				case "state":
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Update("runtime_version", value).Error)
				case "snapshot":
					must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Update("billing_schema_version", value).Error)
				case "operation":
					must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND resource_id = ?", f.key.OrgID, id).Update("adapter_version", value).Error)
				case "receipt":
					must(t, f.s.DB.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Update("observation_schema_version", i%2*2).Error)
				}
			}
			if blocked != "receipt" {
				must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ?", f.key.OrgID).Update("status", "accepted").Error)
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ?", f.key.OrgID).Updates(map[string]any{"status": "pending", "lease_until": nil}).Error)
				var operations []Operation
				candidates := f.s.pendingOperationCandidates(ctx).Where("o.org_id = ?", f.key.OrgID)
				must(t, f.s.DB.Table("(?) AS eligible", candidates).Where("scope_rank = 1").Order("created_at, id").Limit(20).Find(&operations).Error)
				if len(operations) != 1 || operations[0].ResourceID != validID {
					t.Fatal("unsupported operations consumed ROW_NUMBER or batch", len(operations))
				}
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ?", f.key.OrgID).Updates(map[string]any{"status": "in_flight", "lease_until": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")}).Error)
				operations = nil
				must(t, f.s.recoveryOperationCandidates(ctx).Where("o.org_id = ?", f.key.OrgID).Order("o.updated_at, o.id").Limit(30).Find(&operations).Error)
				if len(operations) != 1 || operations[0].ResourceID != validID {
					t.Fatal("unsupported operations filled recovery batch", len(operations))
				}
			}
			if blocked != "operation" {
				must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ?", f.key.OrgID).Updates(map[string]any{"status": "queued", "upstream_task_id": gorm.Expr("CONCAT('up_', task_id)")}).Error)
				if blocked != "receipt" {
					var states []State
					must(t, f.s.pendingQueryCandidates(ctx).Where("v.org_id = ?", f.key.OrgID).Order("v.next_poll_at, v.task_id").Limit(40).Scan(&states).Error)
					if len(states) != 1 || states[0].TaskID != validID {
						t.Fatal("unsupported states filled query batch", len(states))
					}
				}
				var receipts []QueryReceipt
				must(t, f.s.recoveryQueryCandidates(ctx).Where("r.org_id = ?", f.key.OrgID).Order("r.received_at, r.attempt_id").Limit(40).Find(&receipts).Error)
				if len(receipts) != 1 || receipts[0].TaskID != validID {
					t.Fatal("unsupported receipts filled recovery batch", len(receipts))
				}
			}
		})
	}
	if f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("candidate inspection unexpectedly contacted a provider")
	}
}
