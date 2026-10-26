package video

// The runner overlays this IDENTICAL harness onto two fixed, real source
// archives. It uses only public business entry points, real SQL commits and
// an independently owned loopback HTTP mock. No simulated version aliases,
// receipt insertion, private reducers, or lease/status repairs are allowed.
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
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/identity"
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

const (
	a2cProtocol   = "a2c-process-v1"
	a2cCaSHA      = "64650ecc9e3a672ea49d0ffaf6f417b03fe6ad89"
	a2cPSHA       = "24da28583eddfaad151b9a4a054264a8275b61bb"
	a2cTimeFormat = "2006-01-02T15:04:05.000Z"
)

var a2cInvalid = errors.New("invalid isolated handoff")

type a2cSettings struct {
	run, caseID, instance, mode, role, source, dir, clockMode, mock, upstream, token string
	initial                                                                          time.Time
	action                                                                           string
}

type a2cSeed struct {
	Protocol          string            `json:"protocol"`
	RunID             string            `json:"run_id"`
	CaseID            string            `json:"case_id"`
	Action            string            `json:"action"`
	OrgID             uint64            `json:"org_id,string"`
	KeyID             uint64            `json:"key_id,string"`
	TaskID            string            `json:"task_id"`
	OperationID       string            `json:"operation_id"`
	ScopeID           uint64            `json:"provider_scope_id,string"`
	ChannelID         uint64            `json:"channel_id,string"`
	HoldID            uint64            `json:"hold_id,string"`
	UpstreamID        string            `json:"upstream_id"`
	CreationRole      string            `json:"creation_role"`
	History           bool              `json:"history_enabled"`
	InitialTime       string            `json:"initial_time"`
	FinishedClock     string            `json:"seed_finished_clock"`
	MutationKind      string            `json:"mutation_kind"`
	Oracle            map[string]string `json:"oracle"`
	BaselineFinancial string            `json:"baseline_financial_sha256"`
}

type a2cCommand struct {
	Protocol string `json:"protocol"`
	RunID    string `json:"run_id"`
	CaseID   string `json:"case_id"`
	Instance string `json:"instance"`
	ID       uint64 `json:"cmd_id"`
	Command  string `json:"command"`
	Barrier  string `json:"barrier,omitempty"`
	Owner    uint64 `json:"owner_cmd_id,omitempty"`
	To       string `json:"to,omitempty"`
	Label    string `json:"label,omitempty"`
}

type a2cCycle struct {
	id       uint64
	barrier  string
	reached  bool
	released bool
	resume   chan struct{}
}

type a2cHarness struct {
	e                              a2cSettings
	db, verifier                   *gorm.DB
	pool, verifyPool, responsePool *sql.DB
	s                              *Service
	seed                           a2cSeed
	clock                          atomic.Int64
	ctx                            context.Context
	cancel                         context.CancelFunc
	mu                             sync.Mutex
	active                         *a2cCycle
	outMu                          sync.Mutex
	sequence                       uint64
	work                           sync.WaitGroup
	fatal                          chan string
	failOnce                       sync.Once
	transport                      *http.Transport
	commands                       uint64
}

func a2cCases() map[string]string {
	m := map[string]string{}
	for _, a := range []string{"cancel", "delete"} {
		for _, p := range []string{"L1-", "R1-", "R2-", "M1-", "M2-"} {
			id := p + a
			if p == "R1-" || p == "R2-" {
				id += "-Ca"
			}
			m[id] = a
		}
		for _, c := range []string{"Poff", "Pon"} {
			m["L2-"+a+"-"+c] = a
		}
		for _, n := range []string{"N1", "N2", "N3"} {
			for _, c := range []string{"Ca", "Poff"} {
				m[n+"-"+a+"-"+c] = a
			}
		}
		for _, n := range []string{"1", "2", "3"} {
			m["M3-"+a+"-"+n] = a
		}
	}
	for _, id := range []string{"N1-wall-cancel-Ca", "N4-cancel-502-Ca", "Q1-cancel-succeeded-first", "Q1-cancel-cancelled-first", "N3-cancel-both-flags-Ca"} {
		m[id] = "cancel"
	}
	m["N3-delete-404-Ca"] = "delete"
	m["N4-delete-EOF-Poff"] = "delete"
	return m
}

func a2cParseTime(s string) (time.Time, error) {
	t, err := time.Parse(a2cTimeFormat, s)
	if err != nil || t.Format(a2cTimeFormat) != s {
		return time.Time{}, a2cInvalid
	}
	return t, nil
}

