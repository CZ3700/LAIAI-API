package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func (f *fixture) upstream(t *testing.T, id string) string {
	t.Helper()
	var row task.Task
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).First(&row).Error)
	return row.UpstreamTaskID
}

func (f *fixture) receipt(t *testing.T, id string, sequence uint64, completion *int64) QueryReceipt {
	t.Helper()
	meta, e := json.Marshal(queryObservation{TaskID: f.upstream(t, id), Status: ark.StatusSucceeded, CompletionTokens: completion})
	must(t, e)
	att, e := randomID("att_")
	must(t, e)
	r := QueryReceipt{OrgID: f.key.OrgID, TaskID: id, AttemptID: att, Sequence: sequence, HTTPStatus: 200, Outcome: string(ark.OutcomeAccepted), ObservationJSON: string(meta), ReceivedAt: f.now}
	must(t, f.s.DB.Create(&r).Error)
	return r
}

func TestMySQLHardeningFinancialFinalSurvivesLateAndMissingUsage(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "hard-final-1")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	// A new successful response without usage cannot clear known actual usage.
	f.now = f.now.Add(11 * time.Second)
	f.response = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"id":"`+f.upstream(t, v.ID)+`","status":"succeeded"}`), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.BillingState != "settled" || state.Usage == nil || state.Usage.CompletionTokens == nil || *state.Usage.CompletionTokens != 100 || state.Usage.TotalTokens == nil || *state.Usage.TotalTokens != 999 {
		t.Fatalf("financial projection regressed: %+v", state)
	}
	// An expired worker's older receipt is retained, but cannot apply over seq 2.
	bad := int64(777)
	old := f.receipt(t, v.ID, 3, &bad)
	good := int64(100)
	current := f.receipt(t, v.ID, 4, &good)
	must(t, f.s.applyQueryReceipt(context.Background(), current))
	must(t, f.s.applyQueryReceipt(context.Background(), old))
	must(t, f.s.DB.Where("org_id = ? AND attempt_id = ?", f.key.OrgID, old.AttemptID).First(&old).Error)
	if old.ApplyErrorCode != "stale_query_response" || old.AppliedAt == nil {
		t.Fatal("old response was not fenced")
	}
	// Even a newer conflicting report is evidence, not a silent adjustment.
	newer := f.receipt(t, v.ID, 5, &bad)
	must(t, f.s.applyQueryReceipt(context.Background(), newer))
	state, e = f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.BillingState != "settled" || *state.Usage.CompletionTokens != 100 || state.SyncState != "needs_review" || f.hold(t, v.ID).CapturedAmount.String() != "0.0008" {
		t.Fatal("conflicting observation repriced the task")
	}
}

func TestMySQLHardeningQueryLeasePreventsOverlap(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "hard-query-lease")
	f.dispatch(t, v.ID)
	upstream := f.upstream(t, v.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.response = func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return reply(200, `{"id":"`+upstream+`","status":"running"}`), nil
	}
	done := make(chan error, 1)
	go func() { done <- f.s.pollTask(context.Background(), f.key.OrgID, v.ID) }()
	<-entered
	// Force the cadence due while the network call is active: the lease alone
	// must suppress the second call, even with multiple workers.
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("next_poll_at", f.now.Add(-time.Hour)).Error)
	must(t, f.s.DB.Model(&ProviderRate{}).Where("action = ? AND scope_id = (SELECT provider_scope_id FROM video_task_states WHERE org_id = ? AND task_id = ?)", "query", f.key.OrgID, v.ID).Update("next_allowed_at", f.now.Add(-time.Hour)).Error)
	err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID)
	close(release)
	must(t, err)
	must(t, <-done)
	if calls.Load() != 1 {
		t.Fatalf("overlapping queries: %d", calls.Load())
	}
}

func TestMySQLHardeningReceiptSurvivesSettlementConflict(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "hard-conflict-receipt")
	f.dispatch(t, v.ID)
	upstream := f.upstream(t, v.ID)
	// Simulate an independently committed exact bill before projection refresh.
	n := int64(200)
	eid, _ := randomID("ev_")
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		_, e := videobilling.SettleUsageTx(tx, videobilling.SettlementInput{OrgID: f.key.OrgID, TaskID: v.ID, Evidence: videobilling.UsageEvidence{EvidenceID: eid, Source: "bill_exact", SourceRecordID: upstream, SourceVersion: eid, CompletionTokens: &n, ObservedAt: f.now}, Now: f.now})
		return e
	}))
	err := f.s.pollTask(context.Background(), f.key.OrgID, v.ID)
	if !errors.Is(err, videobilling.ErrConflict) {
		t.Fatalf("want settlement conflict, got %v", err)
	}
	var r QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&r).Error)
	if r.PayloadRef == "" || r.AppliedAt != nil || r.ApplyErrorCode != "settlement_conflict" {
		t.Fatalf("lost pending evidence: %+v", r)
	}
	var payload invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, r.PayloadRef).First(&payload).Error)
	if f.hold(t, v.ID).CapturedAmount.String() != "0.0016" {
		t.Fatal("conflict changed captured amount")
	}
}

func TestMySQLHardeningCancelIntentSharedAcrossKeys(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "hard-cancel-multi")
	f.dispatch(t, v.ID)
	first, _, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "req-one", "cancel-key-one")
	must(t, e)
	second, replay, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "req-two", "cancel-key-two")
	must(t, e)
	if !replay || first.ID != second.ID {
		t.Fatal("different keys created two cancel intents")
	}
	var deletes atomic.Int32
	f.response = func(*http.Request) (*http.Response, error) { deletes.Add(1); return reply(200, `{}`), nil }
	must(t, f.s.processMutation(context.Background(), first))
	third, replay, e := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, "req-three", "cancel-key-three")
	must(t, e)
	if !replay || first.ID != third.ID || third.Status != "unknown" {
		t.Fatal("uncertain cancellation was not reused")
	}
	must(t, f.s.processMutation(context.Background(), third))
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if deletes.Load() != 1 || state.BillingState != "pending_usage" || f.hold(t, v.ID).Status != ledger.HoldActive {
		t.Fatal("uncertain cancellation repeated or refunded")
	}
	// An acknowledgement arriving after successful settlement cannot regress it.
	upstream := f.upstream(t, v.ID)
	f.response = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"id":"`+upstream+`","status":"succeeded","usage":{"completion_tokens":100}}`), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, first.ID).Update("receipt_applied_at", nil).Error)
	must(t, f.s.applyMutationReceipt(context.Background(), first))
	state, e = f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.BillingState != "settled" {
		t.Fatal("late cancellation reset final billing")
	}
}

