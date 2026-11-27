package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// adminLogStats：GET /api/admin/log-stats —— 日志量与保留期一览（gap P2-C，对标 NewAPI 日志统计）。
// 运营据此判断清理影响：各表总行数 + 最旧记录时间 + 当前生效保留期。空表 oldest 为 null。
func (s *Server) adminLogStats(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var invTotal, auditTotal, taskTotal int64
	rdb.Model(&invocation.Invocation{}).Count(&invTotal)
	rdb.Model(&AuditLog{}).Count(&auditTotal)
	rdb.Model(&task.Task{}).Count(&taskTotal)

	var invOldest *string
	rdb.Model(&invocation.Invocation{}).Select("CAST(MIN(created_at) AS TEXT)").Scan(&invOldest)
	var auditOldestMs *int64 // audit_logs.created_at 是 unix 毫秒
	rdb.Model(&AuditLog{}).Select("MIN(created_at)").Scan(&auditOldestMs)

	c.JSON(http.StatusOK, gin.H{
		"invocations":    gin.H{"total": invTotal, "oldest_at": invOldest},
		"audit_logs":     gin.H{"total": auditTotal, "oldest_at_ms": auditOldestMs},
		"request_logs":   gin.H{"total": invTotal, "contexts": 0},
		"task_lifecycle": gin.H{"tasks": taskTotal},
		"retention": gin.H{ // 当前生效保留期（已并入 env 回退），前端展示「清理将影响…」
			"log_retention_days":       s.retentionDaysSetting("log_retention_days", s.invRetentionDefault),
			"audit_log_retention_days": s.retentionDaysSetting("audit_log_retention_days", 0),
			"task_log_retention_days":  s.retentionDaysSetting("task_log_retention_days", 30),
		},
	})
}

// retentionDaysSetting：读某保留期设置，非法/空 → 回退 fallback。
func (s *Server) retentionDaysSetting(key string, fallback int) int {
	if v := s.getSetting(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
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
