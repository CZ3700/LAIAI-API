package server

// These routes are control-plane commands. Financial values are read only
// from encrypted, independently reviewed supplier evidence, never from a
// settlement/proposal HTTP request. An operator upload is explicitly human
// attestation and must not be presented as an authenticated supplier API fetch.
import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ArkBillEvidenceSource stores provenance metadata only. Its payload is held
// exclusively in Invocation Store. Once reviewed, provenance is immutable.
type ArkBillEvidenceSource struct {
	ID               uint64     `gorm:"primaryKey" json:"id"`
	SourceKey        string     `json:"source_key"`
	ProviderScopeID  uint64     `json:"provider_scope_id"`
	StorageOrgID     uint64     `json:"storage_org_id"`
	SourceKind       string     `json:"source_kind"`
	OfficialBillID   string     `json:"official_bill_id"`
	SourceVersion    string     `json:"source_version"`
	SourceFileSHA256 string     `gorm:"column:source_file_sha256" json:"source_file_sha256"`
	PayloadRef       string     `json:"payload_ref"`
	CaptureRequestID string     `json:"-"`
	RowCount         int64      `json:"row_count"`
	UploadedBy       uint64     `json:"uploaded_by"`
	State            string     `json:"state"`
	ReviewedBy       uint64     `json:"reviewed_by"`
	ReviewReason     string     `json:"review_reason"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
}

func (ArkBillEvidenceSource) TableName() string { return "video_bill_evidence_sources" }

type arkBillDocument struct {
	Schema          string             `json:"schema"`
	ProviderScopeID uint64             `json:"provider_scope_id"`
	OfficialBillID  string             `json:"official_bill_id"`
	SourceVersion   string             `json:"source_version"`
	Currency        string             `json:"currency"`
	Rows            []arkBillSourceRow `json:"rows"`
}
type arkBillSourceRow struct {
	SourceRecordID   string    `json:"source_record_id"`
	SourceVersion    string    `json:"source_version"`
	SourceRevision   int64     `json:"source_revision"`
	Granularity      string    `json:"granularity"`
	UpstreamTaskID   string    `json:"upstream_task_id"`
	Model            string    `json:"model"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	CompletionTokens *int64    `json:"completion_tokens"`
	AmountCNY        string    `json:"amount_cny"`
	ObservedAt       time.Time `json:"observed_at"`
}

var arkBillIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var arkBillMoneyPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,8})?$`)
var errArkBillEvidence = errors.New("verified bill evidence required")

func arkBillHash(b []byte) string           { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func arkBillValidID(s string, max int) bool { return len(s) <= max && arkBillIDPattern.MatchString(s) }
func arkBillLock(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "mysql" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

// registerArkBillingAdmin must be mounted beneath the existing superadmin
// group. The additional gate requires an individual authenticated session:
// shared X-Admin-Token identities cannot upload, approve, or move money here.
func (s *Server) registerArkBillingAdmin(su *gin.RouterGroup) {
	g := su.Group("/video", s.arkBillActorRequired())
	g.POST("/bill-evidence", s.arkBillUpload)
	g.GET("/bill-evidence/:source_id", s.arkBillReadSource)
	g.POST("/bill-evidence/:source_id/review", s.arkBillReviewSource)
	g.POST("/bill-imports", s.arkBillBegin)
	g.POST("/bill-imports/:import_id/lines", s.arkBillImportLine)
	g.POST("/bill-imports/:import_id/complete", s.arkBillComplete)
	g.POST("/bill-lines/:line_id/bind", s.arkBillBind)
	g.POST("/orgs/:org_id/tasks/:task_id/settle-bill", s.arkBillSettle)
	g.POST("/orgs/:org_id/tasks/:task_id/adjustments", s.arkBillPropose)
	g.GET("/orgs/:org_id/adjustments/:proposal_id", s.arkBillGetProposal)
	g.POST("/orgs/:org_id/adjustments/:proposal_id/review", s.arkBillReviewAdjustment)
	g.POST("/orgs/:org_id/adjustments/:proposal_id/apply", s.arkBillApply)
}

func (s *Server) arkBillActorRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get("actor_id")
		actor, ok := uid.(uint64)
		if !ok || actor == 0 || c.GetString("actor_role") != identity.PlatRoleSuperadmin {
			arkBillError(c, http.StatusForbidden, "individual_superadmin_session_required")
			return
		}
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			arkBillError(c, 403, "individual_superadmin_session_required")
			return
		}
		claims, err := identity.VerifySessionToken(s.secret, strings.TrimPrefix(h, "Bearer "), s.now())
		if err != nil || claims.UserID != actor || claims.OrgID == 0 {
			arkBillError(c, 403, "individual_superadmin_session_required")
			return
		}
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			arkBillError(c, 503, "temporarily_unavailable")
			return
		}
		c.Set("ark_bill_actor", actor)
		c.Set("ark_bill_storage_org", claims.OrgID)
		c.Set("ark_bill_request_id", "abr_"+hex.EncodeToString(token[:]))
		c.Header("X-Request-ID", c.GetString("ark_bill_request_id"))
		c.Next()
	}
}
func arkBillActor(c *gin.Context) uint64 { return c.MustGet("ark_bill_actor").(uint64) }

func arkBillError(c *gin.Context, code int, message string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"code": message, "request_id": c.GetString("ark_bill_request_id")}})
}
func arkBillRespondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, videoruntime.ErrUnsupported):
		arkBillError(c, 409, "runtime_contract_unsupported")
	case errors.Is(err, videobilling.ErrInvalid), errors.Is(err, video.ErrInvalid):
		arkBillError(c, 422, "invalid_request")
	case errors.Is(err, errArkBillEvidence), errors.Is(err, invocation.ErrVideoExpired), errors.Is(err, invocation.ErrVideoAccessDenied):
		arkBillError(c, 409, "verified_evidence_required")
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, videobilling.ErrNotFound), errors.Is(err, video.ErrNotFound):
		arkBillError(c, 404, "not_found")
	case errors.Is(err, videobilling.ErrReviewRequired):
		arkBillError(c, 409, "independent_review_required")
	case errors.Is(err, videobilling.ErrStale):
		arkBillError(c, 409, "stale_reconciliation_basis")
	case errors.Is(err, videobilling.ErrUnmapped):
		arkBillError(c, 409, "bill_not_uniquely_mapped")
	case errors.Is(err, videobilling.ErrNotSettled):
		arkBillError(c, 409, "initial_settlement_required")
	case errors.Is(err, videobilling.ErrConflict), errors.Is(err, video.ErrConflict):
		arkBillError(c, 409, "conflict")
	case errors.Is(err, video.ErrState):
		arkBillError(c, 409, "task_outcome_unverified")
	case errors.Is(err, videobilling.ErrQuotaExceeded):
		arkBillError(c, 409, "quota_exceeded")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		arkBillError(c, 409, "insufficient_balance")
	default:
		arkBillError(c, 503, "temporarily_unavailable")
	}
}

// encoding/json normally accepts duplicate keys. Reject them as well as
// unknown fields, trailing JSON, oversized bodies, and noninteger numbers.
func arkBillJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return videobilling.ErrInvalid
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
		default:
			return videobilling.ErrInvalid
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return videobilling.ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return videobilling.ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return videobilling.ErrInvalid
	}
	// Every command is a JSON object; null is not an empty command.
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return videobilling.ErrInvalid
	}
	return nil
}
func arkBillDecode(c *gin.Context, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, controlPlaneBodyLimit+1))
	if err != nil || len(raw) > controlPlaneBodyLimit || arkBillJSON(raw, dst) != nil {
		arkBillError(c, 422, "invalid_request")
		return false
	}
	return true
}
func arkBillParam(c *gin.Context, name string) (uint64, bool) {
	s := c.Param(name)
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != s {
		arkBillError(c, 422, "invalid_request")
		return 0, false
	}
	return n, true
}
func arkBillTaskPath(c *gin.Context) (uint64, string, bool) {
	org, ok := arkBillParam(c, "org_id")
	if !ok {
		return 0, "", false
	}
	task := c.Param("task_id")
	if !arkBillValidID(task, 64) {
		arkBillError(c, 422, "invalid_request")
		return 0, "", false
	}
	return org, task, true
}
func (s *Server) arkBillAudit(tx *gorm.DB, c *gin.Context, action, target string, detail any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d(superadmin)", arkBillActor(c)), Action: action, Target: target, Detail: string(b), IP: c.ClientIP(), CreatedAt: s.now().UnixMilli()}).Error
}
func (s *Server) arkBillStore() (*invocation.VideoStore, error) {
	return invocation.NewVideoStore(s.db, s.keyring, invocation.VideoStoreOptions{Now: s.now})
}
func validateArkBillDocument(d arkBillDocument, now time.Time) error {
	if d.Schema != "ark_supplier_bill_v1" || d.ProviderScopeID == 0 || !arkBillValidID(d.OfficialBillID, 128) || !arkBillValidID(d.SourceVersion, 64) || d.Currency != "CNY" || len(d.Rows) == 0 || len(d.Rows) > 1000 {
		return videobilling.ErrInvalid
	}
	seen := map[string]bool{}
	for _, r := range d.Rows {
		if !arkBillValidID(r.SourceRecordID, 128) || !arkBillValidID(r.SourceVersion, 64) || r.SourceRevision <= 0 || !arkBillValidID(r.Model, 64) || r.PeriodStart.IsZero() || !r.PeriodEnd.After(r.PeriodStart) || r.ObservedAt.IsZero() || r.ObservedAt.After(now.Add(5*time.Minute)) || (r.CompletionTokens != nil && *r.CompletionTokens < 0) || !arkBillMoneyPattern.MatchString(r.AmountCNY) {
			return videobilling.ErrInvalid
		}
		a, err := decimal.NewFromString(r.AmountCNY)
		if err != nil {
			return videobilling.ErrInvalid
		}
		if r.Granularity == videobilling.BillTaskTotal {
			if a.IsNegative() || !arkBillValidID(r.UpstreamTaskID, 128) {
				return videobilling.ErrInvalid
			}
		} else if r.Granularity != videobilling.BillAggregate || r.UpstreamTaskID != "" {
			return videobilling.ErrInvalid
		}
		key := r.SourceRecordID + "\x00" + r.SourceVersion
		if seen[key] {
			return videobilling.ErrInvalid
		}
		seen[key] = true
	}
	return nil
}

func (s *Server) arkBillUpload(c *gin.Context) {
	var req struct {
		SourceKey string          `json:"source_key"`
		Document  arkBillDocument `json:"document"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	now := s.now().UTC()
	if !arkBillValidID(req.SourceKey, 64) || validateArkBillDocument(req.Document, now) != nil {
		arkBillError(c, 422, "invalid_request")
		return
	}
	// The canonical reviewed document uses the database's UTC millisecond
	// precision, so later evidence comparisons cannot depend on timezone text.
	for i := range req.Document.Rows {
		r := &req.Document.Rows[i]
		r.PeriodStart = r.PeriodStart.UTC().Truncate(time.Millisecond)
		r.PeriodEnd = r.PeriodEnd.UTC().Truncate(time.Millisecond)
		r.ObservedAt = r.ObservedAt.UTC().Truncate(time.Millisecond)
	}
	if validateArkBillDocument(req.Document, now) != nil {
		arkBillError(c, 422, "invalid_request")
		return
	}
	body, err := json.Marshal(req.Document)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	store, err := s.arkBillStore()
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	storageOrg := c.MustGet("ark_bill_storage_org").(uint64)
	p, err := store.Prepare(invocation.VideoPayloadInput{OrgID: storageOrg, RequestID: c.GetString("ark_bill_request_id"), Direction: invocation.VideoClientRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: now.Add(30 * 24 * time.Hour), CaptureEnabled: true})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	source := ArkBillEvidenceSource{SourceKey: req.SourceKey, ProviderScopeID: req.Document.ProviderScopeID, StorageOrgID: storageOrg, SourceKind: "operator_upload", OfficialBillID: req.Document.OfficialBillID, SourceVersion: req.Document.SourceVersion, SourceFileSHA256: arkBillHash(body), PayloadRef: p.PayloadID(), CaptureRequestID: c.GetString("ark_bill_request_id"), RowCount: int64(len(req.Document.Rows)), UploadedBy: arkBillActor(c), State: "pending_review", CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	replay := false
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var scope video.ProviderScope
		if e := tx.Where("id = ?", source.ProviderScopeID).Take(&scope).Error; e != nil {
			return e
		}
		var org identity.Organization
		if e := tx.Where("id = ?", storageOrg).Take(&org).Error; e != nil {
			return e
		}
		var old ArkBillEvidenceSource
		e := arkBillLock(tx).Where("provider_scope_id = ? AND source_key = ?", source.ProviderScopeID, source.SourceKey).Take(&old).Error
		if e == nil {
			if old.SourceFileSHA256 != source.SourceFileSHA256 || old.StorageOrgID != storageOrg || old.UploadedBy != source.UploadedBy {
				return videobilling.ErrConflict
			}
			source = old
			replay = true
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if _, e = store.StageTx(tx, p); e != nil {
			return e
		}
		if e = tx.Create(&source).Error; e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_source_upload", fmt.Sprint(source.ID), gin.H{"request_id": c.GetString("ark_bill_request_id"), "source_id": source.ID, "source_kind": source.SourceKind, "source_file_sha256": source.SourceFileSHA256})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(201, gin.H{"source": source, "replay": replay})
}

func (s *Server) arkBillSource(c *gin.Context, id uint64, verified bool) (ArkBillEvidenceSource, arkBillDocument, error) {
	var src ArkBillEvidenceSource
	var d arkBillDocument
	if err := s.db.WithContext(c.Request.Context()).Where("id = ?", id).Take(&src).Error; err != nil {
		return src, d, err
	}
	if err := s.arkBillSourceValidTx(s.db.WithContext(c.Request.Context()), src, verified); err != nil {
		return src, d, err
	}
	store, err := s.arkBillStore()
	if err != nil {
		return src, d, err
	}
	body, err := store.ReadForHistory(c.Request.Context(), src.StorageOrgID, src.PayloadRef, invocation.VideoAccess{ActorRole: invocation.VideoRoleRoot, ActorID: fmt.Sprintf("user_%d", arkBillActor(c)), ReasonCode: "bill_source_review", OperationID: c.GetString("ark_bill_request_id")})
	if err != nil {
		return src, d, err
	}
	defer clear(body)
	if arkBillJSON(body, &d) != nil || validateArkBillDocument(d, s.now()) != nil || d.ProviderScopeID != src.ProviderScopeID || d.OfficialBillID != src.OfficialBillID || d.SourceVersion != src.SourceVersion || int64(len(d.Rows)) != src.RowCount {
		return src, d, errArkBillEvidence
	}
	canonical, err := json.Marshal(d)
	if err != nil || arkBillHash(canonical) != src.SourceFileSHA256 {
		return src, d, errArkBillEvidence
	}
	return src, d, nil
}
func (s *Server) arkBillSourceValidTx(tx *gorm.DB, src ArkBillEvidenceSource, verified bool) error {
	if src.SourceKind != "operator_upload" || src.UploadedBy == 0 || !src.ExpiresAt.After(s.now()) || (verified && (src.State != "verified" || src.ReviewedBy == 0 || src.ReviewedBy == src.UploadedBy || src.ReviewedAt == nil)) {
		return errArkBillEvidence
	}
	var p invocation.VideoPayload
	if err := tx.Where("org_id = ? AND payload_id = ?", src.StorageOrgID, src.PayloadRef).Take(&p).Error; err != nil {
		return err
	}
	if p.Purpose != invocation.VideoPurposeHistory || p.Direction != invocation.VideoClientRequest || p.SchemaVersion != 1 || p.RequestID != src.CaptureRequestID || !p.ExpiresAt.After(s.now()) || (p.CaptureStatus != invocation.VideoCaptureStaged && p.CaptureStatus != invocation.VideoCaptureStored) {
		return errArkBillEvidence
	}
	return nil
}
func (s *Server) arkBillReadSource(c *gin.Context) {
	id, ok := arkBillParam(c, "source_id")
	if !ok {
		return
	}
	src, d, err := s.arkBillSource(c, id, false)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"source": src, "document": d, "authenticity": "operator_attestation_required"})
}
func (s *Server) arkBillReviewSource(c *gin.Context) {
	id, ok := arkBillParam(c, "source_id")
	if !ok {
		return
	}
	var req struct {
		Approved *bool  `json:"approved"`
		Reason   string `json:"reason"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	if req.Approved == nil || (*req.Approved && req.Reason != "verified_official_export") || (!*req.Approved && req.Reason != "source_not_verified") {
		arkBillError(c, 422, "invalid_request")
		return
	}
	_, _, err := s.arkBillSource(c, id, false)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var src ArkBillEvidenceSource
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if e := arkBillLock(tx).Where("id = ?", id).Take(&src).Error; e != nil {
			return e
		}
		if e := s.arkBillSourceValidTx(tx, src, false); e != nil {
			return e
		}
		if src.UploadedBy == arkBillActor(c) {
			return videobilling.ErrReviewRequired
		}
		state := "rejected"
		if *req.Approved {
			state = "verified"
		}
		if src.State != "pending_review" {
			if src.State == state && src.ReviewedBy == arkBillActor(c) && src.ReviewReason == req.Reason {
				return nil
			}
			return videobilling.ErrConflict
		}
		now := s.now().UTC()
		src.State = state
		src.ReviewedBy = arkBillActor(c)
		src.ReviewedAt = &now
		src.ReviewReason = req.Reason
		if e := tx.Model(&ArkBillEvidenceSource{}).Where("id = ? AND state = ?", id, "pending_review").Updates(map[string]any{"state": state, "reviewed_by": src.ReviewedBy, "reviewed_at": now, "review_reason": req.Reason}).Error; e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_source_review", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id"), "approved": *req.Approved, "reason": req.Reason})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"source": src})
}

func (s *Server) arkBillBegin(c *gin.Context) {
	var req struct {
		SourceID  uint64 `json:"source_id"`
		ImportKey string `json:"import_key"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	if req.SourceID == 0 || !arkBillValidID(req.ImportKey, 64) {
		arkBillError(c, 422, "invalid_request")
		return
	}
	src, _, err := s.arkBillSource(c, req.SourceID, true)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var result videobilling.BeginImportResult
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		var e error
		result, e = videobilling.BeginImportTx(tx, videobilling.BeginImportInput{ImportKey: req.ImportKey, ProviderScopeID: src.ProviderScopeID, Source: "ark_official_bill", SourceVersion: src.SourceVersion, SourceHash: src.SourceFileSHA256, PayloadRef: src.PayloadRef, ExpectedRows: src.RowCount, Now: s.now()})
		if e != nil || result.Replay {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_import_begin", fmt.Sprint(result.Import.ID), gin.H{"request_id": c.GetString("ark_bill_request_id"), "source_id": src.ID})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"import_id": result.Import.ID, "state": result.Import.State, "expected_rows": result.Import.ExpectedRows, "replay": result.Replay})
}
func (s *Server) arkBillSourceByPayload(c *gin.Context, scope uint64, payload string) (ArkBillEvidenceSource, arkBillDocument, error) {
	var src ArkBillEvidenceSource
	if err := s.db.Where("provider_scope_id = ? AND payload_ref = ?", scope, payload).Take(&src).Error; err != nil {
		return src, arkBillDocument{}, err
	}
	return s.arkBillSource(c, src.ID, true)
}
func arkBillLineFromSource(src ArkBillEvidenceSource, r arkBillSourceRow) videobilling.BillLine {
	b, _ := json.Marshal(r)
	amount, _ := decimal.NewFromString(r.AmountCNY)
	return videobilling.BillLine{ProviderScopeID: src.ProviderScopeID, Source: "ark_official_bill", SourceRecordID: r.SourceRecordID, SourceVersion: r.SourceVersion, SourceRevision: r.SourceRevision, EvidenceHash: arkBillHash(b), PayloadRef: src.PayloadRef, Granularity: r.Granularity, UpstreamTaskID: r.UpstreamTaskID, Model: r.Model, PeriodStart: r.PeriodStart, PeriodEnd: r.PeriodEnd, CompletionTokens: r.CompletionTokens, AmountCNY: amount, ObservedAt: r.ObservedAt}
}
func arkBillBindingTx(tx *gorm.DB, line videobilling.BillLine) (*videobilling.VerifiedBinding, error) {
	if line.Granularity != videobilling.BillTaskTotal {
		return nil, nil
	}
	var b video.UpstreamBinding
	if err := tx.Where("provider_scope_id = ? AND upstream_task_id = ?", line.ProviderScopeID, line.UpstreamTaskID).Take(&b).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var st video.State
	if err := tx.Where("org_id = ? AND task_id = ? AND provider_scope_id = ?", b.OrgID, b.TaskID, b.ProviderScopeID).Take(&st).Error; err != nil {
		return nil, err
	}
	var count int64
	if err := tx.Table("tasks").Where("org_id = ? AND task_id = ? AND provider = ? AND upstream_task_id = ? AND model = ?", b.OrgID, b.TaskID, video.Provider, b.UpstreamTaskID, line.Model).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, videobilling.ErrUnmapped
	}
	canonical, _ := json.Marshal([]any{"ark-bill-binding-v1", b.ProviderScopeID, b.UpstreamTaskID, b.OrgID, b.TaskID})
	return &videobilling.VerifiedBinding{ProviderScopeID: b.ProviderScopeID, UpstreamTaskID: b.UpstreamTaskID, OrgID: b.OrgID, TaskID: b.TaskID, BindingVersion: arkBillHash(canonical)}, nil
}
func (s *Server) arkBillImportLine(c *gin.Context) {
	id, ok := arkBillParam(c, "import_id")
	if !ok {
		return
	}
	var req struct {
		RowNumber int64 `json:"row_number"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	var imp videobilling.BillImport
	if err := s.db.Where("id = ?", id).Take(&imp).Error; err != nil {
		arkBillRespondError(c, err)
		return
	}
	src, doc, err := s.arkBillSourceByPayload(c, imp.ProviderScopeID, imp.PayloadRef)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	if imp.SourceHash != src.SourceFileSHA256 || imp.SourceVersion != src.SourceVersion || imp.ExpectedRows != src.RowCount {
		arkBillRespondError(c, errArkBillEvidence)
		return
	}
	if req.RowNumber <= 0 || req.RowNumber > int64(len(doc.Rows)) {
		arkBillError(c, 422, "invalid_request")
		return
	}
	line := arkBillLineFromSource(src, doc.Rows[req.RowNumber-1])
	var result videobilling.ImportLineResult
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		binding, e := arkBillBindingTx(tx, line)
		if e != nil {
			return e
		}
		result, e = videobilling.ImportLineTx(tx, videobilling.ImportLineInput{ImportID: id, RowNumber: req.RowNumber, Line: line, Binding: binding, Now: s.now()})
		if e != nil || result.Replay {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_import_line", fmt.Sprint(result.Line.ID), gin.H{"request_id": c.GetString("ark_bill_request_id"), "import_id": id, "row_number": req.RowNumber, "state": result.Item.State})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"line_id": result.Line.ID, "reconciliation_id": result.Item.ID, "state": result.Item.State, "replay": result.Replay})
}
func (s *Server) arkBillComplete(c *gin.Context) {
	id, ok := arkBillParam(c, "import_id")
	if !ok {
		return
	}
	var req struct{}
	if !arkBillDecode(c, &req) {
		return
	}
	var result videobilling.BillImport
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var e error
		result, e = videobilling.CompleteImportTx(tx, id, s.now())
		if e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_import_complete", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id")})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"import_id": result.ID, "state": result.State})
}

func (s *Server) arkBillVerifiedLine(c *gin.Context, id uint64) (videobilling.BillLine, ArkBillEvidenceSource, error) {
	var line videobilling.BillLine
	if err := s.db.Where("id = ?", id).Take(&line).Error; err != nil {
		return line, ArkBillEvidenceSource{}, err
	}
	// Same supplier revision can be reacquired after ciphertext expiry without
	// mutating its immutable BillLine or fabricating a revision. A durable import
	// receipt must connect the refreshed, separately reviewed source to this line.
	var sourceIDs []uint64
	err := s.db.WithContext(c.Request.Context()).Table("video_provider_bill_import_rows AS r").
		Select("DISTINCT s.id").Joins("JOIN video_provider_bill_imports i ON i.id = r.import_id").
		Joins("JOIN video_bill_evidence_sources s ON s.payload_ref = i.payload_ref AND s.provider_scope_id = i.provider_scope_id").
		Where("r.line_id = ? AND s.provider_scope_id = ? AND s.state = ? AND s.expires_at > ?", line.ID, line.ProviderScopeID, "verified", s.now()).Order("s.id DESC").Limit(8).Scan(&sourceIDs).Error
	if err != nil {
		return line, ArkBillEvidenceSource{}, err
	}
	for _, sourceID := range sourceIDs {
		src, doc, err := s.arkBillSource(c, sourceID, true)
		if err != nil {
			return line, src, err
		}
		for _, r := range doc.Rows {
			candidate := arkBillLineFromSource(src, r)
			if candidate.SourceRecordID != line.SourceRecordID || candidate.SourceVersion != line.SourceVersion {
				continue
			}
			candidate.ID = line.ID
			candidate.ContentHash = line.ContentHash
			// Payload location may rotate; authoritative content and all numerical
			// facts must be byte-identical in canonical typed representation.
			candidate.PayloadRef = line.PayloadRef
			a, _ := json.Marshal(candidate)
			b, _ := json.Marshal(line)
			if !bytes.Equal(a, b) {
				return line, src, errArkBillEvidence
			}
			return line, src, nil
		}
	}
	return line, ArkBillEvidenceSource{}, errArkBillEvidence
}
func (s *Server) arkBillBind(c *gin.Context) {
	id, ok := arkBillParam(c, "line_id")
	if !ok {
		return
	}
	var req struct{}
	if !arkBillDecode(c, &req) {
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, id)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var item videobilling.ReconciliationItem
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		b, e := arkBillBindingTx(tx, line)
		if e != nil {
			return e
		}
		if b == nil {
			return videobilling.ErrUnmapped
		}
		item, e = videobilling.BindBillLineTx(tx, id, *b, s.now())
		if e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_line_bind", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id"), "org_id": b.OrgID, "task_id": b.TaskID})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"line_id": id, "reconciliation_id": item.ID, "state": item.State})
}
func arkBillCheckBoundTx(tx *gorm.DB, org uint64, task string, line videobilling.BillLine) error {
	b, err := arkBillBindingTx(tx, line)
	if err != nil {
		return err
	}
	if b == nil || b.OrgID != org || b.TaskID != task {
		return videobilling.ErrUnmapped
	}
	return nil
}

func (s *Server) arkBillSettle(c *gin.Context) {
	org, taskID, ok := arkBillTaskPath(c)
	if !ok {
		return
	}
	var req struct {
		LineID uint64 `json:"line_id"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, req.LineID)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var result videobilling.SettlementResult
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		t, _, e := video.LockBillingTaskTx(tx, org, taskID)
		if e != nil {
			return e
		}
		// A bill does not prove generation success or resolve ambiguous submit.
		// Preserve the hold until an independently obtained official outcome.
		if t.Status != "succeeded" {
			return video.ErrState
		}
		if e = s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		if e = arkBillCheckBoundTx(tx, org, taskID, line); e != nil {
			return e
		}
		result, e = videobilling.SettleBillUsageTx(tx, org, taskID, req.LineID, s.now())
		if e != nil {
			return e
		}
		if e = video.ApplyBillingSettlementTx(tx, org, taskID, c.GetString("ark_bill_request_id"), src.PayloadRef, result, s.now()); e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_settle", taskID, gin.H{"request_id": c.GetString("ark_bill_request_id"), "org_id": org, "line_id": req.LineID, "source_id": src.ID, "state": result.State, "replay": result.Replay})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"task_id": taskID, "billing_state": result.State, "replay": result.Replay, "quota_overrun": result.QuotaOverrun})
}
func (s *Server) arkBillPropose(c *gin.Context) {
	org, taskID, ok := arkBillTaskPath(c)
	if !ok {
		return
	}
	var req struct {
		ProposalKey  string    `json:"proposal_key"`
		LineID       uint64    `json:"line_id"`
		Reason       string    `json:"reason"`
		SupplierOnly *bool     `json:"supplier_only"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	if req.SupplierOnly == nil || !arkBillValidID(req.ProposalKey, 64) || req.Reason != "verified_invoice_correction" || !req.ExpiresAt.After(s.now()) || req.ExpiresAt.After(s.now().Add(7*24*time.Hour)) {
		arkBillError(c, 422, "invalid_request")
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, req.LineID)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var result videobilling.AdjustmentProposal
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, _, e := video.LockBillingTaskTx(tx, org, taskID); e != nil {
			return e
		}
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		if e := arkBillCheckBoundTx(tx, org, taskID, line); e != nil {
			return e
		}
		var existing videobilling.AdjustmentProposal
		existingErr := tx.Where("org_id = ? AND proposal_key = ?", org, req.ProposalKey).Take(&existing).Error
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		var e error
		result, e = videobilling.ProposeAdjustmentTx(tx, videobilling.ProposeAdjustmentInput{OrgID: org, TaskID: taskID, ProposalKey: req.ProposalKey, LineID: req.LineID, ProposerID: arkBillActor(c), Reason: req.Reason, SupplierOnly: *req.SupplierOnly, Now: s.now(), ExpiresAt: req.ExpiresAt})
		if e != nil {
			return e
		}
		if existingErr != nil {
			if e = video.AppendBillingEventTx(tx, org, taskID, "billing_adjustment_proposed", c.GetString("ark_bill_request_id"), src.PayloadRef, s.now()); e != nil {
				return e
			}
		}
		return s.arkBillAudit(tx, c, "ark_bill_adjustment_propose", fmt.Sprint(result.ID), gin.H{"request_id": c.GetString("ark_bill_request_id"), "org_id": org, "task_id": taskID, "line_id": req.LineID, "source_id": src.ID, "supplier_only": *req.SupplierOnly})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(201, arkBillProposalView(result))
}
func arkBillProposalView(p videobilling.AdjustmentProposal) gin.H {
	return gin.H{"proposal_id": p.ID, "org_id": p.OrgID, "task_id": p.TaskID, "line_id": p.LineID, "proposer_id": p.ProposerID, "supplier_only": p.SupplierOnly, "base_net_usd": p.BaseNetUSD.StringFixed(8), "target_net_usd": p.TargetNetUSD.StringFixed(8), "customer_delta_usd": p.TargetNetUSD.Sub(p.BaseNetUSD).StringFixed(8), "target_supplier_cny": p.TargetCNY.StringFixed(8), "target_completion_tokens": p.TargetTokens, "expires_at": p.ExpiresAt}
}
func (s *Server) arkBillGetProposal(c *gin.Context) {
	org, ok := arkBillParam(c, "org_id")
	if !ok {
		return
	}
	id, ok := arkBillParam(c, "proposal_id")
	if !ok {
		return
	}
	var p videobilling.AdjustmentProposal
	if err := s.db.WithContext(c.Request.Context()).Where("id = ? AND org_id = ?", id, org).Take(&p).Error; err != nil {
		arkBillRespondError(c, err)
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, p.LineID)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	v := arkBillProposalView(p)
	v["source"] = src
	v["source_record_id"] = line.SourceRecordID
	v["source_revision"] = line.SourceRevision
	c.JSON(200, v)
}
func (s *Server) arkBillReviewAdjustment(c *gin.Context) {
	org, ok := arkBillParam(c, "org_id")
	if !ok {
		return
	}
	id, ok := arkBillParam(c, "proposal_id")
	if !ok {
		return
	}
	var req struct {
		Approved *bool  `json:"approved"`
		Reason   string `json:"reason"`
	}
	if !arkBillDecode(c, &req) {
		return
	}
	if req.Approved == nil || (*req.Approved && req.Reason != "verified_bill") || (!*req.Approved && req.Reason != "correction_not_approved") {
		arkBillError(c, 422, "invalid_request")
		return
	}
	var p videobilling.AdjustmentProposal
	if err := s.db.Where("id = ? AND org_id = ?", id, org).Take(&p).Error; err != nil {
		arkBillRespondError(c, err)
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, p.LineID)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var result videobilling.AdjustmentReview
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, _, e := video.LockBillingTaskTx(tx, org, p.TaskID); e != nil {
			return e
		}
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		if e := arkBillCheckBoundTx(tx, org, p.TaskID, line); e != nil {
			return e
		}
		var existing videobilling.AdjustmentReview
		existingErr := tx.Where("org_id = ? AND proposal_id = ?", org, id).Take(&existing).Error
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		var e error
		result, e = videobilling.ReviewAdjustmentTx(tx, videobilling.ReviewAdjustmentInput{OrgID: org, ProposalID: id, ReviewerID: arkBillActor(c), Approved: *req.Approved, Reason: req.Reason, Now: s.now()})
		if e != nil {
			return e
		}
		if existingErr != nil {
			if e = video.AppendBillingEventTx(tx, org, p.TaskID, "billing_adjustment_reviewed", c.GetString("ark_bill_request_id"), src.PayloadRef, s.now()); e != nil {
				return e
			}
		}
		return s.arkBillAudit(tx, c, "ark_bill_adjustment_review", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id"), "source_id": src.ID, "approved": *req.Approved})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"review_id": result.ID, "proposal_id": id, "reviewer_id": result.ReviewerID, "approved": result.Approved})
}
func (s *Server) arkBillApply(c *gin.Context) {
	org, ok := arkBillParam(c, "org_id")
	if !ok {
		return
	}
	id, ok := arkBillParam(c, "proposal_id")
	if !ok {
		return
	}
	var req struct{}
	if !arkBillDecode(c, &req) {
		return
	}
	var p videobilling.AdjustmentProposal
	if err := s.db.Where("id = ? AND org_id = ?", id, org).Take(&p).Error; err != nil {
		arkBillRespondError(c, err)
		return
	}
	// A committed application remains replayable after source expiry or later
	// corrections. This path returns that immutable receipt and moves no money.
	var applied videobilling.AdjustmentApplication
	appliedErr := s.db.Where("proposal_id = ? AND org_id = ?", id, org).Take(&applied).Error
	if appliedErr == nil {
		var replay videobilling.AdjustmentResult
		err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if _, _, e := video.LockBillingTaskTx(tx, org, p.TaskID); e != nil {
				return e
			}
			var e error
			replay, e = videobilling.ApplyAdjustmentTx(tx, videobilling.ApplyAdjustmentInput{OrgID: org, ProposalID: id, Now: s.now()})
			if e != nil {
				return e
			}
			if !replay.Replay {
				return videobilling.ErrInvariant
			}
			return s.arkBillAudit(tx, c, "ark_bill_adjustment_replay", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id"), "application_id": replay.Application.ID})
		})
		if err != nil {
			arkBillRespondError(c, err)
			return
		}
		c.JSON(200, arkBillApplicationView(replay))
		return
	}
	if !errors.Is(appliedErr, gorm.ErrRecordNotFound) {
		arkBillRespondError(c, appliedErr)
		return
	}
	line, src, err := s.arkBillVerifiedLine(c, p.LineID)
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	var result videobilling.AdjustmentResult
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if _, _, e := video.LockBillingTaskTx(tx, org, p.TaskID); e != nil {
			return e
		}
		if e := s.arkBillSourceValidTx(tx, src, true); e != nil {
			return e
		}
		if e := arkBillCheckBoundTx(tx, org, p.TaskID, line); e != nil {
			return e
		}
		var e error
		result, e = videobilling.ApplyAdjustmentTx(tx, videobilling.ApplyAdjustmentInput{OrgID: org, ProposalID: id, Now: s.now()})
		if e != nil {
			return e
		}
		if e = video.ApplyBillingAdjustmentTx(tx, org, p.TaskID, c.GetString("ark_bill_request_id"), src.PayloadRef, result, s.now()); e != nil {
			return e
		}
		return s.arkBillAudit(tx, c, "ark_bill_adjustment_apply", fmt.Sprint(id), gin.H{"request_id": c.GetString("ark_bill_request_id"), "source_id": src.ID, "application_id": result.Application.ID, "replay": result.Replay})
	})
	if err != nil {
		arkBillRespondError(c, err)
		return
	}
	c.JSON(200, arkBillApplicationView(result))
}
func arkBillApplicationView(result videobilling.AdjustmentResult) gin.H {
	a := result.Application
	return gin.H{"application_id": a.ID, "proposal_id": a.ProposalID, "task_id": a.TaskID, "customer_delta_usd": a.CustomerDeltaUSD.StringFixed(8), "net_usd": a.NetUSD.StringFixed(8), "supplier_cny": a.SupplierCNY.StringFixed(8), "completion_tokens": a.CompletionTokens, "replay": result.Replay, "quota_overrun": result.QuotaOverrun}
}
