package video

// This common harness is overlaid, byte-for-byte, on two real git archives.
// It deliberately uses only APIs present at the fixed N-1. Production code is
// never patched by the runner. The a2c canonical-row/financial helpers below
// are read-only test utilities already present in both archives; the frozen
// A2c protocol, entry point and files are not changed or invoked.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const executionHandoffProtocol = "execution-clock-handoff-v1"
const executionHandoffOldSource = "9a6fd9426e33b2e1c269b6cb77a170e8890073f2"

// Set by the audited build command, then matched to the source manifest and
// binary hash by the external verifier. No default invents a new-source SHA.
var executionHandoffCompiledSource string
var errExecutionHandoff = errors.New("isolated execution handoff failed")

type executionHandoffCommand struct {
	Protocol      string `json:"protocol"`
	Run           string `json:"run_id"`
	Case          string `json:"case_id"`
	Instance      string `json:"instance"`
	ID            uint64 `json:"cmd_id"`
	Command       string `json:"command"`
	Barrier       string `json:"barrier,omitempty"`
	Owner         uint64 `json:"owner_cmd_id,omitempty"`
	ClockOffsetMS *int64 `json:"clock_offset_ms,omitempty"`
	Fault         string `json:"fault,omitempty"`
}

type executionHandoffSettings struct{ run, caseID, instance, role, source, dir, mock, upstream, token string }
type executionHandoffCycle struct {
	id      uint64
	barrier string
	reached bool
	resume  chan struct{}
	cancel  context.CancelFunc
}
type executionHandoffHarness struct {
	e                executionHandoffSettings
	db, verify       *gorm.DB
	pool, verifyPool *sql.DB
	s                *Service
	seed             a2cSeed
	ctx              context.Context
	cancel           context.CancelFunc
	offset           atomic.Int64
	mu               sync.Mutex
	active           *executionHandoffCycle
	wg               sync.WaitGroup
	outMu            sync.Mutex
	seq, commands    uint64
	transport        *http.Transport
	failed           atomic.Bool
}

func executionHandoffCase(id string) bool {
	switch id {
	case "N01", "N02", "N03-create", "N03-action", "N04-schema-new", "N04-schema-old", "N04-runtime-new", "N04-runtime-old":
		return true
	default:
		return false
	}
}

func decodeExecutionHandoffCommand(raw []byte) (executionHandoffCommand, error) {
	var out executionHandoffCommand
	if len(raw) == 0 || len(raw) > 4096 {
		return out, errExecutionHandoff
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return out, errExecutionHandoff
	}
	seen := map[string]bool{}
	for d.More() {
		k, err := d.Token()
		key, ok := k.(string)
		if err != nil || !ok || seen[key] {
			return out, errExecutionHandoff
		}
		seen[key] = true
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return out, errExecutionHandoff
		}
	}
	if _, err = d.Token(); err != nil {
		return out, errExecutionHandoff
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return out, errExecutionHandoff
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || out.ID == 0 || out.Protocol != executionHandoffProtocol {
		return out, errExecutionHandoff
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(out.Run) || !executionHandoffCase(out.Case) || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`).MatchString(out.Instance) {
		return out, errExecutionHandoff
	}
	switch out.Command {
	case "seed", "action", "inspect", "read_action", "purge", "stop":
		if out.Barrier != "" || out.Owner != 0 || out.ClockOffsetMS != nil || out.Fault != "" {
			return out, errExecutionHandoff
		}
	case "run_once":
		if out.Barrier != "" && out.Barrier != "receipt_committed" {
			return out, errExecutionHandoff
		}
		if out.Owner != 0 || out.ClockOffsetMS != nil || out.Fault != "" {
			return out, errExecutionHandoff
		}
	case "cancel_active":
		if out.Owner == 0 || out.Barrier != "" || out.ClockOffsetMS != nil || out.Fault != "" {
			return out, errExecutionHandoff
		}
	case "clock":
		if out.ClockOffsetMS == nil || *out.ClockOffsetMS < -int64(24*time.Hour/time.Millisecond) || *out.ClockOffsetMS > int64(24*time.Hour/time.Millisecond) || out.Owner != 0 || out.Barrier != "" || out.Fault != "" {
			return out, errExecutionHandoff
		}
	case "unknown":
		if (out.Fault != "schema" && out.Fault != "runtime") || out.Owner != 0 || out.Barrier != "" || out.ClockOffsetMS != nil {
			return out, errExecutionHandoff
		}
	default:
		return out, errExecutionHandoff
	}
	return out, nil
}

func executionHandoffEnvironment() (executionHandoffSettings, error) {
	e := executionHandoffSettings{run: os.Getenv("B1B_RUN_ID"), caseID: os.Getenv("B1B_CASE_ID"), instance: os.Getenv("B1B_INSTANCE"), role: os.Getenv("B1B_ROLE"), source: os.Getenv("B1B_SOURCE_SHA"), dir: os.Getenv("B1B_CASE_DIR"), mock: os.Getenv("B1B_MOCK_ORIGIN"), upstream: os.Getenv("B1B_UPSTREAM_ID"), token: os.Getenv("B1B_PROVIDER_TOKEN")}
	known := map[string]bool{}
	for _, k := range strings.Fields("B1B_PROTOCOL B1B_RUN_ID B1B_CASE_ID B1B_INSTANCE B1B_ROLE B1B_SOURCE_SHA B1B_CASE_DIR B1B_MOCK_ORIGIN B1B_UPSTREAM_ID B1B_PROVIDER_TOKEN") {
		known[k] = true
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "B1B_") && !known[k] {
			return e, errExecutionHandoff
		}
	}
	if os.Getenv("B1B_PROTOCOL") != executionHandoffProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.run) || !executionHandoffCase(e.caseID) || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`).MatchString(e.instance) || (e.role != "old" && e.role != "new") || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(e.source) || e.source != executionHandoffCompiledSource || e.role == "old" && e.source != executionHandoffOldSource || e.role == "new" && e.source == executionHandoffOldSource {
		return e, errExecutionHandoff
	}
	u, err := url.Parse(e.mock)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return e, errExecutionHandoff
	}
	if !filepath.IsAbs(e.dir) || !regexp.MustCompile(`^b1b-[a-zA-Z0-9_-]{1,100}$`).MatchString(e.upstream) || len(e.token) < 24 || len(e.token) > 128 {
		return e, errExecutionHandoff
	}
	info, err := os.Lstat(e.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return e, errExecutionHandoff
	}
	return e, nil
}

