package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var invocationExportTestColumns = []string{"time", "request_id", "model", "status", "modality", "api_key_id",
	"prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "cost_usd", "cost_display", "currency", "cny_per_usd",
	"rate_snapshot_at", "rate_source", "as_of", "filter_hash", "amount_source", "completeness", "usage_presence"}

func invocationExportTestURL(admin bool, values url.Values) string {
	return strings.Replace(invocationAcceptanceURL(admin, values), "?", "/export?", 1)
}

func invocationExportTestRead(t *testing.T, h *invocationAcceptanceHarness, subject string, admin bool, values url.Values) (*httptest.ResponseRecorder, []map[string]string) {
	t.Helper()
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationExportTestURL(admin, values)))
	if w.Code != 200 {
		t.Fatalf("CSV failed: %d %s", w.Code, w.Body.String())
	}
	all, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(all) < 1 {
		t.Fatalf("invalid CSV: %v", err)
	}
	wantHeader := append([]string(nil), invocationExportTestColumns...)
	if admin {
		wantHeader = append(wantHeader, "org_id", "user_id", "channel_id", "provider")
	}
	if !reflect.DeepEqual(all[0], wantHeader) {
		t.Fatalf("CSV column contract changed: %v", all[0])
	}
	rows := make([]map[string]string, 0, len(all)-1)
	for _, record := range all[1:] {
		row := map[string]string{}
		for i, cell := range record {
			row[all[0][i]] = cell
		}
		rows = append(rows, row)
	}
	if w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="invocation-log-`) ||
		w.Header().Get("X-Invocation-Record-Count") != strconv.Itoa(len(rows)) || w.Header().Get("X-Invocation-Filter-Version") != "1" ||
		w.Header().Get("X-Invocation-Amount-Source") != "log_recorded_cost" || w.Header().Get("X-Invocation-Completeness") != "best_effort" ||
		w.Header().Get("X-Invocation-Usage-Presence") != "legacy_not_distinguishable" || w.Header().Get("X-Invocation-Consistency") != "response_snapshot" ||
		w.Header().Get("X-Invocation-Cross-Request-Consistency") != "live" {
		t.Fatal("missing export metadata", w.Header())
	}
	for _, key := range []string{"X-Invocation-As-Of", "X-Rate-Snapshot-At"} {
		if stamp, err := time.Parse(time.RFC3339Nano, w.Header().Get(key)); err != nil || stamp.IsZero() {
			t.Fatal("invalid timestamp header", key)
		}
	}
	for key, values := range w.Header() {
		for _, value := range values {
			for _, r := range value {
				if r < 32 || r > 126 {
					t.Fatal("non-ASCII export response header", key)
				}
			}
		}
	}
	return w, rows
}

