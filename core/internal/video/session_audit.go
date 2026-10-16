package video

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func auditRequestOrg(c *gin.Context) uint64 {
	if key := requestKey(c); key != nil {
		return key.OrgID
	}
	value, _ := c.Get("video_session_audit_org")
	org, _ := value.(uint64)
	return org
}

// SessionAudit must follow authenticated active-membership middleware. resolve
// reads only trusted server context. It never creates or impersonates an API Key.
func (s *Service) SessionAudit(resolve func(*gin.Context) (orgID, userID uint64, err error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		org, user, err := resolve(c)
		if err != nil || org == 0 || user == 0 {
			apiError(c, 403, "forbidden")
			return
		}
		rid, err := randomID("")
		if err != nil {
			apiError(c, 503, "audit_unavailable")
			return
		}
		c.Set("video_request_id", rid)
		c.Set("video_session_audit_org", org)
		c.Header("X-Request-Id", rid)
		c.Header("Cache-Control", "private, no-store")
		start := invocation.VideoRequestStart{RequestID: rid, OrgID: &org, UserID: &user, AuthOutcome: "authenticated", Method: c.Request.Method, Route: c.FullPath(), Operation: requestOperation(c.Request.Method, c.FullPath()), StartedAt: s.Now().UTC()}
		start.ResourceType, start.ResourceID = requestResource(c)
		if c.Request.ContentLength > 0 {
			start.RequestBytes = c.Request.ContentLength
		}
		if e := s.DB.Transaction(func(tx *gorm.DB) error { return s.Evidence.RequestStartTx(tx, start) }); e != nil {
			apiError(c, 503, "audit_unavailable")
			return
		}
		original := c.Writer
		buffered := &auditWriter{ResponseWriter: original}
		stream := strings.HasSuffix(c.FullPath(), "/preview") || strings.HasSuffix(c.FullPath(), "/content")
		if !stream {
			c.Writer = buffered
		}
		defer func() {
			if recover() != nil {
				if stream && c.Writer.Written() {
					c.Set("video_stream_incomplete", true)
				} else {
					buffered.body.Reset()
					buffered.status = 0
					apiError(c, 500, "internal_error")
				}
			}
			status, size := c.Writer.Status(), c.Writer.Size()
			if size < 0 {
				size = 0
			}
			outcome := "completed"
			if stream && (c.GetBool("video_stream_incomplete") || c.Request.Context().Err() != nil) {
				outcome, status = "incomplete", 0
			}
			e := s.DB.Transaction(func(tx *gorm.DB) error {
				if e := s.finishRequestContextTx(tx, c); e != nil {
					return e
				}
				ref := ""
				if !stream {
					var e error
					ref, e = s.captureResponseTx(tx, org, rid, buffered.body.Bytes())
					if e != nil {
						return e
					}
				}
				return s.Evidence.RequestFinishTx(tx, invocation.VideoRequestFinish{RequestID: rid, OrgID: &org, HTTPStatus: status, ResponseBytes: int64(size), ResponsePayloadID: ref, SafeErrorCode: c.GetString("video_error"), Outcome: outcome, FinishedAt: s.Now().UTC()})
			})
			c.Writer = original
			if e != nil {
				s.Log.Error("video session request finish not persisted", "request_id", rid)
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
		contentType := strings.ToLower(c.ContentType())
		if contentType != "" && contentType != "application/json" {
			c.Set("video_skip_request_body", true)
		}
		if e := s.captureReceivedRequest(c); e != nil {
			respondError(c, e)
			return
		}
		c.Next()
	}
}
