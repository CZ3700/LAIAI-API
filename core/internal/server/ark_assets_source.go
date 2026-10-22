package server

import "github.com/gin-gonic/gin"

func (s *Server) getArkAssetSource(c *gin.Context) {
	a, ok := s.assetConsoleCaller(c, false)
	if !ok {
		return
	}
	out, err := s.video.Assets.OfficialSource(c.Request.Context(), a.OrgID, c.Param("id"))
	if err != nil {
		assetConsoleError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, out)
}
