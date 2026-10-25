package server

import (
	"encoding/csv"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These literal columns and declaration-based expectations intentionally do not
// call the production CSV encoder, projection, predicates or money converter.
func invocationExportAcceptanceColumns(admin bool) []string {
	columns := []string{"time", "request_id", "model", "status", "modality", "api_key_id", "prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "cost_usd", "cost_display", "currency", "cny_per_usd", "rate_snapshot_at", "rate_source", "as_of", "filter_hash", "amount_source", "completeness", "usage_presence"}
	if admin {
		columns = append(columns, "org_id", "user_id", "channel_id", "provider")
	}
	return columns
}

func invocationExportAcceptanceURL(admin bool, values url.Values) string {
	return strings.Replace(invocationAcceptanceURL(admin, values), "?", "/export?", 1)
}

func invocationExportAcceptanceCall(h *invocationAcceptanceHarness, subject string, admin bool, values url.Values) *httptest.ResponseRecorder {
	return invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationExportAcceptanceURL(admin, values)))
}

func invocationExportAcceptanceDecode(t *testing.T, w *httptest.ResponseRecorder, admin bool) []map[string]string {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("export HTTP %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("CSV response metadata does not meet its public contract")
	}
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="invocation-log-`) {
		t.Fatal("missing controlled CSV filename")
	}
	all, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(all) == 0 || !reflect.DeepEqual(all[0], invocationExportAcceptanceColumns(admin)) {
		t.Fatal("invalid CSV or changed literal column contract", err)
	}
	rows := make([]map[string]string, 0, len(all)-1)
	for _, cells := range all[1:] {
		if len(cells) != len(all[0]) {
			t.Fatal("CSV row width differs from header")
		}
		row := make(map[string]string, len(cells))
		for i, cell := range cells {
			row[all[0][i]] = cell
		}
		rows = append(rows, row)
	}
	for _, secret := range []string{"synthetic-hidden-payload-pointer", "synthetic-upstream-id", "synthetic-wrapped-key", "synthetic-ciphertext", "payload_object_key", "credential_enc", "key_hash"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("export exposed a forbidden field or synthetic secret")
		}
	}
	return rows
}

func invocationExportAcceptanceAssert(t *testing.T, w *httptest.ResponseRecorder, admin bool, declarations []invocationAcceptanceRow, accept func(invocationAcceptanceRow) bool, currency string) []string {
	t.Helper()
	rows := invocationExportAcceptanceDecode(t, w, admin)
	wantIDs, totals := invocationAcceptanceOracle(t, declarations, accept)
	gotIDs := make([]string, 0, len(rows))
	index := make(map[string]invocationAcceptanceRow, len(declarations))
	for _, row := range declarations {
		index[row.RequestID] = row
	}
	rate := new(big.Rat).SetInt64(1)
	if currency == "CNY" {
		rate = invocationAcceptanceRat(t, "6.65")
	}
	for _, row := range rows {
		id := row["request_id"]
		gotIDs = append(gotIDs, id)
		declared, ok := index[id]
		if !ok || (accept != nil && !accept(declared)) {
			t.Fatal("CSV contains a row outside the independent declaration predicate", id)
		}
		for key, value := range map[string]string{
			"model": declared.Model, "modality": declared.Modality, "status": declared.Status,
			"api_key_id":    strconv.FormatUint(invocationAcceptanceKeys[declared.Key], 10),
			"prompt_tokens": declared.PromptTokens, "completion_tokens": declared.CompletionTokens,
			"cached_tokens": declared.CachedTokens, "reasoning_tokens": declared.ReasoningTokens,
			"currency": currency, "cny_per_usd": "6.65", "rate_source": "setting",
			"amount_source": "log_recorded_cost", "completeness": "best_effort", "usage_presence": "legacy_not_distinguishable",
		} {
			if row[key] != value {
				t.Fatalf("request %s field %s = %q, want %q", id, key, row[key], value)
			}
		}
		created, err := time.Parse(time.RFC3339Nano, row["time"])
		if err != nil || !created.Equal(declared.CreatedAt) {
			t.Fatal("CSV changed the declared creation time")
		}
		cost := invocationAcceptanceRat(t, declared.CostUSD)
		if invocationAcceptanceRat(t, row["cost_usd"]).Cmp(cost) != 0 || invocationAcceptanceRat(t, row["cost_display"]).Cmp(new(big.Rat).Mul(cost, rate)) != 0 {
			t.Fatal("CSV row money differs from the independent rational oracle", id)
		}
		for column, header := range map[string]string{"rate_snapshot_at": "X-Rate-Snapshot-At", "as_of": "X-Invocation-As-Of", "filter_hash": "X-Invocation-Filter-Hash"} {
			if row[column] != w.Header().Get(header) || row[column] == "" {
				t.Fatal("row metadata does not belong to the response's single snapshot")
			}
		}
		if admin {
			for key, value := range map[string]string{"org_id": strconv.FormatUint(invocationAcceptanceOrgs[declared.Org], 10), "user_id": strconv.FormatUint(invocationAcceptanceOrgs[declared.Org]+4000, 10), "channel_id": strconv.FormatUint(declared.ChannelID, 10), "provider": "synthetic-internal-provider"} {
				if row[key] != value {
					t.Fatal("admin metadata differs from declaration", key)
				}
			}
		}
	}
	invocationAcceptanceEqualIDs(t, gotIDs, wantIDs)
	for header, value := range map[string]string{
		"X-Invocation-Filter-Version": "1", "X-Invocation-Record-Count": strconv.FormatInt(totals.RecordCount, 10),
		"X-Invocation-Success-Count": strconv.FormatInt(totals.SuccessCount, 10), "X-Invocation-Non-Success-Count": strconv.FormatInt(totals.NonSuccessCount, 10),
		"X-Invocation-Prompt-Tokens": totals.PromptTokens, "X-Invocation-Completion-Tokens": totals.CompletionTokens,
		"X-Invocation-Cached-Tokens": totals.CachedTokens, "X-Invocation-Reasoning-Tokens": totals.ReasoningTokens,
		"X-Display-Currency": currency, "X-CNY-Per-USD": "6.65", "X-Rate-Source": "setting",
		"X-Invocation-Amount-Source": "log_recorded_cost", "X-Invocation-Completeness": "best_effort", "X-Invocation-Usage-Presence": "legacy_not_distinguishable",
		"X-Invocation-Consistency": "response_snapshot", "X-Invocation-Cross-Request-Consistency": "live",
	} {
		if w.Header().Get(header) != value {
			t.Fatalf("export header %s = %q, want %q", header, w.Header().Get(header), value)
		}
	}
	if invocationAcceptanceRat(t, w.Header().Get("X-Invocation-Cost-USD")).Cmp(invocationAcceptanceRat(t, totals.CostUSD)) != 0 || invocationAcceptanceRat(t, w.Header().Get("X-Invocation-Cost-Display")).Cmp(new(big.Rat).Mul(invocationAcceptanceRat(t, totals.CostUSD), rate)) != 0 {
		t.Fatal("CSV aggregate money differs from independent rational oracle")
	}
	for _, name := range []string{"X-Rate-Snapshot-At", "X-Invocation-As-Of"} {
		if _, err := time.Parse(time.RFC3339Nano, w.Header().Get(name)); err != nil {
			t.Fatal("missing metadata even for the zero-row export", name)
		}
	}
	if len(w.Header().Get("X-Invocation-Filter-Hash")) != 71 {
		t.Fatal("missing stable filter identity")
	}
	return gotIDs
}

