package server

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// 状态等级（公开状态页用）：从好到坏，整体状态取各组件最差。
const (
	statusOperational = "operational"
	statusDegraded    = "degraded"
	statusDown        = "down"
)

// statusRank：用于取"最差"组件作为整体状态。
func statusRank(s string) int {
	switch s {
	case statusDown:
		return 2
	case statusDegraded:
		return 1
	default:
		return 0
	}
}

// publicStatus：GET /api/status —— 公开状态页数据（无需登录）。
// **只暴露粗粒度组件状态**（operational/degraded/down），不泄露渠道名/厂商/成功率原始数字/
// 任何 org 数据——状态页是对外信任信号，不是运营看板（运营细节在 admin 渠道健康）。
func (s *Server) publicStatus(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())

	// 1. 数据库：ping。挂了则整体 down，跳过其余查询（查询本身也会失败）。
	dbStatus := statusOperational
	if sqlDB, err := s.db.DB(); err != nil || sqlDB.Ping() != nil {
		dbStatus = statusDown
	}

	// 2. relay（LLM 网关）：无活跃渠道=down；近 1h 样本足够且成功率低=degraded/down；否则乐观。
	relayStatus := statusOperational
	if dbStatus == statusDown {
		relayStatus = statusDown // 库挂了无从判断，保守标记
	} else {
		var activeChannels int64
		rdb.Model(&relay.Channel{}).Where("status = 1").Count(&activeChannels)
		if activeChannels == 0 {
			relayStatus = statusDown // 无上游容量
		} else {
			since := s.now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
			var total, success int64
			rdb.Model(&invocation.Invocation{}).Where("created_at >= ?", since).Count(&total)
			if total >= 20 { // 样本足够才据成功率判级（防个别失败误报）
				rdb.Model(&invocation.Invocation{}).
					Where("created_at >= ? AND status = ?", since, invocation.StatusSuccess).Count(&success)
				rate := float64(success) / float64(total)
				switch {
				case rate < 0.5:
					relayStatus = statusDown
				case rate < 0.9:
					relayStatus = statusDegraded
				}
			}
		}
	}

	// API 控制面：我们正在应答 = operational（库挂时降级为 degraded）。
	apiStatus := statusOperational
	if dbStatus == statusDown {
		apiStatus = statusDegraded
	}

	components := []gin.H{
		{"key": "api", "status": apiStatus},
		{"key": "database", "status": dbStatus},
		{"key": "relay", "status": relayStatus},
	}
	overall := statusOperational
	for _, comp := range components {
		if statusRank(comp["status"].(string)) > statusRank(overall) {
			overall = comp["status"].(string)
		}
	}

	code := http.StatusOK
	if overall == statusDown {
		code = http.StatusServiceUnavailable // 监控/uptime 探针可据 HTTP 码告警
	}
	c.JSON(code, gin.H{
		"status":     overall,
		"components": components,
		"updated_at": s.now().UTC().Format("2006-01-02T15:04:05Z"),
	})
}
