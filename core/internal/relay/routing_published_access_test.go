package relay

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func routingPublishedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := routingChannelDB(t)
	if err := db.Exec("ALTER TABLE channels ADD COLUMN models TEXT NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE channels SET models=' z , Alpha,Alpha,图片, , a' WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	return db
}
func TestRoutingPublishedChannelBoundedExactProjection(t *testing.T) {
	db := routingPublishedDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	if err := tx.Exec("UPDATE channels SET models='new,New' WHERE id=2").Error; err != nil {
		t.Fatal(err)
	}
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("published_projection", func(q *gorm.DB) {
		reads++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("escaped tx")
		}
		if _, ok := q.Statement.Clauses["FOR"]; ok {
			t.Error("unexpected lock")
		}
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("bad_table").Select("credential_enc").Where("id=999").Limit(0)
	got, err := ReadRoutingPublishedChannelsTx(context.Background(), dirty, []uint64{1, 2, 3, 99})
	want := []RoutingPublishedChannel{{ID: 1, Enabled: true, Models: []string{"Alpha", "a", "z", "图片"}}, {ID: 2, OwnerOrgID: 7, Models: []string{"New", "new"}}, {ID: 3, OwnerOrgID: 9, Models: []string{}}}
	if err != nil || !reflect.DeepEqual(got, want) || reads != 1 {
		t.Fatal(got, err, reads)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var models string
	if err := db.Table("channels").Select("models").Where("id=2").Scan(&models).Error; err != nil || models != "" {
		t.Fatal("reader committed changes", models, err)
	}
}
func TestRoutingPublishedChannelRawBounds(t *testing.T) {
	db := routingPublishedDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	// Repeated short public names retain large raw size without invalid names;
	// deduplication must not hide the per-row or batch byte limits.
	value := strings.Repeat("a,", 32768)
	if err := tx.Exec("UPDATE channels SET models=? WHERE id=1", value).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, []uint64{1}); err != nil || !reflect.DeepEqual(got[0].Models, []string{"a"}) {
		t.Fatal(got, err)
	}
	if err := tx.Exec("UPDATE channels SET models=? WHERE id=1", value+"a").Error; err != nil {
		t.Fatal(err)
	}
	var sql string
	if err := db.Callback().Query().After("gorm:query").Register("published_bounded_sql", func(q *gorm.DB) { sql = q.Statement.SQL.String() }); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, []uint64{1}); !errors.Is(err, ErrRoutingPublishedLimit) || got != nil || !strings.Contains(sql, "CASE WHEN") {
		t.Fatal(got, err, sql)
	}
	if err := tx.Exec("UPDATE channels SET models='' WHERE id IN (1,2,3)").Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint64{}
	for i := 10; i < 27; i++ {
		if err := tx.Exec("INSERT INTO channels(id,status,owner_org_id,models) VALUES(?,1,0,?)", i, value).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, uint64(i))
	}
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, ids[:16]); err != nil || len(got) != 16 {
		t.Fatal("exact1MiB", len(got), err)
	} else {
		for i, row := range got {
			if row.ID != ids[i] || !reflect.DeepEqual(row.Models, []string{"a"}) {
				t.Fatal("exact1MiB lost complete deduplicated facts", row)
			}
		}
	}
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, ids); !errors.Is(err, ErrRoutingPublishedLimit) || got != nil {
		t.Fatal("total raw limit", got, err)
	}
}

// Observe the two real context lifetimes without replacing the SQL driver or
// changing production deadlines. Err remains the caller's actual result.
type routingPublishedContextProbe struct {
	context.Context
	query        atomic.Value
	parseChecks  atomic.Int32
	queryNotDone atomic.Bool
}

func (c *routingPublishedContextProbe) Err() error {
	if value := c.query.Load(); value != nil {
		c.parseChecks.Add(1)
		select {
		case <-value.(context.Context).Done():
		default:
			c.queryNotDone.Store(true)
		}
	}
	return c.Context.Err()
}

