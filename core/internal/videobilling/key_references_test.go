package videobilling

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestKeySnapshotReferenceUsesOriginalTransactionAndScope(t *testing.T) {
	db, q := setup(t, "100")
	tx := db.Begin()
	defer tx.Rollback()
	if _, err := ReserveTx(tx, ReserveInput{BillingSchemaVersion: videoruntime.BillingV1, OrgID: 10, APIKeyID: 1, TaskID: "uncommitted", RequestHMAC: strings.Repeat("a", 64), Quote: q, Now: testAt}); err != nil {
		t.Fatal(err)
	}
	var child context.Context
	queries := 0
	if err := db.Callback().Query().After("gorm:query").Register("key_reference_shape", func(query *gorm.DB) {
		if query.Statement.Table != "video_billing_snapshots" {
			t.Error("reference reader queried another table")
		}
		queries++
		child = query.Statement.Context
		if query.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("reference lookup left original transaction")
		}
		limit, ok := query.Statement.Clauses["LIMIT"].Expression.(clause.Limit)
		if !ok || limit.Limit == nil || *limit.Limit != 1 {
			t.Error("reference probe is not bounded")
		}
		if deadline, ok := child.Deadline(); !ok || time.Until(deadline) > 200*time.Millisecond {
			t.Error("reference deadline missing")
		}
		if !strings.HasPrefix(query.Statement.SQL.String(), "SELECT `id` FROM") {
			t.Error("reference read selected private fields")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("key_reference_shape") })
	for _, item := range []struct {
		org, key uint64
		want     bool
	}{{10, 1, true}, {10, 3, false}, {20, 1, false}} {
		got, err := HasKeySnapshotReferenceTx(tx.Table("wrong_private_table").Where("id=999").Select("secret"), item.org, item.key)
		if err != nil || got != item.want {
			t.Fatalf("scoped reference result=%v want=%v err=%v", got, item.want, err)
		}
		if child == nil || !errors.Is(child.Err(), context.Canceled) || tx.Statement.Context.Err() != nil {
			t.Fatal("child lifetime leaked into caller")
		}
	}
	if queries != 3 {
		t.Fatal("reference probe used extra queries", queries)
	}
	_ = db.Callback().Query().Remove("key_reference_shape")
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if count(t, db, "video_billing_snapshots") != 0 || !keyUsed(t, db).IsZero() {
		t.Fatal("reference read committed caller reservation")
	}
}

func TestKeySnapshotReferenceRequiresTransactionAndPreservesSafeErrors(t *testing.T) {
	db, _ := setup(t, "100")
	tx := db.Begin()
	defer tx.Rollback()
	for _, item := range []struct {
		db       *gorm.DB
		org, key uint64
	}{{nil, 10, 1}, {db, 10, 1}, {tx.Session(&gorm.Session{DryRun: true}), 10, 1}, {tx, 0, 1}, {tx, 10, 0}} {
		if got, err := HasKeySnapshotReferenceTx(item.db, item.org, item.key); got || !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid lookup admitted", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := HasKeySnapshotReferenceTx(tx.WithContext(ctx), 10, 1); got || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrKeyReferenceUnavailable) {
		t.Fatal("cancelled lookup lost error", got, err)
	}
	var logs bytes.Buffer
	warn := logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})
	if err := tx.Exec("DROP TABLE video_billing_snapshots").Error; err != nil {
		t.Fatal(err)
	}
	if got, err := HasKeySnapshotReferenceTx(tx.Session(&gorm.Session{Logger: warn}), 10, 1); got || !errors.Is(err, ErrKeyReferenceUnavailable) || err.Error() != ErrKeyReferenceUnavailable.Error() || logs.Len() != 0 {
		t.Fatal("SQL failure became empty success or disclosed details", got, err)
	}
	_ = tx.Session(&gorm.Session{Logger: warn}).Exec("SELECT inherited_key_reference_control FROM missing_key_reference_table").Error
	if !strings.Contains(logs.String(), "inherited_key_reference_control") {
		t.Fatal("inherited logger positive control missing")
	}
}
