package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
)

func TestExecutionRecoveryPermanentErrorTree(t *testing.T) {
	for _, err := range []error{invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoNotFound, invocation.ErrVideoAccessDenied, invocation.ErrVideoCorrupt, invocation.ErrVideoConflict, ErrConflict, videoruntime.ErrUnsupported, fmt.Errorf("wrapped: %w", invocation.ErrVideoCorrupt)} {
		if !permanentCreateExecutionIdentityError(err) {
			t.Fatal("known permanent identity denial not recognized")
		}
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, invocation.ErrVideoExpired, invocation.ErrVideoStorage, authorityClockFailure(invocation.ErrVideoCorrupt), errors.Join(invocation.ErrVideoCorrupt, context.Canceled), errors.Join(videoruntime.ErrUnsupported, errors.New("synthetic DB failure")), errors.Join(invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoExecutionAuthorityUnavailable)} {
		if permanentCreateExecutionIdentityError(err) {
			t.Fatal("ambiguous or temporary error became diagnostic authority")
		}
	}
}

// Storage assertions must not honor public DTO json:"-" tags. Keep exported
// persisted fields, including private task/operation/event identities, without
// printing this in-memory projection or recursively opening opaque value types.
func executionRecoveryStorageFields(row any) map[string]any {
	v := reflect.ValueOf(row)
	fields := make(map[string]any, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if field.IsExported() && field.Tag.Get("gorm") != "-" {
			fields[field.Name] = v.Field(i).Interface()
		}
	}
	return fields
}

// Full original evidence and task projections stay private in memory. Only the
// three explicitly authorized diagnostic fields are normalized for comparison.
func executionRecoveryFacts(t *testing.T, f *fixture, id string, normalized bool) string {
	t.Helper()
	var row task.Task
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&row).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	op := runtimeWorkerCreateOperation(t, f, id)
	if normalized {
		op.Status, op.ErrorCode, op.UpdatedAt = "", "", time.Time{}
	}
	var attempts []Attempt
	var events []Event
	var inputs []invocation.VideoPayload
	var staging []invocation.VideoPayloadStaging
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&attempts).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("id").Find(&events).Error)
	must(t, f.s.DB.Where("org_id = ? AND payload_id IN ?", f.key.OrgID, []string{op.PayloadRef, op.ReceiptRef, st.EffectivePayloadRef, st.ResponsePayloadRef, st.ResultPayloadRef}).Order("payload_id").Find(&inputs).Error)
	ids := make([]string, 0, len(inputs))
	for _, p := range inputs {
		ids = append(ids, p.PayloadID)
	}
	if len(ids) != 0 {
		must(t, f.s.DB.Where("payload_id IN ?", ids).Order("payload_id").Find(&staging).Error)
	}
	storedEvents := make([]map[string]any, 0, len(events))
	for _, event := range events {
		storedEvents = append(storedEvents, executionRecoveryStorageFields(event))
	}
	raw, err := json.Marshal([]any{executionRecoveryStorageFields(row), st, executionRecoveryStorageFields(op), attempts, storedEvents, inputs, staging, authorityFinancialFacts(t, f, id)})
	must(t, err)
	return string(raw)
}

// Real processOperation claims, reads its accepted input, then encounters the
// corrupted identity at its final send guard. No invented status/claim/EA.
func executionRecoveryClaim(t *testing.T, f *fixture, id string) Operation {
	t.Helper()
	op := runtimeWorkerCreateOperation(t, f, id)
	var hit atomic.Bool
	name := fmt.Sprintf("execution-recovery-claim-%s", op.ID)
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table != "video_credential_snapshots" || !hit.CompareAndSwap(false, true) {
			return
		}
		q.AddError(q.Session(&gorm.Session{NewDB: true}).Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 99).Error)
	}))
	err := f.s.processOperation(context.Background(), op)
	must(t, f.s.DB.Callback().Query().Remove(name))
	if !hit.Load() || !permanentCreateExecutionIdentityError(err) {
		t.Fatal("real claim did not reach the final permanent identity fault", err)
	}
	op = runtimeWorkerCreateOperation(t, f, id)
	if op.Status != "in_flight" || op.AttemptID == "" || op.LeaseUntil == nil || op.ReceiptRef != "" || f.posts.Load() != 0 {
		t.Fatal("claim fixture did not retain actual unsent uncertainty")
	}
	return op
}

