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
	"github.com/laiai/laiai-next/core/internal/server"
	"github.com/laiai/laiai-next/core/internal/task"
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

	// Invocation Store：异步记录器 + 保留期清理循环（设计 §6.1）
	rec := invocation.NewRecorder(db, log)
	retentionDone := make(chan struct{})
	// 后台维护循环：调用记录保留期清理 + relay 卡死 hold 清扫（审查 H-1）
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if cfg.InvocationRetentionDays > 0 {
					cutoff := time.Now().UTC().AddDate(0, 0, -cfg.InvocationRetentionDays)
					if n, err := invocation.PurgeOlderThan(db, cutoff, 1000); err != nil {
						log.Error("invocation retention purge failed", "err", err)
					} else if n > 0 {
						log.Info("invocation retention purged", "deleted", n, "cutoff", cutoff)
					}
				}
				// 兜底同步结算失败留下的孤儿 usage hold（远早于任何正常请求时长）
				stuckCutoff := time.Now().UTC().Add(-15 * time.Minute)
				if n, err := ledger.SweepStuckHolds(db, "usage", stuckCutoff, 500); err != nil {
					log.Error("stuck hold sweep failed", "err", err)
				} else if n > 0 {
					log.Warn("released stuck relay holds", "count", n)
				}
			case <-retentionDone:
				return
			}
		}
	}()

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

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: server.New(db, server.Options{
			AuthSecret:  cfg.AuthSecret,
			AdminToken:  cfg.AdminToken,
			Keyring:     cfg.Keyring,
			Recorder:    rec,
			TaskService: taskSvc,
			Log:         log,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

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
