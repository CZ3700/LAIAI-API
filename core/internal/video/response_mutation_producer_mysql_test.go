package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Setup uses the established consumer fixture's actual Stage/HMAC/binding and
// settlement functions. Only this new wrapper installs a counted mock transport
// for the real producer; it never fabricates an execution registration.
type mutationProducerFixture struct {
	*mutationConsumerFixture
	deletes atomic.Int32
	gets    atomic.Int32
	respond func(*http.Request) (*http.Response, error)
}

func testMutationProducerDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("fixture must have required the isolated MySQL DSN")
	}
	return dsn
}

func mutationProducerStagingCount(t *testing.T, p *mutationProducerFixture) int64 {
	t.Helper()
	var count int64
	must(t, p.s.DB.Table("invocation_payload_staging AS s").Joins("JOIN invocation_payloads p ON p.payload_id=s.payload_id").Where("p.org_id = ?", p.key.OrgID).Count(&count).Error)
	return count
}

func newMutationProducerFixture(t *testing.T, final string) *mutationProducerFixture {
	t.Helper()
	p := &mutationProducerFixture{mutationConsumerFixture: newMutationConsumerFixture(t, final)}
	p.s.MutationResponseInboxEnabled = true
	p.s.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			p.deletes.Add(1)
		} else if r.Method == http.MethodGet {
			p.gets.Add(1)
		} else {
			return nil, errors.New("unexpected producer test HTTP method")
		}
		if p.respond != nil {
			return p.respond(r)
		}
		if r.Method == http.MethodGet {
			return reply(200, `{"id":"`+path.Base(r.URL.Path)+`","status":"running"}`), nil
		}
		return reply(204, ""), nil
	})}
	return p
}

func (p *mutationProducerFixture) execution(t *testing.T, op Operation) ExecutionAttempt {
	t.Helper()
	var rows []ExecutionAttempt
	must(t, p.s.DB.Where("org_id = ? AND operation_id = ?", p.key.OrgID, op.ID).Find(&rows).Error)
	if len(rows) != 1 || rows[0].StorageProtocol != videoruntime.ActionStorageProtocolV1 || rows[0].ExecutionSchemaVersion != 2 || rows[0].InputIdentityHMAC == "" || rows[0].TargetUpstreamTaskID == nil {
		t.Fatalf("producer did not create one complete immutable action identity: count=%d", len(rows))
	}
	return rows[0]
}

func TestMySQLMutationProducerFlagsAreIndependent(t *testing.T) {
	for _, createInbox := range []bool{false, true} {
		for _, mutationInbox := range []bool{false, true} {
			t.Run(fmt.Sprintf("create=%v/mutation=%v", createInbox, mutationInbox), func(t *testing.T) {
				f := mysqlFixture(t)
				f.s.ResponseInboxEnabled, f.s.MutationResponseInboxEnabled = createInbox, mutationInbox
				v := f.create(t, "producer-flag-matrix")
				var deletes atomic.Int32
				f.response = func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodPost {
						return reply(200, `{"id":"up_`+v.ID+`"}`), nil
					}
					if r.Method == http.MethodDelete {
						deletes.Add(1)
						return reply(204, ""), nil
					}
					return nil, errors.New("unexpected flag-matrix query")
				}
				f.dispatch(t, v.ID)
				createOp := runtimeWorkerCreateOperation(t, f, v.ID)
				wantCreate := int64(0)
				if createInbox {
					wantCreate = 1
				}
				if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", f.key.OrgID, createOp.ID) != wantCreate {
					t.Fatal("mutation flag changed create storage protocol")
				}
				op, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "cancel-flag", "cancel-flag")
				must(t, err)
				before := responseInboxFunds(t, f, v.ID)
				must(t, f.s.processMutation(context.Background(), op))
				_ = f.s.processMutation(context.Background(), op)
				wantMutation := int64(0)
				if mutationInbox {
					wantMutation = 1
				}
				if responseInboxCount(t, f, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", f.key.OrgID, op.ID) != wantMutation || deletes.Load() != 1 || f.posts.Load() != 1 || responseInboxFunds(t, f, v.ID) != before {
					t.Fatal("flag matrix changed mutation storage, resent HTTP or altered funds")
				}
			})
		}
	}
}

