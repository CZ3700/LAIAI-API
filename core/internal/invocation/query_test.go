package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestQueryDoesNotLogFiltersOrDatabaseDiagnostics(t *testing.T) {
	db, filter := queryFixture(t)
	var output bytes.Buffer
	noisy := logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info, SlowThreshold: time.Nanosecond})
	db = db.Session(&gorm.Session{Logger: noisy})
	var one int
	if err := db.Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 || output.Len() == 0 {
		t.Fatal("parent SQL logger positive control did not emit")
	}
	output.Reset()
	filter.Model = "private-filter-canary"
	if _, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().After("gorm:query").Register("query_no_diagnostics", func(tx *gorm.DB) {
		tx.AddError(errors.New("private-driver-canary"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20); !errors.Is(err, ErrQueryUnavailable) || strings.Contains(err.Error(), "canary") {
		t.Fatal("query diagnostics escaped the safe error boundary")
	}
	if output.Len() != 0 {
		t.Fatal("query emitted SQL or driver diagnostics through the parent logger")
	}
}

func queryFixture(t *testing.T) (*gorm.DB, QueryFilter) {
	t.Helper()
	db := setup(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	filter := QueryFilter{Since: since, Until: since.AddDate(0, 0, 1), Timezone: "UTC"}
	for i := 1; i <= 25; i++ {
		row := Invocation{
			ID: uint64(i), OrgID: 1, APIKeyID: 11, ChannelID: 7,
			RequestID: fmt.Sprintf("query-%02d", i), Model: "model-a", Modality: ModalityChat,
			Status: StatusSuccess, IsStream: true, PromptTokens: int64(i), CompletionTokens: 2,
			CachedTokens: 1, ReasoningTokens: 1, CostUSD: decimal.NewFromInt(int64(i)),
			CreatedAt: since.Add(time.Duration(i) * time.Second),
		}
		if i%2 == 0 {
			row.APIKeyID, row.ChannelID, row.Model = 12, 8, "model-b"
			row.Modality, row.Status, row.IsStream = "", StatusUpstreamError, false
		}
		if i == 1 {
			row.CreatedAt = since // inclusive lower bound
		}
		if i == 3 {
			row.PromptTokens = 9007199254740993
		}
		if i == 25 {
			row.CreatedAt = filter.Until.Add(-time.Millisecond)
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	outside := []Invocation{
		{ID: 101, OrgID: 2, APIKeyID: 21, ChannelID: 7, RequestID: "other-org", Model: "model-a", Status: StatusSuccess, CreatedAt: since},
		{ID: 102, OrgID: 1, APIKeyID: 11, RequestID: "at-upper-bound", Model: "model-a", Status: StatusSuccess, CreatedAt: filter.Until},
		{ID: 103, OrgID: 1, APIKeyID: 11, RequestID: "before-lower-bound", Model: "model-a", Status: StatusSuccess, CreatedAt: since.Add(-time.Millisecond)},
	}
	if err := db.Create(&outside).Error; err != nil {
		t.Fatal(err)
	}
	return db, filter
}

func TestQueryLogsTenantPageAndWholeFilterTotals(t *testing.T) {
	db, filter := queryFixture(t)
	first, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := QueryTotals{RecordCount: 25, SuccessCount: 13, NonSuccessCount: 12,
		PromptTokens: "9007199254741315", CompletionTokens: "50", CachedTokens: "25", ReasoningTokens: "25", CostUSD: "325"}
	if first.Totals != want || len(first.Items) != 20 || first.Items[0].ID != 25 || first.Items[19].ID != 6 || first.AsOf.IsZero() {
		t.Fatalf("unexpected first page: totals=%+v, rows=%d", first.Totals, len(first.Items))
	}
	second, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if second.Totals != want || len(second.Items) != 5 || second.Items[0].ID != 5 || second.Items[4].ID != 1 {
		t.Fatalf("page 2 must retain whole-filter totals: %+v", second)
	}
	for _, page := range []QueryResult{first, second} {
		for _, row := range page.Items {
			if row.OrgID != 1 {
				t.Fatal("organization scope leaked")
			}
		}
	}
	emptyPage, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 3, 20)
	if err != nil || len(emptyPage.Items) != 0 || emptyPage.Items == nil || emptyPage.Totals != want {
		t.Fatalf("empty page changed aggregate or array shape: %+v %v", emptyPage, err)
	}
}

func TestQueryLogsExactFiltersAndCrossOrganizationIDs(t *testing.T) {
	db, base := queryFixture(t)
	empty, chat, yes, no := "", ModalityChat, true, false
	cases := []struct {
		name  string
		scope QueryScope
		edit  func(*QueryFilter)
		ids   []uint64
	}{
		{"request", OrganizationQueryScope(1), func(f *QueryFilter) { f.RequestID = "query-03" }, []uint64{3}},
		{"foreign_request", OrganizationQueryScope(1), func(f *QueryFilter) { f.RequestID = "other-org" }, nil},
		{"foreign_key", OrganizationQueryScope(1), func(f *QueryFilter) { f.APIKeyID = 21 }, nil},
		{"empty_modality", OrganizationQueryScope(1), func(f *QueryFilter) { f.Modality = &empty }, []uint64{24, 22, 20, 18, 16, 14, 12, 10, 8, 6, 4, 2}},
		{"combined", OrganizationQueryScope(1), func(f *QueryFilter) {
			f.Model, f.Modality, f.IsStream, f.Result, f.APIKeyID = "model-a", &chat, &yes, "success", 11
		}, []uint64{25, 23, 21, 19, 17, 15, 13, 11, 9, 7, 5, 3, 1}},
		{"failed", OrganizationQueryScope(1), func(f *QueryFilter) { f.Result = "failed"; f.IsStream = &no }, []uint64{24, 22, 20, 18, 16, 14, 12, 10, 8, 6, 4, 2}},
		{"status", OrganizationQueryScope(1), func(f *QueryFilter) { f.Status = StatusNoChannel }, nil},
		{"admin_org_channel", AdminQueryScope(), func(f *QueryFilter) { f.OrgID, f.ChannelID = 2, 7 }, []uint64{101}},
		{"sql_text_is_literal", AdminQueryScope(), func(f *QueryFilter) { f.Model = "' OR 1=1 --" }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := base
			tc.edit(&filter)
			got, err := QueryLogs(context.Background(), db, tc.scope, filter, 1, 100)
			if err != nil {
				t.Fatal(err)
			}
			var ids []uint64
			for _, row := range got.Items {
				ids = append(ids, row.ID)
			}
			if !reflect.DeepEqual(ids, tc.ids) || got.Totals.RecordCount != int64(len(tc.ids)) ||
				got.Totals.SuccessCount+got.Totals.NonSuccessCount != got.Totals.RecordCount {
				t.Fatalf("got ids=%v totals=%+v, want=%v", ids, got.Totals, tc.ids)
			}
			if len(tc.ids) == 0 && got.Totals != (QueryTotals{PromptTokens: "0", CompletionTokens: "0", CachedTokens: "0", ReasoningTokens: "0", CostUSD: "0"}) {
				t.Fatalf("empty set must have exact zero strings: %+v", got.Totals)
			}
		})
	}
}

func TestQueryRejectsInvalidAuthorityBeforeDatabase(t *testing.T) {
	filter := QueryFilter{Since: time.Now().UTC(), Until: time.Now().UTC().Add(time.Hour), Timezone: "UTC"}
	for _, scope := range []QueryScope{{}, OrganizationQueryScope(0), {kind: 3}, {kind: 2, orgID: 1}} {
		if _, err := QueryLogs(context.Background(), nil, scope, filter, 1, 20); !errors.Is(err, ErrQueryInvalidScope) {
			t.Fatalf("invalid scope reached database: %v", err)
		}
	}
	for _, edit := range []func(*QueryFilter){func(f *QueryFilter) { f.OrgID = 1 }, func(f *QueryFilter) { f.ChannelID = 7 }} {
		f := filter
		edit(&f)
		if _, err := QueryLogs(context.Background(), nil, OrganizationQueryScope(1), f, 1, 20); !errors.Is(err, ErrQueryInvalidScope) {
			t.Fatalf("org scope accepted administrative filter: %v", err)
		}
	}
}

func TestQueryCalendarWindowAndPaginationValidation(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// This 90-calendar-day window crosses the autumn 25-hour day. A fixed
	// duration guard would incorrectly reject it.
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, zone)
	filter := QueryFilter{Since: since.UTC(), Until: since.AddDate(0, 0, 90).UTC(), Timezone: zone.String()}
	if err := validateQuery(AdminQueryScope(), filter, 5001, 20); err != nil {
		t.Fatalf("valid DST window and inclusive offset limit rejected: %v", err)
	}
	for _, tc := range [][2]int{{0, 20}, {1, 21}, {5002, 20}, {1002, 100}, {math.MaxInt, 100}} {
		if err := validateQuery(AdminQueryScope(), filter, tc[0], tc[1]); !errors.Is(err, ErrQueryInvalidPage) {
			t.Fatalf("invalid pagination accepted: %v => %v", tc, err)
		}
	}
	for name, edit := range map[string]func(*QueryFilter){
		"over90":           func(f *QueryFilter) { f.Until = f.Until.Add(time.Millisecond) },
		"equal":            func(f *QueryFilter) { f.Until = f.Since },
		"missing_time":     func(f *QueryFilter) { f.Since = time.Time{} },
		"local":            func(f *QueryFilter) { f.Timezone = "Local" },
		"bad_zone":         func(f *QueryFilter) { f.Timezone = "Not/A_Timezone" },
		"result_conflict":  func(f *QueryFilter) { f.Result, f.Status = "failed", StatusUpstreamError },
		"unknown_status":   func(f *QueryFilter) { f.Status = "anything" },
		"unknown_result":   func(f *QueryFilter) { f.Result = "anything" },
		"unknown_modality": func(f *QueryFilter) { v := "anything"; f.Modality = &v },
		"long_model":       func(f *QueryFilter) { f.Model = strings.Repeat("m", 129) },
		"long_request":     func(f *QueryFilter) { f.RequestID = strings.Repeat("r", 33) },
	} {
		t.Run(name, func(t *testing.T) {
			f := filter
			edit(&f)
			if err := validateQuery(AdminQueryScope(), f, 1, 20); !errors.Is(err, ErrQueryInvalidFilter) {
				t.Fatalf("invalid filter accepted: %v", err)
			}
		})
	}
}

type queryBeginObserver struct {
	*sql.DB
	options []sql.TxOptions
}

func (p *queryBeginObserver) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	observed := sql.TxOptions{}
	if opts != nil {
		observed = *opts
	}
	p.options = append(p.options, observed)
	return p.DB.BeginTx(ctx, opts)
}

func TestQueryUsesTwoSelectsInOwnReadOnlyRepeatableReadTransaction(t *testing.T) {
	db, filter := queryFixture(t)
	sqlDB, _ := db.DB()
	pool := &queryBeginObserver{DB: sqlDB}
	db.Statement.ConnPool = pool
	var statements []string
	if err := db.Callback().Query().After("gorm:query").Register("query_test_read_only", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20)
	if err != nil || got.Totals.RecordCount != 25 {
		t.Fatalf("query failed: %v %+v", err, got.Totals)
	}
	if len(pool.options) != 1 || pool.options[0].Isolation != sql.LevelRepeatableRead || !pool.options[0].ReadOnly {
		t.Fatalf("wrong transaction options: %+v", pool.options)
	}
	if len(statements) != 2 || !strings.Contains(statements[0], "COUNT(*)") || !strings.Contains(statements[1], "ORDER BY id DESC") {
		t.Fatalf("expected exactly aggregate and page selects: %v", statements)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := QueryLogs(context.Background(), tx, AdminQueryScope(), filter, 1, 20)
		if !errors.Is(err, ErrQueryUnavailable) {
			t.Fatalf("nested transaction accepted: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFreezesOptionalPredicateValues(t *testing.T) {
	db, filter := queryFixture(t)
	modality, stream := ModalityChat, true
	filter.Modality, filter.IsStream = &modality, &stream
	if err := db.Callback().Query().After("gorm:query").Register("query_test_freeze", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "COUNT(*)") {
			modality, stream = "", false
		}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20)
	if err != nil || got.Totals.RecordCount != 13 || len(got.Items) != 13 {
		t.Fatalf("external filter mutation changed row predicate: rows=%d totals=%+v err=%v", len(got.Items), got.Totals, err)
	}
	for _, row := range got.Items {
		if row.Modality != ModalityChat || !row.IsStream {
			t.Fatal("different predicate used for the second select")
		}
	}
}

func TestQueryNeverReturnsPartialSuccess(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("select_%d", failAt), func(t *testing.T) {
			db, filter := queryFixture(t)
			n := 0
			failure := errors.New("internal database detail must not become an HTTP error")
			if err := db.Callback().Query().Before("gorm:query").Register("query_test_fail", func(tx *gorm.DB) {
				n++
				if n == failAt {
					tx.AddError(failure)
				}
			}); err != nil {
				t.Fatal(err)
			}
			got, err := QueryLogs(context.Background(), db, OrganizationQueryScope(1), filter, 1, 20)
			if !errors.Is(err, ErrQueryUnavailable) || !errors.Is(err, failure) || err.Error() != ErrQueryUnavailable.Error() || !reflect.DeepEqual(got, QueryResult{}) {
				t.Fatalf("query failure became partial success or leaked detail: %+v %v", got, err)
			}
		})
	}
	db, filter := queryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := QueryLogs(ctx, db, AdminQueryScope(), filter, 1, 20); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, QueryResult{}) {
		t.Fatalf("cancellation lost: %+v %v", got, err)
	}
}

