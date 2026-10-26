package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func TestMySQLVideoCatalogAggregatePhysicalExtremes(t *testing.T) {
	db := routingAdminMySQLDatabase(t)
	// This is a test-environment prerequisite, outside the measured production
	// three reads. Never SET GLOBAL or alter the business connection settings.
	preflight, stop := context.WithTimeout(context.Background(), time.Second)
	var packet uint64
	err := db.WithContext(preflight).Raw("SELECT @@max_allowed_packet").Scan(&packet).Error
	stop()
	if err != nil || packet < 16_385_024 {
		t.Fatal("isolated MySQL packet prerequisite is unavailable or below the derived row budget")
	}
	t.Logf("aggregate packet prerequisite bytes=%d required=16385024; no low-packet experiment performed", packet)
	set := catalogTariffsMySQLSet(0)
	set.ID = math.MaxUint64
	if err := db.Exec("INSERT INTO video_minimum_rule_sets(id,model,version,source_url,evidence_hash,observed_at,verified_by,verified_at) VALUES(?,?,?,?,?,?,?,?)",
		set.ID, set.Model, set.Version, set.SourceURL, set.EvidenceHash, set.ObservedAt, set.VerifiedBy, set.VerifiedAt).Error; err != nil {
		t.Fatal("tracked unsigned minimum-set identity fixture failed")
	}
	rule := catalogTariffsMySQLRule(0)
	rule.InputVideo, rule.MinimumRuleVersion = true, set.Version
	catalogTariffsMySQLInsertRules(t, db, []videobilling.PricingRule{rule})
	t.Run("empty_is_one_physical_row", func(t *testing.T) {
		catalogTariffsMySQLRead(t, db, catalogTariffsMySQLRef(), []int64{1, 1, 0}, nil, func(s videobilling.CatalogTariffSnapshot) {
			catalogTariffsMySQLReason(t, s, rule.PricingKey, videobilling.CatalogTariffMinimumCoverageMissing)
		})
	})
	// These are schema-valid imported facts, deliberately not legal publication
	// cells. The first three preserve physical extrema outside the requested
	// dimensions. The fourth matches the request and has an invalid zero floor,
	// so the unchanged pure rule validator must reject that relevant bad fact.
	want := []videobilling.MinimumRuleCell{
		{ID: 9007199254740993, RuleSetID: set.ID, Resolution: strings.Repeat("😀", 16), Ratio: strings.Repeat("\x00", 16), OutputDurationMS: math.MinInt64, FPS: math.MaxInt64, MinimumTokens: 9007199254740993},
		{ID: uint64(math.MaxInt64) + 1, RuleSetID: set.ID, Resolution: strings.Repeat("\\", 16), Ratio: strings.Repeat("\"", 16), OutputDurationMS: math.MaxInt64, FPS: math.MinInt64, MinimumTokens: math.MinInt64},
		{ID: math.MaxUint64, RuleSetID: set.ID, Resolution: strings.Repeat("\x01", 16), Ratio: strings.Repeat("/", 16), OutputDurationMS: 9007199254740993, FPS: 0, MinimumTokens: math.MaxInt64},
		{ID: 9007199254740994, RuleSetID: set.ID, Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 0},
	}
	for _, cell := range want {
		if err := db.Exec("INSERT INTO video_minimum_rule_cells(id,rule_set_id,resolution,ratio,output_duration_ms,fps,minimum_tokens) VALUES(?,?,?,?,?,?,?)",
			cell.ID, cell.RuleSetID, cell.Resolution, cell.Ratio, cell.OutputDurationMS, cell.FPS, cell.MinimumTokens).Error; err != nil {
			t.Fatal("tracked physical column extreme was not stored exactly")
		}
	}
	t.Run("exact_integer_and_string_facts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		tx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if tx.Error != nil {
			t.Fatal("cannot begin exact aggregate snapshot")
		}
		defer tx.Rollback()
		var actual *gorm.DB
		const name = "catalog_aggregate_extreme"
		if err := db.Callback().Row().After("gorm:row").Register(name, func(q *gorm.DB) {
			if actual != nil || q.Statement.ConnPool != tx.Statement.ConnPool || !strings.Contains(q.Statement.SQL.String(), "AS bounded_cells") {
				t.Error("aggregate extreme did not use one actual same-transaction SELECT")
			}
			actual = q
		}); err != nil {
			t.Fatal("cannot observe exact aggregate")
		}
		defer db.Callback().Row().Remove(name)
		snapshot, err := videobilling.ReadCatalogTariffsTx(ctx, tx, catalogTariffsMySQLRef(), catalogTariffsMySQLAt)
		if err != nil || actual == nil || actual.RowsAffected != 1 {
			t.Fatal("physical extrema aggregate did not complete in unchanged budget", err)
		}
		count, decoded, body := catalogTariffsAggregateFacts(t, actual)
		if count != int64(len(want)) || decoded != count || len(body) > 512*len(want)+2 {
			t.Fatal("aggregate count, exact decoder or derived size bound failed")
		}
		// Independent raw-token oracle: never parse numbers through float64 or
		// call the production tuple parser to compute the expected values.
		var tuples [][]json.RawMessage
		if json.Unmarshal(body, &tuples) != nil || len(tuples) != len(want) {
			t.Fatal("database aggregate was not a complete array of seven-tuples")
		}
		expected := make(map[string]videobilling.MinimumRuleCell)
		for _, cell := range want {
			expected[fmt.Sprint(cell.ID)] = cell
		}
		for _, tuple := range tuples {
			if len(tuple) != 7 {
				t.Fatal("database aggregation dropped or added columns")
			}
			cell, ok := expected[string(tuple[0])]
			if !ok || string(tuple[1]) != fmt.Sprint(cell.RuleSetID) || string(tuple[4]) != fmt.Sprint(cell.OutputDurationMS) || string(tuple[5]) != fmt.Sprint(cell.FPS) || string(tuple[6]) != fmt.Sprint(cell.MinimumTokens) {
				t.Fatal("database JSON rounded or changed a full-range integer")
			}
			var resolution, ratio string
			if json.Unmarshal(tuple[2], &resolution) != nil || json.Unmarshal(tuple[3], &ratio) != nil || resolution != cell.Resolution || ratio != cell.Ratio {
				t.Fatal("database JSON changed Unicode or escaped scalar facts")
			}
			delete(expected, string(tuple[0]))
		}
		if len(expected) != 0 {
			t.Fatal("aggregate lost or duplicated a physical tuple")
		}
		catalogTariffsMySQLReason(t, snapshot, rule.PricingKey, videobilling.CatalogTariffMinimumRuleInvalid)
		if err := tx.Commit().Error; err != nil {
			t.Fatal("cannot commit bounded read-only extreme snapshot")
		}
		t.Logf("aggregate physical_rows=1 cell_count=%d decoded=%d json_bytes=%d derived_tuple_bound=%d", count, decoded, len(body), 512*len(want)+2)
	})
}
