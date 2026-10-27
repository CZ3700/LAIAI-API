package video

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A P-only test file replaces this function. No production callback is added.
// N/C cannot obtain a producer by changing the process environment.
var phaseConfigureProducer = func(s *Service, gate string) (string, error) {
	if gate != "" && gate != "false" {
		return "", errPhaseHarness
	}
	return "not_present", nil
}

type phaseSettings struct{ run, caseID, instance, role, source, dir, dsn, mock, token, gate string }
type phaseCycle struct {
	command phaseCommand
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}
type phaseBarrier struct {
	task, boundary, finance string
	armedID                 string
	reached                 bool
	release                 chan struct{}
	released                bool
	network                 phaseHTTPCounts
	proof                   *phaseSnapshot
}
type phaseHarness struct {
	e                phaseSettings
	db, verify       *gorm.DB
	pool, verifyPool *sql.DB
	s                *Service
	router           *gin.Engine
	seed             phaseSeed
	schema, producer string
	transport        *http.Transport
	mu, emitMu       sync.Mutex
	http             phaseHTTPCounts
	active           *phaseCycle
	barrier          *phaseBarrier
	fatal            bool
	sequence         uint64
	columns, primary map[string][]string
}

func phaseEnvironment() (phaseSettings, error) {
	e := phaseSettings{run: os.Getenv("ARK_PHASE_HANDOFF_RUN"), caseID: os.Getenv("ARK_PHASE_HANDOFF_CASE"), instance: os.Getenv("ARK_PHASE_HANDOFF_INSTANCE"), role: os.Getenv("ARK_PHASE_HANDOFF_ROLE"), source: os.Getenv("ARK_PHASE_HANDOFF_SOURCE"), dir: os.Getenv("ARK_PHASE_HANDOFF_DIR"), dsn: os.Getenv("ARK_PHASE_HANDOFF_DSN"), mock: os.Getenv("ARK_PHASE_HANDOFF_MOCK_URL"), token: os.Getenv("ARK_PHASE_HANDOFF_MOCK_TOKEN"), gate: os.Getenv("ARK_PHASE_HANDOFF_GATE")}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("PROTOCOL RUN CASE INSTANCE ROLE SOURCE DIR DSN MOCK_URL MOCK_TOKEN GATE") {
		allowed["ARK_PHASE_HANDOFF_"+k] = true
	}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "ARK_PHASE_HANDOFF_") && !allowed[k] {
			return e, errPhaseHarness
		}
	}
	if os.Getenv("ARK_PHASE_HANDOFF_PROTOCOL") != phaseProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.run) || !phaseCaseValid(e.caseID) || !regexp.MustCompile(`^(N|C|P|D)-[a-z0-9-]{1,32}$`).MatchString(e.instance) || !strings.HasPrefix(e.instance, e.role+"-") || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(e.source) || e.source != queryPhaseCompiledSource || e.role != queryPhaseCompiledRole {
		return e, errPhaseHarness
	}
	if (e.role == "N") != (e.source == phaseOldSource) || (e.role != "N" && e.role != "C" && e.role != "P" && e.role != "D") {
		return e, errPhaseHarness
	}
	u, err := url.Parse(e.mock)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(e.token) < 24 || len(e.token) > 128 || strings.ContainsAny(e.token, "\r\n") || !filepath.IsAbs(e.dir) {
		return e, errPhaseHarness
	}
	if e.gate != "" && e.gate != "false" && e.gate != "true" {
		return e, errPhaseHarness
	}
	info, err := os.Lstat(e.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return e, errPhaseHarness
	}
	return e, nil
}

