package server

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// The declaration and oracle do not call the production parser, predicates or
// SQL builder. Only insertion uses the domain's persisted row type.
//
//go:embed testdata/invocation_ui1_237.json
var invocationAcceptanceJSON []byte

type invocationAcceptanceRow struct {
	Ordinal          int       `json:"ordinal"`
	Org              string    `json:"org"`
	Key              string    `json:"key"`
	RequestID        string    `json:"request_id"`
	Model            string    `json:"model"`
	Modality         string    `json:"modality"`
	ChannelID        uint64    `json:"channel_id"`
	Status           string    `json:"status"`
	IsStream         bool      `json:"is_stream"`
	CreatedAt        time.Time `json:"created_at"`
	CostUSD          string    `json:"cost_usd"`
	PromptTokens     string    `json:"prompt_tokens"`
	CompletionTokens string    `json:"completion_tokens"`
	CachedTokens     string    `json:"cached_tokens"`
	ReasoningTokens  string    `json:"reasoning_tokens"`
}

type invocationAcceptanceTotals struct {
	RecordCount       int64           `json:"record_count"`
	SuccessCount      int64           `json:"success_count"`
	NonSuccessCount   int64           `json:"non_success_count"`
	PromptTokens      string          `json:"prompt_tokens"`
	CompletionTokens  string          `json:"completion_tokens"`
	CachedTokens      string          `json:"cached_tokens"`
	ReasoningTokens   string          `json:"reasoning_tokens"`
	CostUSD           string          `json:"cost_usd"`
	CostDisplay       string          `json:"cost_display"`
	UsagePresence     string          `json:"usage_presence"`
	MissingUsageCount json.RawMessage `json:"missing_usage_count"`
}

