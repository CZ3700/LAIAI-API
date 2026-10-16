package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) registerFundActivities(org *gin.RouterGroup) {
	org.GET("/fund-activities", s.listFundActivities)
	org.GET("/fund-activities/:id", s.getFundActivity)
	org.GET("/fund-summary", s.getFundSummary)
	org.GET("/fund-exports", s.exportFundActivities)
	org.POST("/fund-exports", s.createFundExport)
	org.GET("/fund-exports/:id", s.getFundExport)
	org.GET("/fund-exports/:id/download", s.downloadFundExport)
}
func fundFilter(c *gin.Context, limit int) (ledger.ActivityFilter, error) {
	f := ledger.ActivityFilter{Limit: limit, Type: c.Query("type"), Direction: c.Query("direction"), Status: c.Query("status"), RefID: c.Query("ref_id"), RefType: c.Query("ref_type"), Keyword: c.Query("keyword"), FailureCode: c.Query("failure_code")}
	if len(f.Type) > 64 || len(f.RefID) > 160 || len(f.RefType) > 64 || len(f.Keyword) > 160 || len(f.FailureCode) > 64 {
		return f, ledger.ErrInvalidInput
	}
	if f.Direction != "" && !containsPrice([]string{"credit", "debit", "freeze", "unfreeze"}, f.Direction) {
		return f, ledger.ErrInvalidInput
	}
	if f.Status != "" && !containsPrice([]string{"success", "failed", "processing", "reversed"}, f.Status) {
		return f, ledger.ErrInvalidInput
	}
	if raw := c.Query("before"); raw != "" {
		n, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || n == 0 {
			return f, ledger.ErrInvalidInput
		}
		f.Before = n
	}
	for _, entry := range []struct {
		key string
		dst **time.Time
	}{{"from", &f.From}, {"until", &f.Until}} {
		if raw := c.Query(entry.key); raw != "" {
			v, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return f, e
			}
			v = v.UTC()
			*entry.dst = &v
		}
	}
	if f.From != nil && f.Until != nil && !f.From.Before(*f.Until) {
		return f, ledger.ErrInvalidInput
	}
	return f, nil
}
func (s *Server) listFundActivities(c *gin.Context) {
	f, e := fundFilter(c, 51)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	if c.DefaultQuery("view", "movements") == "operations" {
		rows, e := ledger.ListOperations(s.db, org, f)
		if e != nil {
			fail(c, 503, "operation query unavailable")
			return
		}
		var next uint64
		if len(rows) > 50 {
			next = rows[49].ID
			rows = rows[:50]
		}
		c.JSON(200, gin.H{"items": rows, "next_before": next, "view": "operations", "amount_semantics": "requested amount is not a debit"})
		return
	}
	if c.DefaultQuery("view", "movements") != "movements" {
		fail(c, 422, "invalid activity view")
		return
	}
	rows, e := ledger.ListMovements(s.db, org, f)
	if e != nil {
		fail(c, 503, "movement query unavailable")
		return
	}
	var next uint64
	if len(rows) > 50 {
		next = rows[49].ID
		rows = rows[:50]
	}
	c.JSON(200, gin.H{"items": rows, "next_before": next, "view": "movements", "amount_semantics": "committed deltas; freezing is not consumption"})
}
func (s *Server) getFundActivity(c *gin.Context) {
	id, e := strconv.ParseUint(c.Param("id"), 10, 64)
	if e != nil || id == 0 {
		fail(c, 404, "activity not found")
		return
	}
	op, rows, events, e := ledger.OperationDetail(s.db, mustClaims(c).OrgID, id)
	if e != nil {
		fail(c, 404, "activity not found")
		return
	}
	related := []ledger.Movement{}
	if op.RefID != "" {
		refs := []string{op.RefType}
		if op.RefType == "video" || op.RefType == "video_adjustment" {
			refs = []string{"video", "video_adjustment"}
		}
		if e = s.db.Where("org_id = ? AND account_type = ? AND ref_id = ? AND ref_type IN ?", op.OrgID, ledger.AccountUserCredit, op.RefID, refs).Order("id").Limit(101).Find(&related).Error; e != nil {
			fail(c, 503, "related movements unavailable")
			return
		}
	}
	hasMore := len(related) > 100
	if hasMore {
		related = related[:100]
	}
	c.JSON(200, gin.H{"operation": op, "movements": rows, "events": events, "related_movements": related, "related_has_more": hasMore})
}
func (s *Server) getFundSummary(c *gin.Context) {
	f, e := fundFilter(c, 1)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	var posted, held decimal.Decimal
	var totals struct {
		NetChange   decimal.Decimal `json:"net_change_usd"`
		Refunds     decimal.Decimal `json:"refund_usd"`
		UsageDebits decimal.Decimal `json:"usage_debit_usd"`
	}
	sums := []struct {
		Direction      string          `json:"direction"`
		Amount         decimal.Decimal `json:"amount"`
		PostedDelta    decimal.Decimal `json:"posted_delta"`
		HeldDelta      decimal.Decimal `json:"held_delta"`
		AvailableDelta decimal.Decimal `json:"available_delta"`
	}{}
	e = s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		posted, held, e = ledger.OrgBalances(tx, org, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		q := tx.Model(&ledger.Movement{}).Where("org_id = ? AND account_type = ? AND type <> ?", org, ledger.AccountUserCredit, "internal_transfer")
		if f.From != nil {
			q = q.Where("occurred_at >= ?", *f.From)
		}
		if f.Until != nil {
			q = q.Where("occurred_at < ?", *f.Until)
		}
		if f.Type != "" {
			q = q.Where("type = ?", f.Type)
		}
		if f.Direction != "" {
			q = q.Where("direction = ?", f.Direction)
		}
		if f.RefID != "" {
			q = q.Where("ref_id = ?", f.RefID)
		}
		if f.RefType != "" {
			q = q.Where("ref_type = ?", f.RefType)
		}
		if f.Keyword != "" {
			escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(f.Keyword)
			q = q.Where("ref_id LIKE ? ESCAPE '!'", "%"+escaped+"%")
		}
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		}
		if f.FailureCode != "" {
			q = q.Where("1 = 0")
		}
		if e := q.Session(&gorm.Session{}).Select("COALESCE(SUM(posted_delta),0) AS net_change, COALESCE(SUM(CASE WHEN type = 'video_refund' THEN amount ELSE 0 END),0) AS refunds, COALESCE(SUM(CASE WHEN type IN ('usage','video_debit_adjustment') THEN amount ELSE 0 END),0) AS usage_debits").Scan(&totals).Error; e != nil {
			return e
		}
		return q.Session(&gorm.Session{}).Select("direction, SUM(amount) AS amount, SUM(posted_delta) AS posted_delta, SUM(held_delta) AS held_delta, SUM(available_delta) AS available_delta").Group("direction").Scan(&sums).Error
	})
	if e != nil {
		fail(c, 503, "fund summary unavailable")
		return
	}
	c.JSON(200, gin.H{"currency": "USD", "posted_balance": posted.String(), "held_amount": held.String(), "available_balance": posted.Sub(held).String(), "movements": sums, "net_change_usd": totals.NetChange.String(), "refund_usd": totals.Refunds.String(), "usage_debit_usd": totals.UsageDebits.String(), "net_usage_usd": totals.UsageDebits.Sub(totals.Refunds).String(), "balance_scope": "current organization, all user-credit shards", "history_scope": "tracked committed movements; earlier history may be incomplete", "balance_after_semantics": "per-operation organization balance is not reconstructed"})
}
func fundCSVSafe(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 0 && strings.ContainsAny(trimmed[:1], "=+-@") {
		return "'" + s
	}
	return s
}
func (s *Server) exportFundActivities(c *gin.Context) {
	f, e := fundFilter(c, 1000)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	rows, e := ledger.ListMovements(s.db, org, f)
	if e != nil {
		fail(c, 503, "export unavailable")
		return
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"id", "operation_id", "occurred_at", "recorded_at", "type", "direction", "status", "currency", "amount", "posted_delta", "held_delta", "available_delta", "ref_type", "ref_id", "quality", "original_journal_id", "original_hold_id", "correction_proposal_id", "correction_review_id"})
	for _, r := range rows {
		occurred := ""
		if r.OccurredAt != nil {
			occurred = r.OccurredAt.Format(time.RFC3339)
		}
		_ = w.Write([]string{fmt.Sprint(r.ID), fmt.Sprint(r.OperationID), occurred, r.CreatedAt.Format(time.RFC3339), fundCSVSafe(r.Type), r.Direction, r.Status, r.Currency, r.Amount.String(), r.PostedDelta.String(), r.HeldDelta.String(), r.AvailableDelta.String(), fundCSVSafe(r.RefType), fundCSVSafe(r.RefID), r.Quality, fundOptionalID(r.OriginalJournalID), fundOptionalID(r.OriginalHoldID), fundOptionalID(r.CorrectionProposalID), fundOptionalID(r.CorrectionReviewID)})
	}
	w.Flush()
	if w.Error() != nil {
		fail(c, 503, "export unavailable")
		return
	}
	// JSON transport permits authenticated downloads through the existing API
	// helper; the caller saves csv verbatim and can continue with next_before.
	var next uint64
	if len(rows) == 1000 {
		next = rows[len(rows)-1].ID
	}
	c.JSON(200, gin.H{"filename": "fund-movements.csv", "csv": b.String(), "rows": len(rows), "next_before": next, "limit": 1000})
}