func TestMySQLExecutionRecoveryIsolationBoundaries(t *testing.T) {
	for _, mode := range []string{"schema", "correlation", "missing", "reference", "purpose", "live_claim", "expired_claim", "future_state", "future_operation", "future_billing", "future_registration", "stale_candidate", "padded_candidate", "repaired", "cancelled", "joined_database", "clock_sql", "metadata_sql"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "recovery-boundary")
			op := runtimeWorkerCreateOperation(t, f, v.ID)
			if mode == "future_registration" {
				a, registered := responseInboxRegister(t, f, v.ID)
				op = registered
				must(t, f.s.DB.Model(&ExecutionAttempt{}).Where("org_id = ? AND attempt_id = ?", a.OrgID, a.AttemptID).Update("storage_protocol", "future-inbox").Error)
			}
			if mode == "live_claim" || mode == "expired_claim" {
				op = executionRecoveryClaim(t, f, v.ID)
				if mode == "expired_claim" {
					op = expireFixtureOperationLease(t, f.s.DB, op)
				}
			} else if mode != "repaired" {
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 99).Error)
			}
			switch mode {
			case "correlation", "purpose":
				invalidCorrelation := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("invalid-correlation:%d:%s", op.OrgID, op.PayloadRef))))
				changes := map[string]any{"schema_version": 1, "correlation_key": invalidCorrelation}
				if mode == "purpose" {
					changes = map[string]any{"schema_version": 1, "purpose": invocation.VideoPurposeHistory}
				}
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Updates(changes).Error)
			case "missing":
				must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Delete(&invocation.VideoPayload{}).Error)
			case "reference":
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Update("payload_ref", "payload_wrong_reference").Error)
				op = runtimeWorkerCreateOperation(t, f, v.ID)
			case "future_state":
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", op.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
			case "future_operation":
				must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", op.OrgID, op.ID).Update("adapter_version", "future-adapter").Error)
				op = runtimeWorkerCreateOperation(t, f, v.ID)
			case "future_billing":
				must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ? AND task_id = ?", op.OrgID, v.ID).Update("billing_schema_version", "future-billing").Error)
			case "stale_candidate":
				op.Version++ // Only the stale input changes; storage stays authentic.
			case "padded_candidate":
				op.ID += " " // Legacy PAD SPACE lookup is not exact authority.
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := error(invocation.ErrVideoSchemaUnsupported)
			if mode == "cancelled" {
				cancel()
			}
			if mode == "joined_database" {
				cause = errors.Join(cause, errors.New("synthetic DB error"))
			}
			var hit atomic.Int32
			name := fmt.Sprintf("recovery-sql-%d", f.key.OrgID)
			if mode == "clock_sql" {
				must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, func(q *gorm.DB) {
					if strings.Contains(q.Statement.SQL.String(), "SELECT UTC_TIMESTAMP(3) AS database_time") {
						hit.Add(1)
						q.Statement.SQL.Reset()
						q.Statement.SQL.WriteString("SELECT execution_recovery_missing_clock AS database_time")
					}
				}))
				defer f.s.DB.Callback().Row().Remove(name)
			}
			// Record projections before registering the metadata error callback.
			before := executionRecoveryFacts(t, f, v.ID, true)
			original := runtimeWorkerCreateOperation(t, f, v.ID)
			if mode == "metadata_sql" {
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
					if q.Statement.Table == "invocation_payloads" {
						q.Statement.SQL.Reset()
						q.Statement.SQL.WriteString("SELECT execution_recovery_missing_metadata")
						q.Statement.Vars = nil
						hit.Add(1)
					}
				}))
			}
			isolated, err := f.s.isolateInvalidCreateExecution(ctx, op, cause)
			if mode == "metadata_sql" {
				must(t, f.s.DB.Callback().Query().Remove(name))
			}
			want := mode == "schema" || mode == "correlation" || mode == "missing" || mode == "reference" || mode == "purpose" || mode == "expired_claim"
			if isolated != want || want && err != nil {
				t.Fatal("isolation did not respect its exact supported boundary", mode, isolated, err)
			}
			if mode == "clock_sql" || mode == "metadata_sql" {
				if hit.Load() != 1 || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "execution_recovery_missing") {
					t.Fatal("actual driver failure did not remain unavailable", hit.Load(), err)
				}
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
			if executionRecoveryFacts(t, f, v.ID, true) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("diagnostic changed original evidence, state or money")
			}
			after := runtimeWorkerCreateOperation(t, f, v.ID)
			if want {
				if after.Status != "needs_review" || after.ErrorCode != executionIdentityReviewCode {
					t.Fatal("missing fixed operation diagnostic")
				}
				again, err := f.s.isolateInvalidCreateExecution(context.Background(), op, cause)
				must(t, err)
				if again || !reflect.DeepEqual(after, runtimeWorkerCreateOperation(t, f, v.ID)) {
					t.Fatal("stale retry rewrote isolated operation")
				}
			} else if !reflect.DeepEqual(original, after) {
				t.Fatal("noneligible record changed")
			}
		})
	}
}

