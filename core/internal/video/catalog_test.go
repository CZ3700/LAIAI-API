package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func catalogLocalDB(t *testing.T) *gorm.DB {
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
	for _, ddl := range []string{
		`CREATE TABLE video_model_configs(public_model TEXT PRIMARY KEY,version TEXT,provider_scope_id INTEGER,channel_id INTEGER,enabled BOOLEAN,capability_json TEXT,pricing_version TEXT,no_charge_statuses TEXT,max_input_video_duration_ms INTEGER,bounds_json TEXT,verified_at DATETIME,updated_at DATETIME)`,
		`CREATE TABLE video_asset_org_routes(org_id INTEGER,provider_scope_id INTEGER,scope_version INTEGER,enabled BOOLEAN,default_route BOOLEAN,updated_at DATETIME,PRIMARY KEY(org_id,provider_scope_id))`,
		`CREATE TABLE video_asset_shared_defaults(id INTEGER PRIMARY KEY,revision INTEGER,provider_scope_id INTEGER,scope_version INTEGER,enabled BOOLEAN,allow_async BOOLEAN,updated_by INTEGER,updated_at DATETIME)`,
		`CREATE TABLE video_asset_scope_configs(provider_scope_id INTEGER,version INTEGER,account_ref TEXT,region TEXT,project_name TEXT,capabilities_json TEXT,credential_cipher TEXT,enabled BOOLEAN,created_by INTEGER,created_at DATETIME,updated_at DATETIME,PRIMARY KEY(provider_scope_id,version))`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestVideoCatalogConfigReadIsBoundedAndCleansCallerChain(t *testing.T) {
	for _, sample := range []struct {
		name        string
		rows, bytes int
		want        error
	}{
		{"64_rows", 64, 2, nil}, {"65_rows", 65, 2, ErrCatalogLimit},
		{"exact_total", 16, 65534, nil}, {"over_total", 17, 65534, ErrCatalogLimit},
		{"over_field", 1, 65537, ErrCatalogLimit},
	} {
		t.Run(sample.name, func(t *testing.T) {
			db := catalogLocalDB(t)
			for i := 0; i < sample.rows; i++ {
				cfg := ModelConfig{PublicModel: fmt.Sprintf("fixture-%02d", i), CapabilityJSON: strings.Repeat(" ", sample.bytes), BoundsJSON: "{}"}
				if err := db.Create(&cfg).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				rows, err := catalogConfigs(context.Background(), tx.Where("public_model = ?", "not-present").Limit(1))
				if sample.want == nil {
					if err != nil || len(rows) != sample.rows {
						t.Fatal("valid bounded read was truncated or contaminated", err)
					}
				} else if !errors.Is(err, sample.want) || rows != nil {
					t.Fatal("limit returned partial configuration", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVideoCatalogReadCancellationWinsOverEmptyAndSQLFailure(t *testing.T) {
	for _, mode := range []string{"cancel_after_read", "select_budget", "sql_failure"} {
		t.Run(mode, func(t *testing.T) {
			db := catalogLocalDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "sql_failure" {
				if err := db.Exec("DROP TABLE video_model_configs").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Callback().Query().After("gorm:query").Register("catalog_test:cancel", func(q *gorm.DB) {
					if q.Statement.Table == "video_model_configs" {
						if mode == "select_budget" {
							time.Sleep(225 * time.Millisecond)
						} else {
							cancel()
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer db.Callback().Query().Remove("catalog_test:cancel")
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				rows, err := catalogConfigs(ctx, tx)
				want := ErrCatalogUnavailable
				if mode == "cancel_after_read" {
					want = context.Canceled
				}
				if mode == "select_budget" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) || rows != nil {
					t.Fatal("query failure became empty successful data", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVideoCatalogRejectsNonTransactionsAndDryRun(t *testing.T) {
	db := catalogLocalDB(t)
	if !errors.Is(catalogValidTx(context.Background(), db), ErrCatalogInvalid) {
		t.Fatal("bare DB accepted")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if !errors.Is(catalogValidTx(context.Background(), tx.Session(&gorm.Session{DryRun: true})), ErrCatalogInvalid) {
			t.Fatal("dry run accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !errors.Is(catalogValidTx(ctx, tx), context.Canceled) {
			t.Fatal("caller cancellation erased")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
