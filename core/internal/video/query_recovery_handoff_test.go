package video

// This file is appended, byte-for-byte, to the fixed old and new archives.
// Do not reference S1-only model fields, private types or methods statically.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"strconv"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const queryHandoffProtocol = "query-recovery-handoff-v1"
const queryHandoffOldSource = "a34a46c0b46bee63c9509224a1b6ebbf8e3fca8c"
const queryHandoffVideoPackage = "github.com/laiai/laiai-next/core/internal/video"

type queryHandoffPersistedConsumer interface {
	ProcessPersistedEvidenceOnce(context.Context) error
}

type queryHandoffDispatchConsumer interface {
	ProcessDispatchOnce(context.Context) error
}

type queryHandoffCommand struct {
	Protocol string `json:"protocol"`
	Run      string `json:"run_id"`
	Case     string `json:"case_id"`
	Instance string `json:"instance"`
	ID       uint64 `json:"cmd_id"`
	Command  string `json:"command"`
	Group    string `json:"group,omitempty"`
	Slot     string `json:"slot,omitempty"`
	Ordinals []int  `json:"ordinals,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

func decodeQueryHandoffCommand(raw []byte) (queryHandoffCommand, error) {
	var command queryHandoffCommand
	invalid := errors.New("invalid query handoff command")
	if len(raw) == 0 || len(raw) > 4096 {
		return command, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return command, invalid
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return command, invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return command, invalid
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return command, invalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return command, invalid
	}
	allowed := map[string]bool{"protocol": true, "run_id": true, "case_id": true, "instance": true, "cmd_id": true, "command": true}
	for key := range allowed {
		if fields[key] == nil {
			return command, invalid
		}
	}
	if err := json.Unmarshal(raw, &command); err != nil || command.Protocol != queryHandoffProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(command.Run) || !regexp.MustCompile(`^P0[1-4]$`).MatchString(command.Case) || !regexp.MustCompile(`^(old|new)-[a-z0-9-]{1,32}$`).MatchString(command.Instance) || command.ID == 0 {
		return command, invalid
	}
	switch command.Command {
	case "seed":
		allowed["group"], allowed["slot"] = true, true
		if command.Group != "bad40_good2" && command.Group != "one_good" && command.Group != "one_bad" && command.Group != "one_temporary" {
			return command, invalid
		}
		if command.Slot != "primary" && command.Slot != "natural" {
			return command, invalid
		}
	case "fault_json":
		allowed["ordinals"] = true
		if command.Case != "P01" && command.Case != "P03" || command.Case == "P01" && len(command.Ordinals) != 40 || command.Case == "P03" && len(command.Ordinals) != 1 {
			return command, invalid
		}
		previous := -1
		for _, ordinal := range command.Ordinals {
			if ordinal < 0 || ordinal >= 40 || ordinal <= previous || command.Case == "P03" && ordinal != 0 {
				return command, invalid
			}
			previous = ordinal
		}
	case "schedule_fixture":
		allowed["mode"], allowed["slot"] = true, true
		if command.Mode != "due_now" && command.Mode != "park_dispatch" || command.Slot != "primary" && command.Slot != "natural" || command.Mode == "due_now" && (command.Slot == "natural" || command.Case != "P01" && command.Case != "P03") {
			return command, invalid
		}
	case "direct_apply":
		if command.Case != "P03" {
			return command, invalid
		}
	case "recover", "inspect", "stop":
	default:
		return command, invalid
	}
	for key := range fields {
		if !allowed[key] {
			return command, invalid
		}
	}
	return command, nil
}

func queryHandoffPrivateType(pkg, name, wanted string) bool {
	return pkg == queryHandoffVideoPackage && name == wanted
}

// Never call Error() or generically follow Unwrap on an unknown error. A
// wrapper's own meaning may be a DB/authority failure even when it contains
// an otherwise expected decoder error. The two newer private types are
// identified by exact Go package and type, without making old source import
// them. Reflection only reads the decoder's fixed numeric discriminant.
func queryHandoffErrorClasses(err error) []string {
	if err == nil {
		return nil
	}
	remaining := 128
	var out []string
	var visit func(error)
	visit = func(current error) {
		remaining--
		if remaining < 0 {
			out = append(out, "tree_limit")
			return
		}
		if current == nil {
			out = append(out, "nil_cause")
			return
		}
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			out = append(out, "typed_nil")
			return
		}
		switch current {
		case context.Canceled:
			out = append(out, "context_cancelled")
			return
		case context.DeadlineExceeded:
			out = append(out, "context_deadline")
			return
		case ErrUnavailable:
			out = append(out, "authority_unavailable")
			return
		case ErrConflict, videobilling.ErrConflict:
			out = append(out, "identity_or_billing_conflict")
			return
		case videoruntime.ErrUnsupported:
			out = append(out, "unsupported_contract")
			return
		}
		switch typed := current.(type) {
		case *mysqlDriver.MySQLError:
			out = append(out, fmt.Sprintf("mysql_%d", typed.Number))
			return
		case *json.SyntaxError, *json.UnmarshalTypeError:
			out = append(out, "legacy_json_decode")
			return
		}
		typ := value.Type()
		if typ.Kind() != reflect.Pointer {
			out = append(out, "unknown")
			return
		}
		typ = typ.Elem()
		if queryHandoffPrivateType(typ.PkgPath(), typ.Name(), "persistedRecoveryErrors") {
			group, ok := current.(interface{ Unwrap() []error })
			if !ok {
				out = append(out, "unknown")
				return
			}
			children := group.Unwrap()
			if len(children) == 0 || len(children) > remaining {
				out = append(out, "tree_limit")
				return
			}
			for _, child := range children {
				visit(child)
				if remaining < 0 {
					break
				}
			}
			return
		}
		if queryHandoffPrivateType(typ.PkgPath(), typ.Name(), "queryObservationDecodeError") {
			kind := value.Elem().FieldByName("kind")
			if kind.IsValid() && kind.Kind() == reflect.Uint8 && (kind.Uint() == 1 || kind.Uint() == 2) {
				out = append(out, "query_decode")
			} else {
				out = append(out, "invalid_decode_kind")
			}
			return
		}
		if queryHandoffPrivateType(typ.PkgPath(), typ.Name(), "authorityClockError") {
			out = append(out, "authority_failure")
			return
		}
		out = append(out, "unknown")
	}
	visit(err)
	return out
}

func queryHandoffExpectedCycleClasses(classes []string) bool {
	for _, class := range classes {
		if class != "query_decode" && class != "context_deadline" {
			return false
		}
	}
	return true
}

func TestQueryRecoveryHandoffProtocol(t *testing.T) {
	base := `{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P01","instance":"old-writer","cmd_id":1,"command":"recover"}`
	if command, err := decodeQueryHandoffCommand([]byte(base)); err != nil || command.ID != 1 {
		t.Fatal("valid query handoff command rejected")
	}
	for _, raw := range []string{
		base + `{}`, `[]`, `null`,
		`{"protocol":"query-recovery-handoff-v1","protocol":"query-recovery-handoff-v1"}`,
		`{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P01","instance":"old-writer","cmd_id":1.0,"command":"recover"}`,
		`{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P01","instance":"old-writer","cmd_id":1,"command":"recover","sql":"UPDATE tasks"}`,
		`{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P04","instance":"new-writer","cmd_id":1,"command":"schedule_fixture","mode":"due_now","slot":"natural"}`,
		`{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P01","instance":"old-writer","cmd_id":1,"command":"fault_json","ordinals":[1,1]}`,
		`{"protocol":"query-recovery-handoff-v1","run_id":"0123456789abcdef","case_id":"P01","instance":"old-writer","cmd_id":1,"command":"recover","mode":null}`,
	} {
		if _, err := decodeQueryHandoffCommand([]byte(raw)); err == nil {
			t.Fatal("invalid or capability-expanding command accepted")
		}
	}
}

// Set by the audited compiler for each real archive. A runtime environment
// value cannot turn an uncommitted development build into a fixed release.
var queryHandoffCompiledSource string

var errQueryHandoff = errors.New("isolated query recovery handoff failed")

type queryHandoffSettings struct {
	run, caseID, instance, role, source, dir, dsn, mock, token string
}
type queryHandoffTask struct {
	Ordinal        int    `json:"ordinal"`
	Slot           string `json:"slot"`
	OrgIndex       int    `json:"org_index"`
	TaskID         string `json:"task_id"`
	OperationID    string `json:"create_operation_id"`
	RequestBodySHA string `json:"request_body_sha256"`
}
type queryHandoffSeed struct {
	Protocol string             `json:"protocol"`
	Run      string             `json:"run_id"`
	Case     string             `json:"case_id"`
	Orgs     [2]uint64          `json:"orgs"`
	Keys     [2]uint64          `json:"keys"`
	Scope    uint64             `json:"scope"`
	Channel  uint64             `json:"channel"`
	Faulted  bool               `json:"faulted"`
	Tasks    []queryHandoffTask `json:"tasks"`
}
type queryHandoffHTTP struct {
	Post   int `json:"post"`
	Get    int `json:"get"`
	Delete int `json:"delete"`
	Other  int `json:"other"`
}
type queryHandoffHarness struct {
	e                queryHandoffSettings
	db, verify       *gorm.DB
	pool, verifyPool *sql.DB
	s                *Service
	seed             queryHandoffSeed
	seq, completed   uint64
	http             queryHandoffHTTP
	ordinal          int
	seeding          bool
	transport        *http.Transport
	columns, primary map[string][]string
}

func queryHandoffEnvironment() (queryHandoffSettings, error) {
	e := queryHandoffSettings{run: os.Getenv("ARK_QUERY_HANDOFF_RUN"), caseID: os.Getenv("ARK_QUERY_HANDOFF_CASE"), instance: os.Getenv("ARK_QUERY_HANDOFF_INSTANCE"), role: os.Getenv("ARK_QUERY_HANDOFF_ROLE"), source: os.Getenv("ARK_QUERY_HANDOFF_SOURCE"), dir: os.Getenv("ARK_QUERY_HANDOFF_DIR"), dsn: os.Getenv("ARK_QUERY_HANDOFF_DSN"), mock: os.Getenv("ARK_QUERY_HANDOFF_MOCK_URL"), token: os.Getenv("ARK_QUERY_HANDOFF_MOCK_TOKEN")}
	keys := map[string]bool{}
	for _, key := range strings.Fields("ARK_QUERY_HANDOFF_PROTOCOL ARK_QUERY_HANDOFF_RUN ARK_QUERY_HANDOFF_CASE ARK_QUERY_HANDOFF_INSTANCE ARK_QUERY_HANDOFF_ROLE ARK_QUERY_HANDOFF_SOURCE ARK_QUERY_HANDOFF_DIR ARK_QUERY_HANDOFF_DSN ARK_QUERY_HANDOFF_MOCK_URL ARK_QUERY_HANDOFF_MOCK_TOKEN") {
		keys[key] = true
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ARK_QUERY_HANDOFF_") && !keys[key] {
			return e, errQueryHandoff
		}
	}
	if os.Getenv("ARK_QUERY_HANDOFF_PROTOCOL") != queryHandoffProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(e.run) || !regexp.MustCompile(`^P0[1-4]$`).MatchString(e.caseID) || !regexp.MustCompile(`^(old|new)-[a-z0-9-]{1,32}$`).MatchString(e.instance) || !strings.HasPrefix(e.instance, e.role+"-") || (e.role != "old" && e.role != "new") || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(e.source) || e.source != queryHandoffCompiledSource || (e.role == "old") != (e.source == queryHandoffOldSource) {
		return e, errQueryHandoff
	}
	u, err := url.Parse(e.mock)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || len(e.token) < 24 || len(e.token) > 128 || strings.ContainsAny(e.token, "\r\n") || !filepath.IsAbs(e.dir) {
		return e, errQueryHandoff
	}
	info, err := os.Lstat(e.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return e, errQueryHandoff
	}
	return e, nil
}