func invocationExportTestFailure(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || strings.Contains(w.Header().Get("Content-Type"), "text/csv") || w.Header().Get("Content-Disposition") != "" || w.Header().Get("X-Invocation-Record-Count") != "" || strings.HasPrefix(w.Body.String(), "time,request_id,") {
		t.Fatalf("failure published CSV metadata/body: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestInvocationExportHTTPContractScopeAndExactUsage(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	if err := h.db.Model(&invocation.Invocation{}).Where("id = ?", 1).Update("prompt_tokens", int64(9007199254740993)).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		subject string
		admin   bool
		count   int
	}{{"A", false, 2}, {"superadmin", true, 6}, {"root", true, 6}} {
		t.Run(tc.subject, func(t *testing.T) {
			list := invocationAcceptanceRead(t, h, tc.subject, tc.admin, url.Values{"display_currency": {"CNY"}})
			w, rows := invocationExportTestRead(t, h, tc.subject, tc.admin, url.Values{"display_currency": {"CNY"}})
			if len(rows) != tc.count || w.Header().Get("X-Invocation-Filter-Hash") != list.Query.FilterHash ||
				w.Header().Get("X-Invocation-Cost-USD") != list.Totals.CostUSD || w.Header().Get("X-Invocation-Cost-Display") != list.Totals.CostDisplay ||
				w.Header().Get("X-Invocation-Prompt-Tokens") != list.Totals.PromptTokens || w.Header().Get("X-Invocation-Completion-Tokens") != list.Totals.CompletionTokens ||
				w.Header().Get("X-Invocation-Cached-Tokens") != list.Totals.CachedTokens || w.Header().Get("X-Invocation-Reasoning-Tokens") != list.Totals.ReasoningTokens ||
				w.Header().Get("X-Invocation-Success-Count") != strconv.FormatInt(list.Totals.SuccessCount, 10) ||
				w.Header().Get("X-Invocation-Non-Success-Count") != strconv.FormatInt(list.Totals.NonSuccessCount, 10) {
				t.Fatal("CSV and list describe different filtered records or amounts", w.Header(), list.Totals)
			}
			var sawLarge bool
			for i, row := range rows {
				var requestID string
				_ = json.Unmarshal(list.Items[i]["request_id"], &requestID)
				var exact map[string]string
				if err := json.Unmarshal(list.Items[i]["usage_exact"], &exact); err != nil {
					t.Fatal("v1 precise usage missing", err)
				}
				for _, field := range []string{"prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens"} {
					if exact[field] != row[field] || string(list.Items[i][field]) != row[field] {
						t.Fatal("exact strings changed existing integer fields or CSV", field)
					}
				}
				sawLarge = sawLarge || row["prompt_tokens"] == "9007199254740993"
				if row["request_id"] != requestID || row["currency"] != "CNY" || row["cny_per_usd"] != "6.65" || row["cost_display"] != "0.83125" ||
					row["filter_hash"] != list.Query.FilterHash || row["amount_source"] != "log_recorded_cost" || row["completeness"] != "best_effort" ||
					row["rate_snapshot_at"] != w.Header().Get("X-Rate-Snapshot-At") || row["as_of"] != w.Header().Get("X-Invocation-As-Of") {
					t.Fatal("row lost its fixed metadata/precision", row)
				}
				for _, forbidden := range []string{"payload_object_key", "credential", "cost_cny", "error_brief"} {
					if _, ok := row[forbidden]; ok {
						t.Fatal("export exposed private/unbounded data", forbidden)
					}
				}
				if !tc.admin {
					for _, field := range []string{"org_id", "channel_id", "provider", "attempts", "payload_object_key"} {
						if _, exists := list.Items[i][field]; exists {
							t.Fatal("usage_exact expanded organization visibility", field)
						}
					}
				}
			}
			if !sawLarge || strings.Contains(w.Body.String(), "synthetic-hidden-payload-pointer") || strings.Contains(w.Body.String(), "synthetic-upstream-id") {
				t.Fatal("exact token or secret projection regression")
			}
		})
	}
	w, rows := invocationExportTestRead(t, h, "A", false, url.Values{"api_key_id": {"21"}})
	if len(rows) != 0 || w.Header().Get("X-Invocation-Cost-USD") != "0" || w.Header().Get("X-Invocation-Prompt-Tokens") != "0" {
		t.Fatal("cross-organization Key disclosed rows or zero export lacks totals")
	}
	legacy := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", "/api/usage/export?days=30"))
	if legacy.Code != 200 || legacy.Header().Get("X-Invocation-Filter-Version") != "" || !strings.HasPrefix(legacy.Body.String(), "time,request_id,model,status,status_code,stream,") {
		t.Fatal("unversioned export contract changed", legacy.Code, legacy.Body.String())
	}
}

func TestInvocationExportHTTPStrictQueriesAndAuthorization(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	for _, tc := range []struct {
		name, raw string
		admin     bool
	}{
		{"admin_requires_version", "", true}, {"unknown_version", "filter_version=2", false}, {"empty_version", "filter_version=", false},
		{"malformed_version", "filter_version=%ZZ", false}, {"encoded_version", "filter%5Fversion=2", false},
		{"duplicate_version", "filter_version=1&filter_version=1", false}, {"page", "filter_version=1&page=1", false},
		{"empty_page_size", "filter_version=1&page_size=", true}, {"duplicate_model", "filter_version=1&model=a&model=b", false},
		{"unknown", "filter_version=1&secret-do-not-echo=x", true}, {"legacy_days", "filter_version=1&days=30", false},
		{"org_override", "filter_version=1&org_id=1002", false}, {"channel_override", "filter_version=1&channel_id=201", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, subject := "/api/usage/export?", "A"
			if tc.admin {
				base, subject = "/api/admin/invocations/export?", "superadmin"
			}
			w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, base+tc.raw))
			invocationExportTestFailure(t, w, 422)
			if !strings.Contains(w.Body.String(), `"code":"invalid_query"`) || strings.Contains(w.Body.String(), "secret-do-not-echo") {
				t.Fatal("strict input error changed or reflected parameter", w.Body.String())
			}
		})
	}
	for _, subject := range []string{"A", "admin", ""} {
		w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationExportTestURL(true, nil)))
		status := 403
		if subject == "" {
			status = 401
		}
		invocationExportTestFailure(t, w, status)
	}
	var key string
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		var err error
		key, _, err = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: 1001, Name: "synthetic-export-denial", Scopes: "video:*"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, admin := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, invocationExportTestURL(admin, nil), nil)
		r.Header.Set("Authorization", "Bearer "+key)
		invocationExportTestFailure(t, invocationAcceptanceCall(h, r), 401)
	}
	if err := h.db.Exec("UPDATE users SET role='admin' WHERE id=5004").Error; err != nil {
		t.Fatal(err)
	}
	invocationExportTestFailure(t, invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationExportTestURL(true, nil))), 403)
}

