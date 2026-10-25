package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const invocationScaleRows = 100000

var invocationScaleStart = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func invocationScaleRequestID(ordinal int) string { return fmt.Sprintf("scale-%026d", ordinal) }

// The oracle uses ordinal membership and integer arithmetic only. It does not
// invoke the production parser, predicate, SQL builder or result aggregation.
func invocationScaleOracle(accept func(int) bool) (invocationAcceptanceTotals, []string) {
	var totals invocationAcceptanceTotals
	amount, prompt, completion, cached, reasoning := new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	ids := make([]string, 0, 100)
	for i := invocationScaleRows - 1; i >= 0; i-- {
		if !accept(i) {
			continue
		}
		totals.RecordCount++
		if i%5 < 4 {
			totals.SuccessCount++
		} else {
			totals.NonSuccessCount++
		}
		amount.Add(amount, big.NewInt(int64(1+i%23)))
		prompt.Add(prompt, big.NewInt(int64(100+i%17)))
		completion.Add(completion, big.NewInt(int64(50+i%13)))
		cached.Add(cached, big.NewInt(int64(i%7)))
		reasoning.Add(reasoning, big.NewInt(int64(i%5)))
		if len(ids) < 100 {
			ids = append(ids, invocationScaleRequestID(i))
		}
	}
	totals.CostUSD = new(big.Rat).SetFrac(amount, big.NewInt(100000000)).FloatString(8)
	totals.PromptTokens, totals.CompletionTokens = prompt.String(), completion.String()
	totals.CachedTokens, totals.ReasoningTokens = cached.String(), reasoning.String()
	return totals, ids
}

type invocationScaleStatement struct {
	sql  string
	args []any
}

type invocationScalePlan struct {
	Table  sql.NullString `gorm:"column:table"`
	Access sql.NullString `gorm:"column:type"`
	Key    sql.NullString `gorm:"column:key"`
	Rows   sql.NullInt64  `gorm:"column:rows"`
}

// EXPLAIN consumes the captured production SQL on a separate connection, after
// the business query has completed. No SQL or bound values are printed.
func invocationScaleExplain(t *testing.T, ctx context.Context, db *gorm.DB, label string, statements []invocationScaleStatement, uniquePage bool) {
	t.Helper()
	if len(statements) != 2 || !strings.Contains(statements[0].sql, "COUNT(*)") || strings.Contains(statements[1].sql, "COUNT(*)") {
		t.Fatal("scale query did not issue exactly one aggregate and one page SELECT")
	}
	for index, statement := range statements {
		if !strings.HasPrefix(strings.ToUpper(statement.sql), "SELECT ") || !strings.Contains(statement.sql, "MAX_EXECUTION_TIME(") {
			t.Fatal("captured statement is not a bounded production SELECT")
		}
		var plan []invocationScalePlan
		short, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := db.WithContext(short).Raw("EXPLAIN "+statement.sql, statement.args...).Scan(&plan).Error
		cancel()
		if err != nil || len(plan) == 0 {
			t.Fatal("EXPLAIN failed for the captured production statement")
		}
		unique := false
		for _, row := range plan {
			t.Logf("scale case=%s select=%d table=%s access=%s key=%s estimated_rows=%d rows_known=%t", label, index+1,
				row.Table.String, row.Access.String, row.Key.String, row.Rows.Int64, row.Rows.Valid)
			switch row.Access.String {
			case "const", "eq_ref", "ref", "range":
				if row.Table.String == "invocations" && row.Key.String == "uk_inv_request" && row.Rows.Valid && row.Rows.Int64 >= 0 && row.Rows.Int64 <= 1 {
					unique = true
				}
			}
		}
		// Wide scans are evidence, not an optimizer-version-dependent index
		// assertion. An existing exact request, however, must have a bounded
		// unique page lookup; the aggregate may be optimized away entirely.
		if uniquePage && index == 1 && !unique {
			t.Fatal("existing exact request did not use a bounded unique page lookup")
		}
	}
}

