package server

import (
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
)

func (s *Server) registerArkOfficialPriceCatalog(su *gin.RouterGroup) {
	su.GET("/video/official-price-catalog", s.getArkOfficialPriceCatalog)
}

func (s *Server) getArkOfficialPriceCatalog(c *gin.Context) {
	if _, ok := s.videoAdminActor(c); !ok {
		return
	}
	catalog, err := arkpricecatalog.Load()
	if err != nil {
		fail(c, 503, "official reference catalog integrity check failed")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, catalog)
}