func TestQueryCancellationBetweenSelectsDiscardsAggregate(t *testing.T) {
	db, filter := queryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selects := 0
	if err := db.Callback().Query().After("gorm:query").Register("query_test_cancel_after_aggregate", func(tx *gorm.DB) {
		selects++
		if strings.Contains(tx.Statement.SQL.String(), "COUNT(*)") {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := QueryLogs(ctx, db, OrganizationQueryScope(1), filter, 1, 20)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, QueryResult{}) || selects != 1 {
		t.Fatalf("cancellation must stop before the page and discard aggregate: %+v %v selects=%d", got, err, selects)
	}
}

func TestQuerySelectHintHonorsRemainingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1250*time.Millisecond)
	defer cancel()
	prefix, err := querySelectPrefix(ctx, "mysql")
	var ms int
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(prefix, "/*+ MAX_EXECUTION_TIME(%d) */ ", &ms); err != nil || ms < 1 || ms > 1250 {
		t.Fatalf("unsafe query deadline hint: %q %v", prefix, err)
	}
	short, stop := context.WithDeadline(context.Background(), time.Now().Add(time.Microsecond))
	defer stop()
	if prefix, err := querySelectPrefix(short, "mysql"); prefix != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sub-millisecond deadline must not generate an unlimited hint: %q %v", prefix, err)
	}
	if _, err := querySelectPrefix(context.Background(), "mysql"); !errors.Is(err, ErrQueryUnavailable) {
		t.Fatalf("unbounded mysql query accepted: %v", err)
	}
}

func TestQueryAggregateConvertsArbitraryPrecisionWithoutIntegerOrFloat(t *testing.T) {
	a := queryAggregate{RecordCount: 2, SuccessCount: 1, PromptTokens: "18446744073709551614", CompletionTokens: "9007199254740993",
		CachedTokens: "0", ReasoningTokens: "1", CostUSD: "1999999999999.99999998"}
	got, err := a.totals()
	if err != nil || got.PromptTokens != a.PromptTokens || got.CompletionTokens != a.CompletionTokens || got.CostUSD != a.CostUSD || got.NonSuccessCount != 1 {
		t.Fatalf("precision lost: %+v %v", got, err)
	}
	for _, bad := range []string{"", "not-a-number", "1.5"} {
		broken := a
		broken.PromptTokens = bad
		if _, err := broken.totals(); !errors.Is(err, ErrQueryUnavailable) {
			t.Fatalf("invalid token aggregate accepted: %q %v", bad, err)
		}
	}
}
