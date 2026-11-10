package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/video"
)

// The root router mounts this under the named-superadmin group, alongside
// registerVideoUserLimits. These routes never return management credentials.
func (s *Server) registerVideoProviderQuotas(su *gin.RouterGroup) {
	su.GET("/video/provider-accounts/:account_ref/model-rate-limits/:foundation_model", s.getVideoProviderModelQuota)
	su.PUT("/video/provider-accounts/:account_ref/model-rate-limits/:foundation_model/manual", s.putVideoProviderModelManualQuota)
	su.POST("/video/provider-scopes/:scope_id/model-rate-limits/sync", s.syncVideoProviderModelQuota)
}

func (s *Server) getVideoProviderModelQuota(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil {
		fail(c, http.StatusServiceUnavailable, "video gateway unavailable")
		return
	}
	row, err := s.video.ReadProviderModelQuota(c.Request.Context(), actor, c.Param("account_ref"), c.Param("foundation_model"))
	if errors.Is(err, video.ErrInvalid) {
		fail(c, http.StatusBadRequest, "invalid account or foundation model")
		return
	}
	if errors.Is(err, video.ErrNotFound) {
		fail(c, http.StatusNotFound, "model quota not initialized")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "model quota unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"quota": row, "effective": row.Effective(), "official_stale": row.OfficialStale(s.now().UTC())})
}

func (s *Server) putVideoProviderModelManualQuota(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil {
		fail(c, http.StatusServiceUnavailable, "video gateway unavailable")
		return
	}
	var input struct {
		ExpectedVersion      *uint64 `json:"expected_version"`
		CreateTaskRPM        *int    `json:"create_task_rpm"`
		ConcurrentRequests   *int    `json:"concurrent_requests"`
		CreateTaskRPM4K      *int    `json:"create_task_rpm_4k"`
		ConcurrentRequests4K *int    `json:"concurrent_requests_4k"`
	}
	if videoAdminDecode(c, &input) != nil || input.ExpectedVersion == nil {
		fail(c, http.StatusBadRequest, "invalid manual quota")
		return
	}
	row, err := s.video.SetProviderModelManualQuota(c.Request.Context(), actor, video.ProviderModelManualQuota{
		AccountRef: c.Param("account_ref"), FoundationModel: c.Param("foundation_model"), ExpectedVersion: *input.ExpectedVersion,
		CreateTaskRPM: input.CreateTaskRPM, ConcurrentRequests: input.ConcurrentRequests,
		CreateTaskRPM4K: input.CreateTaskRPM4K, ConcurrentRequests4K: input.ConcurrentRequests4K,
	})
	if errors.Is(err, video.ErrInvalid) {
		fail(c, http.StatusBadRequest, "invalid manual quota")
		return
	}
	if errors.Is(err, video.ErrConflict) {
		fail(c, http.StatusConflict, "manual quota version changed")
		return
	}
	if errors.Is(err, video.ErrCapability) {
		fail(c, http.StatusConflict, "provider account is not configured")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "manual quota unavailable")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"quota": row, "effective": row.Effective(), "official_stale": row.OfficialStale(s.now().UTC())})
}

func (s *Server) syncVideoProviderModelQuota(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	if s.video == nil {
		fail(c, http.StatusServiceUnavailable, "video gateway unavailable")
		return
	}
	scopeID, err := strconv.ParseUint(c.Param("scope_id"), 10, 64)
	var input struct {
		FoundationModelNames []string `json:"foundation_model_names"`
	}
	if err != nil || scopeID == 0 || videoAdminDecode(c, &input) != nil {
		fail(c, http.StatusBadRequest, "invalid model quota sync request")
		return
	}
	rows, err := s.video.SyncProviderModelRateLimits(c.Request.Context(), actor, scopeID, input.FoundationModelNames)
	if errors.Is(err, video.ErrInvalid) {
		fail(c, http.StatusBadRequest, "invalid foundation model names")
		return
	}
	if errors.Is(err, video.ErrCapability) {
		fail(c, http.StatusConflict, "management scope is not configured")
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "official model quota sync failed; prior snapshot retained")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"quotas": rows})
}