func TestMySQLExecutionInputBinaryHandoffProcess(t *testing.T) {
	if os.Getenv("B1B_PROTOCOL") == "" {
		t.Skip("dedicated fixed-source binary handoff runner required")
	}
	e, err := executionHandoffEnvironment()
	if err != nil {
		t.Fatal(errExecutionHandoff)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &executionHandoffHarness{e: e, ctx: ctx, cancel: cancel}
	defer func() {
		if err := h.close(); err != nil {
			t.Error("handoff drain failed")
		} else {
			h.emit(0, "drained", map[string]any{"active_work": false, "pools_closed": true})
		}
	}()
	if err = h.initialize(); err != nil {
		t.Fatal("handoff initialization failed")
	}
	h.emit(0, "ready", map[string]any{"compiled_source": executionHandoffCompiledSource, "old_source": executionHandoffOldSource, "clock": "wall", "create_producer": h.s.ResponseInboxEnabled, "mutation_producer": h.s.MutationResponseInboxEnabled})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4097)
	stopped := false
	for scanner.Scan() {
		c, e := decodeExecutionHandoffCommand(scanner.Bytes())
		if e != nil || c.Run != h.e.run || c.Case != h.e.caseID || c.Instance != h.e.instance || c.ID != h.commands+1 {
			t.Fatal("invalid handoff command")
		}
		h.commands = c.ID
		stop, e := h.command(c)
		if e != nil {
			h.emit(c.ID, "failed", map[string]any{"code": "command_failed"})
			t.Fatal("handoff command failed")
		}
		if stop {
			stopped = true
			break
		}
	}
	if !stopped || scanner.Err() != nil || h.failed.Load() {
		t.Fatal("handoff did not complete controlled drain")
	}
}