func fundOptionalID(v *uint64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatUint(*v, 10)
}

type fundExportJob struct {
	ID             uint64    `gorm:"primaryKey" json:"id"`
	OrgID          uint64    `json:"-"`
	UserID         uint64    `json:"-"`
	IdempotencyKey string    `json:"-"`
	RequestHash    string    `json:"-"`
	FilterJSON     string    `gorm:"column:filter_json" json:"-"`
	MaxMovementID  uint64    `json:"snapshot_max_movement_id"`
	CursorID       uint64    `json:"-"`
	RowCount       int       `json:"row_count"`
	Status         string    `json:"status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	CSVData        string    `gorm:"column:csv_data" json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (fundExportJob) TableName() string { return "ledger_export_jobs" }

type fundExportRequest struct {
	Type      string `json:"type"`
	Direction string `json:"direction"`
	Status    string `json:"status"`
	RefID     string `json:"ref_id"`
	From      string `json:"from"`
	Until     string `json:"until"`
	RefType   string `json:"ref_type"`
	Keyword   string `json:"keyword"`
}

func (s *Server) createFundExport(c *gin.Context) {
	claims := mustClaims(c)
	var in fundExportRequest
	key := c.GetHeader("Idempotency-Key")
	if claims.OrgID == 0 || claims.UserID == 0 || videoAdminDecode(c, &in) != nil || len(key) == 0 || len(key) > 128 {
		fail(c, 422, "export filters and Idempotency-Key required")
		return
	}
	// Reuse the list parser without permitting caller-supplied organization or
	// snapshot bounds. Each export freezes the maximum immutable movement ID.
	query := c.Request.URL.Query()
	for k, v := range map[string]string{"type": in.Type, "direction": in.Direction, "status": in.Status, "ref_id": in.RefID, "from": in.From, "until": in.Until, "ref_type": in.RefType, "keyword": in.Keyword} {
		query.Set(k, v)
	}
	query.Del("before")
	c.Request.URL.RawQuery = query.Encode()
	f, e := fundFilter(c, 1000)
	if e != nil {
		fail(c, 422, "invalid export filters")
		return
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var job fundExportJob
	e = s.db.Transaction(func(tx *gorm.DB) error {
		old := tx.Where("org_id = ? AND user_id = ? AND idempotency_key = ?", claims.OrgID, claims.UserID, key).Take(&job).Error
		if old == nil {
			if job.RequestHash != hash {
				return ledger.ErrOperationConflict
			}
			return nil
		}
		if !errors.Is(old, gorm.ErrRecordNotFound) {
			return old
		}
		var max uint64
		if e := tx.Model(&ledger.Movement{}).Where("org_id = ? AND account_type = ? AND type <> ?", claims.OrgID, ledger.AccountUserCredit, "internal_transfer").Select("COALESCE(MAX(id),0)").Scan(&max).Error; e != nil {
			return e
		}
		filter, _ := json.Marshal(f)
		now := s.now().UTC()
		job = fundExportJob{OrgID: claims.OrgID, UserID: claims.UserID, IdempotencyKey: key, RequestHash: hash, FilterJSON: string(filter), MaxMovementID: max, Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
		if e := tx.Create(&job).Error; e != nil {
			return e
		}
		return tx.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", claims.UserID), Action: "fund.export.create", Target: fmt.Sprintf("org:%d/job:%d", claims.OrgID, job.ID), Detail: "USD snapshot; filter_sha256=" + hash, CreatedAt: now.UnixMilli()}).Error
	})
	if e != nil {
		fail(c, 409, "export request could not be recorded")
		return
	}
	c.JSON(202, job)
}
func (s *Server) ownedFundExport(c *gin.Context) (fundExportJob, bool) {
	var job fundExportJob
	id, e := strconv.ParseUint(c.Param("id"), 10, 64)
	claims := mustClaims(c)
	if e != nil || id == 0 || claims.OrgID == 0 || claims.UserID == 0 || s.db.Where("id = ? AND org_id = ? AND user_id = ? AND expires_at > ?", id, claims.OrgID, claims.UserID, s.now().UTC()).Take(&job).Error != nil {
		fail(c, 404, "export not found or expired")
		return job, false
	}
	return job, true
}
func (s *Server) getFundExport(c *gin.Context) {
	if job, ok := s.ownedFundExport(c); ok {
		c.JSON(200, job)
	}
}
func (s *Server) downloadFundExport(c *gin.Context) {
	job, ok := s.ownedFundExport(c)
	if !ok {
		return
	}
	if job.Status != "success" {
		fail(c, 409, "export is not ready")
		return
	}
	if e := s.db.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", job.UserID), Action: "fund.export.download", Target: fmt.Sprintf("org:%d/job:%d", job.OrgID, job.ID), Detail: "USD financial metadata", CreatedAt: s.now().UnixMilli()}).Error; e != nil {
		fail(c, 503, "export audit unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"filename": fmt.Sprintf("fund-movements-%d.csv", job.ID), "csv": job.CSVData, "rows": job.RowCount, "currency": "USD", "snapshot_at": job.CreatedAt})
}

// RunFundExportJobs resumes durable pending jobs after process restart. One
// chunk is a bounded DB transaction; no network calls or financial writes.
func (s *Server) RunFundExportJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var ids []uint64
			if s.db.WithContext(ctx).Model(&fundExportJob{}).Where("status IN ? AND expires_at > ?", []string{"pending", "processing"}, s.now().UTC()).Order("id").Limit(4).Pluck("id", &ids).Error == nil {
				for _, id := range ids {
					if ctx.Err() != nil {
						return
					}
					_ = s.processFundExportChunk(ctx, id)
				}
			}
			// Remove expired CSV contents, keeping only the audited job metadata.
			_ = s.db.WithContext(ctx).Model(&fundExportJob{}).Where("expires_at <= ? AND status <> ?", s.now().UTC(), "expired").Updates(map[string]any{"status": "expired", "csv_data": "", "updated_at": s.now().UTC()}).Error
		}
	}
}
func (s *Server) processFundExportChunk(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job fundExportJob
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&job).Error; e != nil {
			return e
		}
		if job.Status != "pending" && job.Status != "processing" {
			return nil
		}
		if !job.ExpiresAt.After(s.now().UTC()) {
			return nil
		}
		var f ledger.ActivityFilter
		if json.Unmarshal([]byte(job.FilterJSON), &f) != nil {
			return ledger.ErrInvalidInput
		}
		f.Limit = 1000
		f.Before = job.CursorID
		rows, e := ledger.ListMovements(tx.Where("id <= ?", job.MaxMovementID), job.OrgID, f)
		if e != nil {
			return e
		}
		// ListMovements orders descending, so use a descending cursor from the
		// snapshot on subsequent chunks (first chunk excludes all newer inserts).

		return s.appendFundExportChunk(tx, &job, rows)
	})
}
func (s *Server) appendFundExportChunk(tx *gorm.DB, job *fundExportJob, rows []ledger.Movement) error {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if job.RowCount == 0 {
		_ = w.Write([]string{"id", "operation_id", "occurred_at", "recorded_at", "type", "direction", "status", "currency", "amount", "posted_delta", "held_delta", "available_delta", "ref_type", "ref_id", "quality", "original_journal_id", "original_hold_id", "correction_proposal_id", "correction_review_id"})
	}
	for _, r := range rows {
		occurred := ""
		if r.OccurredAt != nil {
			occurred = r.OccurredAt.Format(time.RFC3339)
		}
		_ = w.Write([]string{fmt.Sprint(r.ID), fmt.Sprint(r.OperationID), occurred, r.CreatedAt.Format(time.RFC3339), fundCSVSafe(r.Type), r.Direction, r.Status, "USD", r.Amount.String(), r.PostedDelta.String(), r.HeldDelta.String(), r.AvailableDelta.String(), fundCSVSafe(r.RefType), fundCSVSafe(r.RefID), r.Quality, fundOptionalID(r.OriginalJournalID), fundOptionalID(r.OriginalHoldID), fundOptionalID(r.CorrectionProposalID), fundOptionalID(r.CorrectionReviewID)})
	}
	w.Flush()
	if w.Error() != nil {
		return w.Error()
	}
	job.CSVData += b.String()
	job.RowCount += len(rows)
	job.UpdatedAt = s.now().UTC()
	job.Status = "processing"
	if len(rows) < 1000 {
		job.Status = "success"
	}
	if len(rows) > 0 {
		job.CursorID = rows[len(rows)-1].ID
	}
	if len(job.CSVData) > 14<<20 || job.RowCount > 100000 {
		job.Status = "failed"
		job.ErrorCode = "export_limit_exceeded"
		job.CSVData = ""
	}
	return tx.Save(job).Error
}
