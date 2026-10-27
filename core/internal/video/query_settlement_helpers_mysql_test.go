package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func newSettlementFixture(t *testing.T) *fixture {
	t.Helper()
	// Every caller owns a tracked-migration private schema. Keep the actual
	// global B queue intact; an org scope would hide cross-test interference
	// and change the production candidate plan.
	f := queryRecoveryNew(t).users[0]
	f.s.ResponseInboxEnabled, f.s.MutationResponseInboxEnabled = true, true
	return f
}

// Consumer-C admission fixture: the actual plan/reservation and encrypted
// schema1 input are created once with V2 identity before any Stage/registration.
// Public Create intentionally still chooses V1. No existing capsule or runtime
// is rewritten and no response HMAC is synthesized by the test.
func createSettlementV2(t *testing.T, f *fixture, label string) TaskView {
	t.Helper()
	taskID, err := randomID("task_")
	must(t, err)
	opID, err := randomID("op_")
	must(t, err)
	requestID, err := randomID("")
	must(t, err)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "synthetic settlement fixture"}}}
	body, err := json.Marshal(in)
	must(t, err)
	var out TaskView
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		now, e := authorityTimeTx(tx)
		if e != nil {
			return e
		}
		idem, replay, e := claimIdempotencyTx(tx, f.s.Secret, f.key.OrgID, http.MethodPost, "/v1/video/generations", label, body, taskID, opID, now)
		if e != nil {
			return e
		}
		if replay {
			return ErrConflict
		}
		p, e := f.s.plan(tx, f.key.OrgID, in)
		if e != nil {
			return e
		}
		p.runtime, e = videoruntime.Resolve(videoruntime.GenerationSettlementV2())
		if e != nil {
			return e
		}
		execution, e := f.s.Evidence.PrepareCreateExecutionTx(tx, invocation.VideoExecutionInput{OrgID: f.key.OrgID, RequestID: requestID, Body: p.normalized.Body})
		if e != nil {
			return e
		}
		history, e := f.s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: f.key.OrgID, RequestID: requestID, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: p.normalized.Body, ExpiresAt: f.s.Now().UTC().Add(30 * 24 * time.Hour), CaptureEnabled: true, CaptureRepresentation: "effective_json_redacted"})
		if e != nil {
			return e
		}
		var ch relay.Channel
		if e = locked(tx).Where("id = ? AND status = 1", p.config.ChannelID).Take(&ch).Error; e != nil {
			return e
		}
		credentialID, ciphertext, ok := relay.SelectCredential(tx, &ch)
		if !ok {
			return ErrUnavailable
		}
		credential := CredentialSnapshot{OrgID: f.key.OrgID, TaskID: taskID, ProviderScopeID: p.scope.ID, SourceCredentialID: credentialID, Ciphertext: ciphertext, CreatedAt: now}
		if e = tx.Create(&credential).Error; e != nil {
			return e
		}
		reserved, e := videobilling.ReserveTx(tx, videobilling.ReserveInput{OrgID: f.key.OrgID, APIKeyID: f.key.ID, TaskID: taskID, RequestHMAC: idem.BodyHMAC, BillingSchemaVersion: p.runtime.Contract.BillingSchemaVersion, Quote: p.quote, Now: now})
		if e != nil {
			return e
		}
		payload, e := f.s.Evidence.StageExecutionTx(tx, execution)
		if e != nil {
			return e
		}
		if _, e = f.s.Evidence.StageTx(tx, history); e != nil {
			return e
		}
		row := task.Task{TaskID: taskID, OrgID: f.key.OrgID, UserID: f.key.UserID, APIKeyID: f.key.ID, Model: in.Model, ChannelID: ch.ID, Provider: Provider, Status: "accepted", HoldID: reserved.Snapshot.HoldID, CredentialID: credentialID, EstTokens: p.quote.EstimatedTokens, PriceOutUSDPer1M: p.quote.Rule.RetailUSDPer1M, HoldAmountUSD: p.quote.HoldUSD, GroupRatio: p.quote.Metering.EffectiveRatio, SubmittedAt: now}
		if e = tx.Create(&row).Error; e != nil {
			return e
		}
		if e = f.s.persistTaskResourcesTx(tx, f.key.OrgID, taskID, p); e != nil {
			return e
		}
		st := State{TaskID: taskID, OrgID: f.key.OrgID, CreateRequestID: requestID, ProviderScopeID: p.scope.ID, CredentialSnapshotID: credential.ID, ProviderBaseURL: ch.BaseURL, ConfigVersion: p.config.Version, CapabilityVersion: p.normalized.CapabilityVersion, UpstreamModel: p.normalized.UpstreamModel, NoChargeStatuses: p.config.NoChargeStatuses, BillingState: "reserved", SyncState: "fresh", ArtifactStatus: "pending", EffectivePayloadRef: payload, BillingSnapshotID: reserved.Snapshot.ID, IdempotencyRecordID: idem.ID, RowVersion: 1, CreatedAt: now, UpdatedAt: now, NextPollAt: now, CaptureHistory: true, RetentionDays: 30,
			RuntimeVersion: p.runtime.Contract.RuntimeVersion, AdapterVersion: p.runtime.Contract.AdapterVersion, ProtocolContractVersion: p.runtime.Contract.ProtocolContractVersion, BillingSchemaVersion: p.runtime.Contract.BillingSchemaVersion}
		if e = tx.Create(&st).Error; e != nil {
			return e
		}
		op := Operation{ID: opID, OrgID: st.OrgID, ResourceID: taskID, Kind: "create_task", Status: "pending", Version: 1, PayloadRef: payload, CreatedAt: now, UpdatedAt: now}
		inheritOperationRuntime(st, &op)
		if e = tx.Create(&op).Error; e != nil {
			return e
		}
		if e = appendEventTx(tx, &row, &st, "task.accepted", "", "accepted", requestID, payload, now); e != nil {
			return e
		}
		out = taskView(row, st)
		return f.s.linkCreateRequestTx(tx, st.OrgID, requestID, row, st, opID, false)
	}))
	return out
}

