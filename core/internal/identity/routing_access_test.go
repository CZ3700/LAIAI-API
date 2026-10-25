package identity

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
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func routingOrganizationDB(t *testing.T) *gorm.DB {
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
	// Deliberately omit every non-whitelisted Organization column.
	if err := db.Exec("CREATE TABLE organizations(id INTEGER PRIMARY KEY,status INTEGER NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO organizations VALUES(1,1),(2,0),(3,7)").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRoutingOrganizationUsesCallerTransactionAndProjection(t *testing.T) {
	db := routingOrganizationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE organizations SET status=0 WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	var reads int
	var gotLock bool
	if err := db.Callback().Query().Before("gorm:query").Register("routing_org_contract", func(q *gorm.DB) {
		reads++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("bridge escaped caller transaction")
		}
		lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking)
		gotLock = ok && lock.Strength == "UPDATE"
	}); err != nil {
		t.Fatal(err)
	}
	want := []RoutingOrganization{{ID: 1}, {ID: 2}, {ID: 3}}
	// A caller query must not leak predicates, table or selected private fields.
	dirty := tx.Table("wrong_table").Select("private_field").Where("id = ?", 999)
	got, err := LockRoutingOrganizations(context.Background(), dirty, []uint64{1, 2, 3, 4})
	if err != nil || !reflect.DeepEqual(got, want) || reads != 1 || !gotLock {
		t.Fatal("current locking projection differs", err, got, reads, gotLock)
	}
	got, err = ReadRoutingOrganizations(context.Background(), dirty, []uint64{1, 2, 3, 4})
	if err != nil || !reflect.DeepEqual(got, want) || reads != 2 || gotLock {
		t.Fatal("snapshot projection acquired a lock or changed transaction", err, got)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var status int
	if err := db.Raw("SELECT status FROM organizations WHERE id=1").Scan(&status).Error; err != nil || status != 1 {
		t.Fatal("bridge committed the caller transaction", err)
	}
}

func TestRoutingOrganizationRejectsInvalidAccessBeforeSQL(t *testing.T) {
	db := routingOrganizationDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("routing_org_no_query", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	large := make([]uint64, 101)
	for i := range large {
		large[i] = uint64(i + 1)
	}
	for _, read := range []func(context.Context, *gorm.DB, []uint64) ([]RoutingOrganization, error){LockRoutingOrganizations, ReadRoutingOrganizations} {
		for _, ids := range [][]uint64{{0}, {2, 1}, {1, 1}, large} {
			if _, err := read(context.Background(), tx, ids); !errors.Is(err, ErrRoutingAccessInvalid) {
				t.Fatal("invalid IDs accepted", err)
			}
		}
		for _, bad := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
			if _, err := read(context.Background(), bad, nil); !errors.Is(err, ErrRoutingAccessInvalid) {
				t.Fatal("nontransaction or dry run accepted", err)
			}
		}
		if _, err := read(nil, tx, nil); !errors.Is(err, ErrRoutingAccessInvalid) {
			t.Fatal("nil context accepted", err)
		}
		if got, err := read(context.Background(), tx, nil); err != nil || got == nil || len(got) != 0 {
			t.Fatal("empty valid batch contract", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := read(ctx, tx, []uint64{1}); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled access accepted", err)
		}
	}
	if queries != 0 {
		t.Fatal("invalid or empty access executed SQL")
	}
}

func TestRoutingOrganizationSafeErrorsAndDeadline(t *testing.T) {
	db := routingOrganizationDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	tx := db.Begin()
	defer tx.Rollback()
	marker := "synthetic-private-db-diagnostic"
	if err := db.Callback().Query().After("gorm:query").Register("routing_org_diagnostic", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	_ = tx.Table("organizations").Find(&raw).Error
	if !strings.Contains(logs.String(), marker) {
		t.Fatal("Warn logger positive control did not emit diagnostic")
	}
	logs.Reset()
	for _, read := range []func(context.Context, *gorm.DB, []uint64) ([]RoutingOrganization, error){LockRoutingOrganizations, ReadRoutingOrganizations} {
		got, err := read(context.Background(), tx, []uint64{1})
		if got != nil || err != ErrRoutingAccessUnavailable || strings.Contains(err.Error(), marker) || logs.Len() != 0 {
			t.Fatal("bridge exposed partial success, SQL or a private diagnostic")
		}
	}
	if err := db.Callback().Query().Remove("routing_org_diagnostic"); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("routing_org_deadline", func(q *gorm.DB) {
		<-q.Statement.Context.Done()
		q.AddError(q.Statement.Context.Err())
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := ReadRoutingOrganizations(ctx, tx, []uint64{1})
	if got != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("read ignored caller deadline", err)
	}
}
