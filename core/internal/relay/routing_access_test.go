package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func routingChannelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// No addresses, credentials or other channel configuration columns exist.
	if err := db.Exec("CREATE TABLE channels(id INTEGER PRIMARY KEY,name TEXT,provider TEXT,status INTEGER,owner_org_id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO channels VALUES(1,'platform','openai',1,0),(2,'private','volcengine',0,7),(3,'unknown','custom',7,9)").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRoutingChannelProjectionUsesOneCallerTransactionQuery(t *testing.T) {
	db := routingChannelDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE channels SET owner_org_id=8 WHERE id=2").Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	locked := false
	if err := db.Callback().Query().Before("gorm:query").Register("routing_channel_contract", func(q *gorm.DB) {
		queries++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("channel bridge escaped supplied transaction")
		}
		v, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
		locked = ok && v.Strength == "UPDATE"
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("wrong_table").Select("credential_enc").Where("id=999")
	got, err := LockRoutingChannels(context.Background(), dirty, []uint64{1, 2, 3, 4})
	if err != nil || !reflect.DeepEqual(got, []RoutingChannel{{ID: 1, Status: 1}, {ID: 2, OwnerOrgID: 8}, {ID: 3, OwnerOrgID: 9, Status: 7}}) || queries != 1 || !locked {
		t.Fatal("locking facts or query count differ", err, got)
	}
	ids := make([]uint64, 100)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	rows, err := ReadRoutingChannelSummaries(context.Background(), dirty, ids)
	want := []RoutingChannelSummary{{ID: 1, Name: "platform", Provider: "openai", Status: 1}, {ID: 2, Name: "private", Provider: "volcengine", OwnerOrgID: 8}, {ID: 3, Name: "unknown", Provider: "custom", OwnerOrgID: 9, Status: 7}}
	if err != nil || !reflect.DeepEqual(rows, want) || queries != 2 || locked {
		t.Fatal("summary is not one safe non-locking batch", err, rows)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var owner uint64
	if err := db.Raw("SELECT owner_org_id FROM channels WHERE id=2").Scan(&owner).Error; err != nil || owner != 7 {
		t.Fatal("bridge committed caller transaction", err)
	}
}

func TestRoutingChannelRejectsInvalidAccessBeforeSQL(t *testing.T) {
	db := routingChannelDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("routing_channel_no_query", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	readers := []func(context.Context, *gorm.DB, []uint64) error{
		func(ctx context.Context, db *gorm.DB, ids []uint64) error {
			_, err := LockRoutingChannels(ctx, db, ids)
			return err
		},
		func(ctx context.Context, db *gorm.DB, ids []uint64) error {
			_, err := ReadRoutingChannelSummaries(ctx, db, ids)
			return err
		},
	}
	large := make([]uint64, 101)
	for i := range large {
		large[i] = uint64(i + 1)
	}
	for _, read := range readers {
		for _, ids := range [][]uint64{{0}, {2, 1}, {1, 1}, large} {
			if err := read(context.Background(), tx, ids); !errors.Is(err, ErrRoutingAccessInvalid) {
				t.Fatal("invalid IDs accepted", err)
			}
		}
		for _, bad := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
			if err := read(context.Background(), bad, nil); !errors.Is(err, ErrRoutingAccessInvalid) {
				t.Fatal("nontransaction or dry run accepted", err)
			}
		}
		if err := read(nil, tx, nil); !errors.Is(err, ErrRoutingAccessInvalid) {
			t.Fatal("nil context accepted", err)
		}
		if err := read(context.Background(), tx, nil); err != nil {
			t.Fatal("empty transaction batch failed", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := read(ctx, tx, []uint64{1}); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled access accepted", err)
		}
	}
	if queries != 0 {
		t.Fatal("invalid or empty access executed SQL")
	}
}

func TestRoutingChannelErrorsCannotLeakViaWarnLogger(t *testing.T) {
	db := routingChannelDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	tx := db.Begin()
	defer tx.Rollback()
	marker := "synthetic-private-channel-diagnostic"
	if err := db.Callback().Query().After("gorm:query").Register("routing_channel_diagnostic", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	_ = tx.Table("channels").Find(&raw).Error
	if !strings.Contains(logs.String(), marker) {
		t.Fatal("Warn positive control did not emit diagnostic")
	}
	logs.Reset()
	if rows, err := LockRoutingChannels(context.Background(), tx, []uint64{1}); rows != nil || err != ErrRoutingAccessUnavailable || logs.Len() != 0 {
		t.Fatal("lock exposed private diagnostic or partial rows")
	}
	if rows, err := ReadRoutingChannelSummaries(context.Background(), tx, []uint64{1}); rows != nil || err != ErrRoutingAccessUnavailable || logs.Len() != 0 {
		t.Fatal("summary exposed private diagnostic or partial rows")
	}
}

type routingNamedMySQL struct{ gorm.Dialector }

func (routingNamedMySQL) Name() string { return "mysql" }

func TestRoutingChannelSelectHintAndReadDeadline(t *testing.T) {
	db := routingChannelDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	// Execute the generated comment on SQLite, without claiming real MySQL
	// optimizer/row-lock validation. The latter belongs to the MySQL gate.
	mysqlNamed := tx.Session(&gorm.Session{})
	config := *mysqlNamed.Config
	mysqlNamed.Config = &config
	mysqlNamed.Dialector = routingNamedMySQL{db.Dialector}
	var query string
	if err := db.Callback().Query().After("gorm:query").Register("routing_channel_hint", func(q *gorm.DB) { query = q.Statement.SQL.String() }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if rows, err := ReadRoutingChannelSummaries(ctx, mysqlNamed, []uint64{1}); err != nil || len(rows) != 1 {
		t.Fatal("hinted safe SELECT failed", err)
	}
	var milliseconds int
	if _, err := fmt.Sscanf(query, "SELECT /*+ MAX_EXECUTION_TIME(%d)", &milliseconds); err != nil || milliseconds < 1 || milliseconds > 50 {
		t.Fatal("SELECT hint exceeds caller budget")
	}
	if err := db.Callback().Query().Before("gorm:query").Register("routing_channel_deadline", func(q *gorm.DB) {
		<-q.Statement.Context.Done()
		q.AddError(q.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	rows, err := ReadRoutingChannelSummaries(context.Background(), tx, []uint64{1})
	if rows != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("summary did not bound a SELECT to 200ms", err)
	}
}
