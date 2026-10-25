package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func invocationExportAcceptancePreservedFacts(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	// Extend only this private schema with tracked migrations. There is no
	// copied test DDL and no write to the caller's shared ark_test database.
	for _, name := range []string{"0002_ledger.sql", "0003_relay.sql", "0006_tasks.sql", "0028_ledger_shards.sql", "0056_invocation_payloads.sql", "0057_invocation_payload_staging.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(body)).Error; err != nil {
			t.Fatalf("private preservation migration %s failed", name)
		}
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		account, err := ledger.EnsureAccount(tx, 1001, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		if _, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "export-preserved-credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.RequireFromString("123.12345678")}); err != nil {
			return err
		}
		hold, _, err := ledger.CreateHold(tx, ledger.HoldInput{IdempotencyKey: "export-preserved-hold", AccountID: account.ID, Amount: decimal.RequireFromString("1.23456789"), RefType: "task", RefID: "export-preserved-task"})
		if err != nil {
			return err
		}
		statements := []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO api_keys(id,org_id,name,prefix,key_hash) VALUES(11,1001,'export-preserved-key','test-prefix',?)", []any{strings.Repeat("a", 64)}},
			{"INSERT INTO channels(id,name,provider,base_url,credential_enc,models) VALUES(201,'export-preserved-channel','test','https://upstream.invalid','synthetic-credential-ciphertext','fixture-model')", nil},
			{"INSERT INTO tasks(task_id,org_id,api_key_id,model,channel_id,provider,status,hold_id,price_out_usd_per_1m) VALUES('export-preserved-task',1001,11,'fixture-model',201,'test','queued',?,'0.123456')", []any{hold.ID}},
			{"INSERT INTO model_prices(model,input_usd_per_1m,output_usd_per_1m) VALUES('export-preserved-model','1.234567','2.345678')", nil},
			{"INSERT INTO invocation_payloads(payload_id,org_id,request_id,correlation_key,direction,purpose,schema_version,body_format,wrapped_key,nonce,redaction_version,capture_status,created_at,expires_at) VALUES('export-preserved-payload',1001,'export-preserved-request',?,'client_request','history',1,'json','synthetic-wrapped-key','nonce','test','available',?,?)", []any{strings.Repeat("b", 64), invocationAcceptanceNow, invocationAcceptanceNow.Add(24 * time.Hour)}},
			{"INSERT INTO invocation_payload_staging(payload_id,ciphertext,next_attempt_at,created_at) VALUES('export-preserved-payload','synthetic-ciphertext',?,?)", []any{invocationAcceptanceNow, invocationAcceptanceNow}},
		}
		for _, statement := range statements {
			if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("could not create nonempty preservation facts", err)
	}
	return []string{"ledger_accounts", "ledger_journal", "ledger_holds", "api_keys", "channels", "tasks", "model_prices", "invocation_payloads", "invocation_payload_staging", "invocations", "ext_settings"}
}