func TestInvocationExportHTTPFixedFXAndFormulaProtection(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	if err := h.db.Model(&invocation.Invocation{}).Where("id = ?", 1).Updates(map[string]any{"model": " \t=SUM(1,2)", "request_id": "@example", "provider": "\r-1+2", "modality": "+danger", "status": "\n=evil", "user_id": nil}).Error; err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	name := "test:export-fx-is-fixed"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Error == nil && tx.Statement.Table == "ext_settings" && changed.CompareAndSwap(false, true) {
			tx.AddError(h.db.Model(&Setting{}).Where("name = ?", "cny_per_usd").Update("value", "7.3").Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	w, rows := invocationExportTestRead(t, h, "superadmin", true, url.Values{"display_currency": {"CNY"}})
	if !changed.Load() || w.Header().Get("X-CNY-Per-USD") != "6.65" || w.Header().Get("X-Invocation-Cost-Display") != "4.9875" {
		t.Fatal("FX changed within export", w.Header())
	}
	var saw bool
	for _, row := range rows {
		if row["request_id"] == "'@example" {
			saw = true
			if row["model"] != "' \t=SUM(1,2)" || row["provider"] != "'\r-1+2" || row["status"] != "'\n=evil" || row["modality"] != "'+danger" || row["user_id"] != "" {
				t.Fatal("untrusted spreadsheet formula or nil user projection changed", row)
			}
		}
		if row["cny_per_usd"] != "6.65" || row["cost_display"] != "0.83125" || row["rate_source"] != "setting" {
			t.Fatal("mixed FX rows", row)
		}
	}
	if !saw {
		t.Fatal("did not exercise formula protection")
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	next, _ := invocationExportTestRead(t, h, "A", false, url.Values{"display_currency": {"CNY"}})
	if next.Header().Get("X-CNY-Per-USD") != "7.3" {
		t.Fatal("new request did not take a fresh FX snapshot")
	}
	for _, value := range []string{"=1+1", " \uFEFF@danger", "\t\r-7", "\nplain"} {
		if !strings.HasPrefix(invocationExportText(value), "'") {
			t.Fatal("formula/control prefix not protected")
		}
	}
}

func TestInvocationExportHTTPRowLimitIsExplicitAndReleasesSlots(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	// This SQLite DB belongs only to this test. Money values remain exactly
	// representable here; >MaxInt64 aggregates are accepted on true MySQL.
	if err := h.db.Exec("DELETE FROM invocations").Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]invocation.Invocation, 5001)
	for i := range rows {
		rows[i] = invocation.Invocation{OrgID: 1001, RequestID: "bounded-" + strconv.Itoa(i), Status: "success", Model: "bounded", CostUSD: decimal.RequireFromString("0.125"), PromptTokens: 1, CreatedAt: invocationAcceptanceNow.Add(-24 * time.Hour)}
	}
	if err := h.db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationExportTestURL(false, nil)))
	invocationExportTestFailure(t, w, 422)
	if !strings.Contains(w.Body.String(), `"code":"export_limit_exceeded"`) {
		t.Fatal("row limit not explicit", w.Body.String())
	}
	if err := h.db.Delete(&rows[5000]).Error; err != nil {
		t.Fatal(err)
	}
	w, exported := invocationExportTestRead(t, h, "A", false, nil)
	if len(exported) != 5000 || w.Header().Get("X-Invocation-Cost-USD") != "625" || w.Header().Get("X-Invocation-Prompt-Tokens") != "5000" {
		t.Fatal("boundary export truncated or gate leaked", len(exported), w.Header())
	}
}

