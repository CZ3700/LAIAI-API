package videobilling

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func publicationRowsFixture(t *testing.T) (*gorm.DB, *gorm.DB, []MinimumRuleCell) {
	t.Helper()
	db := publicationDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	r := ruleFixture()
	r.RuleID = "reference"
	r.InputVideo = true
	r.MinimumRuleVersion = "minimum-v1"
	set, cells := publicationMinimumFixture()
	for _, value := range []any{&r, &set, &cells} {
		if err := tx.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, tx, cells
}

func TestCatalogPublicationAggregateRowsUseExactProjectionAndTransaction(t *testing.T) {
	db, tx, want := publicationRowsFixture(t)
	var executed *gorm.DB
	var actualRows *sql.Rows
	var actualSQL string
	var actualVars []any
	var child context.Context
	if err := db.Callback().Row().After("gorm:row").Register("publication_typed_rows", func(q *gorm.DB) {
		executed, actualSQL, actualVars, child = q, q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...), q.Statement.Context
		actualRows, _ = q.Statement.Dest.(*sql.Rows)
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("typed rows escaped caller transaction")
		}
		if deadline, ok := child.Deadline(); !ok || time.Until(deadline) > 200*time.Millisecond {
			t.Error("typed rows lack 200ms deadline")
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove("publication_typed_rows") })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := ReadCatalogTariffsTx(ctx, tx.Table("wrong_private_table").Where("id=999").Select("secret").Limit(1), []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
	if err != nil || !reflect.DeepEqual(s.cells, want) || executed == nil || executed.RowsAffected != 1 {
		t.Fatalf("typed rows lost exact fields or actual count: %v", err)
	}
	if !strings.HasPrefix(actualSQL, "SELECT COUNT(*) AS cell_count, json_group_array(json_array(id, rule_set_id, resolution, ratio, output_duration_ms, fps, minimum_tokens)) AS cells_json FROM (SELECT id, rule_set_id, resolution, ratio, output_duration_ms, fps, minimum_tokens FROM ") || !strings.Contains(actualSQL, "ORDER BY id LIMIT 32001) AS bounded_cells") || !reflect.DeepEqual(actualVars, []any{uint64(1)}) {
		t.Fatal("typed rows changed trusted projection or parameterized identity")
	}
	result, ok := executed.Statement.Dest.(*catalogMinimumAggregate)
	if !ok || !result.CellCount.Valid || result.CellCount.Int64 != int64(len(want)) || result.DecodedCount != len(want) {
		t.Fatal("physical aggregate row concealed actual cell or decoded counts")
	}
	if actualRows == nil || actualRows.Next() || child == nil || !errors.Is(child.Err(), context.Canceled) || ctx.Err() != nil {
		t.Fatal("row set or child deadline was not closed independently")
	}
	if out, err := s.Evaluate(ruleFixture().PricingKey, publicationFixture()); err != nil || out.Price == nil {
		t.Fatal("completed row deadline leaked into snapshot evaluation", err)
	}
}

func TestCatalogPublicationAggregateRowsFailureReturnsZeroSnapshot(t *testing.T) {
	for _, mode := range []string{"sql_error", "after_open_error", "invalid_field", "cancel_after_open", "deadline_before_open", "sqlite_null_empty"} {
		t.Run(mode, func(t *testing.T) {
			db, tx, _ := publicationRowsFixture(t)
			if mode == "invalid_field" {
				// SQLite's actual dynamic value passes through aggregation unchanged
				// and must be rejected, not coerced, by the common exact decoder.
				if err := tx.Exec("UPDATE video_minimum_rule_cells SET fps='private_scan_value' WHERE id=2").Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "sqlite_null_empty" {
				if err := tx.Exec("DELETE FROM video_minimum_rule_cells").Error; err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			warn := logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var opened *sql.Rows
			var executed *gorm.DB
			var child context.Context
			if err := db.Callback().Row().Before("gorm:row").Register("publication_row_before", func(q *gorm.DB) {
				if mode == "sql_error" {
					q.Statement.Selects = []string{"private_nonexistent_column"}
				}
				if mode == "sqlite_null_empty" {
					// Actual SQL returns count=0 plus NULL; that is not SQLite's
					// supported empty json_group_array value and cannot be normalized.
					q.Statement.Selects = []string{"COUNT(*) AS cell_count, NULL AS cells_json"}
				}
				if mode == "deadline_before_open" {
					<-q.Statement.Context.Done()
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Row().Remove("publication_row_before") })
			if err := db.Callback().Row().After("gorm:row").Register("publication_row_after", func(q *gorm.DB) {
				executed, child = q, q.Statement.Context
				opened, _ = q.Statement.Dest.(*sql.Rows)
				if mode == "after_open_error" {
					if opened == nil {
						t.Error("after-open fault did not acquire real rows")
					}
					q.AddError(errors.New("private_driver_error"))
				}
				if mode == "cancel_after_open" {
					if opened == nil {
						t.Error("cancel fault did not acquire real rows")
					}
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Row().Remove("publication_row_after") })
			s, err := ReadCatalogTariffsTx(ctx, tx.Session(&gorm.Session{Logger: warn}), []CatalogTariffRef{{11, "seedance-2.0", "v1"}}, testAt)
			if !errors.Is(err, ErrCatalogTariffUnavailable) || err.Error() != ErrCatalogTariffUnavailable.Error() || !reflect.DeepEqual(s, CatalogTariffSnapshot{}) || logs.Len() != 0 {
				t.Fatal("row failure disclosed data or returned partial snapshot", err)
			}
			if executed == nil || child == nil || child.Err() == nil || (opened != nil && opened.Next()) {
				t.Fatal("failed row read leaked rows or child context")
			}
			wantCount := int64(0)
			if mode == "invalid_field" || mode == "sqlite_null_empty" {
				wantCount = 1
			}
			if executed.RowsAffected != wantCount {
				t.Fatalf("actual visited rows=%d want=%d", executed.RowsAffected, wantCount)
			}
			if mode == "cancel_after_open" && !errors.Is(err, context.Canceled) {
				t.Fatal("row cancellation cause lost")
			}
			if mode == "deadline_before_open" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("row deadline cause lost")
			}
			_ = db.Callback().Row().Remove("publication_row_before")
			_ = db.Callback().Row().Remove("publication_row_after")
			_ = tx.Session(&gorm.Session{Logger: warn}).Exec("SELECT inherited_row_warn_control FROM nonexistent_publication_rows_table").Error
			if !strings.Contains(logs.String(), "inherited_row_warn_control") {
				t.Fatal("inherited logger positive control missing")
			}
		})
	}
}

// Fault-only rows verify ownership of Err/Close and cancellation during those
// stages. They do not stand in for real SQL cardinality or performance evidence.
type publicationFaultRows struct {
	left, scanned, closed     int
	scanErr, rowErr, closeErr error
	onScan, onClose           func()
}

func (r *publicationFaultRows) Next() bool { return r.left > 0 }
func (r *publicationFaultRows) Scan(dest ...any) error {
	r.left--
	r.scanned++
	if r.onScan != nil {
		r.onScan()
	}
	if r.scanErr != nil {
		return r.scanErr
	}
	*dest[0].(*sql.NullInt64) = sql.NullInt64{Int64: 1, Valid: true}
	*dest[1].(*[]byte) = []byte(`[[7,9,"720p","16:9",4000,24,123456789]]`)
	return nil
}
func (r *publicationFaultRows) Err() error { return r.rowErr }
func (r *publicationFaultRows) Close() error {
	r.closed++
	if r.onClose != nil {
		r.onClose()
	}
	return r.closeErr
}

func TestCatalogPublicationAggregateRowsAlwaysCloseWithinChild(t *testing.T) {
	fault := errors.New("synthetic_rows_fault")
	for _, mode := range []string{"success", "scan_error", "rows_error", "close_error", "cancel_before_scan", "cancel_during_scan", "cancel_during_close", "no_physical_row", "extra_physical_row"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			rows := &publicationFaultRows{left: 1}
			wantErr := error(nil)
			switch mode {
			case "no_physical_row":
				rows.left, wantErr = 0, ErrCatalogTariffUnavailable
			case "extra_physical_row":
				rows.left, wantErr = 2, ErrCatalogTariffUnavailable
			case "scan_error":
				rows.scanErr, wantErr = fault, fault
			case "rows_error":
				rows.rowErr, wantErr = fault, fault
			case "close_error":
				rows.closeErr, wantErr = fault, fault
			case "cancel_before_scan":
				cancel()
				wantErr = context.Canceled
			case "cancel_during_scan":
				rows.onScan, wantErr = cancel, context.Canceled
			case "cancel_during_close":
				wantErr = context.Canceled
				rows.onClose = func() {
					if ctx.Err() != nil {
						t.Error("child disposed before Close")
					}
					cancel()
				}
			}
			var aggregate catalogMinimumAggregate
			count, err := scanCatalogMinimumAggregate(ctx, rows, &aggregate)
			wantCount := int64(rows.scanned)
			if mode == "extra_physical_row" {
				wantCount++ // visited the unexpected second row without scanning it.
			}
			if !errors.Is(err, wantErr) || rows.closed != 1 || count != wantCount {
				t.Fatalf("rows lifecycle err=%v count=%d scans=%d closes=%d", err, count, rows.scanned, rows.closed)
			}
			if mode == "success" {
				cells, decodeErr := decodeCatalogMinimumAggregate(context.Background(), &aggregate)
				if decodeErr != nil || !reflect.DeepEqual(cells, []MinimumRuleCell{{ID: 7, RuleSetID: 9, Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 123456789}}) || aggregate.DecodedCount != 1 {
					t.Fatal("aggregate projection changed values", decodeErr)
				}
			}
		})
	}
}