func TestMySQLQueryRecoveryBinaryHandoffProcess(t *testing.T) {
	if os.Getenv("ARK_QUERY_HANDOFF_PROTOCOL") == "" {
		t.Skip("dedicated fixed-source query recovery binary runner required")
	}
	e, err := queryHandoffEnvironment()
	if err != nil {
		t.Fatal("invalid isolated query handoff environment")
	}
	h := &queryHandoffHarness{e: e, ordinal: -1, columns: map[string][]string{}, primary: map[string][]string{}}
	closed := false
	defer func() {
		if !closed {
			if h.close() != nil {
				t.Error("query handoff emergency close failed")
			}
		}
	}()
	if h.initialize() != nil {
		t.Fatal("query handoff initialization failed")
	}
	_, persisted := any(h.s).(queryHandoffPersistedConsumer)
	_, dispatch := any(h.s).(queryHandoffDispatchConsumer)
	if persisted != (e.role == "new") || dispatch != (e.role == "new") {
		t.Fatal("compiled source role does not match available production entry points")
	}
	cfg, _ := mysqlDriver.ParseDSN(e.dsn)
	h.emit(0, "ready", map[string]any{"schema": cfg.DBName, "migration_max": 135, "has_persisted_consumer": persisted, "has_dispatch_consumer": dispatch, "task_count": len(h.seed.Tasks)})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4097)
	for scanner.Scan() {
		c, err := decodeQueryHandoffCommand(scanner.Bytes())
		if err != nil || c.Run != e.run || c.Case != e.caseID || c.Instance != e.instance || c.ID != h.completed+1 {
			t.Fatal("invalid query handoff command")
		}
		if c.Command == "stop" {
			h.completed = c.ID
			h.emit(c.ID, "command_done", map[string]any{"command": "stop", "completed_commands": h.completed})
			if h.close() != nil {
				t.Fatal("query handoff drain failed")
			}
			closed = true
			h.emit(c.ID, "drained", map[string]any{"completed_commands": h.completed})
			return
		}
		kind, details, err := h.command(c)
		if err != nil {
			t.Fatal("query handoff command failed")
		}
		snapshot, err := h.inspect()
		if err != nil {
			t.Fatal("query handoff observation failed")
		}
		artifact := map[string]any{"protocol": queryHandoffProtocol, "run_id": e.run, "case_id": e.caseID, "instance": e.instance, "role": e.role, "source": e.source, "cmd_id": c.ID, "kind": kind, "details": details, "snapshot": snapshot}
		leaf := fmt.Sprintf("%s-%04d-%s.json", e.instance, c.ID, kind)
		hash, err := h.writeJSON(leaf, artifact, false)
		if err != nil {
			t.Fatal("query handoff evidence write failed")
		}
		h.emit(c.ID, kind, map[string]any{"artifact": leaf, "sha256": hash})
		h.completed = c.ID
		h.emit(c.ID, "command_done", map[string]any{"command": c.Command, "completed_commands": h.completed})
	}
	t.Fatal("query handoff ended without a completed stop")
}

