package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

func TestInvocationAcceptanceFixtureIndependentOracle(t *testing.T) {
	rows := invocationAcceptanceLoad(t)
	_, all := invocationAcceptanceOracle(t, rows, nil)
	if all.RecordCount != 237 || all.SuccessCount != 180 || all.NonSuccessCount != 57 || all.CostUSD != "123456789.12634435" || all.PromptTokens != "9007199254743333" || all.CompletionTokens != "4680" || all.CachedTokens != "468" || all.ReasoningTokens != "702" {
		t.Fatal("fixture differs from independently reviewed golden values", all)
	}
	for _, tc := range []struct {
		org   string
		count int64
		cost  string
	}{{"A", 120, "123456789.12490057"}, {"B", 80, "0.00098720"}, {"C", 37, "0.00045658"}} {
		_, got := invocationAcceptanceOracle(t, rows, func(r invocationAcceptanceRow) bool { return r.Org == tc.org })
		if got.RecordCount != tc.count || got.CostUSD != tc.cost {
			t.Fatal(tc.org, got)
		}
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.RequestID] {
			t.Fatal("duplicate fixture request")
		}
		seen[r.RequestID] = true
	}
}

func TestInvocationAcceptanceHTTPOrganizationAndRoleBoundaries(t *testing.T) {
	h, rows := invocationAcceptanceSmall(t)
	global := invocationAcceptanceRead(t, h, "superadmin", true, nil)
	wantIDs, want := invocationAcceptanceOracle(t, rows, nil)
	invocationAcceptanceAssertTotals(t, global.Totals, want)
	invocationAcceptanceEqualIDs(t, invocationAcceptanceIDs(t, global.Items), wantIDs)
	for _, org := range []string{"A", "B", "C"} {
		got := invocationAcceptanceRead(t, h, org, false, nil)
		ids, totals := invocationAcceptanceOracle(t, rows, func(r invocationAcceptanceRow) bool { return r.Org == org })
		invocationAcceptanceAssertTotals(t, got.Totals, totals)
		invocationAcceptanceEqualIDs(t, invocationAcceptanceIDs(t, got.Items), ids)
		if got.Query.FilterHash == global.Query.FilterHash {
			t.Fatal("authorized scope absent from filter identity")
		}
		for _, row := range got.Items {
			for _, key := range []string{"org_id", "channel_id", "provider", "attempts", "payload_object_key", "credential", "key_hash"} {
				if _, exists := row[key]; exists {
					t.Fatalf("organization DTO exposed %s", key)
				}
			}
		}
	}
	empty := invocationAcceptanceRead(t, h, "A", false, url.Values{"api_key_id": {"21"}})
	_, zero := invocationAcceptanceOracle(t, nil, nil)
	invocationAcceptanceAssertTotals(t, empty.Totals, zero)
	if len(empty.Items) != 0 || empty.Total != 0 {
		t.Fatal("foreign Key query disclosed rows")
	}
	for _, subject := range []string{"A", "admin"} {
		w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationAcceptanceURL(true, nil)))
		if w.Code != 403 {
			t.Fatalf("%s acquired global logs: %d", subject, w.Code)
		}
	}
	// Both root compatibility and named superadmin use the existing authorization.
	invocationAcceptanceRead(t, h, "root", true, nil)
	if err := h.db.Exec("UPDATE users SET role='admin' WHERE id=5004").Error; err != nil {
		t.Fatal(err)
	}
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationAcceptanceURL(true, nil)))
	if w.Code != 403 {
		t.Fatal("role revocation did not take effect", w.Code)
	}
	if err := h.db.Exec("DELETE FROM org_members WHERE org_id=1001 AND user_id=5001").Error; err != nil {
		t.Fatal(err)
	}
	w = invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)))
	if w.Code != 403 {
		t.Fatal("membership removal did not take effect", w.Code)
	}
	var key string
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		var err error
		key, _, err = identity.IssueKey(tx, identity.IssueKeyInput{OrgID: 1002, Name: "synthetic-console-denial", Scopes: "video:*"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", invocationAcceptanceURL(false, nil), nil)
	r.Header.Set("Authorization", "Bearer "+key)
	if got := invocationAcceptanceCall(h, r); got.Code != 401 {
		t.Fatal("data-plane API Key accessed session-only logs", got.Code)
	}
}

func TestInvocationAcceptanceHTTPStrictInputAndLegacyIsolation(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	for _, tc := range []struct {
		name   string
		admin  bool
		values url.Values
	}{
		{"unknown_version", true, url.Values{"filter_version": {"2"}}},
		{"duplicate_version", true, url.Values{"filter_version": {"1", "1"}}},
		{"unknown_field", true, url.Values{"include_totals": {"true"}}},
		{"bad_id", true, url.Values{"org_id": {"broken"}}},
		{"zero_id", true, url.Values{"api_key_id": {"0"}}},
		{"org_override", false, url.Values{"org_id": {"1002"}}},
		{"channel_override", false, url.Values{"channel_id": {"201"}}},
		{"status_conflict", true, url.Values{"result": {"all"}, "status": {"success"}}},
		{"no_offset", true, url.Values{"since": {"2026-09-30T00:00:00"}}},
		{"invalid_page", true, url.Values{"page": {"99999999999999999999"}}},
		{"invalid_page_size", true, url.Values{"page_size": {"21"}}},
		{"duplicate_model", true, url.Values{"model": {"x", "y"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subject := "A"
			if tc.admin {
				subject = "superadmin"
			}
			w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationAcceptanceURL(tc.admin, tc.values)))
			if w.Code != 422 {
				t.Fatalf("invalid input widened query: %d %s", w.Code, w.Body.String())
			}
			var body map[string]json.RawMessage
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if _, ok := body["totals"]; ok {
				t.Fatal("invalid input returned successful totals")
			}
		})
	}
	// A1 does not reinterpret existing clients: absence of a version still
	// invokes the original handler and its response shape.
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", "/api/usage?page=1&page_size=20"))
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || legacy["query"] != nil || legacy["totals"] != nil {
		t.Fatal("unversioned client unexpectedly opted into aggregation", w.Body.String())
	}
}

