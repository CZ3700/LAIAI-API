package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) assetConsoleCaller(c *gin.Context, write bool) (videoasset.CreateContext, bool) {
	if s.video == nil || s.video.Assets == nil {
		fail(c, 503, "video asset gateway unavailable")
		return videoasset.CreateContext{}, false
	}
	claims := mustClaims(c)
	var user identity.User
	if s.db.WithContext(c.Request.Context()).Select("id", "role", "status").Where("id = ? AND status = ?", claims.UserID, identity.StatusActive).Take(&user).Error != nil {
		fail(c, 403, "named active user required")
		return videoasset.CreateContext{}, false
	}
	role, e := identity.RoleIn(s.db.WithContext(c.Request.Context()), claims.OrgID, claims.UserID)
	if e != nil || write && role == identity.RoleBilling {
		fail(c, 403, "asset permission required")
		return videoasset.CreateContext{}, false
	}
	var raw [16]byte
	if _, e = rand.Read(raw[:]); e != nil {
		fail(c, 503, "request ID unavailable")
		return videoasset.CreateContext{}, false
	}
	id := c.GetString("video_request_id")
	if id == "" {
		id = hex.EncodeToString(raw[:])
	}
	c.Header("X-Request-Id", id)
	a := videoasset.CreateContext{CallerKind: "session_user", OrgID: claims.OrgID, UserID: claims.UserID, RequestID: id, IdempotencyKey: c.GetHeader("Idempotency-Key"), Superadmin: user.Role == identity.PlatRoleSuperadmin}
	c.Request = c.Request.WithContext(videoasset.WithCaller(c.Request.Context(), a))
	return a, true
}
func assetConsoleError(c *gin.Context, e error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(e, videoasset.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(e, videoasset.ErrInvalid):
		status, code = 422, "invalid_request"
	case errors.Is(e, videoasset.ErrConflict):
		status, code = 409, "invalid_state"
	case errors.Is(e, videoasset.ErrInUse):
		status, code = 409, "resource_in_use"
	case errors.Is(e, videoasset.ErrNotEmpty):
		status, code = 409, "group_not_empty"
	case errors.Is(e, videoasset.ErrUnsupported):
		status, code = 422, "unsupported_capability"
	case errors.Is(e, videoasset.ErrUnconfigured):
		status, code = 503, "portrait_not_configured"
	case errors.Is(e, videoasset.ErrCallbackNotRegistered):
		status, code = 422, "callback_not_registered"
	case errors.Is(e, videoasset.ErrSecret), errors.Is(e, videoasset.ErrAuthorization):
		status, code = 403, "authorization_required"
	case errors.Is(e, videoasset.ErrStorage):
		status, code = 503, "storage_unavailable"
	}
	c.JSON(status, gin.H{"error": code, "code": code})
}
func assetConsoleFilter(c *gin.Context) videoasset.ListFilter {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	return videoasset.ListFilter{Kind: c.Query("kind"), GroupID: c.Query("group_id"), Name: c.Query("name"), MediaType: c.Query("media_type"), Status: c.Query("status"), ProviderStatus: c.Query("provider_status"), Cursor: c.Query("cursor"), Limit: limit}
}