// Validate the complete returned cause tree, not merely errors.Is on one leaf:
// unrelated storage, authority or cancellation failures must fail the fixture.
// Only the coordinator's private aggregate may contain the expected sentinel.
func requireRecoveryCause(t *testing.T, err, want error, count int, injectedStorageCause ...error) {
	t.Helper()
	pending := []error{err}
	leaves, visited := 0, 0
	for len(pending) != 0 {
		current := pending[0]
		pending = pending[1:]
		visited++
		if visited > 1024 || current == nil {
			t.Fatal("invalid or unbounded recovery cause tree")
		}
		if len(injectedStorageCause) == 0 && current == want {
			leaves++
			continue
		}
		// Invocation preserves the exact injected DB cause in its safe storage
		// wrapper. Accept only that single known cause, never an arbitrary
		// wrapper/join that happens to match the storage sentinel.
		if len(injectedStorageCause) == 1 && want == invocation.ErrVideoStorage && errors.Unwrap(current) == injectedStorageCause[0] && errors.Is(current, want) {
			leaves++
			continue
		}
		group, ok := current.(*persistedRecoveryErrors)
		if !ok || group == nil || len(group.causes) == 0 {
			t.Fatalf("unexpected recovery cause type %T", current)
		}
		pending = append(pending, group.causes...)
	}
	if leaves != count {
		t.Fatal("recovery cause count differs from the injected records", "got", leaves, "want", count)
	}
}

// Only bounded candidate projections are scoped. In particular a sender
// COUNT, the locked Task/State/Operation rows and authorization reads retain
// their exact original predicates. This fixture does not recover the whole DB.
func executionRecoveryCandidateScope(q *gorm.DB, orgs []uint64) {
	if _, locking := q.Statement.Clauses["FOR"]; locking {
		return
	}
	projection := strings.Join(q.Statement.Selects, ",")
	alias := q.Statement.Table
	candidate := projection == alias+".*"
	if alias == "o" && strings.HasPrefix(projection, "o.*, ROW_NUMBER() OVER (") {
		candidate = true
	}
	if alias == "eligible" && len(q.Statement.Selects) == 0 && q.Statement.TableExpr != nil && q.Statement.TableExpr.SQL == "(?) AS eligible" {
		candidate = true
	}
	switch alias {
	case "o", "v", "ea", "ap", "r", "eligible":
		if candidate {
			q.Where(alias+".org_id IN ?", orgs)
		}
	}
}

