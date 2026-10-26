package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Only the wire is mocked. Acceptance, encrypted evidence, reducers, ledger
// and replay all use production functions and the tracked MySQL schema.
type runtimeHTTPFixture struct {
	f          *fixture
	id         string
	upstreamID string
	inbox      bool
	history    bool
	state      State
	task       task.Task
	snapshot   videobilling.Snapshot
	credential CredentialSnapshot
	input      []byte
	hold       ledger.Hold
	account    ledger.Account
	buckets    map[uint64]videobilling.QuotaBucket
	keyUsed    decimal.Decimal
}

type runtimeHTTPKeyUsage struct {
	ID, OrgID              uint64
	QuotaLimit, UsedAmount decimal.Decimal
}

func runtimeHTTPNew(t *testing.T, inbox, history bool) *runtimeHTTPFixture {
	t.Helper()
	f := mysqlFixture(t)
	f.s.ResponseInboxEnabled = inbox
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", history).Error)
	v := f.create(t, "raw-http-persistence")
	h := &runtimeHTTPFixture{f: f, id: v.ID, upstreamID: "up_" + v.ID, inbox: inbox, history: history}
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&h.state).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Take(&h.task).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, h.state.BillingSnapshotID).Take(&h.snapshot).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, h.state.CredentialSnapshotID).Take(&h.credential).Error)
	h.hold = f.hold(t, v.ID)
	must(t, f.s.DB.First(&h.account, h.hold.AccountID).Error)
	var err error
	h.input, err = f.s.Evidence.ReadForExecutionSchema(context.Background(), f.key.OrgID, h.state.EffectivePayloadRef, 1, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "http-conformance", ReasonCode: "dispatch"})
	must(t, err)
	if stateRuntime(h.state) != videoruntime.LegacyV1() || h.state.CaptureHistory != history || h.hold.Status != ledger.HoldActive || !h.hold.Amount.Equal(h.snapshot.HoldUSD) {
		t.Fatal("fixture did not accept and freeze the fixed v1 contract")
	}
	_, buckets, key := h.quotaFacts(t)
	h.buckets = make(map[uint64]videobilling.QuotaBucket, len(buckets))
	for _, bucket := range buckets {
		h.buckets[bucket.ID] = bucket
	}
	h.keyUsed = key.UsedAmount
	return h
}

func (h *runtimeHTTPFixture) quotaFacts(t *testing.T) ([]videobilling.QuotaReservation, []videobilling.QuotaBucket, runtimeHTTPKeyUsage) {
	t.Helper()
	var reservations []videobilling.QuotaReservation
	var buckets []videobilling.QuotaBucket
	var key runtimeHTTPKeyUsage
	must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Order("id").Find(&reservations).Error)
	ids := make([]uint64, 0, len(reservations))
	for _, reservation := range reservations {
		ids = append(ids, reservation.BucketID)
	}
	// Supplier buckets intentionally have org_id=0. Tenant-scoped reservation
	// ownership supplies the exact IDs; an org filter here would hide them.
	if len(ids) == 0 {
		t.Fatal("fixture has no supplier budget reservation")
	}
	must(t, h.f.s.DB.Where("id IN ?", ids).Order("id").Find(&buckets).Error)
	if len(buckets) != len(reservations) {
		t.Fatal("task reservation lost its referenced quota bucket")
	}
	must(t, h.f.s.DB.Model(&identity.APIKey{}).Select("id", "org_id", "quota_limit", "used_amount").Where("org_id = ? AND id = ?", h.f.key.OrgID, h.f.key.ID).Take(&key).Error)
	return reservations, buckets, key
}

func (h *runtimeHTTPFixture) funds(t *testing.T) string {
	t.Helper()
	_, buckets, key := h.quotaFacts(t)
	data, err := json.Marshal([]any{responseInboxFunds(t, h.f, h.id), buckets, key})
	must(t, err)
	return string(data)
}