func invocationExportAcceptanceFingerprints(t *testing.T, db *gorm.DB, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, table := range tables { // fixed internal table names only
		rows, err := db.WithContext(ctx).Table(table).Rows()
		if err != nil {
			t.Fatal("cannot read preservation snapshot")
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal("cannot inspect preservation columns")
		}
		var encoded []string
		for rows.Next() {
			values, refs := make([]sql.RawBytes, len(columns)), make([]any, len(columns))
			for i := range refs {
				refs[i] = &values[i]
			}
			if err := rows.Scan(refs...); err != nil {
				_ = rows.Close()
				t.Fatal("cannot scan preservation facts")
			}
			row := make([]any, len(values))
			for i, value := range values {
				if value != nil {
					row[i] = string(value)
				}
			}
			data, _ := json.Marshal(row)
			encoded = append(encoded, string(data))
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil || len(encoded) == 0 {
			t.Fatal("preservation evidence must be complete and nonempty", table)
		}
		sort.Strings(encoded)
		hash := sha256.Sum256([]byte(strings.Join(encoded, "\n")))
		out[table] = fmt.Sprintf("%d:%s", len(encoded), hex.EncodeToString(hash[:]))
	}
	return out
}

func TestMySQLInvocationExportAcceptance237FiltersPagesAndPreservation(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)
	invocationAcceptanceSeed(t, h.db, rows)
	tables := invocationExportAcceptancePreservedFacts(t, h.db)
	before := invocationExportAcceptanceFingerprints(t, h.db, tables)
	for _, currency := range []string{"USD", "CNY"} {
		w := invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"display_currency": {currency}})
		invocationExportAcceptanceAssert(t, w, true, rows, nil, currency)
		for _, size := range []int{20, 50, 100} {
			var seen []string
			for page := 1; page <= (len(rows)+size-1)/size; page++ {
				out := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"display_currency": {currency}, "page_size": {strconv.Itoa(size)}, "page": {strconv.Itoa(page)}})
				invocationExportAcceptanceJSONExact(t, out.Items, rows)
				_, totals := invocationAcceptanceOracle(t, rows, nil)
				invocationAcceptanceAssertTotals(t, out.Totals, totals)
				if out.Query.FilterHash != w.Header().Get("X-Invocation-Filter-Hash") {
					t.Fatal("pagination or currency changed the authorized filter identity")
				}
				seen = append(seen, invocationAcceptanceIDs(t, out.Items)...)
			}
			ids, _ := invocationAcceptanceOracle(t, rows, nil)
			invocationAcceptanceEqualIDs(t, seen, ids)
		}
	}
	start := time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	cases := []struct {
		name   string
		values url.Values
		accept func(invocationAcceptanceRow) bool
	}{
		{"org", url.Values{"org_id": {"1002"}}, func(r invocationAcceptanceRow) bool { return r.Org == "B" }},
		{"model", url.Values{"model": {"fixture-model-1"}}, func(r invocationAcceptanceRow) bool { return r.Model == "fixture-model-1" }},
		{"key", url.Values{"api_key_id": {"21"}}, func(r invocationAcceptanceRow) bool { return r.Key == "B1" }},
		{"request", url.Values{"request_id": {rows[2].RequestID}}, func(r invocationAcceptanceRow) bool { return r.Ordinal == 3 }},
		{"historical_modality", url.Values{"modality": {""}}, func(r invocationAcceptanceRow) bool { return r.Modality == "" }},
		{"video", url.Values{"modality": {"video"}}, func(r invocationAcceptanceRow) bool { return r.Modality == "video" }},
		{"non_success", url.Values{"result": {"failed"}}, func(r invocationAcceptanceRow) bool { return r.Status != "success" }},
		{"status", url.Values{"status": {"no_channel"}}, func(r invocationAcceptanceRow) bool { return r.Status == "no_channel" }},
		{"stream", url.Values{"is_stream": {"true"}}, func(r invocationAcceptanceRow) bool { return r.IsStream }},
		{"channel", url.Values{"channel_id": {"201"}}, func(r invocationAcceptanceRow) bool { return r.ChannelID == 201 }},
		{"combined", url.Values{"org_id": {"1002"}, "model": {"fixture-model-3"}, "result": {"success"}}, func(r invocationAcceptanceRow) bool {
			return r.Org == "B" && r.Model == "fixture-model-3" && r.Status == "success"
		}},
		{"shanghai_exclusive_date", url.Values{"timezone": {"Asia/Shanghai"}, "since": {"2026-10-01"}, "until": {"2026-10-02"}}, func(r invocationAcceptanceRow) bool { return !r.CreatedAt.Before(start) && r.CreatedAt.Before(end) }},
		{"empty", url.Values{"request_id": {"ffffffffffffffffffffffffffffffff"}}, func(invocationAcceptanceRow) bool { return false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := invocationExportAcceptanceCall(h, "superadmin", true, tc.values)
			ids := invocationExportAcceptanceAssert(t, w, true, rows, tc.accept, "USD")
			for _, size := range []int{20, 50, 100} {
				var found []string
				for page := 1; page <= max(1, (len(ids)+size-1)/size); page++ {
					q := url.Values{}
					for k, v := range tc.values {
						q[k] = append([]string(nil), v...)
					}
					q.Set("page_size", strconv.Itoa(size))
					q.Set("page", strconv.Itoa(page))
					out := invocationAcceptanceRead(t, h, "superadmin", true, q)
					invocationExportAcceptanceJSONExact(t, out.Items, rows)
					_, want := invocationAcceptanceOracle(t, rows, tc.accept)
					invocationAcceptanceAssertTotals(t, out.Totals, want)
					if out.Query.FilterHash != w.Header().Get("X-Invocation-Filter-Hash") {
						t.Fatal("export filter differs from list")
					}
					found = append(found, invocationAcceptanceIDs(t, out.Items)...)
				}
				invocationAcceptanceEqualIDs(t, found, ids)
			}
		})
	}
	for _, subject := range []string{"A", "B", "C", "A-in-B"} {
		org := subject
		if org == "A-in-B" {
			org = "B"
		}
		invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, subject, false, nil), false, rows, func(r invocationAcceptanceRow) bool { return r.Org == org }, "USD")
	}
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "root", true, nil), true, rows, nil, "USD")
	for _, currency := range []string{"USD", "CNY"} {
		invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"request_id": {rows[1].RequestID}, "display_currency": {currency}}), true, rows, func(r invocationAcceptanceRow) bool { return r.Ordinal == 2 }, currency)
	}
	if after := invocationExportAcceptanceFingerprints(t, h.db, tables); !reflect.DeepEqual(before, after) {
		t.Fatal("read-only export/list changed nonempty money, Key, Task, pricing, payload or metadata facts")
	}
}

