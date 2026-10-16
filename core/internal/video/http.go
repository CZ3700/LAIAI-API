package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

func apiError(c *gin.Context, status int, code string) {
	c.Set("video_error", code)
	publicCode := publicErrorCode(code, status)
	c.AbortWithStatusJSON(status, gin.H{"request_id": c.GetString("video_request_id"), "error": gin.H{"code": publicCode, "message": strings.ReplaceAll(publicCode, "_", " "), "retryable": status == 429 || status == 503}})
}

func publicErrorCode(code string, status int) string {
	switch code {
	case "invalid_request", "unauthenticated", "forbidden", "not_found", "idempotency_conflict", "invalid_state", "insufficient_balance", "quota_exceeded", "rate_limited", "unsupported_capability", "invalid_asset", "authorization_required", "authorization_expired", "group_not_empty", "resource_in_use", "content_expired", "content_not_ready", "provider_unavailable", "submission_uncertain", "generation_failed", "cancellation_failed", "deletion_failed", "internal_error":
		return code
	}
	switch status {
	case 400, 413, 422:
		return "invalid_request"
	case 401:
		return "unauthenticated"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 409:
		return "content_not_ready"
	case 410:
		return "content_expired"
	case 429:
		return "rate_limited"
	case 502, 503, 504:
		return "provider_unavailable"
	default:
		return "internal_error"
	}
}
func respondError(c *gin.Context, err error) {
	var a *ark.Error
	switch {
	case errors.As(err, &a):
		apiError(c, a.HTTPStatus, a.Code)
	case isNotFound(err):
		apiError(c, 404, "not_found")
	case errors.Is(err, ErrForbidden):
		apiError(c, 403, "forbidden")
	case errors.Is(err, ErrInvalid) || errors.Is(err, videobilling.ErrInvalid):
		apiError(c, 422, "invalid_request")
	case errors.Is(err, ErrConflict) || errors.Is(err, videobilling.ErrConflict):
		apiError(c, 409, "idempotency_conflict")
	case errors.Is(err, ErrState):
		apiError(c, 409, "invalid_state")
	case errors.Is(err, ErrCapability) || errors.Is(err, videobilling.ErrPricingUnavailable):
		apiError(c, 422, "unsupported_capability")
	case errors.Is(err, task.ErrConcurrencyLimit):
		apiError(c, 429, "concurrency_limit")
	case errors.Is(err, videobilling.ErrQuotaExceeded):
		apiError(c, 429, "quota_exceeded")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		apiError(c, 402, "insufficient_balance")
	default:
		apiError(c, 503, "temporarily_unavailable")
	}
}

// Buffered JSON responses become visible only after the finish record commits.
// Content streams retain durable start intent and an eventual finished record.
type auditWriter struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *auditWriter) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *auditWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = 200
	}
}
func (w *auditWriter) Write(p []byte) (int, error)       { w.WriteHeaderNow(); return w.body.Write(p) }
func (w *auditWriter) WriteString(p string) (int, error) { return w.Write([]byte(p)) }
func (w *auditWriter) Status() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}
func (w *auditWriter) Size() int     { return w.body.Len() }
func (w *auditWriter) Written() bool { return w.status != 0 }

func hasScope(key *identity.APIKey, scope string) bool {
	for _, v := range strings.FieldsFunc(key.Scopes, func(r rune) bool { return r == ',' || r == ' ' }) {
		if v == scope || v == "video:*" || v == "*" {
			return true
		}
	}
	return false
}

