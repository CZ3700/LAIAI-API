package server

// Staging-only Formal adapter. The process envelope/seed decoder lives in the
// companion adapter; these helpers always enter the actual HTTP handlers.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type phaseAdminSeed struct {
	OrgID, KeyID, ScopeID                                      uint64
	TaskID, UpstreamID, Model, CreateRequestID, QueryAttemptID string
	ActorIDs                                                   [3]uint64
}

type phaseAdminRequest struct {
	Action     string `json:"action"`
	Actor      string `json:"actor"`
	Status     int    `json:"status"`
	RequestID  string `json:"request_id"`
	BodySHA256 string `json:"body_sha256"`
}

type phaseAdminLine struct {
	SourceID, ImportID, LineID    uint64
	PayloadRef, RecordID, Version string
	ObservedAt                    time.Time
}

type phaseAdminResult struct {
	Action        string              `json:"action"`
	Status        int                 `json:"status"`
	Replay        bool                `json:"replay"`
	LineID        string              `json:"line_id,omitempty"`
	ProposalID    string              `json:"proposal_id,omitempty"`
	ApplicationID string              `json:"application_id,omitempty"`
	RequestID     string              `json:"request_id"`
	Requests      []phaseAdminRequest `json:"requests"`
}

type phaseAdminAdapter struct {
	db                     *gorm.DB
	service                *video.Service
	server                 *Server
	router                 *gin.Engine
	seed                   phaseAdminSeed
	tokens                 [3]string
	prefix                 string
	periodStart, periodEnd time.Time
	initial                *phaseAdminLine
	adjusted               bool
	mu                     sync.Mutex
	requests               []phaseAdminRequest
}

func newPhaseAdminAdapter(ctx context.Context, db *gorm.DB, service *video.Service, kr *crypto.Keyring, secret []byte, seed phaseAdminSeed, prefix string) (*phaseAdminAdapter, error) {
	if db == nil || service == nil || service.DB != db || kr == nil || len(secret) < 32 || seed.OrgID == 0 || seed.KeyID == 0 || seed.ScopeID == 0 || seed.TaskID == "" || seed.UpstreamID == "" || seed.QueryAttemptID == "" || !arkBillValidID(prefix, 40) {
		return nil, errors.New("admin_seed_invalid")
	}
	var database string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
		return nil, err
	}
	if !strings.HasPrefix(database, "ark_test_") {
		return nil, errors.New("admin_private_schema_required")
	}
	var now time.Time
	if err := db.WithContext(ctx).Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error; err != nil {
		return nil, err
	}
	if now.IsZero() {
		return nil, errors.New("admin_db_clock_invalid")
	}
	a := &phaseAdminAdapter{db: db, service: service, seed: seed, prefix: prefix, periodEnd: now.UTC()}
	a.periodStart = a.periodEnd.Add(-time.Hour)
	a.server = &Server{db: db, keyring: kr, secret: append([]byte(nil), secret...), now: time.Now, video: service}
	seen := map[uint64]bool{}
	for i, id := range seed.ActorIDs {
		if id == 0 || seen[id] {
			return nil, errors.New("admin_actor_identity_invalid")
		}
		seen[id] = true
		var u identity.User
		if err := db.WithContext(ctx).Where("id = ?", id).Take(&u).Error; err != nil {
			return nil, err
		}
		if u.Status != identity.StatusActive || u.Role != identity.PlatRoleSuperadmin {
			return nil, errors.New("admin_actor_role_invalid")
		}
		var err error
		a.tokens[i], _, err = identity.IssueSessionToken(secret, id, seed.OrgID, time.Hour, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	// No Task/State/Key/money row is manufactured here. The P-produced seed is
	// checked again by every actual handler and by the read-only oracle.
	a.router = gin.New()
	su := a.router.Group("/api/admin", a.server.adminRequired(identity.PlatRoleAdmin), a.server.requireSuperadmin())
	a.server.registerArkBillingAdmin(su)
	return a, nil
}

func phaseAdminDecode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(out); err != nil {
		return errors.New("admin_response_json_invalid")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return errors.New("admin_response_trailing_data")
	}
	return nil
}

