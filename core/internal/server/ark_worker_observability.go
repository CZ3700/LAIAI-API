package server

import "github.com/gin-gonic/gin"

// Operational aggregates are global. Require a current named superadmin even
// when the outer admin group accepted a shared emergency token.
func (s *Server) getArkWorkerObservability(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	// There is no caller-controlled scope, time window, SQL or label set.
	if c.Request.URL.RawQuery != "" {
		fail(c, 422, "worker observability does not accept query parameters")
		return
	}
	if s.video == nil {
		fail(c, 503, "video runtime unavailable")
		return
	}
	c.JSON(200, s.video.WorkerObservability(c.Request.Context()))
}