func TestMySQLQueryPhaseBinaryHandoffProcess(t *testing.T) {
	if os.Getenv("ARK_PHASE_HANDOFF_PROTOCOL") == "" {
		t.Skip("dedicated fixed-source phase handoff runner required")
	}
	e, err := phaseEnvironment()
	if err != nil {
		t.Fatal("invalid isolated phase environment")
	}
	h := &phaseHarness{e: e, columns: map[string][]string{}, primary: map[string][]string{}}
	closed := false
	defer func() {
		if !closed {
			if h.close() != nil {
				t.Error("phase emergency drain failed")
			}
		}
	}()
	if h.initialize() != nil {
		t.Fatal("phase initialization failed")
	}
	_, settlement := any(h.s).(phaseSettlementConsumer)
	if settlement == (e.role == "N") || (e.role == "P") != (h.producer != "not_present") {
		t.Fatal("compiled phase capabilities mismatch")
	}
	if h.emit("0", "ready", map[string]any{"pid": os.Getpid(), "schema_version": h.schema, "settlement": settlement, "producer_gate": h.producer, "source": e.source}, nil) != nil {
		t.Fatal("phase ready persistence failed")
	}
	input := make(chan []byte)
	inputErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 65537)
		for scanner.Scan() {
			input <- append([]byte(nil), scanner.Bytes()...)
		}
		inputErr <- scanner.Err()
		close(input)
	}()
	var last uint64
	for {
		raw, ok := <-input
		if !ok {
			<-inputErr
			t.Fatal("phase input ended without stop")
		}
		c, err := decodePhaseCommand(raw)
		id, idErr := phaseID(c.ID)
		if err != nil || idErr != nil || id != last+1 || c.Run != e.run || c.Case != e.caseID || c.Instance != e.instance {
			t.Fatal("invalid phase command ownership")
		}
		last = id
		h.mu.Lock()
		active := h.active
		failed := h.fatal
		h.mu.Unlock()
		if failed {
			t.Fatal("phase prior command failed")
		}
		if active != nil {
			select {
			case <-active.done:
				h.mu.Lock()
				h.active = nil
				h.mu.Unlock()
				active = nil
			default:
			}
		}
		if c.Command == "stop" {
			if h.close() != nil {
				t.Fatal("phase drain failed")
			}
			closed = true
			if h.emit(c.ID, "drained", map[string]any{"joined": true, "closed": true}, nil) != nil {
				t.Fatal("phase drain evidence failed")
			}
			return
		}
		if active != nil && !(c.Command == "barrier" && c.Action == "release") {
			t.Fatal("phase command overlaps active work")
		}
		if c.Command == "cycle" {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			cycle := &phaseCycle{command: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
			h.mu.Lock()
			h.active = cycle
			h.mu.Unlock()
			go func() {
				defer close(cycle.done)
				defer cancel()
				cycle.err = h.executeCycle(cycle)
				if cycle.err != nil {
					h.mu.Lock()
					h.fatal = true
					h.mu.Unlock()
				}
			}()
			continue
		}
		kind, data, err := h.command(c)
		if err != nil {
			_ = h.emit(c.ID, "failed", map[string]any{"class": "harness_failure", "command": c.Command}, nil)
			t.Fatal("phase command failed")
		}
		proof, err := h.inspect()
		if err != nil {
			t.Fatal("phase command snapshot failed")
		}
		if h.emit(c.ID, kind, data, &proof) != nil {
			t.Fatal("phase command evidence failed")
		}
	}
}