func TestInvocationExportHTTPNoPartialCSVOnDatabaseOrSizeFailure(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	name := "test:export-row-read-failure"
	var failed atomic.Bool
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "invocations" && !strings.Contains(tx.Statement.SQL.String(), "COUNT(*)") && failed.CompareAndSwap(false, true) {
			tx.AddError(errors.New("private-driver-secret SELECT raw-filter"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationExportTestURL(false, nil)))
	invocationExportTestFailure(t, w, 503)
	if !failed.Load() || strings.Contains(w.Body.String(), "private-driver") || strings.Contains(w.Body.String(), "raw-filter") {
		t.Fatal("row read failure was not exercised or leaked diagnostics")
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	invocationExportTestRead(t, h, "A", false, nil)
	// SQLite permits oversized legacy text; this verifies the actual HTTP
	// encoder rejects bytes before sending CSV headers, not after streaming.
	if err := h.db.Model(&invocation.Invocation{}).Where("org_id = ?", 1001).Update("model", strings.Repeat("x", 4<<20)).Error; err != nil {
		t.Fatal(err)
	}
	w = invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationExportTestURL(false, nil)))
	invocationExportTestFailure(t, w, 422)
	if !strings.Contains(w.Body.String(), `"code":"export_size_exceeded"`) {
		t.Fatal("oversize CSV was not explicit", w.Body.String())
	}
	if err := h.db.Model(&invocation.Invocation{}).Where("org_id = ?", 1001).Update("model", "small").Error; err != nil {
		t.Fatal(err)
	}
	invocationExportTestRead(t, h, "A", false, nil)
}

func TestInvocationExportSharedReadAndPrincipalExportLimits(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	entered := make(chan struct{}, 4)
	barrier := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(barrier) }) }
	var requests sync.WaitGroup
	name := "test:export-shared-slots"
	if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "ext_settings" {
			entered <- struct{}{}
			select {
			case <-barrier:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock(); requests.Wait(); _ = h.db.Callback().Query().Remove(name) })
	done := make(chan *httptest.ResponseRecorder, 4)
	start := func(subject string, export bool) {
		path := invocationAcceptanceURL(false, nil)
		if export {
			path = invocationExportTestURL(false, nil)
		}
		requests.Add(1)
		go func() {
			defer requests.Done()
			done <- invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, path))
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("admitted request did not enter FX read")
		}
	}
	denied := func(subject string, admin, export bool) {
		path := invocationAcceptanceURL(admin, nil)
		if export {
			path = invocationExportTestURL(admin, nil)
		}
		w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, path))
		invocationExportTestFailure(t, w, 429)
		if w.Header().Get("Retry-After") != "1" {
			t.Fatal("missing retry guidance")
		}
	}
	start("A", true)
	denied("A-in-B", false, true) // Same principal cannot change org to get a second export.
	start("A-in-B", false)        // Rejected export did not reserve the other shared slot.
	denied("A", false, false)
	start("B", true)
	start("C", true)
	denied("root", true, true)
	unblock()
	for i := 0; i < 4; i++ {
		select {
		case w := <-done:
			if w.Code != 200 {
				t.Fatal("admitted request failed", w.Code, w.Body.String())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("admitted request did not finish")
		}
	}
	requests.Wait()
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	g := &h.server.srv.invocationQueries
	g.mu.Lock()
	clean := g.active == 0 && len(g.subjects) == 0 && len(g.exports) == 0
	g.mu.Unlock()
	if !clean {
		t.Fatal("finished queries leaked slot/subject bookkeeping")
	}
	invocationExportTestRead(t, h, "A", false, nil)
}

