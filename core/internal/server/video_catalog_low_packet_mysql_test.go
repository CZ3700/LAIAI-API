package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// This dedicated gate is selected only against a separately owned MySQL
// container started with --max-allowed-packet=1048576. It never changes a
// shared GLOBAL or session setting. A normal suite skip is not its acceptance.
func TestMySQLVideoCatalogLowPacketFailsClosed(t *testing.T) {
	if os.Getenv("LAIAI_CATALOG_LOW_PACKET_TEST") != "isolated-1m-v1" {
		t.Skip("dedicated isolated low-packet container gate required")
	}
	db := routingAdminMySQLDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	var packet uint64
	err := db.WithContext(ctx).Raw("SELECT @@max_allowed_packet").Scan(&packet).Error
	cancel()
	if err != nil || packet != 1_048_576 {
		t.Fatal("dedicated MySQL packet setting is not exactly one MiB")
	}
	const cellCount = 8192
	// Each of the two 16-control-character strings needs at least 98 JSON
	// bytes (16 six-byte escapes plus quotes). The five integer scalars need
	// at least six bytes, including FPS=24; each seven-scalar tuple needs eight
	// punctuation bytes. Including outer commas/brackets gives 211*N+1.
	// This independent lower bound isolates packet overflow from the separate
	// 32000/32001 cardinality gate without changing the production query limit.
	if 211*cellCount+1 <= packet {
		t.Fatal("low-packet fixture cannot prove a serialized result above the packet limit")
	}
	set := catalogTariffsMySQLSet(0)
	if err := db.Create(&set).Error; err != nil {
		t.Fatal("cannot create real minimum-rule set")
	}
	rule := catalogTariffsMySQLRule(0)
	rule.InputVideo, rule.MinimumRuleVersion = true, set.Version
	catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{rule})
	// The same actual three-read reader must work before the large aggregate.
	catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1, 1, 0}, nil, nil)
	cells := make([]videobilling.MinimumRuleCell, cellCount)
	for i := range cells {
		cells[i] = videobilling.MinimumRuleCell{RuleSetID: set.ID, Resolution: strings.Repeat("\x01", 16),
			Ratio: strings.Repeat("\x02", 16), OutputDurationMS: int64(i + 1), FPS: 24, MinimumTokens: 1}
	}
	// Small INSERT packets stay below the dedicated limit. These are physical
	// schema-valid imported facts; their invalid publication meaning cannot turn
	// a truncated transport result into a usable snapshot.
	if err := db.CreateInBatches(&cells, 64).Error; err != nil {
		t.Fatal("cannot store low-packet physical facts below the INSERT packet limit")
	}
	catalogTariffsMySQLCount(t, db, "video_minimum_rule_cells", cellCount)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal("cannot begin low-packet read-only snapshot")
	}
	defer tx.Rollback()
	var statements []*gorm.DB
	var observations []catalogTariffsMySQLStatement
	var started time.Time
	var aggregateStatement bool
	const name = "catalog_low_packet_actual_select"
	observe := func(q *gorm.DB) {
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("low-packet reader escaped its original transaction")
		}
		statements = append(statements, q)
		// Capture the child context while the callback is still inside its
		// execution window. After return the production helper cancels it even
		// on success, so a later child.Err would not identify a timeout.
		observations = append(observations, catalogTariffsMySQLStatement{sql: q.Statement.SQL.String(), executed: q,
			openedNS: time.Since(started).Nanoseconds(), openedError: catalogTariffsDiagnosticError(q.Error),
			openedContext: catalogTariffsDiagnosticError(q.Statement.Context.Err())})
		if len(statements) == 3 {
			aggregateStatement = strings.Contains(q.Statement.SQL.String(), "AS bounded_cells")
		}
	}
	if db.Callback().Query().After("gorm:query").Register(name, observe) != nil ||
		db.Callback().Row().After("gorm:row").Register(name, observe) != nil {
		t.Fatal("cannot observe actual low-packet statements")
	}
	defer db.Callback().Query().Remove(name)
	defer db.Callback().Row().Remove(name)
	started = time.Now()
	snapshot, err := videobilling.ReadCatalogTariffsTx(ctx, tx, catalogTariffsMySQLRef(), catalogTariffsMySQLAt)
	elapsed := time.Since(started)
	parentAtReturn := catalogTariffsDiagnosticError(ctx.Err())
	readerAtReturn := catalogTariffsDiagnosticError(err)
	unavailableAtReturn := errors.Is(err, videobilling.ErrCatalogTariffUnavailable)
	invalidAtReturn := errors.Is(err, videobilling.ErrCatalogTariffInvalid)
	limitAtReturn := errors.Is(err, videobilling.ErrCatalogTariffLimit)
	deadlineAtReturn := errors.Is(err, context.DeadlineExceeded)
	cancelledAtReturn := errors.Is(err, context.Canceled)
	// SQL warnings are diagnostic only, after the production read has failed.
	// Remove observers first; this is not a fourth business SELECT.
	db.Callback().Query().Remove(name)
	db.Callback().Row().Remove(name)
	if !errors.Is(err, videobilling.ErrCatalogTariffUnavailable) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, videobilling.CatalogTariffSnapshot{}) {
		// Only bounded primitive facts are emitted before the original fatal.
		// Row callbacks mean opened, not scan/Close completion. Completed facts
		// come from the actual returned statement handle and private aggregate.
		diagnostic := struct {
			Format            string                              `json:"format"`
			ElapsedNS         int64                               `json:"elapsed_ns"`
			ParentError       string                              `json:"parent_error"`
			ReaderError       string                              `json:"reader_error"`
			ReaderUnavailable bool                                `json:"reader_unavailable"`
			ReaderInvalid     bool                                `json:"reader_invalid"`
			ReaderLimit       bool                                `json:"reader_limit"`
			ReaderDeadline    bool                                `json:"reader_deadline"`
			ReaderCancelled   bool                                `json:"reader_cancelled"`
			ZeroSnapshot      bool                                `json:"zero_snapshot"`
			StatementCount    int                                 `json:"statement_count"`
			Statements        []catalogTariffsDiagnosticStatement `json:"statements"`
		}{Format: "catalog-low-packet-observation-v1", ElapsedNS: elapsed.Nanoseconds(), ParentError: parentAtReturn,
			ReaderError: readerAtReturn, ReaderUnavailable: unavailableAtReturn, ReaderInvalid: invalidAtReturn,
			ReaderLimit: limitAtReturn, ReaderDeadline: deadlineAtReturn, ReaderCancelled: cancelledAtReturn,
			ZeroSnapshot: reflect.DeepEqual(snapshot, videobilling.CatalogTariffSnapshot{}), StatementCount: len(statements)}
		for i, observation := range observations {
			if i == 4 {
				break
			}
			observation.rows = observation.executed.RowsAffected
			fact := catalogTariffsDiagnosticStatementFacts(i, observation)
			if fact.Kind == "query" {
				switch observation.executed.Statement.Table {
				case "video_pricing_rules":
					fact.Kind = "pricing_rules"
				case "video_minimum_rule_sets":
					fact.Kind = "minimum_sets"
				default:
					fact.Kind = "unknown"
				}
			}
			diagnostic.Statements = append(diagnostic.Statements, fact)
		}
		raw, marshalErr := json.Marshal(diagnostic)
		if marshalErr != nil {
			t.Fatal("cannot marshal bounded low-packet diagnostic primitives")
		}
		t.Logf("RC46_LOW_PACKET_OBSERVATION=%s", raw)
		t.Fatal("packet overflow did not fail closed, or a time-budget failure disguised the packet experiment")
	}
	if len(statements) != 3 || statements[2].RowsAffected != 1 || !aggregateStatement {
		t.Fatal("packet failure did not traverse the actual complete three-read aggregate")
	}
	count, decoded, body := catalogTariffsAggregateFacts(t, statements[2])
	if count != cellCount || decoded != 0 || body != nil {
		t.Fatal("expected positive real COUNT and server NULL aggregate, with zero decoded facts")
	}
	// MySQL 8.4 ER_WARN_ALLOWED_PACKET_OVERFLOWED is warning 1301. Require
	// that actual server evidence; an arbitrary SQL error must not pass this gate.
	var warnings []struct{ Code uint16 }
	if tx.Raw("SHOW WARNINGS").Scan(&warnings).Error != nil {
		t.Fatal("cannot read same-connection packet overflow diagnostic")
	}
	found := false
	for _, warning := range warnings {
		found = found || warning.Code == 1301
	}
	if !found {
		t.Fatal("actual packet overflow warning 1301 was absent")
	}
	if tx.Commit().Error != nil {
		t.Fatal("cannot close low-packet read-only transaction")
	}
	t.Logf("dedicated packet_bytes=%d physical_rows=1 cell_count=%d decoded=0 server_null=true warning_code=1301 zero_snapshot=true", packet, cellCount)
}
