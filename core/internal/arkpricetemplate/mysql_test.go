package arkpricetemplate

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Run serially against the dedicated ark_test database. With an empty database
// this covers initial installation over populated business tables. If setup has
// already applied 0120/0121, it covers installed contents and replay instead;
// it never deletes migration records or resets another package's fixtures.
func TestMySQLTemplateMigrationsInstallAndPreserveBusinessData(t *testing.T) {
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN required for template migration integration")
	}
	connection, parseErr := mysqlDriver.ParseDSN(dsn)
	if parseErr != nil || connection.DBName != "ark_test" || !strings.Contains(dsn, "/ark_test?") {
		t.Fatal("dedicated ark_test database required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open test MySQL: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// migrate.Up holds an advisory lock on a dedicated connection while its
	// transaction executes on another connection.
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })

	base, templates := templateMySQLMigrations(t)
	if _, err := migrate.Up(db, base); err != nil {
		t.Fatalf("apply pre-template formal migrations: %v", err)
	}
	seedTemplateMySQLBusinessRows(t, db)
	tables := []string{
		"channels", "video_provider_scopes", "video_model_configs", "video_pricing_rules",
		"video_quota_policies", "video_quota_buckets", "ledger_accounts", "ledger_journal", "ledger_holds",
	}
	before := map[string]templateMySQLTableSnapshot{}
	for _, table := range tables {
		before[table] = snapshotTemplateMySQLTable(t, db, table)
		if before[table].Count == 0 {
			t.Fatalf("%s has no sentinel rows; preservation assertion would be empty", table)
		}
	}
	// These additional append-only tables need not be populated by this fixture,
	// but any pre-existing rows must also remain byte-for-byte unchanged.
	for _, table := range []string{"ledger_account_shards", "ledger_operations", "ledger_operation_events", "ledger_movements", "video_pricing_publications"} {
		before[table] = snapshotTemplateMySQLTable(t, db, table)
	}

	ran, err := migrate.Up(db, templates)
	if err != nil {
		t.Fatalf("apply formal template migrations: %v", err)
	}
	if len(ran) == 0 {
		t.Log("0120/0121 already installed: validating installed data and replay; initial-install preservation requires a fresh ark_test database")
	} else {
		t.Logf("applied template migrations over populated business tables: %v", ran)
	}
	assertTemplateMySQLBusinessUnchanged(t, db, before)

	var records []Record
	if err := db.Where("version = ?", InitialVersion).Order("model").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	want := map[string]int{ark.Model20: 8, ark.Model25: 6, ark.Model20Fast: 4, ark.Model20Mini: 4}
	if len(records) != len(want) {
		t.Fatalf("installed templates = %d, want 4", len(records))
	}
	total := 0
	for _, record := range records {
		value, err := Decode(record)
		if err != nil {
			t.Fatalf("installed template %s failed digest/evidence validation: %v", record.Model, err)
		}
		count, ok := want[value.Model]
		if !ok || len(value.Cells) != count {
			t.Fatalf("unexpected template model/count: %s/%d", value.Model, len(value.Cells))
		}
		for _, cell := range value.Cells {
			if cell.RiskMultiplier != "4" {
				t.Fatalf("%s/%s risk = %s, want 4", value.Model, cell.Key(), cell.RiskMultiplier)
			}
		}
		total += len(value.Cells)
		delete(want, value.Model)
	}
	if len(want) != 0 || total != 22 {
		t.Fatalf("template coverage: missing models=%d, cells=%d, want 0/22", len(want), total)
	}
	installed := snapshotTemplateMySQLTable(t, db, "video_pricing_templates")
	ran, err = migrate.Up(db, templates)
	if err != nil || len(ran) != 0 {
		t.Fatalf("second template migration must be a no-op: applied=%v err=%v", ran, err)
	}
	assertTemplateMySQLBusinessUnchanged(t, db, before)
	if after := snapshotTemplateMySQLTable(t, db, "video_pricing_templates"); after != installed {
		t.Fatalf("replay changed templates: before=%+v after=%+v", installed, after)
	}
}