func TestInvocationExportCancellationDeadlineAndEncodingBound(t *testing.T) {
	for _, where := range []string{"ext_settings", "invocations"} {
		t.Run(where, func(t *testing.T) {
			h := invocationExportCancellationHarness(t)
			name := "test:export-cancel"
			entered := make(chan struct{})
			var once sync.Once
			var budget time.Duration
			if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == where {
					deadline, ok := tx.Statement.Context.Deadline()
					if !ok {
						tx.AddError(errors.New("missing deadline"))
						return
					}
					budget = time.Until(deadline)
					once.Do(func() { close(entered) })
					<-tx.Statement.Context.Done()
					tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan *httptest.ResponseRecorder, 1)
			finished := make(chan struct{})
			t.Cleanup(func() { cancel(); <-finished; _ = h.db.Callback().Query().Remove(name) })
			go func() {
				defer close(finished)
				r := invocationAcceptanceRequest(h, "A", invocationExportTestURL(false, nil)).WithContext(ctx)
				done <- invocationAcceptanceCall(h, r)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("cancel test did not reach selected query")
			}
			cancel()
			select {
			case w := <-done:
				invocationExportTestFailure(t, w, 503)
			case <-time.After(time.Second):
				t.Fatal("cancelled export did not return")
			}
			if budget <= 0 || budget > 3*time.Second {
				t.Fatal("export did not inherit bounded business deadline", budget)
			}
			if err := h.db.Callback().Query().Remove(name); err != nil {
				t.Fatal(err)
			}
			invocationExportTestRead(t, h, "A", false, nil)
		})
	}
	buffer := &invocationExportBuffer{ctx: context.Background()}
	if n, err := buffer.Write(make([]byte, invocationExportMaxBytes)); err != nil || n != invocationExportMaxBytes || cap(buffer.data) > invocationExportMaxBytes {
		t.Fatal("buffer rejected exact byte boundary or overallocated", n, err, cap(buffer.data))
	}
	if n, err := buffer.Write([]byte("x")); !errors.Is(err, errInvocationExportSize) || n != 0 || len(buffer.data) != invocationExportMaxBytes {
		t.Fatal("oversize buffer partially accepted bytes", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if bytes, err := encodeInvocationExport(ctx, invocation.QueryResult{}, false, &exportMoneySnapshot{Currency: "USD"}, "setting", "safe"); !errors.Is(err, context.Canceled) || bytes != nil {
		t.Fatal("cancelled encoding returned a partial CSV", err)
	}
}

func invocationExportCancellationHarness(t *testing.T) *invocationAcceptanceHarness {
	t.Helper()
	// database/sql may discard a connection cancelled during a transaction.
	// A file DB keeps the fixture alive after that legitimate behavior; a lone
	// :memory: connection would lose all users and confuse slot-release checks.
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "export.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	for _, schema := range []string{testSchema, relayTestSchema} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	engine, host := NewWithServer(db, Options{AuthSecret: testSecret, AdminToken: []byte(testAdminToken), Keyring: testKeyring(t), AuthRateBurst: 100000})
	h := invocationAcceptanceSubjects(t, &gin999{engine: engine, srv: host}, db)
	row := invocation.Invocation{OrgID: 1001, APIKeyID: 11, RequestID: "cancel-fixture", Model: "synthetic", Status: "success", CostUSD: decimal.RequireFromString("0.125"), CreatedAt: invocationAcceptanceNow.Add(-24 * time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return h
}