func TestMySQLInvocationExportAcceptanceExactBeyondInt64(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)[:2]
	for i := range rows {
		rows[i].CostUSD = "0.00000001"
		rows[i].PromptTokens, rows[i].CompletionTokens = "9223372036854775807", "9223372036854775807"
		rows[i].CachedTokens, rows[i].ReasoningTokens = "9223372036854775807", "9223372036854775807"
	}
	invocationAcceptanceSeed(t, h.db, rows)
	for _, currency := range []string{"USD", "CNY"} {
		w := invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"display_currency": {currency}})
		invocationExportAcceptanceAssert(t, w, true, rows, nil, currency)
		for _, kind := range []string{"Prompt", "Completion", "Cached", "Reasoning"} {
			if w.Header().Get("X-Invocation-"+kind+"-Tokens") != "18446744073709551614" {
				t.Fatal("MySQL SUM was narrowed through int64 or float64")
			}
		}
		out := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"display_currency": {currency}})
		invocationExportAcceptanceJSONExact(t, out.Items, rows)
	}
}

func TestMySQLInvocationExportAcceptanceBoundariesAndActualPlans(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	base := invocationAcceptanceLoad(t)[0]
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, nil), true, nil, nil, "USD")
	rows := make([]invocationAcceptanceRow, 5001)
	for i := range rows {
		rows[i] = base
		rows[i].Ordinal, rows[i].RequestID = i+1, fmt.Sprintf("%032x", i+1)
		rows[i].CostUSD = "0.00000001"
		rows[i].PromptTokens, rows[i].CompletionTokens, rows[i].CachedTokens, rows[i].ReasoningTokens = "1", "2", "0", "0"
	}
	invocationAcceptanceSeed(t, h.db, rows[:5000])
	type statement struct {
		sql  string
		args []any
	}
	var statements []statement
	name := "test:export-acceptance-capture-real-selects"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "invocations" {
			statements = append(statements, statement{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	w := invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"display_currency": {"CNY"}})
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	invocationExportAcceptanceAssert(t, w, true, rows[:5000], nil, "CNY")
	if len(statements) != 2 || !strings.Contains(strings.ToUpper(statements[0].sql), "COUNT(*)") || strings.Contains(strings.ToUpper(statements[1].sql), "COUNT(*)") {
		t.Fatal("export was not one aggregate plus one complete row read")
	}
	for _, forbidden := range []string{"payload_object_key", "upstream_request_id", "error_brief", "ciphertext", "credential"} {
		if strings.Contains(strings.ToLower(statements[1].sql), forbidden) {
			t.Fatal("export fetched data outside its safe metadata projection")
		}
	}
	t.Logf("5000 actual CSV rows: bytes=%d elapsed=%s allocated_bytes=%d heap_before=%d heap_after=%d (single-run observation, not a capacity claim)", w.Body.Len(), elapsed, after.TotalAlloc-before.TotalAlloc, before.HeapAlloc, after.HeapAlloc)
	for i, statement := range statements {
		if !strings.Contains(statement.sql, "MAX_EXECUTION_TIME(") {
			t.Fatal("actual MySQL export query omitted its server time bound")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var plan string
		err := h.db.WithContext(ctx).Raw("EXPLAIN FORMAT=JSON "+statement.sql, statement.args...).Scan(&plan).Error
		cancel()
		if err != nil || !strings.Contains(plan, "invocations") {
			t.Fatal("could not EXPLAIN the actual captured export SQL")
		}
		// A full-date export may rationally scan this small table; do not force
		// an index or mislabel ALL as failure. Preserve the actual plan evidence.
		t.Logf("actual export SELECT %d EXPLAIN: %s", i+1, plan)
	}
	invocationAcceptanceSeed(t, h.db, rows[5000:])
	statements = nil
	w = invocationExportAcceptanceCall(h, "superadmin", true, nil)
	invocationExportAcceptanceFailure(t, w, 422)
	if !strings.Contains(w.Body.String(), `"code":"export_limit_exceeded"`) || len(statements) != 1 || !strings.Contains(strings.ToUpper(statements[0].sql), "COUNT(*)") {
		t.Fatal("5001 rows were truncated or the unnecessary detail SELECT was issued")
	}
	// A narrow next request proves rejection released every gate slot.
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"request_id": {rows[0].RequestID}}), true, rows, func(r invocationAcceptanceRow) bool { return r.Ordinal == 1 }, "USD")
}

