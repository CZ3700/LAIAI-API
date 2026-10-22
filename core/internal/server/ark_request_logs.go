package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

// org must be the authenticated, active-membership group. su must be the
// superadmin group; its additional individual-session check rejects shared keys.
func (s *Server) registerArkRequestLogs(org, su *gin.RouterGroup) {
	register := func(group *gin.RouterGroup, internal bool) {
		g := group.Group("/video/request-logs", s.arkRequestLogActor(internal))
		g.GET("", s.arkRequestLogList)
		g.GET("/:request_id", s.arkRequestLogDetail)
		g.GET("/:request_id/events", s.arkRequestLogEvents)
		g.GET("/:request_id/attempts", s.arkRequestLogAttempts)
		g.POST("/:request_id/payload-reads", s.arkRequestLogPayload)
	}
	if org != nil {
		register(org, false)
	}
	if su != nil {
		register(su, true)
	}
}

func (s *Server) arkRequestLogActor(internal bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		if s.video == nil || s.video.Evidence == nil {
			arkLogError(c, video.ErrUnavailable)
			return
		}
		var org, actor uint64
		if internal {
			u, ok := s.videoAdminActor(c)
			if !ok {
				return
			}
			actor = u.ID
			var e error
			org, e = strconv.ParseUint(c.Query("org_id"), 10, 64)
			if e != nil || org == 0 {
				arkLogError(c, video.ErrInvalid)
				return
			}
		} else {
			v, ok := c.Get("claims")
			if !ok {
				arkLogError(c, video.ErrForbidden)
				return
			}
			claims, ok := v.(*identity.SessionClaims)
			if !ok {
				arkLogError(c, video.ErrForbidden)
				return
			}
			org, actor = claims.OrgID, claims.UserID
			if e := identity.VerifyActiveMembership(s.db.WithContext(c.Request.Context()), actor, org); e != nil {
				arkLogError(c, video.ErrForbidden)
				return
			}
		}
		c.Set("ark_log_org", org)
		c.Set("ark_log_actor", actor)
		c.Set("ark_log_internal", internal)
		c.Request = c.Request.WithContext(video.WithRequestLogActor(c.Request.Context(), org, actor, internal))
		c.Next()
	}
}
func arkLogOrg(c *gin.Context) uint64 { return c.MustGet("ark_log_org").(uint64) }
func arkLogError(c *gin.Context, e error) {
	status, code := 503, "temporarily_unavailable"
	switch {
	case errors.Is(e, video.ErrInvalid), errors.Is(e, invocation.ErrVideoInvalid):
		status, code = 422, "invalid_request"
	case errors.Is(e, video.ErrForbidden), errors.Is(e, invocation.ErrVideoAccessDenied):
		status, code = 403, "forbidden"
	case errors.Is(e, video.ErrNotFound), errors.Is(e, gorm.ErrRecordNotFound), errors.Is(e, invocation.ErrVideoNotFound):
		status, code = 404, "not_found"
	case errors.Is(e, invocation.ErrVideoExpired):
		status, code = 410, "content_expired"
	case errors.Is(e, invocation.ErrVideoRetentionDenied):
		status, code = 409, "history_disabled"
	}
	c.AbortWithStatusJSON(status, gin.H{"error": code, "code": code})
}
func arkLogLimit(c *gin.Context) (int, error) {
	n, e := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if e != nil || n < 1 || n > 100 {
		return 0, video.ErrInvalid
	}
	return n, nil
}