func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID, err := randomID("")
		if err != nil {
			apiError(c, 503, "temporarily_unavailable")
			return
		}
		c.Set("video_request_id", requestID)
		c.Header("X-Request-Id", requestID)
		c.Header("Cache-Control", "private, no-store")
		start := invocation.VideoRequestStart{RequestID: requestID, AuthOutcome: "rejected", Method: c.Request.Method, Route: c.FullPath(), Operation: "video_api", StartedAt: s.Now().UTC()}
		if c.Request.ContentLength > 0 {
			start.RequestBytes = c.Request.ContentLength
		}
		var key *identity.APIKey
		auth := c.GetHeader("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			key, err = identity.VerifyKey(s.DB, strings.TrimPrefix(auth, "Bearer "), s.Now())
		} else {
			err = identity.ErrKeyInvalid
		}
		if err == nil && key != nil {
			start.OrgID = &key.OrgID
			start.APIKeyID = &key.ID
			start.UserID = key.UserID
			start.AuthOutcome = "authenticated"
			c.Set("video_key", key)
		}
		if err := s.DB.Transaction(func(tx *gorm.DB) error { return s.Evidence.RequestStartTx(tx, start) }); err != nil {
			apiError(c, 503, "audit_unavailable")
			return
		}
		original := c.Writer
		buffered := &auditWriter{ResponseWriter: original}
		stream := strings.HasSuffix(c.FullPath(), "/content")
		if !stream {
			c.Writer = buffered
		}
		defer func() {
			if v := recover(); v != nil {
				if !stream {
					buffered.body.Reset()
					buffered.status = 0
				}
				apiError(c, 500, "internal_error")
			}
			status := c.Writer.Status()
			size := c.Writer.Size()
			if size < 0 {
				size = 0
			}
			finishErr := s.DB.Transaction(func(tx *gorm.DB) error {
				return s.Evidence.RequestFinishTx(tx, invocation.VideoRequestFinish{RequestID: requestID, OrgID: start.OrgID, HTTPStatus: status, ResponseBytes: int64(size), SafeErrorCode: c.GetString("video_error"), Outcome: "completed", FinishedAt: s.Now().UTC()})
			})
			c.Writer = original
			if finishErr != nil {
				s.Log.Error("video request finish not persisted", "request_id", requestID)
				if !stream {
					apiError(c, 503, "audit_unavailable")
				}
				return
			}
			if !stream {
				original.WriteHeader(buffered.Status())
				_, _ = original.Write(buffered.body.Bytes())
			}
		}()
		if start.AuthOutcome != "authenticated" {
			apiError(c, 401, "invalid_api_key")
			return
		}
		if !key.AllowsIP(c.ClientIP()) {
			apiError(c, 403, "ip_not_allowed")
			return
		}
		scope := "video:read"
		if c.Request.Method != http.MethodGet {
			scope = "video:write"
		}
		if !hasScope(key, scope) {
			apiError(c, 403, "insufficient_scope")
			return
		}
		if h, ok := c.Get("video_route_handler"); ok {
			h.(gin.HandlerFunc)(c)
		} else {
			c.Next()
		}
	}
}

func requestKey(c *gin.Context) *identity.APIKey {
	v, _ := c.Get("video_key")
	key, _ := v.(*identity.APIKey)
	return key
}

