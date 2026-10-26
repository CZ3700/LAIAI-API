package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type videoCatalogMySQLFixture struct {
	db      *gorm.DB
	host    *Server
	service *video.Service
	key     *identity.APIKey
	plain   string
	at      time.Time
}

// This suite constructs only the two discovery handlers and the video domain.
// No relay startup goroutine or upstream client can obscure transaction probes.
func newVideoCatalogMySQLFixture(t *testing.T) *videoCatalogMySQLFixture {
	t.Helper()
	db := routingAdminMySQLDatabase(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	routingAdminSQL(t, db, "INSERT INTO organizations(id,slug,name,group_name) VALUES(70001,'video-catalog','Synthetic catalog','catalog-vip')")
	routingAdminSQL(t, db, "INSERT INTO group_ratios(name,ratio) VALUES('catalog-vip',0.5)")
	routingAdminSQL(t, db, "INSERT INTO model_group_ratios(group_name,model,ratio) VALUES('catalog-vip','seedance-2.0-mini',0.125)")
	routingAdminSQL(t, db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models) VALUES(70001,'Synthetic official','volcengine','https://ark.cn-beijing.volces.com/api/v3/','synthetic-never-decrypted',''),(70002,'Synthetic text','openai','https://never-send.invalid','synthetic','priced')")
	for _, item := range []any{
		&relay.ModelPrice{Model: "priced", InputUSDPer1M: decimal.NewFromInt(1)},
		&video.ProviderScope{ID: 70001, ScopeKey: "catalog-scope", AccountRef: "synthetic-account", Region: "cn-beijing", ProjectName: "synthetic", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "fixture-fx", FXObservedAt: at},
		&video.Policy{OrgID: 70001, AsyncExecutionAllowed: true, CaptureHistory: false, RetentionDays: 7, UpdatedAt: at},
		&video.AssetScopeConfig{ProviderScopeID: 70001, Version: 1, AccountRef: "synthetic-account", Region: "cn-beijing", ProjectName: "synthetic", CapabilitiesJSON: `{"portrait":true}`, CredentialCipher: "synthetic-never-decrypted", Enabled: true, CreatedBy: 1, CreatedAt: at, UpdatedAt: at},
	} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal("cannot seed isolated catalog fixture", err)
		}
	}
	routingAdminSQL(t, db, "UPDATE video_asset_shared_defaults SET revision=1,provider_scope_id=70001,scope_version=1,enabled=1,allow_async=1,updated_by=1,updated_at=? WHERE id=1", at)
	for i, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
		upstream := []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-5-260628", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615"}[i]
		cap := ark.Capability{PublicModel: model, UpstreamModel: upstream, Version: "cap-v1", Enabled: true, Verified: true, Resolutions: []string{"480p"}, Ratios: []string{"1:1"}, MinDuration: 4, MaxDuration: 5, DefaultDuration: 4, DefaultResolution: "480p", DefaultRatio: "1:1", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4, VideoInput: true, MaxVideos: 1}
		raw, _ := json.Marshal(cap)
		cfg := video.ModelConfig{PublicModel: model, Version: "cfg-v1", ProviderScopeID: 70001, ChannelID: 70001, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "price-v1", BoundsJSON: `{"480p/1:1":{"width":640,"height":640}}`, NoChargeStatuses: "failed,cancelled", MaxInputVideoDurationMS: 15000, VerifiedAt: &at, UpdatedAt: at}
		if err := db.Create(&cfg).Error; err != nil {
			t.Fatal(err)
		}
		set := videobilling.MinimumRuleSet{Model: model, Version: "min-v1", SourceURL: "https://docs.volcengine.com/docs/fixture", EvidenceHash: strings.Repeat("a", 64), ObservedAt: at, VerifiedAt: at, VerifiedBy: 1}
		if err := db.Create(&set).Error; err != nil {
			t.Fatal(err)
		}
		for _, duration := range []int64{4000, 5000} {
			cell := videobilling.MinimumRuleCell{RuleSetID: set.ID, Resolution: "480p", Ratio: "1:1", OutputDurationMS: duration, FPS: 24, MinimumTokens: 67200 + ((duration - 4000) * 16800 / 1000)}
			if err := db.Create(&cell).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, input := range []bool{false, true} {
			amount := int64(8 + i*2)
			if input {
				amount += 2
			}
			rule := videobilling.PricingRule{RuleID: fmt.Sprintf("catalog-%d-%t", i, input), Version: "price-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: 70001, Model: model, Mode: "generate", Resolution: "480p", InputVideo: input, ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(amount), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), MinimumRuleVersion: "min-v1", Enabled: true, EffectiveFrom: at.Add(-time.Hour), FormulaVersion: "ark-video-token-v1", SourceHash: strings.Repeat("a", 64)}
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	plain, key, err := identity.IssueKey(db, identity.IssueKeyInput{OrgID: 70001, Scopes: "video:*"})
	if err != nil {
		t.Fatal(err)
	}
	service := &video.Service{DB: db, Now: func() time.Time { return at }, Assets: &videoasset.Service{}}
	host := &Server{db: db, video: service, now: func() time.Time { return at }}
	return &videoCatalogMySQLFixture{db: db, host: host, service: service, key: key, plain: plain, at: at}
}

func (f *videoCatalogMySQLFixture) catalog(t *testing.T) video.CatalogObservation {
	t.Helper()
	out, err := f.service.Catalog(context.Background(), video.CatalogSubjectRef{OrgID: 70001, KeyID: f.key.ID})
	if err != nil {
		t.Fatal("real catalog failed", err)
	}
	return out
}

func TestMySQLVideoCatalogFourModelsPricesAndNoWrites(t *testing.T) {
	f := newVideoCatalogMySQLFixture(t)
	tables := []string{"api_keys", "channels", "video_model_configs", "video_pricing_rules", "video_org_policies", "ledger_accounts", "ledger_holds", "ledger_journal", "ledger_operations", "ledger_movements", "tasks", "video_operations", "video_billing_snapshots", "video_quota_buckets", "video_quota_reservations"}
	before := routingAdminFingerprints(t, f.db, tables...)
	out := f.catalog(t)
	want := map[string][2]string{ark.Model20: {"4", "5"}, ark.Model25: {"5", "6"}, ark.Model20Fast: {"6", "7"}, ark.Model20Mini: {"0.875", "1"}}
	if len(out.Models) != 4 || !out.CheckedAt.Equal(f.at) {
		t.Fatal("missing fixed canonical models")
	}
	for _, row := range out.Models {
		pair, ok := want[row.ID]
		if !ok || row.Publication.Status != "published" || len(row.Publication.Combinations) != 2 || len(row.Prices) != 2 || !row.Capabilities.PortraitAssets || !row.Actions.Create.Eligible || !row.Actions.Estimate.Eligible || !row.Actions.ModelsRead.Eligible {
			t.Fatalf("wrong canonical observation for %s: %+v", row.ID, row.Actions)
		}
		for _, p := range row.Prices {
			index := 0
			if p.InputVideo {
				index = 1
			}
			if p.Amount != pair[index] || p.Currency != "USD" || p.Unit != "million_video_tokens" {
				t.Fatal("independent price oracle mismatch", row.ID, p.Amount)
			}
		}
		q, err := f.service.EstimateForKey(context.Background(), f.key, catalogEstimateRequest(row.ID))
		// 640*640*24*4/1024 = 38400; this oracle does not call Quote.
		amount := decimal.RequireFromString(pair[0]).Mul(decimal.RequireFromString("0.0384"))
		if err != nil || q.EstimatedTokens != 38400 || !q.HoldUSD.Equal(amount) {
			t.Fatal("estimate disagrees with independently computed catalog amount", row.ID, err)
		}
	}
	ids := catalogHostIDs(t, catalogHostRequest(t, f.host, f.plain, context.Background()))
	if !reflect.DeepEqual(ids, []string{"priced", ark.Model20, ark.Model20Fast, ark.Model20Mini, ark.Model25}) {
		t.Fatal("SDK canonical identity/order mismatch", ids)
	}
	rows, err := f.host.publicPricing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	marketing := map[string]string{}
	for _, row := range rows {
		if p, ok := row["pricing"].(pricingDescriptor); ok && p.BillingKind == "video" {
			for _, cell := range p.Combinations {
				if !cell.InputVideo {
					marketing[row["model"].(string)] = cell.Amount
				}
			}
		}
	}
	if !reflect.DeepEqual(marketing, map[string]string{ark.Model20: "8", ark.Model25: "10", ark.Model20Fast: "12", ark.Model20Mini: "14"}) {
		t.Fatal("marketing must retain unmultiplied base prices", marketing)
	}
	if !reflect.DeepEqual(before, routingAdminFingerprints(t, f.db, tables...)) {
		t.Fatal("directory or estimate wrote financial/configuration facts")
	}
}

func TestMySQLVideoCatalogSDKSnapshotAndReadOnlyBudget(t *testing.T) {
	f := newVideoCatalogMySQLFixture(t)
	var pool gorm.ConnPool
	count := 0
	changed := false
	readonly := false
	name := "catalog_same_snapshot"
	observe := func(q *gorm.DB) {
		if _, ok := q.Statement.ConnPool.(gorm.TxCommitter); !ok {
			return
		}
		count++
		if pool == nil {
			pool = q.Statement.ConnPool
		}
		if pool != q.Statement.ConnPool {
			t.Error("discovery query escaped one transaction")
		}
		if !strings.Contains(q.Statement.SQL.String(), "MAX_EXECUTION_TIME(") {
			t.Error("catalog read omitted MySQL statement deadline")
		}
		if deadline, ok := q.Statement.Context.Deadline(); !ok || time.Until(deadline) > 205*time.Millisecond {
			t.Error("catalog SQL exceeds 200ms budget")
		}
		if !changed && q.Statement.Table == "video_model_configs" {
			changed = true
			_, err := q.Statement.ConnPool.ExecContext(context.Background(), "UPDATE organizations SET name='should-not-write' WHERE id=70001")
			var my *mysqlDriver.MySQLError
			readonly = errors.As(err, &my) && my.Number == 1792
			// A second physical connection commits after this snapshot began.
			if err := f.db.Transaction(func(tx *gorm.DB) error {
				for _, statement := range []string{"UPDATE api_keys SET model_limits='priced' WHERE org_id=70001", "UPDATE video_pricing_rules SET retail_usd_per_1m=99 WHERE provider_scope_id=70001", "UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=70001"} {
					if e := tx.Exec(statement).Error; e != nil {
						return e
					}
				}
				return nil
			}); err != nil {
				t.Error("second-connection change failed")
			}
		}
	}
	if err := f.db.Callback().Query().After("gorm:query").Register(name, observe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(name) })
	if err := f.db.Callback().Row().After("gorm:row").Register(name, observe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Row().Remove(name) })
	w := catalogHostRequest(t, f.host, f.plain, context.Background())
	_ = f.db.Callback().Query().Remove(name)
	_ = f.db.Callback().Row().Remove(name)
	if ids := catalogHostIDs(t, w); len(ids) != 5 || !changed || !readonly || count != 18 {
		t.Fatal("SDK did not retain readonly 18-SELECT snapshot", ids, changed, readonly, count)
	}
	if ids := catalogHostIDs(t, catalogHostRequest(t, f.host, f.plain, context.Background())); !reflect.DeepEqual(ids, []string{"priced"}) {
		t.Fatal("next snapshot did not observe committed restriction", ids)
	}
}