// Dry SQL construction checks this test-only hook; it does not claim SQLite
// implements MySQL clocks or locking. Actual MySQL tests below use the real DB.
func TestExecutionRecoveryScopeOnlyChangesCandidateTenantPredicate(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitMySQLName(authorityClockUnitDB(t, &output)).Session(&gorm.Session{DryRun: true})
	s := &Service{DB: db}
	ctx := context.Background()
	orgs := []uint64{401, 402}
	tests := []struct {
		name, alias string
		candidate   bool
		query       func() *gorm.DB
	}{
		{"pending", "o", true, func() *gorm.DB { return s.pendingOperationCandidates(ctx).Order("o.created_at, o.id").Limit(20) }},
		{"legacy", "o", true, func() *gorm.DB { return s.recoveryOperationCandidates(ctx).Order("o.updated_at, o.id").Limit(30) }},
		{"create_receipts", "ap", true, func() *gorm.DB {
			return s.responseInboxCandidates(ctx).Order("ap.next_attempt_at, ap.receipt_id").Limit(30)
		}},
		{"mutation_receipts", "ap", true, func() *gorm.DB {
			return s.mutationResponseInboxCandidates(ctx).Order("ap.next_attempt_at, ap.receipt_id").Limit(30)
		}},
		{"query_receipts", "r", true, func() *gorm.DB { return s.recoveryQueryCandidates(ctx).Order("r.recovery_due_at").Limit(40) }},
		{"query_dispatch", "v", true, func() *gorm.DB { return s.pendingQueryCandidates(ctx).Order("v.next_poll_at").Limit(40) }},
		{"ranked_dispatch", "eligible", true, func() *gorm.DB {
			return db.Table("(?) AS eligible", s.pendingOperationCandidates(ctx)).Where("scope_rank = 1").Order("created_at, id").Limit(20)
		}},
		{"sender_count", "o", false, func() *gorm.DB {
			return db.Table("video_operations AS o").Select("COUNT(*)").Where("o.id = ? AND o.version = ?", "claimed", 7)
		}},
		{"operation_lock", "o", false, func() *gorm.DB {
			return db.Table("video_operations AS o").Select("o.*").Clauses(clause.Locking{Strength: "UPDATE"}).Where("o.id = ?", "claimed")
		}},
		{"task_lock", "tasks", false, func() *gorm.DB {
			return db.Table("tasks").Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND task_id = ?", 999, "exact-task")
		}},
		{"credential_auth", "video_credential_snapshots", false, func() *gorm.DB { return db.Table("video_credential_snapshots").Where("org_id = ? AND id = ?", 999, 42) }},
	}
	build := func(q *gorm.DB) (string, []any) {
		q.Statement.Dest = &[]Operation{}
		q.Statement.BuildClauses = q.Callback().Query().Clauses
		callbacks.BuildQuerySQL(q)
		must(t, q.Error)
		if q.Statement.SQL.Len() == 0 {
			t.Fatal("SQL projection check did not build a statement")
		}
		return q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plainSQL, plainVars := build(tc.query())
			filtered := tc.query()
			executionRecoveryCandidateScope(filtered, orgs)
			gotSQL, gotVars := build(filtered)
			want := tc.query()
			if tc.candidate {
				want = want.Where(tc.alias+".org_id IN ?", orgs)
			}
			wantSQL, wantVars := build(want)
			if gotSQL != wantSQL || !reflect.DeepEqual(gotVars, wantVars) {
				t.Fatal("fixture scope changed SQL/Vars beyond its tenant predicate")
			}
			if tc.candidate == (plainSQL == gotSQL && reflect.DeepEqual(plainVars, gotVars)) {
				t.Fatal("candidate scope failed to change only intended statements")
			}
		})
	}
}

func executionRecoveryScope(t *testing.T, f *fixture, orgs []uint64) {
	t.Helper()
	name := fmt.Sprintf("recovery-scope-%d", f.key.OrgID)
	filter := func(q *gorm.DB) { executionRecoveryCandidateScope(q, orgs) }
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, filter))
	must(t, f.s.DB.Callback().Row().Before("gorm:row").Register(name, filter))
	t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name); _ = f.s.DB.Callback().Row().Remove(name) })
}

