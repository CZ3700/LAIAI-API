// core 控制面守护进程：env-only 配置，缺关键密钥拒绝启动，优雅停机。
// schema 由 cmd/migrate 在容器 entrypoint 先行应用（本进程不做任何 DDL，红线 5）。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/mailer"
	"github.com/laiai/laiai-next/core/internal/server"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "laiai-core")

	cfg, err := config.Load()
	if err != nil {
		log.Error("config load failed", "err", err)
		os.Exit(1)
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		log.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Error("db handle failed", "err", err)
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// Invocation Store：异步记录器（保留期清理循环移至 apiSrv 构建后，见下）
	rec := invocation.NewRecorder(db, log)
	retentionDone := make(chan struct{})

	// 异步任务轮询循环（Seedance 类任务的状态同步与结算，设计 §4.4）
	var taskSvc *task.Service
	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	defer pollerCancel()
	if cfg.Keyring != nil {
		taskSvc = task.NewService(db, cfg.Keyring, log.With("component", "task"))
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					taskSvc.PollOnce(pollerCtx)
				case <-pollerCtx.Done():
					return
				}
			}
		}()
	}

	// 跨实例限流：配了 LAIAI_REDIS_ADDR 才接 Redis；否则 relay 退化进程内限流（单实例正确）。
	var redisRL *redis.Client
	if cfg.RedisAddr != "" {
		redisRL = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
		log.Info("redis rate limiter enabled", "addr", cfg.RedisAddr)
	}

	handler, apiSrv := server.NewWithServer(db, server.Options{
		AuthSecret:  cfg.AuthSecret,
		AdminToken:  cfg.AdminToken,
		Keyring:     cfg.Keyring,
		Recorder:    rec,
		TaskService: taskSvc,
		Mailer: mailer.New(mailer.Config{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom,
		}, log.With("component", "mailer")),
		Log:                     log,
		InvocationRetentionDays: cfg.InvocationRetentionDays, // ext_settings.log_retention_days 未配置时的回退默认
		RedisRateLimiter:        redisRL,
	})

	// 后台维护循环（每小时）：旧日志保留期清理（调用日志 + 审计日志，运营可热配，gap P2-C）
	//   + relay 卡死 hold 清扫（审查 H-1）。放在 apiSrv 构建后以复用 RunLogRetention（读 ext_settings）。
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if inv, aud, err := apiSrv.RunLogRetention(context.Background()); err != nil {
					log.Error("log retention purge failed", "err", err)
				} else if inv > 0 || aud > 0 {
					log.Info("log retention purged", "invocations", inv, "audit_logs", aud)
				}
				// 兜底同步结算失败留下的孤儿 usage hold（远早于任何正常请求时长）
				stuckCutoff := time.Now().UTC().Add(-15 * time.Minute)
				if n, err := ledger.SweepStuckHolds(db, "usage", stuckCutoff, 500); err != nil {
					log.Error("stuck hold sweep failed", "err", err)
				} else if n > 0 {
					log.Warn("released stuck relay holds", "count", n)
				}
				// 账本漂移自检（P1-1）：重算两条对账恒等式，任一账户不一致即告警人工裁定。
				// 只读不修复；正常应恒为 0，一旦非零说明有绕开 ledger 包写余额 / 局部写损坏。
				if drifts, err := ledger.CheckDrift(db); err != nil {
					log.Error("ledger drift check failed", "err", err)
				} else if len(drifts) > 0 {
					for _, d := range drifts {
						log.Error("LEDGER DRIFT DETECTED — manual reconcile needed",
							"account", d.AccountID, "org", d.OrgID, "type", d.Type,
							"posted_stored", d.PostedStored.String(), "posted_derived", d.PostedDerived.String(), "posted_drift", d.PostedDrift().String(),
							"held_stored", d.HeldStored.String(), "held_derived", d.HeldDerived.String(), "held_drift", d.HeldDrift().String())
					}
				}
			case <-retentionDone:
				return
			}
		}
	}()
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 充值对账 job（M4，金融级双保险）：每 5 分钟主动 Query 兜底 callback/webhook 漏发。
	// 只对账建单超过 2 分钟的 pending 单（callback 通常秒级到，给足正常支付窗口）。
	reconcileCtx, reconcileCancel := context.WithCancel(context.Background())
	defer reconcileCancel()
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if checked, credited, err := apiSrv.ReconcileTopups(reconcileCtx, 2*time.Minute, 200); err != nil {
					log.Error("topup reconcile failed", "err", err)
				} else if credited > 0 {
					log.Warn("topup reconcile credited orphans", "checked", checked, "credited", credited)
				}
			case <-reconcileCtx.Done():
				return
			}
		}
	}()

	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown error", "err", err)
	}
	close(retentionDone)
	rec.Close() // 排空调用记录队列后再关库
	_ = sqlDB.Close()
	log.Info("bye")
}