func TestMySQLInvocationExportAcceptanceTwoConnectionSnapshot(t *testing.T) {
	h, writer := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)[:2]
	invocationAcceptanceSeed(t, h.db, rows)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var reached, readonly atomic.Bool
	unblock := func() { once.Do(func() { close(release) }) }
	name := "test:export-acceptance-real-rr-barrier"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Table != "invocations" || !strings.Contains(tx.Statement.SQL.String(), "COUNT(*)") || !reached.CompareAndSwap(false, true) {
			return
		}
		err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE invocations SET cost_usd=cost_usd WHERE id=1").Error
		var driverErr *mysqlDriver.MySQLError
		if !errors.As(err, &driverErr) || driverErr.Number != 1792 {
			tx.AddError(errors.New("export transaction is not actually read-only"))
		} else {
			readonly.Store(true)
		}
		close(entered)
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	var requests sync.WaitGroup
	requests.Add(1)
	go func() {
		defer requests.Done()
		done <- invocationExportAcceptanceCall(h, "superadmin", true, url.Values{"display_currency": {"CNY"}})
	}()
	t.Cleanup(func() { unblock(); requests.Wait(); _ = h.db.Callback().Query().Remove(name) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("actual export aggregate did not reach the concurrency barrier")
	}
	next := rows[0]
	next.Ordinal, next.RequestID, next.CostUSD = 99, fmt.Sprintf("%032x", 99), "0.75"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := writer.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE invocations SET cost_usd='0.5' WHERE id=1").Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM invocations WHERE id=2").Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO invocations(id,org_id,user_id,api_key_id,channel_id,provider,request_id,model,modality,status,is_stream,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,cost_usd,created_at) VALUES(99,1001,5001,11,?,'synthetic-internal-provider',?,?,?,?,?,?,?,?,?,'0.75',?)", next.ChannelID, next.RequestID, next.Model, next.Modality, next.Status, next.IsStream, next.PromptTokens, next.CompletionTokens, next.CachedTokens, next.ReasoningTokens, next.CreatedAt).Error
	})
	cancel()
	if err != nil {
		t.Fatal("independent writer did not commit between aggregate and rows", err)
	}
	unblock()
	select {
	case w := <-done:
		invocationExportAcceptanceAssert(t, w, true, rows, nil, "CNY")
	case <-time.After(2 * time.Second):
		t.Fatal("export failed to finish after writer commit")
	}
	if !readonly.Load() {
		t.Fatal("read-only RR positive control was not exercised")
	}
	changed := []invocationAcceptanceRow{rows[0], next}
	changed[0].CostUSD = "0.5"
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, nil), true, changed, nil, "USD")
}