func TestMySQLExecutionRecoveryBadQueueDoesNotStarveSuccessor(t *testing.T) {
	for _, phase := range []string{"pending", "expired"} {
		t.Run(phase, func(t *testing.T) {
			f := mysqlFixture(t)
			var bad []Operation
			for i := 0; i < 31; i++ {
				f.now = f.now.Add(time.Millisecond)
				v := f.create(t, fmt.Sprintf("bad-input-%d", i))
				op := runtimeWorkerCreateOperation(t, f, v.ID)
				if phase == "expired" {
					op = executionRecoveryClaim(t, f, v.ID)
					op = expireFixtureOperationLease(t, f.s.DB, op)
					var st State
					must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&st).Error)
					makeFixtureProviderRateDue(t, f.s.DB, st.ProviderScopeID, "create")
				} else {
					must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 99).Error)
				}
				bad = append(bad, op)
			}
			f.now = f.now.Add(time.Millisecond)
			good := f.create(t, "same-scope-successor")
			other := mysqlFixture(t)
			cross := other.create(t, "cross-org-successor")
			executionRecoveryScope(t, f, []uint64{f.key.OrgID, other.key.OrgID})
			var allBefore []Operation
			if phase == "pending" {
				must(t, f.s.pendingOperationCandidates(context.Background()).Find(&allBefore).Error)
				if len(allBefore) != 33 {
					t.Fatal("unlimited actual pending selector did not contain 31 bad and two successors")
				}
			} else {
				must(t, f.s.recoveryOperationCandidates(context.Background()).Find(&allBefore).Error)
				if len(allBefore) != 31 {
					t.Fatal("unlimited actual recovery selector did not expose the whole bad prefix")
				}
			}
			// These are the actual independent recovery and dispatch entrypoints,
			// restricted to these two fixture tenants,
			// not a replacement selector: rank/order/runtime/LIMIT are unchanged.
			f.response = func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					return reply(200, fmt.Sprintf(`{"id":"up_recovery_%d"}`, f.posts.Load())), nil
				}
				return reply(200, fmt.Sprintf(`{"id":%q,"status":"running"}`, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])), nil
			}
			before := make(map[string]string)
			for _, op := range bad {
				before[op.ID] = executionRecoveryFacts(t, f, op.ResourceID, true)
			}
			funds := authorityFinancialFacts(t, f, good.ID)
			otherFunds := authorityFinancialFacts(t, other, cross.ID)
			cycles, reviewed := 0, 0
			for cycle := 0; cycle < 34; cycle++ {
				previousReviewed := reviewed
				recoveryErr := f.s.ProcessPersistedEvidenceOnce(context.Background())
				if phase == "expired" && previousReviewed < len(bad) {
					// The runtime wrapper reports the unknown input schema using
					// this exact sentinel. Every one of this batch's causes must
					// be that denial; no DB/context/authority error is accepted.
					wantCauses := min(30, len(bad)-previousReviewed)
					requireRecoveryCause(t, recoveryErr, videoruntime.ErrUnsupported, wantCauses)
				} else {
					must(t, recoveryErr)
				}
				must(t, f.s.ProcessDispatchOnce(context.Background()))
				cycles, reviewed = cycle+1, 0
				for _, op := range bad {
					got := runtimeWorkerCreateOperation(t, f, op.ResourceID)
					if got.Status == "needs_review" && got.ErrorCode == executionIdentityReviewCode {
						reviewed++
					}
				}
				t.Log("independent recovery/dispatch cycle", "cycle", cycles, "new_review", reviewed-previousReviewed, "remaining", len(bad)-reviewed)
				if phase == "expired" && reviewed-previousReviewed > 30 {
					t.Fatal("recovery exceeded its real bounded batch")
				}
				// recoverOperations has a real LIMIT 30. Successful dispatch in
				// that same cycle cannot prove the 31st recovery was processed.
				if phase == "expired" && cycle == 0 && reviewed != 30 {
					t.Fatal("first bounded recovery batch did not leave one successor", "reviewed", reviewed, "remaining", len(bad)-reviewed)
				}
				if runtimeWorkerCreateOperation(t, f, good.ID).Status == "confirmed" && runtimeWorkerCreateOperation(t, other, cross.ID).Status == "confirmed" && reviewed == len(bad) {
					break
				}
			}
			if phase == "expired" && cycles < 2 {
				t.Fatal("expired queue did not cross the real recovery batch boundary", "cycles", cycles)
			}
			t.Log("bounded recovery progress", "cycles", cycles, "reviewed", reviewed, "remaining", len(bad)-reviewed)
			if runtimeWorkerCreateOperation(t, f, good.ID).Status != "confirmed" || runtimeWorkerCreateOperation(t, other, cross.ID).Status != "confirmed" || f.posts.Load() != 2 {
				t.Fatal("bounded real worker cycles did not reach both valid successors")
			}
			for _, op := range bad {
				got := runtimeWorkerCreateOperation(t, f, op.ResourceID)
				if got.Status != "needs_review" || got.ErrorCode != executionIdentityReviewCode {
					t.Fatal("bad candidate stayed in its automatic queue")
				}
				// Other successful tasks add only their own evidence/events. Every
				// bad task's immutable data and original funds remain unchanged.
				original := op
				got.Status, got.ErrorCode, got.UpdatedAt = original.Status, original.ErrorCode, original.UpdatedAt
				if !reflect.DeepEqual(got, original) || f.hold(t, op.ResourceID).Status != ledger.HoldActive {
					t.Fatal("bad operation isolation changed evidence identity or hold")
				}
				if executionRecoveryFacts(t, f, op.ResourceID, true) != before[op.ID] {
					t.Fatal("bad candidate changed task/state, evidence or financial facts")
				}
			}
			if authorityFinancialFacts(t, f, good.ID) != funds || authorityFinancialFacts(t, other, cross.ID) != otherFunds {
				t.Fatal("queue progress changed retained holds/quota/key/ledger")
			}
			var pendingAfter, recoveryAfter []Operation
			// No extra LIMIT can conceal a surviving bad candidate here.
			must(t, f.s.pendingOperationCandidates(context.Background()).Find(&pendingAfter).Error)
			must(t, f.s.recoveryOperationCandidates(context.Background()).Find(&recoveryAfter).Error)
			if len(pendingAfter) != 0 || len(recoveryAfter) != 0 {
				t.Fatal("isolated rows remained selectable after bounded worker progress")
			}
		})
	}
}

