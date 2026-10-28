package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

// The actual registerVideoJobs -> ProcessSettlementOnce stack is tracked by
// mainJobs. This complements, rather than claims to replace, SIGTERM coverage.
func TestMySQLQuerySettlementJobTrackedDrain(t *testing.T) {
	for _, mode := range []string{"completed", "cancel_in_candidate"} {
		t.Run(mode, func(t *testing.T) {
			db, _, _ := mutationShutdownPrivateDB(t)
			kr, err := crypto.NewKeyring(mutationShutdownKeySpec, "test")
			if err != nil {
				t.Fatal("settlement job keyring fixture failed")
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			s, err := video.NewService(db, kr, []byte(strings.Repeat("j", 32)), log)
			if err != nil {
				t.Fatal("settlement job service fixture failed")
			}
			var network atomic.Int32
			deny := &http.Client{Transport: mutationShutdownTransport(func(*http.Request) (*http.Response, error) {
				network.Add(1)
				return nil, errors.New("unexpected settlement job HTTP")
			})}
			s.Client = deny
			if err := s.ConfigureAssets(video.AssetIntegrationOptions{Client: videoasset.NewManagementClient(deny)}); err != nil {
				t.Fatal("settlement job assets fixture failed")
			}
			if err := s.Assets.ConfigureConsole(videoasset.ConsoleOptions{}); err != nil {
				t.Fatal("settlement job console fixture failed")
			}
			entered := make(chan struct{})
			var once sync.Once
			var hits atomic.Int32
			name := "test:settlement-host-" + mode
			isSettlement := func(q *gorm.DB) bool {
				return q.Statement.Table == "r" && strings.Contains(q.Statement.SQL.String(), "r.generation_applied_at >= CAST") && strings.Contains(q.Statement.SQL.String(), "r.settlement_kind IN")
			}
			if mode == "cancel_in_candidate" {
				err = db.Callback().Query().Before("gorm:query").Register(name, func(q *gorm.DB) {
					callbacks.BuildQuerySQL(q)
					if !isSettlement(q) {
						return
					}
					deadline, ok := q.Statement.Context.Deadline()
					if !ok || time.Until(deadline) > 2*time.Second {
						q.AddError(errors.New("settlement job missing bounded context"))
						return
					}
					hits.Add(1)
					once.Do(func() { close(entered) })
					<-q.Statement.Context.Done()
				})
			} else {
				err = db.Callback().Query().After("gorm:query").Register(name, func(q *gorm.DB) {
					if isSettlement(q) && q.Error == nil {
						hits.Add(1)
						once.Do(func() { close(entered) })
					}
				})
			}
			if err != nil {
				t.Fatal("settlement job observer failed")
			}
			jobs := newMainJobs()
			// Always cancel/join real workers before removing their callback.
			t.Cleanup(func() {
				jobs.stop()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if jobs.wait(ctx) != nil {
					t.Error("settlement job failed to drain")
					return
				}
				if db.Callback().Query().Remove(name) != nil {
					t.Error("settlement callback cleanup failed")
				}
			})
			registerVideoJobs(jobs, s, func() bool { return true }, log)
			jobs.tracker.mu.Lock()
			active := jobs.tracker.active
			jobs.tracker.mu.Unlock()
			if active != 6 {
				t.Fatal("settlement loop was not separately tracked")
			}
			select {
			case <-entered:
			case <-time.After(6 * time.Second):
				t.Fatal("actual settlement loop did not reach its database candidate")
			}
			if mode == "completed" {
				// Query.After only proves the candidate SELECT finished. Wait for
				// the actual service cycle's successful return before cancellation.
				waitCtx, waitCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				defer waitCancel()
				for {
					completed := false
					for _, cycle := range s.WorkerObservability(waitCtx).Runtime.Cycles {
						if cycle.Kind == "settlement" && cycle.Completed != 0 {
							if cycle.Started != 1 || cycle.Completed != 1 || cycle.Errors != 0 {
								t.Fatal("settlement success control did not complete without error")
							}
							completed = true
						}
					}
					if completed {
						break
					}
					select {
					case <-waitCtx.Done():
						t.Fatal("settlement success control never completed")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			jobs.stop()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := jobs.wait(ctx); err != nil {
				t.Fatal("settlement iteration outlived its tracked shutdown")
			}
			out := s.WorkerObservability(context.Background())
			var started, completed, cycleErrors, lanes uint64
			for _, cycle := range out.Runtime.Cycles {
				if cycle.Kind == "settlement" {
					started, completed, cycleErrors = cycle.Started, cycle.Completed, cycle.Errors
				}
			}
			for _, lane := range out.Runtime.RecoveryLanes {
				if lane.Kind == "query_settlement" {
					lanes = lane.Cycles
				}
			}
			if hits.Load() != 1 || started != 1 || completed != 1 || lanes != 1 || network.Load() != 0 {
				t.Fatal("settlement loop was omitted, double-counted or called upstream")
			}
			if mode == "cancel_in_candidate" && cycleErrors != 1 {
				t.Fatal("cancelled settlement loop hid its context cause")
			}
			if mode == "completed" && cycleErrors != 0 {
				t.Fatal("settlement success control was actually cancelled")
			}
			if jobs.goRun(func(context.Context) {}) {
				t.Fatal("settlement shutdown admitted a later worker")
			}
		})
	}
}
