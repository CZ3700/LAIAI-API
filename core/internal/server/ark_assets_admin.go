package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

func (s *Server) registerArkAssetAdmin(su *gin.RouterGroup) {
	// Diagnose a disabled gateway too; mutation routes below still fail closed.
	su.GET("/video/asset-setup", s.getArkAssetSetup)
	// Local project identity registration does not call the official gateway.
	su.POST("/video/asset-projects", s.registerArkAssetProject)
	g := su.Group("/video", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			c.Abort()
			return
		}
		if s.video == nil || s.video.Assets == nil {
			fail(c, 503, "video asset gateway unavailable")
			c.Abort()
			return
		}
		c.Set("video_admin_actor", actor)
	})
	g.POST("/asset-scopes", func(c *gin.Context) {
		actor, _ := s.videoAdminActor(c)
		var in video.AssetScopeRegistration
		if videoAdminDecode(c, &in) != nil {
			fail(c, 422, "invalid registration")
			return
		}
		s.videoAdminReply(c, s.video.RegisterAssetScope(c.Request.Context(), actor, in))
	})
	g.PUT("/asset-scopes/:scope_id/:version", func(c *gin.Context) {
		actor, _ := s.videoAdminActor(c)
		scope, e := strconv.ParseUint(c.Param("scope_id"), 10, 64)
		version, e2 := strconv.ParseUint(c.Param("version"), 10, 64)
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if e != nil || e2 != nil || videoAdminDecode(c, &in) != nil {
			fail(c, 422, "invalid scope")
			return
		}
		s.videoAdminReply(c, s.video.SetAssetScopeEnabled(c.Request.Context(), actor, scope, version, in.Enabled))
	})
	g.PUT("/asset-org-routes", func(c *gin.Context) {
		actor, _ := s.videoAdminActor(c)
		var in video.AssetOrgRoute
		if videoAdminDecode(c, &in) != nil {
			fail(c, 422, "invalid route")
			return
		}
		s.videoAdminReply(c, s.video.SetOrgAssetRoute(c.Request.Context(), actor, in))
	})
	g.POST("/verified-media", func(c *gin.Context) {
		actor, _ := s.videoAdminActor(c)
		var in video.AssetMediaRegistration
		if videoAdminDecode(c, &in) != nil {
			fail(c, 422, "invalid media evidence")
			return
		}
		s.videoAdminReply(c, s.video.RegisterAssetMedia(c.Request.Context(), actor, in))
	})
	g.POST("/verified-callbacks", func(c *gin.Context) {
		actor, _ := s.videoAdminActor(c)
		var in video.AssetCallbackRegistration
		if videoAdminDecode(c, &in) != nil {
			fail(c, 422, "invalid callback evidence")
			return
		}
		s.videoAdminReply(c, s.video.RegisterAssetCallback(c.Request.Context(), actor, in))
	})
	for _, kind := range []string{"media", "callbacks"} {
		kind := kind
		g.POST("/orgs/:org_id/verified-"+kind+"/:resource_id/revoke", func(c *gin.Context) {
			actor, _ := s.videoAdminActor(c)
			org, e := strconv.ParseUint(c.Param("org_id"), 10, 64)
			if e != nil {
				fail(c, 422, "invalid organization")
				return
			}
			if kind == "media" {
				e = s.video.RevokeAssetMedia(c.Request.Context(), actor, org, c.Param("resource_id"))
			} else {
				e = s.video.RevokeAssetCallback(c.Request.Context(), actor, org, c.Param("resource_id"))
			}
			s.videoAdminReply(c, e)
		})
	}
	g.POST("/orgs/:org_id/portrait-authorizations/:session_id/license", s.reviewArkLicense)
	g.POST("/orgs/:org_id/portrait-authorizations/:session_id/license/revoke", s.revokeArkLicense)
}

func (s *Server) getArkAssetSetup(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	org, err := strconv.ParseUint(c.Query("org_id"), 10, 64)
	if err != nil || org == 0 || len(c.QueryArray("org_id")) != 1 {
		fail(c, 422, "invalid organization")
		return
	}
	service := s.video
	if service == nil {
		// This read-only Service needs no secrets or runtime registration.
		service = &video.Service{DB: s.db}
	}
	out, err := service.AssetSetup(c.Request.Context(), actor, org)
	if err != nil {
		if errors.Is(err, video.ErrNotFound) {
			fail(c, 404, "organization not found")
		} else {
			fail(c, 503, "asset setup unavailable")
		}
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}

type arkLicenseReview struct {
	Source        string          `json:"source"`
	ValidFrom     time.Time       `json:"valid_from"`
	ValidUntil    time.Time       `json:"valid_until"`
	ReverifyDueAt time.Time       `json:"reverify_due_at"`
	Evidence      json.RawMessage `json:"evidence"`
}

// Evidence is captured under the reviewing organization's scope in the same
// transaction as the license event. It never enters ordinary audit logs.
func (s *Server) prepareArkLicense(org uint64, body json.RawMessage) (*invocation.PreparedVideoPayload, error) {
	if org == 0 || len(body) < 2 || string(body) == "null" {
		return nil, video.ErrInvalid
	}
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return nil, e
	}
	return s.video.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: hex.EncodeToString(id[:]), Direction: invocation.VideoClientRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: s.now().UTC().Add(30 * 24 * time.Hour), CaptureEnabled: true})
}
func (s *Server) reviewArkLicense(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	org, e := strconv.ParseUint(c.Param("org_id"), 10, 64)
	var in arkLicenseReview
	if e != nil || videoAdminDecode(c, &in) != nil {
		fail(c, 422, "invalid license evidence")
		return
	}
	payload, e := s.prepareArkLicense(org, in.Evidence)
	if e != nil {
		s.videoAdminReply(c, e)
		return
	}
	now := s.now().UTC()
	if in.ReverifyDueAt.After(now.Add(24 * time.Hour)) {
		fail(c, 422, "license evidence must be reviewed within 24 hours")
		return
	}
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		ref, e := s.video.Evidence.StageTx(tx, payload)
		if e != nil {
			return e
		}
		return s.video.Assets.VerifyLicenseTx(tx, org, c.Param("session_id"), videoasset.LicenseEvidence{ActorID: strconv.FormatUint(actor.ID, 10), EvidenceRef: ref, Source: in.Source, Purpose: "video_generation", ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil, VerifiedAt: now, ReverifyDueAt: in.ReverifyDueAt})
	})
	s.videoAdminReply(c, e)
}
func (s *Server) revokeArkLicense(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	org, e := strconv.ParseUint(c.Param("org_id"), 10, 64)
	var in struct {
		Evidence json.RawMessage `json:"evidence"`
	}
	if e != nil || videoAdminDecode(c, &in) != nil {
		fail(c, 422, "invalid revocation evidence")
		return
	}
	payload, e := s.prepareArkLicense(org, in.Evidence)
	if e != nil {
		s.videoAdminReply(c, e)
		return
	}
	e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		ref, e := s.video.Evidence.StageTx(tx, payload)
		if e != nil {
			return e
		}
		return s.video.Assets.RevokeLicenseTx(tx, org, c.Param("session_id"), ref, strconv.FormatUint(actor.ID, 10))
	})
	s.videoAdminReply(c, e)
}