func (s *Service) HandleCreate(c *gin.Context) {
	key := requestKey(c)
	if key == nil {
		apiError(c, 401, "invalid_api_key")
		return
	}
	in, err := ark.Decode(c.Request.Body, ark.DefaultMaxRequestBytes)
	if err != nil {
		respondError(c, err)
		return
	}
	if !key.AllowsModel(in.Model) {
		apiError(c, 403, "model_not_allowed")
		return
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		respondError(c, ErrInvalid)
		return
	}
	previous, replay, err := lookupIdempotency(s.DB.WithContext(c.Request.Context()), s.Secret, key.OrgID, http.MethodPost, "/v1/video/generations", c.GetHeader("Idempotency-Key"), canonical)
	if err != nil {
		respondError(c, err)
		return
	}
	if replay {
		result, err := s.Get(key.OrgID, previous.ResourceID)
		if err != nil {
			respondError(c, err)
			return
		}
		c.Header("Location", "/v1/video/generations/"+result.ID)
		c.Header("Idempotency-Replayed", "true")
		c.JSON(202, result)
		return
	}
	ent, e := identity.LoadEntitlements(s.DB, key.OrgID)
	if e != nil || !ent.FeatureVideo {
		apiError(c, 403, "video_not_enabled")
		return
	}
	if s.Gate == nil {
		apiError(c, 503, "admission_unavailable")
		return
	}
	if code, _, ok := s.Gate.GateKey(key, c.ClientIP(), in.Model); !ok {
		apiError(c, code, "admission_rejected")
		return
	}
	if ok, _ := s.Gate.AllowModelRate(key.OrgID, in.Model); !ok {
		apiError(c, 429, "rate_limit")
		return
	}
	result, replay, err := s.Create(c.Request.Context(), key, c.GetString("video_request_id"), c.GetHeader("Idempotency-Key"), in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Location", "/v1/video/generations/"+result.ID)
	if replay {
		c.Header("Idempotency-Replayed", "true")
	} else {
		c.Header("Idempotency-Replayed", "false")
	}
	c.JSON(202, result)
}
func (s *Service) HandleGet(c *gin.Context) {
	v, e := s.Get(requestKey(c).OrgID, c.Param("id"))
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Service) HandleCancel(c *gin.Context) {
	if b, e := io.ReadAll(io.LimitReader(c.Request.Body, 2)); e != nil || len(bytes.TrimSpace(b)) > 0 {
		apiError(c, 422, "body_not_allowed")
		return
	}
	v, replay, e := s.Cancel(c.Request.Context(), requestKey(c).OrgID, c.Param("id"), c.GetString("video_request_id"), c.GetHeader("Idempotency-Key"))
	if e != nil {
		respondError(c, e)
		return
	}
	if replay {
		c.Header("Idempotency-Replayed", "true")
	}
	c.JSON(202, v)
}
func (s *Service) HandleDelete(c *gin.Context) {
	v, done, e := s.Delete(c.Request.Context(), requestKey(c).OrgID, c.Param("id"), c.GetString("video_request_id"))
	if e != nil {
		respondError(c, e)
		return
	}
	if done {
		c.Status(204)
		return
	}
	c.JSON(202, v)
}
func (s *Service) HandleOperation(c *gin.Context) {
	v, e := s.GetOperation(requestKey(c).OrgID, c.Param("operation_id"))
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Service) HandleEstimate(c *gin.Context) {
	in, e := ark.Decode(c.Request.Body, ark.DefaultMaxRequestBytes)
	if e != nil {
		respondError(c, e)
		return
	}
	key := requestKey(c)
	if !key.AllowsModel(in.Model) {
		apiError(c, 403, "model_not_allowed")
		return
	}
	q, e := s.Estimate(key.OrgID, in)
	if e != nil {
		respondError(c, e)
		return
	}
	c.JSON(200, gin.H{"model": in.Model, "estimated_tokens": q.EstimatedTokens, "maximum_reserved_tokens": q.EstimatedTokens, "estimated_amount": q.HoldUSD.StringFixed(8), "reserved_amount": q.HoldUSD.StringFixed(8), "currency": "USD", "capability_version": q.Metering.CapabilityVersion, "pricing_version": q.Rule.Version, "billing_rule_version": q.Rule.FormulaVersion, "guaranteed_price": false})
}

// LegacyCreate selects the existing adapter for other model families. Any
// official Seedance spelling is sent to the strict decoder, never to fallback.
func (s *Service) LegacyCreate(legacy gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, e := io.ReadAll(io.LimitReader(c.Request.Body, ark.DefaultMaxRequestBytes+1))
		if e != nil || int64(len(raw)) > ark.DefaultMaxRequestBytes {
			s.Middleware()(withNext(c, func(c *gin.Context) { apiError(c, 413, "request_too_large") }))
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		official := false
		// Walk raw members rather than decoding a map: duplicate model keys
		// must not conceal an official model behind a later legacy spelling.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		first, err := decoder.Token()
		if err != nil || first != json.Delim('{') {
			official = true
		}
		for !official && decoder.More() {
			member, err := decoder.Token()
			if err != nil {
				official = true
				break
			}
			k, ok := member.(string)
			var v json.RawMessage
			if !ok || decoder.Decode(&v) != nil {
				official = true
				break
			}
			if strings.EqualFold(k, "model") {
				var m string
				_ = json.Unmarshal(v, &m)
				m = strings.ToLower(strings.TrimSpace(m))
				if strings.Contains(m, "seedance-2") || strings.Contains(m, "seedance_2") || strings.Contains(m, "seedance2") {
					official = true
				}
			}
		}
		if !json.Valid(raw) {
			official = true
		}
		if !official {
			legacy(c)
			return
		}
		s.Middleware()(withNext(c, s.HandleCreate))
	}
}

// A direct wrapper invokes its selected handler without changing Gin indices.
func withNext(c *gin.Context, handler gin.HandlerFunc) *gin.Context {
	c.Set("video_route_handler", handler)
	return c
}

func (s *Service) RecoverRequestLogs(ctxTime time.Time) error {
	var rows []invocation.VideoRequestLog
	if e := s.DB.Where("outcome = ? AND started_at < ?", "started", ctxTime.Add(-10*time.Minute)).Limit(100).Find(&rows).Error; e != nil {
		return e
	}
	for _, r := range rows {
		if e := s.DB.Transaction(func(tx *gorm.DB) error {
			return s.Evidence.RequestFinishTx(tx, invocation.VideoRequestFinish{RequestID: r.RequestID, OrgID: r.OrgID, Outcome: "incomplete", HTTPStatus: 0, SafeErrorCode: "request_interrupted", FinishedAt: ctxTime})
		}); e != nil {
			return e
		}
	}
	return nil
}