func (h *queryHandoffHarness) initialize() error {
	cfg, err := mysqlDriver.ParseDSN(h.e.dsn)
	if err != nil || cfg.Net != "tcp" || !regexp.MustCompile(`^ark_test_qhandoff_[0-9a-f]{24}$`).MatchString(cfg.DBName) || !cfg.ParseTime || cfg.Loc != time.UTC {
		return errQueryHandoff
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	ip := net.ParseIP(host)
	if err != nil || port != "3306" || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errQueryHandoff
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
			pool.Close()
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
	var version string
	if err = h.verify.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil || version != "0135_video_query_settlement_shape" {
		return errQueryHandoff
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
	if err = h.s.ConfigureResponseInbox(cfg.FormatDSN(), false); err != nil {
		return err
	}
	h.s.MutationResponseInboxEnabled = false
	h.transport = &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 10 * time.Second}
	h.s.Client = &http.Client{Transport: roundTripFunc(h.roundTrip)}
	leaf := filepath.Join(h.e.dir, "seed-manifest.json")
	if info, e := os.Lstat(leaf); e == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 65536 {
			return errQueryHandoff
		}
		raw, e := os.ReadFile(leaf)
		if e != nil {
			return e
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&h.seed) != nil || h.seed.Protocol != queryHandoffProtocol || h.seed.Run != h.e.run || h.seed.Case != h.e.caseID || len(h.seed.Tasks) > 42 {
			return errQueryHandoff
		}
		for i, x := range h.seed.Tasks {
			if x.Ordinal != i || x.OrgIndex < 0 || x.OrgIndex > 1 || x.TaskID == "" || x.OperationID == "" {
				return errQueryHandoff
			}
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}

func (h *queryHandoffHarness) close() error {
	var errs []error
	if h.transport != nil {
		h.transport.CloseIdleConnections()
	}
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
			return errQueryHandoff
		}
	}
	return nil
}
func (h *queryHandoffHarness) emit(id uint64, event string, data any) {
	h.seq++
	raw, err := json.Marshal(map[string]any{"protocol": queryHandoffProtocol, "run_id": h.e.run, "case_id": h.e.caseID, "instance": h.e.instance, "role": h.e.role, "source": h.e.source, "pid": os.Getpid(), "seq": h.seq, "cmd_id": id, "event": event, "data": data})
	if err != nil {
		panic("query handoff event encoding failed")
	}
	fmt.Fprintf(os.Stdout, "QUERY_HANDOFF_EVENT %s\n", raw)
}
func queryHandoffHash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (h *queryHandoffHarness) writeJSON(leaf string, value any, replace bool) (string, error) {
	if filepath.Base(leaf) != leaf || strings.ContainsAny(leaf, "/\\") {
		return "", errQueryHandoff
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	path := filepath.Join(h.e.dir, leaf)
	if info, e := os.Lstat(path); e == nil {
		if !replace || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", errQueryHandoff
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (h *queryHandoffHarness) saveSeed() error {
	_, err := h.writeJSON("seed-manifest.json", h.seed, true)
	return err
}

func (h *queryHandoffHarness) roundTrip(req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodPost:
		h.http.Post++
	case http.MethodGet:
		h.http.Get++
	case http.MethodDelete:
		h.http.Delete++
	default:
		h.http.Other++
	}
	if !h.seeding || h.ordinal < 0 || req.URL.Scheme != "https" || req.URL.Host != "ark.cn-beijing.volces.com" || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer "+h.e.token {
		return nil, errQueryHandoff
	}
	path := "/api/v3/contents/generations/tasks"
	id := fmt.Sprintf("qhandoff_%s_%s_%d", h.e.run, h.e.caseID, h.ordinal)
	if !(req.Method == http.MethodPost && req.URL.Path == path) && !(req.Method == http.MethodGet && req.URL.Path == path+"/"+id) {
		return nil, errQueryHandoff
	}
	copy := req.Clone(req.Context())
	if req.Method == http.MethodPost {
		if req.Body == nil || h.ordinal >= len(h.seed.Tasks) {
			return nil, errQueryHandoff
		}
		body, e := io.ReadAll(io.LimitReader(req.Body, 1048577))
		closeErr := req.Body.Close()
		if e != nil || closeErr != nil || len(body) == 0 || len(body) > 1048576 {
			return nil, errQueryHandoff
		}
		sum := sha256.Sum256(body)
		h.seed.Tasks[h.ordinal].RequestBodySHA = hex.EncodeToString(sum[:])
		copy.Body = io.NopCloser(bytes.NewReader(body))
		copy.ContentLength = int64(len(body))
	}
	u, err := url.Parse(h.e.mock + req.URL.EscapedPath())
	if err != nil {
		return nil, err
	}
	copy.URL = u
	copy.Host = u.Host
	copy.Header = make(http.Header)
	copy.Header.Set("Content-Type", "application/json")
	copy.Header.Set("Authorization", "Bearer "+h.e.token)
	copy.Header.Set("X-Query-Handoff-Run", h.e.run)
	copy.Header.Set("X-Query-Handoff-Case", h.e.caseID)
	copy.Header.Set("X-Query-Handoff-Instance", h.e.instance)
	copy.Header.Set("X-Query-Handoff-Ordinal", strconv.Itoa(h.ordinal))
	return h.transport.RoundTrip(copy)
}

func (h *queryHandoffHarness) seedConfiguration(ctx context.Context) error {
	if h.seed.Protocol != "" {
		return nil
	}
	var n int64
	if err := h.db.WithContext(ctx).Model(&task.Task{}).Count(&n).Error; err != nil || n != 0 {
		return errQueryHandoff
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	h.seed = queryHandoffSeed{Protocol: queryHandoffProtocol, Run: h.e.run, Case: h.e.caseID, Tasks: []queryHandoffTask{}}
	ciphertext, err := h.s.Keyring.Encrypt(h.e.token)
	if err != nil {
		return err
	}
	channel := relay.Channel{Name: "Synthetic query handoff", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", CredentialEnc: ciphertext, Models: ark.Model20, Weight: 1, Status: 1, MaxConcurrentTasks: 100}
	if err = h.db.WithContext(ctx).Create(&channel).Error; err != nil {
		return err
	}
	scope := ProviderScope{ScopeKey: "qh-" + h.e.run, AccountRef: "synthetic-query-handoff", Region: "cn-beijing", ProjectName: "default", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "query-handoff-v1", FXObservedAt: now}
	if err = h.db.WithContext(ctx).Create(&scope).Error; err != nil {
		return err
	}
	h.seed.Scope = scope.ID
	h.seed.Channel = channel.ID
	limit := decimal.NewFromInt(1000000)
	if err = h.db.WithContext(ctx).Create(&videobilling.QuotaPolicy{Scope: videobilling.ScopeProvider, OwnerID: scope.ID, Metric: videobilling.MetricCNY, Period: videobilling.PeriodLifetime, Timezone: "Asia/Shanghai", LimitAmount: &limit, Version: "query-handoff-v1", Enabled: true}).Error; err != nil {
		return err
	}
	capability := ark.Capability{PublicModel: ark.Model20, UpstreamModel: "doubao-seedance-2-0-260128", Version: "query-handoff-v1", Enabled: true, Verified: true, Resolutions: []string{"720p"}, Ratios: []string{"16:9"}, MinDuration: 4, MaxDuration: 15, DefaultDuration: 5, DefaultResolution: "720p", DefaultRatio: "16:9", DefaultSeed: -1, DefaultExecutionExpiresAfter: 172800, MinExecutionExpiresAfter: 3600, MaxExecutionExpiresAfter: 259200, FPS: 24, MaxContentItems: 4}
	raw, err := json.Marshal(capability)
	if err != nil {
		return err
	}
	if err = h.db.WithContext(ctx).Create(&ModelConfig{PublicModel: ark.Model20, Version: "query-handoff-v1", ProviderScopeID: scope.ID, ChannelID: channel.ID, Enabled: true, CapabilityJSON: string(raw), PricingVersion: "query-handoff-v1", NoChargeStatuses: "failed,cancelled", BoundsJSON: `{"720p/16:9":{"width":1280,"height":720}}`, VerifiedAt: &now, UpdatedAt: now}).Error; err != nil {
		return err
	}
	if err = h.db.WithContext(ctx).Create(&videobilling.PricingRule{RuleID: "query-handoff-v1", Version: "query-handoff-v1", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: ark.Model20, Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "query-handoff-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: now.Add(-time.Hour), Enabled: true}).Error; err != nil {
		return err
	}
	for i := range h.seed.Orgs {
		org := identity.Organization{Slug: fmt.Sprintf("qh-%s-%d", h.e.run, i), Name: "Synthetic query handoff", Group: "default", Status: 1}
		if err = h.db.WithContext(ctx).Create(&org).Error; err != nil {
			return err
		}
		h.seed.Orgs[i] = org.ID
		if err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			_, key, e := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "query handoff", Scopes: "video:*"})
			if e != nil {
				return e
			}
			h.seed.Keys[i] = key.ID
			a, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
			if e != nil {
				return e
			}
			_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: fmt.Sprintf("qh-credit-%d", i), AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100)})
			return e
		}); err != nil {
			return err
		}
		if err = h.db.WithContext(ctx).Create(&Policy{OrgID: org.ID, AsyncExecutionAllowed: true, CaptureHistory: true, RetentionDays: 30, UpdatedAt: now}).Error; err != nil {
			return err
		}
	}
	return h.saveSeed()
}

