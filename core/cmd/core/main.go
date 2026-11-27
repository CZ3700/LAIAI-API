// core control-plane daemon: env-only configuration, fail-fast on missing secrets, 4-stage drain.
// Schema migrations are applied ahead of startup by cmd/migrate (no DDL at runtime).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/laiai/laiai-next/core/internal/alert"
	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/dbopen"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/leader"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/mailer"
	"github.com/laiai/laiai-next/core/internal/server"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const leaderAdvisoryLockID int64 = 7829140002

func main() {
	log := slog.New(alert.NewHandler(slog.NewJSONHandler(os.Stdout, nil))).With("service", "laiai-core")
	os.Exit(runCore(log))
}

func runCore(log *slog.Logger) (code int) {
	cfg, err := config.Load()
	if err != nil {
		log.Error("config load failed", "err", err)
		return 1
	}

	gormCfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Warn)}
	db, err := dbopen.Open(cfg.DSN, gormCfg)
	if err != nil {
		log.Error("db connect failed", "err", err)
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Error("db handle failed", "err", err)
		return 1
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	readDB := db
	if len(cfg.ReplicaDSNs) > 0 {
		rdb, rerr := dbopen.Open(cfg.ReplicaDSNs[0], gormCfg)
		if rerr != nil {
			log.Error("replica db connect failed, reads fall back to primary", "err", rerr)
		} else {
			if rsql, e := rdb.DB(); e == nil {
				rsql.SetMaxOpenConns(cfg.DBMaxOpenConns)
				rsql.SetMaxIdleConns(cfg.DBMaxIdleConns)
				rsql.SetConnMaxLifetime(5 * time.Minute)
			}
			readDB = rdb
			log.Info("read replica enabled for analytics reads", "replica_dsns", len(cfg.ReplicaDSNs))
		}
	}

	rec := invocation.NewRecorder(db, log)
	jobs := newMainJobs()
	requests := newDrainTracker()
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var srv *http.Server
	defer func() {
		log.Info("shutting down")
		if err := shutdownCore(srv, jobs, requests, cancelRequests, rec.Close, productionShutdownBudgets); err != nil {
			log.Error("shutdown incomplete", "err", err)
			code = shutdownExitCode(nil, err)
		}
		if code == 0 {
			log.Info("bye")
		}
	}()

	var redisRL *redis.Client
	if cfg.RedisAddr != "" {
		redisRL = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
		log.Info("redis enabled (rate limit + cache invalidation + leader lease)", "addr", cfg.RedisAddr)
	}

	var leaderStore leader.Store
	if redisRL != nil {
		leaderStore = leader.NewRedisStore(redisRL)
	} else if db.Dialector.Name() == "postgres" {
		pgLeader := leader.NewPGStore(sqlDB, leaderAdvisoryLockID)
		defer func() { _ = pgLeader.Close() }()
		leaderStore = pgLeader
		log.Info("postgres advisory lock enabled for singleton workers")
	}
	leaderLease := leader.New(leaderStore, "laiai:leader:jobs", randToken(), 30*time.Second, log.With("component", "leader"))
	jobs.goRun(leaderLease.Run)

	var taskSvc *task.Service
	if cfg.Keyring != nil {
		taskSvc = task.NewService(db, cfg.Keyring, log.With("component", "task"))
		jobs.goRun(func(ctx context.Context) {
			runWakeLoop(ctx, taskSvc.WakeChan(), 20*time.Second, 2*time.Second, func(ctx context.Context) bool {
				if !leaderLease.IsLeader() {
					return false
				}
				return taskSvc.PollOnce(ctx) > 0
			})
		})
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
		InvocationRetentionDays: cfg.InvocationRetentionDays,
		RedisRateLimiter:        redisRL,
		ReadDB:                  readDB,
		TrustedProxies:          cfg.TrustedProxies,
	})

	if err := apiSrv.WarmCaches(stopCtx); err != nil {
		log.Warn("settings cache warmup failed", "err", err)
	}

	jobs.goRun(func(ctx context.Context) {
		runWakeLoop(ctx, apiSrv.NotifyWakeChan(), 60*time.Second, 5*time.Second, func(ctx context.Context) bool {
			if !leaderLease.IsLeader() {
				return false
			}
			sentN, failedN := apiSrv.ProcessNotifyOutbox(ctx, 200)
			if sentN > 0 || failedN > 0 {
				log.Info("notify outbox processed", "sent", sentN, "failed", failedN)
			}
			return sentN >= 200
		})
	})

	jobs.goRun(func(ctx context.Context) {
		runStartupLeaderInit(ctx, leaderLease, log, func(ctx context.Context) {
			stuckCutoff := time.Now().UTC().Add(-15 * time.Minute)
			if n, err := ledger.SweepStuckHolds(db.WithContext(ctx), "usage", stuckCutoff, 500); err != nil {
				log.Error("startup stuck hold sweep failed", "err", err)
			} else if n > 0 {
				log.Warn("startup released stuck relay holds", "count", n)
			}
			if n, err := apiSrv.BackfillDailyUsageStats(ctx); err != nil {
				log.Warn("startup daily stats backfill failed", "err", err)
			} else if n > 0 {
				log.Info("startup daily stats backfilled", "rows", n)
			}
			if arkDel, syncDel, obDel, err := apiSrv.CleanupStorageAndPluginLogs(ctx); err != nil {
				log.Warn("startup storage cleanup failed", "err", err)
			} else if arkDel > 0 || syncDel > 0 || obDel > 0 {
				log.Info("startup storage cleanup done", "ark_assets", arkDel, "sync_logs", syncDel, "outbox", obDel)
			}
		})
	})

	jobs.goRun(func(ctx context.Context) {
		periodic(ctx, time.Minute, func(ctx context.Context) {
			if !leaderLease.IsLeader() {
				return
			}
			if synced := apiSrv.SyncUpstreamChannelRates(ctx); synced > 0 {
				log.Info("upstream channel rates synced", "count", synced)
			}
			if sum, err := apiSrv.SyncArkMonitor(ctx); err != nil {
				log.Warn("ark monitor sync failed", "err", err)
			} else if sum != nil && (sum.TasksUpserted > 0 || sum.BillsReconciled > 0 || sum.EndpointsFused > 0 || sum.EndpointsRecovered > 0) {
				log.Info("ark monitor synced", "accounts", sum.AccountsProcessed, "tasks", sum.TasksUpserted, "reconciled", sum.BillsReconciled, "fused", sum.EndpointsFused, "recovered", sum.EndpointsRecovered)
			}
		})
	})

	jobs.goRun(func(ctx context.Context) {
		periodic(ctx, 5*time.Minute, func(ctx context.Context) {
			if !leaderLease.IsLeader() {
				return
			}
			if checked, credited, err := apiSrv.ReconcileTopups(ctx, 2*time.Minute, 200); err != nil {
				log.Error("topup reconcile failed", "err", err)
			} else if credited > 0 {
				log.Warn("topup reconcile credited orphans", "checked", checked, "credited", credited)
			}
			if n, err := apiSrv.ExpireStaleTopups(ctx, 24*time.Hour, 500); err != nil {
				log.Error("topup expire sweep failed", "err", err)
			} else if n > 0 {
				log.Info("topup orders expired", "count", n)
			}
		})
	})

	jobs.goRun(func(ctx context.Context) {
		periodic(ctx, 10*time.Minute, func(ctx context.Context) {
			if !leaderLease.IsLeader() {
				return
			}
			stuckCutoff := time.Now().UTC().Add(-15 * time.Minute)
			if n, err := ledger.SweepStuckHolds(db.WithContext(ctx), "usage", stuckCutoff, 500); err != nil {
				log.Error("stuck hold sweep failed", "err", err)
			} else if n > 0 {
				log.Warn("released stuck relay holds", "count", n)
			}
			if drifts, err := ledger.CheckDrift(db.WithContext(ctx)); err != nil {
				log.Error("ledger drift check failed", "err", err)
			} else {
				for _, d := range drifts {
					log.Error("LEDGER DRIFT DETECTED — manual reconcile needed",
						"account", d.AccountID, "org", d.OrgID, "type", d.Type,
						"posted_stored", d.PostedStored.String(), "posted_derived", d.PostedDerived.String(), "posted_drift", d.PostedDrift().String(),
						"held_stored", d.HeldStored.String(), "held_derived", d.HeldDerived.String(), "held_drift", d.HeldDrift().String())
				}
			}
		})
	})

	jobs.goRun(func(ctx context.Context) {
		periodic(ctx, time.Hour, func(ctx context.Context) {
			if !leaderLease.IsLeader() {
				return
			}
			runHourlyMaintenance(ctx, db, apiSrv, log)
		})
	})

	var dailyScheduleMu sync.Mutex
	jobs.goRun(func(ctx context.Context) {
		runDailyScheduledTask(ctx, apiSrv, leaderLease, &dailyScheduleMu, log, "DailyStatsSync", server.LogCleanupSettings.SafeDailyStatsHM, func(ctx context.Context) {
			if n, err := apiSrv.SyncDailyUsageStats(ctx, false); err != nil {
				log.Warn("daily stats sync failed", "err", err)
			} else if n > 0 {
				log.Info("daily stats synced", "rows", n)
			}
		})
	})
	jobs.goRun(func(ctx context.Context) {
		runDailyScheduledTask(ctx, apiSrv, leaderLease, &dailyScheduleMu, log, "LogContentCleanup", server.LogCleanupSettings.SafeCleanHM, func(ctx context.Context) {
			cfg := apiSrv.LoadLogCleanupSettings()
			if n, err := apiSrv.CleanupLogContent(ctx, cfg.LogRetentionDays); err != nil {
				log.Warn("log content cleanup failed", "err", err)
			} else if n > 0 {
				log.Info("log content cleaned", "rows", n, "retention_days", cfg.LogRetentionDays)
			}
		})
	})
	jobs.goRun(func(ctx context.Context) {
		runDailyScheduledTask(ctx, apiSrv, leaderLease, &dailyScheduleMu, log, "LogRowArchive", server.LogCleanupSettings.SafeArchiveHM, func(ctx context.Context) {
			cfg := apiSrv.LoadLogCleanupSettings()
			if n, err := apiSrv.ArchiveOldLogs(ctx, cfg.LogRowRetentionDays); err != nil {
				log.Warn("log row archive failed", "err", err)
			} else if n > 0 {
				log.Info("log rows archived", "rows", n, "row_retention_days", cfg.LogRowRetentionDays)
			}
		})
	})
	jobs.goRun(func(ctx context.Context) {
		runDailyScheduledTask(ctx, apiSrv, leaderLease, &dailyScheduleMu, log, "ErrorLogsCleanup", server.LogCleanupSettings.SafeErrorCleanHM, func(ctx context.Context) {
			cfg := apiSrv.LoadLogCleanupSettings()
			if h, a, tk, err := apiSrv.CleanupErrorLogs(ctx, cfg.ErrorLogRetentionDays); err != nil {
				log.Warn("error logs cleanup failed", "err", err)
			} else if h > 0 || a > 0 || tk > 0 {
				log.Info("error logs cleaned", "invocations", h, "archive", a, "tasks", tk, "retention_days", cfg.ErrorLogRetentionDays)
			}
		})
	})
	jobs.goRun(func(ctx context.Context) {
		runDailyScheduledTask(ctx, apiSrv, leaderLease, &dailyScheduleMu, log, "StorageAndPluginCleanup", server.LogCleanupSettings.SafeStorageCleanHM, func(ctx context.Context) {
			if arkDel, syncDel, obDel, err := apiSrv.CleanupStorageAndPluginLogs(ctx); err != nil {
				log.Warn("storage cleanup failed", "err", err)
			} else if arkDel > 0 || syncDel > 0 || obDel > 0 {
				log.Info("storage and plugin cleanup done", "ark_assets", arkDel, "sync_logs", syncDel, "outbox", obDel)
			}
		})
	})

	srv = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           requests.wrap(handler),
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("listening", "addr", cfg.ListenAddr, "dialect", db.Dialector.Name())
	err = awaitServerStop(stopCtx, jobs, srv.ListenAndServe)
	if err != nil {
		log.Error("server failed", "err", err)
	}
	return shutdownExitCode(err, nil)
}