func (s *Server) arkRequestLogList(c *gin.Context) {
	f := video.RequestLogFilter{RequestID: c.Query("request_id"), TaskID: c.Query("task_id"), Model: c.Query("model"), Operation: c.DefaultQuery("operation", "create_task"), TaskStatus: c.Query("task_status"), BillingState: c.Query("billing_state")}
	if f.Operation == "all" {
		f.Operation = ""
	}
	for _, v := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if raw := c.Query(v.name); raw != "" {
			t, e := time.Parse(time.RFC3339Nano, raw)
			if e != nil {
				arkLogError(c, video.ErrInvalid)
				return
			}
			*v.dst = &t
		}
	}
	if raw := c.Query("api_key_id"); raw != "" {
		n, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || n == 0 {
			arkLogError(c, video.ErrInvalid)
			return
		}
		f.APIKeyID = n
	}
	if raw := c.Query("http_status"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 100 || n > 599 {
			arkLogError(c, video.ErrInvalid)
			return
		}
		f.HTTPStatus = n
	}
	limit, e := arkLogLimit(c)
	if e != nil {
		arkLogError(c, e)
		return
	}
	v, e := s.video.ListRequestLogs(c.Request.Context(), arkLogOrg(c), f, c.Query("cursor"), limit)
	if e != nil {
		arkLogError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) arkRequestLogDetail(c *gin.Context) {
	v, e := s.video.RequestLogDetail(c.Request.Context(), arkLogOrg(c), c.Param("request_id"), c.GetBool("ark_log_internal"))
	if e != nil {
		arkLogError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) arkRequestLogEvents(c *gin.Context)   { s.arkRequestLogExecution(c, "events") }
func (s *Server) arkRequestLogAttempts(c *gin.Context) { s.arkRequestLogExecution(c, "attempts") }
func (s *Server) arkRequestLogExecution(c *gin.Context, kind string) {
	limit, e := arkLogLimit(c)
	if e != nil {
		arkLogError(c, e)
		return
	}
	v, e := s.video.RequestExecution(c.Request.Context(), arkLogOrg(c), c.Param("request_id"), kind, c.Query("cursor"), limit, c.GetBool("ark_log_internal"))
	if e != nil {
		arkLogError(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *Server) arkRequestLogPayload(c *gin.Context) {
	var in struct {
		Section    string `json:"section"`
		AttemptID  string `json:"attempt_id"`
		ReasonCode string `json:"reason_code"`
	}
	if e := videoAdminDecode(c, &in); e != nil {
		arkLogError(c, video.ErrInvalid)
		return
	}
	if in.ReasonCode != "request_debugging" && in.ReasonCode != "billing_review" && in.ReasonCode != "customer_support" {
		arkLogError(c, video.ErrInvalid)
		return
	}
	switch in.Section {
	case invocation.VideoClientRequest, invocation.VideoClientResponse, invocation.VideoEffectiveRequest, invocation.VideoUpstreamResponse:
	default:
		arkLogError(c, video.ErrInvalid)
		return
	}
	role := invocation.VideoRoleOrgReader
	if c.GetBool("ark_log_internal") {
		role = invocation.VideoRoleRoot
	}
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		arkLogError(c, video.ErrUnavailable)
		return
	}
	accessID := "logread_" + hex.EncodeToString(id[:])
	actor := c.MustGet("ark_log_actor").(uint64)
	// Audit contains only IDs and enumerated purpose. It is durable before any
	// decryption, including denied attempts; the store additionally audits each read.
	audit := func(outcome string) error {
		return s.db.Create(&AuditLog{Actor: fmt.Sprintf("user#%d", actor), Action: "video.request_history." + outcome, Target: accessID, Detail: fmt.Sprintf("org=%d;request=%s;section=%s;reason=%s", arkLogOrg(c), c.Param("request_id"), in.Section, in.ReasonCode), CreatedAt: s.now().UnixMilli()}).Error
	}
	if !arkBillValidID(c.Param("request_id"), 64) {
		arkLogError(c, video.ErrInvalid)
		return
	}
	if e := audit("started"); e != nil {
		arkLogError(c, video.ErrUnavailable)
		return
	}
	v, e := s.video.ReadRequestHistory(c.Request.Context(), arkLogOrg(c), c.Param("request_id"), in.Section, in.AttemptID, invocation.VideoAccess{ActorRole: role, ActorID: fmt.Sprintf("user_%d", actor), ReasonCode: in.ReasonCode, OperationID: accessID})
	outcome := "succeeded"
	if e != nil {
		outcome = "denied"
	}
	if auditErr := audit(outcome); auditErr != nil {
		clear(v.Body)
		arkLogError(c, video.ErrUnavailable)
		return
	}
	if e != nil {
		arkLogError(c, e)
		return
	}
	defer clear(v.Body)
	c.JSON(200, v)
}
