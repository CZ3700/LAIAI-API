package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

// Uses the existing guarded ark_test-only allocator and tracked migrations.
// These readers do not construct a Relay/HTTP client or hold official secrets.
// SQLite projection tests are separate; these cases require actual MySQL.
func videoCatalogReaderMySQLFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db := routingAdminMySQLDatabase(t)
	routingAdminSQL(t, db, "INSERT INTO organizations(id,slug,name,group_name) VALUES(89001,'catalog-reader','Catalog reader','catalog-vip')")
	for _, row := range []struct {
		id, owner                  int
		provider, endpoint, legacy string
	}{{89001, 0, "volcengine", "https://ark.cn-beijing.volces.com/api/v3", "synthetic-legacy"}, {89002, 0, "modelark", "https://ark.cn-beijing.volces.com/api/v3///", "must-not-revive"}, {89003, 89001, "volcengine", "https://ark.cn-beijing.volces.com/api/v3", ""}, {89004, 0, "volcengine", "https://never-send.invalid", ""}} {
		routingAdminSQL(t, db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models,owner_org_id) VALUES(?,? ,?,?,?,?,?)", row.id, fmt.Sprintf("Reader %d", row.id), row.provider, row.endpoint, row.legacy, "", row.owner)
	}
	routingAdminSQL(t, db, "INSERT INTO channel_credentials(channel_id,credential_enc,status) VALUES(89002,'disabled-synthetic',2),(89002,'',1),(89003,'active-synthetic',1)")
	routingAdminSQL(t, db, "INSERT INTO group_ratios(name,ratio) VALUES('catalog-vip',0.1250)")
	routingAdminSQL(t, db, "INSERT INTO model_group_ratios(group_name,model,ratio) VALUES('catalog-vip','seedance-2.0-mini',1.234567),('catalog-vip','seedance-2.0-fast',-2)")
	return db
}

type videoCatalogReaderStatement struct {
	sql  string
	args []any
}