func (a *phaseAdminAdapter) call(ctx context.Context, actor int, action, path string, body any, want int) (map[string]json.RawMessage, string, error) {
	if actor < 0 || actor >= len(a.tokens) || ctx.Err() != nil {
		return nil, "", errors.New("admin_request_invalid")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/video"+path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.tokens[actor])
	w := httptest.NewRecorder()
	a.router.ServeHTTP(w, req)
	hash := sha256.Sum256(w.Body.Bytes())
	rid := w.Header().Get("X-Request-ID")
	a.mu.Lock()
	a.requests = append(a.requests, phaseAdminRequest{Action: action, Actor: strconv.FormatUint(a.seed.ActorIDs[actor], 10), Status: w.Code, RequestID: rid, BodySHA256: hex.EncodeToString(hash[:])})
	a.mu.Unlock()
	// The response body/credential is deliberately never included in an error.
	if w.Code != want {
		return nil, rid, fmt.Errorf("admin_http_status_%d", w.Code)
	}
	if !strings.HasPrefix(rid, "abr_") || len(rid) != 36 {
		return nil, rid, errors.New("admin_request_id_missing")
	}
	var out map[string]json.RawMessage
	if err = phaseAdminDecode(w.Body.Bytes(), &out); err != nil {
		return nil, rid, err
	}
	return out, rid, nil
}

func phaseAdminID(m map[string]json.RawMessage, key string) (uint64, error) {
	var id uint64
	if err := phaseAdminDecode(m[key], &id); err != nil || id == 0 {
		return 0, errors.New("admin_response_identity_invalid")
	}
	return id, nil
}

func phaseAdminReplay(m map[string]json.RawMessage) (bool, error) {
	var replay *bool
	if err := phaseAdminDecode(m["replay"], &replay); err != nil || replay == nil {
		return false, errors.New("admin_replay_missing")
	}
	return *replay, nil
}

func (a *phaseAdminAdapter) requestSlice(start int) []phaseAdminRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]phaseAdminRequest(nil), a.requests[start:]...)
}

func (a *phaseAdminAdapter) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *phaseAdminAdapter) importBill(ctx context.Context, revision int64) (phaseAdminLine, error) {
	var line phaseAdminLine
	if revision != 1 && revision != 2 {
		return line, errors.New("admin_revision_invalid")
	}
	n, amount := int64(100), "0.00460000"
	if revision == 2 {
		n, amount = 90, "0.00414000"
	}
	line.RecordID, line.Version, line.ObservedAt = "row-"+a.prefix, fmt.Sprintf("r%d", revision), a.periodEnd
	row := arkBillSourceRow{SourceRecordID: line.RecordID, SourceVersion: line.Version, SourceRevision: revision, Granularity: "task_total", UpstreamTaskID: a.seed.UpstreamID, Model: a.seed.Model, PeriodStart: a.periodStart, PeriodEnd: a.periodEnd, CompletionTokens: &n, AmountCNY: amount, ObservedAt: line.ObservedAt}
	doc := arkBillDocument{Schema: "ark_supplier_bill_v1", ProviderScopeID: a.seed.ScopeID, OfficialBillID: "bill-" + a.prefix, SourceVersion: line.Version, Currency: "CNY", Rows: []arkBillSourceRow{row}}
	m, _, err := a.call(ctx, 0, "upload_"+line.Version, "/bill-evidence", gin.H{"source_key": a.prefix + "-" + line.Version, "document": doc}, 201)
	if err != nil {
		return line, err
	}
	var source ArkBillEvidenceSource
	if err = phaseAdminDecode(m["source"], &source); err != nil || source.ID == 0 || source.ProviderScopeID != a.seed.ScopeID || source.PayloadRef == "" {
		return line, errors.New("admin_source_identity_invalid")
	}
	line.SourceID, line.PayloadRef = source.ID, source.PayloadRef
	if _, _, err = a.call(ctx, 1, "source_review_"+line.Version, fmt.Sprintf("/bill-evidence/%d/review", source.ID), gin.H{"approved": true, "reason": "verified_official_export"}, 200); err != nil {
		return line, err
	}
	m, _, err = a.call(ctx, 0, "import_begin_"+line.Version, "/bill-imports", gin.H{"source_id": source.ID, "import_key": a.prefix + "-import-" + line.Version}, 200)
	if err != nil {
		return line, err
	}
	line.ImportID, err = phaseAdminID(m, "import_id")
	if err != nil {
		return line, err
	}
	m, _, err = a.call(ctx, 0, "import_line_"+line.Version, fmt.Sprintf("/bill-imports/%d/lines", line.ImportID), gin.H{"row_number": 1}, 200)
	if err != nil {
		return line, err
	}
	line.LineID, err = phaseAdminID(m, "line_id")
	if err != nil {
		return line, err
	}
	if _, _, err = a.call(ctx, 0, "import_complete_"+line.Version, fmt.Sprintf("/bill-imports/%d/complete", line.ImportID), gin.H{}, 200); err != nil {
		return line, err
	}
	var bound int64
	err = a.db.WithContext(ctx).Table("video_provider_bill_bindings").Where("line_id = ? AND org_id = ? AND task_id = ?", line.LineID, a.seed.OrgID, a.seed.TaskID).Count(&bound).Error
	if err != nil {
		return line, err
	}
	if bound != 1 {
		return line, errors.New("admin_import_not_bound_to_seed")
	}
	return line, nil
}

