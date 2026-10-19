package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func assetTestActor() *identity.User {
	return &identity.User{ID: 77, Role: identity.PlatRoleSuperadmin, Status: identity.StatusActive}
}
func assetTestSetup(t *testing.T, f *fixture) (ProviderScope, ark.Capability) {
	t.Helper()
	must(t, f.s.ConfigureAssets(AssetIntegrationOptions{PlatformCallbackURL: "https://gateway.example/v1/video/portrait-callback"}))
	var cfg ModelConfig
	must(t, f.s.DB.First(&cfg, "public_model = ?", ark.Model20).Error)
	var scope ProviderScope
	must(t, f.s.DB.First(&scope, cfg.ProviderScopeID).Error)
	must(t, f.s.RegisterAssetScope(context.Background(), assetTestActor(), AssetScopeRegistration{ProviderScopeID: scope.ID, Version: 1, Enabled: true, Capabilities: videoasset.Capabilities{VirtualGroups: true, Images: true, Videos: true, Audio: true, Portrait: true}, AccessKey: "fixture-access-key", SecretKey: "fixture-secret-key"}))
	must(t, f.s.SetOrgAssetRoute(context.Background(), assetTestActor(), AssetOrgRoute{OrgID: f.key.OrgID, ProviderScopeID: scope.ID, ScopeVersion: 1, Enabled: true, DefaultRoute: true}))
	var cap ark.Capability
	must(t, json.Unmarshal([]byte(cfg.CapabilityJSON), &cap))
	cap.ImageInput = true
	cap.VideoInput = true
	cap.AudioInput = true
	cap.MaxImages = 4
	cap.MaxVideos = 3
	cap.MaxAudios = 3
	cap.MaxContentItems = 6
	raw, _ := json.Marshal(cap)
	must(t, f.s.DB.Model(&cfg).Updates(map[string]any{"capability_json": string(raw), "max_input_video_duration_ms": 15000}).Error)
	return scope, cap
}
func assetTestMedia(f *fixture) AssetMediaRegistration {
	id, _ := randomID("media_")
	return AssetMediaRegistration{ID: id, OrgID: f.key.OrgID, ClientURL: "https://client.example/" + id + "?proof=private-client", FetchURL: "https://immutable.example/" + id + "?signature=private-fetch", MediaType: "video", ImmutableObjectRef: "object_" + id, ObjectSHA256: strings.Repeat("a", 64), MIME: "video/mp4", ByteSize: 12000, DurationMS: 4000, Width: 1280, Height: 720, FPSNum: 24, FPSDen: 1, Version: 1, VerifiedAt: f.now, ExpiresAt: f.now.Add(30 * time.Minute), EvidenceRef: "evidence_" + id}
}

func TestAssetRegistryValidationAndEnvelope(t *testing.T) {
	now := time.Now().UTC()
	f := &fixture{key: &identity.APIKey{OrgID: 1}, now: now}
	input := assetTestMedia(f)
	if !validRegisteredMedia(input, now) {
		t.Fatal("valid immutable media rejected")
	}
	bad := input
	bad.ExpiresAt = now.Add(2 * time.Hour)
	if validRegisteredMedia(bad, now) {
		t.Fatal("media freshness lifetime bypassed")
	}
	bad = input
	bad.FetchURL = "https://user:password@media.example/clip"
	if validRegisteredMedia(bad, now) {
		t.Fatal("URL credentials accepted")
	}
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	must(t, err)
	s := &Service{Keyring: kr}
	sealed, err := s.assetSeal("media", 1, "record1", 1, input)
	must(t, err)
	var got AssetMediaRegistration
	must(t, s.assetOpen("media", 1, "record1", 1, sealed, &got))
	for _, test := range []struct {
		domain  string
		org     uint64
		id      string
		version uint64
	}{{"callback", 1, "record1", 1}, {"media", 2, "record1", 1}, {"media", 1, "record2", 1}, {"media", 1, "record1", 2}} {
		if s.assetOpen(test.domain, test.org, test.id, test.version, sealed, &got) == nil {
			t.Fatal("envelope binding bypassed")
		}
	}
	for _, actor := range []*identity.User{nil, {ID: 77, Role: identity.PlatRoleAdmin, Status: identity.StatusActive}, {ID: 0, Role: identity.PlatRoleSuperadmin, Status: identity.StatusActive}, {ID: 77, Role: identity.PlatRoleSuperadmin, Status: identity.StatusDisabled}} {
		if assetAdmin(actor) == nil {
			t.Fatal("non-superadmin registration accepted")
		}
	}
}

