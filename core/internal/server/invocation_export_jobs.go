package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	invocationJobMaxBytes = 128 << 20
	invocationJobBatch    = 1000
	invocationJobLifetime = 24 * time.Hour
)

var (
	invocationExportKey      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	errInvocationJobCapacity = errors.New("invocation export queue full")
	errInvocationJobConflict = errors.New("invocation export idempotency conflict")
)

type invocationJobSpec struct {
	Query      invocationQuerySpec
	Money      exportMoneySnapshot
	RateSource string
}

type invocationExportJob struct {
	ID, OrgID, UserID, MaxLogID, CursorID uint64
	RequestKey, FilterHash, SpecJSON      string
	SliceUntil                            time.Time
	CursorTime                            *time.Time
	RowCount, ByteCount                   int64
	ChunkCount                            int
	Status, ErrorCode                     string
	CreatedAt, UpdatedAt, ExpiresAt       time.Time
}

func (invocationExportJob) TableName() string { return "invocation_export_jobs" }

type invocationExportChunk struct {
	JobID uint64
	Seq   int
	Body  []byte
}

func (invocationExportChunk) TableName() string { return "invocation_export_chunks" }

func (s *Server) invocationJobDB(ctx context.Context) *gorm.DB {
	return s.db.Session(&gorm.Session{Context: ctx, Logger: logger.Discard})
}

func (j invocationExportJob) view(now time.Time) (gin.H, error) {
	var spec invocationJobSpec
	if err := json.Unmarshal([]byte(j.SpecJSON), &spec); err != nil {
		return nil, err
	}
	status := j.Status
	if !j.ExpiresAt.After(now) {
		status = "expired"
	}
	scannedSince := j.SliceUntil
	if j.CursorTime != nil {
		scannedSince = j.CursorTime.UTC()
	}
	return gin.H{
		"id": strconv.FormatUint(j.ID, 10), "status": status, "error_code": j.ErrorCode,
		"row_count": j.RowCount, "byte_count": j.ByteCount, "created_at": j.CreatedAt,
		"updated_at": j.UpdatedAt, "expires_at": j.ExpiresAt, "scanned_since": scannedSince,
		"filters":          spec.Query.wireFilters(),
		"display_currency": spec.Money.Currency,
	}, nil
}