func (a *phaseAdminAdapter) prepareInitial(ctx context.Context) (phaseAdminResult, error) {
	out := phaseAdminResult{Action: "prepare_initial_100"}
	if a.initial != nil {
		return out, errors.New("admin_initial_already_prepared")
	}
	before, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if err = before.requirePendingSeed(); err != nil {
		return out, err
	}
	start := a.requestCount()
	line, err := a.importBill(ctx, 1)
	if err != nil {
		return out, err
	}
	after, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if err = phaseAdminSameMoney(before, after); err != nil {
		return out, err
	}
	for _, k := range []string{"task", "state", "receipt", "events", "outbox"} {
		if before.StorageHashes[k] != after.StorageHashes[k] {
			return out, errors.New("admin_import_changed_projection_" + k)
		}
	}
	a.initial = &line
	out.Status, out.LineID, out.Requests = 200, strconv.FormatUint(line.LineID, 10), a.requestSlice(start)
	out.RequestID = out.Requests[len(out.Requests)-1].RequestID
	if err = a.requireAudits(ctx, out.Requests); err != nil {
		return out, err
	}
	return out, nil
}

func (a *phaseAdminAdapter) settleInitial(ctx context.Context) (phaseAdminResult, error) {
	out := phaseAdminResult{Action: "settle_100"}
	if a.initial == nil {
		return out, errors.New("admin_initial_not_prepared")
	}
	start := a.requestCount()
	m, rid, err := a.call(ctx, 0, "settle_100", fmt.Sprintf("/orgs/%d/tasks/%s/settle-bill", a.seed.OrgID, a.seed.TaskID), gin.H{"line_id": a.initial.LineID}, 200)
	if err != nil {
		return out, err
	}
	out.Replay, err = phaseAdminReplay(m)
	if err != nil {
		return out, err
	}
	var state string
	if err = phaseAdminDecode(m["billing_state"], &state); err != nil || state != "settled" {
		return out, errors.New("admin_initial_state_invalid")
	}
	out.Status, out.LineID, out.RequestID, out.Requests = 200, strconv.FormatUint(a.initial.LineID, 10), rid, a.requestSlice(start)
	return out, nil
}