func (h *runtimeHTTPFixture) quotaFinal(t *testing.T, tokens int64, released bool) {
	t.Helper()
	reservations, buckets, key := h.quotaFacts(t)
	byID := make(map[uint64]videobilling.QuotaBucket, len(buckets))
	for _, bucket := range buckets {
		byID[bucket.ID] = bucket
	}
	wantUSD := decimal.NewFromInt(tokens).Mul(decimal.RequireFromString("0.000008"))
	wantCNY := decimal.NewFromInt(tokens).Mul(decimal.RequireFromString("0.000046"))
	wantState := "consumed"
	if released {
		wantUSD, wantCNY, wantState = decimal.Zero, decimal.Zero, "released"
	}
	supplierActual := decimal.Zero
	supplierCount := 0
	for _, reservation := range reservations {
		before, ok := h.buckets[reservation.BucketID]
		after := byID[reservation.BucketID]
		// The fixture deliberately has one shared supplier CNY policy. Keep the
		// oracle fixed, independent of the changed active pricing/configuration.
		if !ok || !reservation.Supplier || reservation.Metric != videobilling.MetricCNY || before.OrgID != 0 || after.OrgID != 0 {
			t.Fatal("unexpected fixture quota identity")
		}
		supplierCount++
		supplierActual = supplierActual.Add(reservation.ActualAmount)
		if reservation.State != wantState || reservation.SettledAt == nil || !reservation.ActualAmount.Equal(wantCNY) || reservation.ActualUnits != 0 || !after.ReservedAmount.Equal(before.ReservedAmount.Sub(reservation.ReservedAmount)) || after.ReservedUnits != before.ReservedUnits-reservation.ReservedUnits || !after.ConsumedAmount.Equal(before.ConsumedAmount.Add(wantCNY)) || after.ConsumedUnits != before.ConsumedUnits || after.Blocked != before.Blocked {
			t.Fatal("supplier actual or reservation return differs from the original fixed CNY oracle")
		}
	}
	if supplierCount != 1 || !supplierActual.Equal(wantCNY) || !key.UsedAmount.Equal(h.keyUsed.Sub(h.hold.Amount).Add(wantUSD)) {
		t.Fatal("supplier or API Key usage changed by an incorrect settlement/release amount")
	}
}

func (h *runtimeHTTPFixture) operation(t *testing.T) Operation {
	t.Helper()
	var op Operation
	must(t, h.f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", h.f.key.OrgID, h.id, "create_task").Take(&op).Error)
	return op
}

func (h *runtimeHTTPFixture) rows(t *testing.T) (task.Task, State) {
	t.Helper()
	var row task.Task
	var st State
	must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Take(&row).Error)
	must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Take(&st).Error)
	return row, st
}

func (h *runtimeHTTPFixture) count(t *testing.T, model any) int64 {
	t.Helper()
	return responseInboxCount(t, h.f, model, "org_id = ? AND task_id = ?", h.f.key.OrgID, h.id)
}

func (h *runtimeHTTPFixture) pinned(t *testing.T) {
	t.Helper()
	row, st := h.rows(t)
	var snapshot videobilling.Snapshot
	var credential CredentialSnapshot
	must(t, h.f.s.DB.Where("org_id = ? AND id = ?", h.f.key.OrgID, st.BillingSnapshotID).Take(&snapshot).Error)
	must(t, h.f.s.DB.Where("org_id = ? AND id = ?", h.f.key.OrgID, st.CredentialSnapshotID).Take(&credential).Error)
	if !reflect.DeepEqual(snapshot, h.snapshot) || !reflect.DeepEqual(credential, h.credential) || stateRuntime(st) != stateRuntime(h.state) || st.ProviderScopeID != h.state.ProviderScopeID || st.ProviderBaseURL != h.state.ProviderBaseURL || st.UpstreamModel != h.state.UpstreamModel || st.ConfigVersion != h.state.ConfigVersion || st.CapabilityVersion != h.state.CapabilityVersion || st.EffectivePayloadRef != h.state.EffectivePayloadRef || row.ChannelID != h.task.ChannelID || row.CredentialID != h.task.CredentialID || row.Model != h.task.Model || !row.HoldAmountUSD.Equal(h.task.HoldAmountUSD) {
		t.Fatal("accepted identity, price, FX or credential snapshot changed")
	}
	// Check only this fixture's ledger facts. Other suites deliberately retain
	// unrelated fault rows in ark_test; do not clear or assume a clean database.
	var account ledger.Account
	var journals []ledger.JournalEntry
	var holds []ledger.Hold
	must(t, h.f.s.DB.Where("org_id = ? AND id = ?", h.f.key.OrgID, h.hold.AccountID).Take(&account).Error)
	must(t, h.f.s.DB.Where("account_id = ?", h.hold.AccountID).Find(&journals).Error)
	must(t, h.f.s.DB.Where("account_id = ?", h.hold.AccountID).Find(&holds).Error)
	posted, held := decimal.Zero, decimal.Zero
	for _, journal := range journals {
		posted = posted.Add(journal.Delta)
	}
	for _, hold := range holds {
		if hold.Status == ledger.HoldActive {
			held = held.Add(hold.Amount)
		}
	}
	if !account.PostedBalance.Equal(posted) || !account.HeldAmount.Equal(held) {
		t.Fatal("fixture ledger drift is nonzero")
	}
}

