package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

// adminLogStats：GET /api/admin/log-stats —— 日志量与保留期一览（gap P2-C，对标 NewAPI 日志统计）。
// 运营据此判断清理影响：各表总行数 + 最旧记录时间 + 当前生效保留期。空表 oldest 为 null。
func (s *Server) adminLogStats(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var invTotal, auditTotal int64
	rdb.Model(&invocation.Invocation{}).Count(&invTotal)
	rdb.Model(&AuditLog{}).Count(&auditTotal)
	var reqTotal, ctxTotal, evtTotal, attTotal, obxTotal, aseTotal int64
	rdb.Table("api_request_logs").Count(&reqTotal)
	rdb.Table("video_request_contexts").Count(&ctxTotal)
	rdb.Table("video_events").Count(&evtTotal)
	rdb.Table("video_attempts").Count(&attTotal)
	rdb.Table("video_outbox").Count(&obxTotal)
	rdb.Table("video_asset_events").Count(&aseTotal)

	var invOldest *string // invocations.created_at 是 DATETIME（字符串口径）
	rdb.Model(&invocation.Invocation{}).Select("MIN(created_at)").Scan(&invOldest)
	var auditOldestMs *int64 // audit_logs.created_at 是 unix 毫秒
	rdb.Model(&AuditLog{}).Select("MIN(created_at)").Scan(&auditOldestMs)

	c.JSON(http.StatusOK, gin.H{
		"invocations":    gin.H{"total": invTotal, "oldest_at": invOldest},
		"audit_logs":     gin.H{"total": auditTotal, "oldest_at_ms": auditOldestMs},
		"request_logs":   gin.H{"total": reqTotal, "contexts": ctxTotal},
		"task_lifecycle": gin.H{"video_events": evtTotal, "video_attempts": attTotal, "video_outbox": obxTotal, "video_asset_events": aseTotal},
		"retention": gin.H{ // 当前生效保留期（已并入 env 回退），前端展示「清理将影响…」
			"log_retention_days":         s.retentionDaysSetting("log_retention_days", s.invRetentionDefault),
			"audit_log_retention_days":   s.retentionDaysSetting("audit_log_retention_days", 0),
			"request_log_retention_days": s.retentionDaysSetting("request_log_retention_days", 14),
			"task_log_retention_days":    s.retentionDaysSetting("task_log_retention_days", 30),
		},
	})
}

// 旧日志清理（gap P2-C，运营可热配）。两类日志各自独立保留期：
//   - log_retention_days       → invocations（调用日志，created_at 是 DATETIME）
//   - audit_log_retention_days → audit_logs（审计日志，created_at 是 unix 毫秒 BIGINT）
// ext_settings 优先（admin 可不重启热改）；invocations 未配置时回退到启动期 env 默认
// （向后兼容旧的 INVOCATION_RETENTION_DAYS）。各自 <=0 表示「永久保留，不清理」。

// retentionDaysSetting：读某保留期设置，非法/空 → 回退 fallback。
func (s *Server) retentionDaysSetting(key string, fallback int) int {
	if v := s.getSetting(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// RunLogRetention：按运营可配的保留期清理旧日志。后台维护循环每 tick 调一次（事务外，无死锁风险）。
// 批量删除避免长事务锁表。返回各自删除行数；任一类出错即返回（另一类已删的计数保留）。
func (s *Server) RunLogRetention(ctx context.Context) (invDeleted, auditDeleted int64, err error) {
	if d := s.retentionDaysSetting("log_retention_days", s.invRetentionDefault); d > 0 {
		cutoff := s.now().UTC().AddDate(0, 0, -d)
		invDeleted, err = invocation.PurgeOlderThan(s.db.WithContext(ctx), cutoff, 1000)
		if err != nil {
			return invDeleted, auditDeleted, err
		}
	}
	if d := s.retentionDaysSetting("audit_log_retention_days", 0); d > 0 {
		cutoffMs := s.now().UTC().AddDate(0, 0, -d).UnixMilli()
		auditDeleted, err = purgeAuditLogsOlderThan(s.db.WithContext(ctx), cutoffMs, 1000)
		if err != nil {
			return invDeleted, auditDeleted, err
		}
	}
	// —— 控制台访问日志（0138/0139：素材 GET 2xx 已不落库，存量按期清理）——
	if d := s.retentionDaysSetting("request_log_retention_days", 14); d > 0 {
		cutoff := s.now().UTC().AddDate(0, 0, -d)
		for _, spec := range [][4]string{
			{"api_request_logs", "request_id", "started_at", ""},
			{"video_request_contexts", "request_id", "created_at", ""},
		} {
			n, err := purgeRowsOlderThan(s.db.WithContext(ctx), spec[0], spec[1], spec[2], cutoff, 1000, spec[3])
			if err != nil {
				return invDeleted, auditDeleted, err
			}
			if n > 0 {
				s.Log().Info("log retention purged", "table", spec[0], "rows", n)
			}
		}
	}
	// —— 任务生命周期日志（默认 30 天；outbox 只删已 processed 的行）——
	if d := s.retentionDaysSetting("task_log_retention_days", 30); d > 0 {
		cutoff := s.now().UTC().AddDate(0, 0, -d)
		for _, spec := range [][4]string{
			{"video_events", "id", "created_at", ""},
			{"video_attempts", "id", "observed_at", ""},
			{"video_outbox", "event_id", "created_at", "processed"},
			{"video_asset_events", "event_id", "created_at", ""},
			{"video_callback_hints", "id", "received_at", ""},
		} {
			n, err := purgeRowsOlderThan(s.db.WithContext(ctx), spec[0], spec[1], spec[2], cutoff, 1000, spec[3])
			if err != nil {
				return invDeleted, auditDeleted, err
			}
			if n > 0 {
				s.Log().Info("log retention purged", "table", spec[0], "rows", n)
			}
		}
	}
	return invDeleted, auditDeleted, nil
}

// purgeRowsOlderThan：批量删除某表时间列早于 cutoff 的行（先选 PK 再删，
// SQLite/MySQL 双方言兼容，同 purgeAuditLogsOlderThan）。table/pkCol/timeCol
// 只允许来自代码内常量，绝不拼外部输入。requireStatus 非空时仅删该状态行。
func purgeRowsOlderThan(db *gorm.DB, table, pkCol, timeCol string, cutoff time.Time, batch int, requireStatus string) (int64, error) {
	if batch <= 0 {
		batch = 1000
	}
	var total int64
	for {
		var ids []string
		sel := db.Table(table).Where(timeCol+" < ?", cutoff)
		if requireStatus != "" {
			sel = sel.Where("status = ?", requireStatus)
		}
		if err := sel.Order(pkCol).Limit(batch).Pluck(pkCol, &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		del := db.Table(table).Where(pkCol+" IN ?", ids)
		if requireStatus != "" {
			del = del.Where("status = ?", requireStatus)
		}
		res := del.Delete(nil)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < batch {
			return total, nil
		}
	}
}

// purgeAuditLogsOlderThan：分批删除 created_at(unix ms) < cutoffMs 的审计日志。
// 先选 id 再删（不依赖 DELETE...LIMIT），SQLite/MySQL 双方言兼容，同 invocation.PurgeOlderThan。
func purgeAuditLogsOlderThan(db *gorm.DB, cutoffMs int64, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1000
	}
	var total int64
	for {
		var ids []uint64
		if err := db.Model(&AuditLog{}).Where("created_at < ?", cutoffMs).
			Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.Where("id IN ?", ids).Delete(&AuditLog{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < batch {
			return total, nil
		}
	}
}