func (h *executionHandoffHarness) initialize() error {
	cfg, err := config.Load()
	if err != nil || cfg.Keyring == nil || len(cfg.VideoHMACSecret) < 32 || len(cfg.ReplicaDSNs) != 0 {
		return errExecutionHandoff
	}
	my, err := mysqlDriver.ParseDSN(cfg.DSN)
	v := sha256.Sum256([]byte(h.e.caseID))
	schema := "ark_test_exec_" + h.e.run + "_" + hex.EncodeToString(v[:])[:12]
	if err != nil || my.Net != "tcp" || my.DBName != schema || !my.ParseTime || my.Loc != time.UTC {
		return errExecutionHandoff
	}
	host, port, err := net.SplitHostPort(my.Addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errExecutionHandoff
	}
	h.db, err = gorm.Open(gormmysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return errExecutionHandoff
	}
	h.pool, err = h.db.DB()
	if err != nil {
		return err
	}
	h.pool.SetMaxOpenConns(8)
	h.pool.SetMaxIdleConns(4)
	h.verify, err = gorm.Open(gormmysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	h.verifyPool, err = h.verify.DB()
	if err != nil {
		return err
	}
	h.verifyPool.SetMaxOpenConns(2)
	h.verifyPool.SetMaxIdleConns(1)
	h.db, err = ledger.EnableActivityTracking(h.db)
	if err != nil {
		return err
	}
	h.s, err = NewService(h.db, cfg.Keyring, cfg.VideoHMACSecret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	h.s.Now = h.now
	if err = h.s.ConfigureResponseInbox(cfg.DSN, cfg.VideoResponseInboxEnabled); err != nil {
		return err
	}
	h.s.MutationResponseInboxEnabled = cfg.VideoMutationResponseInboxEnabled
	if info, e := os.Lstat(filepath.Join(h.e.dir, "seed.json")); e == nil {
		if !info.Mode().IsRegular() || info.Size() > 65536 {
			return errExecutionHandoff
		}
		raw, readErr := a2cReadFile(h.e.dir, "seed.json", 65536)
		if readErr != nil || json.Unmarshal(raw, &h.seed) != nil || h.seed.Protocol != executionHandoffProtocol || h.seed.RunID != h.e.run || h.seed.CaseID != h.e.caseID || h.seed.OrgID == 0 || h.seed.TaskID == "" || h.seed.UpstreamID != h.e.upstream {
			return errExecutionHandoff
		}
	} else if !os.IsNotExist(e) {
		return errExecutionHandoff
	}
	rp, err := h.s.responseDB.DB()
	if err != nil {
		return err
	}
	wrapper := &executionHandoffPool{DB: rp, h: h}
	h.s.responseDB.ConnPool = wrapper
	h.s.responseDB.Statement.ConnPool = wrapper
	h.installHTTP()
	return nil
}

func (h *executionHandoffHarness) now() time.Time {
	return time.Now().UTC().Add(time.Duration(h.offset.Load()) * time.Millisecond).Truncate(time.Millisecond)
}
func (h *executionHandoffHarness) dbNow() (time.Time, error) {
	var now time.Time
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	err := h.verify.WithContext(ctx).Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error
	return now.UTC().Truncate(time.Millisecond), err
}
func (h *executionHandoffHarness) emit(id uint64, event string, data any) {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	h.seq++
	raw, err := json.Marshal(map[string]any{"protocol": executionHandoffProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "role": h.e.role, "source_sha": h.e.source, "pid": os.Getpid(), "seq": h.seq, "cmd_id": id, "event": event, "data": data})
	if err != nil {
		h.failed.Store(true)
		return
	}
	fmt.Fprintf(os.Stdout, "B1B_EVENT %s\n", raw)
}
func (h *executionHandoffHarness) close() error {
	h.cancel()
	h.mu.Lock()
	if h.active != nil {
		h.active.cancel()
		select {
		case <-h.active.resume:
		default:
			close(h.active.resume)
		}
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		// Do not close a pool ahead of an unconfirmed response save. The
		// controller records a failed drain and owns bounded process cleanup.
		return errExecutionHandoff
	}
	if h.transport != nil {
		h.transport.CloseIdleConnections()
	}
	var closeErr error
	if h.s != nil {
		closeErr = errors.Join(closeErr, h.s.CloseResponseInbox())
	}
	if h.verifyPool != nil {
		closeErr = errors.Join(closeErr, h.verifyPool.Close())
	}
	if h.pool != nil {
		closeErr = errors.Join(closeErr, h.pool.Close())
	}
	if closeErr != nil {
		return errExecutionHandoff
	}
	return nil
}

func (h *executionHandoffHarness) installHTTP() {
	mock, _ := url.Parse(h.e.mock)
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	h.transport = &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != mock.Host {
			return nil, errExecutionHandoff
		}
		return dialer.DialContext(ctx, network, address)
	}}
	h.s.Client = &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: a2cRoundTrip(func(r *http.Request) (*http.Response, error) {
		base := "/api/v3/contents/generations/tasks"
		pathOK := r.Method == http.MethodPost && r.URL.Path == base || (r.Method == http.MethodGet || r.Method == http.MethodDelete) && r.URL.Path == base+"/"+h.e.upstream
		if r.URL.Scheme != "https" || r.URL.Host != "ark.cn-beijing.volces.com" || r.URL.User != nil || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.URL.RawPath != "" || !pathOK || r.Header.Get("Authorization") != "Bearer "+h.e.token {
			return nil, errExecutionHandoff
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = mock.Scheme
		u.Host = mock.Host
		clone.URL = &u
		clone.Host = mock.Host
		clone.Header.Set("X-B1B-Run", h.e.run)
		clone.Header.Set("X-B1B-Case", h.e.caseID)
		clone.Header.Set("X-B1B-Instance", h.e.instance)
		return h.transport.RoundTrip(clone)
	})}
}

