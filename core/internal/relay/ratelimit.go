package relay

import (
	"fmt"
	"strconv"
	"time"
)

// 模型级限流（gap #2，对标 NewAPI ModelRequestRateLimit）：每 (org, model) 固定 60s 窗口计数。
// **进程内**实现——单实例部署正确；多实例必须换 Redis 原子计数（设计 §4.4 限流红线）。
// 策略从 ext_settings 读，5s 缓存避免每请求打库。0/关闭 = 不限。

type rateWindow struct {
	start time.Time
	count int
}

// modelRatePolicy：返回 (是否启用, 每分钟上限)。带 5s 缓存。
func (r *Relay) modelRatePolicy() (bool, int) {
	now := r.Now()
	r.ratePolMu.Lock()
	if now.Sub(r.ratePolAt) < 5*time.Second {
		en, rpm := r.rateEnabled, r.rateRPM
		r.ratePolMu.Unlock()
		return en, rpm
	}
	r.ratePolMu.Unlock()

	enabled, rpm := false, 0
	var rows []struct{ Name, Value string }
	r.DB.Table("ext_settings").Where("name IN ?",
		[]string{"model_rate_limit_enabled", "model_rate_limit_rpm"}).Find(&rows)
	for _, row := range rows {
		switch row.Name {
		case "model_rate_limit_enabled":
			enabled = row.Value == "true"
		case "model_rate_limit_rpm":
			if n, err := strconv.Atoi(row.Value); err == nil && n > 0 {
				rpm = n
			}
		}
	}
	r.ratePolMu.Lock()
	r.rateEnabled, r.rateRPM, r.ratePolAt = enabled, rpm, now
	r.ratePolMu.Unlock()
	return enabled, rpm
}

// allowModelRate：(org, model) 在当前 60s 窗口内是否还可放行。未启用/RPM=0 一律放行。
func (r *Relay) allowModelRate(orgID uint64, model string) (ok bool, rpm int) {
	enabled, limit := r.modelRatePolicy()
	if !enabled || limit <= 0 {
		return true, 0
	}
	key := fmt.Sprintf("%d:%s", orgID, model)
	now := r.Now()

	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	// 惰性驱逐：每 5 分钟清掉 2 分钟前的旧窗口，防 map 无界增长
	if now.Sub(r.rateLastEvic) > 5*time.Minute {
		for k, w := range r.rateWindows {
			if now.Sub(w.start) > 2*time.Minute {
				delete(r.rateWindows, k)
			}
		}
		r.rateLastEvic = now
	}
	w := r.rateWindows[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		r.rateWindows[key] = &rateWindow{start: now, count: 1}
		return true, limit
	}
	if w.count >= limit {
		return false, limit
	}
	w.count++
	return true, limit
}
