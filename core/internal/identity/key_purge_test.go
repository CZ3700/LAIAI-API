package identity

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func keyPurgeDB(t *testing.T) *gorm.DB {
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
	if err := db.Exec(`CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER NOT NULL,status INTEGER NOT NULL,used_amount TEXT NOT NULL);
INSERT INTO api_keys VALUES(1,10,0,'2.12345678'),(2,10,1,'3.00000001'),(3,20,0,'4.00000002'),(4,10,9,'5.00000003');`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func keyPurgeRows(t *testing.T, db *gorm.DB) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := db.Table("api_keys").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPurgeRevokedKeyRequiresTransactionAndGuard(t *testing.T) {
	db := keyPurgeDB(t)
	guard := func(*gorm.DB, uint64, uint64) (bool, error) {
		t.Fatal("invalid request invoked guard")
		return false, nil
	}
	tx := db.Begin()
	defer tx.Rollback()
	for _, item := range []struct {
		db       *gorm.DB
		org, key uint64
		guard    KeyPurgeReferenceGuard
	}{
		{nil, 10, 1, guard}, {db, 10, 1, guard}, {tx.Session(&gorm.Session{DryRun: true}), 10, 1, guard},
		{tx, 0, 1, guard}, {tx, 10, 0, guard}, {tx, 10, 1, nil},
	} {
		if err := PurgeRevokedKeyTx(item.db, item.org, item.key, item.guard); !errors.Is(err, ErrKeyInvalid) {
			t.Fatal("invalid purge admitted", err)
		}
	}
}

func TestPurgeRevokedKeyKeepsIdentityAndReferences(t *testing.T) {
	fault := errors.New("reference read failed")
	for _, item := range []struct {
		name           string
		org, key       uint64
		referenced     bool
		guardErr, want error
		calls          int
	}{
		{"active", 10, 2, false, nil, ErrKeyPurgeActive, 0},
		{"foreign", 10, 3, false, nil, gorm.ErrRecordNotFound, 0},
		{"missing", 10, 99, false, nil, gorm.ErrRecordNotFound, 0},
		{"unknown_status", 10, 4, false, nil, ErrKeyInvalid, 0},
		{"referenced", 10, 1, true, nil, ErrKeyPurgeReferenced, 1},
		{"guard_error", 10, 1, false, fault, fault, 1},
		{"unreferenced", 10, 1, false, nil, nil, 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			db := keyPurgeDB(t)
			before := keyPurgeRows(t, db)
			calls := 0
			err := db.Transaction(func(tx *gorm.DB) error {
				return PurgeRevokedKeyTx(tx.Table("wrong_table").Where("id=99").Select("secret"), item.org, item.key, func(q *gorm.DB, org, key uint64) (bool, error) {
					calls++
					if q.Statement.ConnPool != tx.Statement.ConnPool || org != item.org || key != item.key {
						t.Fatal("guard escaped original identity/transaction")
					}
					return item.referenced, item.guardErr
				})
			})
			if !errors.Is(err, item.want) || calls != item.calls {
				t.Fatalf("purge err=%v calls=%d", err, calls)
			}
			after := keyPurgeRows(t, db)
			if item.want != nil && !reflect.DeepEqual(before, after) {
				t.Fatal("failed purge changed keys or quota")
			}
			if item.want == nil && !reflect.DeepEqual(before[1:], after) {
				t.Fatal("purge changed another Key or quota")
			}
		})
	}
}

func TestPurgeRevokedKeyCancellationAndOuterRollback(t *testing.T) {
	db := keyPurgeDB(t)
	before := keyPurgeRows(t, db)
	stop := errors.New("outer transaction failure")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := PurgeRevokedKeyTx(tx, 10, 1, func(*gorm.DB, uint64, uint64) (bool, error) { return false, nil }); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) || !reflect.DeepEqual(before, keyPurgeRows(t, db)) {
		t.Fatal("purge escaped outer rollback", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = db.Transaction(func(tx *gorm.DB) error {
		cancel()
		return PurgeRevokedKeyTx(tx.WithContext(ctx), 10, 1, func(*gorm.DB, uint64, uint64) (bool, error) {
			t.Fatal("cancelled purge invoked guard")
			return false, nil
		})
	})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(before, keyPurgeRows(t, db)) {
		t.Fatal("cancelled purge changed keys", err)
	}
}