type executionHandoffPool struct {
	*sql.DB
	h *executionHandoffHarness
}

func (p *executionHandoffPool) BeginTx(ctx context.Context, o *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &executionHandoffTx{Tx: tx, h: p.h}, nil
}
func (p *executionHandoffPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

type executionHandoffTx struct {
	*sql.Tx
	h *executionHandoffHarness
}

func (tx *executionHandoffTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil {
		tx.h.afterResponseCommit()
	}
	return err
}

func (h *executionHandoffHarness) afterResponseCommit() {
	h.mu.Lock()
	cycle := h.active
	if cycle == nil || cycle.barrier != "receipt_committed" || cycle.reached {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	proof, err := h.inspect()
	if err != nil {
		h.failed.Store(true)
		cycle.cancel()
		return
	}
	if proof["attempt_count"] != int64(1) || proof["receipt_count"] != int64(1) || proof["application_state"] != "pending" {
		return
	}
	input, ok := proof["input"].(map[string]any)
	if !ok {
		h.failed.Store(true)
		cycle.cancel()
		return
	}
	expires, expiryErr := a2cParseTime(input["expires_at"].(string))
	observed, clockErr := a2cParseTime(proof["database_time"].(string))
	if expiryErr != nil || clockErr != nil || !expires.After(observed) {
		// A slow setup is a failed precondition, never permission to extend
		// an already encrypted expiry or shorten the real database lease.
		h.failed.Store(true)
		cycle.cancel()
		return
	}
	h.mu.Lock()
	if h.active != cycle || cycle.reached {
		h.mu.Unlock()
		return
	}
	cycle.reached = true
	h.mu.Unlock()
	h.emit(cycle.id, "receipt_committed", proof)
	select {
	case <-cycle.resume:
	case <-h.ctx.Done():
	}
}

func (h *executionHandoffHarness) command(c executionHandoffCommand) (bool, error) {
	h.mu.Lock()
	active := h.active
	if c.Command == "cancel_active" {
		if active == nil || active.id != c.Owner || !active.reached {
			h.mu.Unlock()
			return false, errExecutionHandoff
		}
		active.cancel()
		select {
		case <-active.resume:
		default:
			close(active.resume)
		}
		h.mu.Unlock()
		h.emit(c.ID, "cancel_requested", map[string]any{"owner_cmd_id": c.Owner})
		return false, nil
	}
	h.mu.Unlock()
	if active != nil {
		return false, errExecutionHandoff
	}
	if c.Command != "seed" && c.Command != "clock" && c.Command != "stop" && h.seed.TaskID == "" {
		return false, errExecutionHandoff
	}
	switch c.Command {
	case "seed":
		if err := h.seedTask(); err != nil {
			return false, err
		}
		p, err := h.inspect()
		if err != nil {
			return false, err
		}
		h.emit(c.ID, "seeded", p)
	case "action":
		if h.seed.MutationKind != "create_task" {
			return false, errExecutionHandoff
		}
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		defer cancel()
		op, replay, err := h.s.Cancel(ctx, h.seed.OrgID, h.seed.TaskID, "b1b-cancel-request", "b1b-cancel")
		if err != nil || replay || op.Status != "pending" {
			return false, errExecutionHandoff
		}
		h.seed.OperationID = op.ID
		h.seed.MutationKind = "cancel_task"
		if err = h.saveSeed(); err != nil {
			return false, err
		}
		p, err := h.inspect()
		if err != nil {
			return false, err
		}
		h.emit(c.ID, "action_pending", p)
	case "clock":
		if h.e.role != "old" {
			return false, errExecutionHandoff
		}
		h.offset.Store(*c.ClockOffsetMS)
		dbTime, err := h.dbNow()
		if err != nil {
			return false, err
		}
		h.emit(c.ID, "clock_set", map[string]any{"offset_ms": *c.ClockOffsetMS, "host_time": a2cTime(h.now()), "database_time": a2cTime(dbTime)})
	case "inspect":
		p, err := h.inspect()
		if err != nil {
			return false, err
		}
		h.emit(c.ID, "inspected", p)
	case "run_once":
		ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
		cycle := &executionHandoffCycle{id: c.ID, barrier: c.Barrier, resume: make(chan struct{}), cancel: cancel}
		h.mu.Lock()
		h.active = cycle
		h.mu.Unlock()
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer cancel()
			err := h.s.ProcessOnce(ctx)
			p, inspectErr := h.inspect()
			code := "none"
			if err != nil {
				code = "operation_error"
				if errors.Is(err, context.Canceled) {
					code = "cancelled"
				} else if errors.Is(err, context.DeadlineExceeded) {
					code = "deadline"
				}
			}
			if inspectErr != nil {
				h.failed.Store(true)
			}
			h.mu.Lock()
			h.active = nil
			h.mu.Unlock()
			h.emit(c.ID, "cycle_completed", map[string]any{"error_class": code, "proof": p, "barrier_reached": cycle.reached})
		}()
	case "read_action":
		if h.e.caseID != "N03-action" || h.e.role != "old" || h.offset.Load() != 0 || h.seed.MutationKind != "cancel_task" {
			return false, errExecutionHandoff
		}
		var op Operation
		if err := h.verify.Where("org_id = ? AND id = ?", h.seed.OrgID, h.seed.OperationID).Take(&op).Error; err != nil {
			return false, err
		}
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		defer cancel()
		body, err := h.s.Evidence.ReadForActionExecution(ctx, h.seed.OrgID, op.PayloadRef, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "binary-handoff", ReasonCode: "compatibility", OperationID: op.ID})
		if err != nil {
			return false, errExecutionHandoff
		}
		sum := a2cHash(body)
		size := len(body)
		clear(body)
		if size == 0 {
			return false, errExecutionHandoff
		}
		h.emit(c.ID, "action_read", map[string]any{"payload_ref": op.PayloadRef, "body_sha256": sum, "bytes": size})
	case "purge":
		if h.e.role != "new" {
			return false, errExecutionHandoff
		}
		before, err := h.inspect()
		if err != nil {
			return false, err
		}
		input := before["input"].(map[string]any)
		expires, err := a2cParseTime(input["expires_at"].(string))
		if err != nil {
			return false, err
		}
		now, err := h.dbNow()
		if err != nil || now.Before(expires) {
			return false, errExecutionHandoff
		}
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		defer cancel()
		n, err := h.s.Evidence.PurgeExpired(ctx, 1000)
		if err != nil {
			return false, err
		}
		after, err := h.inspect()
		if err != nil {
			return false, err
		}
		current := after["input"].(map[string]any)
		if current["identity_sha256"] != input["identity_sha256"] || current["capture_status"] != "expired" || current["key_present"] != false || current["nonce_present"] != false || current["staging_count"] != int64(0) || after["receipt_sha256"] != before["receipt_sha256"] || after["attempt_sha256"] != before["attempt_sha256"] || after["financial_sha256"] != before["financial_sha256"] {
			return false, errExecutionHandoff
		}
		h.emit(c.ID, "purged", map[string]any{"newly_expired": n, "before": before, "after": after})
	case "unknown":
		if !strings.HasPrefix(h.e.caseID, "N04-") || !strings.Contains(h.e.caseID, "-"+c.Fault+"-") {
			return false, errExecutionHandoff
		}
		before, err := h.inspect()
		if err != nil || before["receipt_count"] != int64(1) || before["application_state"] != "pending" {
			return false, errExecutionHandoff
		}
		err = h.db.Transaction(func(tx *gorm.DB) error {
			var q *gorm.DB
			if c.Fault == "schema" {
				p := before["input"].(map[string]any)
				q = tx.Table("invocation_payloads").Where("org_id = ? AND payload_id = ? AND schema_version = 2", h.seed.OrgID, p["payload_ref"]).Update("schema_version", 99)
			} else {
				q = tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND runtime_version = ?", h.seed.OrgID, h.seed.TaskID, videoruntime.LegacyV1().RuntimeVersion).Update("runtime_version", "future-runtime")
			}
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return errExecutionHandoff
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		after, err := h.inspect()
		if err != nil || after["financial_sha256"] != before["financial_sha256"] || after["receipt_sha256"] != before["receipt_sha256"] || after["attempt_sha256"] != before["attempt_sha256"] {
			return false, errExecutionHandoff
		}
		h.emit(c.ID, "unknown_injected", map[string]any{"kind": c.Fault, "before": before, "after": after})
	case "stop":
		h.emit(c.ID, "stop_ack", map[string]any{"active_work": false})
		return true, nil
	default:
		return false, errExecutionHandoff
	}
	return false, nil
}

func (h *executionHandoffHarness) saveSeed() error {
	raw, err := json.Marshal(h.seed)
	if err != nil {
		return err
	}
	path := filepath.Join(h.e.dir, "seed.json")
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errExecutionHandoff
	}
	return os.WriteFile(path, raw, 0600)
}

func (h *executionHandoffHarness) seedTask() error {
	if h.seed.TaskID != "" {
		return errExecutionHandoff
	}
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	defer cancel()
	db := h.db.WithContext(ctx)
	var count int64
	if err := db.Model(&task.Task{}).Count(&count).Error; err != nil || count != 0 {
		return errExecutionHandoff
	}
	now, err := h.dbNow()
	if err != nil {
		return err
	}
	org := identity.Organization{Slug: "b1b-" + h.e.run, Name: "Synthetic execution handoff", Group: "default", Status: identity.StatusActive}
	if err = db.Create(&org).Error; err != nil {
		return err
	}
	var key *identity.APIKey
	if err = db.Transaction(func(tx *gorm.DB) error {
		_, issued, e := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "execution-handoff", Scopes: "video:*"})
		key = issued
		return e
	}); err != nil {
		return err
	}
	if err = db.Transaction(func(tx *gorm.DB) error {
		account, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "b1b-credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)})
		return e
	}); err != nil {
		return err
	}
	ciphertext, err := h.s.Keyring.Encrypt(h.e.token)
	if err != nil {
		return err
	}
	channel := relay.Channel{Name: "Synthetic execution handoff", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: ciphertext, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	if err = db.Create(&channel).Error; err != nil {
		return err
	}
	scope := ProviderScope{ScopeKey: "b1b-" + h.e.run, AccountRef: "synthetic-handoff", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "handoff-v1", FXObservedAt: now}
	if err = db.Create(&scope).Error; err != nil {
		return err
	}
	limit := decimal.NewFromInt(1000000)
	if err = db.Create(&videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "handoff-v1", Enabled: true}).Error; err != nil {
		return err
	}
	capability := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "handoff-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	capJSON, err := json.Marshal(capability)
	if err != nil {
		return err
	}
	if err = db.Create(&ModelConfig{PublicModel: ark.Model20, Version: "handoff-v1", ProviderScopeID: scope.ID, ChannelID: channel.ID, Enabled: true, CapabilityJSON: string(capJSON), PricingVersion: "handoff-v1", NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}).Error; err != nil {
		return err
	}
	if err = db.Create(&videobilling.PricingRule{RuleID: "handoff-v1", Version: "handoff-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "handoff-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-72 * time.Hour), Enabled: true}).Error; err != nil {
		return err
	}
	if err = db.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: false, RetentionDays: 30, UpdatedAt: now}).Error; err != nil {
		return err
	}
	view, replay, err := h.s.Create(ctx, key, "b1b-create-request", "b1b-create", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "synthetic binary handoff"}}})
	if err != nil || replay {
		return errExecutionHandoff
	}
	var snapshot videobilling.Snapshot
	if err = db.Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&snapshot).Error; err != nil {
		return err
	}
	var op Operation
	if err = db.Where("org_id = ? AND resource_id = ? AND kind = ?", org.ID, view.ID, "create_task").Take(&op).Error; err != nil {
		return err
	}
	if snapshot.EstimatedTokens != 108000 || snapshot.HoldUSD.StringFixed(8) != "0.86400000" || snapshot.SupplierReservedCNY.StringFixed(8) != "4.96800000" || op.Status != "pending" {
		return errExecutionHandoff
	}
	h.seed = a2cSeed{Protocol: executionHandoffProtocol, RunID: h.e.run, CaseID: h.e.caseID, OrgID: org.ID, KeyID: key.ID, TaskID: view.ID, OperationID: op.ID, ScopeID: scope.ID, ChannelID: channel.ID, HoldID: snapshot.HoldID, UpstreamID: h.e.upstream, CreationRole: h.e.role, History: false, MutationKind: "create_task", Oracle: map[string]string{"credit_usd": "100.00000000", "retail_usd_per_1m": "8", "supplier_cny_per_1m": "46", "fx_usd_per_cny": "0.14", "estimated_tokens": "108000", "hold_usd": "0.86400000", "supplier_reserved_cny": "4.96800000", "completion_tokens": "100", "captured_usd": "0.00080000", "released_usd": "0.86320000", "supplier_cny": "0.00460000"}}
	// Inspect the actual stored body once as an independent transport oracle.
	// This common old-format reader is not claimed as an N database-window test.
	body, err := h.s.Evidence.ReadForExecutionSchema(ctx, org.ID, op.PayloadRef, 1, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "binary-handoff", ReasonCode: "seed-oracle", OperationID: op.ID})
	if err != nil {
		return err
	}
	h.seed.Oracle["request_body_sha256"] = a2cHash(body)
	clear(body)
	return h.saveSeed()
}