func (h *queryHandoffHarness) seedPending(c queryHandoffCommand) (map[string]any, error) {
	wantGroup := map[string]string{"P01": "bad40_good2", "P02": "one_good", "P03": "one_bad", "P04": "one_temporary"}[h.e.caseID]
	if c.Group != wantGroup || c.Slot == "natural" && (h.e.caseID != "P04" || h.e.role != "new" || len(h.seed.Tasks) != 1) || c.Slot == "primary" && len(h.seed.Tasks) != 0 {
		return nil, errQueryHandoff
	}
	if h.e.caseID == "P01" && h.e.role != "old" || h.e.caseID != "P01" && h.e.role != "new" {
		return nil, errQueryHandoff
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := h.seedConfiguration(ctx); err != nil {
		return nil, err
	}
	beforeHTTP := h.http
	failures := 0
	insertCount := 0
	omits := true
	current := map[string]bool{}
	for _, x := range h.seed.Tasks {
		current[x.TaskID] = true
	}
	const hook = "test:query-handoff-pending"
	if err := h.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		e, ok := tx.Statement.Dest.(*Event)
		if !ok || tx.Statement.Table != "video_events" || !current[e.TaskID] || e.Type != "task.status_changed" || e.ToStatus != "succeeded" {
			return
		}
		if e.OrgID != h.seed.Orgs[0] && e.OrgID != h.seed.Orgs[1] {
			return
		}
		failures++
		failure := tx.Exec("INSERT INTO video_events (query_handoff_missing_column) VALUES (1)").Error
		var driver *mysqlDriver.MySQLError
		if !errors.As(failure, &driver) || driver.Number != 1054 {
			tx.AddError(errQueryHandoff)
			return
		}
		tx.AddError(failure)
	}); err != nil {
		return nil, err
	}
	defer h.db.Callback().Create().Remove(hook)
	if err := h.db.Callback().Create().After("gorm:create").Register(hook+":insert", func(tx *gorm.DB) {
		if tx.Statement.Table != "video_query_receipts" {
			return
		}
		insertCount++
		// Inspect the actual emitted INSERT column list in both binaries.
		text := strings.ToLower(tx.Statement.SQL.String())
		prefix, _, found := strings.Cut(text, " values ")
		if tx.Error != nil || !found || strings.Contains(prefix, "recovery_due_at") || strings.Contains(prefix, "recovery_version") {
			omits = false
		}
	}); err != nil {
		return nil, err
	}
	defer h.db.Callback().Create().Remove(hook + ":insert")
	count := 1
	if c.Group == "bad40_good2" {
		count = 42
	}
	ordinals := make([]int, 0, count)
	for i := 0; i < count; i++ {
		ordinal := len(h.seed.Tasks)
		orgIndex := 0
		if c.Group == "bad40_good2" {
			orgIndex = ordinal % 2
		}
		var key identity.APIKey
		if err := h.db.WithContext(ctx).Where("id = ? AND org_id = ?", h.seed.Keys[orgIndex], h.seed.Orgs[orgIndex]).Take(&key).Error; err != nil {
			return nil, err
		}
		request := fmt.Sprintf("qh-%s-%d", h.e.caseID, ordinal)
		view, replay, err := h.s.Create(ctx, &key, request, request, ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "synthetic query recovery handoff"}}})
		if err != nil || replay {
			return nil, errQueryHandoff
		}
		var op Operation
		if err = h.db.WithContext(ctx).Where("org_id = ? AND resource_id = ? AND kind = ?", key.OrgID, view.ID, "create_task").Take(&op).Error; err != nil {
			return nil, err
		}
		item := queryHandoffTask{Ordinal: ordinal, Slot: c.Slot, OrgIndex: orgIndex, TaskID: view.ID, OperationID: op.ID}
		h.seed.Tasks = append(h.seed.Tasks, item)
		current[view.ID] = true
		if err = h.makeRatesDue(ctx); err != nil {
			return nil, err
		}
		funds, err := h.financialHash()
		if err != nil {
			return nil, err
		}
		httpBefore := h.http
		hitsBefore := failures
		h.seeding = true
		h.ordinal = ordinal
		if consumer, ok := any(h.s).(queryHandoffDispatchConsumer); ok {
			err = consumer.ProcessDispatchOnce(ctx)
		} else {
			err = h.s.ProcessOnce(ctx)
		}
		h.seeding = false
		h.ordinal = -1
		if err != nil || h.http.Post != httpBefore.Post+1 || h.http.Get != httpBefore.Get+1 || failures <= hitsBefore {
			return nil, errQueryHandoff
		}
		after, err := h.financialHash()
		if err != nil || after != funds {
			return nil, errQueryHandoff
		}
		var receipt QueryReceipt
		if err = h.db.WithContext(ctx).Where("org_id = ? AND task_id = ?", key.OrgID, view.ID).Take(&receipt).Error; err != nil {
			return nil, err
		}
		if receipt.AppliedAt != nil || receipt.PayloadRef == "" || receipt.ObservationJSON == "" || receipt.Outcome != "accepted" || receipt.HTTPStatus != 200 {
			return nil, errQueryHandoff
		}
		var st State
		if err = h.db.WithContext(ctx).Where("org_id = ? AND task_id = ?", key.OrgID, view.ID).Take(&st).Error; err != nil {
			return nil, err
		}
		if st.QuerySequence != 1 || st.AppliedQuerySequence != 0 || st.QueryLeaseUntil == nil {
			return nil, errQueryHandoff
		}
		if err = h.db.WithContext(ctx).Where("org_id = ? AND id = ?", key.OrgID, op.ID).Take(&op).Error; err != nil || op.Status != "confirmed" || op.ReceiptAppliedAt == nil {
			return nil, errQueryHandoff
		}
		if err = h.parkTask(ctx, item); err != nil {
			return nil, err
		}
		if err = h.saveSeed(); err != nil {
			return nil, err
		}
		ordinals = append(ordinals, ordinal)
	}
	if !omits || insertCount != count {
		return nil, errQueryHandoff
	}
	return map[string]any{"group": c.Group, "slot": c.Slot, "ordinals": ordinals, "post_delta": h.http.Post - beforeHTTP.Post, "get_delta": h.http.Get - beforeHTTP.Get, "event_sql_failures": failures, "receipt_insert_omits_progress": omits, "financial_rollback_verified": true}, nil
}