type invocationAcceptanceResponse struct {
	Items    []map[string]json.RawMessage `json:"items"`
	Total    int64                        `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
	Totals   invocationAcceptanceTotals   `json:"totals"`
	Query    struct {
		FilterVersion           int                        `json:"filter_version"`
		NormalizedFilters       map[string]json.RawMessage `json:"normalized_filters"`
		FilterHash              string                     `json:"filter_hash"`
		AsOf                    time.Time                  `json:"as_of"`
		Consistency             string                     `json:"consistency"`
		CrossRequestConsistency string                     `json:"cross_request_consistency"`
		AmountSource            string                     `json:"amount_source"`
		Completeness            string                     `json:"completeness"`
		DisplayMoney            struct {
			Currency       string    `json:"currency"`
			CNYPerUSD      string    `json:"cny_per_usd"`
			RateSnapshotAt time.Time `json:"rate_snapshot_at"`
			RateSource     string    `json:"rate_source"`
		} `json:"display_money"`
	} `json:"query"`
}

type invocationAcceptanceHarness struct {
	server *gin999
	db     *gorm.DB
	tokens map[string]string
}

var invocationAcceptanceOrgs = map[string]uint64{"A": 1001, "B": 1002, "C": 1003}
var invocationAcceptanceKeys = map[string]uint64{"A1": 11, "A2": 12, "B1": 21, "B2": 22, "C1": 31}
var invocationAcceptanceNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func invocationAcceptanceLoad(t *testing.T) []invocationAcceptanceRow {
	t.Helper()
	var data struct {
		Version  int                       `json:"version"`
		FixedNow time.Time                 `json:"fixed_now"`
		Rows     []invocationAcceptanceRow `json:"rows"`
	}
	if err := json.Unmarshal(invocationAcceptanceJSON, &data); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(invocationAcceptanceJSON)
	if hex.EncodeToString(hash[:]) != "dc408fb51296e248bb346a96a7d24731f6aa9769fa1e9aa46a97ec682dfcd61b" || data.Version != 1 || !data.FixedNow.Equal(invocationAcceptanceNow) || len(data.Rows) != 237 {
		t.Fatal("fixed fixture identity changed; review its independent oracle before updating the hash")
	}
	return data.Rows
}

func invocationAcceptanceInt(t *testing.T, text string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		t.Fatalf("invalid fixture integer %q", text)
	}
	return n
}

func invocationAcceptanceRat(t *testing.T, text string) *big.Rat {
	t.Helper()
	n, ok := new(big.Rat).SetString(text)
	if !ok {
		t.Fatalf("invalid exact decimal %q", text)
	}
	return n
}

func invocationAcceptanceOracle(t *testing.T, rows []invocationAcceptanceRow, accept func(invocationAcceptanceRow) bool) ([]string, invocationAcceptanceTotals) {
	t.Helper()
	ids := []string{}
	var totals invocationAcceptanceTotals
	amount := new(big.Rat)
	prompt, completion, cached, reasoning := new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if accept != nil && !accept(r) {
			continue
		}
		ids = append(ids, r.RequestID)
		totals.RecordCount++
		if r.Status == "success" {
			totals.SuccessCount++
		} else {
			totals.NonSuccessCount++
		}
		amount.Add(amount, invocationAcceptanceRat(t, r.CostUSD))
		prompt.Add(prompt, invocationAcceptanceInt(t, r.PromptTokens))
		completion.Add(completion, invocationAcceptanceInt(t, r.CompletionTokens))
		cached.Add(cached, invocationAcceptanceInt(t, r.CachedTokens))
		reasoning.Add(reasoning, invocationAcceptanceInt(t, r.ReasoningTokens))
	}
	totals.CostUSD = amount.FloatString(8)
	totals.PromptTokens, totals.CompletionTokens = prompt.String(), completion.String()
	totals.CachedTokens, totals.ReasoningTokens = cached.String(), reasoning.String()
	return ids, totals
}

func invocationAcceptanceAssertTotals(t *testing.T, got, want invocationAcceptanceTotals) {
	t.Helper()
	if got.RecordCount != want.RecordCount || got.SuccessCount != want.SuccessCount || got.NonSuccessCount != want.NonSuccessCount || got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens || got.CachedTokens != want.CachedTokens || got.ReasoningTokens != want.ReasoningTokens || invocationAcceptanceRat(t, got.CostUSD).Cmp(invocationAcceptanceRat(t, want.CostUSD)) != 0 {
		t.Fatalf("totals differ from independent fixture oracle: got %+v, want %+v", got, want)
	}
	if got.UsagePresence != "legacy_not_distinguishable" || string(got.MissingUsageCount) != "null" {
		t.Fatal("legacy zero usage was falsely classified as present or absent")
	}
}

func invocationAcceptanceSeed(t *testing.T, db *gorm.DB, rows []invocationAcceptanceRow) {
	t.Helper()
	items := make([]invocation.Invocation, 0, len(rows))
	for _, r := range rows {
		parse := func(s string) int64 {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		user := invocationAcceptanceOrgs[r.Org] + 4000
		items = append(items, invocation.Invocation{
			ID: uint64(r.Ordinal), OrgID: invocationAcceptanceOrgs[r.Org], UserID: &user, APIKeyID: invocationAcceptanceKeys[r.Key], RequestID: r.RequestID,
			Model: r.Model, Modality: r.Modality, ChannelID: r.ChannelID, Provider: "synthetic-internal-provider", Status: r.Status, StatusCode: 200, IsStream: r.IsStream,
			PromptTokens: parse(r.PromptTokens), CompletionTokens: parse(r.CompletionTokens), CachedTokens: parse(r.CachedTokens), ReasoningTokens: parse(r.ReasoningTokens),
			CostUSD: decimal.RequireFromString(r.CostUSD), CreatedAt: r.CreatedAt, UpstreamRequestID: "synthetic-upstream-id", PayloadObjectKey: "synthetic-hidden-payload-pointer",
		})
	}
	if err := db.CreateInBatches(items, 50).Error; err != nil {
		t.Fatal(err)
	}
}

func invocationAcceptanceSubjects(t *testing.T, server *gin999, db *gorm.DB) *invocationAcceptanceHarness {
	t.Helper()
	server.srv.now = func() time.Time { return invocationAcceptanceNow }
	h := &invocationAcceptanceHarness{server: server, db: db, tokens: map[string]string{}}
	for _, name := range []string{"A", "B", "C"} {
		org := invocationAcceptanceOrgs[name]
		if err := db.Exec("INSERT INTO organizations (id,slug,name,status) VALUES (?,?,?,1)", org, "ui1-"+name, "Synthetic "+name).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, name := range []string{"A", "B", "C", "superadmin", "admin"} {
		uid, org, role := uint64(5001+i), uint64(1001), "user"
		if v, ok := invocationAcceptanceOrgs[name]; ok {
			org = v
		} else {
			role = name
		}
		if err := db.Exec("INSERT INTO users (id,email,email_canonical,role,status) VALUES (?,?,?,?,1)", uid, name+"@ui1.invalid", name+"@ui1.invalid", role).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO org_members (org_id,user_id,role) VALUES (?,?,'member')", org, uid).Error; err != nil {
			t.Fatal(err)
		}
		token, _, err := identity.IssueSessionToken(testSecret, uid, org, 24*time.Hour, invocationAcceptanceNow)
		if err != nil {
			t.Fatal(err)
		}
		h.tokens[name] = token
	}
	if err := db.Exec("INSERT INTO org_members (org_id,user_id,role) VALUES (1002,5001,'member')").Error; err != nil {
		t.Fatal(err)
	}
	token, _, err := identity.IssueSessionToken(testSecret, 5001, 1002, 24*time.Hour, invocationAcceptanceNow)
	if err != nil {
		t.Fatal(err)
	}
	h.tokens["A-in-B"] = token
	if err := db.Save(&Setting{Name: "cny_per_usd", Value: "6.65"}).Error; err != nil {
		t.Fatal(err)
	}
	return h
}

func invocationAcceptanceSmall(t *testing.T) (*invocationAcceptanceHarness, []invocationAcceptanceRow) {
	t.Helper()
	srv, db := setupRelayServer(t)
	h := invocationAcceptanceSubjects(t, srv, db)
	all := invocationAcceptanceLoad(t)
	rows := []invocationAcceptanceRow{all[0], all[1], all[120], all[121], all[200], all[201]}
	// SQLite's NUMERIC/SUM is not a MySQL DECIMAL acceptance oracle. Keep these
	// HTTP/control tests binary-exact; the full precision fixture runs on MySQL.
	for i := range rows {
		rows[i].CostUSD = "0.125"
		rows[i].PromptTokens = "10"
		rows[i].CompletionTokens = "20"
		rows[i].CachedTokens = "2"
		rows[i].ReasoningTokens = "3"
	}
	invocationAcceptanceSeed(t, db, rows)
	return h, rows
}

func invocationAcceptanceURL(admin bool, values url.Values) string {
	q := url.Values{"filter_version": {"1"}, "since": {"2026-09-30T00:00:00Z"}, "until": {"2026-10-04T00:00:00Z"}, "timezone": {"UTC"}}
	for key, v := range values {
		q[key] = append([]string(nil), v...)
	}
	base := "/api/usage"
	if admin {
		base = "/api/admin/invocations"
	}
	return base + "?" + q.Encode()
}

func invocationAcceptanceCall(h *invocationAcceptanceHarness, request *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.server.engine.ServeHTTP(w, request)
	return w
}

func invocationAcceptanceRequest(h *invocationAcceptanceHarness, subject, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if subject == "root" {
		r.Header.Set("X-Admin-Token", testAdminToken)
	} else if subject != "" {
		r.Header.Set("Authorization", "Bearer "+h.tokens[subject])
	}
	return r
}

func invocationAcceptanceRead(t *testing.T, h *invocationAcceptanceHarness, subject string, admin bool, values url.Values) invocationAcceptanceResponse {
	t.Helper()
	w := invocationAcceptanceCall(h, invocationAcceptanceRequest(h, subject, invocationAcceptanceURL(admin, values)))
	if w.Code != 200 {
		t.Fatalf("query %d: %s", w.Code, w.Body.String())
	}
	var out invocationAcceptanceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Query.FilterVersion != 1 || out.Query.Consistency != "response_snapshot" || out.Query.CrossRequestConsistency != "live" || out.Query.AmountSource != "log_recorded_cost" || out.Query.Completeness != "best_effort" || out.Query.AsOf.IsZero() || len(out.Query.FilterHash) != 71 || out.Query.DisplayMoney.RateSnapshotAt.IsZero() {
		t.Fatal("missing truthful query observation/snapshot contract", w.Body.String())
	}
	return out
}

func invocationAcceptanceIDs(t *testing.T, rows []map[string]json.RawMessage) []string {
	t.Helper()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row["request_id"], &id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func invocationAcceptanceEqualIDs(t *testing.T, got, want []string) {
	t.Helper()
	if !bytes.Equal([]byte(fmt.Sprint(got)), []byte(fmt.Sprint(want))) {
		t.Fatalf("ordered request set differs: got %v want %v", got, want)
	}
}