func TestMySQLAssetRegistryScopeMediaAndCallback(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	ctx := context.Background()
	runtime, err := f.s.resolveAssetRuntime(ctx, f.key.OrgID, videoasset.Scope{})
	must(t, err)
	if runtime.Scope.ID != assetScopeID(scope.ID) || runtime.Credentials.SecretKey != "fixture-secret-key" {
		t.Fatal("scope did not resolve encrypted credentials")
	}
	wrong := runtime.Scope
	wrong.Project = "other"
	if _, err = f.s.resolveAssetRuntime(ctx, f.key.OrgID, wrong); err == nil {
		t.Fatal("cross-project scope accepted")
	}
	if _, err = f.s.resolveAssetRuntime(ctx, f.key.OrgID+1000000, videoasset.Scope{}); err == nil {
		t.Fatal("scope granted to another tenant")
	}
	media := assetTestMedia(f)
	must(t, f.s.RegisterAssetMedia(ctx, assetTestActor(), media))
	if err = f.s.RegisterAssetMedia(ctx, assetTestActor(), media); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable overwrite: %v", err)
	}
	verified, err := f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video")
	must(t, err)
	if verified.FetchURL != media.FetchURL || verified.DurationMS != 4000 {
		t.Fatal("registered metadata lost")
	}
	for _, raw := range []string{media.ClientURL + "&changed=1", strings.Replace(media.ClientURL, "https://", "https://CLIENT.example/", 1)} {
		if _, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, raw, "video"); err == nil {
			t.Fatal("non-exact URL accepted")
		}
	}
	if _, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID+1000000, media.ClientURL, "video"); err == nil {
		t.Fatal("media crossed tenant")
	}
	var row AssetMediaRegistry
	must(t, f.s.DB.First(&row, "id = ?", media.ID).Error)
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "private-") || strings.Contains(row.ValueCipher, "private-") {
		t.Fatal("plaintext URL retained in metadata")
	}
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+",next:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("n", 32))), "next")
	must(t, err)
	f.s.Keyring = kr
	_, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video")
	must(t, err)
	cb := AssetCallbackRegistration{ID: "cb_" + media.ID, OrgID: f.key.OrgID, URL: "https://client.example/return?private=destination", ExpiresAt: f.now.Add(time.Hour), EvidenceRef: media.EvidenceRef}
	must(t, f.s.RegisterAssetCallback(ctx, assetTestActor(), cb))
	dest, err := f.s.resolveAssetCallbackURL(ctx, f.key.OrgID, cb.URL)
	must(t, err)
	if dest.ID != cb.ID {
		t.Fatal("callback ID lost")
	}
	dest, err = f.s.resolveAssetCallbackID(ctx, f.key.OrgID, cb.ID)
	must(t, err)
	if dest.URL != cb.URL {
		t.Fatal("callback destination changed")
	}
	if _, err = f.s.resolveAssetCallbackID(ctx, f.key.OrgID+1000000, cb.ID); err == nil {
		t.Fatal("callback crossed tenant")
	}
	must(t, f.s.RevokeAssetCallback(ctx, assetTestActor(), f.key.OrgID, cb.ID))
	if _, err = f.s.resolveAssetCallbackID(ctx, f.key.OrgID, cb.ID); err == nil {
		t.Fatal("revoked callback accepted")
	}
	must(t, f.s.RevokeAssetMedia(ctx, assetTestActor(), f.key.OrgID, media.ID))
	if _, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video"); err == nil {
		t.Fatal("revoked media accepted")
	}
	must(t, f.s.DB.First(&row, "id = ?", media.ID).Error)
	if row.ValueCipher != "" {
		t.Fatal("revoked media ciphertext not destroyed")
	}
	media = assetTestMedia(f)
	must(t, f.s.RegisterAssetMedia(ctx, assetTestActor(), media))
	f.now = f.now.Add(time.Hour)
	if _, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video"); err == nil {
		t.Fatal("expired registration accepted")
	}
}