func settlementRatesDue(t *testing.T, f *fixture, id string) {
	t.Helper()
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	var rates []ProviderRate
	must(t, f.s.DB.Where("scope_id = ?", st.ProviderScopeID).Find(&rates).Error)
	for _, rate := range rates {
		makeFixtureProviderRateDue(t, f.s.DB, st.ProviderScopeID, rate.Action)
	}
}

func submitSettlementV2(t *testing.T, f *fixture, label string) TaskView {
	t.Helper()
	v := createSettlementV2(t, f, label)
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v3/contents/generations/tasks" || req.Header.Get("Authorization") != "Bearer official-test-credential" {
			return nil, errors.New("unexpected synthetic create request")
		}
		return reply(200, `{"id":"up_`+v.ID+`"}`), nil
	}
	posts := f.posts.Load()
	settlementRatesDue(t, f, v.ID)
	f.dispatch(t, v.ID)
	if f.posts.Load() != posts+1 {
		t.Fatal("V2 did not send exactly one real mock POST")
	}
	var attempt ExecutionAttempt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND action = ?", f.key.OrgID, v.ID, "create_task").Take(&attempt).Error)
	var receipt ResponseInbox
	must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND attempt_id = ?", f.key.OrgID, v.ID, attempt.AttemptID).Take(&receipt).Error)
	if attempt.RuntimeVersion != videoruntime.RuntimeV2 || attempt.InputIdentityHMAC == "" || receipt.EvidenceHMAC == "" {
		t.Fatal("V2 fixture lacks real Stage/registration/HMAC")
	}
	return v
}

func settlementPoll(t *testing.T, f *fixture, id string, status ark.Status, completion *int64) QueryReceipt {
	t.Helper()
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
	makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, st.QuerySequence, false)
	settlementRatesDue(t, f, id)
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v3/contents/generations/tasks/up_"+id || req.Header.Get("Authorization") != "Bearer official-test-credential" {
			return nil, errors.New("unexpected synthetic query request")
		}
		body := map[string]any{"id": "up_" + id, "status": status}
		if completion != nil {
			body["usage"] = map[string]any{"completion_tokens": *completion, "total_tokens": *completion}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return reply(200, string(raw)), nil
	}
	gets := f.gets.Load()
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, id))
	if f.gets.Load() != gets+1 {
		t.Fatal("V2 poll did not persist one real mock GET")
	}
	var row QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Order("sequence DESC").Take(&row).Error)
	if row.RuntimeVersion != videoruntime.RuntimeV2 || row.PayloadRef == "" || row.GenerationAppliedAt == nil {
		t.Fatal("V2 G did not persist its real evidence and phase")
	}
	return row
}

func settlementForbidNetwork(t *testing.T, f *fixture) {
	t.Helper()
	f.response = func(*http.Request) (*http.Response, error) {
		t.Error("B attempted provider HTTP")
		return nil, errors.New("B is zero network")
	}
	name := "test:settlement-no-input-" + t.Name()
	must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
		if q.Statement.Table == "video_credential_snapshots" || q.Statement.Table == "invocation_payloads" || q.Statement.Table == "invocation_payload_staging" {
			q.AddError(errors.New("B attempted credential or encrypted input read"))
			t.Error("B read execution/credential storage")
		}
	}))
	t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
}