func (h *phaseHarness) initialize() error {
	cfg, err := mysqlDriver.ParseDSN(h.e.dsn)
	if err != nil || cfg.Net != "tcp" || !regexp.MustCompile(`^ark_test_phase_[0-9a-f]{24}$`).MatchString(cfg.DBName) || !cfg.ParseTime || cfg.Loc != time.UTC {
		return errPhaseHarness
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	ip := net.ParseIP(host)
	if err != nil || port != "3306" || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errPhaseHarness
	}
	cfg = cfg.Clone()
	cfg.Logger = log.New(io.Discard, "", 0)
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	open := func() (*gorm.DB, *sql.DB, error) {
		connector, e := mysqlDriver.NewConnector(cfg.Clone())
		if e != nil {
			return nil, nil, e
		}
		pool := sql.OpenDB(connector)
		pool.SetMaxOpenConns(8)
		pool.SetMaxIdleConns(2)
		db, e := gorm.Open(gormmysql.New(gormmysql.Config{Conn: pool, DSNConfig: cfg.Clone()}), &gorm.Config{Logger: logger.Discard})
		if e != nil {
			_ = pool.Close()
		}
		return db, pool, e
	}
	h.db, h.pool, err = open()
	if err != nil {
		return err
	}
	h.verify, h.verifyPool, err = open()
	if err != nil {
		return err
	}
	if err = h.verify.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&h.schema).Error; err != nil || (h.schema != "0133_video_query_recovery_progress" && h.schema != "0135_video_query_settlement_shape") {
		return errPhaseHarness
	}
	if h.schema == "0133_video_query_recovery_progress" && (h.e.role != "N" || !strings.HasPrefix(h.e.caseID, "F01_")) {
		return errPhaseHarness
	}
	h.db, err = ledger.EnableActivityTracking(h.db)
	if err != nil {
		return err
	}
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	if err != nil {
		return err
	}
	h.s, err = NewService(h.db, kr, []byte(strings.Repeat("h", 32)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	h.s.Gate = allowGate{}
	if err = h.loadSeed(); err != nil {
		return err
	}
	inbox := h.seed.Profile != "legacy" && !strings.HasSuffix(h.e.caseID, "_legacy")
	if err = h.s.ConfigureResponseInbox(cfg.FormatDSN(), inbox); err != nil {
		return err
	}
	h.s.MutationResponseInboxEnabled = inbox
	if h.producer, err = phaseConfigureProducer(h.s, h.e.gate); err != nil {
		return err
	}
	// Wrap only test connections, preserving the real transaction and COMMIT.
	wrapper := &phaseCommitPool{DB: h.pool, h: h}
	h.db.ConnPool = wrapper
	h.db.Statement.ConnPool = wrapper
	rpool, e := h.s.responseDB.DB()
	if e != nil {
		return e
	}
	rw := &phaseCommitPool{DB: rpool, h: h}
	h.s.responseDB.ConnPool = rw
	h.s.responseDB.Statement.ConnPool = rw
	h.transport = &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 10 * time.Second}
	h.s.Client = &http.Client{Transport: roundTripFunc(h.roundTrip), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err = h.db.Callback().Query().After("gorm:query").Register("phase_credential_count", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "video_credential_snapshots" {
			h.mu.Lock()
			h.http.CredentialReads++
			h.mu.Unlock()
		}
	}); err != nil {
		return err
	}
	gin.SetMode(gin.TestMode)
	h.router = gin.New()
	h.s.RegisterRoutes(h.router.Group(""), &task.Service{})
	return nil
}

