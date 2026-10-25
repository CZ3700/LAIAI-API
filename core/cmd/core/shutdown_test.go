package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var testShutdownBudgets = shutdownBudgets{total: 2 * time.Second, http: 50 * time.Millisecond, recorder: 100 * time.Millisecond}

func await(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier timed out")
	}
}

func TestShutdownWaitsForCompleteJobAndHandlerBeforeRecorder(t *testing.T) {
	jobs := newMainJobs()
	requests := newDrainTracker()
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	jobStarted, jobSaving, jobRelease, jobSaved := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	jobs.goRun(func(ctx context.Context) {
		close(jobStarted)
		<-ctx.Done()
		close(jobSaving)
		<-jobRelease
		close(jobSaved)
	})
	handlerStarted, handlerSaving, handlerRelease, handlerSaved := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := requests.wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-r.Context().Done()
		// Same call stack as a response saved with WithoutCancel: HTTP caller
		// cancellation must not make the request tracker report it completed.
		if context.WithoutCancel(r.Context()).Err() != nil {
			panic("detached context canceled")
		}
		close(handlerSaving)
		<-handlerRelease
		close(handlerSaved)
	}))
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(requestCtx))
	await(t, jobStarted)
	await(t, handlerStarted)
	closed := make(chan struct{})
	drained := make(chan error, 1)
	go func() {
		drained <- shutdownCore(nil, jobs, requests, cancelRequests, func() {
			select {
			case <-jobSaved:
			default:
				panic("recorder closed before job saved")
			}
			select {
			case <-handlerSaved:
			default:
				panic("recorder closed before handler saved")
			}
			close(closed)
		}, testShutdownBudgets)
	}()
	await(t, jobSaving)
	await(t, handlerSaving)
	select {
	case <-closed:
		t.Fatal("recorder closed while known producers run")
	default:
	}
	close(jobRelease)
	await(t, jobSaved)
	select {
	case <-closed:
		t.Fatal("recorder closed before handler return")
	default:
	}
	close(handlerRelease)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	await(t, closed)
	if jobs.goRun(func(context.Context) { t.Error("new shutdown job ran") }) {
		t.Fatal("admission reopened")
	}
}