func (h *runtimeHTTPFixture) wire(t *testing.T, method string, status int, body string) {
	t.Helper()
	h.f.response = func(req *http.Request) (*http.Response, error) {
		if req.Method != method || req.URL.Scheme != "https" || req.URL.Host != "ark.cn-beijing.volces.com" || req.Header.Get("Authorization") != "Bearer official-test-credential" {
			return nil, errors.New("unexpected HTTP method, target or credential")
		}
		path := "/api/v3/contents/generations/tasks"
		if method == http.MethodGet {
			path += "/" + h.upstreamID
		}
		if req.URL.Path != path {
			return nil, errors.New("unexpected HTTP task target")
		}
		if method == http.MethodPost {
			got, err := io.ReadAll(req.Body)
			if err != nil || !bytes.Equal(got, h.input) {
				return nil, errors.New("POST did not use the original encrypted execution input")
			}
		}
		return reply(status, body), nil
	}
}

// Fail only this task's application event, after response persistence. This
// is deliberately not a callback on the dedicated append pool.
func (h *runtimeHTTPFixture) failApply(t *testing.T, run func() error) {
	t.Helper()
	name := fmt.Sprintf("test:runtime-http-apply-%d", h.f.key.OrgID)
	injected := errors.New("synthetic raw HTTP application failure")
	fired := 0
	must(t, h.f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "video_events" {
			return
		}
		event, ok := tx.Statement.Dest.(*Event)
		// The claim has its own committed task.submitting event before HTTP.
		// Only the post-response reducers' events are application faults.
		if ok && event.OrgID == h.f.key.OrgID && event.TaskID == h.id && (event.Type == "task.status_changed" || event.Type == "task.submission_unknown") {
			fired++
			tx.AddError(injected)
		}
	}))
	err := run()
	must(t, h.f.s.DB.Callback().Create().Remove(name))
	if fired != 1 || !errors.Is(err, injected) {
		t.Fatal("fault did not reach the actual scoped application transaction")
	}
}

func (h *runtimeHTTPFixture) evidence(t *testing.T, ref, body string) {
	t.Helper()
	var meta invocation.VideoPayload
	must(t, h.f.s.DB.Where("org_id = ? AND payload_id = ?", h.f.key.OrgID, ref).Take(&meta).Error)
	staged := responseInboxCount(t, h.f, &invocation.VideoPayloadStaging{}, "payload_id = ?", ref)
	if meta.Purpose != invocation.VideoPurposeHistory || meta.Direction != invocation.VideoUpstreamResponse || meta.SchemaVersion != 1 {
		t.Fatal("response did not persist as scoped history metadata")
	}
	if !h.history {
		if meta.CaptureStatus != invocation.VideoCaptureDisabled || staged != 0 {
			t.Fatal("disabled history retained raw response bytes")
		}
		return
	}
	if meta.CaptureStatus != invocation.VideoCaptureStaged || staged != 1 {
		t.Fatal("enabled history lost its encrypted response")
	}
	plain, err := h.f.s.Evidence.ReadForHistory(context.Background(), h.f.key.OrgID, ref, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "http-conformance", ReasonCode: "request_debugging"})
	must(t, err)
	var got, expected any
	must(t, json.Unmarshal(plain, &got))
	must(t, json.Unmarshal([]byte(body), &expected))
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("audited encrypted evidence differs from the actual raw HTTP response")
	}
}

