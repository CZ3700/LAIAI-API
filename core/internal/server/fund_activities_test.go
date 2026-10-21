package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fundFixture(t *testing.T) (*gin.Engine, *gorm.DB, uint64) {
	t.Helper()
	f, db := setupServer(t)
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sql.Close() })
	if e := db.Exec(fundActivityTestSchema).Error; e != nil {
		t.Fatal(e)
	}
	var e error
	db, e = ledger.EnableActivityTracking(db)
	if e != nil {
		t.Fatal(e)
	}
	f.srv.db = db
	var account uint64
	if e := db.Transaction(func(tx *gorm.DB) error {
		a, e := ledger.EnsureAccount(tx, 10, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		account = a.ID
		if _, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "topup1", AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100), RefType: "topup", RefID: "=1+1"}); e != nil {
			return e
		}
		h, _, e := ledger.CreateHold(tx, ledger.HoldInput{IdempotencyKey: "hold1", AccountID: a.ID, Amount: decimal.NewFromInt(10), RefType: "video", RefID: "task_one"})
		if e != nil {
			return e
		}
		_, e = ledger.Capture(tx, h.ID, decimal.NewFromInt(4), "")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	r := gin.New()
	g := r.Group("/api", func(c *gin.Context) {
		org := uint64(10)
		if c.GetHeader("Test-Org") == "20" {
			org = 20
		}
		user := uint64(1)
		if c.GetHeader("Test-User") == "2" {
			user = 2
		}
		c.Set("claims", &identity.SessionClaims{OrgID: org, UserID: user})
	})
	f.srv.registerFundActivities(g)
	return r, db, account
}
func fundCall(t *testing.T, r *gin.Engine, path, org string, want int) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Test-Org", org)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	var body map[string]json.RawMessage
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	return body
}
func TestFundActivitiesTenantSummaryAndExport(t *testing.T) {
	r, _, _ := fundFixture(t)
	body := fundCall(t, r, "/api/fund-activities", "10", 200)
	var rows []ledger.Movement
	if e := json.Unmarshal(body["items"], &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 4 {
		t.Fatal("missing movements", len(rows))
	}
	// The client cannot choose the org with a query parameter.
	body = fundCall(t, r, "/api/fund-activities?org_id=10", "20", 200)
	if string(body["items"]) != "[]" {
		t.Fatal("cross tenant collection")
	}
	fundCall(t, r, "/api/fund-activities/1", "20", 404)
	body = fundCall(t, r, "/api/fund-summary", "10", 200)
	if string(body["posted_balance"]) != `"96"` || string(body["held_amount"]) != `"0"` || string(body["available_balance"]) != `"96"` {
		t.Fatalf("balance %s %s %s", body["posted_balance"], body["held_amount"], body["available_balance"])
	}
	var sums []struct {
		Direction string `json:"direction"`
		Amount    string `json:"amount"`
	}
	_ = json.Unmarshal(body["movements"], &sums)
	found := false
	for _, s := range sums {
		if s.Direction == "debit" {
			found = true
			if s.Amount != "4" {
				t.Fatal("freeze counted as debit")
			}
		}
	}
	if !found {
		t.Fatal("debit missing")
	}
	preview := httptest.NewRecorder()
	r.ServeHTTP(preview, httptest.NewRequest("GET", "/api/fund-exports", nil))
	if preview.Code != 404 {
		t.Fatal("synchronous export bypass remains")
	}
	fundCall(t, r, "/api/fund-activities?direction=invalid", "10", 422)
	fundCall(t, r, "/api/fund-summary?from=not-a-date", "10", 422)
}
func TestFundOperationsFailedNotCountedAsConsumption(t *testing.T) {
	r, db, account := fundFixture(t)
	_, e := ledger.RunOperation(db, ledger.OperationIntent{OrgID: 10, IdempotencyKey: "failed_debit", Type: "admin_debit", Direction: "debit", RequestedAmount: decimal.NewFromInt(200)}, func(tx *gorm.DB) error {
		_, _, e := ledger.Post(tx, ledger.PostInput{IdempotencyKey: "failed-post", AccountID: account, Kind: ledger.KindDebit, Delta: decimal.NewFromInt(-200), RefType: "admin_debit"})
		return e
	})
	if e == nil {
		t.Fatal("expected insufficient funds")
	}
	body := fundCall(t, r, "/api/fund-activities?view=operations&status=failed", "10", 200)
	var ops []ledger.Operation
	_ = json.Unmarshal(body["items"], &ops)
	if len(ops) != 1 || ops[0].Status != "failed" {
		t.Fatal("failed attempt missing")
	}
	body = fundCall(t, r, "/api/fund-activities?status=failed", "10", 200)
	if string(body["items"]) != "[]" {
		t.Fatal("failed attempt fabricated movement")
	}
	body = fundCall(t, r, "/api/fund-summary?status=failed", "10", 200)
	if string(body["movements"]) != "[]" {
		t.Fatal("failed included in financial sums")
	}
}

const fundExportTestSchema = `CREATE TABLE ledger_export_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,user_id INTEGER NOT NULL,idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,filter_json TEXT NOT NULL,max_movement_id INTEGER NOT NULL,cursor_id INTEGER NOT NULL,row_count INTEGER NOT NULL,status TEXT NOT NULL,error_code TEXT NOT NULL,csv_data TEXT NOT NULL,created_at DATETIME NOT NULL,expires_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,user_id,idempotency_key));`

func TestFundExportSnapshotReplayResumeAndExpiry(t *testing.T) {
	r, db, account := fundFixture(t)
	if e := db.Exec(fundExportTestSchema).Error; e != nil {
		t.Fatal(e)
	}
	create := func(body string, want int) fundExportJob {
		req := httptest.NewRequest("POST", "/api/fund-exports", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "export-one")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("create export %d: %s", w.Code, w.Body.String())
		}
		var job fundExportJob
		_ = json.Unmarshal(w.Body.Bytes(), &job)
		return job
	}
	job := create(`{}`, 202)
	replay := create(`{}`, 202)
	if job.ID == 0 || job.ID != replay.ID {
		t.Fatal("export idempotency")
	}
	create(`{"direction":"debit"}`, 409)
	// Later activity cannot enter the frozen export, even while the worker is
	// interrupted and then resumed by a different Server instance.
	if e := db.Transaction(func(tx *gorm.DB) error {
		_, _, e := ledger.Post(tx, ledger.PostInput{IdempotencyKey: "later", AccountID: account, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(2), RefType: "topup", RefID: "later-credit"})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	worker := &Server{db: db, now: time.Now}
	if e := worker.processFundExportChunk(context.Background(), job.ID); e != nil {
		t.Fatal(e)
	}
	var stored fundExportJob
	db.First(&stored, job.ID)
	if stored.Status != "success" || stored.RowCount != 4 || strings.Contains(stored.CSVData, "later-credit") {
		t.Fatalf("snapshot exported wrong rows %#v", stored)
	}
	if !strings.Contains(stored.CSVData, "'=1+1") {
		t.Fatal("CSV formula not escaped")
	}
	path := fmt.Sprintf("/api/fund-exports/%d", job.ID)
	fundCall(t, r, path, "20", 404)
	fundCall(t, r, path+"/download", "20", 404)
	body := fundCall(t, r, path+"/download", "10", 200)
	if string(body["rows"]) != "4" {
		t.Fatal("download row count")
	}
	foreign := httptest.NewRequest("GET", path+"/download", nil)
	foreign.Header.Set("Test-User", "2")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, foreign)
	if response.Code != 404 {
		t.Fatal("same org non-owner downloaded export")
	}
	if e := db.Model(&fundExportJob{}).Where("id = ?", job.ID).Update("expires_at", time.Now().UTC().Add(-time.Second)).Error; e != nil {
		t.Fatal(e)
	}
	fundCall(t, r, path+"/download", "10", 404)
}

func TestFundExportMultipleChunksResumeWithoutDuplicates(t *testing.T) {
	_, db, account := fundFixture(t)
	if e := db.Exec(fundExportTestSchema).Error; e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	rows := make([]ledger.Movement, 1001)
	for i := range rows {
		rows[i] = ledger.Movement{OrgID: 10, AccountID: account, AccountType: ledger.AccountUserCredit, OperationID: 1, Type: "topup", Direction: "credit", Status: "success", Currency: "USD", SourceType: "fixture", SourceID: uint64(i + 1), Component: "posted", RefType: "fixture", RefID: fmt.Sprint(i), Quality: "fixture", OccurredAt: &now, CreatedAt: now}
	}
	if e := db.CreateInBatches(rows, 100).Error; e != nil {
		t.Fatal(e)
	}
	var max uint64
	db.Model(&ledger.Movement{}).Select("MAX(id)").Scan(&max)
	job := fundExportJob{OrgID: 10, UserID: 1, IdempotencyKey: "chunks", FilterJSON: `{"Limit":1000}`, MaxMovementID: max, Status: "pending", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UpdatedAt: now}
	if e := db.Create(&job).Error; e != nil {
		t.Fatal(e)
	}
	worker := &Server{db: db, now: time.Now}
	if e := worker.processFundExportChunk(context.Background(), job.ID); e != nil {
		t.Fatal(e)
	}
	db.First(&job, job.ID)
	if job.Status != "processing" || job.RowCount != 1000 {
		t.Fatal("first chunk not saved", job.Status, job.RowCount)
	}
	restarted := &Server{db: db, now: time.Now}
	if e := restarted.processFundExportChunk(context.Background(), job.ID); e != nil {
		t.Fatal(e)
	}
	db.First(&job, job.ID)
	if job.Status != "success" || job.RowCount != 1005 {
		t.Fatal("resume did not finish", job.Status, job.RowCount)
	}
	parsed, e := csv.NewReader(strings.NewReader(job.CSVData)).ReadAll()
	if e != nil || len(parsed) != 1006 {
		t.Fatal("CSV row count", len(parsed), e)
	}
	seen := map[string]bool{}
	for _, row := range parsed[1:] {
		if seen[row[0]] {
			t.Fatal("duplicate CSV movement")
		}
		seen[row[0]] = true
	}
}

const fundActivityTestSchema = `
CREATE TABLE ledger_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL,
    type TEXT NOT NULL,
    shard INTEGER NOT NULL DEFAULT 0,
    posted_balance NUMERIC NOT NULL DEFAULT 0,
    held_amount NUMERIC NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, type, shard)
);
CREATE TABLE ledger_account_shards (
    org_id INTEGER NOT NULL,
    type TEXT NOT NULL,
    shard_count INTEGER NOT NULL DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, type)
);
CREATE TABLE ledger_journal (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    account_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    delta NUMERIC NOT NULL,
    balance_after NUMERIC NOT NULL,
    hold_id INTEGER,
    ref_type TEXT NOT NULL DEFAULT '',
    ref_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE ledger_holds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key TEXT NOT NULL UNIQUE,
    account_id INTEGER NOT NULL,
    amount NUMERIC NOT NULL,
    captured_amount NUMERIC NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 0,
    ref_type TEXT NOT NULL DEFAULT '',
    ref_id TEXT NOT NULL DEFAULT '',
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ledger_operations(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,idempotency_key TEXT NOT NULL,request_hash TEXT NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,requested_amount NUMERIC NOT NULL,currency TEXT NOT NULL,status TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,failure_code TEXT NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,idempotency_key));
CREATE TABLE ledger_operation_events(id INTEGER PRIMARY KEY AUTOINCREMENT,operation_id INTEGER NOT NULL,status TEXT NOT NULL,code TEXT NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE ledger_movements(id INTEGER PRIMARY KEY AUTOINCREMENT,org_id INTEGER NOT NULL,account_id INTEGER NOT NULL,account_type TEXT NOT NULL,operation_id INTEGER NOT NULL,type TEXT NOT NULL,direction TEXT NOT NULL,status TEXT NOT NULL,currency TEXT NOT NULL,amount NUMERIC NOT NULL,posted_delta NUMERIC NOT NULL,held_delta NUMERIC NOT NULL,available_delta NUMERIC NOT NULL,journal_id INTEGER,hold_id INTEGER,source_type TEXT NOT NULL,source_id INTEGER NOT NULL,component TEXT NOT NULL,ref_type TEXT NOT NULL,ref_id TEXT NOT NULL,quality TEXT NOT NULL,original_movement_id INTEGER,original_journal_id INTEGER,original_hold_id INTEGER,correction_proposal_id INTEGER,correction_review_id INTEGER,occurred_at DATETIME,created_at DATETIME NOT NULL,UNIQUE(source_type,source_id,component));`