func TestMySQLMutationProducerTwoServicesSendOnce(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	other, err := NewService(p.s.DB, p.s.Keyring, p.s.Secret, p.s.Log)
	must(t, err)
	other.Now, other.Client, other.Gate = p.s.Now, p.s.Client, p.s.Gate
	other.MutationResponseInboxEnabled = true
	// A competing Service has a separate bounded evidence pool, not a copied
	// atomic latch or an alias of the original Service.
	must(t, other.ConfigureResponseInbox(testMutationProducerDSN(t), false))
	t.Cleanup(func() { must(t, other.CloseResponseInbox()) })
	before := p.funds(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, service := range []*Service{p.s, other} {
		wg.Add(1)
		go func(s *Service) { defer wg.Done(); <-start; results <- s.processMutation(context.Background(), op) }(service)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes == 0 || p.deletes.Load() != 1 || p.funds(t) != before {
		t.Fatal("competing real producers did not preserve one DELETE and the hold")
	}
	a := p.execution(t, op)
	r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
	if a.InputPayloadRef != p.operation(t, op.ID).PayloadRef || responseInboxApp(t, p.fixture, r).State != "applied" {
		t.Fatal("winner did not stage/apply its own action input")
	}
	_ = other.processMutation(context.Background(), op)
	_ = p.s.processMutation(context.Background(), op)
	if p.deletes.Load() != 1 {
		t.Fatal("repeated process resent a registered action")
	}
}

func TestMySQLMutationProducerTypedResponsesNeverRefundCancel(t *testing.T) {
	for _, action := range []string{"cancel_task", "delete_task"} {
		for _, tc := range []struct {
			name               string
			status             int
			body               string
			transport          error
			outcome, operation string
		}{
			{"ack200", 200, `{}`, nil, "accepted", "ack"},
			{"ack202", 202, `{}`, nil, "accepted", "ack"},
			{"ack204", 204, "", nil, "accepted", "ack"},
			{"not_found", 404, `{}`, nil, "rejected", "ack"},
			{"rejected", 400, `{}`, nil, "rejected", "rejected"},
			{"unknown502", 502, `{}`, nil, "unknown", "unknown"},
			{"lost_response", 0, "", io.ErrUnexpectedEOF, "unknown", "unknown"},
			{"definite_dial_failure", 0, "", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic dial refused")}, "not_sent", "rejected"},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				final := ""
				if action == "delete_task" {
					final = "succeeded"
				}
				p := newMutationProducerFixture(t, final)
				op := p.pending(t, action)
				before := p.funds(t)
				p.respond = func(*http.Request) (*http.Response, error) {
					if tc.transport != nil {
						return nil, tc.transport
					}
					return reply(tc.status, tc.body), nil
				}
				must(t, p.s.processMutation(context.Background(), op))
				r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
				current := p.operation(t, op.ID)
				_, st := p.rows(t)
				want := tc.operation
				if want == "ack" {
					want = "unknown"
					if action == "delete_task" {
						want = "confirmed"
					}
				}
				if r.Outcome != tc.outcome || current.Status != want || responseInboxApp(t, p.fixture, r).State != "applied" || p.funds(t) != before || p.deletes.Load() != 1 || (st.DeletedAt != nil) != (action == "delete_task" && want == "confirmed") {
					t.Fatal("typed producer response changed the wrong task/funds/operation state")
				}
				_ = p.s.processMutation(context.Background(), op)
				if p.deletes.Load() != 1 {
					t.Fatal("unknown or completed mutation was sent twice")
				}
			})
		}
	}
}

func TestMySQLMutationProducerClaimFailuresRollbackBeforeHTTP(t *testing.T) {
	for _, table := range []string{"video_operations", "video_attempts", "invocation_payloads", "invocation_payload_staging", "video_execution_attempts"} {
		t.Run(table, func(t *testing.T) {
			p := newMutationProducerFixture(t, "")
			op := p.pending(t, "cancel_task")
			before := p.funds(t)
			payloads := responseInboxCount(t, p.fixture, &invocation.VideoPayload{}, "org_id = ?", p.key.OrgID)
			staging := mutationProducerStagingCount(t, p)
			var hit atomic.Int32
			name := fmt.Sprintf("producer-claim-fault-%d", p.key.OrgID)
			inject := func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					hit.Add(1)
					tx.AddError(errors.New("synthetic atomic claim failure"))
				}
			}
			if table == "video_operations" {
				must(t, p.s.DB.Callback().Update().Before("gorm:update").Register(name, inject))
				t.Cleanup(func() { _ = p.s.DB.Callback().Update().Remove(name) })
			} else {
				must(t, p.s.DB.Callback().Create().Before("gorm:create").Register(name, inject))
				t.Cleanup(func() { _ = p.s.DB.Callback().Create().Remove(name) })
			}
			err := p.s.processMutation(context.Background(), op)
			if table == "video_operations" {
				must(t, p.s.DB.Callback().Update().Remove(name))
			} else {
				must(t, p.s.DB.Callback().Create().Remove(name))
			}
			current := p.operation(t, op.ID)
			if err == nil || hit.Load() == 0 || p.deletes.Load() != 0 || current.Status != "pending" || current.Version != op.Version || current.AttemptID != "" || current.PayloadRef != "" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 || responseInboxCount(t, p.fixture, &Attempt{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 || responseInboxCount(t, p.fixture, &invocation.VideoPayload{}, "org_id = ?", p.key.OrgID) != payloads || mutationProducerStagingCount(t, p) != staging {
				t.Fatal("failed claim left HTTP, orphaned input/registration or partial state")
			}
		})
	}
}

func TestMySQLMutationProducerPreparationCooldownDoesNotBlockSuccessor(t *testing.T) {
	p := newMutationProducerFixture(t, "")
	op := p.pending(t, "cancel_task")
	n := p.another(t, "producer-ready-successor")
	healthy := n.pending(t, "cancel_task")
	before := p.funds(t)
	var reads atomic.Int32
	name := fmt.Sprintf("producer-preparation-failure-%d", p.key.OrgID)
	must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*CredentialSnapshot); ok && row.TaskID == p.taskID {
			reads.Add(1)
			tx.AddError(errors.New("synthetic private credential unavailable"))
		}
	}))
	t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
	if err := p.s.processMutation(context.Background(), op); err == nil {
		t.Fatal("preparation failure was hidden")
	}
	for i := 0; i < 3; i++ {
		_ = p.s.processMutation(context.Background(), op)
	}
	current := p.operation(t, op.ID)
	if reads.Load() != 1 || current.ErrorCode != "action_prepare_deferred" || current.Status != "pending" || current.Version != op.Version || current.AttemptID != "" || p.funds(t) != before || p.deletes.Load() != 0 {
		t.Fatal("already selected operation repeatedly prepared or changed reservation")
	}
	var candidates []Operation
	q := p.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ?", p.key.OrgID)
	must(t, p.s.DB.Table("(?) AS eligible", q).Where("scope_rank = 1").Limit(20).Find(&candidates).Error)
	if len(candidates) != 1 || candidates[0].ID != healthy.ID {
		t.Fatal("cooling operation occupied same-scope ROW_NUMBER window")
	}
	must(t, p.s.processMutation(context.Background(), candidates[0]))
	if p.deletes.Load() != 1 {
		t.Fatal("healthy successor was not sent")
	}
	must(t, p.s.DB.Callback().Query().Remove(name))
	expireFixturePreparationCooldown(t, p.s.DB, p.operation(t, op.ID))
	_, currentState := p.rows(t)
	makeFixtureProviderRateDue(t, p.s.DB, currentState.ProviderScopeID, "delete")
	must(t, p.s.processMutation(context.Background(), op))
	if p.deletes.Load() != 2 || p.operation(t, op.ID).ErrorCode == "action_prepare_deferred" {
		t.Fatal("expired preparation cooldown did not permit genuine retry")
	}
	p.execution(t, op)
}

