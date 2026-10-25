package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	shutdownTotal    = 45 * time.Second
	shutdownHTTP     = 10 * time.Second
	shutdownRecorder = 5 * time.Second
)

// drainTracker seals admission before waiting. Add/Wait cannot race: the done
// channel closes only after Seal and the last admitted operation returns.
type drainTracker struct {
	mu     sync.Mutex
	sealed bool
	active int
	done   chan struct{}
}

func newDrainTracker() *drainTracker { return &drainTracker{done: make(chan struct{})} }

func (t *drainTracker) enter() (func(), bool) {
	t.mu.Lock()
	if t.sealed {
		t.mu.Unlock()
		return nil, false
	}
	t.active++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.active--
			if t.sealed && t.active == 0 {
				close(t.done)
			}
		})
	}, true
}

func (t *drainTracker) seal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return
	}
	t.sealed = true
	if t.active == 0 {
		close(t.done)
	}
}

func (t *drainTracker) wait(ctx context.Context) error {
	// Prefer already-completed work even if the deadline expired concurrently.
	select {
	case <-t.done:
		return nil
	default:
	}
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *drainTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done, ok := t.enter()
		if !ok {
			http.Error(w, "server shutting down", http.StatusServiceUnavailable)
			return
		}
		defer done()
		// Preserve the original ResponseWriter and all optional interfaces,
		// especially Hijacker/Flusher used by realtime and streaming handlers.
		next.ServeHTTP(w, r)
	})
}

type mainJobs struct {
	ctx     context.Context
	cancel  context.CancelFunc
	tracker *drainTracker
}

func newMainJobs() *mainJobs {
	ctx, cancel := context.WithCancel(context.Background())
	return &mainJobs{ctx: ctx, cancel: cancel, tracker: newDrainTracker()}
}

func (j *mainJobs) goRun(run func(context.Context)) bool {
	done, ok := j.tracker.enter()
	if !ok {
		return false
	}
	go func() { defer done(); run(j.ctx) }()
	return true
}

func (j *mainJobs) stop()                          { j.tracker.seal(); j.cancel() }
func (j *mainJobs) wait(ctx context.Context) error { return j.tracker.wait(ctx) }

// periodic checks cancellation again after a tick is selected. Cancellation and
// ticker readiness together must not start another iteration with stale work.
func periodic(ctx context.Context, interval time.Duration, run func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			run(ctx)
		}
	}
}

// awaitServerStop registers the listener goroutine with the same lifecycle.
// A listener error is returned to the owner, never os.Exit from a goroutine.
func awaitServerStop(ctx context.Context, jobs *mainJobs, serve func() error) error {
	if ctx.Err() != nil {
		return nil
	}
	result := make(chan error, 1)
	if !jobs.goRun(func(context.Context) { result <- serve() }) {
		return errors.New("server lifecycle already stopped")
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-result:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return errors.New("server stopped before shutdown")
		}
		return err
	}
}

type shutdownBudgets struct{ total, http, recorder time.Duration }

var productionShutdownBudgets = shutdownBudgets{total: shutdownTotal, http: shutdownHTTP, recorder: shutdownRecorder}

// shutdownCore guarantees only main-managed jobs, complete HTTP handler calls,
// and the Recorder. Detached legacy relay metadata jobs are not tracked here.
// No phase closes SQL pools: only process exit reclaims those connections.
func shutdownCore(srv *http.Server, jobs *mainJobs, requests *drainTracker, cancelRequests context.CancelFunc, closeRecorder func(), budgets shutdownBudgets) error {
	if budgets.total <= 0 || budgets.http <= 0 || budgets.recorder <= 0 || budgets.http+budgets.recorder >= budgets.total {
		return errors.New("invalid shutdown budget")
	}
	deadline := time.Now().Add(budgets.total)
	requests.seal()
	jobs.stop()
	var failures []error
	if srv != nil {
		ctx, cancel := context.WithDeadline(context.Background(), minTime(time.Now().Add(budgets.http), deadline.Add(-budgets.recorder)))
		err := srv.Shutdown(ctx)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("http shutdown: %w", err))
		}
	}
	// Shutdown alone does not cancel active handlers or hijacked connections.
	// BaseContext reaches both; the outer tracker waits until their call stack
	// (including late-response evidence persistence) actually returns.
	cancelRequests()
	joinCtx, joinCancel := context.WithDeadline(context.Background(), deadline.Add(-budgets.recorder))
	defer joinCancel()
	jobsErr := jobs.wait(joinCtx)
	if jobsErr != nil {
		failures = append(failures, fmt.Errorf("background jobs did not drain: %w", jobsErr))
	}
	requestsErr := requests.wait(joinCtx)
	if requestsErr != nil {
		failures = append(failures, fmt.Errorf("request handlers did not drain: %w", requestsErr))
	}
	// If a known producer is still running, closing Recorder would race it.
	// Return failure and let the process terminate without closing resources.
	if jobsErr != nil || requestsErr != nil {
		return errors.Join(failures...)
	}
	closed := make(chan struct{})
	go func() { closeRecorder(); close(closed) }()
	closeCtx, closeCancel := context.WithDeadline(context.Background(), minTime(time.Now().Add(budgets.recorder), deadline))
	defer closeCancel()
	select {
	case <-closed:
	case <-closeCtx.Done():
		failures = append(failures, errors.New("recorder did not drain before shutdown deadline"))
	}
	return errors.Join(failures...)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func shutdownExitCode(runErr, drainErr error) int {
	if runErr != nil || drainErr != nil {
		return 1
	}
	return 0
}