var queryHandoffFinanceTables = []string{"ledger_accounts", "ledger_holds", "ledger_journal", "ledger_operations", "ledger_operation_events", "ledger_movements", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "video_quota_reservations", "video_quota_buckets", "api_keys"}

// Every column is selected from information_schema in ordinal order. Raw
// binary cells use hex, not lossy UTF-8 conversion; DTO json:"-" tags cannot
// hide durable fields. SQL identifiers come only from the private real schema.
func (h *queryHandoffHarness) projection(tx *gorm.DB, table, where string, args []any, omit map[string]bool) ([]map[string]any, error) {
	identifier := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	if !identifier.MatchString(table) {
		return nil, errQueryHandoff
	}
	if h.columns[table] == nil {
		var columns, keys []string
		if e := tx.Raw("SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position", table).Scan(&columns).Error; e != nil {
			return nil, e
		}
		if e := tx.Raw("SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY seq_in_index", table).Scan(&keys).Error; e != nil {
			return nil, e
		}
		if len(columns) == 0 || len(keys) == 0 {
			return nil, errQueryHandoff
		}
		for _, c := range append(append([]string{}, columns...), keys...) {
			if !identifier.MatchString(c) {
				return nil, errQueryHandoff
			}
		}
		h.columns[table] = columns
		h.primary[table] = keys
	}
	columns := []string{}
	quoted := []string{}
	for _, c := range h.columns[table] {
		if !omit[c] {
			columns = append(columns, c)
			quoted = append(quoted, "`"+c+"`")
		}
	}
	query := "SELECT " + strings.Join(quoted, ",") + " FROM `" + table + "`"
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY `" + strings.Join(h.primary[table], "`,`") + "`"
	rows, err := tx.Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		if len(result) >= 30000 {
			return nil, errQueryHandoff
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, v := range values {
			switch x := v.(type) {
			case nil:
				row[columns[i]] = nil
			case []byte:
				row[columns[i]] = map[string]any{"bytes_hex": hex.EncodeToString(x)}
			case time.Time:
				row[columns[i]] = map[string]any{"utc": x.UTC().Format(time.RFC3339Nano)}
			case float64:
				return nil, errQueryHandoff
			default:
				row[columns[i]] = map[string]any{"scalar": fmt.Sprint(x)}
			}
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}
func (h *queryHandoffHarness) tableHash(tx *gorm.DB, table, where string, args ...any) (string, error) {
	rows, err := h.projection(tx, table, where, args, nil)
	if err != nil {
		return "", err
	}
	return queryHandoffHash(rows)
}
func (h *queryHandoffHarness) receiptHash(tx *gorm.DB, id string, immutable bool) (string, error) {
	omit := map[string]bool{}
	if immutable {
		for _, k := range []string{"applied_at", "apply_error_code", "recovery_due_at", "recovery_version"} {
			omit[k] = true
		}
	}
	rows, err := h.projection(tx, "video_query_receipts", "attempt_id = ?", []any{id}, omit)
	if err != nil || len(rows) != 1 {
		return "", errQueryHandoff
	}
	return queryHandoffHash(rows)
}
func (h *queryHandoffHarness) financialTx(tx *gorm.DB) (string, error) {
	facts := map[string]any{}
	for _, table := range queryHandoffFinanceTables {
		rows, e := h.projection(tx, table, "", nil, nil)
		if e != nil {
			return "", e
		}
		facts[table] = rows
	}
	return queryHandoffHash(facts)
}
func (h *queryHandoffHarness) financialHash() (string, error) {
	var out string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := h.verify.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; out, e = h.financialTx(tx); return e }, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}
func queryHandoffTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func queryHandoffID(id uint64) string { return strconv.FormatUint(id, 10) }

func (h *queryHandoffHarness) inspect() (map[string]any, error) {
	out := map[string]any{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := h.verify.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var now time.Time
		if e := tx.Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error; e != nil {
			return e
		}
		out["database_utc"] = now.UTC().Format(time.RFC3339Nano)
		out["task_count"] = len(h.seed.Tasks)
		out["http"] = h.http
		finance, e := h.financialTx(tx)
		if e != nil {
			return e
		}
		out["financial_sha256"] = finance
		evidence := map[string]any{}
		for _, table := range []string{"invocation_payloads", "invocation_payload_staging", "invocation_payload_accesses"} {
			rows, e := h.projection(tx, table, "", nil, nil)
			if e != nil {
				return e
			}
			evidence[table] = rows
		}
		out["encrypted_evidence_sha256"], e = queryHandoffHash(evidence)
		if e != nil {
			return e
		}
		out["all_events_sha256"], e = h.tableHash(tx, "video_events", "")
		if e != nil {
			return e
		}
		out["outbox_sha256"], e = h.tableHash(tx, "video_outbox", "")
		if e != nil {
			return e
		}
		orgs := []map[string]any{}
		for i, org := range h.seed.Orgs {
			var a ledger.Account
			var key identity.APIKey
			if e = tx.Where("org_id = ? AND type = ? AND shard = 0", org, ledger.AccountUserCredit).Take(&a).Error; e != nil {
				return e
			}
			if e = tx.Where("org_id = ? AND id = ?", org, h.seed.Keys[i]).Take(&key).Error; e != nil {
				return e
			}
			orgs = append(orgs, map[string]any{"org_id": queryHandoffID(org), "key_id": queryHandoffID(key.ID), "posted_usd": a.PostedBalance.StringFixed(8), "held_usd": a.HeldAmount.StringFixed(8), "available_usd": a.Available().StringFixed(8), "key_used_usd": key.UsedAmount.StringFixed(8)})
		}
		out["orgs"] = orgs
		var provider struct{ Reserved, Consumed decimal.Decimal }
		if e = tx.Raw("SELECT COALESCE(SUM(reserved_amount),0) AS reserved,COALESCE(SUM(consumed_amount),0) AS consumed FROM video_quota_buckets WHERE org_id=0").Scan(&provider).Error; e != nil {
			return e
		}
		out["provider"] = map[string]any{"reserved_cny": provider.Reserved.StringFixed(8), "consumed_cny": provider.Consumed.StringFixed(8)}
		tasks := []map[string]any{}
		for _, item := range h.seed.Tasks {
			org := h.seed.Orgs[item.OrgIndex]
			var t task.Task
			var st State
			var op Operation
			var receipt QueryReceipt
			var snapshot videobilling.Snapshot
			var hold ledger.Hold
			for _, query := range []*gorm.DB{tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&t), tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&st), tx.Where("org_id = ? AND id = ? AND resource_id = ?", org, item.OperationID, item.TaskID).Take(&op), tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&receipt), tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&snapshot)} {
				if query.Error != nil {
					return query.Error
				}
			}
			if e = tx.Where("id = ?", snapshot.HoldID).Take(&hold).Error; e != nil {
				return e
			}
			var progress struct {
				RecoveryDueAt   *time.Time
				RecoveryVersion uint64
			}
			if e = tx.Table("video_query_receipts").Select("recovery_due_at,recovery_version").Where("attempt_id = ?", receipt.AttemptID).Take(&progress).Error; e != nil {
				return e
			}
			immutable, e := h.receiptHash(tx, receipt.AttemptID, true)
			if e != nil {
				return e
			}
			full, e := h.receiptHash(tx, receipt.AttemptID, false)
			if e != nil {
				return e
			}
			rp := map[string]any{"attempt_id": receipt.AttemptID, "sequence": queryHandoffID(receipt.Sequence), "task_row_version": queryHandoffID(receipt.TaskRowVersion), "outcome": receipt.Outcome, "http_status": receipt.HTTPStatus, "payload_ref": receipt.PayloadRef, "received_at": receipt.ReceivedAt.UTC().Format(time.RFC3339Nano), "applied_at": queryHandoffTime(receipt.AppliedAt), "due_at": queryHandoffTime(progress.RecoveryDueAt), "recovery_version": queryHandoffID(progress.RecoveryVersion), "error_code": receipt.ApplyErrorCode, "immutable_sha256": immutable, "full_sha256": full}
			p := map[string]any{"ordinal": item.Ordinal, "slot": item.Slot, "org_index": item.OrgIndex, "org_id": queryHandoffID(org), "key_id": queryHandoffID(h.seed.Keys[item.OrgIndex]), "task_id": item.TaskID, "create_operation_id": op.ID, "request_body_sha256": item.RequestBodySHA, "task_status": t.Status, "billing_state": st.BillingState, "receipt": rp}
			for field, table := range map[string]string{"task_sha256": "tasks", "state_sha256": "video_task_states", "operation_sha256": "video_operations", "event_sha256": "video_events", "billing_snapshot_sha256": "video_billing_snapshots"} {
				column := "task_id"
				if table == "video_operations" {
					column = "resource_id"
				}
				p[field], e = h.tableHash(tx, table, "org_id = ? AND "+column+" = ?", org, item.TaskID)
				if e != nil {
					return e
				}
			}
			input := map[string]any{}
			for _, table := range []string{"invocation_payloads", "invocation_payload_staging"} {
				rows, e := h.projection(tx, table, "payload_id = ?", []any{op.PayloadRef}, nil)
				if e != nil {
					return e
				}
				input[table] = rows
			}
			p["input_sha256"], e = queryHandoffHash(input)
			if e != nil {
				return e
			}
			var eventCount, outboxCount, journalCount int64
			if e = tx.Model(&Event{}).Where("org_id = ? AND task_id = ?", org, item.TaskID).Count(&eventCount).Error; e != nil {
				return e
			}
			if e = tx.Table("video_outbox").Where("org_id = ? AND task_id = ?", org, item.TaskID).Count(&outboxCount).Error; e != nil {
				return e
			}
			if e = tx.Model(&ledger.JournalEntry{}).Where("hold_id = ?", hold.ID).Count(&journalCount).Error; e != nil {
				return e
			}
			p["event_count"], p["outbox_count"], p["journal_count"] = eventCount, outboxCount, journalCount
			var entries []videobilling.BillingEntry
			if e = tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Order("id").Find(&entries).Error; e != nil {
				return e
			}
			captured, released, supplierCNY, supplierUSD := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
			for _, entry := range entries {
				captured = captured.Add(entry.CapturedUSD)
				released = released.Add(entry.ReleasedUSD)
				supplierCNY = supplierCNY.Add(entry.SupplierCNY)
				supplierUSD = supplierUSD.Add(entry.SupplierUSD)
			}
			var reservations []videobilling.QuotaReservation
			if e = tx.Where("org_id = ? AND task_id = ? AND supplier = 1 AND metric = ?", org, item.TaskID, videobilling.MetricCNY).Find(&reservations).Error; e != nil || len(reservations) != 1 {
				return errQueryHandoff
			}
			quota := reservations[0]
			status := map[int8]string{ledger.HoldActive: "active", ledger.HoldCaptured: "captured", ledger.HoldReleased: "released"}[hold.Status]
			if status == "" {
				return errQueryHandoff
			}
			p["money"] = map[string]any{"hold_status": status, "hold_amount_usd": hold.Amount.StringFixed(8), "hold_captured_usd": hold.CapturedAmount.StringFixed(8), "task_cost_usd": t.CostUSD.StringFixed(8), "billing_entry_count": len(entries), "captured_usd": captured.StringFixed(8), "released_usd": released.StringFixed(8), "supplier_cny": supplierCNY.StringFixed(8), "supplier_usd": supplierUSD.StringFixed(8), "quota_state": quota.State, "quota_reserved_cny": quota.ReservedAmount.StringFixed(8), "quota_actual_cny": quota.ActualAmount.StringFixed(8)}
			p["price"] = map[string]any{"retail_usd_per_1m": snapshot.RetailUSDPer1M.StringFixed(8), "supplier_cny_per_1m": snapshot.SupplierCNYPer1M.StringFixed(8), "fx_usd_per_cny": snapshot.FXUSDPerCNY.StringFixed(8), "hold_usd": snapshot.HoldUSD.StringFixed(8), "supplier_reserved_cny": snapshot.SupplierReservedCNY.StringFixed(8), "effective_ratio": snapshot.EffectiveRatio.StringFixed(8), "rule_version": snapshot.RuleVersion, "fx_version": snapshot.FXVersion}
			tasks = append(tasks, p)
		}
		out["tasks"] = tasks
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}

