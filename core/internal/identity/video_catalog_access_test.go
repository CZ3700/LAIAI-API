package identity

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func videoBillingGroupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := routingOrganizationDB(t)
	if err := db.Exec("ALTER TABLE organizations ADD COLUMN group_name TEXT; UPDATE organizations SET group_name='default'").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestVideoCatalogBillingGroupCallerTxAndProjection(t *testing.T) {
	db := videoBillingGroupDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE organizations SET group_name='VIP' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := db.Callback().Query().After("gorm:query").Register("video_billing_group_projection", func(q *gorm.DB) {
		queries++
		if q.Statement.ConnPool != tx.Statement.ConnPool || strings.Contains(q.Statement.SQL.String(), "status") || strings.Contains(q.Statement.SQL.String(), "FOR UPDATE") {
			t.Error("reader escaped transaction or narrow non-locking projection")
		}
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("missing").Select("secret").Where("id=999").Limit(0).Order("secret")
	got, err := ReadVideoBillingGroupTx(context.Background(), dirty, 1)
	if err != nil || got != "VIP" || queries != 1 {
		t.Fatal(got, err, queries)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Remove("video_billing_group_projection"); err != nil {
		t.Fatal(err)
	}
	var unchanged string
	if err := db.Raw("SELECT group_name FROM organizations WHERE id=1").Scan(&unchanged).Error; err != nil || unchanged != "default" {
		t.Fatal("reader committed caller writes", unchanged, err)
	}
}

func TestVideoCatalogBillingGroupMissingEmptyMalformedAndFailure(t *testing.T) {
	db := videoBillingGroupDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if _, err := ReadVideoBillingGroupTx(context.Background(), tx, 99); !errors.Is(err, ErrVideoCatalogNotFound) {
		t.Fatal("missing organization", err)
	}
	for _, value := range []string{"", " ", strings.Repeat("界", 64)} {
		if err := tx.Exec("UPDATE organizations SET group_name=? WHERE id=1", value).Error; err != nil {
			t.Fatal(err)
		}
		if got, err := ReadVideoBillingGroupTx(context.Background(), tx, 1); err != nil || got != value {
			t.Fatal("legacy group value changed", got, err)
		}
	}
	for _, value := range []any{nil, strings.Repeat("x", 65), strings.Repeat("界", 65), string([]byte{0xff})} {
		if err := tx.Exec("UPDATE organizations SET group_name=? WHERE id=1", value).Error; err != nil {
			t.Fatal(err)
		}
		if got, err := ReadVideoBillingGroupTx(context.Background(), tx, 1); got != "" || !errors.Is(err, ErrVideoCatalogUnavailable) {
			t.Fatal("malformed group became default", got, err)
		}
	}
	if err := tx.Exec("DROP TABLE organizations").Error; err != nil {
		t.Fatal(err)
	}
	if got, err := ReadVideoBillingGroupTx(context.Background(), tx, 1); got != "" || !errors.Is(err, ErrVideoCatalogUnavailable) {
		t.Fatal("SQL error became missing/default", got, err)
	}
}

func TestVideoCatalogBillingGroupValidationDeadlineAndSafeErrors(t *testing.T) {
	db := videoBillingGroupDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	tx := db.Begin()
	defer tx.Rollback()
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("video_billing_group_count", func(*gorm.DB) { queries++ }); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
		if _, err := ReadVideoBillingGroupTx(context.Background(), invalid, 1); !errors.Is(err, ErrVideoCatalogInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := ReadVideoBillingGroupTx(nil, tx, 1); !errors.Is(err, ErrVideoCatalogInvalid) {
		t.Fatal(err)
	}
	if _, err := ReadVideoBillingGroupTx(context.Background(), tx, 0); !errors.Is(err, ErrVideoCatalogInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadVideoBillingGroupTx(canceled, tx, 1); !errors.Is(err, context.Canceled) || queries != 0 {
		t.Fatal(err, queries)
	}
	marker := "synthetic-private-group-error"
	if err := db.Callback().Query().Before("gorm:query").Register("video_billing_group_error", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVideoBillingGroupTx(context.Background(), tx, 1); !errors.Is(err, ErrVideoCatalogUnavailable) || strings.Contains(err.Error(), marker) || strings.Contains(logs.String(), marker) || strings.Contains(logs.String(), "SELECT") {
		t.Fatal("diagnostic escaped", err, logs.String())
	}
	if err := db.Callback().Query().Remove("video_billing_group_error"); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("video_billing_group_timeout", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := ReadVideoBillingGroupTx(context.Background(), tx, 1); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("SQL deadline missing", err, time.Since(start))
	}
}

func TestVideoCatalogLegacyWhitelistKeepsKeyModeGate(t *testing.T) {
	for _, tc := range []struct {
		limits, model string
		want          bool
	}{{"", "x", true}, {" \t", "x", true}, {" seedance-2.0 , seedance-2.5 ", "seedance-2.0", true}, {"seedance-2.0", "Seedance-2.0", false}, {"seedance-2.0", "seedance-2.0 ", false}, {"seedance-*", "seedance-2.0", false}, {"doubao-seedance-2-0", "seedance-2.0", false}} {
		key := &APIKey{ModelAccessMode: ModelAccessLegacy, PolicyVersion: 1, ModelLimits: tc.limits}
		if LegacyModelAllowed(tc.limits, tc.model) != tc.want || key.AllowsModel(tc.model) != tc.want {
			t.Fatal(tc)
		}
	}
	group := uint64(8)
	for _, key := range []*APIKey{nil, {}, {ModelAccessMode: "inherit_group", PolicyVersion: 1}, {ModelAccessMode: ModelAccessLegacy}, {ModelAccessMode: ModelAccessLegacy, PolicyVersion: 1, RoutingGroupID: &group}} {
		if key.AllowsModel("seedance-2.0") {
			t.Fatal("pure predicate bypassed key mode validation")
		}
	}
}
