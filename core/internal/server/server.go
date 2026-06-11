// Package server：core 控制面 HTTP 层（Gin）。
// 业务逻辑一律在 internal/{identity,ledger,relay,...}，handler 只做参数与编排。
package server

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

type Options struct {
	AuthSecret []byte               // session token HMAC（必填）
	AdminToken []byte               // X-Admin-Token；空 = admin API 整组禁用
	Keyring    *crypto.Keyring      // 渠道凭证信封加密；nil = 渠道管理禁用
	Recorder   *invocation.Recorder // 调用记录；nil = relay 不记录
	Log        *slog.Logger
}

type Server struct {
	db         *gorm.DB
	secret     []byte
	adminToken []byte
	keyring    *crypto.Keyring
	now        func() time.Time // 可注入时钟，测试用
}

func New(db *gorm.DB, opts Options) *gin.Engine {
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	s := &Server{
		db: db, secret: opts.AuthSecret,
		adminToken: opts.AdminToken, keyring: opts.Keyring,
		now: time.Now,
	}
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
			authed.GET("/usage", s.getUsage)
		}

		admin := api.Group("/admin", s.adminRequired())
		{
			admin.POST("/credits", s.adminGrantCredit)
			admin.POST("/channels", s.adminCreateChannel)
			admin.GET("/channels", s.adminListChannels)
			admin.DELETE("/channels/:id", s.adminDisableChannel)
			admin.POST("/model-prices", s.adminUpsertPrice)
			admin.GET("/model-prices", s.adminListPrices)
		}
	}

	// Relay 数据面（M1 暂驻 core 进程，M2/M3 拆独立二进制）
	if opts.Keyring != nil {
		rl := relay.New(db, opts.Keyring, opts.Recorder, log.With("component", "relay"))
		r.POST("/v1/chat/completions", rl.HandleChatCompletions)
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