func TestMySQLExecutionRecoveryCurrentMetadataRepair(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "recovery-current-read")
	op := runtimeWorkerCreateOperation(t, f, v.ID)
	must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 99).Error)
	var repaired, share, oldSnapshot atomic.Bool
	name := fmt.Sprintf("recovery-current-%d", f.key.OrgID)
	must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "video_billing_snapshots" && repaired.CompareAndSwap(false, true) {
			// The first ordinary SELECT has established A's RR snapshot. B
			// commits the original valid metadata before A's SHARE read.
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			err := f.s.DB.Session(&gorm.Session{NewDB: true, Context: ctx}).Transaction(func(writer *gorm.DB) error {
				return writer.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 1).Error
			})
			q.AddError(err)
			if err == nil {
				var stale struct{ SchemaVersion uint32 }
				q.AddError(q.Session(&gorm.Session{NewDB: true}).Model(&invocation.VideoPayload{}).Select("schema_version").Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Take(&stale).Error)
				oldSnapshot.Store(stale.SchemaVersion == 99)
			}
		}
		if q.Statement.Table == "invocation_payloads" {
			lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
			share.Store(ok && lock.Strength == "SHARE")
		}
	}))
	isolated, err := f.s.isolateInvalidCreateExecution(context.Background(), op, invocation.ErrVideoSchemaUnsupported)
	must(t, f.s.DB.Callback().Query().Remove(name))
	must(t, err)
	if isolated || !repaired.Load() || !oldSnapshot.Load() || !share.Load() || !reflect.DeepEqual(op, runtimeWorkerCreateOperation(t, f, v.ID)) {
		t.Fatal("stale RR metadata caused isolation after committed repair")
	}
}