func TestMySQLMutationProducerMissingPoolAndBadRuntimeDoNotFallback(t *testing.T) {
	for _, fault := range []string{"missing_pool", "missing_credential", "state_runtime", "operation_runtime", "billing_pending"} {
		t.Run(fault, func(t *testing.T) {
			final := ""
			action := "cancel_task"
			if fault == "billing_pending" {
				final, action = "succeeded", "delete_task"
			}
			p := newMutationProducerFixture(t, final)
			op := p.pending(t, action)
			switch fault {
			case "missing_pool":
				original := p.s.responseDB
				p.s.responseDB = nil
				t.Cleanup(func() { p.s.responseDB = original })
			case "missing_credential":
				must(t, p.s.DB.Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Delete(&CredentialSnapshot{}).Error)
			case "state_runtime":
				must(t, p.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Update("runtime_version", "future-runtime").Error)
			case "operation_runtime":
				must(t, p.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", p.key.OrgID, op.ID).Update("runtime_version", "future-runtime").Error)
			case "billing_pending":
				must(t, p.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", p.key.OrgID, p.taskID).Update("billing_state", "pending_usage").Error)
			}
			before := p.funds(t)
			_ = p.s.processMutation(context.Background(), op)
			current := p.operation(t, op.ID)
			if p.deletes.Load() != 0 || current.Status != "pending" || current.Version != op.Version || current.AttemptID != "" || p.funds(t) != before || responseInboxCount(t, p.fixture, &ExecutionAttempt{}, "org_id = ? AND operation_id = ?", p.key.OrgID, op.ID) != 0 {
				t.Fatal("unavailable new producer fell back or claimed unsupported work")
			}
			if fault == "missing_credential" && current.ErrorCode != "action_prepare_deferred" {
				t.Fatal("missing fixed credential was not deferred safely")
			}
		})
	}
}

func TestMySQLMutationProducerTruncated404AndCredentialReflection(t *testing.T) {
	for _, fault := range []string{"truncated_404", "reflected_header"} {
		t.Run(fault, func(t *testing.T) {
			p := newMutationProducerFixture(t, "succeeded")
			op := p.pending(t, "delete_task")
			before := p.funds(t)
			p.respond = func(*http.Request) (*http.Response, error) {
				if fault == "truncated_404" {
					return reply(404, strings.Repeat("x", int(ark.DefaultMaxResponseBytes)+1)), nil
				}
				r := reply(204, "")
				r.Header = http.Header{"X-Request-Id": []string{"official-test-credential"}}
				return r, nil
			}
			must(t, p.s.processMutation(context.Background(), op))
			r := mutationOneReceipt(t, p.mutationConsumerFixture, op)
			_, st := p.rows(t)
			want := "confirmed"
			if fault == "truncated_404" {
				want = "unknown"
				if r.Outcome != "unknown" {
					t.Fatal("truncated 404 became a definite acknowledgement")
				}
			}
			if p.operation(t, op.ID).Status != want || (st.DeletedAt != nil) != (want == "confirmed") || r.ProviderRequestID != "" || strings.Contains(r.SafeErrorCode, "official-test-credential") || p.funds(t) != before || p.deletes.Load() != 1 {
				t.Fatal("unsafe response metadata or truncated acknowledgement was accepted")
			}
		})
	}
}
