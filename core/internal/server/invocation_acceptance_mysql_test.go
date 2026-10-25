package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func invocationAcceptanceMySQL(t *testing.T) (*invocationAcceptanceHarness, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN required; SQLite does not prove DECIMAL or MySQL repeatable-read behavior")
	}
	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("invocation acceptance requires the dedicated ark_test test connection")
	}
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("cannot allocate isolated test schema")
	}
	name := "ark_test_ui1_" + hex.EncodeToString(random[:])
	if !regexp.MustCompile(`^ark_test_ui1_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("unsafe generated test schema name")
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("cannot open test database connection")
	}
	created := false
	var pools []*sql.DB
	t.Cleanup(func() {
		for _, pool := range pools {
			_ = pool.Close()
		}
		if created {
			if _, e := admin.Exec("DROP DATABASE `" + name + "`"); e != nil {
				t.Error("could not remove this invocation's private schema")
			}
		}
		_ = admin.Close()
	})
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("private invocation schema requires CREATE/DROP DATABASE on the test server")
	}
	created = true
	cfg.DBName = name
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = true
	open := func() *gorm.DB {
		db, e := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
		if e != nil {
			t.Fatal("cannot open private invocation schema")
		}
		pool, e := db.DB()
		if e != nil {
			t.Fatal(e)
		}
		pool.SetMaxOpenConns(6)
		pools = append(pools, pool)
		return db
	}
	db := open()
	// Use the tracked SQL for these read-only paths, not AutoMigrate, and never
	// alter the shared ark_test schema or the video/financial fixtures.
	for _, file := range []string{"0001_identity.sql", "0005_invocations.sql", "0013_settings_audit.sql", "0020_user_roles.sql", "0031_invocation_client_meta.sql", "0040_invocation_modality.sql", "0045_invocation_diagnostics.sql"} {
		body, e := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
		if e != nil {
			t.Fatal(e)
		}
		if e = db.Exec(string(body)).Error; e != nil {
			t.Fatalf("isolated fixture migration %s: %v", file, e)
		}
	}
	engine, server := NewWithServer(db, Options{AuthSecret: testSecret, AdminToken: []byte(testAdminToken), AuthRateBurst: 100000})
	h := invocationAcceptanceSubjects(t, &gin999{engine: engine, srv: server}, db)
	return h, open()
}

func TestMySQLInvocationAcceptance237CrossPageAndFilterOracle(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)
	invocationAcceptanceSeed(t, h.db, rows)
	ids, want := invocationAcceptanceOracle(t, rows, nil)
	var baseHash string
	for _, pageSize := range []int{20, 50, 100} {
		t.Run(fmt.Sprintf("page_size_%d", pageSize), func(t *testing.T) {
			found := []string{}
			for page := 1; page <= (237+pageSize-1)/pageSize; page++ {
				out := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}})
				invocationAcceptanceAssertTotals(t, out.Totals, want)
				if out.Total != 237 || out.Page != page || out.PageSize != pageSize {
					t.Fatal("page metadata disagrees with full set")
				}
				if baseHash == "" {
					baseHash = out.Query.FilterHash
				}
				if out.Query.FilterHash != baseHash {
					t.Fatal("pagination changed filter identity")
				}
				end := page * pageSize
				if end > 237 {
					end = 237
				}
				start := (page - 1) * pageSize
				invocationAcceptanceEqualIDs(t, invocationAcceptanceIDs(t, out.Items), ids[start:end])
				found = append(found, invocationAcceptanceIDs(t, out.Items)...)
			}
			invocationAcceptanceEqualIDs(t, found, ids)
		})
	}
	for _, currency := range []string{"USD", "CNY"} {
		out := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"display_currency": {currency}})
		invocationAcceptanceAssertTotals(t, out.Totals, want)
		rate := "1"
		if currency == "CNY" {
			rate = "6.65"
		}
		expected := invocationAcceptanceRat(t, want.CostUSD)
		expected.Mul(expected, invocationAcceptanceRat(t, rate))
		if invocationAcceptanceRat(t, out.Totals.CostDisplay).Cmp(expected) != 0 || out.Query.FilterHash != baseHash {
			t.Fatal("display conversion rounded rows before summing or changed the filtered identity")
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
			wantIDs, totals := invocationAcceptanceOracle(t, rows, tc.accept)
			found := []string{}
			for page := 1; page <= max(1, (len(wantIDs)+19)/20); page++ {
				q := url.Values{}
				for k, v := range tc.values {
					q[k] = v
				}
				q.Set("page", strconv.Itoa(page))
				out := invocationAcceptanceRead(t, h, "superadmin", true, q)
				invocationAcceptanceAssertTotals(t, out.Totals, totals)
				if out.Total != int64(len(wantIDs)) {
					t.Fatal("filtered total mismatch")
				}
				found = append(found, invocationAcceptanceIDs(t, out.Items)...)
			}
			invocationAcceptanceEqualIDs(t, found, wantIDs)
		})
	}
	for _, org := range []string{"A", "B", "C"} {
		out := invocationAcceptanceRead(t, h, org, false, url.Values{"page_size": {"100"}})
		_, totals := invocationAcceptanceOracle(t, rows, func(r invocationAcceptanceRow) bool { return r.Org == org })
		invocationAcceptanceAssertTotals(t, out.Totals, totals)
		for _, row := range out.Items {
			for _, key := range []string{"org_id", "channel_id", "provider", "payload_object_key"} {
				if _, ok := row[key]; ok {
					t.Fatal("ordinary MySQL DTO leaked", key)
				}
			}
		}
	}
	last := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"page": {"13"}})
	if len(last.Items) != 0 || last.Total != 237 {
		t.Fatal("out-of-range page lost full totals")
	}
	invocationAcceptanceAssertTotals(t, last.Totals, want)
	micro := invocationAcceptanceRead(t, h, "superadmin", true, url.Values{"request_id": {rows[1].RequestID}, "display_currency": {"CNY"}})
	if invocationAcceptanceRat(t, micro.Totals.CostUSD).Sign() == 0 || invocationAcceptanceRat(t, micro.Totals.CostDisplay).Cmp(invocationAcceptanceRat(t, "0.0000000665")) != 0 {
		t.Fatal("micro charge became zero")
	}
}

func TestMySQLInvocationAcceptanceSumBeyondInt64(t *testing.T) {
	h, _ := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)[:2]
	for i := range rows {
		rows[i].CostUSD = "0.00000001"
		rows[i].PromptTokens = "9223372036854775807"
		rows[i].CompletionTokens = "9223372036854775807"
		rows[i].CachedTokens = "9223372036854775807"
		rows[i].ReasoningTokens = "9223372036854775807"
	}
	invocationAcceptanceSeed(t, h.db, rows)
	out := invocationAcceptanceRead(t, h, "superadmin", true, nil)
	_, want := invocationAcceptanceOracle(t, rows, nil)
	invocationAcceptanceAssertTotals(t, out.Totals, want)
	if out.Totals.PromptTokens != "18446744073709551614" || out.Totals.CachedTokens != "18446744073709551614" {
		t.Fatal("SUM overflowed signed BIGINT or passed through float64")
	}
}

func TestMySQLInvocationAcceptanceTwoConnectionSnapshot(t *testing.T) {
	h, writer := invocationAcceptanceMySQL(t)
	rows := invocationAcceptanceLoad(t)[:2]
	for i := range rows {
		rows[i].CostUSD = []string{"0.125", "0.25"}[i]
		rows[i].PromptTokens = "10"
		rows[i].CompletionTokens = "20"
		rows[i].CachedTokens = "2"
		rows[i].ReasoningTokens = "3"
	}
	invocationAcceptanceSeed(t, h.db, rows)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var intercepted atomic.Bool
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	name := "test:ui1-after-aggregate-before-rows"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Error == nil && tx.Statement.Table == "invocations" && strings.Contains(strings.ToUpper(tx.Statement.SQL.String()), "COUNT(*)") && intercepted.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock(); _ = h.db.Callback().Query().Remove(name) })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationAcceptanceURL(true, nil)))
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("real aggregate did not reach snapshot barrier")
	}
	// This independently committed transaction changes all three kinds of set
	// membership/value evidence between the actual aggregate and page SELECTs.
	if err := writer.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&invocation.Invocation{}).Where("id = ?", 1).Update("cost_usd", "0.5").Error; e != nil {
			return e
		}
		if e := tx.Delete(&invocation.Invocation{}, "id = ?", 2).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO invocations (id,org_id,api_key_id,request_id,model,status,cost_usd,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens,created_at) VALUES (99,1001,11,?,'snapshot-insert','success',0.75,10,20,2,3,?)", fmt.Sprintf("%032x", 99), rows[0].CreatedAt).Error
	}); err != nil {
		t.Fatal(err)
	}
	unblock()
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot response did not finish")
	}
	if w.Code != 200 {
		t.Fatalf("snapshot HTTP %d: %s", w.Code, w.Body.String())
	}
	var before invocationAcceptanceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	ids, totals := invocationAcceptanceOracle(t, rows, nil)
	invocationAcceptanceAssertTotals(t, before.Totals, totals)
	invocationAcceptanceEqualIDs(t, invocationAcceptanceIDs(t, before.Items), ids)
	for _, row := range before.Items {
		var id, cost string
		_ = json.Unmarshal(row["request_id"], &id)
		_ = json.Unmarshal(row["cost_usd"], &cost)
		want := "0.125"
		if id == rows[1].RequestID {
			want = "0.25"
		}
		if invocationAcceptanceRat(t, cost).Cmp(invocationAcceptanceRat(t, want)) != 0 {
			t.Fatal("page observed a later update than its aggregate")
		}
	}
	after := invocationAcceptanceRead(t, h, "superadmin", true, nil)
	invocationAcceptanceEqualIDs(t, invocationAcceptanceIDs(t, after.Items), []string{fmt.Sprintf("%032x", 99), rows[0].RequestID})
	if after.Total != 2 || invocationAcceptanceRat(t, after.Totals.CostUSD).Cmp(invocationAcceptanceRat(t, "1.25")) != 0 || after.Query.CrossRequestConsistency != "live" {
		t.Fatal("next request did not observe the committed changes")
	}
}

func TestMySQLInvocationAcceptanceReadPoolWaitUsesCallerDeadline(t *testing.T) {
	h, reader := invocationAcceptanceMySQL(t)
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
	beforeWaits := pool.Stats().WaitCount
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	request := invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)).WithContext(ctx)
	go func() { done <- invocationAcceptanceCall(h, request) }()
	deadline := time.Now().Add(200 * time.Millisecond)
	for pool.Stats().WaitCount == beforeWaits && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pool.Stats().WaitCount == beforeWaits {
		t.Fatal("request did not wait on the exhausted read pool")
	}
	var one int
	if err := h.db.Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 {
		t.Fatal("analysis pool exhaustion blocked the primary pool", err)
	}
	select {
	case w := <-done:
		if w.Code != 503 || strings.Contains(w.Body.String(), `"totals"`) {
			t.Fatal("connection wait ignored cancellation or returned false totals", w.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("read-pool wait outlived the caller's deadline")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	invocationAcceptanceRead(t, h, "A", false, nil)
}