// org must already authenticate a live user and active organization membership.
// su must already require superadmin; privileged handlers additionally require a
// named actor, so a break-glass root token cannot silently claim external groups.
func (s *Server) registerArkAssetsConsole(org, su, mediaOrg *gin.RouterGroup) {
	g := org.Group("/video")
	media := mediaOrg.Group("/video")
	if s.video != nil {
		audit := s.video.SessionAudit(func(c *gin.Context) (uint64, uint64, error) {
			claims := mustClaims(c)
			return claims.OrgID, claims.UserID, nil
		})
		g.Use(audit)
		media.Use(audit)
	}
	g.GET("/asset-capabilities", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		c.JSON(200, s.video.Assets.ConsoleCapabilities(c.Request.Context(), a.OrgID))
	})
	g.GET("/asset-groups", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.ConsoleGroups(c.Request.Context(), a.OrgID, assetConsoleFilter(c))
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/asset-groups", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var in videoasset.GroupCreate
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		out, replay, e := s.video.Assets.CreateGroup(c.Request.Context(), a, in)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.Header("Idempotency-Replayed", strconv.FormatBool(replay))
		c.JSON(202, out)
	})
	for _, resource := range []string{"asset-groups", "assets"} {
		resource := resource
		g.PATCH("/"+resource+"/:id", func(c *gin.Context) {
			a, ok := s.assetConsoleCaller(c, true)
			if !ok {
				return
			}
			var in videoasset.Rename
			if videoAdminDecode(c, &in) != nil {
				assetConsoleError(c, videoasset.ErrInvalid)
				return
			}
			out, e := s.video.Assets.RenameResource(c.Request.Context(), a, c.Param("id"), resource == "asset-groups", in)
			if e != nil {
				assetConsoleError(c, e)
				return
			}
			c.JSON(202, out)
		})
		g.DELETE("/"+resource+"/:id", func(c *gin.Context) {
			a, ok := s.assetConsoleCaller(c, true)
			if !ok {
				return
			}
			var out videoasset.OperationDTO
			var done bool
			var e error
			if resource == "asset-groups" {
				out, done, e = s.video.Assets.DeleteGroup(c.Request.Context(), a, c.Param("id"), false)
			} else {
				out, done, e = s.video.Assets.DeleteAsset(c.Request.Context(), a, c.Param("id"))
			}
			if e != nil {
				assetConsoleError(c, e)
				return
			}
			if done {
				c.Status(204)
			} else {
				c.JSON(202, out)
			}
		})
	}
	g.GET("/assets", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.ConsoleAssets(c.Request.Context(), a.OrgID, assetConsoleFilter(c))
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/assets", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var in struct {
			GroupID   string `json:"group_id"`
			MediaRef  string `json:"media_ref"`
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
			Name      string `json:"name"`
		}
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		var out videoasset.AssetDTO
		var replay bool
		var e error
		if in.MediaRef != "" && (in.URL != "" || in.MediaType != "") {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		if in.MediaRef != "" {
			out, replay, e = s.video.Assets.CreateAssetFromMedia(c.Request.Context(), a, in.GroupID, in.MediaRef, in.Name)
		} else {
			out, replay, e = s.video.Assets.CreateAsset(c.Request.Context(), a, videoasset.AssetCreate{GroupID: in.GroupID, URL: in.URL, MediaType: in.MediaType, Name: in.Name})
		}
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.Header("Idempotency-Replayed", strconv.FormatBool(replay))
		c.JSON(202, out)
	})
	g.POST("/assets/:id/media-verifications", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		if e := s.video.Assets.VerifyImportedAsset(c.Request.Context(), a.OrgID, c.Param("id")); e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, gin.H{"verified": true})
	})
	g.GET("/assets/:id/preview", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		if e := s.video.Assets.Preview(c.Request.Context(), a.OrgID, c.Param("id"), c.Writer, c.Request); e != nil {
			assetConsoleError(c, e)
		}
	})
	g.GET("/assets/:id/source", s.getArkAssetSource)
	g.POST("/portrait-authorizations", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var in videoasset.PortraitCreate
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		out, _, e := s.video.Assets.CreateConsolePortrait(c.Request.Context(), a, in)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		out.AuthorizationURL = ""
		c.Header("Cache-Control", "private, no-store")
		c.JSON(202, out)
	})
	g.GET("/portrait-authorizations", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		f := assetConsoleFilter(c)
		out, next, e := s.video.Assets.ListPortraits(c.Request.Context(), a.OrgID, f.Limit, f.Cursor)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, gin.H{"data": out, "next_cursor": next})
	})
	g.GET("/portrait-authorizations/:id", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.GetPortraitForCaller(c.Request.Context(), a, c.Param("id"), false)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(200, out)
	})
	g.POST("/portrait-authorizations/:id/link-reads", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		out, e := s.video.Assets.GetPortraitForCaller(c.Request.Context(), a, c.Param("id"), true)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(200, out)
	})
	g.POST("/media-uploads", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var in struct {
			MediaType string `json:"media_type"`
		}
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		out, e := s.video.Assets.BeginUpload(c.Request.Context(), a, in.MediaType)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(201, out)
	})
	g.GET("/media-uploads", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, err := s.video.Assets.ListUploads(c.Request.Context(), a.OrgID, assetConsoleFilter(c))
		if err != nil {
			assetConsoleError(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.GET("/media-uploads/:id", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.GetUpload(c.Request.Context(), a.OrgID, c.Param("id"))
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	media.POST("/media-uploads/:id/complete", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var reader io.Reader = http.MaxBytesReader(c.Writer, c.Request.Body, 200_000_001)
		source := ""
		if strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
			var in struct {
				URL string `json:"url"`
			}
			if videoAdminDecode(c, &in) != nil {
				assetConsoleError(c, videoasset.ErrInvalid)
				return
			}
			source = in.URL
			reader = nil
		}
		out, e := s.video.Assets.CompleteUpload(c.Request.Context(), a, c.Param("id"), reader, source)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/asset-sync-jobs", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, true)
		if !ok {
			return
		}
		var in struct {
			Kind string `json:"kind"`
		}
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		out, e := s.video.Assets.CreateSyncJob(c.Request.Context(), a, in.Kind, "owned")
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(202, out)
	})
	g.GET("/asset-sync-jobs/latest", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		kinds, present := c.GetQueryArray("kind")
		if !present || len(kinds) != 1 {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		out, e := s.video.Assets.LatestSyncJob(c.Request.Context(), a.OrgID, kinds[0])
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		c.JSON(200, gin.H{"job": out})
	})
	g.GET("/asset-sync-jobs/:id", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.GetSyncJob(c.Request.Context(), a.OrgID, c.Param("id"), false)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	g.GET("/asset-operations/:id", func(c *gin.Context) {
		a, ok := s.assetConsoleCaller(c, false)
		if !ok {
			return
		}
		out, e := s.video.Assets.GetOperation(c.Request.Context(), a.OrgID, c.Param("id"))
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	ag := su.Group("/video")
	ag.POST("/asset-discovery-jobs", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			return
		}
		if s.video == nil || s.video.Assets == nil {
			fail(c, 503, "video gateway unavailable")
			return
		}
		var in struct {
			OrgID   uint64 `json:"org_id"`
			Kind    string `json:"kind"`
			ScopeID string `json:"scope_id"`
		}
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		caps := s.video.Assets.ConsoleCapabilities(c.Request.Context(), in.OrgID)
		if !caps.Ready || caps.ScopeID != in.ScopeID {
			assetConsoleError(c, videoasset.ErrUnsupported)
			return
		}
		a := videoasset.CreateContext{CallerKind: "session_user", OrgID: in.OrgID, UserID: actor.ID, RequestID: assetAdminRequestID(c), IdempotencyKey: c.GetHeader("Idempotency-Key"), Superadmin: true}
		out, e := s.video.Assets.CreateSyncJob(c.Request.Context(), a, in.Kind, "discover")
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(202, out)
	})
	ag.GET("/asset-discovery-jobs/:id", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			return
		}
		if s.video == nil || s.video.Assets == nil {
			fail(c, 503, "video asset gateway unavailable")
			return
		}
		org, err := strconv.ParseUint(c.Query("org_id"), 10, 64)
		if err != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		ctx := videoasset.WithCaller(c.Request.Context(), videoasset.CreateContext{OrgID: org, UserID: actor.ID, CallerKind: "session_user", Superadmin: true})
		out, err := s.video.Assets.GetSyncJob(ctx, org, c.Param("id"), true)
		if err != nil {
			assetConsoleError(c, err)
			return
		}
		c.JSON(200, out)
	})
	ag.GET("/asset-discoveries", func(c *gin.Context) {
		if _, ok := s.videoAdminActor(c); !ok {
			return
		}
		if s.video == nil || s.video.Assets == nil {
			fail(c, 503, "video gateway unavailable")
			return
		}
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
		out, e := s.video.Assets.DiscoveryPage(c.Request.Context(), c.Query("scope_id"), limit, c.Query("cursor"))
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
	ag.POST("/asset-discoveries/:id/assign", func(c *gin.Context) {
		actor, ok := s.videoAdminActor(c)
		if !ok {
			return
		}
		if s.video == nil || s.video.Assets == nil {
			fail(c, 503, "video gateway unavailable")
			return
		}
		var in struct {
			OrgID           uint64          `json:"org_id"`
			ExpectedVersion uint64          `json:"expected_version"`
			Evidence        json.RawMessage `json:"evidence"`
		}
		if videoAdminDecode(c, &in) != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		a := videoasset.CreateContext{CallerKind: "session_user", OrgID: in.OrgID, UserID: actor.ID, RequestID: assetAdminRequestID(c), IdempotencyKey: c.GetHeader("Idempotency-Key"), Superadmin: true}
		payload, e := s.prepareArkLicense(in.OrgID, in.Evidence)
		if e != nil {
			assetConsoleError(c, videoasset.ErrInvalid)
			return
		}
		ref := ""
		e = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error { var err error; ref, err = s.video.Evidence.StageTx(tx, payload); return err })
		if e != nil {
			assetConsoleError(c, videoasset.ErrStorage)
			return
		}
		out, e := s.video.Assets.AssignDiscovery(c.Request.Context(), a, c.Param("id"), in.ExpectedVersion, ref)
		if e != nil {
			assetConsoleError(c, e)
			return
		}
		c.JSON(200, out)
	})
}

func assetAdminRequestID(c *gin.Context) string {
	if id := c.GetString("video_request_id"); id != "" {
		return id
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(raw[:])
	c.Set("video_request_id", id)
	c.Header("X-Request-Id", id)
	return id
}
