package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Consumer fixtures never dispatch a provider request, including setup. The
// existing create inbox registers and binds the task using production prepare,
// append and application functions; only the provider observation is synthetic.
type mutationConsumerFixture struct {
	*fixture
	taskID    string
	httpCalls atomic.Int32
}

func newMutationConsumerFixture(t *testing.T, finalStatus string) *mutationConsumerFixture {
	t.Helper()
	m := &mutationConsumerFixture{fixture: mysqlFixture(t)}
	m.s.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		m.httpCalls.Add(1)
		t.Error("mutation consumer unexpectedly attempted provider HTTP")
		return nil, errors.New("consumer has no provider transport")
	})}
	t.Cleanup(func() {
		if m.httpCalls.Load() != 0 {
			t.Error("consumer or recovery issued provider HTTP")
		}
	})
	v := m.create(t, "mutation-consumer-task")
	m.taskID = v.ID
	attempt, claim := responseInboxRegister(t, m.fixture, v.ID)
	receipt := responseInboxAppend(t, m.fixture, attempt, responseInboxResult(ark.OutcomeAccepted, "up_"+v.ID))
	must(t, m.s.applyClaimedCreateInbox(context.Background(), claim, receipt.ReceiptID))
	if finalStatus != "" {
		m.observe(t, ark.Status(finalStatus), true)
	}
	return m
}

func (m *mutationConsumerFixture) rows(t *testing.T) (task.Task, State) {
	t.Helper()
	var row task.Task
	var st State
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Take(&row).Error)
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Take(&st).Error)
	return row, st
}

func (m *mutationConsumerFixture) operation(t *testing.T, id string) Operation {
	t.Helper()
	var op Operation
	must(t, m.s.DB.Where("org_id = ? AND id = ? AND resource_id = ?", m.key.OrgID, id, m.taskID).Take(&op).Error)
	return op
}

func (m *mutationConsumerFixture) pending(t *testing.T, action string) Operation {
	t.Helper()
	var op Operation
	var err error
	switch action {
	case "cancel_task":
		op, _, err = m.s.Cancel(context.Background(), m.key.OrgID, m.taskID, "mutation-cancel-request", "mutation-cancel-idempotency")
	case "delete_task":
		op, _, err = m.s.Delete(context.Background(), m.key.OrgID, m.taskID, "mutation-delete-request")
	default:
		t.Fatalf("unsupported consumer fixture action %q", action)
	}
	must(t, err)
	if op.Status != "pending" {
		t.Fatalf("fixture operation is %s, want pending", op.Status)
	}
	return op
}

func (m *mutationConsumerFixture) register(t *testing.T, action string) (ExecutionAttempt, Operation) {
	t.Helper()
	op, prepared := m.prepareClaim(t, action)
	attempt, err := m.commitClaim(context.Background(), op, prepared)
	must(t, err)
	return attempt, m.operation(t, op.ID)
}

func (m *mutationConsumerFixture) prepareClaim(t *testing.T, action string) (Operation, *preparedMutationInput) {
	t.Helper()
	op := m.pending(t, action)
	row, st := m.rows(t)
	var err error
	op.AttemptID, err = randomID("att_")
	must(t, err)
	op.Version++
	op.Status = "in_flight"
	lease := m.now.Add(3 * time.Minute)
	op.LeaseUntil = &lease
	prepared, err := m.s.prepareMutationInput(context.Background(), row, st, op, "official-test-credential")
	must(t, err)
	return op, prepared
}

func (m *mutationConsumerFixture) commitClaim(ctx context.Context, op Operation, prepared *preparedMutationInput) (ExecutionAttempt, error) {
	var attempt ExecutionAttempt
	err := m.s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		currentTask, currentState, e := loadTask(tx, m.key.OrgID, m.taskID, true)
		if e != nil {
			return e
		}
		var current Operation
		if e = locked(tx).Where("org_id = ? AND id = ? AND resource_id = ?", m.key.OrgID, op.ID, m.taskID).Take(&current).Error; e != nil {
			return e
		}
		if current.Status != "pending" || current.Version+1 != op.Version {
			return ErrConflict
		}
		if e = tx.Model(&Operation{}).Where("org_id = ? AND id = ?", m.key.OrgID, op.ID).Updates(map[string]any{
			"status": op.Status, "version": op.Version, "attempt_id": op.AttemptID, "lease_until": op.LeaseUntil,
		}).Error; e != nil {
			return e
		}
		attempt, e = m.s.registerMutationAttemptTx(tx, currentTask, currentState, op, prepared)
		return e
	})
	return attempt, err
}