func (h *runtimeHTTPFixture) restart(t *testing.T) {
	t.Helper()
	old := h.f.s
	s, err := NewService(old.DB, old.Keyring, old.Secret, old.Log)
	must(t, err)
	must(t, s.ConfigureResponseInbox(os.Getenv("ARK_TEST_MYSQL_DSN"), false))
	t.Cleanup(func() { must(t, s.CloseResponseInbox()) })
	s.Now = func() time.Time { return h.f.now }
	s.Client, s.Gate = old.Client, old.Gate
	h.f.s = s
	h.f.response = func(*http.Request) (*http.Response, error) {
		return nil, errors.New("recovery must not call the provider")
	}
	if s.ResponseInboxEnabled || s.MutationResponseInboxEnabled {
		t.Fatal("fresh consumer unexpectedly enabled producer writes")
	}
}

func (h *runtimeHTTPFixture) reprice(t *testing.T) {
	t.Helper()
	// Activity changes are not credential takeover of this accepted task.
	var cfg ModelConfig
	must(t, h.f.s.DB.Where("public_model = ?", h.task.Model).Take(&cfg).Error)
	var cap ark.Capability
	must(t, json.Unmarshal([]byte(cfg.CapabilityJSON), &cap))
	cap.UpstreamModel, cap.Version, cap.DefaultDuration = "synthetic-replacement-model", "http-replacement-capability", 9
	capJSON, err := json.Marshal(cap)
	must(t, err)
	cipher, err := h.f.s.Keyring.Encrypt("synthetic-replacement-credential")
	must(t, err)
	channel := relay.Channel{Name: "HTTP conformance replacement", Provider: "volcengine", BaseURL: h.state.ProviderBaseURL, CredentialEnc: cipher, Models: h.task.Model, Status: 1, Weight: 1}
	must(t, h.f.s.DB.Create(&channel).Error)
	scope := ProviderScope{ScopeKey: "http-" + h.id, AccountRef: "http-" + h.id, Region: "cn-beijing", ProjectName: "replacement", Enabled: true, FXUSDPerCNY: "0.20", FXVersion: "http-replacement", FXObservedAt: h.f.now}
	must(t, h.f.s.DB.Create(&scope).Error)
	must(t, h.f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", h.task.Model).Updates(map[string]any{"channel_id": channel.ID, "provider_scope_id": scope.ID, "version": "http-replacement-config", "capability_json": string(capJSON), "pricing_version": "http-replacement-price"}).Error)
	must(t, h.f.s.DB.Model(&relay.Channel{}).Where("id = ?", h.task.ChannelID).Update("credential_enc", cipher).Error)
	must(t, h.f.s.DB.Model(&relay.ChannelCredential{}).Where("channel_id = ?", h.task.ChannelID).Update("credential_enc", cipher).Error)
	must(t, h.f.s.DB.Model(&ProviderScope{}).Where("id = ?", h.state.ProviderScopeID).Updates(map[string]any{"fx_usd_per_cny": "0.20", "fx_version": "http-replacement"}).Error)
	must(t, h.f.s.DB.Model(&videobilling.PricingRule{}).Where("rule_id = ?", h.snapshot.RuleID).Updates(map[string]any{"retail_usd_per_1m": 80, "supplier_cny_per_1m": 90}).Error)
	h.pinned(t)
}

func (h *runtimeHTTPFixture) settle(t *testing.T, tokens int64) {
	t.Helper()
	row, st := h.rows(t)
	hold := h.f.hold(t, h.id)
	var entry videobilling.BillingEntry
	must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Take(&entry).Error)
	wantUSD := decimal.NewFromInt(tokens).Mul(decimal.RequireFromString("0.000008"))
	wantCNY := decimal.NewFromInt(tokens).Mul(decimal.RequireFromString("0.000046"))
	if row.Status != "succeeded" || st.BillingState != "settled" || st.ObservedCompletionTokens == nil || *st.ObservedCompletionTokens != tokens || hold.Status != ledger.HoldCaptured || !hold.CapturedAmount.Equal(wantUSD) || entry.SnapshotID != h.snapshot.ID || entry.HoldID != h.hold.ID || !entry.CalculatedUSD.Equal(wantUSD) || !entry.CapturedUSD.Equal(wantUSD) || !entry.SupplierCNY.Equal(wantCNY) || !entry.SupplierUSD.Equal(wantCNY.Mul(decimal.RequireFromString("0.14"))) || !entry.ReleasedUSD.Equal(h.hold.Amount.Sub(wantUSD)) || h.count(t, &videobilling.BillingEntry{}) != 1 || h.count(t, &videobilling.UsageEvidence{}) != 1 {
		t.Fatal("settlement did not retain exact original price/FX and nullable usage semantics")
	}
	var account ledger.Account
	must(t, h.f.s.DB.First(&account, h.hold.AccountID).Error)
	if !account.PostedBalance.Equal(h.account.PostedBalance.Sub(wantUSD)) || !account.HeldAmount.Equal(h.account.HeldAmount.Sub(h.hold.Amount)) {
		t.Fatal("settlement account delta differs from the exact accepted hold and charge")
	}
	h.pinned(t)
	h.quotaFinal(t, tokens, false)
}