func TestMySQLAssetAuditFailureAndMetadataTamper(t *testing.T) {
	f := mysqlFixture(t)
	assetTestSetup(t, f)
	ctx := context.Background()
	media := assetTestMedia(f)
	must(t, f.s.RegisterAssetMedia(ctx, assetTestActor(), media))
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register("fail_asset_audit", func(tx *gorm.DB) {
		if tx.Statement.Table == "video_asset_events" {
			tx.AddError(errors.New("audit unavailable"))
		}
	}))
	got, err := f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video")
	if err == nil || got.FetchURL != "" {
		t.Fatal("plaintext escaped failed audit")
	}
	cb := AssetCallbackRegistration{ID: "cb_" + media.ID, OrgID: f.key.OrgID, URL: "https://example.com/return", ExpiresAt: f.now.Add(time.Hour), EvidenceRef: media.EvidenceRef}
	if f.s.RegisterAssetCallback(ctx, assetTestActor(), cb) == nil {
		t.Fatal("registration survived audit failure")
	}
	var count int64
	must(t, f.s.DB.Model(&AssetCallbackRegistry{}).Where("id = ?", cb.ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("registration audit rollback was not atomic")
	}
	must(t, f.s.DB.Callback().Create().Remove("fail_asset_audit"))
	must(t, f.s.DB.Model(&AssetMediaRegistry{}).Where("id = ?", media.ID).Update("duration_ms", 100).Error)
	if got, err = f.s.verifyRegisteredAssetMedia(ctx, f.key.OrgID, media.ClientURL, "video"); err == nil || got.FetchURL != "" {
		t.Fatal("unsigned duration override accepted")
	}
}

func assetTestSeed(t *testing.T, f *fixture, provider ProviderScope, kind string) (videoasset.Group, videoasset.Asset) {
	t.Helper()
	groupID, _ := randomID("ag_")
	assetID, _ := randomID("asset_")
	upGroup := "up_" + groupID
	upAsset := "up_" + assetID
	g := videoasset.Group{ID: groupID, OrgID: f.key.OrgID, Kind: kind, Name: "fixture", ScopeID: assetScopeID(provider.ID), ScopeVersion: 1, AccountID: provider.AccountRef, Region: provider.Region, Project: provider.ProjectName, ProviderID: &upGroup, Status: videoasset.StatusActive, ReviewState: videoasset.ReviewClear, RowVersion: 1, CreatedAt: f.now, UpdatedAt: f.now}
	must(t, f.s.DB.Create(&g).Error)
	a := videoasset.Asset{ID: assetID, OrgID: f.key.OrgID, GroupID: g.ID, MediaType: "image", Name: "fixture", ScopeID: g.ScopeID, ScopeVersion: 1, ProviderID: &upAsset, Status: videoasset.StatusActive, ReviewState: videoasset.ReviewClear, ImmutableObjectRef: "object1", ObjectSHA256: strings.Repeat("a", 64), MIME: "image/png", ByteSize: 1234, Width: 1280, Height: 720, MetadataVersion: 1, VerifiedAt: f.now, RowVersion: 1, CreatedAt: f.now, UpdatedAt: f.now}
	must(t, f.s.DB.Create(&a).Error)
	return g, a
}

