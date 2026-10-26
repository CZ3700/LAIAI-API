package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
)

func authorityMutationNow(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var now time.Time
	authorityFixtureTx(t, db, func(tx *gorm.DB) (err error) {
		now, err = authorityFixtureNow(tx)
		return err
	})
	return now
}

func authorityMutationDueBetween(t *testing.T, due, before, after time.Time, interval time.Duration) {
	t.Helper()
	if due.Before(before.Add(interval)) || due.After(after.Add(interval)) {
		t.Fatalf("schedule is outside independent primary-clock bracket: due=%s before=%s after=%s interval=%s", due.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano), interval)
	}
}

// B06: each positive case advances only Service.Now after real input reads,
// safely inside the existing 24h input TTL. The negative case expires only the
// exact claim at the final COUNT boundary. Neither case models whole-host TTL.
func TestMySQLDatabaseAuthorityFinalMutationSendUsesPrimaryLease(t *testing.T) {
	for _, mode := range []string{"legacy_create", "legacy_delete", "inbox_cancel", "inbox_delete"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%v", mode, expired), func(t *testing.T) {
				var f *fixture
				var p *mutationProducerFixture
				var op Operation
				var taskID string
				if mode == "legacy_create" {
					f = mysqlFixture(t)
					f.s.ResponseInboxEnabled = false
					v := f.create(t, "db-clock-final-create")
					taskID, op = v.ID, runtimeWorkerCreateOperation(t, f, v.ID)
				} else {
					final, action := "succeeded", "delete_task"
					if mode == "inbox_cancel" {
						final, action = "", "cancel_task"
					}
					p = newMutationProducerFixture(t, final)
					f, taskID = p.fixture, p.taskID
					f.s.MutationResponseInboxEnabled = mode != "legacy_delete"
					op = p.pending(t, action)
				}
				funds := authorityFinancialFacts(t, f, taskID)
				var hit atomic.Int32
				name := fmt.Sprintf("authority-final-mutation-%d", f.key.OrgID)
				must(t, f.s.DB.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table != "o" {
						return
					}
					finalCount := false
					for _, join := range tx.Statement.Joins {
						if strings.Contains(join.Name, "video_task_states v") {
							finalCount = true
						}
					}
					if !finalCount || hit.Add(1) != 1 {
						return
					}
					var current Operation
					must(t, f.s.DB.Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&current).Error)
					if current.Status != "in_flight" || current.AttemptID == "" || current.LeaseUntil == nil {
						t.Fatal("injection did not reach a committed real claim")
					}
					if expired {
						expireFixtureOperationLease(t, f.s.DB, current)
					} else {
						// A captured lease appears old to this host but remains live
						// in MySQL. The input remains well inside its separate TTL.
						f.now = f.now.Add(time.Hour)
					}
				}))
				t.Cleanup(func() { _ = f.s.DB.Callback().Query().Remove(name) })
				var err error
				if mode == "legacy_create" {
					err = f.s.processOperation(context.Background(), op)
				} else {
					err = f.s.processMutation(context.Background(), op)
				}
				must(t, f.s.DB.Callback().Query().Remove(name))
				calls := f.posts.Load()
				if p != nil {
					calls = p.deletes.Load()
				}
				if hit.Load() != 1 || authorityFinancialFacts(t, f, taskID) != funds {
					t.Fatal("final primary-clock guard was not reached exactly once or changed financial facts")
				}
				var current Operation
				must(t, f.s.DB.Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&current).Error)
				if expired {
					if calls != 0 || current.Status != "in_flight" || current.ReceiptRef != "" || current.ReceiptAppliedAt != nil || responseInboxCount(t, f, &ResponseInbox{}, "org_id = ? AND operation_id = ?", op.OrgID, op.ID) != 0 || responseInboxCount(t, f, &Attempt{}, "org_id = ? AND operation_id = ? AND phase = ?", op.OrgID, op.ID, "not_sent") != 0 {
						t.Fatal("database-expired claim sent, fabricated no-send evidence or changed operation outcome")
					}
				} else {
					must(t, err)
					if calls != 1 || current.ReceiptAppliedAt == nil {
						t.Fatal("valid primary lease was rejected because of shifted Service.Now")
					}
				}
				if p != nil && mode != "legacy_delete" {
					p.execution(t, op) // Real schema-2 Stage/HMAC, never inserted by this test.
				}
			})
		}
	}
}