func (m *mutationConsumerFixture) append(t *testing.T, attempt ExecutionAttempt, result ark.Result) ResponseInbox {
	t.Helper()
	prepared, err := m.s.prepareMutationResponse(attempt, result, "official-test-credential")
	must(t, err)
	receipt, err := m.s.appendResponseReceipt(context.Background(), prepared)
	must(t, err)
	return receipt
}

func (m *mutationConsumerFixture) recoverable(t *testing.T, op Operation) {
	t.Helper()
	must(t, m.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", m.key.OrgID, op.ID).Updates(map[string]any{
		"status": "unknown", "version": op.Version + 1,
	}).Error)
}

// A real QueryReceipt is independently staged and applied through the ordinary
// query reducer. This isolates the consumer's zero-HTTP assertion from the
// separate GET producer while retaining real settlement/release transitions.
func (m *mutationConsumerFixture) observe(t *testing.T, status ark.Status, withUsage bool) QueryReceipt {
	t.Helper()
	row, st := m.rows(t)
	attempt, err := randomID("att_")
	must(t, err)
	var usage *int64
	if withUsage {
		n := int64(100)
		if status != ark.StatusSucceeded {
			n = 0
		}
		usage = &n
	}
	body, err := json.Marshal(queryObservation{TaskID: row.UpstreamTaskID, Model: st.UpstreamModel, Status: status, CompletionTokens: usage})
	must(t, err)
	prepared, err := m.s.Evidence.Prepare(invocation.VideoPayloadInput{
		OrgID: m.key.OrgID, RequestID: attempt, AttemptID: attempt,
		Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory,
		SchemaVersion: 1, Body: body, ExpiresAt: m.now.Add(30 * 24 * time.Hour), CaptureEnabled: true,
	})
	must(t, err)
	receipt := QueryReceipt{OrgID: m.key.OrgID, TaskID: m.taskID, AttemptID: attempt,
		Sequence: st.AppliedQuerySequence + 1, TaskRowVersion: st.RowVersion,
		HTTPStatus: 200, Outcome: string(ark.OutcomeAccepted), ObservationJSON: string(body), ReceivedAt: m.now,
		RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion,
		ProtocolContractVersion: st.ProtocolContractVersion, ObservationSchemaVersion: videoruntime.ObservationSchemaV1,
	}
	must(t, m.s.DB.Transaction(func(tx *gorm.DB) error {
		ref, e := m.s.Evidence.StageTx(tx, prepared)
		if e != nil {
			return e
		}
		receipt.PayloadRef = ref
		return tx.Create(&receipt).Error
	}))
	must(t, m.s.applyQueryReceipt(context.Background(), receipt))
	return receipt
}

func mutationResponse(outcome ark.Outcome, status int) ark.Result {
	return ark.Result{Action: ark.ActionDelete, Outcome: outcome, HTTPStatus: status, Body: []byte(`{}`)}
}

func (m *mutationConsumerFixture) funds(t *testing.T) string {
	t.Helper()
	return responseInboxFunds(t, m.fixture, m.taskID)
}

func (m *mutationConsumerFixture) effects(t *testing.T) string {
	t.Helper()
	var events []Event
	var outbox []Outbox
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Order("id").Find(&events).Error)
	must(t, m.s.DB.Where("org_id = ? AND task_id = ?", m.key.OrgID, m.taskID).Order("event_id").Find(&outbox).Error)
	data, err := json.Marshal([]any{events, outbox})
	must(t, err)
	return string(data)
}
