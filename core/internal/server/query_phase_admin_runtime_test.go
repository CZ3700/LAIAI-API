package server

// Staging-only process entry. No production function or data is replaced.
import (
	"bufio"
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type phaseAdminSettings struct{ run, caseID, instance, role, source, dir, dsn, mock, token string }
type phaseAdminHarness struct {
	e                phaseAdminSettings
	db, verify       *gorm.DB
	pool, verifyPool *sql.DB
	s                *video.Service
	adapter          *phaseAdminAdapter
	seed             phaseSeed
	schema           string
	transport        *http.Transport
	mu               sync.Mutex
	http             phaseHTTPCounts
	sequence         uint64
	columns, primary map[string][]string
}
type phaseAdminGate struct{}

func (phaseAdminGate) GateKey(*identity.APIKey, string, string) (int, string, bool) {
	return 200, "", true
}
func (phaseAdminGate) AllowModelRate(uint64, string) (bool, int) { return true, 0 }

type phaseAdminTransport func(*http.Request) (*http.Response, error)

func (f phaseAdminTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func phaseNum(v uint64) string                                                  { return strconv.FormatUint(v, 10) }
func phaseHash(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func phaseAdminEnvironment() (phaseAdminSettings, error) {
	e := phaseAdminSettings{run: os.Getenv("ARK_PHASE_HANDOFF_RUN"), caseID: os.Getenv("ARK_PHASE_HANDOFF_CASE"), instance: os.Getenv("ARK_PHASE_HANDOFF_INSTANCE"), role: os.Getenv("ARK_PHASE_HANDOFF_ROLE"), source: os.Getenv("ARK_PHASE_HANDOFF_SOURCE"), dir: os.Getenv("ARK_PHASE_HANDOFF_DIR"), dsn: os.Getenv("ARK_PHASE_HANDOFF_DSN"), mock: os.Getenv("ARK_PHASE_HANDOFF_MOCK_URL"), token: os.Getenv("ARK_PHASE_HANDOFF_MOCK_TOKEN")}
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
	if os.Getenv("ARK_PHASE_HANDOFF_PROTOCOL") != phaseProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.run) || !regexp.MustCompile(`^(N|C)-[a-z0-9-]{1,32}$`).MatchString(e.instance) || !strings.HasPrefix(e.instance, e.role+"-") || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(e.source) || e.source != queryPhaseCompiledSource || e.role != queryPhaseCompiledRole || (e.role == "N") != (e.source == phaseOldSource) {
		return e, errPhaseHarness
	}
	if e.role == "N" {
		if e.caseID != "F03_settled_delivery" {
			return e, errPhaseHarness
		}
	} else if e.role != "C" || (e.caseID != "F05_admin_first" && e.caseID != "F05_settlement_first" && e.caseID != "F05_admin_adjust_delete_then_fresh_B") {
		return e, errPhaseHarness
	}
	if g := os.Getenv("ARK_PHASE_HANDOFF_GATE"); g != "" && g != "false" {
		return e, errPhaseHarness
	}
	u, err := url.Parse(e.mock)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(e.token) < 24 || len(e.token) > 128 || strings.ContainsAny(e.token, "\r\n") || !filepath.IsAbs(e.dir) {
		return e, errPhaseHarness
	}
	info, err := os.Lstat(e.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return e, errPhaseHarness
	}
	return e, nil
}

func TestMySQLQueryPhaseAdminHandoffProcess(t *testing.T) {
	if os.Getenv("ARK_PHASE_HANDOFF_PROTOCOL") == "" {
		t.Skip("dedicated fixed-source phase admin runner required")
	}
	e, err := phaseAdminEnvironment()
	if err != nil {
		t.Fatal("invalid isolated admin environment")
	}
	h := &phaseAdminHarness{e: e, columns: map[string][]string{}, primary: map[string][]string{}}
	closed := false
	defer func() {
		if !closed && h.close() != nil {
			t.Error("admin emergency drain failed")
		}
	}()
	if h.initialize() != nil {
		t.Fatal("admin initialization failed")
	}
	_, settlement := any(h.s).(phaseSettlementConsumer)
	if settlement != (e.role == "C") {
		t.Fatal("admin compiled capability mismatch")
	}
	if h.emit("0", "ready", map[string]any{"pid": os.Getpid(), "schema_version": h.schema, "settlement": settlement, "management": true, "producer_gate": "not_present", "source": e.source}, nil) != nil {
		t.Fatal("admin ready evidence failed")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 65537)
	var last uint64
	for scanner.Scan() {
		c, x := decodePhaseCommand(scanner.Bytes())
		id, ie := phaseID(c.ID)
		if x != nil || ie != nil || id != last+1 || c.Run != e.run || c.Case != e.caseID || c.Instance != e.instance {
			t.Fatal("invalid admin command ownership")
		}
		last = id
		if c.Command == "stop" {
			if h.close() != nil {
				t.Fatal("admin drain failed")
			}
			closed = true
			if h.emit(c.ID, "drained", map[string]any{"joined": true, "closed": true}, nil) != nil {
				t.Fatal("admin drain evidence failed")
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		kind, data, x := h.command(ctx, c)
		cancel()
		if x != nil {
			_ = h.emit(c.ID, "failed", map[string]any{"class": "harness_failure", "command": c.Command}, nil)
			t.Fatal("admin command failed")
		}
		proof, x := h.inspect()
		if x != nil {
			t.Fatal("admin snapshot failed")
		}
		if h.emit(c.ID, kind, data, &proof) != nil {
			t.Fatal("admin command evidence failed")
		}
	}
	t.Fatal("admin input ended without stop")
}

func (h *phaseAdminHarness) initialize() error {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = h.verify.WithContext(ctx).Raw("SELECT MAX(version) FROM schema_migrations").Scan(&h.schema).Error; err != nil || h.schema != "0135_video_query_settlement_shape" {
		return errPhaseHarness
	}
	if err = h.loadSeed(ctx); err != nil {
		return err
	}
	h.db, err = ledger.EnableActivityTracking(h.db)
	if err != nil {
		return err
	}
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	if err != nil {
		return err
	}
	h.s, err = video.NewService(h.db, kr, []byte(strings.Repeat("h", 32)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	h.s.Gate = phaseAdminGate{}
	if err = h.s.ConfigureAssets(video.AssetIntegrationOptions{}); err != nil {
		return err
	}
	if err = h.s.ConfigureResponseInbox(cfg.FormatDSN(), true); err != nil {
		return err
	}
	h.s.MutationResponseInboxEnabled = true
	h.transport = &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 10 * time.Second}
	h.s.Client = &http.Client{Transport: phaseAdminTransport(h.roundTrip), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if err = h.db.Callback().Query().After("gorm:query").Register("phase_admin_credential_count", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "video_credential_snapshots" {
			h.mu.Lock()
			h.http.CredentialReads++
			h.mu.Unlock()
		}
	}); err != nil {
		return err
	}
	item := h.seed.Tasks["primary"]
	org, _ := phaseID(h.seed.OrgID)
	key, _ := phaseID(h.seed.KeyID)
	scope, _ := phaseID(h.seed.ScopeID)
	var tk task.Task
	if err = h.verify.WithContext(ctx).Where("org_id=? AND task_id=?", org, item.TaskID).Take(&tk).Error; err != nil {
		return err
	}
	seed := phaseAdminSeed{OrgID: org, KeyID: key, ScopeID: scope, TaskID: item.TaskID, UpstreamID: item.UpstreamID, Model: tk.Model, CreateRequestID: item.CreateRequestID, QueryAttemptID: item.QueryAttemptID}
	for i, v := range h.seed.Actors {
		seed.ActorIDs[i], _ = phaseID(v)
	}
	gin.SetMode(gin.TestMode)
	h.adapter, err = newPhaseAdminAdapter(ctx, h.db, h.s, kr, []byte(strings.Repeat("s", 32)), seed, "phase_"+h.e.run)
	return err
}

func (h *phaseAdminHarness) loadSeed(ctx context.Context) error {
	path := filepath.Join(h.e.dir, "seed-private.json")
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 65536 {
		return errPhaseHarness
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var seed phaseSeed
	if _, e = phaseStrictObject(raw, &seed); e != nil {
		return e
	}
	if seed.Protocol != phaseProtocol || seed.Run != h.e.run || seed.Case != h.e.caseID || seed.Profile != "inbox" || len(seed.PlainAPIKey) < 20 || len(seed.Tasks) != 1 {
		return errPhaseHarness
	}
	for _, v := range append([]string{seed.OrgID, seed.KeyID, seed.UserID, seed.ScopeID, seed.ChannelID}, seed.Actors[:]...) {
		if _, e = phaseID(v); e != nil {
			return e
		}
	}
	item, ok := seed.Tasks["primary"]
	if !ok || item.TaskID == "" || item.OperationID == "" || item.CreateRequestID == "" || item.UpstreamID == "" || item.QueryAttemptID == "" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(item.BodySHA) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(item.ProviderBodySHA) || item.IdempotencyKey == "" {
		return errPhaseHarness
	}
	org, _ := phaseID(seed.OrgID)
	keyID, _ := phaseID(seed.KeyID)
	user, _ := phaseID(seed.UserID)
	channel, _ := phaseID(seed.ChannelID)
	scope, _ := phaseID(seed.ScopeID)
	key, e := identity.VerifyKey(h.verify.WithContext(ctx), seed.PlainAPIKey, time.Now().UTC())
	if e != nil || key.ID != keyID || key.OrgID != org || key.UserID == nil || *key.UserID != user {
		return errPhaseHarness
	}
	var tk task.Task
	var st video.State
	var op video.Operation
	for _, q := range []*gorm.DB{h.verify.WithContext(ctx).Where("org_id=? AND task_id=?", org, item.TaskID).Take(&tk), h.verify.WithContext(ctx).Where("org_id=? AND task_id=?", org, item.TaskID).Take(&st), h.verify.WithContext(ctx).Where("org_id=? AND id=?", org, item.OperationID).Take(&op)} {
		if q.Error != nil {
			return q.Error
		}
	}
	if tk.APIKeyID != keyID || tk.ChannelID != channel || tk.UpstreamTaskID != item.UpstreamID || st.ProviderScopeID != scope || st.CreateRequestID != item.CreateRequestID || st.RuntimeVersion != "ark-official-generation-settlement-v2" || op.ResourceID != item.TaskID || op.Kind != "create_task" {
		return errPhaseHarness
	}
	h.seed = seed
	return nil
}

func (h *phaseAdminHarness) command(ctx context.Context, c phaseCommand) (string, map[string]any, error) {
	h.mu.Lock()
	before := h.http
	h.mu.Unlock()
	kind, data, err := h.commandInner(ctx, c)
	if err != nil {
		return kind, data, err
	}
	h.mu.Lock()
	after := h.http
	h.mu.Unlock()
	if c.Command == "api" && c.Action == "delete" {
		if after.POST != before.POST || after.GET != before.GET || after.Other != before.Other || after.DELETE != before.DELETE+1 {
			return "", nil, errPhaseHarness
		}
	} else if after != before {
		return "", nil, errPhaseHarness
	}
	management, err := h.adapter.managementProof(ctx)
	if err != nil {
		return "", nil, err
	}
	data["management"] = management
	data["network_before"], data["network_after"] = before, after
	return kind, data, nil
}

func (h *phaseAdminHarness) commandInner(ctx context.Context, c phaseCommand) (string, map[string]any, error) {
	if c.Command == "inspect" {
		return "snapshot", map[string]any{"label": c.Label}, nil
	}
	if c.Task != "primary" {
		return "", nil, errPhaseHarness
	}
	if h.e.role == "N" {
		if c.Command != "admin" {
			return "", nil, errPhaseHarness
		}
		r, e := h.adapter.oldRuntimeAdmin(ctx, c.Action)
		return "admin_result", r, e
	}
	if c.Command == "api" && c.Action == "delete" && h.e.caseID == "F05_admin_adjust_delete_then_fresh_B" {
		r, e := h.adapter.publicDelete(ctx, h.seed.PlainAPIKey)
		return "api_result", map[string]any{"action": "delete", "task": "primary", "status": r.Status, "proof": r}, e
	}
	r, race, e := h.adapter.executeAdmin(ctx, h.e.caseID, c)
	data := map[string]any{"action": r.Action, "task": "primary", "status": r.Status, "code": "", "error_classes": []string{}, "replay": r.Replay, "requests": r.Requests, "result": r}
	if race != nil {
		data["race"] = race
		return "race_result", data, e
	}
	return "admin_result", data, e
}

func (h *phaseAdminHarness) close() error {
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
	return nil
}
func (h *phaseAdminHarness) emit(id, event string, data map[string]any, proof *phaseSnapshot) error {
	h.sequence++
	leaf := fmt.Sprintf("%s-%04d-%s.json", h.e.instance, h.sequence, event)
	artifact := map[string]any{"protocol": phaseProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "cmd_id": id, "event": event, "source_role": h.e.role, "compiled_source": h.e.source, "data": data, "snapshot": proof}
	raw, e := json.Marshal(artifact)
	if e != nil {
		return e
	}
	file, e := os.OpenFile(filepath.Join(h.e.dir, leaf), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = file.Write(raw)
	if e == nil {
		e = file.Sync()
	}
	ce := file.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	sum := sha256.Sum256(raw)
	line, e := json.Marshal(map[string]any{"protocol": phaseProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "cmd_id": id, "event": event, "source_role": h.e.role, "compiled_source": h.e.source, "artifact": leaf, "artifact_sha256": hex.EncodeToString(sum[:]), "data": data})
	if e != nil {
		return e
	}
	_, e = fmt.Fprintf(os.Stdout, "%s%s\n", phaseEventPrefix, line)
	return e
}
func (h *phaseAdminHarness) roundTrip(req *http.Request) (*http.Response, error) {
	// Server only dispatches the one public Delete in the third F05 leaf. Any
	// other request is refused before transport; no official socket is opened.
	allowed := h.e.role == "C" && h.e.caseID == "F05_admin_adjust_delete_then_fresh_B" && req.Method == http.MethodDelete && req.URL.Scheme == "https" && req.URL.Host == "ark.cn-beijing.volces.com" && req.URL.User == nil && req.URL.RawQuery == "" && req.URL.Path == "/api/v3/contents/generations/tasks/"+h.seed.Tasks["primary"].UpstreamID && req.Header.Get("Authorization") == "Bearer "+h.e.token
	h.mu.Lock()
	if !allowed {
		h.http.Other++
		h.mu.Unlock()
		return nil, errPhaseHarness
	}
	h.http.DELETE++
	h.mu.Unlock()
	if req.Body != nil {
		body, e := io.ReadAll(io.LimitReader(req.Body, 1))
		_ = req.Body.Close()
		if e != nil || len(body) != 0 {
			return nil, errPhaseHarness
		}
	}
	u, e := url.Parse(h.e.mock + req.URL.RequestURI())
	if e != nil {
		return nil, e
	}
	copy := req.Clone(req.Context())
	copy.URL = u
	copy.Host = u.Host
	copy.Body = nil
	copy.ContentLength = 0
	copy.Header = make(http.Header)
	copy.Header.Set("Authorization", "Bearer "+h.e.token)
	copy.Header.Set("Content-Type", "application/json")
	copy.Header.Set("X-Phase-Handoff-Run", h.e.run)
	copy.Header.Set("X-Phase-Handoff-Case", h.e.caseID)
	copy.Header.Set("X-Phase-Handoff-Instance", h.e.instance)
	sum := sha256.Sum256(nil)
	copy.Header.Set("X-Phase-Handoff-Body-SHA256", hex.EncodeToString(sum[:]))
	return h.transport.RoundTrip(copy)
}