func runHourlyMaintenance(ctx context.Context, db *gorm.DB, apiSrv *server.Server, log *slog.Logger) {
	if inv, aud, err := apiSrv.RunLogRetention(ctx); err != nil {
		log.Error("log retention purge failed", "err", err)
	} else if inv > 0 || aud > 0 {
		log.Info("log retention purged", "invocations", inv, "audit_logs", aud)
	}
	if ctx.Err() != nil {
		return
	}
	if n, err := apiSrv.PurgeNotifyOutbox(ctx); err != nil {
		log.Error("notify outbox purge failed", "err", err)
	} else if n > 0 {
		log.Info("notify outbox purged", "count", n)
	}
	if ctx.Err() != nil {
		return
	}
	stuckCutoff := time.Now().UTC().Add(-15 * time.Minute)
	if n, err := ledger.SweepStuckHolds(db.WithContext(ctx), "usage", stuckCutoff, 500); err != nil {
		log.Error("stuck hold sweep failed", "err", err)
	} else if n > 0 {
		log.Warn("released stuck relay holds", "count", n)
	}
	if ctx.Err() != nil {
		return
	}
	if drifts, err := ledger.CheckDrift(db.WithContext(ctx)); err != nil {
		log.Error("ledger drift check failed", "err", err)
	} else {
		for _, d := range drifts {
			log.Error("LEDGER DRIFT DETECTED — manual reconcile needed",
				"account", d.AccountID, "org", d.OrgID, "type", d.Type,
				"posted_stored", d.PostedStored.String(), "posted_derived", d.PostedDerived.String(), "posted_drift", d.PostedDrift().String(),
				"held_stored", d.HeldStored.String(), "held_derived", d.HeldDerived.String(), "held_drift", d.HeldDrift().String())
		}
	}
	if ctx.Err() != nil {
		return
	}
	if n, err := apiSrv.ExpireStaleTopups(ctx, 24*time.Hour, 500); err != nil {
		log.Error("topup expire sweep failed", "err", err)
	} else if n > 0 {
		log.Info("topup orders expired", "count", n)
	}
	if ctx.Err() != nil {
		return
	}
	if checked, withNew := apiSrv.SyncChannelModels(ctx, 200); withNew > 0 {
		log.Info("model sync detected new upstream models", "channels_checked", checked, "channels_with_new", withNew)
	}
	if ctx.Err() != nil {
		return
	}
	if checked, sentN := apiSrv.SweepLowBalanceAlerts(ctx, 200); sentN > 0 {
		log.Info("low balance alerts sent", "orgs_checked", checked, "sent", sentN)
	}
	if ctx.Err() != nil {
		return
	}
	runID := "rebal-" + time.Now().UTC().Format("20060102T150405.000")
	if orgsN, moves, err := ledger.RebalanceAllShardedOrgs(db.WithContext(ctx), runID); err != nil {
		log.Error("shard rebalance failed", "err", err)
	} else if moves > 0 {
		log.Info("shard rebalance applied", "orgs", orgsN, "moves", moves)
	}
	if ctx.Err() != nil {
		return
	}
	if deleted, err := apiSrv.CleanupExpiredArkAssets(ctx); err != nil {
		log.Warn("ark asset retention cleanup failed", "err", err)
	} else if deleted > 0 {
		log.Info("ark expired assets cleaned", "deleted", deleted)
	}
}

func runWakeLoop(ctx context.Context, wake <-chan struct{}, baseInterval, jitter time.Duration, step func(context.Context) bool) {
	nextWait := func(busy bool) time.Duration {
		if busy {
			return 2 * time.Second
		}
		d := baseInterval
		if jitter > 0 {
			d += time.Duration(mathrand.Int64N(int64(jitter)*2+1)) - jitter
		}
		return max(time.Second, d)
	}
	timer := time.NewTimer(nextWait(false))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		busy := step(ctx)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(nextWait(busy))
	}
}

func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "inst-" + time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}
