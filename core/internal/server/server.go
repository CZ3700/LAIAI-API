// Package server：core 控制面 HTTP 层（Gin）。
// 业务逻辑一律在 internal/{identity,ledger,...}，handler 只做参数与编排。
package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	db     *gorm.DB
	secret []byte
	now    func() time.Time // 可注入时钟，测试用
}

func New(db *gorm.DB, authSecret []byte) *gin.Engine {
	s := &Server{db: db, secret: authSecret, now: time.Now}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", s.healthz)

	api := r.Group("/api")
	{
		api.POST("/auth/register", s.register)
		api.POST("/auth/login", s.login)

		authed := api.Group("", s.authRequired())
		{
			authed.GET("/me", s.me)
			authed.POST("/keys", s.createKey)
			authed.GET("/keys", s.listKeys)
			authed.DELETE("/keys/:id", s.revokeKey)
		}
	}
	return r
}

func (s *Server) healthz(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err == nil {
		err = sqlDB.Ping()
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "db": "down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func fail(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}