func (s *Server) registerInvocationExportJobs(org *gin.RouterGroup) {
	g := org.Group("/usage/export-jobs", func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		if _, _, ok := invocationQueryIdentity(c, false); !ok {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	g.POST("", s.createInvocationExportJob)
	g.GET("", s.listInvocationExportJobs)
	g.GET("/:id/download", s.downloadInvocationExportJob)
}

func (s *Server) createInvocationExportJob(c *gin.Context) {
	key := c.GetHeader("Idempotency-Key")
	if !invocationExportKey.MatchString(key) || len(c.Request.URL.RawQuery) > 8192 {
		invocationQueryFailure(c, invalidInvocationFilter("query", "invalid_export_request"))
		return
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		invocationQueryFailure(c, invalidInvocationFilter("query", "invalid_encoding"))
		return
	}
	spec, err := parseInvocationExport(values, false, s.now())
	if err != nil {
		invocationQueryFailure(c, err)
		return
	}
	if spec.Filter.Since.Nanosecond()%int(time.Millisecond) != 0 || spec.Filter.Until.Nanosecond()%int(time.Millisecond) != 0 {
		invocationQueryFailure(c, invalidInvocationFilter("until", "unsupported_time_precision"))
		return
	}
	actor := mustClaims(c)
	now := s.now().UTC().Truncate(time.Millisecond)
	ctx, cancel := context.WithTimeout(c.Request.Context(), invocationQueryTimeout)
	defer cancel()
	money, rateSource, err := s.invocationMoneySnapshot(ctx, spec.DisplayCurrency)
	if err != nil {
		invocationQueryFailure(c, err)
		return
	}
	jobSpec, err := json.Marshal(invocationJobSpec{Query: spec, Money: *money, RateSource: rateSource})
	if err != nil {
		invocationQueryFailure(c, err)
		return
	}
	hash := spec.filterHash(false, actor.OrgID)
	var job invocationExportJob
	err = s.invocationJobDB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize the capacity decision across Core replicas.
		var admission struct{ ID uint8 }
		if err := tx.Table("invocation_export_admission").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&admission, "id = ?", 1).Error; err != nil {
			return err
		}
		err := tx.Where("org_id = ? AND user_id = ? AND request_key = ?", actor.OrgID, actor.UserID, key).Take(&job).Error
		if err == nil {
			var stored invocationJobSpec
			if json.Unmarshal([]byte(job.SpecJSON), &stored) != nil || job.FilterHash != hash || stored.Money.Currency != spec.DisplayCurrency {
				return errInvocationJobConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		unexpired := func() *gorm.DB { return tx.Model(&invocationExportJob{}).Where("expires_at > ?", now) }
		if err := unexpired().Count(&count).Error; err != nil {
			return err
		}
		if count >= 16 {
			return errInvocationJobCapacity
		}
		if err := unexpired().Where("org_id = ?", actor.OrgID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 4 {
			return errInvocationJobCapacity
		}
		// Capture the committed ID horizon from the primary. Subsequent batches
		// also read the primary so replica lag cannot omit an entire slice.
		var maxID uint64
		if err := tx.Model(&invocation.Invocation{}).Select("COALESCE(MAX(id),0)").Scan(&maxID).Error; err != nil {
			return err
		}
		job = invocationExportJob{OrgID: actor.OrgID, UserID: actor.UserID, RequestKey: key,
			FilterHash: hash, SpecJSON: string(jobSpec), MaxLogID: maxID, SliceUntil: spec.Filter.Until,
			Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(invocationJobLifetime)}
		return tx.Create(&job).Error
	})
	switch {
	case errors.Is(err, errInvocationJobCapacity):
		c.JSON(http.StatusTooManyRequests, gin.H{"code": "export_job_capacity", "error": "invocation export capacity reached"})
	case errors.Is(err, errInvocationJobConflict):
		fail(c, http.StatusConflict, "export request already used")
	case err != nil:
		invocationQueryFailure(c, err)
	default:
		view, err := job.view(now)
		if err != nil {
			invocationQueryFailure(c, err)
			return
		}
		c.JSON(http.StatusAccepted, view)
	}
}

func (s *Server) listInvocationExportJobs(c *gin.Context) {
	actor := mustClaims(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), invocationQueryTimeout)
	defer cancel()
	var jobs []invocationExportJob
	if err := s.invocationJobDB(ctx).Where("org_id = ? AND user_id = ?", actor.OrgID, actor.UserID).
		Order("id DESC").Limit(20).Find(&jobs).Error; err != nil {
		invocationQueryFailure(c, err)
		return
	}
	list := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		view, err := job.view(s.now().UTC())
		if err != nil {
			invocationQueryFailure(c, err)
			return
		}
		list = append(list, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": list})
}

func (s *Server) downloadInvocationExportJob(c *gin.Context) {
	id, err := invocationPositiveInteger(c.Param("id"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	actor := mustClaims(c)
	subject, _, _ := invocationQueryIdentity(c, false)
	release, allowed := s.invocationQueries.tryAcquireExport(subject)
	if !allowed {
		c.Header("Retry-After", "1")
		fail(c, http.StatusTooManyRequests, "invocation export capacity exceeded")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	started := false
	err = s.invocationJobDB(ctx).Transaction(func(tx *gorm.DB) error {
		var job invocationExportJob
		if err := tx.Take(&job, "id = ? AND org_id = ? AND user_id = ? AND expires_at > ?",
			id, actor.OrgID, actor.UserID, s.now().UTC()).Error; err != nil {
			return err
		}
		if job.Status != "ready" {
			c.Status(http.StatusConflict)
			return nil
		}
		if job.ByteCount < 1 || job.ByteCount > invocationJobMaxBytes || job.ChunkCount < 1 {
			return invocation.ErrQueryUnavailable
		}
		c.Header("Content-Disposition", `attachment; filename="invocation-log-`+strconv.FormatUint(id, 10)+`.csv"`)
		c.Header("Content-Type", "text/csv; charset=utf-8")
		size := strconv.FormatInt(job.ByteCount, 10)
		c.Header("Content-Length", size)
		c.Header("X-Invocation-Export-Bytes", size)
		var remaining = job.ByteCount
		for i := 1; i <= job.ChunkCount; i++ {
			var chunk invocationExportChunk
			if err := tx.Take(&chunk, "job_id = ? AND seq = ?", job.ID, i).Error; err != nil {
				return err
			}
			remaining -= int64(len(chunk.Body))
			if remaining < 0 || (i == job.ChunkCount && remaining != 0) {
				return invocation.ErrQueryUnavailable
			}
			started = true
			if _, err := c.Writer.Write(chunk.Body); err != nil {
				return err
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil && !started {
		c.Writer.Header().Del("Content-Length")
		c.Writer.Header().Del("Content-Disposition")
		c.Writer.Header().Del("Content-Type")
		c.Writer.Header().Del("X-Invocation-Export-Bytes")
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Status(http.StatusNotFound)
		} else {
			invocationQueryFailure(c, err)
		}
	}
}

// RunInvocationExportJobs yields between batches. A row lock couples the cursor
// and each CSV write so a restart or leader overlap cannot duplicate data.
func (s *Server) RunInvocationExportJobs(ctx context.Context, isLeader func() bool) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var nextCleanup time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !isLeader() {
				timer.Reset(2 * time.Second)
				continue
			}
			now := s.now().UTC()
			if !now.Before(nextCleanup) {
				s.expireInvocationExportJobs(ctx, now)
				nextCleanup = now.Add(time.Minute)
			}
			if s.processInvocationExportBatch(ctx, now) {
				timer.Reset(250 * time.Millisecond)
			} else {
				timer.Reset(2 * time.Second)
			}
		}
	}
}

func (s *Server) expireInvocationExportJobs(parent context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	db := s.invocationJobDB(ctx)
	var ids []uint64
	if db.Model(&invocationExportJob{}).Where("expires_at <= ?", now).Order("expires_at, id").Limit(2).Pluck("id", &ids).Error == nil && len(ids) != 0 {
		db.Delete(&invocationExportJob{}, "id IN ? AND expires_at <= ?", ids, now)
	}
}

func (s *Server) processInvocationExportBatch(parent context.Context, now time.Time) bool {
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	progress := false
	err := s.invocationJobDB(ctx).Transaction(func(tx *gorm.DB) error {
		var job invocationExportJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status IN ? AND expires_at > ?", []string{"pending", "processing"}, now).
			Order("updated_at, id").Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		progress = true
		return s.writeInvocationExportBatch(ctx, tx, &job, now)
	})
	if err != nil && parent.Err() == nil {
		s.Log().Warn("invocation export batch could not be committed")
	}
	return progress && err == nil
}

func (s *Server) writeInvocationExportBatch(ctx context.Context, tx *gorm.DB, job *invocationExportJob, now time.Time) error {
	failJob := func(code string) error {
		if err := tx.Delete(&invocationExportChunk{}, "job_id = ?", job.ID).Error; err != nil {
			return err
		}
		return tx.Model(job).Updates(map[string]any{"status": "failed", "error_code": code,
			"byte_count": 0, "chunk_count": 0, "updated_at": now}).Error
	}
	var spec invocationJobSpec
	if json.Unmarshal([]byte(job.SpecJSON), &spec) != nil || spec.Money.validate() != nil {
		return failJob("invalid_snapshot")
	}
	filter := spec.Query.Filter
	filter.Until = job.SliceUntil
	filter.Since = filter.Until.Add(-24 * time.Hour)
	if filter.Since.Before(spec.Query.Filter.Since) {
		filter.Since = spec.Query.Filter.Since
	}
	rows, err := invocation.QueryLogExportBatch(ctx, tx, job.OrgID, filter, job.MaxLogID, job.CursorTime, job.CursorID, invocationJobBatch)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failJob("query_failed")
	}
	if len(rows) > 0 || job.ChunkCount == 0 {
		result := invocation.QueryResult{Items: rows, AsOf: job.CreatedAt}
		body, err := encodeInvocationExportChunk(ctx, result, false, &spec.Money, spec.RateSource, job.FilterHash, job.ChunkCount == 0)
		if err != nil {
			return err
		}
		job.ByteCount += int64(len(body))
		if job.ByteCount > invocationJobMaxBytes {
			return failJob("size_exceeded")
		}
		job.ChunkCount++
		job.RowCount += int64(len(rows))
		if err := tx.Create(&invocationExportChunk{JobID: job.ID, Seq: job.ChunkCount, Body: body}).Error; err != nil {
			return err
		}
	}
	job.Status = "processing"
	if len(rows) == invocationJobBatch {
		last := rows[len(rows)-1]
		job.CursorTime, job.CursorID = &last.CreatedAt, last.ID
	} else {
		job.SliceUntil, job.CursorTime, job.CursorID = filter.Since, nil, 0
		if !job.SliceUntil.After(spec.Query.Filter.Since) {
			job.Status = "ready"
		}
	}
	return tx.Model(job).Updates(map[string]any{"status": job.Status, "slice_until": job.SliceUntil,
		"cursor_time": job.CursorTime, "cursor_id": job.CursorID, "row_count": job.RowCount,
		"byte_count": job.ByteCount, "chunk_count": job.ChunkCount, "updated_at": now}).Error
}