func TestInvocationAcceptanceHTTPNinetyNaturalDaysAcrossDST(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	for _, tc := range []struct {
		name, since, until string
		want               int
	}{
		{"spring_90", "2026-01-01T00:00:00-05:00", "2026-04-01T00:00:00-04:00", 200},
		{"fall_90", "2026-09-01T00:00:00-04:00", "2026-11-30T00:00:00-05:00", 200},
		{"fall_over_90", "2026-09-01T00:00:00-04:00", "2026-11-30T00:00:00.001-05:00", 422},
		{"date_90", "2026-09-01", "2026-11-30", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"timezone": {"America/New_York"}, "since": {tc.since}, "until": {tc.until}}
			w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "superadmin", invocationAcceptanceURL(true, q)))
			if w.Code != tc.want {
				t.Fatalf("natural-day bound %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestInvocationAcceptanceHTTPFixedRateAndFailureAreExplicit(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	var changed atomic.Bool
	name := "test:ui1-fx-after-observation"
	if err := h.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Error == nil && tx.Statement.Table == "ext_settings" && changed.CompareAndSwap(false, true) {
			if err := h.db.Model(&Setting{}).Where("name = ?", "cny_per_usd").Update("value", "7.3").Error; err != nil {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	first := invocationAcceptanceRead(t, h, "A", false, url.Values{"display_currency": {"CNY"}})
	if !changed.Load() || first.Query.DisplayMoney.CNYPerUSD != "6.65" || invocationAcceptanceRat(t, first.Totals.CostDisplay).Cmp(invocationAcceptanceRat(t, "1.6625")) != 0 {
		t.Fatal("query did not retain its observed FX", first.Totals, first.Query.DisplayMoney)
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	next := invocationAcceptanceRead(t, h, "A", false, url.Values{"display_currency": {"CNY"}})
	if next.Query.DisplayMoney.CNYPerUSD != "7.3" || next.Query.FilterHash != first.Query.FilterHash {
		t.Fatal("FX observation was cached forever or changed the filtered set identity")
	}
	for _, value := range []string{"broken", "0", "101"} {
		if err := h.db.Model(&Setting{}).Where("name = ?", "cny_per_usd").Update("value", value).Error; err != nil {
			t.Fatal(err)
		}
		w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)))
		if w.Code != 503 || strings.Contains(w.Body.String(), `"totals"`) {
			t.Fatal("corrupt FX silently succeeded", w.Code, w.Body.String())
		}
	}
	if err := h.db.Model(&Setting{}).Where("name = ?", "cny_per_usd").Update("value", "").Error; err != nil {
		t.Fatal(err)
	}
	got := invocationAcceptanceRead(t, h, "A", false, nil)
	if got.Query.DisplayMoney.RateSource != "default_empty" || got.Query.DisplayMoney.CNYPerUSD != "7.3" {
		t.Fatal("empty setting fallback not identified")
	}
	if err := h.db.Delete(&Setting{}, "name = ?", "cny_per_usd").Error; err != nil {
		t.Fatal(err)
	}
	got = invocationAcceptanceRead(t, h, "A", false, nil)
	if got.Query.DisplayMoney.RateSource != "default_missing" {
		t.Fatal("absent setting fallback not identified")
	}
	// Failure after entering the read transaction must not become a zero total,
	// and every error releases both query slots for later healthy requests.
	queryName := "test:ui1-aggregate-failure"
	if err := h.db.Callback().Query().After("gorm:query").Register(queryName, func(tx *gorm.DB) {
		if strings.Contains(strings.ToUpper(tx.Statement.SQL.String()), "COUNT(*)") && tx.Statement.Table == "invocations" {
			tx.AddError(errors.New("synthetic aggregate read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(queryName) })
	for i := 0; i < 3; i++ {
		w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)))
		if w.Code != 503 || strings.Contains(w.Body.String(), `"totals"`) {
			t.Fatal("read failure faked success or leaked a query slot", w.Code, w.Body.String())
		}
	}
	if err := h.db.Callback().Query().Remove(queryName); err != nil {
		t.Fatal(err)
	}
	invocationAcceptanceRead(t, h, "A", false, nil)
}

func TestInvocationAcceptanceHTTPConcurrencyLimitReleasesSlots(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	name := "test:ui1-request-budget-barrier"
	if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "ext_settings" {
			entered <- struct{}{}
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
	done := make(chan *httptest.ResponseRecorder, 4)
	start := func(subject string) {
		go func() {
			done <- invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationAcceptanceURL(false, nil)))
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not acquire a slot before FX lookup")
		}
	}
	start("A")
	start("A-in-B")
	if w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil))); w.Code != 429 {
		t.Fatal("same user escaped two-query limit by changing organization", w.Code)
	}
	start("B")
	start("C")
	if w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "root", invocationAcceptanceURL(true, nil))); w.Code != 429 {
		t.Fatal("global four-query limit not enforced", w.Code)
	}
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
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		invocationAcceptanceRead(t, h, "A", false, nil)
	}
}