func TestMySQLExecutionRecoveryLateReceiptRemainsRecoverable(t *testing.T) {
	for _, outcome := range []ark.Outcome{ark.OutcomeAccepted, ark.OutcomeRejected, ark.OutcomeUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "recovery-real-late-response")
			op := runtimeWorkerCreateOperation(t, f, v.ID)
			executionRecoveryScope(t, f, []uint64{f.key.OrgID})
			var armed, hit atomic.Bool
			injected := errors.New("test-only fail apply after actual receipt commit")
			name := fmt.Sprintf("recovery-late-apply-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(q *gorm.DB) {
				if q.Statement.Table == "video_events" && armed.Load() && hit.CompareAndSwap(false, true) {
					q.AddError(injected)
				}
			}))
			defer f.s.DB.Callback().Create().Remove(name)
			f.response = func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					return nil, errors.New("unexpected external query")
				}
				claimed := runtimeWorkerCreateOperation(t, f, v.ID)
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", claimed.OrgID, claimed.PayloadRef).Update("schema_version", 99).Error)
				// The HTTP has really entered the transport. This fixture changes
				// only its precise existing lease; it is not a production renewal.
				claimed = expireFixtureOperationLease(t, f.s.DB, claimed)
				before := executionRecoveryFacts(t, f, v.ID, true)
				isolated, err := f.s.isolateInvalidCreateExecution(context.Background(), claimed, invocation.ErrVideoSchemaUnsupported)
				must(t, err)
				if !isolated || executionRecoveryFacts(t, f, v.ID, true) != before {
					t.Error("late sender diagnostic changed original task or financial facts")
					return nil, errors.New("isolation precondition failed")
				}
				armed.Store(true)
				if outcome == ark.OutcomeRejected {
					return reply(400, `{"error":{"code":"InvalidParameter"}}`), nil
				}
				if outcome == ark.OutcomeUnknown {
					return reply(502, `{}`), nil
				}
				return reply(200, fmt.Sprintf(`{"id":%q}`, "up_"+v.ID)), nil
			}
			err := f.s.processOperation(context.Background(), op)
			must(t, f.s.DB.Callback().Create().Remove(name))
			if !errors.Is(err, injected) || !hit.Load() || f.posts.Load() != 1 {
				t.Fatal("actual response did not commit before apply failure", err)
			}
			stored := runtimeWorkerCreateOperation(t, f, v.ID)
			if stored.Status != "needs_review" || stored.ErrorCode != executionIdentityReviewCode || stored.ReceiptRef == "" || stored.ReceiptAppliedAt != nil || stored.ReceivedOutcome != string(outcome) {
				t.Fatal("late response was lost or applied before recovery")
			}
			if responseInboxCount(t, f, &Attempt{}, "org_id = ? AND operation_id = ? AND phase = ? AND evidence_ref = ?", f.key.OrgID, op.ID, "response_received", stored.ReceiptRef) != 1 || f.hold(t, v.ID).Status != ledger.HoldActive {
				t.Fatal("late response evidence was not durable with its original hold")
			}
			restarted, err := NewService(f.s.DB, f.s.Keyring, f.s.Secret, f.s.Log)
			must(t, err)
			restarted.Client = f.s.Client
			must(t, restarted.recoverOperations(context.Background()))
			after := runtimeWorkerCreateOperation(t, f, v.ID)
			if after.ReceiptAppliedAt == nil || after.ReceiptRef != stored.ReceiptRef || after.AttemptID != stored.AttemptID || after.Version != stored.Version {
				t.Fatal("fresh service did not apply the original late evidence")
			}
			wantStatus, wantHold := "confirmed", ledger.HoldActive
			if outcome == ark.OutcomeUnknown {
				wantStatus = "unknown"
			} else if outcome == ark.OutcomeRejected {
				wantStatus, wantHold = "rejected", ledger.HoldReleased
			}
			if after.Status != wantStatus || f.hold(t, v.ID).Status != wantHold {
				t.Fatal("late actual outcome changed its original financial meaning")
			}
			beforeReplay := executionRecoveryFacts(t, f, v.ID, false)
			must(t, restarted.recoverOperations(context.Background()))
			must(t, restarted.applyCreateReceipt(context.Background(), stored))
			if executionRecoveryFacts(t, f, v.ID, false) != beforeReplay || f.posts.Load() != 1 || f.gets.Load() != 0 {
				t.Fatal("late evidence replay added HTTP, money or duplicate events")
			}
		})
	}
}

