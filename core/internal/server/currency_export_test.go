package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func csvRecords(t *testing.T, raw string) []map[string]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil || len(rows) == 0 {
		t.Fatalf("invalid CSV: %v", err)
	}
	out := []map[string]string{}
	for _, row := range rows[1:] {
		item := map[string]string{}
		for i, name := range rows[0] {
			item[name] = row[i]
		}
		out = append(out, item)
	}
	return out
}

func TestFundExportDisplaySnapshotAndLegacy(t *testing.T) {
	r, db, _ := fundFixture(t)
	if err := db.Exec(fundExportTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	db.Save(&Setting{Name: "cny_per_usd", Value: "6.65"})
	create := func(key, currency string, want int) fundExportJob {
		req := httptest.NewRequest("POST", "/api/fund-exports", strings.NewReader(fmt.Sprintf(`{"display_currency":%q}`, currency)))
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("create %d: %s", w.Code, w.Body.String())
		}
		var job fundExportJob
		_ = json.Unmarshal(w.Body.Bytes(), &job)
		return job
	}
	create("invalid", "EUR", 422)
	job := create("snapshot", "CNY", 202)
	if job.DisplayMoney == nil || job.DisplayMoney.CNYPerUSD != "6.65" {
		t.Fatal("missing rate snapshot", job.DisplayMoney)
	}
	db.Save(&Setting{Name: "cny_per_usd", Value: "7.3"})
	replay := create("snapshot", "CNY", 202)
	if replay.ID != job.ID || replay.DisplayMoney.CNYPerUSD != "6.65" {
		t.Fatal("replay changed snapshot")
	}
	create("snapshot", "USD", 409)
	worker := &Server{db: db, now: time.Now}
	if err := worker.processFundExportChunk(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	body := fundCall(t, r, fmt.Sprintf("/api/fund-exports/%d/download", job.ID), "10", 200)
	var raw string
	_ = json.Unmarshal(body["csv"], &raw)
	rows := csvRecords(t, raw)
	if len(rows) != 4 || string(body["currency"]) != `"CNY"` {
		t.Fatal("wrong export", body)
	}
	for _, row := range rows {
		if row["currency"] != "CNY" || row["source_currency"] != "USD" || row["cny_per_usd"] != "6.65" || row["rate_snapshot_at"] == "" {
			t.Fatal(row)
		}
		for _, field := range []string{"amount", "posted_delta", "held_delta", "available_delta"} {
			usd := decimal.RequireFromString(row[field+"_usd"])
			if row[field] != usd.Mul(decimal.RequireFromString("6.65")).String() {
				t.Fatal(field, row)
			}
		}
	}
	if !strings.Contains(raw, "'=1+1") {
		t.Fatal("lost CSV injection protection")
	}
	usd := create("usd", "USD", 202)
	if err := worker.processFundExportChunk(context.Background(), usd.ID); err != nil {
		t.Fatal(err)
	}
	db.First(&usd, usd.ID)
	for _, row := range csvRecords(t, usd.CSVData) {
		if row["currency"] != "USD" || row["amount"] != row["amount_usd"] {
			t.Fatal(row)
		}
	}
}

func TestUsageMoneyExportsAndEmptyCollections(t *testing.T) {
	s, db := setupRelayServer(t)
	tok := registerAndLogin(t, s, "currency-empty@example.com")
	for _, path := range []string{"/api/usage/summary", "/api/usage/by-model", "/api/usage/by-key", "/api/usage"} {
		w, out := s.do(t, "GET", path, tok, nil)
		items, ok := out["items"].([]any)
		if w.Code != 200 || !ok || len(items) != 0 {
			t.Fatalf("empty collection %s: %s", path, w.Body.String())
		}
	}
	_, me := s.do(t, "GET", "/api/me", tok, nil)
	insertInvocation(t, db, me["org_id"].(float64), "currency-cost", "success", "openai", 0.02, time.Now())
	db.Save(&Setting{Name: "cny_per_usd", Value: "6.65"})
	for _, currency := range []string{"CNY", "USD", "EUR", ""} {
		for _, admin := range []bool{false, true} {
			path := "/api/usage/export?display_currency=" + currency
			var w *httptest.ResponseRecorder
			if admin {
				w, _ = s.doAdmin(t, "GET", "/api/admin/usage/by-user/export?display_currency="+currency, nil)
			} else {
				w, _ = s.do(t, "GET", path, tok, nil)
			}
			if currency == "EUR" {
				if w.Code != 422 {
					t.Fatal("invalid currency accepted")
				}
				continue
			}
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			rows := csvRecords(t, w.Body.String())
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			row := rows[0]
			if row["cost_usd"] != "0.02" {
				t.Fatal("USD mutated", row)
			}
			if currency == "" {
				if _, ok := row["cost_display"]; ok {
					t.Fatal("legacy schema changed")
				}
				continue
			}
			want := "0.02"
			if currency == "CNY" {
				want = "0.133"
			}
			if row["cost_display"] != want || row["display_currency"] != currency || row["cny_per_usd"] != "6.65" || w.Header().Get("X-Display-Currency") != currency {
				t.Fatal(row)
			}
		}
	}
	other := registerAndLogin(t, s, "currency-other@example.com")
	w, _ := s.do(t, "GET", "/api/usage/export?display_currency=CNY", other, nil)
	if len(csvRecords(t, w.Body.String())) != 0 {
		t.Fatal("export crossed organizations")
	}
}

func TestExportMoneyPrecisionAndCorruptSnapshot(t *testing.T) {
	m := &exportMoneySnapshot{Currency: "CNY", CNYPerUSD: "6.12345678", SnapshotAt: time.Now()}
	if m.amount(decimal.RequireFromString("0.00000001")) != "0.0000000612345678" {
		t.Fatal("export lost precision")
	}
	for _, raw := range []string{`{"money":{"display_currency":"EUR","cny_per_usd":"6.65"}}`, `{"money":{"display_currency":"CNY","cny_per_usd":"0","rate_snapshot_at":"2026-10-22T00:00:00Z"}}`} {
		j := fundExportJob{FilterJSON: raw}
		if j.loadMoney() == nil {
			t.Fatal("invalid persisted snapshot accepted")
		}
	}
}