func a2cLoadSettings() (a2cSettings, error) {
	known := map[string]bool{}
	for _, k := range strings.Fields("A2C_PROTOCOL A2C_RUN_ID A2C_CASE_ID A2C_INSTANCE A2C_MODE A2C_BUILD_ROLE A2C_SOURCE_SHA A2C_CASE_DIR A2C_CLOCK_MODE A2C_INITIAL_TIME A2C_MOCK_ORIGIN A2C_UPSTREAM_ID A2C_PROVIDER_TOKEN") {
		known[k] = true
	}
	for _, pair := range os.Environ() {
		k, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(k, "A2C_") && !known[k] {
			return a2cSettings{}, a2cInvalid
		}
	}
	e := a2cSettings{run: os.Getenv("A2C_RUN_ID"), caseID: os.Getenv("A2C_CASE_ID"), instance: os.Getenv("A2C_INSTANCE"), mode: os.Getenv("A2C_MODE"), role: os.Getenv("A2C_BUILD_ROLE"), source: os.Getenv("A2C_SOURCE_SHA"), dir: os.Getenv("A2C_CASE_DIR"), clockMode: os.Getenv("A2C_CLOCK_MODE"), mock: os.Getenv("A2C_MOCK_ORIGIN"), upstream: os.Getenv("A2C_UPSTREAM_ID"), token: os.Getenv("A2C_PROVIDER_TOKEN")}
	e.action = a2cCases()[e.caseID]
	if os.Getenv("A2C_PROTOCOL") != a2cProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.run) || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`).MatchString(e.instance) || e.action == "" || (e.mode != "seed" && e.mode != "worker" && e.mode != "inspect") {
		return e, a2cInvalid
	}
	if (e.role == "Ca" && e.source != a2cCaSHA) || ((e.role == "Poff" || e.role == "Pon" || e.role == "Pboth") && e.source != a2cPSHA) || (e.role != "Ca" && e.role != "Poff" && e.role != "Pon" && e.role != "Pboth") {
		return e, a2cInvalid
	}
	if e.role == "Pboth" && e.caseID != "N3-cancel-both-flags-Ca" {
		return e, a2cInvalid
	}
	if e.clockMode != "logical" && e.clockMode != "wall" {
		return e, a2cInvalid
	}
	if (e.clockMode == "wall") != (e.caseID == "N1-wall-cancel-Ca") {
		return e, a2cInvalid
	}
	var err error
	e.initial, err = a2cParseTime(os.Getenv("A2C_INITIAL_TIME"))
	if err != nil {
		return e, err
	}
	if !filepath.IsAbs(e.dir) || len(e.token) < 32 || len(e.token) > 256 {
		return e, a2cInvalid
	}
	st, err := os.Lstat(e.dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return e, a2cInvalid
	}
	u, err := url.Parse(e.mock)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return e, a2cInvalid
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return e, a2cInvalid
	}
	hash := sha256.Sum256([]byte(e.caseID))
	expected := "a2c_" + e.run + "_" + hex.EncodeToString(hash[:])[:12]
	if e.upstream != expected {
		return e, a2cInvalid
	}
	return e, nil
}

func TestMySQLMutationBinaryHandoffProcess(t *testing.T) {
	if os.Getenv("A2C_PROTOCOL") == "" {
		t.Skip("explicit isolated mutation binary runner required")
	}
	e, err := a2cLoadSettings()
	if err != nil {
		t.Fatal("invalid isolated handoff startup")
	}
	h := &a2cHarness{e: e, fatal: make(chan string, 1)}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.clock.Store(e.initial.UnixMilli())
	if err = h.initialize(); err != nil {
		h.emit(0, "error", map[string]any{"code": "startup_invalid"})
		h.close()
		t.Fatal("isolated handoff startup failed")
	}
	defer h.close()
	ready := map[string]any{"source_sha": e.source, "clock_mode": e.clockMode, "clock": a2cTime(h.now()), "create_inbox": e.role == "Pboth", "mutation_field_present": e.role != "Ca", "mutation_inbox": nil}
	if e.role != "Ca" {
		ready["mutation_inbox"] = e.role == "Pon" || e.role == "Pboth"
	}
	if e.mode != "seed" {
		ready["org_id"] = a2cUint(h.seed.OrgID)
		ready["task_id"] = h.seed.TaskID
		ready["operation_id"] = h.seed.OperationID
	}
	dbTime, err := h.dbTime()
	if err != nil {
		t.Fatal("isolated handoff database clock unavailable")
	}
	ready["db_time"] = dbTime
	h.emit(0, "ready", ready)
	if err = h.control(); err != nil {
		t.Fatal("isolated handoff process failed")
	}
}

func (h *a2cHarness) initialize() error {
	cfg, err := config.Load()
	if err != nil || cfg.Keyring == nil || len(cfg.VideoHMACSecret) < 32 || len(cfg.ReplicaDSNs) != 0 {
		return a2cInvalid
	}
	my, err := mysqlDriver.ParseDSN(cfg.DSN)
	hash := sha256.Sum256([]byte(h.e.caseID))
	schema := "ark_test_action_" + h.e.run + "_" + hex.EncodeToString(hash[:])[:12]
	if err != nil || my.Net != "tcp" || my.DBName != schema || !my.ParseTime {
		return a2cInvalid
	}
	host, port, err := net.SplitHostPort(my.Addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return a2cInvalid
	}
	if cfg.VideoResponseInboxEnabled != (h.e.role == "Pboth") {
		return a2cInvalid
	}
	f := reflect.ValueOf(cfg).Elem().FieldByName("VideoMutationResponseInboxEnabled")
	if h.e.role == "Ca" {
		if f.IsValid() || os.Getenv("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX") != "" {
			return a2cInvalid
		}
	} else if !f.IsValid() || f.Kind() != reflect.Bool || f.Bool() != (h.e.role == "Pon" || h.e.role == "Pboth") {
		return a2cInvalid
	}
	// The real DSN-backed dialector is retained for ConfigureResponseInbox's
	// strict primary identity check. Logs never inherit driver/SQL diagnostics.
	h.db, err = gorm.Open(gormmysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return a2cInvalid
	}
	h.pool, err = h.db.DB()
	if err != nil {
		return a2cInvalid
	}
	h.pool.SetMaxOpenConns(8)
	h.pool.SetMaxIdleConns(4)
	h.verifier, err = gorm.Open(gormmysql.Open(cfg.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return a2cInvalid
	}
	h.verifyPool, err = h.verifier.DB()
	if err != nil {
		return a2cInvalid
	}
	h.verifyPool.SetMaxOpenConns(2)
	h.verifyPool.SetMaxIdleConns(1)
	if h.e.mode != "seed" {
		raw, err := a2cReadFile(h.e.dir, "seed.json", 65536)
		if err != nil {
			return a2cInvalid
		}
		if json.Unmarshal(raw, &h.seed) != nil || h.seed.Protocol != a2cProtocol || h.seed.RunID != h.e.run || h.seed.CaseID != h.e.caseID || h.seed.Action != h.e.action || h.seed.OrgID == 0 || h.seed.KeyID == 0 || h.seed.TaskID == "" || h.seed.OperationID == "" || h.seed.UpstreamID != h.e.upstream {
			return a2cInvalid
		}
	}
	if h.e.mode == "inspect" {
		return nil
	}
	h.db, err = ledger.EnableActivityTracking(h.db)
	if err != nil {
		return a2cInvalid
	}
	h.s, err = NewService(h.db, cfg.Keyring, cfg.VideoHMACSecret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return a2cInvalid
	}
	h.s.Now = h.now
	if err = h.s.ConfigureResponseInbox(cfg.DSN, cfg.VideoResponseInboxEnabled); err != nil {
		return a2cInvalid
	}
	if h.e.role != "Ca" {
		reflect.ValueOf(h.s).Elem().FieldByName("MutationResponseInboxEnabled").SetBool(f.Bool())
	} else if reflect.ValueOf(h.s).Elem().FieldByName("MutationResponseInboxEnabled").IsValid() {
		return a2cInvalid
	}
	h.responsePool, err = h.s.responseDB.DB()
	if err != nil {
		return a2cInvalid
	}
	h.installPool(h.db, h.pool, "main")
	h.installPool(h.s.responseDB, h.responsePool, "response")
	h.installHTTP()
	return nil
}

func (h *a2cHarness) now() time.Time {
	if h.e.clockMode == "wall" {
		return time.Now().UTC().Truncate(time.Millisecond)
	}
	return time.UnixMilli(h.clock.Load()).UTC()
}
func a2cTime(t time.Time) string { return t.UTC().Format(a2cTimeFormat) }
func a2cNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return a2cTime(*t)
}
func a2cUint(n uint64) string   { return strconv.FormatUint(n, 10) }
func a2cHash(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func (h *a2cHarness) dbTime() (string, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	var now time.Time
	err := h.verifier.WithContext(ctx).Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error
	return a2cTime(now), err
}

func (h *a2cHarness) emit(cmd uint64, event string, data map[string]any) {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	h.sequence++
	raw, err := json.Marshal(map[string]any{"protocol": a2cProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "role": h.e.role, "mode": h.e.mode, "pid": os.Getpid(), "seq": h.sequence, "cmd_id": cmd, "event": event, "data": data})
	if err != nil || len(raw) > 65536 {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "A2C_EVENT %s\n", raw)
}

func (h *a2cHarness) failure(cmd uint64, code string) {
	h.failOnce.Do(func() { h.emit(cmd, "error", map[string]any{"code": code}); h.cancel(); h.fatal <- code })
}

func a2cDecodeCommand(raw []byte) (a2cCommand, error) {
	var c a2cCommand
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return c, a2cInvalid
	}
	seen := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return c, a2cInvalid
		}
		s, ok := key.(string)
		if !ok || seen[s] != nil {
			return c, a2cInvalid
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(v, []byte("null")) {
			return c, a2cInvalid
		}
		seen[s] = v
	}
	if _, err = d.Token(); err != nil {
		return c, a2cInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return c, a2cInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, a2cInvalid
	}
	common := []string{"protocol", "run_id", "case_id", "instance", "cmd_id", "command"}
	for _, k := range common {
		if seen[k] == nil {
			return c, a2cInvalid
		}
	}
	extra := map[string]bool{}
	switch c.Command {
	case "seed", "stop", "purge_expired":
	case "run_once":
		extra["barrier"] = true
	case "release_barrier":
		extra["barrier"] = true
		extra["owner_cmd_id"] = true
	case "advance_clock":
		extra["to"] = true
	case "inspect":
		extra["label"] = true
	default:
		return c, a2cInvalid
	}
	if len(seen) != len(common)+len(extra) {
		return c, a2cInvalid
	}
	for k := range extra {
		if seen[k] == nil {
			return c, a2cInvalid
		}
	}
	return c, nil
}

func (h *a2cHarness) control() error {
	lines := make(chan []byte)
	readDone := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 4096), 4097)
		for sc.Scan() {
			raw := append([]byte(nil), sc.Bytes()...)
			if len(raw) > 4096 {
				readErr <- a2cInvalid
				return
			}
			select {
			case lines <- raw:
			case <-h.ctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil {
			readErr <- a2cInvalid
		} else {
			readErr <- io.EOF
		}
	}()
	defer func() {
		h.cancel()
		_ = os.Stdin.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
		}
	}()
	for {
		select {
		case <-h.fatal:
			return a2cInvalid
		case <-readErr:
			h.failure(h.commands, "invalid_command")
			return a2cInvalid
		case raw := <-lines:
			c, err := a2cDecodeCommand(raw)
			if err != nil || c.Protocol != a2cProtocol || c.RunID != h.e.run || c.CaseID != h.e.caseID || c.Instance != h.e.instance || c.ID != h.commands+1 {
				h.failure(0, "invalid_command")
				return a2cInvalid
			}
			h.commands = c.ID
			stop, err := h.command(c)
			if err != nil {
				h.failure(c.ID, "invalid_command")
				return a2cInvalid
			}
			if stop {
				return nil
			}
		}
	}
}

func (h *a2cHarness) command(c a2cCommand) (bool, error) {
	h.mu.Lock()
	active := h.active
	h.mu.Unlock()
	if active != nil && c.Command != "release_barrier" {
		return false, a2cInvalid
	}
	switch c.Command {
	case "stop":
		h.emit(c.ID, "command_done", map[string]any{"command": c.Command})
		h.emit(c.ID, "stopped", map[string]any{"completed_commands": h.commands})
		return true, nil
	case "seed":
		if h.e.mode != "seed" || h.seed.OrgID != 0 {
			return false, a2cInvalid
		}
		if err := h.seedBusiness(c.ID); err != nil {
			h.failure(c.ID, "business_failed")
			return false, a2cInvalid
		}
	case "run_once":
		if h.e.mode != "worker" || !a2cBarrierValid(c.Barrier) {
			return false, a2cInvalid
		}
		cycle := &a2cCycle{id: c.ID, barrier: c.Barrier, resume: make(chan struct{})}
		h.mu.Lock()
		h.active = cycle
		h.mu.Unlock()
		h.emit(c.ID, "run_started", map[string]any{"barrier": c.Barrier, "clock": a2cTime(h.now())})
		h.work.Add(1)
		go func() {
			defer h.work.Done()
			if c.Barrier == "start" {
				h.reachBarrier(cycle)
			}
			ctx, cancel := context.WithTimeout(h.ctx, 60*time.Second)
			err := h.s.ProcessOnce(ctx)
			cancel()
			if err != nil {
				h.failure(c.ID, "business_failed")
				return
			}
			dbTime, err := h.dbTime()
			if err != nil {
				h.failure(c.ID, "database_unavailable")
				return
			}
			h.mu.Lock()
			h.active = nil
			h.mu.Unlock()
			h.emit(c.ID, "command_done", map[string]any{"command": "run_once", "clock": a2cTime(h.now()), "db_time": dbTime, "process_once_returned": true})
		}()
		return false, nil
	case "release_barrier":
		h.mu.Lock()
		defer h.mu.Unlock()
		cycle := h.active
		if h.e.mode != "worker" || cycle == nil || !cycle.reached || cycle.released || cycle.id != c.Owner || cycle.barrier != c.Barrier {
			return false, a2cInvalid
		}
		cycle.released = true
		close(cycle.resume)
	case "advance_clock":
		if h.e.mode == "seed" || h.e.clockMode != "logical" {
			return false, a2cInvalid
		}
		next, err := a2cParseTime(c.To)
		if err != nil || next.Before(h.now()) {
			return false, a2cInvalid
		}
		before := a2cTime(h.now())
		h.clock.Store(next.UnixMilli())
		dbTime, err := h.dbTime()
		if err != nil {
			return false, a2cInvalid
		}
		h.emit(c.ID, "clock_advanced", map[string]any{"previous": before, "to": c.To, "db_time": dbTime})
	case "inspect":
		if h.e.mode != "inspect" || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`).MatchString(c.Label) {
			return false, a2cInvalid
		}
		snapshot, err := h.inspect(c.Label)
		if err != nil {
			h.failure(c.ID, "inspect_failed")
			return false, a2cInvalid
		}
		name := "snapshots/" + h.e.instance + "-" + a2cUint(c.ID) + "-" + c.Label + ".json"
		digest, err := h.writeJSON(name, snapshot)
		if err != nil {
			return false, a2cInvalid
		}
		h.emit(c.ID, "inspected", map[string]any{"snapshot_file": name, "snapshot_sha256": digest})
	case "purge_expired":
		if h.e.mode != "worker" || !strings.HasPrefix(h.e.caseID, "R2-") {
			return false, a2cInvalid
		}
		if err := h.purge(c.ID); err != nil {
			h.failure(c.ID, "invariant_failed")
			return false, a2cInvalid
		}
	default:
		return false, a2cInvalid
	}
	h.emit(c.ID, "command_done", map[string]any{"command": c.Command})
	return false, nil
}