func TestMySQLVideoCatalogReadersFactsAndPlans(t *testing.T) {
	db := videoCatalogReaderMySQLFixture(t)
	// Dispersed unrelated rows make PK/credential/ratio index assertions useful;
	// tiny four-row tables could legitimately choose a full scan.
	for start := 0; start < 512; start += 64 {
		channels, credentials, groups, ratios := []string{}, []string{}, []string{}, []string{}
		var ca, ka, ga, ra []any
		for i := start; i < start+64; i++ {
			id := 90000 + i
			name := fmt.Sprintf("catalog-other-%04d", i)
			channels = append(channels, "(?,?,'test','https://never-send.invalid','synthetic','')")
			ca = append(ca, id, name)
			credentials = append(credentials, "(?,'synthetic',1)")
			ka = append(ka, id)
			groups = append(groups, "(?,1)")
			ga = append(ga, name)
			ratios = append(ratios, "(?,'seedance-2.0-mini',1)")
			ra = append(ra, name)
		}
		routingAdminSQL(t, db, "INSERT INTO channels(id,name,provider,base_url,credential_enc,models) VALUES"+strings.Join(channels, ","), ca...)
		routingAdminSQL(t, db, "INSERT INTO channel_credentials(channel_id,credential_enc,status) VALUES"+strings.Join(credentials, ","), ka...)
		routingAdminSQL(t, db, "INSERT INTO group_ratios(name,ratio) VALUES"+strings.Join(groups, ","), ga...)
		routingAdminSQL(t, db, "INSERT INTO model_group_ratios(group_name,model,ratio) VALUES"+strings.Join(ratios, ","), ra...)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal("cannot begin reader fixture transaction")
	}
	defer tx.Rollback()
	routingAdminSQL(t, tx, "UPDATE channels SET status=0 WHERE id=89004")
	routingAdminSQL(t, tx, "INSERT INTO group_ratios(name,ratio) VALUES('catalog-uncommitted',0.5)")
	routingAdminSQL(t, tx, "UPDATE organizations SET group_name='catalog-uncommitted' WHERE id=89001")
	var statements []videoCatalogReaderStatement
	if err := db.Callback().Query().After("gorm:query").Register("catalog_reader_mysql_capture", func(q *gorm.DB) {
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("reader escaped supplied transaction")
		}
		if strings.Contains(q.Statement.SQL.String(), "FOR UPDATE") {
			t.Error("read acquired update lock")
		}
		statements = append(statements, videoCatalogReaderStatement{q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("catalog_reader_mysql_capture") })
	dirty := tx.Table("not_a_table").Select("private_column").Where("id=0").Order("private_column").Limit(0)
	group, err := identity.ReadVideoBillingGroupTx(context.Background(), dirty, 89001)
	if err != nil || group != "catalog-uncommitted" {
		t.Fatal("caller group write invisible", group, err)
	}
	channels, err := relay.ReadVideoCatalogChannelsTx(context.Background(), dirty, []uint64{89001, 89002, 89003, 89004})
	want := []relay.VideoCatalogChannel{{ID: 89001, PlatformOwned: true, Enabled: true, OfficialArkEndpoint: true, HasUsableCredential: true, Provider: "volcengine"}, {ID: 89002, PlatformOwned: true, Enabled: true, OfficialArkEndpoint: true, Provider: "modelark"}, {ID: 89003, Enabled: true, OfficialArkEndpoint: true, HasUsableCredential: true, Provider: "volcengine"}, {ID: 89004, PlatformOwned: true, Provider: "volcengine"}}
	if err != nil || !reflect.DeepEqual(channels, want) {
		t.Fatal("credential/endpoint/ownership facts", channels, err)
	}
	models := []string{"seedance-2.0-mini", "seedance-2.0", "seedance-2.5", "seedance-2.0-fast"}
	ratios, err := relay.ReadVideoCatalogRatiosTx(context.Background(), dirty, "catalog-vip", models)
	if err != nil || len(ratios) != 4 {
		t.Fatal("actual MySQL ratio derived join", err)
	}
	for i, value := range []string{"0.154320875", "0.125", "0.125", "0.125"} {
		if ratios[i].Model != models[i] || ratios[i].Ratio.String() != value {
			t.Fatal("independent ratio oracle", ratios[i], value)
		}
	}
	if len(statements) != 3 {
		t.Fatal("readers exceeded three batch SELECTs", len(statements))
	}
	if err := db.Callback().Query().Remove("catalog_reader_mysql_capture"); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.ReadVideoCatalogRatiosTx(context.Background(), tx, group, []string{"seedance-2.0"}); err != nil {
		t.Fatal("uncommitted group missing", err)
	}
	for _, statement := range statements {
		if !strings.Contains(statement.sql, "MAX_EXECUTION_TIME(") {
			t.Fatal("actual MySQL SELECT missing bound")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var plan []struct {
			Table  string  `gorm:"column:table"`
			Access string  `gorm:"column:type"`
			Key    *string `gorm:"column:key"`
		}
		err := tx.WithContext(ctx).Raw("EXPLAIN "+statement.sql, statement.args...).Scan(&plan).Error
		cancel()
		if err != nil || len(plan) == 0 {
			t.Fatal("actual captured SQL EXPLAIN failed")
		}
		seen := map[string]bool{}
		for _, row := range plan {
			seen[row.Table] = true
			t.Logf("reader EXPLAIN table=%s access=%s key=%v", row.Table, row.Access, func() string {
				if row.Key == nil {
					return ""
				}
				return *row.Key
			}())
			switch row.Table {
			case "organizations", "c", "gr", "mr", "all_credentials", "usable":
				if row.Key == nil || *row.Key == "" || row.Access == "ALL" {
					t.Fatal("selective owned-table lookup did not use an index", row.Table, row.Access)
				}
			}
		}
		if strings.Contains(statement.sql, "channel_credentials") && (!seen["all_credentials"] || !seen["usable"]) {
			t.Fatal("credential EXISTS plans absent")
		}
		if strings.Contains(statement.sql, "model_group_ratios") && (!seen["gr"] || !seen["mr"]) {
			t.Fatal("ratio join plans absent")
		}
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := db.Raw("SELECT COUNT(*) FROM group_ratios WHERE name='catalog-uncommitted'").Scan(&remaining).Error; err != nil || remaining != 0 {
		t.Fatal("read committed caller fixture", err, remaining)
	}
}

func TestMySQLVideoCatalogReadersCollationAndMissing(t *testing.T) {
	db := videoCatalogReaderMySQLFixture(t)
	for _, test := range []struct{ name, table, column, original, replacement, group string }{
		{"base_group_padding", "group_ratios", "name", "catalog-vip", "catalog-vip ", "catalog-vip"},
		{"model_group_case", "model_group_ratios", "group_name", "catalog-vip", "CATALOG-VIP", "catalog-vip"},
		{"model_group_padding", "model_group_ratios", "group_name", "catalog-vip", "catalog-vip ", "catalog-vip"},
		{"model_case", "model_group_ratios", "model", "seedance-2.0-mini", "Seedance-2.0-mini", "catalog-vip"},
		{"model_padding", "model_group_ratios", "model", "seedance-2.0-mini", "seedance-2.0-mini ", "catalog-vip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := db.Begin()
			defer tx.Rollback()
			routingAdminSQL(t, tx, "UPDATE "+test.table+" SET "+test.column+"=? WHERE "+test.column+"=?", test.replacement, test.original)
			// Positive control: the actual legacy collation really matches this
			// different identity; otherwise rejection would not prove the guard.
			var matched int64
			if err := tx.Raw("SELECT COUNT(*) FROM "+test.table+" WHERE "+test.column+"=?", test.original).Scan(&matched).Error; err != nil || matched == 0 {
				t.Fatal("legacy near-match positive control missing")
			}
			rows, err := relay.ReadVideoCatalogRatiosTx(context.Background(), tx, test.group, []string{"seedance-2.0-mini"})
			if rows != nil || !errors.Is(err, relay.ErrVideoCatalogUnavailable) {
				t.Fatal("near-match silently changed price or became one", rows, err)
			}
		})
	}
	tx := db.Begin()
	defer tx.Rollback()
	rows, err := relay.ReadVideoCatalogRatiosTx(context.Background(), tx, "really-missing", []string{"seedance-2.0-mini"})
	if err != nil || len(rows) != 1 || rows[0].Ratio.String() != "1" {
		t.Fatal("legitimate missing config fallback", rows, err)
	}
	if _, err := identity.ReadVideoBillingGroupTx(context.Background(), tx, 999999); !errors.Is(err, identity.ErrVideoCatalogNotFound) {
		t.Fatal("missing organization confused with group default", err)
	}
	channels, err := relay.ReadVideoCatalogChannelsTx(context.Background(), tx, []uint64{999999})
	if err != nil || channels == nil || len(channels) != 0 {
		t.Fatal("missing channel not omitted", channels, err)
	}
	routingAdminSQL(t, tx, "INSERT INTO group_ratios(name,ratio) VALUES(' ',0.5)")
	routingAdminSQL(t, tx, "INSERT INTO model_group_ratios(group_name,model,ratio) VALUES(' ','seedance-2.0',3)")
	rows, err = relay.ReadVideoCatalogRatiosTx(context.Background(), tx, " ", []string{"seedance-2.0"})
	if err != nil || len(rows) != 1 || rows[0].Ratio.String() != "1.5" {
		t.Fatal("only exact-empty group skips model ratio", rows, err)
	}
}

func TestMySQLVideoCatalogReadersRepeatableReadAndCancellation(t *testing.T) {
	db := videoCatalogReaderMySQLFixture(t)
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal("cannot begin readonly RR")
	}
	defer tx.Rollback()
	group, err := identity.ReadVideoBillingGroupTx(context.Background(), tx, 89001)
	if err != nil || group != "catalog-vip" {
		t.Fatal("first consistent read", group, err)
	}
	var mysqlErr *mysqlDriver.MySQLError
	if err := tx.Exec("UPDATE organizations SET name='must-fail' WHERE id=89001").Error; !errors.As(err, &mysqlErr) || mysqlErr.Number != 1792 {
		t.Fatal("readonly transaction positive control missing")
	}
	var beforeConnection, writerConnection uint64
	if err := tx.Raw("SELECT CONNECTION_ID()").Scan(&beforeConnection).Error; err != nil {
		t.Fatal("cannot identify readonly connection")
	}
	if err := db.Transaction(func(writer *gorm.DB) error {
		if err := writer.Raw("SELECT CONNECTION_ID()").Scan(&writerConnection).Error; err != nil {
			return err
		}
		if err := writer.Exec("UPDATE organizations SET group_name='catalog-new' WHERE id=89001").Error; err != nil {
			return err
		}
		if err := writer.Exec("UPDATE channels SET status=0 WHERE id=89001").Error; err != nil {
			return err
		}
		return writer.Exec("UPDATE model_group_ratios SET ratio=2 WHERE group_name='catalog-vip' AND model='seedance-2.0-mini'").Error
	}); err != nil {
		t.Fatal("independent writer failed")
	}
	if beforeConnection == writerConnection {
		t.Fatal("snapshot test did not use two connections")
	}
	assertSnapshot := func(tx *gorm.DB, wantGroup, wantRatio string, wantEnabled bool) {
		t.Helper()
		group, err := identity.ReadVideoBillingGroupTx(context.Background(), tx, 89001)
		if err != nil || group != wantGroup {
			t.Fatal("mixed group snapshot", group, err)
		}
		channels, err := relay.ReadVideoCatalogChannelsTx(context.Background(), tx, []uint64{89001})
		if err != nil || len(channels) != 1 || channels[0].Enabled != wantEnabled {
			t.Fatal("mixed channel snapshot", channels, err)
		}
		ratios, err := relay.ReadVideoCatalogRatiosTx(context.Background(), tx, "catalog-vip", []string{"seedance-2.0-mini"})
		if err != nil || len(ratios) != 1 || ratios[0].Ratio.String() != wantRatio {
			t.Fatal("mixed price snapshot", ratios, err)
		}
	}
	assertSnapshot(tx, "catalog-vip", "0.154320875", true)
	if err := tx.Commit().Error; err != nil {
		t.Fatal("read snapshot commit failed")
	}
	next := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	assertSnapshot(next, "catalog-new", "0.25", false)
	if err := next.Commit().Error; err != nil {
		t.Fatal("new snapshot commit failed")
	}
	for _, kind := range []string{"identity", "channels", "ratios"} {
		t.Run(kind, func(t *testing.T) {
			read := func(ctx context.Context, tx *gorm.DB) error {
				switch kind {
				case "identity":
					_, err := identity.ReadVideoBillingGroupTx(ctx, tx, 89001)
					return err
				case "channels":
					_, err := relay.ReadVideoCatalogChannelsTx(ctx, tx, []uint64{89001})
					return err
				default:
					_, err := relay.ReadVideoCatalogRatiosTx(ctx, tx, "catalog-vip", []string{"seedance-2.0-mini"})
					return err
				}
			}
			queries := 0
			if err := db.Callback().Query().Before("gorm:query").Register("catalog_real_mysql_sleep", func(q *gorm.DB) {
				queries++
				// Actual MySQL work, rather than sleeping in a callback or simply
				// adding context.Canceled. The production child cancels this query.
				q.Statement.SQL.Reset()
				q.Statement.SQL.WriteString("SELECT SLEEP(2)")
				q.Statement.Vars = nil
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Query().Remove("catalog_real_mysql_sleep")
			for _, budget := range []time.Duration{time.Second, 30 * time.Millisecond} {
				callTx := db.Begin()
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				start := time.Now()
				err := read(ctx, callTx)
				elapsed := time.Since(start)
				cancel()
				_ = callTx.Rollback().Error
				if !errors.Is(err, context.DeadlineExceeded) || elapsed > time.Second {
					t.Fatal("real SQL was not cancelled by bound", err, elapsed)
				}
			}
			callTx := db.Begin()
			defer callTx.Rollback()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := read(ctx, callTx); !errors.Is(err, context.Canceled) || queries != 2 {
				t.Fatal("cancelled reader executed SQL", err, queries)
			}
		})
	}
}
