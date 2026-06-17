package invocation

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testSchema = `
CREATE TABLE invocations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL, user_id INTEGER, api_key_id INTEGER NOT NULL,
    request_id TEXT NOT NULL UNIQUE, model TEXT NOT NULL,
    channel_id INTEGER NOT NULL DEFAULT 0, provider TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0,
    is_stream INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd NUMERIC NOT NULL DEFAULT 0, usage_source TEXT NOT NULL DEFAULT '',
    frt_ms INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
    upstream_request_id TEXT NOT NULL DEFAULT '', error_brief TEXT NOT NULL DEFAULT '',
    payload_object_key TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func setup(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1) // :memory: 每连接独立库（已知陷阱）
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

func TestRecorderAsyncBatchWrite(t *testing.T) {
	db := setup(t)
	rec := NewRecorder(db, testLog())
	for i := 0; i < 150; i++ { // 跨多个 batch
		rec.Record(&Invocation{
			OrgID: 1, APIKeyID: 1, RequestID: reqID(i), Model: "m",
			Status: StatusSuccess, StatusCode: 200,
			CostUSD: decimal.RequireFromString("0.001"),
		})
	}
	rec.Close() // 排空保证全部落库
	var n int64
	db.Model(&Invocation{}).Count(&n)
	if n != 150 {
		t.Fatalf("rows = %d, want 150", n)
	}
	if rec.Dropped() != 0 {
		t.Fatalf("dropped = %d, want 0", rec.Dropped())
	}
}

func TestRecorderCloseIdempotentAndPostCloseNoop(t *testing.T) {
	db := setup(t)
	rec := NewRecorder(db, testLog())
	rec.Close()
	rec.Close()                                                                                       // 幂等不 panic
	rec.Record(&Invocation{OrgID: 1, APIKeyID: 1, RequestID: "x", Model: "m", Status: StatusSuccess}) // 关闭后投递安全 no-op
}

func TestPurgeOlderThan(t *testing.T) {
	db := setup(t)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	recent := time.Now().UTC()
	for i := 0; i < 25; i++ {
		ts := old
		if i >= 20 {
			ts = recent
		}
		db.Create(&Invocation{OrgID: 1, APIKeyID: 1, RequestID: reqID(i), Model: "m",
			Status: StatusSuccess, CreatedAt: ts})
	}
	deleted, err := PurgeOlderThan(db, time.Now().UTC().Add(-30*24*time.Hour), 7) // 小批量验证分批循环
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 20 {
		t.Fatalf("deleted = %d, want 20", deleted)
	}
	var n int64
	db.Model(&Invocation{}).Count(&n)
	if n != 5 {
		t.Fatalf("remaining = %d, want 5", n)
	}
	// 幂等：再跑无可删
	deleted, err = PurgeOlderThan(db, time.Now().UTC().Add(-30*24*time.Hour), 7)
	if err != nil || deleted != 0 {
		t.Fatalf("second purge = %d, %v", deleted, err)
	}
}

func reqID(i int) string { return fmt.Sprintf("req-%d", i) }