func TestInvocationAcceptanceHTTPCancellationReleasesSlot(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	entered := make(chan struct{})
	var once sync.Once
	name := "test:ui1-cancel-before-fx-read"
	if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "ext_settings" {
			once.Do(func() { close(entered) })
			<-tx.Statement.Context.Done()
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- invocationAcceptanceCall(h, r) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter FX read")
	}
	cancel()
	select {
	case w := <-done:
		if w.Code == 200 || strings.Contains(w.Body.String(), `"totals"`) {
			t.Fatal("cancelled query returned successful totals")
		}
	case <-time.After(time.Second):
		t.Fatal("query ignored cancelled context")
	}
	if err := h.db.Callback().Query().Remove(name); err != nil {
		t.Fatal(err)
	}
	invocationAcceptanceRead(t, h, "A", false, nil)
}

func TestInvocationAcceptanceHTTPBusinessDeadlineIncludesFX(t *testing.T) {
	h, _ := invocationAcceptanceSmall(t)
	name := "test:ui1-business-deadline"
	var intercepted atomic.Bool
	var observedBudget time.Duration
	if err := h.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "ext_settings" && intercepted.CompareAndSwap(false, true) {
			deadline, ok := tx.Statement.Context.Deadline()
			if !ok {
				tx.AddError(errors.New("missing query deadline"))
				return
			}
			observedBudget = time.Until(deadline)
			<-tx.Statement.Context.Done()
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(name) })
	started := time.Now()
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, "A", invocationAcceptanceURL(false, nil)))
	if w.Code != 503 || !intercepted.Load() || observedBudget <= 0 || observedBudget > 3*time.Second || time.Since(started) > 6*time.Second || strings.Contains(w.Body.String(), `"totals"`) {
		t.Fatal("FX query escaped the shared three-second budget or fabricated success", w.Code, observedBudget)
	}
	invocationAcceptanceRead(t, h, "A", false, nil)
}