func (h *phaseHarness) close() error {
	h.mu.Lock()
	cycle, b := h.active, h.barrier
	if cycle != nil {
		cycle.cancel()
	}
	if b != nil && !b.released {
		b.released = true
		close(b.release)
	}
	h.mu.Unlock()
	if cycle != nil {
		select {
		case <-cycle.done:
		case <-time.After(7 * time.Second):
			return errPhaseHarness
		}
		if cycle.err != nil {
			return errPhaseHarness
		}
	}
	if h.transport != nil {
		h.transport.CloseIdleConnections()
	}
	var errs []error
	if h.s != nil {
		errs = append(errs, h.s.CloseResponseInbox())
	}
	if h.pool != nil {
		errs = append(errs, h.pool.Close())
		h.pool = nil
	}
	if h.verifyPool != nil {
		errs = append(errs, h.verifyPool.Close())
		h.verifyPool = nil
	}
	for _, e := range errs {
		if e != nil {
			return errPhaseHarness
		}
	}
	h.mu.Lock()
	failed := h.fatal
	h.mu.Unlock()
	if failed {
		return errPhaseHarness
	}
	return nil
}
func phaseHash(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func phaseWrite(path string, value any, replace bool) (string, error) {
	if st, e := os.Lstat(path); e == nil {
		if !replace || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return "", errPhaseHarness
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	tmp := path + ".tmp"
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return "", e
	}
	if e = os.Rename(tmp, path); e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (h *phaseHarness) emit(id, event string, data map[string]any, proof *phaseSnapshot) error {
	h.emitMu.Lock()
	defer h.emitMu.Unlock()
	h.sequence++
	leaf := fmt.Sprintf("%s-%04d-%s.json", h.e.instance, h.sequence, event)
	artifact := map[string]any{"protocol": phaseProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "cmd_id": id, "event": event, "source_role": h.e.role, "compiled_source": h.e.source, "data": data, "snapshot": proof}
	sha, e := phaseWrite(filepath.Join(h.e.dir, leaf), artifact, false)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(map[string]any{"protocol": phaseProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "cmd_id": id, "event": event, "source_role": h.e.role, "compiled_source": h.e.source, "artifact": leaf, "artifact_sha256": sha, "data": data})
	if e != nil {
		return e
	}
	_, e = fmt.Fprintf(os.Stdout, "%s%s\n", phaseEventPrefix, raw)
	return e
}

func phaseErrorClasses(err error) []string {
	if err == nil {
		return []string{}
	}
	remaining := 64
	out := []string{}
	var visit func(error)
	visit = func(e error) {
		remaining--
		if e == nil || remaining < 0 {
			out = append(out, "invalid_error_tree")
			return
		}
		switch e {
		case context.Canceled:
			out = append(out, "context_cancelled")
			return
		case context.DeadlineExceeded:
			out = append(out, "context_deadline")
			return
		case videoruntime.ErrUnsupported, invocation.ErrVideoSchemaUnsupported:
			out = append(out, "unsupported_contract")
			return
		case ErrConflict:
			out = append(out, "conflict")
			return
		case ErrState:
			out = append(out, "invalid_state")
			return
		}
		if me, ok := e.(*mysqlDriver.MySQLError); ok && me != nil {
			out = append(out, fmt.Sprintf("mysql_%d", me.Number))
			return
		}
		t := reflect.TypeOf(e)
		if t != nil && t.Kind() == reflect.Pointer && t.Elem().PkgPath() == "github.com/laiai/laiai-next/core/internal/video" && t.Elem().Name() == "persistedRecoveryErrors" {
			if group, ok := e.(interface{ Unwrap() []error }); ok {
				for _, child := range group.Unwrap() {
					visit(child)
				}
				return
			}
		}
		out = append(out, "unknown")
	}
	visit(err)
	return out
}
func (h *phaseHarness) roundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "ark.cn-beijing.volces.com" || req.URL.User != nil || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer "+h.e.token || !strings.HasPrefix(req.URL.Path, "/api/v3/contents/generations/tasks") {
		return nil, errPhaseHarness
	}
	var body []byte
	var err error
	if req.Body != nil {
		body, err = io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
		_ = req.Body.Close()
		if err != nil || len(body) > 1<<20 {
			return nil, errPhaseHarness
		}
	}
	h.mu.Lock()
	switch req.Method {
	case "POST":
		h.http.POST++
	case "GET":
		h.http.GET++
	case "DELETE":
		h.http.DELETE++
	default:
		h.http.Other++
	}
	h.mu.Unlock()
	u, err := url.Parse(h.e.mock + req.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	copy := req.Clone(req.Context())
	copy.URL = u
	copy.Host = u.Host
	copy.Body = io.NopCloser(bytes.NewReader(body))
	copy.ContentLength = int64(len(body))
	copy.Header = make(http.Header)
	copy.Header.Set("Content-Type", "application/json")
	copy.Header.Set("Authorization", "Bearer "+h.e.token)
	copy.Header.Set("X-Phase-Handoff-Run", h.e.run)
	copy.Header.Set("X-Phase-Handoff-Case", h.e.caseID)
	copy.Header.Set("X-Phase-Handoff-Instance", h.e.instance)
	sum := sha256.Sum256(body)
	if req.Method == http.MethodPost {
		var payload struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return nil, errPhaseHarness
		}
		matched := ""
		for handle, item := range h.seed.Tasks {
			for _, content := range payload.Content {
				if content.Type == "text" && content.Text == "Synthetic phase "+handle {
					if matched != "" {
						return nil, errPhaseHarness
					}
					matched = handle
					actual := hex.EncodeToString(sum[:])
					if item.ProviderBodySHA != "" && item.ProviderBodySHA != actual {
						return nil, errPhaseHarness
					}
					item.ProviderBodySHA = actual
					h.seed.Tasks[handle] = item
				}
			}
		}
		if matched == "" {
			return nil, errPhaseHarness
		}
		if err = h.saveSeed(); err != nil {
			return nil, err
		}
		copy.Header.Set("X-Phase-Handoff-Task", matched)
	}
	copy.Header.Set("X-Phase-Handoff-Body-SHA256", hex.EncodeToString(sum[:]))
	return h.transport.RoundTrip(copy)
}