func (h *queryHandoffHarness) recover() (map[string]any, error) {
	before := h.http
	batches := []map[string]any{}
	invalid := false
	const hook = "test:query-handoff-candidates"
	if err := h.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		rows, ok := tx.Statement.Dest.(*[]QueryReceipt)
		if !ok {
			return
		}
		text := strings.ToLower(tx.Statement.SQL.String())
		limit, limited := tx.Statement.Clauses["LIMIT"].Expression.(clause.Limit)
		if !strings.Contains(text, "video_query_receipts") || !strings.Contains(text, "limit ") || !limited || limit.Limit == nil || *limit.Limit != 40 || limit.Offset != 0 || tx.Error != nil || len(*rows) > 40 {
			invalid = true
			return
		}
		ids := []string{}
		for _, row := range *rows {
			ids = append(ids, row.AttemptID)
		}
		batches = append(batches, map[string]any{"count": len(ids), "attempt_ids": ids})
	}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	entry := "old_combined"
	var err error
	if consumer, ok := any(h.s).(queryHandoffPersistedConsumer); ok {
		entry = "new_persisted"
		err = consumer.ProcessPersistedEvidenceOnce(ctx)
	} else {
		err = h.s.ProcessOnce(ctx)
	}
	cancel()
	removeErr := h.db.Callback().Query().Remove(hook)
	classes := queryHandoffErrorClasses(err)
	if classes == nil {
		classes = []string{}
	}
	if removeErr != nil || invalid || len(batches) != 1 || !queryHandoffExpectedCycleClasses(classes) || h.http != before {
		return nil, errQueryHandoff
	}
	return map[string]any{"entry": entry, "error_classes": classes, "query_batches": batches, "http_before": before, "http_after": h.http}, nil
}