func a2cBarrierValid(s string) bool {
	switch s {
	case "none", "start", "registered_before_http", "inbox_durable_before_apply", "legacy_claim_before_http", "legacy_receipt_durable":
		return true
	}
	return false
}

func (h *a2cHarness) close() {
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Lock()
	if h.active != nil && !h.active.released {
		h.active.released = true
		close(h.active.resume)
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
		h.failure(0, "cleanup_failed")
		return
	}
	if h.transport != nil {
		h.transport.CloseIdleConnections()
	}
	if h.responsePool != nil {
		_ = h.responsePool.Close()
	}
	if h.verifyPool != nil {
		_ = h.verifyPool.Close()
	}
	if h.pool != nil {
		_ = h.pool.Close()
	}
}

func a2cReadFile(dir, name string, max int64) ([]byte, error) {
	path := filepath.Join(dir, filepath.FromSlash(name))
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > max {
		return nil, a2cInvalid
	}
	return os.ReadFile(path)
}
func (h *a2cHarness) writeJSON(name string, value any) (string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return "", a2cInvalid
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	path := filepath.Join(h.e.dir, filepath.FromSlash(name))
	parent := filepath.Dir(path)
	if parent != h.e.dir {
		if err = os.MkdirAll(parent, 0700); err != nil {
			return "", err
		}
		st, err := os.Lstat(parent)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", a2cInvalid
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return "", a2cInvalid
	}
	return a2cHash(raw), nil
}

type a2cRoundTrip func(*http.Request) (*http.Response, error)