func (h *executionHandoffHarness) inspect() (map[string]any, error) {
	if h.seed.OrgID == 0 || h.seed.TaskID == "" || h.seed.OperationID == "" {
		return nil, errExecutionHandoff
	}
	out := map[string]any{}
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	err := h.verify.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var st State
		var t task.Task
		var op Operation
		if err := tx.Where("org_id = ? AND task_id = ?", h.seed.OrgID, h.seed.TaskID).Take(&st).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND task_id = ?", h.seed.OrgID, h.seed.TaskID).Take(&t).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND id = ? AND resource_id = ?", h.seed.OrgID, h.seed.OperationID, h.seed.TaskID).Take(&op).Error; err != nil {
			return err
		}
		var attempts []ExecutionAttempt
		var receipts []ResponseInbox
		var apps []ReceiptApplication
		if err := tx.Where("org_id = ? AND task_id = ? AND operation_id = ?", h.seed.OrgID, h.seed.TaskID, op.ID).Order("attempt_id").Limit(3).Find(&attempts).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND task_id = ? AND operation_id = ?", h.seed.OrgID, h.seed.TaskID, op.ID).Order("receipt_id").Limit(3).Find(&receipts).Error; err != nil {
			return err
		}
		if len(attempts) > 1 || len(receipts) > 1 {
			return errExecutionHandoff
		}
		if len(receipts) == 1 {
			if err := tx.Where("org_id = ? AND task_id = ? AND receipt_id = ?", h.seed.OrgID, h.seed.TaskID, receipts[0].ReceiptID).Limit(2).Find(&apps).Error; err != nil {
				return err
			}
			if len(apps) != 1 {
				return errExecutionHandoff
			}
		}
		out["org_id"], out["key_id"], out["task_id"], out["operation_id"] = a2cUint(h.seed.OrgID), a2cUint(h.seed.KeyID), h.seed.TaskID, op.ID
		out["task_status"], out["billing_state"], out["operation_status"], out["operation_version"] = t.Status, st.BillingState, op.Status, a2cUint(op.Version)
		out["operation_attempt"], out["operation_kind"], out["lease_until"] = op.AttemptID, op.Kind, a2cNullableTime(op.LeaseUntil)
		out["query_sequence"], out["applied_query_sequence"], out["next_poll_at"] = a2cUint(st.QuerySequence), a2cUint(st.AppliedQuerySequence), a2cTime(st.NextPollAt)
		out["runtime_version"], out["deleted_at"], out["upstream_id"] = st.RuntimeVersion, a2cNullableTime(st.DeletedAt), t.UpstreamTaskID
		out["attempt_count"], out["receipt_count"] = int64(len(attempts)), int64(len(receipts))
		out["application_state"] = ""
		out["application_error"] = ""
		out["applied_at"] = nil
		if len(apps) == 1 {
			out["application_state"], out["application_error"], out["applied_at"] = apps[0].State, apps[0].ErrorCode, a2cNullableTime(apps[0].AppliedAt)
		}
		if len(receipts) == 1 {
			out["receipt_id"], out["received_outcome"] = receipts[0].ReceiptID, receipts[0].Outcome
		}
		for name, value := range map[string]any{"attempt_sha256": attempts, "receipt_sha256": receipts} {
			b, err := json.Marshal(value)
			if err != nil {
				return err
			}
			out[name] = a2cHash(b)
		}
		ref := op.PayloadRef
		if ref == "" && op.Kind == "create_task" {
			ref = st.EffectivePayloadRef
		}
		if ref == "" {
			out["input"] = map[string]any{"payload_ref": ""}
		} else {
			var meta invocation.VideoPayload
			if err := tx.Where("org_id = ? AND payload_id = ?", h.seed.OrgID, ref).Take(&meta).Error; err != nil {
				return err
			}
			var stage int64
			if err := tx.Model(&invocation.VideoPayloadStaging{}).Where("payload_id = ?", ref).Count(&stage).Error; err != nil {
				return err
			}
			identityHash, err := a2cRowsHash(tx, "SELECT payload_id,org_id,request_id,attempt_id,correlation_key,direction,purpose,schema_version,body_format,ciphertext_sha256,plaintext_bytes,redaction_version,capture_representation,created_at,expires_at FROM invocation_payloads WHERE org_id=? AND payload_id=?", h.seed.OrgID, ref)
			if err != nil {
				return err
			}
			out["input"] = map[string]any{"payload_ref": ref, "schema": meta.SchemaVersion, "identity_sha256": identityHash, "ciphertext_sha256": meta.CiphertextSHA256, "created_at": a2cTime(meta.CreatedAt), "expires_at": a2cTime(meta.ExpiresAt), "capture_status": meta.CaptureStatus, "key_present": meta.WrappedKey != "", "nonce_present": len(meta.Nonce) > 0, "staging_count": stage}
			var reads int64
			if err := tx.Model(&invocation.VideoPayloadAccess{}).Where("org_id = ? AND payload_id = ? AND phase = ? AND outcome = ?", h.seed.OrgID, ref, "finished", "succeeded").Count(&reads).Error; err != nil {
				return err
			}
			out["input_successful_reads"] = reads
		}
		var snapshot videobilling.Snapshot
		if err := tx.Where("org_id = ? AND task_id = ? AND id = ?", h.seed.OrgID, h.seed.TaskID, st.BillingSnapshotID).Take(&snapshot).Error; err != nil {
			return err
		}
		b, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		out["billing_snapshot_sha256"] = a2cHash(b)
		out["price"] = map[string]string{"retail_usd_per_1m": snapshot.RetailUSDPer1M.String(), "supplier_cny_per_1m": snapshot.SupplierCNYPer1M.String(), "fx_usd_per_cny": snapshot.FXUSDPerCNY.String(), "hold_usd": snapshot.HoldUSD.StringFixed(8), "supplier_reserved_cny": snapshot.SupplierReservedCNY.StringFixed(8), "effective_ratio": snapshot.EffectiveRatio.String(), "rule_version": snapshot.RuleVersion, "fx_version": snapshot.FXVersion}
		binding, err := a2cRowsHash(tx, "SELECT task_id,org_id,provider_scope_id,credential_snapshot_id,provider_base_url,config_version,capability_version,adapter_version,protocol_contract_version,billing_schema_version,upstream_model,billing_snapshot_id FROM video_task_states WHERE org_id=? AND task_id=?", h.seed.OrgID, h.seed.TaskID)
		if err != nil {
			return err
		}
		out["binding_sha256"] = binding
		financeHelper := &a2cHarness{seed: h.seed}
		finance, prints, err := financeHelper.financialFacts(tx)
		if err != nil {
			return err
		}
		out["finance"], out["financial_fingerprints"], out["financial_sha256"] = finance, prints, prints["financial_all"]
		var supplier struct{ Amount decimal.Decimal }
		if err := tx.Raw("SELECT COALESCE(SUM(supplier_cny),0) AS amount FROM video_billing_entries WHERE org_id=? AND task_id=?", h.seed.OrgID, h.seed.TaskID).Scan(&supplier).Error; err != nil {
			return err
		}
		var key struct{ UsedAmount decimal.Decimal }
		if err := tx.Table("api_keys").Select("used_amount").Where("org_id=? AND id=?", h.seed.OrgID, h.seed.KeyID).Take(&key).Error; err != nil {
			return err
		}
		var buckets struct{ Reserved, Consumed decimal.Decimal }
		if err := tx.Raw("SELECT COALESCE(SUM(b.reserved_amount),0) AS reserved,COALESCE(SUM(b.consumed_amount),0) AS consumed FROM video_quota_buckets b WHERE b.org_id=0 AND b.id IN (SELECT r.bucket_id FROM video_quota_reservations r WHERE r.org_id=? AND r.task_id=? AND r.supplier=1 AND r.metric=?)", h.seed.OrgID, h.seed.TaskID, videobilling.MetricCNY).Scan(&buckets).Error; err != nil {
			return err
		}
		out["actual_amounts"] = map[string]string{"supplier_cny": supplier.Amount.StringFixed(8), "key_used_usd": key.UsedAmount.StringFixed(8), "provider_reserved_cny": buckets.Reserved.StringFixed(8), "provider_consumed_cny": buckets.Consumed.StringFixed(8)}
		out["oracle"] = h.seed.Oracle
		var now time.Time
		if err := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error; err != nil {
			return err
		}
		out["database_time"] = a2cTime(now)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}