func TestMySQLInvocationScale100KAndExplain(t *testing.T) {
	// Includes fixture insertion, business calls and plan capture. QueryLogs and
	// the HTTP path independently retain their existing three-second deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	h, writer := invocationAcceptanceMySQL(t)
	full, _ := invocationScaleOracle(func(int) bool { return true })
	// Independently calculated fixed-distribution checks prevent a changed
	// seed formula and a changed oracle from silently redefining the sample.
	if full.RecordCount != 100000 || full.SuccessCount != 80000 || full.NonSuccessCount != 20000 ||
		full.CostUSD != "0.01199962" || full.PromptTokens != "10799967" || full.CompletionTokens != "5599982" ||
		full.CachedTokens != "299995" || full.ReasoningTokens != "200000" {
		t.Fatal("scale oracle changed from its reviewed fixed distribution")
	}
	seedStarted := time.Now()
	for offset := 0; offset < invocationScaleRows; offset += 1000 {
		batch := make([]invocation.Invocation, 0, 1000)
		for i := offset; i < offset+1000; i++ {
			org := uint64(1001 + i%3)
			user := org + 4000
			status := invocation.StatusSuccess
			if i%5 == 4 {
				status = invocation.StatusUpstreamError
			}
			batch = append(batch, invocation.Invocation{
				ID: uint64(i + 1), OrgID: org, UserID: &user, APIKeyID: uint64(11 + 10*(i%3)),
				RequestID: invocationScaleRequestID(i), Model: fmt.Sprintf("scale-model-%d", (i/3)%5),
				Modality: invocation.ModalityChat, ChannelID: uint64(201 + i%2), Provider: "synthetic-scale",
				Status: status, StatusCode: 200, IsStream: i%2 == 0,
				PromptTokens: int64(100 + i%17), CompletionTokens: int64(50 + i%13), CachedTokens: int64(i % 7), ReasoningTokens: int64(i % 5),
				CostUSD:   decimal.New(int64(1+i%23), -8),
				CreatedAt: invocationScaleStart.AddDate(0, 0, (i/15)%90).Add(time.Duration(i%15) * time.Second),
			})
		}
		if err := writer.WithContext(ctx).CreateInBatches(batch, 1000).Error; err != nil {
			t.Fatal("bounded scale fixture insert failed")
		}
	}
	t.Logf("scale fixture rows=%d organizations=3 models=5 natural_days=90 batch_rows=1000 seed_ms=%d", invocationScaleRows, time.Since(seedStarted).Milliseconds())
	// Refresh statistics in this private synthetic schema only. Plans continue
	// to be observed rather than forced with USE/FORCE INDEX.
	short, stop := context.WithTimeout(ctx, 3*time.Second)
	err := writer.WithContext(short).Exec("ANALYZE TABLE invocations").Error
	stop()
	if err != nil {
		t.Fatal("private scale fixture statistics refresh failed")
	}

	var mu sync.Mutex
	var captured []invocationScaleStatement
	const callback = "test:ui1-scale-capture-selects"
	if err := h.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "invocations" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, invocationScaleStatement{sql: tx.Statement.SQL.String(), args: append([]any(nil), tx.Statement.Vars...)})
	}); err != nil {
		t.Fatal("could not register scale statement observation")
	}
	t.Cleanup(func() { _ = h.db.Callback().Query().Remove(callback) })
	take := func() []invocationScaleStatement {
		mu.Lock()
		defer mu.Unlock()
		out := append([]invocationScaleStatement(nil), captured...)
		captured = nil
		return out
	}
	base := invocation.QueryFilter{Since: invocationScaleStart, Until: invocationScaleStart.AddDate(0, 0, 90), Timezone: "UTC"}
	modelWeek := base
	modelWeek.Since, modelWeek.Until = invocationScaleStart.AddDate(0, 0, 30), invocationScaleStart.AddDate(0, 0, 37)
	modelWeek.Model = "scale-model-2"
	exact := base
	exact.RequestID = invocationScaleRequestID(54321) // This ordinal belongs to organization A.
	cases := []struct {
		name, subject string
		admin, unique bool
		scope         invocation.QueryScope
		filter        invocation.QueryFilter
		accept        func(int) bool
	}{
		{"platform_90_days", "superadmin", true, false, invocation.AdminQueryScope(), base, func(int) bool { return true }},
		{"organization_90_days", "A", false, false, invocation.OrganizationQueryScope(1001), base, func(i int) bool { return i%3 == 0 }},
		{"organization_model_7_days", "A", false, false, invocation.OrganizationQueryScope(1001), modelWeek, func(i int) bool {
			day := (i / 15) % 90
			return i%3 == 0 && (i/3)%5 == 2 && day >= 30 && day < 37
		}},
		{"exact_request", "A", false, true, invocation.OrganizationQueryScope(1001), exact, func(i int) bool { return i == 54321 }},
		{"foreign_request_empty", "B", false, false, invocation.OrganizationQueryScope(1002), exact, func(int) bool { return false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, wantIDs := invocationScaleOracle(tc.accept)
			started := time.Now()
			result, err := invocation.QueryLogs(ctx, h.db, tc.scope, tc.filter, 1, 100)
			if err != nil {
				t.Fatal("scale domain query failed within its unchanged business deadline")
			}
			elapsed := time.Since(started)
			got := invocationAcceptanceTotals{
				RecordCount: result.Totals.RecordCount, SuccessCount: result.Totals.SuccessCount, NonSuccessCount: result.Totals.NonSuccessCount,
				PromptTokens: result.Totals.PromptTokens, CompletionTokens: result.Totals.CompletionTokens,
				CachedTokens: result.Totals.CachedTokens, ReasoningTokens: result.Totals.ReasoningTokens, CostUSD: result.Totals.CostUSD,
				UsagePresence: "legacy_not_distinguishable", MissingUsageCount: json.RawMessage("null"),
			}
			invocationAcceptanceAssertTotals(t, got, want)
			ids := make([]string, 0, len(result.Items))
			for _, row := range result.Items {
				ids = append(ids, row.RequestID)
			}
			if !reflect.DeepEqual(ids, wantIDs) {
				t.Fatal("scale domain page differs from independent ordinal membership")
			}
			t.Logf("scale case=%s path=domain matched=%d returned=%d elapsed_ms=%d", tc.name, got.RecordCount, len(ids), elapsed.Milliseconds())
			invocationScaleExplain(t, ctx, writer, tc.name+"/domain", take(), tc.unique)

			values := url.Values{"since": {tc.filter.Since.Format(time.RFC3339Nano)}, "until": {tc.filter.Until.Format(time.RFC3339Nano)}, "page_size": {"100"}}
			if tc.filter.Model != "" {
				values.Set("model", tc.filter.Model)
			}
			if tc.filter.RequestID != "" {
				values.Set("request_id", tc.filter.RequestID)
			}
			request := invocationAcceptanceRequest(h, tc.subject, invocationAcceptanceURL(tc.admin, values)).WithContext(ctx)
			started = time.Now()
			response := invocationAcceptanceCall(h, request)
			elapsed = time.Since(started)
			if response.Code != http.StatusOK {
				t.Fatalf("authorized scale HTTP query failed: status=%d", response.Code)
			}
			var out invocationAcceptanceResponse
			if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
				t.Fatal("scale HTTP response could not be decoded")
			}
			invocationAcceptanceAssertTotals(t, out.Totals, want)
			if out.Total != want.RecordCount || !reflect.DeepEqual(invocationAcceptanceIDs(t, out.Items), wantIDs) {
				t.Fatal("authorized scale HTTP page differs from independent ordinal membership")
			}
			t.Logf("scale case=%s path=authorized_http matched=%d returned=%d elapsed_ms=%d", tc.name, out.Total, len(out.Items), elapsed.Milliseconds())
			invocationScaleExplain(t, ctx, writer, tc.name+"/authorized_http", take(), tc.unique)
		})
	}
	if ctx.Err() != nil {
		t.Fatal("scale evidence exceeded its five-minute test budget")
	}
	t.Log("100000 synthetic rows characterize this sample only; no 500-concurrency or arbitrary-production-size capacity claim")
}