func TestMySQLInvocationExportAcceptanceFailuresCancelAndPoolWait(t *testing.T) {
	h, reader := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)[:2]
	invocationAcceptanceSeed(t, h.db, rows)
	name := "test:export-acceptance-real-row-query-failure"
	var failed atomic.Bool
	if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "invocations" && !strings.Contains(strings.Join(tx.Statement.Selects, " "), "COUNT(*)") && failed.CompareAndSwap(false, true) {
			// Execute an actually invalid MySQL SELECT after a successful real
			// aggregate. The safe handler must not publish its driver's SQL error.
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT `private-driver-secret` FROM invocations")
			tx.Statement.Vars = nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove("test:export-acceptance-real-row-query-failure") })
	w := invocationExportAcceptanceCall(h, "superadmin", true, nil)
	invocationExportAcceptanceFailure(t, w, 503)
	if !failed.Load() {
		t.Fatal("real row SQL failure was not exercised")
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, nil), true, rows, nil, "USD")
	// Cancel only after both real SELECTs have returned their complete rows,
	// proving the handler cannot publish buffered content on a canceled request.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var canceled atomic.Bool
	name = "test:export-acceptance-cancel-after-read"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "invocations" && !strings.Contains(tx.Statement.SQL.String(), "COUNT(*)") && canceled.CompareAndSwap(false, true) {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove("test:export-acceptance-cancel-after-read") })
	w = invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationExportAcceptanceURL(true, nil)).WithContext(ctx))
	invocationExportAcceptanceFailure(t, w, 503)
	if !canceled.Load() {
		t.Fatal("cancel test did not actually read the export rows")
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	pool, err := reader.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	occupied, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	h.server.srv.readDB = reader
	waits := pool.Stats().WaitCount
	ctx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	started := time.Now()
	w = invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationExportAcceptanceURL(true, nil)).WithContext(ctx))
	invocationExportAcceptanceFailure(t, w, 503)
	if pool.Stats().WaitCount == waits || time.Since(started) > time.Second {
		t.Fatal("exhausted read pool did not obey the request deadline")
	}
	var one int
	if err := h.db.Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 {
		t.Fatal("read pool exhaustion blocked the independent primary pool")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	invocationExportAcceptanceAssert(t, invocationExportAcceptanceCall(h, "superadmin", true, nil), true, rows, nil, "USD")
	gate := &h.server.srv.invocationQueries
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.active != 0 || len(gate.subjects) != 0 || len(gate.exports) != 0 {
		t.Fatal("failure/cancel paths retained a query/export capacity slot")
	}
}