// The old archive cannot name the new private review sentinel. This controlled
// boundary instead proves a supported, current NULL receipt was actually read
// under the original lock order and refused without any UPDATE. It never
// treats a plain string error as an identified sentinel or accepts wrappers.
func (h *queryHandoffHarness) directApply() (map[string]any, error) {
	if h.e.role != "new" || h.e.caseID != "P03" || len(h.seed.Tasks) != 1 {
		return nil, errQueryHandoff
	}
	item := h.seed.Tasks[0]
	org := h.seed.Orgs[item.OrgIndex]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var receipt QueryReceipt
	err := h.verify.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, st, e := LockBillingTaskTx(tx, org, item.TaskID)
		if e != nil {
			return e
		}
		if e = locked(tx).Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&receipt).Error; e != nil {
			return e
		}
		if e = requireQueryReceiptRuntime(st, receipt); e != nil {
			return e
		}
		var extra struct {
			RecoveryDueAt   *time.Time
			RecoveryVersion uint64
		}
		if e = tx.Table("video_query_receipts").Select("recovery_due_at,recovery_version").Where("attempt_id = ?", receipt.AttemptID).Take(&extra).Error; e != nil {
			return e
		}
		if receipt.AppliedAt != nil || extra.RecoveryDueAt != nil || extra.RecoveryVersion == 0 {
			return errQueryHandoff
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	before, err := h.inspect()
	if err != nil {
		return nil, err
	}
	beforeHTTP := h.http
	order := []string{}
	updates := 0
	receiptReads := 0
	conflictProbes := 0
	current := false
	invalid := false
	const hook = "test:query-handoff-null-barrier"
	if err = h.db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if !strings.Contains(strings.ToUpper(tx.Statement.SQL.String()), "FOR UPDATE") {
			return
		}
		switch tx.Statement.Table {
		case "tasks", "video_task_states", "video_query_receipts":
			order = append(order, tx.Statement.Table)
		}
		if tx.Statement.Table != "video_query_receipts" {
			return
		}
		if probe, ok := tx.Statement.Dest.(*struct{ AttemptID string }); ok {
			// The settlement barrier probes for a committed financial-conflict
			// marker with a bounded current read (attempt_id projection only).
			// It is not the receipt's own locked read; a probe that actually
			// returns a marker must invalidate the plain-barrier expectation.
			if probe.AttemptID != "" {
				invalid = true
				return
			}
			conflictProbes++
			return
		}
		row, ok := tx.Statement.Dest.(*QueryReceipt)
		if !ok || tx.Error != nil || row.AttemptID != receipt.AttemptID || row.OrgID != org || row.TaskID != item.TaskID {
			invalid = true
			return
		}
		field := reflect.ValueOf(row).Elem().FieldByName("RecoveryDueAt")
		if !field.IsValid() || field.Kind() != reflect.Pointer || !field.IsNil() {
			invalid = true
			return
		}
		receiptReads++
		current = true
	}); err != nil {
		return nil, err
	}
	if err = h.db.Callback().Update().Before("gorm:update").Register(hook, func(*gorm.DB) { updates++ }); err != nil {
		h.db.Callback().Query().Remove(hook)
		return nil, err
	}
	applyErr := h.s.applyQueryReceipt(ctx, receipt)
	queryRemove := h.db.Callback().Query().Remove(hook)
	updateRemove := h.db.Callback().Update().Remove(hook)
	if applyErr == nil || ctx.Err() != nil || queryRemove != nil || updateRemove != nil || invalid || !current || receiptReads != 1 || conflictProbes != 1 || updates != 0 || h.http != beforeHTTP || !reflect.DeepEqual(order, []string{"tasks", "video_task_states", "video_query_receipts", "video_query_receipts"}) {
		return nil, errQueryHandoff
	}
	if !queryHandoffPlainBarrierCause(applyErr) {
		return nil, errQueryHandoff
	}
	if !reflect.DeepEqual(queryHandoffErrorClasses(applyErr), []string{"unknown"}) {
		return nil, errQueryHandoff
	}
	after, err := h.inspect()
	if err != nil {
		return nil, err
	}
	delete(before, "database_utc")
	delete(after, "database_utc")
	if !reflect.DeepEqual(before, after) {
		return nil, errQueryHandoff
	}
	return map[string]any{"barrier_rejection_observed": true, "current_receipt_read": true, "updates_observed": 0, "error_classes": []string{"unclassified_plain_error"}, "http_before": beforeHTTP, "http_after": h.http}, nil
}