func TestMySQLVideoCatalogPartialRulesActionsAndPortraitIsolation(t *testing.T) {
	f := newVideoCatalogMySQLFixture(t)
	// Removing one reference duration blocks that entire input-video cell.
	routingAdminSQL(t, f.db, "DELETE c FROM video_minimum_rule_cells c JOIN video_minimum_rule_sets s ON s.id=c.rule_set_id WHERE s.model=? AND c.output_duration_ms=5000", ark.Model20)
	routingAdminSQL(t, f.db, "UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=70001")
	routingAdminSQL(t, f.db, "INSERT INTO org_entitlements(org_id,feature_video,feature_images) VALUES(70001,0,1) ON DUPLICATE KEY UPDATE feature_video=0")
	// An explicit disabled route to another scope blocks shared inheritance.
	routingAdminSQL(t, f.db, "INSERT INTO video_asset_org_routes(org_id,provider_scope_id,scope_version,enabled,default_route,updated_at) VALUES(70001,99999,1,0,1,?)", f.at)
	out := f.catalog(t)
	for _, row := range out.Models {
		if row.Actions.Create.Eligible || !row.Actions.Estimate.Eligible || !row.Actions.ModelsRead.Eligible || row.Capabilities.PortraitAssets {
			t.Fatal("execution/portrait authority broadened", row.ID)
		}
		if row.ID == ark.Model20 {
			if row.Publication.Status != "partial" || len(row.Prices) != 1 {
				t.Fatal("incomplete minimum coverage was published")
			}
			for _, cell := range row.Publication.Combinations {
				if cell.InputVideo && (cell.Status != "blocked" || !reflect.DeepEqual(cell.ReasonCodes, []string{"minimum_coverage_missing"})) {
					t.Fatal("wrong minimum coverage diagnosis")
				}
			}
		}
	}
	// Cross-version active overlap blocks only the affected tuple.
	var rule videobilling.PricingRule
	if err := f.db.Where("model=? AND input_video=0", ark.Model20Fast).Take(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rule.ID = 0
	rule.RuleID = "overlap-next"
	rule.Version = "price-next"
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range f.catalog(t).Models {
		if row.ID == ark.Model20Fast && (row.Publication.Status != "partial" || len(row.Prices) != 1 || !row.Prices[0].InputVideo) {
			t.Fatal("cross-version overlap escaped shared validation")
		}
	}
	_, err := f.service.EstimateForKey(context.Background(), f.key, catalogEstimateRequest(ark.Model20Fast))
	if err == nil {
		t.Fatal("estimate accepted a catalog-blocked overlap")
	}
}

func TestMySQLVideoCatalogConfigLimitsAndNoPartialFailure(t *testing.T) {
	f := newVideoCatalogMySQLFixture(t)
	for i := 0; i < 60; i++ {
		routingAdminSQL(t, f.db, "INSERT INTO video_model_configs(public_model,version,provider_scope_id,channel_id,enabled,capability_json,pricing_version,no_charge_statuses,max_input_video_duration_ms,bounds_json,updated_at) VALUES(?,'v1',70001,70001,0,'{}','v1','',0,'{}',?)", fmt.Sprintf("unsupported-fixture-%02d", i), f.at)
	}
	if out := f.catalog(t); len(out.Models) != 64 {
		t.Fatal("64 configuration boundary lost diagnostics")
	}
	routingAdminSQL(t, f.db, "INSERT INTO video_model_configs(public_model,version,provider_scope_id,channel_id,enabled,capability_json,pricing_version,no_charge_statuses,max_input_video_duration_ms,bounds_json,updated_at) VALUES('unsupported-fixture-over','v1',70001,70001,0,'{}','v1','',0,'{}',?)", f.at)
	out, err := f.service.Catalog(context.Background(), video.CatalogSubjectRef{OrgID: 70001, KeyID: f.key.ID})
	if !errors.Is(err, video.ErrCatalogLimit) || out.Models != nil {
		t.Fatal("65 configurations silently truncated", err)
	}
	w := catalogHostRequest(t, f.host, f.plain, context.Background())
	if w.Code != 503 || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatal("SDK returned partial models after directory limit")
	}
	routingAdminSQL(t, f.db, "DELETE FROM video_model_configs WHERE public_model='unsupported-fixture-over'")
	// TEXT's 65535-byte SQL limit prevents a 65537 single-field fixture. The
	// semantic aggregate boundary is still reachable without changing schema.
	payload := `{"padding":"` + strings.Repeat("x", 65000) + `"}`
	routingAdminSQL(t, f.db, "UPDATE video_model_configs SET capability_json=? WHERE public_model LIKE 'unsupported-fixture-%'", payload)
	out, err = f.service.Catalog(context.Background(), video.CatalogSubjectRef{OrgID: 70001, KeyID: f.key.ID})
	if !errors.Is(err, video.ErrCatalogLimit) || out.Models != nil {
		t.Fatal("aggregate byte bound did not fail closed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.service.Catalog(ctx, video.CatalogSubjectRef{OrgID: 70001, KeyID: f.key.ID}); !errors.Is(err, context.Canceled) {
		t.Fatal("catalog lost caller cancellation", err)
	}
	// A bare pool is never accepted by the transaction-only entry point.
	if _, err := f.service.ReadCatalogTx(context.Background(), f.db, video.CatalogSubjectRef{OrgID: 70001, KeyID: f.key.ID}); !errors.Is(err, video.ErrCatalogInvalid) {
		t.Fatal("catalog accepted a non-transactional reader")
	}
}

func catalogEstimateRequest(model string) ark.CreateRequest {
	duration, resolution, ratio := 4, "480p", "1:1"
	return ark.CreateRequest{Model: model, Content: []ark.ContentItem{{Type: "text", Text: "synthetic text"}}, Duration: &duration, Resolution: &resolution, Ratio: &ratio}
}