func TestMySQLAssetTaskCreationBindingsAndRollback(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	_, asset := assetTestSeed(t, f, scope, videoasset.KindVirtual)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", Role: "reference_image", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
	_, err := f.s.Estimate(f.key.OrgID, in)
	must(t, err)
	var count int64
	must(t, f.s.DB.Model(&videoasset.TaskReference{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 0 {
		t.Fatal("estimate wrote task refs")
	}
	v, _, err := f.s.Create(context.Background(), f.key, f.requestID("request_asset"), "idempotent-asset-create", in)
	must(t, err)
	var ref videoasset.TaskReference
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&ref).Error)
	if ref.AssetID != asset.ID || ref.Role != "reference_image" {
		t.Fatal("asset ref not locked")
	}
	var metadata TaskParameters
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&metadata).Error)
	if metadata.ProviderScopeVersion != 1 || strings.Contains(metadata.ParametersJSON, "asset://") {
		t.Fatal("bad whitelist snapshot")
	}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error { return f.s.ValidateTaskAssetsTx(tx, f.key.OrgID, v.ID) }))
	must(t, f.s.SetAssetScopeEnabled(context.Background(), assetTestActor(), scope.ID, 1, false))
	if f.s.DB.Transaction(func(tx *gorm.DB) error { return f.s.ValidateTaskAssetsTx(tx, f.key.OrgID, v.ID) }) == nil {
		t.Fatal("disabled scope accepted at dispatch")
	}
	must(t, f.s.SetAssetScopeEnabled(context.Background(), assetTestActor(), scope.ID, 1, true))
	must(t, f.s.DB.Callback().Create().Before("gorm:create").Register("fail_video_event", func(tx *gorm.DB) {
		if tx.Statement.Table == "video_events" {
			tx.AddError(errors.New("event unavailable"))
		}
	}))
	if _, _, err = f.s.Create(context.Background(), f.key, f.requestID("request_asset_failed"), "idempotent-asset-failed", in); err == nil {
		t.Fatal("create accepted failed event")
	}
	must(t, f.s.DB.Callback().Create().Remove("fail_video_event"))
	must(t, f.s.DB.Model(&videoasset.TaskReference{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatal("failed Create retained refs")
	}
	must(t, f.s.DB.Model(&TaskParameters{}).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
	if count != 1 {
		t.Fatal("failed Create retained metadata")
	}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error { return f.s.afterAssetsTerminalTx(tx, f.key.OrgID, v.ID) }))
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&ref).Error)
	if ref.ReleasedAt == nil {
		t.Fatal("terminal refs not released")
	}
	must(t, f.s.DB.Model(&videoasset.Asset{}).Where("asset_id = ?", asset.ID).Update("status", videoasset.StatusProcessing).Error)
	if _, err = f.s.Estimate(f.key.OrgID, in); err == nil {
		t.Fatal("processing asset authorized")
	}
}