func (f a2cRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (h *a2cHarness) installHTTP() {
	mock, _ := url.Parse(h.e.mock)
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	h.transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != mock.Host {
			return nil, a2cInvalid
		}
		return dialer.DialContext(ctx, network, address)
	}, DisableKeepAlives: true, ResponseHeaderTimeout: 55 * time.Second}
	h.s.Client = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: a2cRoundTrip(func(r *http.Request) (*http.Response, error) {
		base := "/api/v3/contents/generations/tasks"
		validPath := r.Method == http.MethodPost && r.URL.Path == base || (r.Method == http.MethodGet || r.Method == http.MethodDelete) && r.URL.Path == base+"/"+h.e.upstream
		if r.URL.Scheme != "https" || r.URL.Host != "ark.cn-beijing.volces.com" || r.URL.User != nil || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.URL.RawPath != "" || !validPath || r.Header.Get("Authorization") != "Bearer "+h.e.token {
			return nil, a2cInvalid
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = mock.Scheme
		u.Host = mock.Host
		clone.URL = &u
		clone.Host = mock.Host
		clone.Header.Set("X-A2C-Run", h.e.run)
		clone.Header.Set("X-A2C-Case", h.e.caseID)
		clone.Header.Set("X-A2C-Instance", h.e.instance)
		return h.transport.RoundTrip(clone)
	})}
}

type a2cPool struct {
	*sql.DB
	h    *a2cHarness
	lane string
}

func (p *a2cPool) BeginTx(ctx context.Context, o *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &a2cTx{Tx: tx, h: p.h, lane: p.lane}, nil
}
func (p *a2cPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

type a2cTx struct {
	*sql.Tx
	h    *a2cHarness
	lane string
}

func (tx *a2cTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil {
		tx.h.afterCommit(tx.lane)
	}
	return err
}
func (h *a2cHarness) installPool(db *gorm.DB, p *sql.DB, lane string) {
	wrapper := &a2cPool{DB: p, h: h, lane: lane}
	db.ConnPool = wrapper
	db.Statement.ConnPool = wrapper
}

func (h *a2cHarness) afterCommit(lane string) {
	h.mu.Lock()
	cycle := h.active
	match := cycle != nil && !cycle.reached && ((lane == "response" && cycle.barrier == "inbox_durable_before_apply") || (lane == "main" && (cycle.barrier == "registered_before_http" || cycle.barrier == "legacy_claim_before_http" || cycle.barrier == "legacy_receipt_durable")))
	h.mu.Unlock()
	if !match {
		return
	}
	snapshot, err := h.inspect("barrier-check")
	if err != nil {
		h.failure(cycle.id, "barrier_failed")
		return
	}
	op := snapshot["operation"].(map[string]any)
	ea := snapshot["mutation_attempt"].(map[string]any)
	inbox := snapshot["inbox"].(map[string]any)
	ready := false
	switch cycle.barrier {
	case "registered_before_http":
		ready = op["status"] == "in_flight" && ea["count"] == int64(1) && inbox["count"] == int64(0)
	case "legacy_claim_before_http":
		ready = op["status"] == "in_flight" && op["attempt_id"] != "" && op["receipt_ref"] == "" && ea["count"] == int64(0)
	case "legacy_receipt_durable":
		ready = op["receipt_ref"] != "" && op["receipt_applied_at"] == nil && ea["count"] == int64(0) && snapshot["legacy"].(map[string]any)["attempt_count"] == int64(1)
	case "inbox_durable_before_apply":
		ready = ea["count"] == int64(1) && inbox["count"] == int64(1) && inbox["application_state"] == "pending" && inbox["response_received_count"] == int64(1) && op["receipt_applied_at"] == nil
	}
	if ready {
		h.reachBarrier(cycle)
	}
}

func (h *a2cHarness) reachBarrier(cycle *a2cCycle) {
	h.mu.Lock()
	if h.active != cycle || cycle.reached {
		h.mu.Unlock()
		return
	}
	cycle.reached = true
	h.mu.Unlock()
	var snapshot map[string]any
	var err error
	if cycle.barrier == "start" {
		snapshot = map[string]any{"protocol": a2cProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "pid": os.Getpid(), "before_process_once": true}
	} else {
		snapshot, err = h.inspect("committed-barrier")
	}
	if err != nil {
		h.failure(cycle.id, "barrier_failed")
		return
	}
	name := "barriers/" + h.e.instance + "-" + a2cUint(cycle.id) + "-" + cycle.barrier + ".json"
	hash, err := h.writeJSON(name, snapshot)
	if err != nil {
		h.failure(cycle.id, "barrier_failed")
		return
	}
	dbTime, err := h.dbTime()
	if err != nil {
		h.failure(cycle.id, "barrier_failed")
		return
	}
	h.emit(cycle.id, "barrier_reached", map[string]any{"barrier": cycle.barrier, "owner_cmd_id": cycle.id, "verification_file": name, "verification_sha256": hash, "clock": a2cTime(h.now()), "db_time": dbTime})
	select {
	case <-cycle.resume:
	case <-h.ctx.Done():
	case <-time.After(60 * time.Second):
		h.failure(cycle.id, "barrier_timeout")
	}
}

func (h *a2cHarness) seedBusiness(cmd uint64) error {
	ctx, cancel := context.WithTimeout(h.ctx, 55*time.Second)
	defer cancel()
	db := h.db.WithContext(ctx)
	now := h.now()
	var existing int64
	if err := db.Model(&task.Task{}).Count(&existing).Error; err != nil || existing != 0 {
		return a2cInvalid
	}
	org := identity.Organization{Slug: "a2c-" + h.e.run, Name: "Synthetic binary handoff", Group: "default", Status: identity.StatusActive}
	if err := db.Create(&org).Error; err != nil {
		return err
	}
	var key *identity.APIKey
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, issued, err := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "synthetic-handoff", Scopes: "video:*"})
		key = issued
		return err
	}); err != nil {
		return err
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		account, err := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		_, _, err = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "a2c-credit", AccountID: account.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)})
		return err
	}); err != nil {
		return err
	}
	ciphertext, err := h.s.Keyring.Encrypt(h.e.token)
	if err != nil {
		return err
	}
	channel := relay.Channel{Name: "Synthetic binary handoff", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: ciphertext, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	if err = db.Create(&channel).Error; err != nil {
		return err
	}
	scope := ProviderScope{ScopeKey: "a2c-" + h.e.run, AccountRef: "synthetic-handoff", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "handoff-v1", FXObservedAt: now}
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
	if err = db.Create(&videobilling.PricingRule{RuleID: "handoff-v1", Version: "handoff-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "handoff-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}).Error; err != nil {
		return err
	}
	history := !strings.HasPrefix(h.e.caseID, "R1-")
	if err = db.Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: history, RetentionDays: 30, UpdatedAt: now}).Error; err != nil {
		return err
	}
	view, replay, err := h.s.Create(ctx, key, "a2c-create-request", "a2c-create", ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "synthetic binary handoff"}}})
	if err != nil || replay {
		return a2cInvalid
	}
	if err = h.s.ProcessOnce(ctx); err != nil {
		return err
	}
	var st State
	var row task.Task
	if err = db.Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&st).Error; err != nil {
		return err
	}
	if err = db.Select("task_id", "org_id", "status", "upstream_task_id").Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&row).Error; err != nil {
		return err
	}
	if row.Status != "queued" || row.UpstreamTaskID != h.e.upstream {
		return a2cInvalid
	}
	if h.e.action == "delete" {
		if h.e.clockMode != "logical" {
			return a2cInvalid
		}
		h.clock.Store(st.NextPollAt.Add(time.Millisecond).UnixMilli())
		if err = h.s.ProcessOnce(ctx); err != nil {
			return err
		}
		if err = db.Select("task_id", "org_id", "status", "upstream_task_id").Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&row).Error; err != nil {
			return err
		}
		if err = db.Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&st).Error; err != nil {
			return err
		}
		if row.Status != "succeeded" || st.BillingState != "settled" {
			return a2cInvalid
		}
	}
	var operation Operation
	if h.e.action == "cancel" {
		operation, replay, err = h.s.Cancel(ctx, org.ID, view.ID, "a2c-cancel-request", "a2c-cancel")
	} else {
		operation, replay, err = h.s.Delete(ctx, org.ID, view.ID, "a2c-delete-request")
	}
	if err != nil || replay || operation.Status != "pending" {
		return a2cInvalid
	}
	var snapshot videobilling.Snapshot
	if err = db.Select("id", "org_id", "task_id", "hold_id", "estimated_tokens", "hold_usd", "supplier_reserved_cny").Where("org_id = ? AND task_id = ?", org.ID, view.ID).Take(&snapshot).Error; err != nil {
		return err
	}
	if snapshot.EstimatedTokens != 108000 || snapshot.HoldUSD.StringFixed(8) != "0.86400000" || snapshot.SupplierReservedCNY.StringFixed(8) != "4.96800000" {
		return a2cInvalid
	}
	h.seed = a2cSeed{Protocol: a2cProtocol, RunID: h.e.run, CaseID: h.e.caseID, Action: h.e.action, OrgID: org.ID, KeyID: key.ID, TaskID: view.ID, OperationID: operation.ID, ScopeID: scope.ID, ChannelID: channel.ID, HoldID: snapshot.HoldID, UpstreamID: h.e.upstream, CreationRole: h.e.role, History: history, InitialTime: a2cTime(h.e.initial), FinishedClock: a2cTime(h.now()), MutationKind: h.e.action + "_task", Oracle: map[string]string{"credit_usd": "100.00000000", "width": "1280", "height": "720", "fps": "24", "duration_seconds": "5", "risk": "1", "effective_ratio": "1", "retail_usd_per_1m": "8", "supplier_cny_per_1m": "46", "fx_usd_per_cny": "0.14", "estimated_tokens": "108000", "hold_usd": "0.86400000", "supplier_reserved_cny": "4.96800000", "completion_tokens": "100", "captured_usd": "0.00080000", "released_usd": "0.86320000", "supplier_cny": "0.00460000"}}
	proof, err := h.inspect("seed")
	if err != nil {
		return err
	}
	if proof["operation"].(map[string]any)["status"] != "pending" || proof["mutation_attempt"].(map[string]any)["count"] != int64(0) || proof["inbox"].(map[string]any)["count"] != int64(0) {
		return a2cInvalid
	}
	var createAttempts []ExecutionAttempt
	if err := db.Where("org_id = ? AND task_id = ? AND action = ?", org.ID, view.ID, "create_task").Limit(2).Find(&createAttempts).Error; err != nil {
		return err
	}
	if h.e.role == "Pboth" {
		if len(createAttempts) != 1 || createAttempts[0].StorageProtocol != "inbox-v1" || createAttempts[0].ExecutionSchemaVersion != 1 || createAttempts[0].TargetUpstreamTaskID != nil {
			return a2cInvalid
		}
	} else if len(createAttempts) != 0 {
		return a2cInvalid
	}
	finance := proof["finance"].(map[string]any)
	if h.e.action == "cancel" {
		if finance["frozen_usd"] != "0.86400000" || finance["captured_usd"] != "0.00000000" {
			return a2cInvalid
		}
	} else if finance["frozen_usd"] != "0.00000000" || finance["captured_usd"] != "0.00080000" || finance["released_usd"] != "0.86320000" || finance["capture_journal_count"] != int64(1) {
		return a2cInvalid
	}
	h.seed.BaselineFinancial = proof["fingerprints"].(map[string]string)["financial_all"]
	digest, err := h.writeJSON("seed.json", h.seed)
	if err != nil {
		return err
	}
	if _, err = h.writeJSON("seed-baseline.json", proof); err != nil {
		return err
	}
	h.emit(cmd, "seeded", map[string]any{"seed_file": "seed.json", "seed_sha256": digest, "org_id": a2cUint(org.ID), "key_id": a2cUint(key.ID), "task_id": view.ID, "operation_id": operation.ID, "action": h.e.action, "clock": a2cTime(h.now()), "upstream_id": h.e.upstream, "baseline_financial_sha256": h.seed.BaselineFinancial})
	h.emit(cmd, "pending_intent", map[string]any{"org_id": a2cUint(org.ID), "task_id": view.ID, "operation_id": operation.ID, "action": h.e.action, "operation_status": "pending", "mutation_attempt_count": 0, "receipt_count": 0})
	return nil
}

