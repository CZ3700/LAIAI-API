package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/video"
)

func arkGroupAssetProjectID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("group_id"), 10, 64)
	if err != nil || id == 0 {
		fail(c, http.StatusBadRequest, "invalid routing group id")
		return 0, false
	}
	return id, true
}

func arkGroupAssetProjectError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, video.ErrInvalid):
		fail(c, http.StatusBadRequest, "invalid group asset project")
	case errors.Is(err, video.ErrNotFound):
		fail(c, http.StatusNotFound, "group asset project not found")
	case errors.Is(err, video.ErrForbidden):
		fail(c, http.StatusForbidden, "routing group cannot use this project")
	case errors.Is(err, video.ErrConflict):
		fail(c, http.StatusConflict, "group asset project revision conflict")
	case errors.Is(err, video.ErrCapability):
		fail(c, http.StatusUnprocessableEntity, "group channels and asset project are not ready or do not match")
	default:
		fail(c, http.StatusServiceUnavailable, "group asset project unavailable")
	}
}

func (s *Server) getArkGroupAssetProject(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	groupID, ok := arkGroupAssetProjectID(c)
	if !ok {
		return
	}
	out, err := s.video.ReadGroupAssetProject(c.Request.Context(), actor, groupID)
	if err != nil {
		arkGroupAssetProjectError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"project": out})
}

func (s *Server) putArkGroupAssetProject(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	groupID, ok := arkGroupAssetProjectID(c)
	if !ok {
		return
	}
	var in struct {
		ProviderScopeID  uint64 `json:"provider_scope_id"`
		ScopeVersion     uint64 `json:"scope_version"`
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if err := videoAdminDecode(c, &in); err != nil {
		fail(c, http.StatusBadRequest, "invalid group asset project")
		return
	}
	out, err := s.video.SetGroupAssetProject(c.Request.Context(), actor, video.GroupAssetProjectInput{GroupID: groupID, ProviderScopeID: in.ProviderScopeID, ScopeVersion: in.ScopeVersion, ExpectedRevision: in.ExpectedRevision})
	if err != nil {
		arkGroupAssetProjectError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, gin.H{"project": out})
}