type denyCountingGate struct{ calls atomic.Int32 }

func (g *denyCountingGate) GateKey(*identity.APIKey, string, string) (int, string, bool) {
	g.calls.Add(1)
	return 429, "exhausted", false
}
func (g *denyCountingGate) AllowModelRate(uint64, string) (bool, int) {
	g.calls.Add(1)
	return false, 0
}

func TestMySQLHardeningHTTPReplayBeforeAdmission(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "hard-http-replay")
	g := &denyCountingGate{}
	f.s.Gate = g
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/video/generations", f.s.Middleware(), f.s.HandleCreate)
	call := func(prompt string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/video/generations", strings.NewReader(`{"model":"seedance-2.0","content":[{"type":"text","text":"`+prompt+`"}]}`))
		req.Header.Set("Authorization", "Bearer "+f.plain)
		req.Header.Set("Idempotency-Key", "hard-http-replay")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := call("test prompt private")
	if w.Code != 202 || w.Header().Get("Idempotency-Replayed") != "true" || !strings.Contains(w.Body.String(), v.ID) || g.calls.Load() != 0 {
		t.Fatalf("replay consumed gate: %d %s", w.Code, w.Body.String())
	}
	w = call("changed")
	if w.Code != 409 || g.calls.Load() != 0 {
		t.Fatalf("conflict was masked by gate: %d", w.Code)
	}
	var envelope map[string]json.RawMessage
	must(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	var body map[string]any
	must(t, json.Unmarshal(envelope["error"], &body))
	if len(envelope) != 2 || envelope["request_id"] == nil || body["request_id"] != nil || body["code"] != "idempotency_conflict" {
		t.Fatal("error contract mismatch")
	}
}

func TestMySQLHardeningLegacyOfficialAliasesCannotFallback(t *testing.T) {
	f := mysqlFixture(t)
	gin.SetMode(gin.TestMode)
	var legacy atomic.Int32
	r := gin.New()
	r.POST("/v1/videos", f.s.LegacyCreate(func(c *gin.Context) { legacy.Add(1); c.Status(204) }))
	for _, raw := range []string{
		`{"model":"doubao-seedance-2-0-260128","content":[]}`,
		`{"model":"doubao-seedance-2-5-260628","content":[]}`,
		`{"model":"Seedance2.0","content":[]}`,
		`{"model":"seedance-2.0","model":"veo","content":[]}`,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/videos", strings.NewReader(raw)))
		if w.Code != 401 || legacy.Load() != 0 {
			t.Fatalf("official model escaped: %d %s", w.Code, raw)
		}
		var out struct {
			RequestID string `json:"request_id"`
			Error     struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		must(t, json.Unmarshal(w.Body.Bytes(), &out))
		if out.RequestID == "" || out.Error.Code != "unauthenticated" {
			t.Fatal("public error contract")
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/videos", strings.NewReader(`{"model":"veo","prompt":"seedance discussion"}`)))
	if w.Code != 204 || legacy.Load() != 1 {
		t.Fatal("unrelated provider was rerouted")
	}
}

func TestPublicErrorCodeContract(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		want   string
	}{{"invalid_api_key", 401, "unauthenticated"}, {"ip_not_allowed", 403, "forbidden"}, {"audit_unavailable", 503, "provider_unavailable"}, {"concurrency_limit", 429, "rate_limited"}, {"request_too_large", 413, "invalid_request"}, {"artifact_not_ready", 409, "content_not_ready"}, {"expired", 410, "content_expired"}, {"arbitrary_provider_error", 500, "internal_error"}} {
		if got := publicErrorCode(tc.code, tc.status); got != tc.want {
			t.Fatalf("%s -> %s, want %s", tc.code, got, tc.want)
		}
	}
}

func TestMySQLHardeningWaitingCancellationsDoNotStarveCreates(t *testing.T) {
	f := mysqlFixture(t)
	// Seed only this fixture's task rows. They represent unknown POST outcomes;
	// no holds or upstream calls are created for these blocked cancel intents.
	for i := 0; i < 25; i++ {
		id, _ := randomID("task_")
		op, _ := randomID("op_")
		must(t, f.s.DB.Create(&task.Task{TaskID: id, OrgID: f.key.OrgID, APIKeyID: f.key.ID, Model: ark.Model20, Provider: Provider, Status: "submission_unknown", SubmittedAt: f.now.Add(-time.Hour)}).Error)
		must(t, f.s.DB.Create(&State{TaskID: id, OrgID: f.key.OrgID, BillingState: "pending_usage", NextPollAt: f.now, CreatedAt: f.now.Add(-time.Hour), UpdatedAt: f.now}).Error)
		must(t, f.s.DB.Create(&Operation{ID: op, OrgID: f.key.OrgID, ResourceID: id, Kind: "cancel_task", Status: "pending", Version: 1, CreatedAt: f.now.Add(-time.Hour), UpdatedAt: f.now}).Error)
	}
	v := f.create(t, "hard-fair-queue")
	var ops []Operation
	queued := f.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ?", f.key.OrgID)
	must(t, f.s.DB.Table("(?) AS eligible", queued).Where("scope_rank = 1").Order("created_at, id").Limit(20).Find(&ops).Error)
	if len(ops) != 1 || ops[0].ResourceID != v.ID {
		t.Fatalf("waiting cancellations consumed bounded batch: %+v", ops)
	}
	must(t, f.s.processOperation(context.Background(), ops[0]))
	if f.posts.Load() != 1 {
		t.Fatalf("eligible create starved: POST count %d", f.posts.Load())
	}
	state, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if state.Status == "accepted" {
		t.Fatal("eligible task remained queued locally")
	}
}