func (a *phaseAdminAdapter) adjust90(ctx context.Context) (phaseAdminResult, error) {
	out := phaseAdminResult{Action: "adjust_90"}
	if a.initial == nil || a.adjusted {
		return out, errors.New("admin_adjustment_precondition")
	}
	before, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if err = before.require100("admin", *a.initial); err != nil {
		return out, err
	}
	if before.Receipt.AppliedAt != nil {
		return out, errors.New("admin_adjustment_requires_pending_B")
	}
	start := a.requestCount()
	line, err := a.importBill(ctx, 2)
	if err != nil {
		return out, err
	}
	var now time.Time
	if err = a.db.WithContext(ctx).Raw("SELECT UTC_TIMESTAMP(3)").Scan(&now).Error; err != nil {
		return out, err
	}
	m, _, err := a.call(ctx, 0, "adjustment_propose", fmt.Sprintf("/orgs/%d/tasks/%s/adjustments", a.seed.OrgID, a.seed.TaskID), gin.H{"proposal_key": a.prefix + "-adjust", "line_id": line.LineID, "reason": "verified_invoice_correction", "supplier_only": false, "expires_at": now.UTC().Add(time.Hour)}, 201)
	if err != nil {
		return out, err
	}
	pid, err := phaseAdminID(m, "proposal_id")
	if err != nil {
		return out, err
	}
	path := fmt.Sprintf("/orgs/%d/adjustments/%d", a.seed.OrgID, pid)
	if _, _, err = a.call(ctx, 1, "adjustment_review", path+"/review", gin.H{"approved": true, "reason": "verified_bill"}, 200); err != nil {
		return out, err
	}
	m, rid, err := a.call(ctx, 2, "adjustment_apply", path+"/apply", gin.H{}, 200)
	if err != nil {
		return out, err
	}
	appID, err := phaseAdminID(m, "application_id")
	if err != nil {
		return out, err
	}
	replay, err := phaseAdminReplay(m)
	if err != nil || replay {
		return out, errors.New("admin_adjustment_unexpected_replay")
	}
	after, err := a.readOracle(ctx)
	if err != nil {
		return out, err
	}
	if err = after.require90(before, pid, appID, line); err != nil {
		return out, err
	}
	requests := a.requestSlice(start)
	ids := make([]string, 0, 3)
	for _, r := range requests {
		if r.Action == "adjustment_propose" || r.Action == "adjustment_review" || r.Action == "adjustment_apply" {
			ids = append(ids, r.RequestID)
		}
	}
	if err = phaseAdminEventDelta(before, after, []string{"billing_adjustment_proposed", "billing_adjustment_reviewed", "billing.adjusted"}, ids, []string{line.PayloadRef, line.PayloadRef, line.PayloadRef}); err != nil {
		return out, err
	}
	if err = a.requireAudits(ctx, requests); err != nil {
		return out, err
	}
	a.adjusted = true
	out.Status, out.ProposalID, out.ApplicationID, out.RequestID, out.Requests = 200, strconv.FormatUint(pid, 10), strconv.FormatUint(appID, 10), rid, requests
	return out, nil
}

// Keep all callback and helper queries on the caller's actual transaction.
func phaseAdminClean(q *gorm.DB) *gorm.DB {
	return q.Session(&gorm.Session{NewDB: true, Context: q.Statement.Context, Logger: logger.Discard})
}

func (a *phaseAdminAdapter) requireAudits(ctx context.Context, requests []phaseAdminRequest) error {
	for _, request := range requests {
		action := ""
		switch request.Action {
		case "upload_r1", "upload_r2":
			action = "ark_bill_source_upload"
		case "source_review_r1", "source_review_r2":
			action = "ark_bill_source_review"
		case "import_begin_r1", "import_begin_r2":
			action = "ark_bill_import_begin"
		case "import_line_r1", "import_line_r2":
			action = "ark_bill_import_line"
		case "import_complete_r1", "import_complete_r2":
			action = "ark_bill_import_complete"
		case "settle_100":
			action = "ark_bill_settle"
		case "adjustment_propose":
			action = "ark_bill_adjustment_propose"
		case "adjustment_review":
			action = "ark_bill_adjustment_review"
		case "adjustment_apply":
			action = "ark_bill_adjustment_apply"
		default:
			return errors.New("admin_audit_action_unknown")
		}
		var rows []AuditLog
		if err := a.db.WithContext(ctx).Where("action = ? AND JSON_UNQUOTE(JSON_EXTRACT(detail, '$.request_id')) = ?", action, request.RequestID).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Actor != "user#"+request.Actor+"(superadmin)" {
			return errors.New("admin_audit_identity")
		}
	}
	return nil
}