func (h *queryHandoffHarness) makeRatesDue(ctx context.Context) error {
	var before, after []ProviderRate
	if err := h.db.WithContext(ctx).Where("scope_id = ?", h.seed.Scope).Order("action").Find(&before).Error; err != nil {
		return err
	}
	if err := h.db.WithContext(ctx).Model(&ProviderRate{}).Where("scope_id = ?", h.seed.Scope).UpdateColumn("next_allowed_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)")).Error; err != nil {
		return err
	}
	if err := h.db.WithContext(ctx).Where("scope_id = ?", h.seed.Scope).Order("action").Find(&after).Error; err != nil {
		return err
	}
	if len(before) != len(after) {
		return errQueryHandoff
	}
	for i := range after {
		after[i].NextAllowedAt = before[i].NextAllowedAt
	}
	if !reflect.DeepEqual(before, after) {
		return errQueryHandoff
	}
	return nil
}
func (h *queryHandoffHarness) parkTask(ctx context.Context, x queryHandoffTask) error {
	var before, after State
	if err := h.db.WithContext(ctx).Where("org_id = ? AND task_id = ?", h.seed.Orgs[x.OrgIndex], x.TaskID).Take(&before).Error; err != nil {
		return err
	}
	q := h.db.WithContext(ctx).Model(&State{}).Where("org_id = ? AND task_id = ? AND query_sequence = 1 AND applied_query_sequence = 0", h.seed.Orgs[x.OrgIndex], x.TaskID).UpdateColumn("next_poll_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR)"))
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected != 1 {
		return errQueryHandoff
	}
	if err := h.db.WithContext(ctx).Where("org_id = ? AND task_id = ?", h.seed.Orgs[x.OrgIndex], x.TaskID).Take(&after).Error; err != nil {
		return err
	}
	after.NextPollAt = before.NextPollAt
	if !reflect.DeepEqual(before, after) {
		return errQueryHandoff
	}
	return nil
}

func queryHandoffPlainBarrierCause(err error) bool {
	for depth := 0; depth < 128; depth++ {
		if err == nil {
			return false
		}
		v := reflect.ValueOf(err)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			return false
		}
		typ := v.Type().Elem()
		if typ.PkgPath() == "errors" && typ.Name() == "errorString" {
			return reflect.DeepEqual(queryHandoffErrorClasses(err), []string{"unknown"})
		}
		if !queryHandoffPrivateType(typ.PkgPath(), typ.Name(), "persistedRecoveryErrors") {
			return false
		}
		group, ok := err.(interface{ Unwrap() []error })
		if !ok {
			return false
		}
		children := group.Unwrap()
		if len(children) != 1 {
			return false
		}
		err = children[0]
	}
	return false
}

func (h *queryHandoffHarness) command(c queryHandoffCommand) (string, any, error) {
	if c.Command == "seed" {
		details, err := h.seedPending(c)
		return "seeded", details, err
	}
	if len(h.seed.Tasks) == 0 {
		return "", nil, errQueryHandoff
	}
	switch c.Command {
	case "inspect":
		return "snapshot", map[string]any{}, nil
	case "recover":
		details, err := h.recover()
		return "cycle", details, err
	case "direct_apply":
		details, err := h.directApply()
		return "direct_result", details, err
	case "fault_json":
		if h.seed.Faulted {
			return "", nil, errQueryHandoff
		}
		funds, err := h.financialHash()
		if err != nil {
			return "", nil, err
		}
		changes := []map[string]any{}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, ordinal := range c.Ordinals {
				if ordinal >= len(h.seed.Tasks) {
					return errQueryHandoff
				}
				item := h.seed.Tasks[ordinal]
				org := h.seed.Orgs[item.OrgIndex]
				var r QueryReceipt
				if e := tx.Where("org_id = ? AND task_id = ?", org, item.TaskID).Take(&r).Error; e != nil {
					return e
				}
				if r.AppliedAt != nil {
					return errQueryHandoff
				}
				before, e := h.receiptHash(tx, r.AttemptID, true)
				if e != nil {
					return e
				}
				unchanged, e := h.projection(tx, "video_query_receipts", "attempt_id = ?", []any{r.AttemptID}, map[string]bool{"observation_json": true})
				if e != nil {
					return e
				}
				q := tx.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ? AND applied_at IS NULL", org, item.TaskID, r.AttemptID).UpdateColumn("observation_json", `{"completion_tokens":"invalid-handoff-type"}`)
				if q.Error != nil {
					return q.Error
				}
				if q.RowsAffected != 1 {
					return errQueryHandoff
				}
				current, e := h.projection(tx, "video_query_receipts", "attempt_id = ?", []any{r.AttemptID}, map[string]bool{"observation_json": true})
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(unchanged, current) {
					return errQueryHandoff
				}
				after, e := h.receiptHash(tx, r.AttemptID, true)
				if e != nil {
					return e
				}
				if before == after {
					return errQueryHandoff
				}
				changes = append(changes, map[string]any{"ordinal": ordinal, "attempt_id": r.AttemptID, "before_immutable_sha256": before, "after_immutable_sha256": after})
			}
			return nil
		})
		if err != nil {
			return "", nil, err
		}
		h.seed.Faulted = true
		if err = h.saveSeed(); err != nil {
			return "", nil, err
		}
		after, err := h.financialHash()
		if err != nil || after != funds {
			return "", nil, errQueryHandoff
		}
		return "fault_applied", map[string]any{"ordinals": c.Ordinals, "allowed_columns": []string{"observation_json"}, "changes": changes, "financial_before_sha256": funds}, nil
	case "schedule_fixture":
		funds, err := h.financialHash()
		if err != nil {
			return "", nil, err
		}
		ordinals := []int{}
		allowed := []string{"recovery_due_at"}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, x := range h.seed.Tasks {
			if x.Slot != c.Slot {
				continue
			}
			ordinals = append(ordinals, x.Ordinal)
			if c.Mode == "park_dispatch" {
				err = h.parkTask(ctx, x)
				allowed = []string{"video_task_states.next_poll_at", "video_provider_rates.next_allowed_at"}
			} else {
				before, e := h.projection(h.db.WithContext(ctx), "video_query_receipts", "org_id = ? AND task_id = ?", []any{h.seed.Orgs[x.OrgIndex], x.TaskID}, map[string]bool{"recovery_due_at": true})
				if e != nil {
					return "", nil, e
				}
				q := h.db.WithContext(ctx).Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND applied_at IS NULL AND recovery_due_at IS NOT NULL", h.seed.Orgs[x.OrgIndex], x.TaskID).UpdateColumn("recovery_due_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND)"))
				err = q.Error
				if err == nil && q.RowsAffected != 1 {
					err = errQueryHandoff
				}
				if err == nil {
					after, e := h.projection(h.db.WithContext(ctx), "video_query_receipts", "org_id = ? AND task_id = ?", []any{h.seed.Orgs[x.OrgIndex], x.TaskID}, map[string]bool{"recovery_due_at": true})
					if e != nil {
						err = e
					} else if !reflect.DeepEqual(before, after) {
						err = errQueryHandoff
					}
				}
			}
			if err != nil {
				return "", nil, err
			}
		}
		if len(ordinals) == 0 {
			return "", nil, errQueryHandoff
		}
		if c.Mode == "park_dispatch" {
			if err = h.makeRatesDue(ctx); err != nil {
				return "", nil, err
			}
		}
		after, err := h.financialHash()
		if err != nil || after != funds {
			return "", nil, errQueryHandoff
		}
		return "scheduled", map[string]any{"mode": c.Mode, "slot": c.Slot, "ordinals": ordinals, "allowed_columns": allowed, "financial_before_sha256": funds}, nil
	default:
		return "", nil, errQueryHandoff
	}
}
