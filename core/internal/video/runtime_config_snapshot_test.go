package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
)

// Updating an active channel credential must not change an accepted task's
// credential snapshot. This is configuration isolation, not an administrator
// workflow for verifying and transferring in-flight tasks to a replacement key.
func TestMySQLRuntimeAcceptedConfigurationSurvivesActivePublication(t *testing.T) {
	f := mysqlFixture(t)
	ctx := context.Background()
	v := f.create(t, "runtime-pinned-configuration")
	var accepted State
	var acceptedTask task.Task
	var snapshot videobilling.Snapshot
	var credential CredentialSnapshot
	var config ModelConfig
	var price videobilling.PricingRule
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&accepted).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&acceptedTask).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, accepted.BillingSnapshotID).First(&snapshot).Error)
	must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, accepted.CredentialSnapshotID).First(&credential).Error)
	must(t, f.s.DB.Where("public_model = ?", acceptedTask.Model).First(&config).Error)
	must(t, f.s.DB.Where("rule_id = ?", snapshot.RuleID).First(&price).Error)
	initialHold := f.hold(t, v.ID)
	var initialAccount ledger.Account
	must(t, f.s.DB.First(&initialAccount, initialHold.AccountID).Error)
	execution, err := f.s.Evidence.ReadForExecutionSchema(ctx, f.key.OrgID, accepted.EffectivePayloadRef, 1, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "runtime-config-test", ReasonCode: "dispatch"})
	must(t, err)
	var expectedRequest ark.CreateRequest
	must(t, json.Unmarshal(execution, &expectedRequest))
	if expectedRequest.Model != accepted.UpstreamModel || stateRuntime(accepted) != videoruntime.LegacyV1() || !initialHold.Amount.Equal(snapshot.HoldUSD) {
		t.Fatal("acceptance fixture did not freeze its complete v1 identity")
	}
	snapshotBytes, err := json.Marshal(snapshot)
	must(t, err)
	credentialBytes, err := json.Marshal(credential)
	must(t, err)

	// Publish a different target for new requests and also replace the original
	// channel's active secret. All destinations remain behind the fixture's
	// in-process RoundTripper; this never contacts a real account or model.
	replacementCipher, err := f.s.Keyring.Encrypt("replacement-active-credential")
	must(t, err)
	must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", acceptedTask.ChannelID).Update("credential_enc", replacementCipher).Error)
	must(t, f.s.DB.Model(&relay.ChannelCredential{}).Where("channel_id = ?", acceptedTask.ChannelID).Update("credential_enc", replacementCipher).Error)
	replacementChannel := relay.Channel{Name: "runtime replacement", Provider: "volcengine", BaseURL: accepted.ProviderBaseURL, CredentialEnc: replacementCipher, Models: acceptedTask.Model, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	must(t, f.s.DB.Create(&replacementChannel).Error)
	replacementScope := ProviderScope{ScopeKey: "replacement-" + v.ID, AccountRef: "replacement-account", Region: "cn-beijing", ProjectName: "replacement-project", Enabled: true, FXUSDPerCNY: "0.20", FXVersion: "replacement-fx", FXObservedAt: f.now}
	must(t, f.s.DB.Create(&replacementScope).Error)
	must(t, f.s.DB.Model(&ProviderScope{}).Where("id = ?", accepted.ProviderScopeID).Updates(map[string]any{"fx_usd_per_cny": "0.20", "fx_version": "updated-active-fx", "fx_observed_at": f.now.Add(time.Second)}).Error)
	price.ID = 0
	price.RuleID = "replacement-" + v.ID
	price.Version = "replacement-price"
	price.ProviderScopeID = replacementScope.ID
	price.RetailUSDPer1M = decimal.NewFromInt(80)
	price.SupplierCNYPer1M = decimal.NewFromInt(90)
	must(t, f.s.DB.Create(&price).Error)
	// Even an accidental edit of the old live price must not reprice the task.
	must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("rule_id = ?", snapshot.RuleID).Updates(map[string]any{"retail_usd_per_1m": 80, "supplier_cny_per_1m": 90}).Error)
	var capability ark.Capability
	must(t, json.Unmarshal([]byte(config.CapabilityJSON), &capability))
	capability.UpstreamModel = "doubao-seedance-2-0-261001" // Synthetic deployment, never an official capability claim.
	capability.Version = "replacement-capability"
	capability.DefaultDuration = 9
	capabilityJSON, err := json.Marshal(capability)
	must(t, err)
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", config.PublicModel).Updates(map[string]any{"version": "replacement-config", "channel_id": replacementChannel.ID, "provider_scope_id": replacementScope.ID, "capability_json": string(capabilityJSON), "pricing_version": price.Version}).Error)

	assertPinned := func() {
		t.Helper()
		var current State
		var currentTask task.Task
		var currentSnapshot videobilling.Snapshot
		var currentCredential CredentialSnapshot
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&current).Error)
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&currentTask).Error)
		must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, current.BillingSnapshotID).First(&currentSnapshot).Error)
		must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, current.CredentialSnapshotID).First(&currentCredential).Error)
		storedSnapshot, e := json.Marshal(currentSnapshot)
		must(t, e)
		storedCredential, e := json.Marshal(currentCredential)
		must(t, e)
		if current.ProviderScopeID != accepted.ProviderScopeID || current.ProviderBaseURL != accepted.ProviderBaseURL || current.UpstreamModel != accepted.UpstreamModel || current.ConfigVersion != accepted.ConfigVersion || current.CapabilityVersion != accepted.CapabilityVersion || current.EffectivePayloadRef != accepted.EffectivePayloadRef || stateRuntime(current) != stateRuntime(accepted) || currentTask.Model != acceptedTask.Model || currentTask.ChannelID != acceptedTask.ChannelID || currentTask.CredentialID != acceptedTask.CredentialID || !currentTask.HoldAmountUSD.Equal(acceptedTask.HoldAmountUSD) || !bytes.Equal(storedSnapshot, snapshotBytes) || !bytes.Equal(storedCredential, credentialBytes) {
			t.Fatal("active publication changed accepted model, scope, input, credential, contract, price, FX or freeze")
		}
		if h := f.hold(t, v.ID); h.ID != initialHold.ID || !h.Amount.Equal(initialHold.Amount) {
			t.Fatal("active publication replaced or resized the original hold")
		}
	}
	assertPinned()
	upstreamID := "up_" + v.ID
	f.response = func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer official-test-credential" || req.URL.Scheme+"://"+req.URL.Host != "https://ark.cn-beijing.volces.com" {
			return nil, errors.New("dispatch/query changed the accepted credential or destination")
		}
		switch req.Method {
		case http.MethodPost:
			body, e := io.ReadAll(req.Body)
			if e != nil {
				return nil, e
			}
			if !bytes.Equal(body, execution) {
				return nil, errors.New("dispatch reconstructed the original request from active configuration")
			}
			return reply(200, fmt.Sprintf(`{"id":%q}`, upstreamID)), nil
		case http.MethodGet:
			if req.URL.Path != "/api/v3/contents/generations/tasks/"+upstreamID {
				return nil, errors.New("query changed the accepted upstream task")
			}
			return reply(200, fmt.Sprintf(`{"id":%q,"model":%q,"status":"succeeded","usage":{"completion_tokens":100}}`, upstreamID, accepted.UpstreamModel)), nil
		default:
			return nil, errors.New("unexpected provider method")
		}
	}
	f.dispatch(t, v.ID)
	assertPinned()
	must(t, f.s.pollTask(ctx, f.key.OrgID, v.ID))
	assertPinned()
	var entry videobilling.BillingEntry
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&entry).Error)
	if entry.SnapshotID != snapshot.ID || entry.HoldID != initialHold.ID || !entry.CalculatedUSD.Equal(decimal.RequireFromString("0.0008")) || !entry.CapturedUSD.Equal(decimal.RequireFromString("0.0008")) || !entry.SupplierCNY.Equal(decimal.RequireFromString("0.0046")) || !entry.SupplierUSD.Equal(decimal.RequireFromString("0.000644")) || !entry.ReleasedUSD.Equal(initialHold.Amount.Sub(entry.CapturedUSD)) {
		t.Fatal("settlement used current pricing/FX instead of the acceptance snapshot")
	}
	var currentAccount ledger.Account
	must(t, f.s.DB.First(&currentAccount, initialHold.AccountID).Error)
	if !currentAccount.PostedBalance.Equal(initialAccount.PostedBalance.Sub(entry.CapturedUSD)) || !currentAccount.HeldAmount.Equal(initialAccount.HeldAmount.Sub(initialHold.Amount)) || f.hold(t, v.ID).Status != ledger.HoldCaptured || f.posts.Load() != 1 || f.gets.Load() != 1 {
		t.Fatal("fixed-configuration task did not settle exactly once")
	}
	var rates []ProviderRate
	must(t, f.s.DB.Where("scope_id IN ?", []uint64{accepted.ProviderScopeID, replacementScope.ID}).Find(&rates).Error)
	if len(rates) != 2 {
		t.Fatal("create/query did not use the original provider scope gates")
	}
	for _, rate := range rates {
		if rate.ScopeID != accepted.ProviderScopeID {
			t.Fatal("accepted task consumed the replacement provider scope")
		}
	}
	var receipt QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
	must(t, requireQueryReceiptRuntime(accepted, receipt))
	must(t, f.s.applyQueryReceipt(ctx, receipt))
	assertPinned()
	var settlements int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&settlements).Error)
	if settlements != 1 || f.posts.Load() != 1 || f.gets.Load() != 1 {
		t.Fatal("receipt replay reselected active configuration or repeated settlement")
	}
}
