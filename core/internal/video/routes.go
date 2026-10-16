package video

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

func (s *Service) wrap(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) { s.Middleware()(withNext(c, handler)) }
}
func (s *Service) existing(official, legacy gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Use a tenant-scoped lookup only to select the protocol. Both selected
		// handlers still perform their own authenticated resource authorization.
		auth := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if key, e := identity.VerifyKey(s.DB, auth, s.Now()); e == nil {
			var row task.Task
			if e = s.DB.Select("provider").Where("org_id = ? AND task_id = ?", key.OrgID, c.Param("id")).First(&row).Error; e == nil && row.Provider != Provider {
				legacy(c)
				return
			}
		}
		s.wrap(official)(c)
	}
}

func (s *Service) RegisterRoutes(r *gin.RouterGroup, legacy *task.Service) {
	r.POST("/v1/video/generations", s.LegacyCreate(legacy.HandleCreate))
	r.GET("/v1/video/generations/:id", s.existing(s.HandleGet, legacy.HandleGet))
	r.GET("/v1/video/generations/:id/content", s.existing(s.HandleContent, legacy.HandleContent))
	g := r.Group("/v1/video", s.Middleware())
	g.GET("/models", s.HandleModels)
	g.POST("/estimates", s.HandleEstimate)
	g.GET("/generations", s.HandleList)
	g.POST("/generations/:id/cancel", s.HandleCancel)
	g.DELETE("/generations/:id", s.HandleDelete)
	g.GET("/generations/:id/billing", s.HandleBilling)
	g.GET("/generations/:id/events", s.HandleEvents)
	g.GET("/operations/:operation_id", func(c *gin.Context) {
		org := requestKey(c).OrgID
		id := c.Param("operation_id")
		op, e := s.GetOperation(org, id)
		if e == nil {
			c.JSON(200, op)
			return
		}
		if !isNotFound(e) || s.Assets == nil {
			respondError(c, e)
			return
		}
		assetOp, e := s.Assets.GetOperation(c.Request.Context(), org, id)
		if e != nil {
			respondError(c, ErrNotFound)
			return
		}
		c.JSON(200, assetOp)
	})
	if s.Assets != nil {
		s.Assets.RegisterRoutes(g)
		r.GET(videoasset.PortraitCallbackPath, s.publicAudit(s.Assets.HandlePortraitCallback))
	}
}

func (s *Service) publicAudit(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		rid, e := randomID("")
		if e != nil {
			apiError(c, 503, "provider_unavailable")
			return
		}
		c.Set("video_request_id", rid)
		c.Header("X-Request-Id", rid)
		if e = s.DB.Transaction(func(tx *gorm.DB) error {
			return s.Evidence.RequestStartTx(tx, invocation.VideoRequestStart{RequestID: rid, AuthOutcome: "anonymous", Method: c.Request.Method, Route: c.FullPath(), Operation: "portrait_callback", StartedAt: s.Now().UTC()})
		}); e != nil {
			apiError(c, 503, "provider_unavailable")
			return
		}
		original := c.Writer
		buffer := &auditWriter{ResponseWriter: original}
		c.Writer = buffer
		next(c)
		c.Writer = original
		if e = s.DB.Transaction(func(tx *gorm.DB) error {
			return s.Evidence.RequestFinishTx(tx, invocation.VideoRequestFinish{RequestID: rid, HTTPStatus: buffer.Status(), ResponseBytes: int64(buffer.Size()), Outcome: "completed", FinishedAt: s.Now().UTC()})
		}); e != nil {
			c.Header("Location", "")
			apiError(c, 503, "provider_unavailable")
			return
		}
		original.WriteHeader(buffer.Status())
		_, _ = original.Write(buffer.body.Bytes())
	}
}

// With the official gateway disabled, a historical Ark spelling must not
// silently fall through into the old retry/refund path.
func DisabledOfficialCreate(legacy gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, e := io.ReadAll(io.LimitReader(c.Request.Body, 10<<20+1))
		if e != nil || len(raw) > 10<<20 {
			c.AbortWithStatusJSON(413, gin.H{"error": gin.H{"code": "invalid_request", "message": "Request body too large.", "retryable": false}, "request_id": ""})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			for name, value := range fields {
				if strings.EqualFold(name, "model") {
					var model string
					_ = json.Unmarshal(value, &model)
					m := strings.ToLower(model)
					if strings.Contains(m, "seedance-2") || strings.Contains(m, "seedance_2") || strings.Contains(m, "seedance2") {
						c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "provider_unavailable", "message": "Official video gateway is not configured.", "retryable": false}, "request_id": ""})
						return
					}
				}
			}
		}
		legacy(c)
	}
}