func (h *runtimeHTTPFixture) queryWithRecovery(t *testing.T, status int, body string) QueryReceipt {
	t.Helper()
	before := h.funds(t)
	events, outbox := h.count(t, &Event{}), h.count(t, &Outbox{})
	posts, gets := h.f.posts.Load(), h.f.gets.Load()
	_, previous := h.rows(t)
	h.wire(t, http.MethodGet, status, body)
	h.failApply(t, func() error { return h.f.s.pollTask(context.Background(), h.f.key.OrgID, h.id) })
	var receipt QueryReceipt
	must(t, h.f.s.DB.Where("org_id = ? AND task_id = ? AND sequence = ?", h.f.key.OrgID, h.id, previous.QuerySequence+1).Take(&receipt).Error)
	if receipt.HTTPStatus != status || receipt.AppliedAt != nil || receipt.PayloadRef == "" || receipt.ObservationSchemaVersion != 1 || receipt.RuntimeVersion != h.state.RuntimeVersion || receipt.AdapterVersion != h.state.AdapterVersion || receipt.ProtocolContractVersion != h.state.ProtocolContractVersion || h.funds(t) != before || h.count(t, &Event{}) != events || h.count(t, &Outbox{}) != outbox || h.f.posts.Load() != posts || h.f.gets.Load() != gets+1 {
		t.Fatal("query application rollback lost evidence or changed funds/events")
	}
	h.evidence(t, receipt.PayloadRef, body)
	h.restart(t)
	reads := responseInboxCount(t, h.f, &invocation.VideoPayloadAccess{}, "org_id = ?", h.f.key.OrgID)
	must(t, h.f.s.applyQueryReceipt(context.Background(), receipt))
	settled := h.funds(t)
	must(t, h.f.s.applyQueryReceipt(context.Background(), receipt))
	var applied QueryReceipt
	must(t, h.f.s.DB.Where("org_id = ? AND attempt_id = ?", h.f.key.OrgID, receipt.AttemptID).Take(&applied).Error)
	if applied.AppliedAt == nil || h.funds(t) != settled || h.count(t, &Event{}) != events+1 || h.count(t, &Outbox{}) != outbox+1 || h.f.posts.Load() != posts || h.f.gets.Load() != gets+1 || responseInboxCount(t, h.f, &invocation.VideoPayloadAccess{}, "org_id = ?", h.f.key.OrgID) != reads {
		t.Fatal("query replay repeated HTTP, decryption, money or events")
	}
	h.pinned(t)
	return applied
}