func TestMySQLDatabaseAuthorityCooldownAndNextDueUsePrimaryTime(t *testing.T) {
	t.Run("preparation_cooldown_successor_and_local_terminal", func(t *testing.T) {
		p := newMutationProducerFixture(t, "")
		op := p.pending(t, "cancel_task")
		n := p.another(t, "clock-cooldown-successor")
		healthy := n.pending(t, "cancel_task")
		local := p.another(t, "clock-cooldown-local")
		localOp := local.pending(t, "cancel_task")
		funds := authorityFinancialFacts(t, p.fixture, p.taskID)
		reads := map[string]int{}
		name := fmt.Sprintf("authority-cooldown-%d", p.key.OrgID)
		must(t, p.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
			if row, ok := tx.Statement.Dest.(*CredentialSnapshot); ok && (row.TaskID == p.taskID || row.TaskID == local.taskID) {
				reads[row.TaskID]++
				tx.AddError(errors.New("synthetic credential preparation failure"))
			}
		}))
		t.Cleanup(func() { _ = p.s.DB.Callback().Query().Remove(name) })
		before := authorityMutationNow(t, p.s.DB)
		if p.s.processMutation(context.Background(), op) == nil || p.s.processMutation(context.Background(), localOp) == nil {
			t.Fatal("fixture preparation failure was hidden")
		}
		after := authorityMutationNow(t, p.s.DB)
		cooling := p.operation(t, op.ID)
		authorityMutationDueBetween(t, cooling.UpdatedAt, before, after, 0)
		if cooling.Status != "pending" || cooling.ErrorCode != mutationPreparationDeferredCode || cooling.Version != op.Version || cooling.AttemptID != "" {
			t.Fatal("real preparation did not persist the original pending cooldown")
		}
		p.now = p.now.Add(time.Hour)
		for i := 0; i < 3; i++ {
			_ = p.s.processMutation(context.Background(), op)
		}
		if reads[p.taskID] != 1 || p.operation(t, op.ID).UpdatedAt != cooling.UpdatedAt {
			t.Fatal("host-clock advance bypassed primary cooldown or repeated preparation")
		}
		// Real authoritative running progress makes this cancellation locally
		// rejectable; the active cooldown and missing credential cannot block it.
		local.observe(t, ark.StatusRunning, true)
		must(t, p.s.processMutation(context.Background(), localOp))
		if local.operation(t, localOp.ID).Status != "rejected" || reads[local.taskID] != 1 || p.deletes.Load() != 0 {
			t.Fatal("cooldown blocked an authoritative locally decidable outcome")
		}
		var candidates []Operation
		q := p.s.pendingOperationCandidates(context.Background()).Where("o.org_id = ?", p.key.OrgID)
		must(t, p.s.DB.Table("(?) AS eligible", q).Where("scope_rank = 1").Limit(20).Find(&candidates).Error)
		if len(candidates) != 1 || candidates[0].ID != healthy.ID {
			t.Fatal("cooldown consumed the same-scope candidate slot")
		}
		must(t, p.s.processMutation(context.Background(), candidates[0]))
		must(t, p.s.DB.Callback().Query().Remove(name))
		if p.deletes.Load() != 1 || authorityFinancialFacts(t, p.fixture, p.taskID) != funds {
			t.Fatal("healthy successor failed or cooldown changed financial facts")
		}
		expireFixturePreparationCooldown(t, p.s.DB, p.operation(t, op.ID))
		_, st := p.rows(t)
		makeFixtureProviderRateDue(t, p.s.DB, st.ProviderScopeID, "delete")
		must(t, p.s.processMutation(context.Background(), op))
		if p.deletes.Load() != 2 || p.operation(t, op.ID).ErrorCode == mutationPreparationDeferredCode || authorityFinancialFacts(t, p.fixture, p.taskID) != funds {
			t.Fatal("database-expired cooldown did not resume exactly one genuine send")
		}
	})

	for _, point := range []string{"create_accepted", "cancel_ack", "query_claim"} {
		t.Run(point, func(t *testing.T) {
			var f *fixture
			var p *mutationProducerFixture
			var id string
			if point == "create_accepted" {
				f = mysqlFixture(t)
				f.s.ResponseInboxEnabled = true
				id = f.create(t, "clock-create-next-due").ID
			} else {
				p = newMutationProducerFixture(t, "")
				f, id = p.fixture, p.taskID
			}
			funds := authorityFinancialFacts(t, f, id)
			f.now = f.now.Add(time.Hour) // Still inside the real 24h execution TTL.
			before := authorityMutationNow(t, f.s.DB)
			var claimed State
			switch point {
			case "create_accepted":
				must(t, f.s.processOperation(context.Background(), runtimeWorkerCreateOperation(t, f, id)))
				if f.posts.Load() != 1 {
					t.Fatal("create acceptance did not follow one real mock POST")
				}
			case "cancel_ack":
				op := p.pending(t, "cancel_task")
				must(t, p.s.processMutation(context.Background(), op))
				if p.deletes.Load() != 1 || p.operation(t, op.ID).Status != "unknown" {
					t.Fatal("cancel scheduling did not follow one genuine ACK")
				}
			case "query_claim":
				_, st := p.rows(t)
				makeFixtureQueryDue(t, f.s.DB, f.key.OrgID, id, st.QuerySequence, false)
				p.respond = func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet {
						return nil, errors.New("unexpected next-due test HTTP method")
					}
					_, claimed = p.rows(t)
					return reply(200, `{"id":"`+path.Base(r.URL.Path)+`","status":"running"}`), nil
				}
				must(t, f.s.pollTask(context.Background(), f.key.OrgID, id))
				if p.gets.Load() != 1 || claimed.QuerySequence != st.QuerySequence+1 || claimed.QueryLeaseUntil == nil {
					t.Fatal("query scheduling was not observed after its actual claim and before GET response")
				}
			}
			after := authorityMutationNow(t, f.s.DB)
			var st State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, id).Take(&st).Error)
			interval := time.Duration(0)
			if point == "query_claim" {
				interval = 10 * time.Second
				authorityMutationDueBetween(t, *claimed.QueryLeaseUntil, before, after, 180*time.Second)
				authorityMutationDueBetween(t, claimed.NextPollAt, before, after, interval)
			}
			authorityMutationDueBetween(t, st.NextPollAt, before, after, interval)
			if authorityFinancialFacts(t, f, id) != funds {
				t.Fatal("scheduling/ACK/running query changed financial facts")
			}
		})
	}
}