func invocationExportAcceptanceJSONExact(t *testing.T, items []map[string]json.RawMessage, declarations []invocationAcceptanceRow) {
	t.Helper()
	index := make(map[string]invocationAcceptanceRow, len(declarations))
	for _, row := range declarations {
		index[row.RequestID] = row
	}
	for _, item := range items {
		var id string
		var exact map[string]string
		if json.Unmarshal(item["request_id"], &id) != nil || json.Unmarshal(item["usage_exact"], &exact) != nil || len(exact) != 4 {
			t.Fatal("v1 list omitted the four exact string usage fields")
		}
		r, ok := index[id]
		if !ok || exact["prompt_tokens"] != r.PromptTokens || exact["completion_tokens"] != r.CompletionTokens || exact["cached_tokens"] != r.CachedTokens || exact["reasoning_tokens"] != r.ReasoningTokens {
			t.Fatal("JSON exact row usage disagrees with independent declaration")
		}
	}
}

func invocationExportAcceptanceFailure(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code || strings.Contains(w.Header().Get("Content-Type"), "csv") || w.Header().Get("Content-Disposition") != "" || w.Header().Get("X-Invocation-Record-Count") != "" || strings.HasPrefix(w.Body.String(), "time,request_id,") {
		t.Fatalf("failed export published partial CSV or wrong error: %d %s", w.Code, w.Body.String())
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["totals"] != nil {
		t.Fatal("error response was not a safe JSON error")
	}
	for _, forbidden := range []string{"private-driver-secret", "synthetic-ciphertext", "synthetic-wrapped-key", "SELECT ", "connection refused"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("export error leaked private diagnostics")
		}
	}
}

func TestInvocationExportAcceptanceIndependentHTTPOracle(t *testing.T) {
	h, rows := invocationAcceptanceSmall(t)
	// The SQLite control uses only exactly representable fixture amounts; full
	// decimal and large SUM assertions below are exclusively real-MySQL tests.
	for _, subject := range []string{"A", "B", "C", "A-in-B", "superadmin", "root"} {
		admin := subject == "superadmin" || subject == "root"
		org := subject
		if org == "A-in-B" {
			org = "B"
		}
		var accept func(invocationAcceptanceRow) bool
		if !admin {
			accept = func(r invocationAcceptanceRow) bool { return r.Org == org }
		}
		w := invocationExportAcceptanceCall(h, subject, admin, nil)
		invocationExportAcceptanceAssert(t, w, admin, rows, accept, "USD")
		out := invocationAcceptanceRead(t, h, subject, admin, nil)
		invocationExportAcceptanceJSONExact(t, out.Items, rows)
		if out.Query.FilterHash != w.Header().Get("X-Invocation-Filter-Hash") {
			t.Fatal("list and export use different authorized filter identities")
		}
	}
}