func TestShutdownTimeoutIsBoundedAndDoesNotCloseRecorderBeforeProducers(t *testing.T) {
	for _, producer := range []string{"job", "handler"} {
		t.Run(producer, func(t *testing.T) {
			jobs := newMainJobs()
			requests := newDrainTracker()
			release := make(chan struct{})
			started := make(chan struct{})
			if producer == "job" {
				jobs.goRun(func(context.Context) { close(started); <-release })
			} else {
				done, _ := requests.enter()
				go func() { close(started); <-release; done() }()
			}
			await(t, started)
			closed := atomic.Bool{}
			began := time.Now()
			err := shutdownCore(nil, jobs, requests, func() {}, func() { closed.Store(true) }, shutdownBudgets{total: 120 * time.Millisecond, http: 20 * time.Millisecond, recorder: 30 * time.Millisecond})
			close(release)
			if shutdownExitCode(nil, err) != 1 || closed.Load() {
				t.Fatal("undrained producer reported success or Recorder closed", err)
			}
			if time.Since(began) > time.Second {
				t.Fatal("shutdown exceeded bound")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if e := jobs.wait(ctx); e != nil {
				t.Fatal(e)
			}
			if e := requests.wait(ctx); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestShutdownRecorderTimeoutReturnsFailure(t *testing.T) {
	jobs := newMainJobs()
	requests := newDrainTracker()
	release, started := make(chan struct{}), make(chan struct{})
	defer close(release)
	err := shutdownCore(nil, jobs, requests, func() {}, func() { close(started); <-release }, shutdownBudgets{total: 150 * time.Millisecond, http: 20 * time.Millisecond, recorder: 30 * time.Millisecond})
	await(t, started)
	if shutdownExitCode(nil, err) != 1 || !strings.Contains(err.Error(), "recorder") {
		t.Fatal("recorder timeout reported success", err)
	}
}

func TestRequestTrackerPreservesWriterAndSealsAtomically(t *testing.T) {
	tracker := newDrainTracker()
	writer := httptest.NewRecorder()
	called := false
	h := tracker.wrap(http.HandlerFunc(func(got http.ResponseWriter, _ *http.Request) {
		if got != writer {
			t.Error("ResponseWriter wrapped")
		}
		called = true
		got.WriteHeader(204)
	}))
	h.ServeHTTP(writer, httptest.NewRequest("GET", "/", nil))
	if !called || writer.Code != 204 {
		t.Fatal("handler not reached")
	}
	tracker.seal()
	tracker.seal()
	called = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if called || w.Code != 503 {
		t.Fatal("sealed handler admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := tracker.wait(ctx); e != nil {
		t.Fatal("completed tracker lost to canceled wait", e)
	}
	// Race Add/Seal/Done repeatedly. The tracker never closes early or admits
	// after seal. Completion is deliberately idempotent for error cleanup.
	for n := 0; n < 100; n++ {
		tr := newDrainTracker()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if done, ok := tr.enter(); ok {
					done()
					done()
				}
			}()
		}
		close(start)
		tr.seal()
		wg.Wait()
		if _, ok := tr.enter(); ok {
			t.Fatal("admitted after seal")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if e := tr.wait(ctx); e != nil {
			t.Fatal(e)
		}
		cancel()
	}
}

func TestPeriodicDoesNotStartWorkAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	periodic(ctx, time.Nanosecond, func(context.Context) { called = true })
	if called {
		t.Fatal("pre-canceled periodic started work")
	}
	ctx, cancel = context.WithCancel(context.Background())
	calls := 0
	periodic(ctx, time.Nanosecond, func(context.Context) { calls++; cancel() })
	if calls != 1 {
		t.Fatal("periodic ran another ready tick after cancellation", calls)
	}
}

func TestListenerFailureUsesSameDrainPath(t *testing.T) {
	jobs := newMainJobs()
	requests := newDrainTracker()
	saved := make(chan struct{})
	jobs.goRun(func(ctx context.Context) { <-ctx.Done(); close(saved) })
	expected := errors.New("test listener unavailable")
	runErr := awaitServerStop(context.Background(), jobs, func() error { return expected })
	closed := false
	drainErr := shutdownCore(nil, jobs, requests, func() {}, func() { <-saved; closed = true }, testShutdownBudgets)
	if !errors.Is(runErr, expected) || drainErr != nil || !closed || shutdownExitCode(runErr, drainErr) != 1 {
		t.Fatal("listener failure skipped cleanup", runErr, drainErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := awaitServerStop(ctx, newMainJobs(), func() error { t.Error("opened listener after signal"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTimeoutUsesFreshDrainContextForLateEvidence(t *testing.T) {
	jobs := newMainJobs()
	requests := newDrainTracker()
	base, cancelBase := context.WithCancel(context.Background())
	started, canceled, release, saved := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := &http.Server{BaseContext: func(net.Listener) context.Context { return base }, Handler: requests.wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
		close(saved)
	}))}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	jobs.goRun(func(context.Context) { _ = srv.Serve(listener) })
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := http.Client{Timeout: 3 * time.Second}
		r, e := client.Get("http://" + listener.Addr().String())
		if e == nil {
			r.Body.Close()
		}
	}()
	await(t, started)
	result := make(chan error, 1)
	closed := make(chan struct{})
	go func() {
		result <- shutdownCore(srv, jobs, requests, cancelBase, func() { close(closed) }, testShutdownBudgets)
	}()
	await(t, canceled) // HTTP phase exhausted; fresh join context is still live.
	select {
	case <-result:
		t.Fatal("reused expired HTTP context for drain")
	case <-closed:
		t.Fatal("closed Recorder before late save")
	default:
	}
	close(release)
	await(t, saved)
	err := <-result
	if err == nil || !strings.Contains(err.Error(), "http shutdown") || strings.Contains(err.Error(), "did not drain") {
		t.Fatal("late evidence was not drained with independent budget", err)
	}
	await(t, closed)
	await(t, clientDone)
}

func TestHijackedHandlerIsCanceledAndJoined(t *testing.T) {
	jobs := newMainJobs()
	requests := newDrainTracker()
	base, cancelBase := context.WithCancel(context.Background())
	started, saved := make(chan struct{}), make(chan struct{})
	srv := &http.Server{BaseContext: func(net.Listener) context.Context { return base }, Handler: requests.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			panic(e)
		}
		defer conn.Close()
		close(started)
		<-r.Context().Done()
		close(saved)
	}))}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	jobs.goRun(func(context.Context) { _ = srv.Serve(listener) })
	conn, e := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); e != nil {
		t.Fatal(e)
	}
	await(t, started)
	closed := false
	e = shutdownCore(srv, jobs, requests, cancelBase, func() {
		select {
		case <-saved:
			closed = true
		default:
			panic("hijacked call still active")
		}
	}, testShutdownBudgets)
	if e != nil || !closed {
		t.Fatal("hijacked handler not joined", e)
	}
}

func TestShutdownChildProcess(t *testing.T) {
	mode := os.Getenv("LAIAI_TEST_SHUTDOWN_CHILD")
	if mode == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := newMainJobs()
	requests := newDrainTracker()
	started := make(chan struct{})
	jobs.goRun(func(jobCtx context.Context) {
		close(started)
		<-jobCtx.Done()
		if mode == "stuck" {
			select {}
		}
		fmt.Println("SAVED")
	})
	<-started
	if os.Getenv("LAIAI_TEST_SHUTDOWN_SIGNAL") != "1" {
		go func() { _, _ = bufio.NewReader(os.Stdin).ReadString('\n'); cancel() }()
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		os.Exit(3)
	}
	base, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{Handler: requests.wrap(http.NotFoundHandler()), BaseContext: func(net.Listener) context.Context { return base }}
	fmt.Println("READY")
	runErr := awaitServerStop(ctx, jobs, func() error { return srv.Serve(listener) })
	drainErr := shutdownCore(srv, jobs, requests, cancelBase, func() { fmt.Println("RECORDER") }, shutdownBudgets{total: 400 * time.Millisecond, http: 80 * time.Millisecond, recorder: 50 * time.Millisecond})
	os.Exit(shutdownExitCode(runErr, drainErr))
}

func runShutdownChild(t *testing.T, mode string, signalStop bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownChildProcess$")
	cmd.Env = append(os.Environ(), "LAIAI_TEST_SHUTDOWN_CHILD="+mode)
	if signalStop {
		cmd.Env = append(cmd.Env, "LAIAI_TEST_SHUTDOWN_SIGNAL=1")
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(stdout)
	line, e := reader.ReadString('\n')
	if e != nil || strings.TrimSpace(line) != "READY" {
		t.Fatal("child startup", line, e)
	}
	if signalStop {
		e = cmd.Process.Signal(syscall.SIGTERM)
	} else {
		_, e = io.WriteString(stdin, "stop\n")
	}
	stdin.Close()
	if e != nil {
		t.Fatal(e)
	}
	remaining, e := io.ReadAll(reader)
	if e != nil {
		t.Fatal(e)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatal("child shutdown exceeded total deadline")
	}
	if mode == "stuck" {
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() != 1 || strings.Contains(string(remaining), "RECORDER") {
			t.Fatalf("stuck child did not fail without closing Recorder: %v %q", waitErr, remaining)
		}
	} else {
		if waitErr != nil || strings.Index(string(remaining), "SAVED") < 0 || strings.Index(string(remaining), "RECORDER") < strings.Index(string(remaining), "SAVED") {
			t.Fatalf("child exit/order: %v %q %s", waitErr, remaining, stderr.String())
		}
	}
}

func TestShutdownSubprocessExitCodes(t *testing.T) {
	for _, mode := range []string{"complete", "stuck"} {
		t.Run(mode, func(t *testing.T) { runShutdownChild(t, mode, false) })
	}
}

func TestShutdownSIGTERMSubprocessExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery is verified on Linux CI; Windows runs the same child exit path via stdin")
	}
	for _, mode := range []string{"complete", "stuck"} {
		t.Run(mode, func(t *testing.T) { runShutdownChild(t, mode, true) })
	}
}
