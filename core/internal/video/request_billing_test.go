package video

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRequestBillingSeparatesAdjustedTokensFromReportedUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	must(t, err)
	sqlDB, err := db.DB()
	must(t, err)
	defer sqlDB.Close()
	// Hand-written read-model schema; no ORM schema mutation.
	must(t, db.Exec(`
CREATE TABLE video_billing_snapshots(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,metering_json TEXT,hold_usd NUMERIC,retail_usd_per_1m NUMERIC,effective_ratio NUMERIC,created_at DATETIME,billing_schema_version TEXT NOT NULL);
CREATE TABLE video_task_parameters(org_id INTEGER,task_id TEXT,parameters_json TEXT);
CREATE TABLE video_billing_entries(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,state TEXT,captured_usd NUMERIC,released_usd NUMERIC,completion_tokens INTEGER,created_at DATETIME);
CREATE TABLE video_adjustment_applications(id INTEGER PRIMARY KEY,org_id INTEGER,task_id TEXT,net_usd NUMERIC,customer_delta_usd NUMERIC,completion_tokens INTEGER,created_at DATETIME);
INSERT INTO video_billing_snapshots VALUES(1,1,'task_test','{}',1,8,1,'2026-10-16 00:00:00','usd-decimal-v1');
INSERT INTO video_billing_entries VALUES(1,1,'task_test','settled',0.0008,0.9992,100,'2026-10-16 00:00:01');
INSERT INTO video_adjustment_applications VALUES(1,1,'task_test',0.0016,0.0008,200,'2026-10-16 00:00:02');
INSERT INTO video_adjustment_applications VALUES(2,2,'task_test',999,999,999,'2026-10-16 00:00:03');
`).Error)
	completion, total := int64(100), int64(999)
	b, err := requestBilling(db, 1, State{TaskID: "task_test", BillingState: "settled", ObservedCompletionTokens: &completion, TotalTokens: &total})
	must(t, err)
	if b.ActualUsage == nil || *b.ActualUsage.CompletionTokens != 100 || *b.ActualUsage.TotalTokens != 999 {
		t.Fatal("adjustment rewrote reported evidence")
	}
	if b.BillableTokens == nil || *b.BillableTokens != 200 || b.NetChargedAmount == nil || *b.NetChargedAmount != "0.00160000" || *b.SettledAmount != "0.00080000" {
		t.Fatalf("wrong confirmed adjustment projection: %+v", b)
	}
}