func TestRoutingPublishedChannelParsingUsesCallerBudget(t *testing.T) {
	db := routingPublishedDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	value := strings.Repeat("a,", 32768)
	ids := make([]uint64, 16)
	for i := range ids {
		ids[i] = uint64(i + 10)
		if err := tx.Exec("INSERT INTO channels(id,status,owner_org_id,models) VALUES(?,1,0,?)", ids[i], value).Error; err != nil {
			t.Fatal(err)
		}
	}
	caller, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	probe := &routingPublishedContextProbe{Context: caller}
	if err := db.Callback().Query().After("gorm:query").Register("published_query_lifetime", func(q *gorm.DB) {
		deadline, ok := q.Statement.Context.Deadline()
		if !ok || time.Until(deadline) > 200*time.Millisecond {
			t.Error("SELECT lost its 200ms child deadline")
		}
		probe.query.Store(q.Statement.Context)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRoutingPublishedChannelsTx(probe, tx, ids)
	if err != nil || len(got) != 16 {
		t.Fatal("complete 1MiB facts must use the common caller budget", len(got), err)
	}
	if probe.parseChecks.Load() == 0 || probe.queryNotDone.Load() || caller.Err() != nil {
		t.Fatal("SQL and parsing did not use separate bounded lifetimes", probe.parseChecks.Load(), probe.queryNotDone.Load(), caller.Err())
	}
}

func TestRoutingPublishedChannelAfterQueryCancellationWinsOverFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []uint64
		bad  bool
	}{
		{name: "existing", ids: []uint64{1}},
		{name: "missing", ids: []uint64{99}},
		{name: "invalid_facts", ids: []uint64{1}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := routingPublishedDB(t)
			tx := db.Begin()
			defer tx.Rollback()
			if tc.bad {
				if err := tx.Exec("UPDATE channels SET models=? WHERE id=1", "invalid\x00model").Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := db.Callback().Query().After("gorm:query").Register("published_cancel_after_query", func(*gorm.DB) { cancel() }); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRoutingPublishedChannelsTx(ctx, tx, tc.ids)
			if got != nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrRoutingPublishedFacts) {
				t.Fatal("canceled SELECT became empty or invalid facts", got, err)
			}
		})
	}
}
func TestRoutingPublishedChannelRejectsBadNamesAndCalls(t *testing.T) {
	db := routingPublishedDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	for _, value := range []string{"bad\x00name", strings.Repeat("a", 129), string([]byte{0xff})} {
		if err := tx.Exec("UPDATE channels SET models=? WHERE id=1", value).Error; err != nil {
			t.Fatal(err)
		}
		if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, []uint64{1}); !errors.Is(err, ErrRoutingPublishedFacts) || got != nil {
			t.Fatal(got, err)
		}
	}
	for _, ids := range [][]uint64{{0}, {2, 1}, {1, 1}, make([]uint64, 101)} {
		if _, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, ids); !errors.Is(err, ErrRoutingPublishedInvalid) {
			t.Fatal(err)
		}
	}
	for _, bad := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
		if _, err := ReadRoutingPublishedChannelsTx(context.Background(), bad, nil); !errors.Is(err, ErrRoutingPublishedInvalid) {
			t.Fatal(err)
		}
	}
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, nil); err != nil || got == nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
func TestRoutingPublishedChannelErrorsAndDeadlines(t *testing.T) {
	db := routingPublishedDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	tx := db.Begin()
	defer tx.Rollback()
	marker := "private-channel-diagnostic"
	if err := db.Callback().Query().After("gorm:query").Register("published_diagnostic", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	_ = tx.Table("channels").Find(&rows).Error
	if !strings.Contains(logs.String(), marker) {
		t.Fatal("logger positive control")
	}
	logs.Reset()
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, []uint64{1}); !errors.Is(err, ErrRoutingPublishedUnavailable) || got != nil || strings.Contains(err.Error(), marker) || logs.Len() != 0 {
		t.Fatal(got, err, logs.String())
	}
	if err := db.Callback().Query().Remove("published_diagnostic"); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("published_deadline", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got, err := ReadRoutingPublishedChannelsTx(context.Background(), tx, []uint64{1}); !errors.Is(err, context.DeadlineExceeded) || got != nil || time.Since(start) > time.Second {
		t.Fatal(got, err, time.Since(start))
	}
}
