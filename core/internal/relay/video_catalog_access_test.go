package relay

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func videoCatalogAccessDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	// Omit models/name and all unrelated channel fields: the reader must not
	// turn a fixed official binding into a channel CSV membership requirement.
	if err := db.Exec(`CREATE TABLE channels(id INTEGER PRIMARY KEY,owner_org_id INTEGER,status INTEGER,provider TEXT,base_url TEXT,credential_enc TEXT);
CREATE TABLE channel_credentials(id INTEGER PRIMARY KEY,channel_id INTEGER,status INTEGER,credential_enc TEXT);
CREATE INDEX channel_credentials_channel ON channel_credentials(channel_id);
CREATE TABLE group_ratios(name TEXT COLLATE NOCASE PRIMARY KEY,ratio TEXT NOT NULL);
CREATE TABLE model_group_ratios(group_name TEXT COLLATE NOCASE,model TEXT COLLATE NOCASE,ratio TEXT NOT NULL,PRIMARY KEY(group_name,model));
INSERT INTO channels VALUES
(1,0,1,'volcengine','https://ark.cn-beijing.volces.com/api/v3','legacy-secret-marker'),
(2,0,1,'modelark','https://ark.cn-beijing.volces.com/api/v3///','legacy-secret-marker'),
(3,8,0,'other','https://example.invalid/api',''),
(4,0,1,'volcengine',' https://ark.cn-beijing.volces.com/api/v3','');
INSERT INTO channel_credentials VALUES(1,2,2,'disabled-secret-marker'),(2,2,1,''),(3,3,1,'active-secret-marker');
INSERT INTO group_ratios VALUES('vip','0.1250'),('nonpositive','0'),('','2');
INSERT INTO model_group_ratios VALUES('vip','seedance-2.0-mini','1.234567'),('vip','seedance-2.0-fast','-2'),('nonpositive','seedance-2.5','3'),('','seedance-2.0','9');`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestVideoCatalogChannelsOneTxSafeFactsAndCredentialFallback(t *testing.T) {
	db := videoCatalogAccessDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE channels SET owner_org_id=0 WHERE id=3").Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	var sql string
	if err := db.Callback().Query().After("gorm:query").Register("catalog_channel_projection", func(q *gorm.DB) {
		queries++
		sql = q.Statement.SQL.String()
		if q.Statement.ConnPool != tx.Statement.ConnPool || strings.Contains(sql, "FOR UPDATE") {
			t.Error("reader escaped tx or locked")
		}
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("missing").Select("secret").Where("id=999").Limit(0).Order("secret")
	rows, err := ReadVideoCatalogChannelsTx(context.Background(), dirty, []uint64{1, 2, 3, 9})
	want := []VideoCatalogChannel{{ID: 1, PlatformOwned: true, Enabled: true, OfficialArkEndpoint: true, HasUsableCredential: true, Provider: "volcengine"}, {ID: 2, PlatformOwned: true, Enabled: true, OfficialArkEndpoint: true, Provider: "modelark"}, {ID: 3, PlatformOwned: true, HasUsableCredential: true, Provider: "other"}}
	if err != nil || !reflect.DeepEqual(rows, want) || queries != 1 || !strings.Contains(sql, "EXISTS") {
		t.Fatal(rows, err, queries, sql)
	}
	if err := tx.Exec("UPDATE channel_credentials SET credential_enc=' ' WHERE id=2").Error; err != nil {
		t.Fatal(err)
	}
	rows, err = ReadVideoCatalogChannelsTx(context.Background(), tx, []uint64{2, 4})
	if err != nil || len(rows) != 2 || !rows[0].HasUsableCredential || rows[1].OfficialArkEndpoint || rows[1].HasUsableCredential {
		t.Fatal("nonempty metadata or exact endpoint changed", rows, err)
	}
	if err := tx.Exec("DELETE FROM channel_credentials WHERE channel_id=2").Error; err != nil {
		t.Fatal(err)
	}
	rows, err = ReadVideoCatalogChannelsTx(context.Background(), tx, []uint64{2})
	if err != nil || len(rows) != 1 || !rows[0].HasUsableCredential {
		t.Fatal("true legacy fallback missing", rows, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var owner uint64
	if err := db.Raw("SELECT owner_org_id FROM channels WHERE id=3").Scan(&owner).Error; err != nil || owner != 8 {
		t.Fatal("reader committed changes", err, owner)
	}
}

func TestVideoCatalogRatiosExactDecimalAndFallbackOneQuery(t *testing.T) {
	db := videoCatalogAccessDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	models := []string{"seedance-2.0-mini", "seedance-2.0", "seedance-2.5", "seedance-2.0-fast"}
	queries := 0
	if err := db.Callback().Query().After("gorm:query").Register("catalog_ratio_projection", func(q *gorm.DB) {
		queries++
		if q.Statement.ConnPool != tx.Statement.ConnPool || strings.Contains(q.Statement.SQL.String(), "organizations") {
			t.Error("ratio reader escaped own domain or transaction")
		}
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("missing").Select("secret").Where("id=999").Limit(0).Order("secret")
	rows, err := ReadVideoCatalogRatiosTx(context.Background(), dirty, "vip", models)
	if err != nil || len(rows) != 4 || queries != 1 {
		t.Fatal(rows, err, queries)
	}
	for i, expected := range []string{"0.154320875", "0.125", "0.125", "0.125"} {
		if rows[i].Model != models[i] || rows[i].Ratio.String() != expected {
			t.Fatal("independent decimal oracle", rows[i], expected)
		}
	}
	for _, tc := range []struct{ group, model, want string }{{"nonpositive", "seedance-2.5", "3"}, {"missing", "seedance-2.0", "1"}, {"", "seedance-2.0", "2"}} {
		rows, err = ReadVideoCatalogRatiosTx(context.Background(), tx, tc.group, []string{tc.model})
		if err != nil || len(rows) != 1 || rows[0].Ratio.String() != tc.want {
			t.Fatal("two-layer fallback", tc, rows, err)
		}
	}
	if err := tx.Exec("DROP TABLE model_group_ratios").Error; err != nil {
		t.Fatal(err)
	}
	if rows, err = ReadVideoCatalogRatiosTx(context.Background(), tx, "vip", models); rows != nil || !errors.Is(err, ErrVideoCatalogUnavailable) {
		t.Fatal("SQL failure silently repriced", rows, err)
	}
}

func TestVideoCatalogRatiosRejectCollationNearMatches(t *testing.T) {
	for _, collation := range []string{"NOCASE", "RTRIM"} {
		for _, target := range []string{"base_group", "model_group", "model"} {
			t.Run(collation+"/"+target, func(t *testing.T) {
				db := videoCatalogAccessDB(t)
				tx := db.Begin()
				defer tx.Rollback()
				if err := tx.Exec("DROP TABLE group_ratios; DROP TABLE model_group_ratios; CREATE TABLE group_ratios(name TEXT COLLATE " + collation + " PRIMARY KEY,ratio TEXT NOT NULL); CREATE TABLE model_group_ratios(group_name TEXT COLLATE " + collation + ",model TEXT COLLATE " + collation + ",ratio TEXT NOT NULL,PRIMARY KEY(group_name,model))").Error; err != nil {
					t.Fatal(err)
				}
				storedGroup, storedModel := "vip", "seedance-2.0-mini"
				near := func(s string) string {
					if collation == "NOCASE" {
						return strings.ToUpper(s)
					}
					return s + " "
				}
				if target == "base_group" {
					if err := tx.Exec("INSERT INTO group_ratios VALUES(?, '0.5')", near(storedGroup)).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					if target == "model_group" {
						storedGroup = near(storedGroup)
					} else {
						storedModel = near(storedModel)
					}
					if err := tx.Exec("INSERT INTO model_group_ratios VALUES(?,?,'0.5')", storedGroup, storedModel).Error; err != nil {
						t.Fatal(err)
					}
				}
				rows, err := ReadVideoCatalogRatiosTx(context.Background(), tx, "vip", []string{"seedance-2.0-mini"})
				if rows != nil || !errors.Is(err, ErrVideoCatalogUnavailable) {
					t.Fatal("near-match became default or altered ratio", rows, err)
				}
			})
		}
	}
}

func TestVideoCatalogReadersRejectInvalidInputsWithoutQueries(t *testing.T) {
	db := videoCatalogAccessDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("catalog_invalid_count", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	readers := []func(context.Context, *gorm.DB) error{
		func(ctx context.Context, db *gorm.DB) error {
			_, err := ReadVideoCatalogChannelsTx(ctx, db, []uint64{1})
			return err
		},
		func(ctx context.Context, db *gorm.DB) error {
			_, err := ReadVideoCatalogRatiosTx(ctx, db, "vip", []string{"seedance-2.0"})
			return err
		},
	}
	for _, read := range readers {
		for _, invalid := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
			if err := read(context.Background(), invalid); !errors.Is(err, ErrVideoCatalogInvalid) {
				t.Fatal(err)
			}
		}
		if err := read(nil, tx); !errors.Is(err, ErrVideoCatalogInvalid) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := read(ctx, tx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	for _, ids := range [][]uint64{{0}, {2, 1}, {1, 1}, {1, 2, 3, 4, 5}} {
		if _, err := ReadVideoCatalogChannelsTx(context.Background(), tx, ids); !errors.Is(err, ErrVideoCatalogInvalid) {
			t.Fatal(err)
		}
	}
	for _, models := range [][]string{{"Seedance-2.0"}, {"seedance-2.0 "}, {"doubao-seedance-2-0"}, {"seedance-2.0", "seedance-2.0"}} {
		if _, err := ReadVideoCatalogRatiosTx(context.Background(), tx, "vip", models); !errors.Is(err, ErrVideoCatalogInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := ReadVideoCatalogRatiosTx(context.Background(), tx, strings.Repeat("界", 65), []string{"seedance-2.0"}); !errors.Is(err, ErrVideoCatalogInvalid) {
		t.Fatal(err)
	}
	if got, err := ReadVideoCatalogChannelsTx(context.Background(), tx, nil); err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := ReadVideoCatalogRatiosTx(context.Background(), tx, "", nil); err != nil || got == nil || len(got) != 0 || queries != 0 {
		t.Fatal(got, err, queries)
	}
}

func TestVideoCatalogReadersBoundDeadlineAndHideDiagnostics(t *testing.T) {
	for _, kind := range []string{"channels", "ratios"} {
		t.Run(kind, func(t *testing.T) {
			db := videoCatalogAccessDB(t)
			var logs bytes.Buffer
			db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
			tx := db.Begin()
			defer tx.Rollback()
			read := func(ctx context.Context) error {
				if kind == "channels" {
					_, err := ReadVideoCatalogChannelsTx(ctx, tx, []uint64{1})
					return err
				}
				_, err := ReadVideoCatalogRatiosTx(ctx, tx, "vip", []string{"seedance-2.0"})
				return err
			}
			marker := "private-provider-diagnostic-marker"
			if err := db.Callback().Query().Before("gorm:query").Register("catalog_injected_error", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
				t.Fatal(err)
			}
			if err := read(context.Background()); !errors.Is(err, ErrVideoCatalogUnavailable) || strings.Contains(err.Error(), marker) || strings.Contains(logs.String(), marker) || strings.Contains(logs.String(), "SELECT") {
				t.Fatal("unsafe diagnostic", err, logs.String())
			}
			if err := db.Callback().Query().Remove("catalog_injected_error"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().Before("gorm:query").Register("catalog_deadline", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
				t.Fatal(err)
			}
			for _, parentLimit := range []time.Duration{time.Second, 30 * time.Millisecond} {
				ctx, cancel := context.WithTimeout(context.Background(), parentLimit)
				start := time.Now()
				err := read(ctx)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 700*time.Millisecond {
					t.Fatal("SELECT timeout ignored", err, time.Since(start))
				}
			}
		})
	}
}

func TestVideoCatalogChannelsBoundedProviderFacts(t *testing.T) {
	db := videoCatalogAccessDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	for _, tc := range []struct {
		column string
		value  any
	}{{"provider", strings.Repeat("x", 33)}, {"provider", nil}, {"provider", string([]byte{0xff})}, {"base_url", strings.Repeat("x", 256)}} {
		if err := tx.Exec("UPDATE channels SET provider='volcengine',base_url='https://ark.cn-beijing.volces.com/api/v3' WHERE id=1").Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Table("channels").Where("id=1").Update(tc.column, tc.value).Error; err != nil {
			t.Fatal(err)
		}
		rows, err := ReadVideoCatalogChannelsTx(context.Background(), tx, []uint64{1})
		if rows != nil || !errors.Is(err, ErrVideoCatalogUnavailable) {
			t.Fatal("malformed facts accepted", rows, err)
		}
	}
}
