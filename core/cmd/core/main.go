// core 控制面守护进程：env-only 配置，缺关键密钥拒绝启动，优雅停机。
// schema 由 cmd/migrate 在容器 entrypoint 先行应用（本进程不做任何 DDL，红线 5）。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // Keep IANA quota timezones available in standalone deployments.

	"github.com/laiai/laiai-next/core/internal/alert"
	"github.com/laiai/laiai-next/core/internal/config"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/leader"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/mailer"
	"github.com/laiai/laiai-next/core/internal/server"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	// root logger 外层包 alert.Handler：命中 critical 的资金事件（漂移/结算失败/
	// 链上已扣但入账失败）自动推飞书告警。所有经此 logger 派生的 main/server/relay
	// 日志全覆盖，零改高危点。未配 LAIAI_ALERT_WEBHOOK 时纯 pass-through。
	log := slog.New(alert.NewHandler(slog.NewJSONHandler(os.Stdout, nil))).With("service", "laiai-core")

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
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns) // 规模化旋钮（env 可调）
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// 读副本（规模化）：把「非金钱、容延迟」的只读（分析/用量历史/排行/公开价目）路由到只读副本，
	// 扩容读吞吐 = 加副本 + 改 LAIAI_DSN_REPLICAS，零代码。多物理副本置于读端 LB/云只读端点之后即一个 DSN。
	// 🚨 金钱判定读（余额/quota/hold 预检）永远走主库 db，绝不经 readDB——复制延迟会致超发（资金红线）。
	readDB := db
	if len(cfg.ReplicaDSNs) > 0 {
		rdb, rerr := gorm.Open(mysql.Open(cfg.ReplicaDSNs[0]), &gorm.Config{
			Logger: gormlogger.Default.LogMode(gormlogger.Warn),
		})
		if rerr != nil {
			log.Error("replica db connect failed, reads fall back to primary", "err", rerr)
		} else {
			if rsql, e := rdb.DB(); e == nil {
				rsql.SetMaxOpenConns(cfg.DBMaxOpenConns)
				rsql.SetMaxIdleConns(cfg.DBMaxIdleConns)
				rsql.SetConnMaxLifetime(5 * time.Minute)
			}
			readDB = rdb
			log.Info("read replica enabled for non-money analytics reads", "replica_dsns", len(cfg.ReplicaDSNs))
		}
	}

	// Invocation Store：异步记录器（保留期清理循环移至 apiSrv 构建后，见下）
	rec := invocation.NewRecorder(db, log)
	retentionDone := make(chan struct{})

	// 跨实例限流 + 缓存失效 + leader 租约共用一个 Redis 客户端。
	// 配了 LAIAI_REDIS_ADDR 才接 Redis；否则单实例：限流退化进程内、租约恒为 leader（都正确）。
	var redisRL *redis.Client
	if cfg.RedisAddr != "" {
		redisRL = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
		log.Info("redis enabled (rate limit + cache invalidation + leader lease)", "addr", cfg.RedisAddr)
	}

	// Leader 租约（WS-3 多实例）：后台 job 只在 leader 实例跑。Redis 未配 → 恒 leader（单实例）。
	// 优化非正确性依赖：fail-open，DB 幂等兜底（见 leader 包注）。token 每实例唯一。
	leaderLease := leader.New(leader.NewRedisStore(redisRL), "laiai:leader:jobs", randToken(),
		30*time.Second, log.With("component", "leader"))
	leaderCtx, leaderCancel := context.WithCancel(context.Background())
	defer leaderCancel()
	go leaderLease.Run(leaderCtx)

	// 异步任务轮询循环（Seedance 类任务的状态同步与结算，设计 §4.4）。仅 leader 实例轮询，
	// 避免 N 副本对同一批在途任务做 N 倍上游 query（终态结算本就 CAS 幂等，多跑只是浪费）。
	var taskSvc *task.Service
	var videoSvc *video.Service
	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	defer pollerCancel()
	if len(cfg.VideoHMACSecret) > 0 {
		videoSvc, err = video.NewService(db, cfg.Keyring, cfg.VideoHMACSecret, log.With("component", "video"))
		if err != nil {
			log.Error("official video initialization failed")
			os.Exit(1)
		}
		blobs, berr := videoartifact.NewFilesystemBlobStore(cfg.VideoArtifactRoot)
		if berr != nil {
			log.Error("private video artifact storage unavailable")
			os.Exit(1)
		}
		defer blobs.Close()
		downloader, derr := videoartifact.NewDownloader(videoartifact.DownloadConfig{AllowedHosts: cfg.VideoArtifactHosts, MaxBytes: 2 << 30, Timeout: 2 * time.Minute, TempDir: cfg.VideoArtifactTemp})
		if derr != nil {
			log.Error("video output download configuration invalid")
			os.Exit(1)
		}
		repo := videoartifact.Repository{DB: db}
		videoSvc.ArtifactWorker = &videoartifact.Worker{Repo: repo, Resolver: videoSvc, Downloader: downloader, Blobs: blobs, LeaseDuration: 4 * time.Minute, TransferTimeout: 3 * time.Minute, MaxAttempts: 3}
		videoSvc.ArtifactService = &videoartifact.Service{Repo: repo, Blobs: blobs}
		if err = videoSvc.ConfigureAssets(video.AssetIntegrationOptions{PlatformCallbackURL: cfg.VideoPortraitCallbackURL}); err != nil {
			log.Error("video asset configuration invalid")
			os.Exit(1)
		}
		jobs := []struct {
			name     string
			interval time.Duration
			run      func(context.Context) error
		}{
			{"task", 2 * time.Second, videoSvc.ProcessOnce},
			{"artifacts", 10 * time.Second, videoSvc.ProcessArtifacts},
			{"assets", 5 * time.Second, videoSvc.Assets.ProcessOnce},
			{"events", 5 * time.Second, videoSvc.ProcessOutbox},
			{"evidence_retention", time.Minute, func(ctx context.Context) error {
				if _, err := videoSvc.Evidence.PurgeExpired(ctx, 100); err != nil {
					return err
				}
				return videoSvc.RecoverRequestLogs(time.Now().UTC())
			}},
		}
		for _, job := range jobs {
			go func() {
				ticker := time.NewTicker(job.interval)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						if leaderLease.IsLeader() {
							if err := job.run(pollerCtx); err != nil && pollerCtx.Err() == nil {
								log.Warn("video background job deferred", "job", job.name)
							}
						}
					case <-pollerCtx.Done():
						return
					}
				}
			}()
		}
	}
	if cfg.Keyring != nil {
		taskSvc = task.NewService(db, cfg.Keyring, log.With("component", "task"))
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if leaderLease.IsLeader() {
						taskSvc.PollOnce(pollerCtx)
					}
				case <-pollerCtx.Done():
					return
				}
			}
		}()
	}

	handler, apiSrv := server.NewWithServer(db, server.Options{
		AuthSecret:   cfg.AuthSecret,
		AdminToken:   cfg.AdminToken,
		Keyring:      cfg.Keyring,
		Recorder:     rec,
		TaskService:  taskSvc,
		VideoService: videoSvc,
		Mailer: mailer.New(mailer.Config{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom,
		}, log.With("component", "mailer")),
		Log:                     log,
		InvocationRetentionDays: cfg.InvocationRetentionDays, // ext_settings.log_retention_days 未配置时的回退默认
		RedisRateLimiter:        redisRL,
		ReadDB:                  readDB,             // 只读副本（仅非金钱分析读用；nil/同主库=全走主）
		TrustedProxies:          cfg.TrustedProxies, // 可信反代（P1-2）：空=私网默认；"none"=不信任代理头
	})

	// Notify outbox 投递循环（§4.9 统一层，leader-only 30s）：扫 pending 到期行发送，指数退避 + DLQ。
	// 独立于每小时维护循环——任务完成回调是产品刚需，需及时投递（首次 emit 后 30s 内送出）。
	// 复用 pollerCtx（与任务轮询同生命周期，shutdown 时一并取消）。
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if leaderLease.IsLeader() {
					if sentN, failedN := apiSrv.ProcessNotifyOutbox(pollerCtx, 200); sentN > 0 || failedN > 0 {
						log.Info("notify outbox processed", "sent", sentN, "failed", failedN)
					}
				}
			case <-pollerCtx.Done():
				return
			}
		}
	}()

	// 后台维护循环（每小时）：旧日志保留期清理（调用日志 + 审计日志，运营可热配，gap P2-C）
	//   + relay 卡死 hold 清扫（审查 H-1）。放在 apiSrv 构建后以复用 RunLogRetention（读 ext_settings）。
	// maintCtx：可取消，shutdown 时 maintCancel 让在途长任务（SyncChannelModels 单趟最坏 ~小时级）能中断。
	maintCtx, maintCancel := context.WithCancel(context.Background())
	defer maintCancel()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !leaderLease.IsLeader() {
					continue // 仅 leader 跑维护/对账（多副本重复执行虽幂等但浪费）
				}
				if inv, aud, err := apiSrv.RunLogRetention(context.Background()); err != nil {
					log.Error("log retention purge failed", "err", err)
				} else if inv > 0 || aud > 0 {
					log.Info("log retention purged", "invocations", inv, "audit_logs", aud)
				}
				// Notify outbox 终态行滚动保留清理（§4.9，防无界膨胀）
				if n, err := apiSrv.PurgeNotifyOutbox(maintCtx); err != nil {
					log.Error("notify outbox purge failed", "err", err)
				} else if n > 0 {
					log.Info("notify outbox purged", "count", n)
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
				// 过期充值单标记（指令 P1-3）：24h 未支付的 pending → expired（运营口径 + 收敛对账扫描集；
				// 非终态——极晚 callback 仍可入金，creditPaidOrder 接受 pending|expired）。
				if n, err := apiSrv.ExpireStaleTopups(context.Background(), 24*time.Hour, 500); err != nil {
					log.Error("topup expire sweep failed", "err", err)
				} else if n > 0 {
					log.Info("topup orders expired", "count", n)
				}
				// 上游模型自动检测（0037 §3.10）：opt-in 渠道 probe 上游 /models，存新模型徽标。
				// 只写元数据（detected_models/last_sync_at），绝不改 models/prices（上货仍手动）。
				if checked, withNew := apiSrv.SyncChannelModels(maintCtx, 200); withNew > 0 {
					log.Info("model sync detected new upstream models", "channels_checked", checked, "channels_with_new", withNew)
				}
				// 余额低预警（0042 批次 3）：已设阈值的 org 可用余额低于阈值 → owner 邮件（24h 防重发）。
				// best-effort 通知，mailer 未配即整段跳过；绝不影响资金语义。
				if checked, sentN := apiSrv.SweepLowBalanceAlerts(maintCtx, 200); sentN > 0 {
					log.Info("low balance alerts sent", "orgs_checked", checked, "sent", sentN)
				}
				// 账户分片摊平（规模化）：把充值默认入的主片余额周期摊到各片，防非主片饿死 → 分片退化为入金单点。
				// 未配置分片（今日全部 org，shard_count=1）时 ListShardedOrgs 为空 → 直接 no-op，零开销零回归。
				runID := "rebal-" + time.Now().UTC().Format("20060102T150405.000")
				if orgsN, moves, err := ledger.RebalanceAllShardedOrgs(db, runID); err != nil {
					log.Error("shard rebalance failed", "err", err)
				} else if moves > 0 {
					log.Info("shard rebalance applied", "orgs", orgsN, "moves", moves)
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
				if !leaderLease.IsLeader() {
					continue // 仅 leader 主动对账，避免 N 副本对同批在途单做 N 倍上游 query
				}
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
	maintCancel() // 中断在途维护长任务（SyncChannelModels probe 循环），再停维护 goroutine
	close(retentionDone)
	rec.Close() // 排空调用记录队列后再关库
	_ = sqlDB.Close()
	log.Info("bye")
}

// randToken：leader 租约的每实例唯一标识（128-bit hex）。rand 失败回退时间戳兜底（极罕见）。
func randToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "inst-" + time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}