func templateMySQLMigrations(t *testing.T) (fstest.MapFS, fstest.MapFS) {
	t.Helper()
	source := os.DirFS("../../../migrations")
	names, err := fs.Glob(source, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, templates := fstest.MapFS{}, fstest.MapFS{}
	for _, name := range names {
		if name >= "0120_" && name != "0120_video_pricing_templates.sql" && name != "0121_seedance_pricing_template_20261019.sql" {
			continue
		}
		raw, err := fs.ReadFile(source, name)
		if err != nil {
			t.Fatal(err)
		}
		if name < "0120_" {
			base[name] = &fstest.MapFile{Data: raw}
		} else {
			templates[name] = &fstest.MapFile{Data: raw}
		}
	}
	if len(base) == 0 || len(templates) != 2 {
		t.Fatalf("formal migration files missing: base=%d templates=%d", len(base), len(templates))
	}
	return base, templates
}

func seedTemplateMySQLBusinessRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	tracked, err := ledger.EnableActivityTracking(db)
	if err != nil {
		t.Fatalf("enable test ledger activity: %v", err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	uid := "template-test-" + hex.EncodeToString(random[:])
	now := time.Now().UTC().Truncate(time.Millisecond)
	var orgID, accountID, channelID, scopeID, policyID uint64
	var createdModels []string
	err = tracked.Transaction(func(tx *gorm.DB) error {
		org := identity.Organization{Slug: uid, Name: "Template migration test", Group: "default", Status: 1}
		if err := tx.Create(&org).Error; err != nil {
			return err
		}
		orgID = org.ID
		account, err := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		accountID = account.ID
		if _, _, err := ledger.Post(tx, ledger.PostInput{IdempotencyKey: uid + ":credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.RequireFromString("123.45"), RefType: "order", RefID: uid, Reason: "migration test fixture"}); err != nil {
			return err
		}
		if _, _, err := ledger.CreateHold(tx, ledger.HoldInput{IdempotencyKey: uid + ":hold", AccountID: account.ID, Amount: decimal.RequireFromString("3.25"), RefType: "task", RefID: uid}); err != nil {
			return err
		}
		// No usable credential or active channel is installed by this test.
		channel := relay.Channel{Name: uid, Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Models: strings.Join([]string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini}, ","), Weight: 1, Status: 0}
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		channelID = channel.ID
		scope := video.ProviderScope{ScopeKey: uid, AccountRef: uid, Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.123456789012", FXVersion: uid, FXObservedAt: now}
		if err := tx.Create(&scope).Error; err != nil {
			return err
		}
		scopeID = scope.ID
		limit := decimal.RequireFromString("456.78")
		policy := videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodDay, Timezone: "UTC", LimitAmount: &limit, Version: uid, Enabled: true}
		if err := tx.Create(&policy).Error; err != nil {
			return err
		}
		policyID = policy.ID
		bucketHash := sha256.Sum256([]byte(uid))
		end := now.Add(24 * time.Hour)
		bucket := videobilling.QuotaBucket{PolicyID: policy.ID, ScopeKey: hex.EncodeToString(bucketHash[:]), Timezone: "UTC", PeriodStart: now, PeriodEnd: &end, LimitAmount: &limit, ReservedAmount: decimal.RequireFromString("1.25"), ConsumedAmount: decimal.RequireFromString("2.5")}
		if err := tx.Create(&bucket).Error; err != nil {
			return err
		}
		for _, model := range []string{ark.Model20, ark.Model25, ark.Model20Fast, ark.Model20Mini} {
			profile, err := arkverification.Load(model)
			if err != nil {
				return err
			}
			price := videobilling.PricingRule{RuleID: uid + "-" + model, Version: uid, PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: model, Mode: "generate", Resolution: "480p", ServiceTier: "default"}, RetailUSDPer1M: decimal.RequireFromString("123.456789012345"), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(3), FormulaVersion: "ark-video-token-v1", SourceHash: profile.EvidenceSHA256, EffectiveFrom: now, Enabled: true}
			if err := tx.Create(&price).Error; err != nil {
				return err
			}
			var existing video.ModelConfig
			if err := tx.Where("public_model = ?", model).Take(&existing).Error; err == nil {
				continue
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			profile.Capability.UpstreamModel = upstream(model)
			capability, err := json.Marshal(profile.Capability)
			if err != nil {
				return err
			}
			bounds, err := json.Marshal(profile.Bounds)
			if err != nil {
				return err
			}
			// 显式列清单：base 迁移只装到 0119（模板安装时代），结构体后续新增的
			// 列（如 0136 的 max_concurrent_tasks/create_rpm）不在该时代的表里，
			// 结构体整字段 INSERT 会撞 Unknown column。种子按 0120 前的表形状写。
			cfg := map[string]any{"public_model": model, "version": uid, "provider_scope_id": scope.ID, "channel_id": channel.ID, "enabled": false, "capability_json": string(capability), "pricing_version": uid, "no_charge_statuses": "failed,cancelled", "max_input_video_duration_ms": profile.MaxInputVideoDurationMS, "bounds_json": string(bounds), "verified_at": now, "updated_at": now}
			if err := tx.Table("video_model_configs").Create(&cfg).Error; err != nil {
				return err
			}
			createdModels = append(createdModels, model)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed isolated business sentinels: %v", err)
	}
	t.Cleanup(func() {
		// This is only fixture teardown in the dedicated test database. Remove
		// the complete synthetic ledger chain in one transaction, child rows
		// first; never change a balance or delete any pre-existing fixture row.
		err := db.Transaction(func(tx *gorm.DB) error {
			if len(createdModels) > 0 {
				if err := tx.Exec("DELETE FROM video_model_configs WHERE public_model IN ? AND version = ? AND provider_scope_id = ?", createdModels, uid, scopeID).Error; err != nil {
					return err
				}
			}
			statements := []struct {
				query string
				args  []any
			}{
				{"DELETE FROM video_pricing_rules WHERE provider_scope_id = ? AND version = ?", []any{scopeID, uid}},
				{"DELETE FROM video_quota_buckets WHERE policy_id = ?", []any{policyID}},
				{"DELETE FROM video_quota_policies WHERE id = ? AND owner_id = ? AND version = ?", []any{policyID, scopeID, uid}},
				{"DELETE FROM video_provider_scopes WHERE id = ? AND scope_key = ?", []any{scopeID, uid}},
				{"DELETE FROM channels WHERE id = ? AND name = ?", []any{channelID, uid}},
				{"DELETE FROM ledger_operation_events WHERE operation_id IN (SELECT id FROM ledger_operations WHERE org_id = ?)", []any{orgID}},
				{"DELETE FROM ledger_movements WHERE account_id = ? AND org_id = ?", []any{accountID, orgID}},
				{"DELETE FROM ledger_operations WHERE org_id = ?", []any{orgID}},
				{"DELETE FROM ledger_journal WHERE account_id = ?", []any{accountID}},
				{"DELETE FROM ledger_holds WHERE account_id = ?", []any{accountID}},
				{"DELETE FROM ledger_account_shards WHERE org_id = ?", []any{orgID}},
				{"DELETE FROM ledger_accounts WHERE id = ? AND org_id = ?", []any{accountID, orgID}},
				{"DELETE FROM organizations WHERE id = ? AND slug = ?", []any{orgID, uid}},
			}
			for _, statement := range statements {
				if err := tx.Exec(statement.query, statement.args...).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("clean up synthetic migration fixtures: %v", err)
		}
	})
}

type templateMySQLTableSnapshot struct {
	Count int
	Hash  string
}

// Table names are fixed test constants. Hash all columns and all rows, sorting
// serialized rows so query order cannot affect the result. Never print row
// contents, which may include another fixture's encrypted credentials.
func snapshotTemplateMySQLTable(t *testing.T, db *gorm.DB, table string) templateMySQLTableSnapshot {
	t.Helper()
	rows, err := db.Raw("SELECT * FROM `" + table + "`").Rows()
	if err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	serialized := []string{}
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		normalized := make([]any, len(columns))
		for i, value := range values {
			if value != nil {
				normalized[i] = hex.EncodeToString(value)
			}
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			t.Fatal(err)
		}
		serialized = append(serialized, string(raw))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(serialized)
	raw, err := json.Marshal([]any{columns, serialized})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return templateMySQLTableSnapshot{Count: len(serialized), Hash: hex.EncodeToString(digest[:])}
}

func assertTemplateMySQLBusinessUnchanged(t *testing.T, db *gorm.DB, before map[string]templateMySQLTableSnapshot) {
	t.Helper()
	for table, old := range before {
		if after := snapshotTemplateMySQLTable(t, db, table); after != old {
			t.Fatalf("template migration changed %s: before=%+v after=%+v", table, old, after)
		}
	}
}
