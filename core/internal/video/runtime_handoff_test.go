package video

// This exact file is overlaid onto a fixed, real pre-runtime source revision by
// scripts/ark-runtime-compat.sh. Keep it compilable against that revision: no
// videoruntime import, no simulated version aliases, no direct receipt inserts.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type runtimeHandoffTask struct {
	Role        string `json:"role"`
	OrgID       uint64 `json:"org_id"`
	TaskID      string `json:"task_id"`
	OperationID string `json:"operation_id"`
	AttemptID   string `json:"query_attempt_id,omitempty"`
	ReceiptRef  string `json:"receipt_ref"`
	HoldID      uint64 `json:"hold_id"`
	HoldUSD     string `json:"hold_usd"`
	CapturedUSD string `json:"captured_usd"`
	Fingerprint string `json:"pre_runtime_columns_sha256"`
}

type runtimeHandoffReport struct {
	Passed        bool                 `json:"passed"`
	Phase         string               `json:"phase"`
	Schema        string               `json:"migration_max"`
	Tasks         []runtimeHandoffTask `json:"tasks"`
	MockPOSTs     int32                `json:"mock_posts"`
	MockGETs      int32                `json:"mock_gets"`
	ProviderCalls int32                `json:"provider_calls"`
	OfficialCalls int                  `json:"official_calls"`
	Settlements   int                  `json:"new_settlements"`
	LedgerDrift   int                  `json:"ledger_drift"`
}

func TestMySQLRuntimeBinaryHandoff(t *testing.T) {
	if os.Getenv("ARK_RUNTIME_COMPAT") != "1" {
		t.Skip("explicit isolated ark-runtime-compat.sh runner required")
	}
	phase := os.Getenv("ARK_RUNTIME_COMPAT_PHASE")
	if phase != "old_before" && phase != "old_after" && phase != "new_verify" {
		t.Fatal("invalid runtime handoff phase")
	}
	root := os.Getenv("ARK_RUNTIME_COMPAT_DIR")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute private artifact directory required")
	}
	report := runtimeHandoffReport{Phase: phase}
	name := phase + ".json"
	if phase == "new_verify" {
		name = "report.json"
	}
	path := filepath.Join(root, name)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("handoff report already exists")
	}
	t.Cleanup(func() {
		report.Passed = report.Passed && !t.Failed()
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error("could not encode handoff report")
			return
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error("could not exclusively create handoff report")
			return
		}
		_, writeErr := f.Write(append(raw, '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Error("could not finish handoff report")
		}
	})
	db := runtimeHandoffDB(t)
	must(t, db.Raw("SELECT MAX(version) FROM schema_migrations").Scan(&report.Schema).Error)
	var count int64
	must(t, db.Model(&task.Task{}).Count(&count).Error)
	if phase == "old_before" {
		if count != 0 || !strings.HasPrefix(report.Schema, "0126_") {
			t.Fatal("old-before requires empty pre-expansion schema")
		}
	} else if !strings.HasPrefix(report.Schema, "0127_") {
		t.Fatal("post-expansion phase requires migration 0127")
	}
	if phase == "old_after" && count != 4 || phase == "new_verify" && count != 8 {
		t.Fatal("handoff database contains unexpected task data")
	}
	if phase == "new_verify" {
		runtimeHandoffVerify(t, db, root, &report)
	} else {
		runtimeHandoffSeed(t, &report)
	}
	drift, err := ledger.CheckDrift(db)
	must(t, err)
	report.LedgerDrift = len(drift)
	if report.LedgerDrift != 0 {
		t.Fatal("runtime handoff introduced ledger drift")
	}
	report.Passed = true
}

func runtimeHandoffDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := drivermysql.ParseDSN(os.Getenv("ARK_TEST_MYSQL_DSN"))
	if err != nil || cfg.DBName != "ark_test" || cfg.Net != "tcp" {
		t.Fatal("handoff requires the runner's isolated loopback ark_test database")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		t.Fatal("handoff refuses non-loopback database connections")
	}
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("could not open isolated handoff database")
	}
	pool, err := db.DB()
	must(t, err)
	pool.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func runtimeHandoffSeed(t *testing.T, report *runtimeHandoffReport) {
	f := mysqlFixture(t)
	ctx := context.Background()
	for _, role := range []string{"pending_create", "pending_query", "submission_unknown", "settled"} {
		f.now = f.now.Add(2 * time.Second) // Advance the existing scope gate, no sleep.
		v := f.create(t, "handoff-"+role)
		upstream := "up_" + v.ID
		f.response = func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				if role == "submission_unknown" {
					return nil, io.ErrUnexpectedEOF
				}
				return reply(200, fmt.Sprintf(`{"id":%q}`, upstream)), nil
			}
			if req.Method != http.MethodGet {
				t.Fatal("unexpected mock provider method")
			}
			return reply(200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100,"total_tokens":999},"content":{"video_url":"https://cdn.example.com/handoff.mp4"}}`, upstream)), nil
		}
		var op Operation
		must(t, f.s.DB.Where("org_id = ? AND resource_id = ? AND kind = ?", f.key.OrgID, v.ID, "create_task").First(&op).Error)
		injected := errors.New("synthetic handoff apply interruption")
		name := "test:handoff:" + role
		if role == "pending_create" || role == "pending_query" {
			must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
				if role == "pending_create" && tx.Statement.Table == "video_upstream_bindings" || role == "pending_query" && tx.Statement.Table == "video_billing_entries" {
					tx.AddError(injected)
				}
			}))
		}
		err := f.s.processOperation(ctx, op)
		if role == "pending_create" {
			if !errors.Is(err, injected) {
				t.Fatal("old create apply interruption was not exercised")
			}
		} else {
			must(t, err)
		}
		if role == "pending_query" || role == "settled" {
			err = f.s.pollTask(ctx, f.key.OrgID, v.ID)
			if role == "pending_query" {
				if !errors.Is(err, injected) {
					t.Fatal("old query apply interruption was not exercised")
				}
			} else {
				must(t, err)
			}
		}
		if role == "pending_create" || role == "pending_query" {
			must(t, f.s.DB.Callback().Create().Remove(name))
		}
		must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&op).Error)
		hold := f.hold(t, v.ID)
		record := runtimeHandoffTask{Role: role, OrgID: f.key.OrgID, TaskID: v.ID, OperationID: op.ID, ReceiptRef: op.ReceiptRef, HoldID: hold.ID, HoldUSD: hold.Amount.String(), CapturedUSD: hold.CapturedAmount.String()}
		var row task.Task
		must(t, f.s.DB.Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).First(&row).Error)
		wantStatus := map[string]string{"pending_create": "submitting", "pending_query": "queued", "submission_unknown": "submission_unknown", "settled": "succeeded"}[role]
		if row.Status != wantStatus || op.ReceiptRef == "" {
			t.Fatal("old binary did not durably produce the expected task/receipt")
		}
		if role == "pending_create" && (op.ReceiptAppliedAt != nil || row.UpstreamTaskID != "") {
			t.Fatal("pending create was already applied")
		}
		if role == "pending_query" || role == "settled" {
			var receipt QueryReceipt
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).First(&receipt).Error)
			record.AttemptID, record.ReceiptRef = receipt.AttemptID, receipt.PayloadRef
			if receipt.PayloadRef == "" || (receipt.AppliedAt == nil) != (role == "pending_query") {
				t.Fatal("query receipt did not retain its expected apply state")
			}
		}
		if role == "settled" {
			if hold.Status != ledger.HoldCaptured || hold.CapturedAmount.String() != "0.0008" {
				t.Fatal("old binary settlement is not the fixed v1 value")
			}
		} else if hold.Status != ledger.HoldActive || !hold.CapturedAmount.IsZero() {
			t.Fatal("unfinished old task changed funds")
		}
		runtimeHandoffEvidenceExists(t, f.s.DB, record)
		record.Fingerprint = runtimeHandoffFingerprint(t, f.s.DB, record)
		report.Tasks = append(report.Tasks, record)
	}
	report.MockPOSTs, report.MockGETs = f.posts.Load(), f.gets.Load()
	report.ProviderCalls = report.MockPOSTs + report.MockGETs
	if report.MockPOSTs != 4 || report.MockGETs != 2 {
		t.Fatal("old binary repeated or skipped a provider call")
	}
}

func runtimeHandoffVerify(t *testing.T, db *gorm.DB, root string, report *runtimeHandoffReport) {
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	must(t, err)
	s, err := NewService(db, kr, []byte(strings.Repeat("h", 32)), nil)
	must(t, err)
	var calls atomic.Int32
	s.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("new handoff process forbids all provider HTTP")
	})}
	ctx := context.Background()
	for _, name := range []string{"old_before", "old_after"} {
		raw, e := os.ReadFile(filepath.Join(root, name+".json"))
		must(t, e)
		var old runtimeHandoffReport
		must(t, json.Unmarshal(raw, &old))
		if !old.Passed || old.Phase != name || len(old.Tasks) != 4 || old.MockPOSTs != 4 || old.MockGETs != 2 {
			t.Fatal("old-process evidence is incomplete")
		}
		for _, record := range old.Tasks {
			if runtimeHandoffFingerprint(t, db, record) != record.Fingerprint {
				t.Fatal("migration or overlap changed pre-existing task or financial data")
			}
			runtimeHandoffAssertV1(t, db, record)
			runtimeHandoffEvidenceExists(t, db, record)
			switch record.Role {
			case "pending_create":
				var op Operation
				must(t, db.Where("org_id = ? AND id = ?", record.OrgID, record.OperationID).First(&op).Error)
				must(t, s.applyCreateReceipt(ctx, op))
				must(t, s.applyCreateReceipt(ctx, op))
				must(t, s.processOperation(ctx, op)) // terminal op must not resend.
				must(t, db.Where("org_id = ? AND id = ?", record.OrgID, record.OperationID).First(&op).Error)
				var row task.Task
				must(t, db.Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).First(&row).Error)
				var h ledger.Hold
				must(t, db.First(&h, record.HoldID).Error)
				var bindings, entries int64
				must(t, db.Model(&UpstreamBinding{}).Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).Count(&bindings).Error)
				must(t, db.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).Count(&entries).Error)
				if op.ReceiptAppliedAt == nil || op.ReceiptRef != record.ReceiptRef || row.Status != "queued" || row.UpstreamTaskID != "up_"+record.TaskID || h.Status != ledger.HoldActive || !h.CapturedAmount.IsZero() || bindings != 1 || entries != 0 {
					t.Fatal("new process failed to recover old create exactly once")
				}
			case "pending_query":
				var receipt QueryReceipt
				must(t, db.Where("org_id = ? AND task_id = ? AND attempt_id = ?", record.OrgID, record.TaskID, record.AttemptID).First(&receipt).Error)
				must(t, s.applyQueryReceipt(ctx, receipt))
				must(t, s.applyQueryReceipt(ctx, receipt))
				must(t, db.Where("org_id = ? AND attempt_id = ?", record.OrgID, record.AttemptID).First(&receipt).Error)
				var h ledger.Hold
				must(t, db.First(&h, record.HoldID).Error)
				var entries, usage, captures int64
				must(t, db.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).Count(&entries).Error)
				must(t, db.Model(&videobilling.UsageEvidence{}).Where("org_id = ? AND task_id = ?", record.OrgID, record.TaskID).Count(&usage).Error)
				must(t, db.Model(&ledger.JournalEntry{}).Where("hold_id = ? AND delta < 0", record.HoldID).Count(&captures).Error)
				if receipt.AppliedAt == nil || receipt.PayloadRef != record.ReceiptRef || h.Status != ledger.HoldCaptured || h.CapturedAmount.String() != "0.0008" || entries != 1 || usage != 1 || captures != 1 {
					t.Fatal("old query receipt did not settle exactly once with original v1 price")
				}
				report.Settlements++
			case "submission_unknown", "settled":
				var op Operation
				must(t, db.Where("org_id = ? AND id = ?", record.OrgID, record.OperationID).First(&op).Error)
				must(t, s.applyCreateReceipt(ctx, op))
				must(t, s.processOperation(ctx, op))
				if record.Role == "settled" {
					var receipt QueryReceipt
					must(t, db.Where("org_id = ? AND attempt_id = ?", record.OrgID, record.AttemptID).First(&receipt).Error)
					must(t, s.applyQueryReceipt(ctx, receipt))
					must(t, s.applyQueryReceipt(ctx, receipt))
				}
				if runtimeHandoffFingerprint(t, db, record) != record.Fingerprint {
					t.Fatal("new binary altered historical unknown/settled facts")
				}
			default:
				t.Fatal("unknown handoff fixture role")
			}
			report.Tasks = append(report.Tasks, record)
		}
	}
	must(t, s.recoverOperations(ctx))
	must(t, s.recoverQueryReceipts(ctx))
	must(t, s.recoverOperations(ctx))
	must(t, s.recoverQueryReceipts(ctx))
	report.ProviderCalls = calls.Load()
	if report.ProviderCalls != 0 || report.Settlements != 2 {
		t.Fatal("new process repeated provider calls or lost an old receipt")
	}
	for _, record := range report.Tasks {
		if record.Role == "submission_unknown" || record.Role == "settled" {
			if runtimeHandoffFingerprint(t, db, record) != record.Fingerprint {
				t.Fatal("recovery sweep changed historical unknown/settled state")
			}
		}
	}
}

// Check actual DB defaults/migrated values. These assertions never rewrite a
// version field; the producer really was the pre-runtime binary.
func runtimeHandoffAssertV1(t *testing.T, db *gorm.DB, record runtimeHandoffTask) {
	t.Helper()
	for _, table := range []string{"video_task_states", "video_billing_snapshots", "video_operations", "video_query_receipts"} {
		key := "task_id"
		if table == "video_operations" {
			key = "resource_id"
		}
		q := db.Table(table).Where("org_id = ? AND "+key+" = ?", record.OrgID, record.TaskID)
		if table == "video_billing_snapshots" {
			q = q.Where("billing_schema_version <> ?", "usd-decimal-v1")
		} else {
			condition := "runtime_version <> ? OR adapter_version <> ? OR protocol_contract_version <> ?"
			args := []any{"ark-official-legacy-v1", "ark-v3-v1", "video-contract-v1"}
			if table == "video_task_states" {
				condition += " OR billing_schema_version <> ?"
				args = append(args, "usd-decimal-v1")
			}
			if table == "video_query_receipts" {
				condition += " OR observation_schema_version <> 1"
			}
			q = q.Where("("+condition+")", args...)
		}
		var wrong int64
		must(t, q.Count(&wrong).Error)
		if wrong != 0 {
			t.Fatal("old binary rows did not receive the explicit legacy compatibility identity")
		}
	}
}

func runtimeHandoffEvidenceExists(t *testing.T, db *gorm.DB, record runtimeHandoffTask) {
	t.Helper()
	var payload invocation.VideoPayload
	must(t, db.Where("org_id = ? AND payload_id = ?", record.OrgID, record.ReceiptRef).First(&payload).Error)
	var staging int64
	must(t, db.Model(&invocation.VideoPayloadStaging{}).Where("payload_id = ?", record.ReceiptRef).Count(&staging).Error)
	if payload.CaptureStatus != invocation.VideoCaptureStaged || staging != 1 || payload.SchemaVersion != 1 {
		t.Fatal("handoff evidence was not durably stored")
	}
}

func runtimeHandoffFingerprint(t *testing.T, db *gorm.DB, record runtimeHandoffTask) string {
	t.Helper()
	h := sha256.New()
	for _, table := range []string{"tasks", "video_task_states", "video_operations", "video_query_receipts", "video_billing_snapshots", "video_billing_entries", "video_usage_evidence", "ledger_holds", "ledger_journal"} {
		key, args := "org_id = ? AND task_id = ?", []any{record.OrgID, record.TaskID}
		if table == "video_operations" {
			key = "org_id = ? AND resource_id = ?"
		} else if table == "ledger_holds" {
			key, args = "id = ?", []any{record.HoldID}
		} else if table == "ledger_journal" {
			key, args = "hold_id = ?", []any{record.HoldID}
		}
		rows, err := db.Raw("SELECT * FROM "+table+" WHERE "+key, args...).Rows()
		must(t, err)
		columns, err := rows.Columns()
		must(t, err)
		encoded := []string{}
		for rows.Next() {
			values := make([]sql.RawBytes, len(columns))
			dest := make([]any, len(columns))
			for i := range dest {
				dest[i] = &values[i]
			}
			must(t, rows.Scan(dest...))
			projection := map[string]any{}
			for i, column := range columns {
				if column == "runtime_version" || column == "adapter_version" || column == "protocol_contract_version" || column == "billing_schema_version" || column == "observation_schema_version" {
					continue
				}
				if values[i] == nil {
					projection[column] = nil
				} else {
					projection[column] = string(values[i])
				}
			}
			raw, e := json.Marshal(projection)
			must(t, e)
			encoded = append(encoded, string(raw))
		}
		must(t, rows.Err())
		must(t, rows.Close())
		sort.Strings(encoded)
		_, _ = io.WriteString(h, table+"\x00"+strings.Join(encoded, "\x00")+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}
