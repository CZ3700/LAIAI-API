package server

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/video"
)

func (s *Server) registerArkAssetProject(c *gin.Context) {
	actor, ok := s.videoAdminActor(c)
	if !ok {
		return
	}
	var input video.AssetProjectRegistration
	if videoAdminDecode(c, &input) != nil {
		fail(c, 422, "invalid asset project registration")
		return
	}
	service := s.video
	if service == nil {
		service = &video.Service{DB: s.db, Now: s.now}
	}
	out, err := service.RegisterAssetProject(c.Request.Context(), actor, input)
	if err != nil {
		switch {
		case errors.Is(err, video.ErrInvalid), errors.Is(err, video.ErrCapability):
			fail(c, 422, "invalid project name or unavailable source scope")
		case errors.Is(err, video.ErrNotFound):
			fail(c, 404, "source provider scope not found")
		case errors.Is(err, video.ErrConflict):
			fail(c, 409, "provider project identity conflict")
		default:
			fail(c, 503, "asset project registration unavailable")
		}
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}