func TestMySQLDraftMetadataAndLicenseRevalidation(t *testing.T) {
	f := mysqlFixture(t)
	scope, cap := assetTestSetup(t, f)
	group, asset := assetTestSeed(t, f, scope, videoasset.KindReal)
	session, _ := randomID("ps_")
	validFrom := f.now.Add(-time.Hour)
	validUntil := f.now.Add(24 * time.Hour)
	reverify := f.now.Add(time.Hour)
	portrait := videoasset.Portrait{ID: session, OrgID: f.key.OrgID, ScopeID: group.ScopeID, ScopeVersion: 1, AccountID: scope.AccountRef, Region: scope.Region, Project: scope.ProjectName, SessionStatus: "succeeded", LicenseStatus: "verified", GroupID: &group.ID, ValidFrom: &validFrom, ValidUntil: &validUntil, LicenseScope: "video_generation", LicenseVersion: 1, LicenseEvidenceRef: "reviewed_evidence", LastVerifiedAt: &f.now, ReverifyDueAt: &reverify, CreatedAt: f.now, UpdatedAt: f.now}
	must(t, f.s.DB.Create(&portrait).Error)
	must(t, f.s.DB.Model(&videoasset.Group{}).Where("group_id = ?", group.ID).Update("portrait_id", session).Error)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "image_url", Role: "reference_image", ImageURL: &ark.MediaReference{URL: "asset://" + asset.ID}}}}
	parent, _, err := f.s.Create(context.Background(), f.key, f.requestID("request_draft_parent"), "idempotent-draft-parent", in)
	must(t, err)
	cap.PublicModel = ark.Model25
	cap.UpstreamModel = "seedance25-fixture"
	cap.Draft = true
	cap.DraftConversion = true
	cap.Resolutions = []string{"720p", "1080p"}
	must(t, f.s.DB.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, parent.ID).Updates(map[string]any{"model": ark.Model25, "status": "succeeded", "upstream_task_id": "official_draft_fixture"}).Error)
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, parent.ID).Update("upstream_model", cap.UpstreamModel).Error)
	var row TaskParameters
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, parent.ID).First(&row).Error)
	var params ark.EffectiveParameters
	must(t, json.Unmarshal([]byte(row.ParametersJSON), &params))
	params.Draft = true
	params.Duration = 8
	data, _ := json.Marshal(params)
	must(t, f.s.DB.Model(&TaskParameters{}).Where("task_id = ?", parent.ID).Updates(map[string]any{"public_model": ark.Model25, "upstream_model": cap.UpstreamModel, "parameters_json": string(data)}).Error)
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error { return f.s.afterAssetsTerminalTx(tx, f.key.OrgID, parent.ID) }))
	resolution := "1080p"
	// Draft inheritance must survive execution-body cryptographic expiration.
	must(t, f.s.DB.Exec("UPDATE invocation_payloads SET wrapped_key = '' WHERE org_id = ?", f.key.OrgID).Error)
	child := ark.CreateRequest{Model: ark.Model25, Resolution: &resolution, Content: []ark.ContentItem{{Type: "draft_task", DraftTask: &ark.DraftReference{ID: parent.ID}}}}
	resolve := func(org uint64) error {
		return f.s.DB.Transaction(func(tx *gorm.DB) error {
			effective, b, r, err := f.s.taskBindingsTx(tx, org, child, scope, cap)
			if err != nil {
				return err
			}
			n, err := ark.Normalize(effective, cap, b)
			if err != nil {
				return err
			}
			if n.Parameters.Duration != 8 || len(r.References) != 1 || r.References[0].AssetID != asset.ID {
				t.Fatal("Draft parameters/references not inherited")
			}
			return nil
		})
	}
	must(t, resolve(f.key.OrgID))
	if resolve(f.key.OrgID+1000000) == nil {
		t.Fatal("cross-tenant Draft accepted")
	}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		return f.s.Assets.RevokeLicenseTx(tx, f.key.OrgID, session, "revoke_evidence", "reviewer")
	}))
	if resolve(f.key.OrgID) == nil {
		t.Fatal("Draft bypassed revoked parent material license")
	}
	must(t, f.s.DB.Model(&State{}).Where("task_id = ?", parent.ID).Update("deleted_at", f.now).Error)
	if resolve(f.key.OrgID) == nil {
		t.Fatal("deleted parent accepted")
	}
}