func TestMySQLExecutionRecoveryNotSentEvidenceRequiresIdentity(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprintf("valid=%v", valid), func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "historical-not-sent")
			op := executionRecoveryClaim(t, f, v.ID)
			op = expireFixtureOperationLease(t, f.s.DB, op)
			if valid {
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 1).Error)
			}
			var row task.Task
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", op.OrgID, v.ID).Take(&row).Error)
			// Defensive historical fixture: current production knownUnsent does
			// not call persistResponse. Preserve the actual encrypted response
			// and typed receipt rather than inventing a naked receipt_ref.
			ref, err := f.s.persistResponse(context.Background(), row, op, ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeNotSent, Body: []byte(`{}`)}, "official-test-credential")
			must(t, err)
			op = runtimeWorkerCreateOperation(t, f, v.ID)
			if op.ReceiptRef != ref || ref == "" {
				t.Fatal("historical no-send evidence did not commit")
			}
			executionRecoveryScope(t, f, []uint64{f.key.OrgID})
			before := executionRecoveryFacts(t, f, v.ID, true)
			recoveryErr := f.s.recoverOperations(context.Background())
			if valid {
				must(t, recoveryErr)
			} else {
				requireRecoveryCause(t, recoveryErr, invocation.ErrVideoSchemaUnsupported, 1)
			}
			after := runtimeWorkerCreateOperation(t, f, v.ID)
			if valid {
				if after.Status != "rejected" || after.ReceiptAppliedAt == nil || f.hold(t, v.ID).Status != ledger.HoldReleased {
					t.Fatal("valid historical no-send lost its exact-once release")
				}
			} else {
				if after.Status != "needs_review" || after.ReceiptAppliedAt != nil || after.ReceiptRef != ref || executionRecoveryFacts(t, f, v.ID, true) != before {
					t.Fatal("bad no-send was released or its evidence discarded")
				}
				var candidates []Operation
				must(t, f.s.recoveryOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID).Limit(30).Find(&candidates).Error)
				if len(candidates) != 0 {
					t.Fatal("quarantined no-send occupied recovery LIMIT again")
				}
				// Repairing input metadata cannot let a previously selected stale
				// no-send receipt bypass the durable manual-review barrier.
				must(t, f.s.DB.Model(&invocation.VideoPayload{}).Where("org_id = ? AND payload_id = ?", op.OrgID, op.PayloadRef).Update("schema_version", 1).Error)
				repaired := executionRecoveryFacts(t, f, v.ID, false)
				if err := f.s.applyCreateReceipt(context.Background(), op); !errors.Is(err, ErrConflict) || executionRecoveryFacts(t, f, v.ID, false) != repaired {
					t.Fatal("stale no-send candidate bypassed durable review after repair", err)
				}
			}
			stable := executionRecoveryFacts(t, f, v.ID, false)
			must(t, f.s.recoverOperations(context.Background()))
			if executionRecoveryFacts(t, f, v.ID, false) != stable || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("no-send replay changed money, evidence or HTTP")
			}
		})
	}
}