// canonicalRows has explicit caller-owned SELECT lists. It never accepts SQL
// from commands; every cell is an exact string/null (including DECIMAL/uint64).
func a2cCanonicalRows(tx *gorm.DB, query string, args ...any) ([][]any, error) {
	rows, err := tx.Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([][]any, 0)
	for rows.Next() {
		if len(out) >= 1000 {
			return nil, a2cInvalid
		}
		values := make([]any, len(cols))
		pointers := make([]any, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return nil, err
		}
		for i, v := range values {
			switch x := v.(type) {
			case nil:
			case []byte:
				values[i] = string(x)
			case time.Time:
				values[i] = a2cTime(x)
			case float64:
				return nil, a2cInvalid
			default:
				values[i] = fmt.Sprint(x)
			}
		}
		out = append(out, values)
	}
	return out, rows.Err()
}
func a2cRowsHash(tx *gorm.DB, query string, args ...any) (string, error) {
	rows, err := a2cCanonicalRows(tx, query, args...)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	return a2cHash(raw), nil
}

func (h *a2cHarness) inputFacts(tx *gorm.DB, attempt ExecutionAttempt) (map[string]any, error) {
	var meta struct {
		PayloadID     string
		OrgID         uint64
		RequestID     string
		AttemptID     string
		Purpose       string
		Direction     string
		SchemaVersion uint32
		CaptureStatus string
		ExpiresAt     time.Time
		HasKey        bool
		HasNonce      bool
	}
	if err := tx.Table("invocation_payloads").Select("payload_id,org_id,request_id,attempt_id,purpose,direction,schema_version,capture_status,expires_at,(OCTET_LENGTH(wrapped_key)>0) AS has_key,(OCTET_LENGTH(nonce)>0) AS has_nonce").Where("org_id = ? AND payload_id = ?", h.seed.OrgID, attempt.InputPayloadRef).Take(&meta).Error; err != nil {
		return nil, err
	}
	if meta.OrgID != h.seed.OrgID || meta.RequestID != h.seed.OperationID || meta.AttemptID != attempt.AttemptID || meta.Purpose != "execution" || meta.Direction != "effective_request" || meta.SchemaVersion != 2 {
		return nil, a2cInvalid
	}
	var staging int64
	if err := tx.Table("invocation_payload_staging").Where("payload_id = ?", meta.PayloadID).Count(&staging).Error; err != nil {
		return nil, err
	}
	digest, err := a2cRowsHash(tx, "SELECT payload_id,org_id,request_id,attempt_id,correlation_key,direction,purpose,schema_version,body_format,capture_representation,encryption_key_id,ciphertext_sha256,plaintext_bytes,redaction_version,created_at,expires_at FROM invocation_payloads WHERE org_id=? AND payload_id=?", h.seed.OrgID, meta.PayloadID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"payload_ref": meta.PayloadID, "capture_status": meta.CaptureStatus, "expires_at": a2cTime(meta.ExpiresAt), "wrapped_key_present": meta.HasKey, "nonce_present": meta.HasNonce, "staging_count": staging, "identity_sha256": digest}, nil
}