func TestMySQLRawVideoUsesAggregateUpperBound(t *testing.T) {
	f := mysqlFixture(t)
	scope, _ := assetTestSetup(t, f)
	var rule videobilling.PricingRule
	must(t, f.s.DB.Where("provider_scope_id = ?", scope.ID).First(&rule).Error)
	rule.ID = 0
	rule.RuleID, _ = randomID("")
	rule.InputVideo = true
	rule.MinimumRuleVersion = "fixture-" + rule.RuleID
	must(t, f.s.DB.Create(&rule).Error)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "video_url", VideoURL: &ark.MediaReference{URL: "https://public.example/one.mp4"}}, {Type: "video_url", VideoURL: &ark.MediaReference{URL: "https://public.example/two.mp4"}}}}
	if _, err := f.s.Estimate(f.key.OrgID, in); !errors.Is(err, videobilling.ErrPricingUnavailable) {
		t.Fatalf("missing versioned minimum evidence must reject admission: %v", err)
	}
	must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := videobilling.PublishMinimumTx(tx, videobilling.MinimumRuleSet{Model: ark.Model20, Version: rule.MinimumRuleVersion, SourceURL: "https://docs.volcengine.com/fixture-only", EvidenceHash: strings.Repeat("a", 64), ObservedAt: f.now, VerifiedAt: f.now, VerifiedBy: 77}, []videobilling.MinimumRuleCell{{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 1000000}})
		return err
	}))
	quote, err := f.s.Estimate(f.key.OrgID, in)
	must(t, err)
	if quote.Metering.InputDurationMS != 15000 || quote.Metering.InputDurationSource != "configured_upper_bound" || quote.Metering.InputVideoCount == nil || *quote.Metering.InputVideoCount != 2 || quote.EstimatedTokens != 1000000 || quote.Metering.MinimumTokens != 1000000 {
		t.Fatal("unknown raw inputs did not use aggregate bound")
	}
	// The published scalar is zero: creation must reselect the same verified
	// minimum as Estimate, instead of conflicting with the geometric estimate.
	created, replay, err := f.s.Create(context.Background(), f.key, f.requestID("minimum-create"), "minimum-create-key", in)
	must(t, err)
	if replay {
		t.Fatal("first minimum reservation was treated as a replay")
	}
	var snap videobilling.Snapshot
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, created.ID).First(&snap).Error)
	var meter videobilling.MeteringInput
	must(t, json.Unmarshal([]byte(snap.MeteringJSON), &meter))
	if snap.EstimatedTokens != quote.EstimatedTokens || !snap.HoldUSD.Equal(quote.HoldUSD) || !snap.SupplierReservedCNY.Equal(quote.SupplierReservedCNY) || meter.MinimumRuleSetID != quote.Metering.MinimumRuleSetID || meter.MinimumEvidenceHash != quote.Metering.MinimumEvidenceHash || meter.MinimumTokens != quote.Metering.MinimumTokens || meter.Ratio != "16:9" || meter.OutputDurationMS != 5000 {
		t.Fatal("accepted snapshot disagrees with minimum estimate")
	}
	if h := f.hold(t, created.ID); h.Status != ledger.HoldActive || !h.Amount.Equal(quote.HoldUSD) {
		t.Fatal("ledger hold disagrees with minimum estimate")
	}
	posted, held, err := ledger.OrgBalances(f.s.DB, f.key.OrgID, ledger.AccountUserCredit)
	must(t, err)
	if posted.String() != "100" || !held.Equal(quote.HoldUSD) {
		t.Fatal("minimum reservation changed posted funds or held the wrong amount")
	}
	again, replay, err := f.s.Create(context.Background(), f.key, f.requestID("minimum-create-replay"), "minimum-create-key", in)
	must(t, err)
	var snapshots int64
	must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ?", f.key.OrgID).Count(&snapshots).Error)
	if !replay || again.ID != created.ID || snapshots != 1 || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("replay duplicated admission or unexpectedly contacted upstream")
	}
	postedAgain, heldAgain, err := ledger.OrgBalances(f.s.DB, f.key.OrgID, ledger.AccountUserCredit)
	must(t, err)
	if !postedAgain.Equal(posted) || !heldAgain.Equal(held) {
		t.Fatal("replay changed ledger balances")
	}
	media := assetTestMedia(f)
	must(t, f.s.RegisterAssetMedia(context.Background(), assetTestActor(), media))
	in.Content = []ark.ContentItem{{Type: "video_url", VideoURL: &ark.MediaReference{URL: media.ClientURL}}}
	quote, err = f.s.Estimate(f.key.OrgID, in)
	must(t, err)
	if quote.Metering.InputDurationMS != 4000 || quote.Metering.InputDurationSource != "verified_media" || quote.Metering.InputVideoCount == nil || *quote.Metering.InputVideoCount != 1 {
		t.Fatal("registered input did not use measured duration")
	}
	must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", ark.Model20).Update("max_input_video_duration_ms", 0).Error)
	in.Content[0].VideoURL.URL = "https://public.example/unknown.mp4"
	if _, err = f.s.Estimate(f.key.OrgID, in); err == nil {
		t.Fatal("unknown-duration raw video accepted without verified bound")
	}
}