func (h *a2cHarness) inspect(label string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	result := map[string]any{"protocol": a2cProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "label": label, "source_sha": h.e.source, "clock": a2cTime(h.now())}
	err := h.verifier.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var dbNow time.Time
		if err := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&dbNow).Error; err != nil {
			return err
		}
		result["db_time"] = a2cTime(dbNow)
		var t task.Task
		var st State
		var op Operation
		if err := tx.Select("task_id", "org_id", "status", "upstream_task_id").Where("org_id = ? AND task_id = ?", h.seed.OrgID, h.seed.TaskID).Take(&t).Error; err != nil {
			return err
		}
		if err := tx.Select("task_id", "org_id", "billing_state", "deleted_at", "next_poll_at", "runtime_version", "adapter_version", "protocol_contract_version", "billing_schema_version", "provider_scope_id", "credential_snapshot_id", "billing_snapshot_id").Where("org_id = ? AND task_id = ?", h.seed.OrgID, h.seed.TaskID).Take(&st).Error; err != nil {
			return err
		}
		if err := tx.Select("id", "org_id", "resource_id", "kind", "status", "version", "attempt_id", "lease_until", "error_code", "receipt_ref", "receipt_applied_at").Where("org_id = ? AND id = ? AND resource_id = ?", h.seed.OrgID, h.seed.OperationID, h.seed.TaskID).Take(&op).Error; err != nil {
			return err
		}
		if op.Kind != h.seed.MutationKind || t.UpstreamTaskID != h.e.upstream {
			return a2cInvalid
		}
		result["ids"] = map[string]any{"org_id": a2cUint(h.seed.OrgID), "key_id": a2cUint(h.seed.KeyID), "task_id": h.seed.TaskID, "operation_id": h.seed.OperationID, "upstream_id": h.e.upstream, "hold_id": a2cUint(h.seed.HoldID)}
		result["task"] = map[string]any{"status": t.Status, "billing_state": st.BillingState, "deleted_at": a2cNullableTime(st.DeletedAt), "next_poll_at": a2cTime(st.NextPollAt), "upstream_id": t.UpstreamTaskID}
		result["operation"] = map[string]any{"status": op.Status, "version": a2cUint(op.Version), "attempt_id": op.AttemptID, "lease_until": a2cNullableTime(op.LeaseUntil), "error_code": op.ErrorCode, "receipt_ref": op.ReceiptRef, "receipt_applied_at": a2cNullableTime(op.ReceiptAppliedAt)}
		var attempts []ExecutionAttempt
		if err := tx.Where("org_id = ? AND operation_id = ?", h.seed.OrgID, h.seed.OperationID).Order("attempt_id").Limit(2).Find(&attempts).Error; err != nil {
			return err
		}
		if len(attempts) > 1 {
			return a2cInvalid
		}
		eaFacts := map[string]any{"count": int64(len(attempts))}
		result["mutation_attempt"] = eaFacts
		result["action_input"] = nil
		if len(attempts) == 1 {
			a := attempts[0]
			contract := videoruntime.Contract{RuntimeVersion: a.RuntimeVersion, AdapterVersion: a.AdapterVersion, ProtocolContractVersion: a.ProtocolContractVersion, BillingSchemaVersion: a.BillingSchemaVersion}
			d, err := videoruntime.Resolve(contract)
			if err != nil {
				return err
			}
			ad, err := videoruntime.ResolveActionContract(videoruntime.ActionContractKey{StorageProtocol: a.StorageProtocol, Action: a.Action, Contract: contract, ExecutionInputSchemaVersion: a.ExecutionSchemaVersion, ObservationSchemaVersion: a.ObservationSchemaVersion, DeliverySchemaVersion: 0})
			if err != nil {
				return err
			}
			stateContract := videoruntime.Contract{RuntimeVersion: st.RuntimeVersion, AdapterVersion: st.AdapterVersion, ProtocolContractVersion: st.ProtocolContractVersion, BillingSchemaVersion: st.BillingSchemaVersion}
			if contract != stateContract || a.ProviderScopeID != h.seed.ScopeID || a.ProviderScopeID != st.ProviderScopeID || a.CredentialSnapshotID != st.CredentialSnapshotID || a.BillingSnapshotID != st.BillingSnapshotID || a.OrgID != h.seed.OrgID || a.TaskID != h.seed.TaskID || a.OperationID != h.seed.OperationID || a.Action != h.seed.MutationKind || a.TargetUpstreamTaskID == nil || *a.TargetUpstreamTaskID != h.e.upstream || a.InputIdentityHMAC == "" || a.InputHMACVersion != "v1" || a.EvidenceHMACVersion != "v1" {
				return a2cInvalid
			}
			eaFacts["id"] = a.AttemptID
			eaFacts["operation_claim_version"] = a2cUint(a.OperationClaimVersion)
			eaFacts["storage_protocol"] = a.StorageProtocol
			eaFacts["action"] = a.Action
			eaFacts["input_schema"] = a.ExecutionSchemaVersion
			eaFacts["observation_schema"] = a.ObservationSchemaVersion
			eaFacts["delivery_schema"] = 0
			eaFacts["target"] = *a.TargetUpstreamTaskID
			eaFacts["input_payload_ref"] = a.InputPayloadRef
			eaFacts["registered_at"] = a2cTime(a.RegisteredAt)
			eaFacts["runtime_descriptor_hash"] = d.Hash
			eaFacts["action_descriptor_hash"] = ad.Hash
			input, err := h.inputFacts(tx, a)
			if err != nil {
				return err
			}
			result["action_input"] = input
		}
		var receipts []ResponseInbox
		if err := tx.Where("org_id = ? AND operation_id = ?", h.seed.OrgID, h.seed.OperationID).Order("receipt_id").Limit(2).Find(&receipts).Error; err != nil {
			return err
		}
		if len(receipts) > 1 {
			return a2cInvalid
		}
		inbox := map[string]any{"count": int64(len(receipts))}
		result["inbox"] = inbox
		var responsePayload string
		if len(receipts) == 1 {
			r := receipts[0]
			if len(attempts) != 1 || r.AttemptID != attempts[0].AttemptID || r.Action != h.seed.MutationKind || r.OrgID != h.seed.OrgID || r.TaskID != h.seed.TaskID || r.StorageProtocol != "inbox-actions-v1" {
				return a2cInvalid
			}
			a := attempts[0]
			if r.RuntimeVersion != a.RuntimeVersion || r.AdapterVersion != a.AdapterVersion || r.ProtocolContractVersion != a.ProtocolContractVersion || r.BillingSchemaVersion != a.BillingSchemaVersion || r.ExecutionSchemaVersion != a.ExecutionSchemaVersion || r.ObservationSchemaVersion != a.ObservationSchemaVersion || r.ProviderScopeID != a.ProviderScopeID || r.CredentialSnapshotID != a.CredentialSnapshotID || r.BillingSnapshotID != a.BillingSnapshotID || r.EvidenceHMACVersion != a.EvidenceHMACVersion || r.EvidenceHMAC == "" {
				return a2cInvalid
			}
			var app ReceiptApplication
			if err := tx.Where("org_id = ? AND receipt_id = ? AND task_id = ?", h.seed.OrgID, r.ReceiptID, h.seed.TaskID).Take(&app).Error; err != nil {
				return err
			}
			// A GET may already have decided the operation, in which case applying
			// its late ACK legitimately creates no event. A nonempty marker must
			// point to this exact receipt's real event, not merely any task event.
			if app.AppliedEventID != "" {
				var matched int64
				if err := tx.Model(&Event{}).Where("id = ? AND org_id = ? AND task_id = ? AND type = ? AND request_id = ? AND evidence_ref = ?", app.AppliedEventID, h.seed.OrgID, h.seed.TaskID, "task.operation_updated", r.ReceiptID, r.PayloadRef).Count(&matched).Error; err != nil {
					return err
				}
				if app.State != "applied" || app.AppliedAt == nil || matched != 1 {
					return a2cInvalid
				}
			}
			var received int64
			if err := tx.Model(&Attempt{}).Where("org_id = ? AND operation_id = ? AND attempt_id = ? AND phase = ? AND evidence_ref = ?", h.seed.OrgID, h.seed.OperationID, r.AttemptID, "response_received", r.PayloadRef).Count(&received).Error; err != nil {
				return err
			}
			inbox["receipt_id"] = r.ReceiptID
			inbox["attempt_id"] = r.AttemptID
			inbox["response_payload_ref"] = r.PayloadRef
			inbox["outcome"] = r.Outcome
			inbox["http_status"] = r.HTTPStatus
			inbox["application_state"] = app.State
			inbox["application_error_code"] = app.ErrorCode
			inbox["next_attempt_at"] = a2cTime(app.NextAttemptAt)
			inbox["applied_at"] = a2cNullableTime(app.AppliedAt)
			inbox["applied_event_id"] = app.AppliedEventID
			inbox["response_received_count"] = received
			inbox["evidence_hmac_sha256"] = a2cHash([]byte(r.EvidenceHMAC))
			responsePayload = r.PayloadRef
		}
		var legacyCount int64
		if len(attempts) == 0 {
			if err := tx.Model(&Attempt{}).Where("org_id = ? AND operation_id = ? AND phase = ? AND evidence_ref <> ''", h.seed.OrgID, h.seed.OperationID, "response_received").Count(&legacyCount).Error; err != nil {
				return err
			}
			responsePayload = op.ReceiptRef
		}
		result["legacy"] = map[string]any{"receipt_present": len(attempts) == 0 && op.ReceiptRef != "", "receipt_ref": func() string {
			if len(attempts) == 0 {
				return op.ReceiptRef
			}
			return ""
		}(), "receipt_applied_at": func() any {
			if len(attempts) == 0 {
				return a2cNullableTime(op.ReceiptAppliedAt)
			}
			return nil
		}(), "attempt_count": legacyCount}
		audits := map[string]any{"execution_read_count": int64(0), "response_capture_status": nil, "response_staging_count": int64(0)}
		if len(attempts) == 1 {
			var count int64
			if err := tx.Table("invocation_payload_accesses").Where("org_id = ? AND payload_id = ?", h.seed.OrgID, attempts[0].InputPayloadRef).Count(&count).Error; err != nil {
				return err
			}
			audits["execution_read_count"] = count
		}
		if responsePayload != "" {
			var meta struct{ CaptureStatus string }
			if err := tx.Table("invocation_payloads").Select("capture_status").Where("org_id = ? AND payload_id = ?", h.seed.OrgID, responsePayload).Take(&meta).Error; err != nil {
				return err
			}
			var count int64
			if err := tx.Table("invocation_payload_staging").Where("payload_id = ?", responsePayload).Count(&count).Error; err != nil {
				return err
			}
			audits["response_capture_status"] = meta.CaptureStatus
			audits["response_staging_count"] = count
		}
		result["audits"] = audits
		finance, prints, err := h.financialFacts(tx)
		if err != nil {
			return err
		}
		result["finance"] = finance
		// Legacy reducers identify events by attempt, whereas inbox reducers
		// identify them by receipt. Include ALL identifiers belonging to this
		// operation, rather than just AppliedEventID, so duplicate events alter
		// the fingerprint even when the application marker is unchanged.
		var legacyAttempts []string
		if err := tx.Model(&Attempt{}).Distinct("attempt_id").Where("org_id = ? AND operation_id = ? AND attempt_id <> ''", h.seed.OrgID, h.seed.OperationID).Order("attempt_id").Limit(1001).Pluck("attempt_id", &legacyAttempts).Error; err != nil {
			return err
		}
		if len(legacyAttempts) > 1000 {
			return a2cInvalid
		}
		eventIDs := a2cActionEventIDs(op.AttemptID, legacyAttempts, attempts, receipts)
		if len(eventIDs) == 0 {
			prints["action_application_events"] = a2cHash([]byte("[]"))
		} else {
			prints["action_application_events"], err = a2cRowsHash(tx, "SELECT id,sequence,type,request_id,evidence_ref,from_status,to_status,billing_state,created_at FROM video_events WHERE org_id=? AND task_id=? AND type='task.operation_updated' AND request_id IN ? ORDER BY id", h.seed.OrgID, h.seed.TaskID, eventIDs)
		}
		if err != nil {
			return err
		}
		result["fingerprints"] = prints
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func a2cActionEventIDs(current string, legacy []string, attempts []ExecutionAttempt, receipts []ResponseInbox) []string {
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" {
			seen[id] = true
		}
	}
	add(current)
	for _, id := range legacy {
		add(id)
	}
	for _, attempt := range attempts {
		add(attempt.AttemptID)
	}
	for _, receipt := range receipts {
		add(receipt.ReceiptID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (h *a2cHarness) financialFacts(tx *gorm.DB) (map[string]any, map[string]string, error) {
	var account ledger.Account
	var hold ledger.Hold
	if err := tx.Select("id", "org_id", "type", "shard", "posted_balance", "held_amount").Where("org_id = ? AND type = ? AND shard = 0", h.seed.OrgID, ledger.AccountUserCredit).Take(&account).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Select("id", "account_id", "amount", "captured_amount", "status").Where("id = ? AND account_id = ?", h.seed.HoldID, account.ID).Take(&hold).Error; err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{}
	countQueries := map[string]string{
		"capture_journal_count":   "SELECT COUNT(*) FROM ledger_journal WHERE account_id=? AND kind='capture'",
		"release_movement_count":  "SELECT COUNT(*) FROM ledger_movements WHERE org_id=? AND direction='unfreeze'",
		"billing_entry_count":     "SELECT COUNT(*) FROM video_billing_entries WHERE org_id=?",
		"quota_reservation_count": "SELECT COUNT(*) FROM video_quota_reservations WHERE org_id=?",
		"quota_bucket_count":      "SELECT COUNT(DISTINCT bucket_id) FROM video_quota_reservations WHERE org_id=?",
	}
	for name, q := range countQueries {
		arg := h.seed.OrgID
		if name == "capture_journal_count" {
			arg = account.ID
		}
		var n int64
		if err := tx.Raw(q, arg).Scan(&n).Error; err != nil {
			return nil, nil, err
		}
		counts[name] = n
	}
	var sums struct {
		Captured decimal.Decimal
		Released decimal.Decimal
	}
	if err := tx.Table("video_billing_entries").Select("COALESCE(SUM(captured_usd),0) AS captured,COALESCE(SUM(released_usd),0) AS released").Where("org_id = ? AND task_id = ?", h.seed.OrgID, h.seed.TaskID).Scan(&sums).Error; err != nil {
		return nil, nil, err
	}
	drifts, err := ledger.CheckDrift(tx)
	if err != nil {
		return nil, nil, err
	}
	if len(drifts) != 0 {
		return nil, nil, a2cInvalid
	}
	state := map[int8]string{ledger.HoldActive: "active", ledger.HoldCaptured: "captured", ledger.HoldReleased: "released"}[hold.Status]
	if state == "" {
		return nil, nil, a2cInvalid
	}
	finance := map[string]any{"credit_usd": "100.00000000", "available_usd": account.Available().StringFixed(8), "frozen_usd": account.HeldAmount.StringFixed(8), "hold_usd": hold.Amount.StringFixed(8), "captured_usd": sums.Captured.StringFixed(8), "released_usd": sums.Released.StringFixed(8), "hold_state": state, "ledger_drift_count": len(drifts)}
	for k, v := range counts {
		finance[k] = v
	}
	type query struct {
		name, sql string
		args      []any
	}
	queries := []query{
		{"accounts", "SELECT id,org_id,type,shard,posted_balance,held_amount FROM ledger_accounts WHERE org_id=? ORDER BY id", []any{h.seed.OrgID}},
		{"holds", "SELECT id,account_id,amount,captured_amount,status,idempotency_key,ref_type,ref_id FROM ledger_holds WHERE account_id=? ORDER BY id", []any{account.ID}},
		{"journals", "SELECT id,account_id,kind,delta,balance_after,hold_id,idempotency_key,ref_type,ref_id FROM ledger_journal WHERE account_id=? ORDER BY id", []any{account.ID}},
		{"ledger_movements", "SELECT id,org_id,account_id,operation_id,type,direction,status,currency,amount,posted_delta,held_delta,available_delta,journal_id,hold_id,source_type,source_id,component,ref_type,ref_id,created_at FROM ledger_movements WHERE org_id=? ORDER BY id", []any{h.seed.OrgID}},
		{"ledger_operations", "SELECT id,org_id,type,direction,requested_amount,currency,status,ref_type,ref_id,failure_code,created_at,updated_at FROM ledger_operations WHERE org_id=? ORDER BY id", []any{h.seed.OrgID}},
		{"ledger_events", "SELECT e.id,e.operation_id,e.status,e.code,e.created_at FROM ledger_operation_events e JOIN ledger_operations o ON o.id=e.operation_id WHERE o.org_id=? ORDER BY e.id", []any{h.seed.OrgID}},
		{"billing_entries", "SELECT id,org_id,task_id,settlement_key,state,snapshot_id,evidence_id,hold_id,journal_id,completion_tokens,calculated_usd,captured_usd,released_usd,absorbed_usd,supplier_cny,supplier_usd,reason,created_at FROM video_billing_entries WHERE org_id=? AND task_id=? ORDER BY id", []any{h.seed.OrgID, h.seed.TaskID}},
		{"quota_reservations", "SELECT id,org_id,task_id,bucket_id,metric,supplier,reserved_amount,reserved_units,actual_amount,actual_units,state,created_at,settled_at FROM video_quota_reservations WHERE org_id=? AND task_id=? ORDER BY id", []any{h.seed.OrgID, h.seed.TaskID}},
		{"quota_buckets", "SELECT b.id,b.policy_id,b.scope_key,b.timezone,b.org_id,b.period_start,b.period_end,b.limit_amount,b.limit_units,b.reserved_amount,b.consumed_amount,b.reserved_units,b.consumed_units,b.blocked FROM video_quota_buckets b WHERE b.id IN (SELECT r.bucket_id FROM video_quota_reservations r WHERE r.org_id=? AND r.task_id=?) ORDER BY b.id", []any{h.seed.OrgID, h.seed.TaskID}},
		{"key_usage", "SELECT id,org_id,status,quota_limit,used_amount,model_access_mode,routing_group_id,policy_version FROM api_keys WHERE org_id=? AND id=? ORDER BY id", []any{h.seed.OrgID, h.seed.KeyID}},
	}
	prints := map[string]string{}
	for _, q := range queries {
		digest, err := a2cRowsHash(tx, q.sql, q.args...)
		if err != nil {
			return nil, nil, err
		}
		prints[q.name] = digest
	}
	raw, err := json.Marshal(prints)
	if err != nil {
		return nil, nil, err
	}
	prints["financial_all"] = a2cHash(raw)
	return finance, prints, nil
}

func (h *a2cHarness) purge(cmd uint64) error {
	before, err := h.inspect("before-purge")
	if err != nil {
		return err
	}
	input, ok := before["action_input"].(map[string]any)
	if !ok {
		return a2cInvalid
	}
	expiry, err := a2cParseTime(input["expires_at"].(string))
	if err != nil || !h.now().After(expiry) {
		return a2cInvalid
	}
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()
	count, err := h.s.Evidence.PurgeExpired(ctx, 1000)
	if err != nil {
		return err
	}
	after, err := h.inspect("after-purge")
	if err != nil {
		return err
	}
	next, ok := after["action_input"].(map[string]any)
	if !ok || next["capture_status"] != "expired" || next["wrapped_key_present"] != false || next["nonce_present"] != false || next["staging_count"] != int64(0) || next["identity_sha256"] != input["identity_sha256"] {
		return a2cInvalid
	}
	h.emit(cmd, "purged", map[string]any{"input_payload_ref": next["payload_ref"], "purged_count": count, "before_identity_sha256": input["identity_sha256"], "after_identity_sha256": next["identity_sha256"], "wrapped_key_present": false, "nonce_present": false, "staging_count": 0, "capture_status": "expired"})
	return nil
}

// These offline checks only validate the test-control boundary. They are not
// counted as any of the real two-binary/MySQL scenarios.
func TestMutationHandoffProtocolControlContract(t *testing.T) {
	if len(a2cCases()) != 39 || a2cCases()["N1-wall-cancel-Ca"] != "cancel" || a2cCases()["N4-delete-EOF-Poff"] != "delete" {
		t.Fatal("independent scenario inventory changed")
	}
	base := `{"protocol":"a2c-process-v1","run_id":"0123456789abcdef","case_id":"N3-cancel-Ca","instance":"sender-p","cmd_id":1,"command":"run_once","barrier":"inbox_durable_before_apply"}`
	if c, err := a2cDecodeCommand([]byte(base)); err != nil || c.ID != 1 || c.Barrier != "inbox_durable_before_apply" {
		t.Fatal("valid runner command rejected")
	}
	bad := []string{
		strings.Replace(base, `"cmd_id":1`, `"cmd_id":1,"cmd_id":2`, 1),
		strings.Replace(base, `"barrier":"inbox_durable_before_apply"`, `"barrier":null`, 1),
		strings.Replace(base, `"barrier":"inbox_durable_before_apply"`, `"barrier":"none","sql":"UPDATE tasks"`, 1),
		strings.Replace(base, `"cmd_id":1`, `"cmd_id":1.0`, 1),
		strings.Replace(base, `"command":"run_once"`, `"command":"private_apply"`, 1),
		base + `{}`, "\ufeff" + base,
	}
	for i, raw := range bad {
		if _, err := a2cDecodeCommand([]byte(raw)); err == nil {
			t.Fatalf("unsafe control vector %d accepted", i)
		}
	}
	ids := a2cActionEventIDs("current", []string{"legacy", "", "current"}, []ExecutionAttempt{{AttemptID: "registered"}}, []ResponseInbox{{ReceiptID: "receipt"}})
	if !reflect.DeepEqual(ids, []string{"current", "legacy", "receipt", "registered"}) {
		t.Fatal("action event scope must retain legacy, registered and receipt identities")
	}
}
