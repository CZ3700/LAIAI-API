package video

import (
	"sync"
	"time"
)

// 受理侧 per-model 限额的热读缓存。并发闸（MaxConcurrentTasks）在受理事务内取
// 行锁后的 p.config 权威值，不走这里；这里只服务受理入口的 CreateRPM 快速失败——
// 每请求省一次 DB 往返，窗口状态本身在 relay（Redis/进程内）。5s 陈旧度与 relay
// modelRatePolicy 的策略缓存同款：管理端改限最多 5s 生效，远快于客户端 Retry-After。
type modelLimitCache struct {
	mu       sync.Mutex
	rpm      map[string]int
	loadedAt time.Time
}

const modelLimitCacheTTL = 5 * time.Second

// modelCreateRPM 返回该模型当前的平台受理 RPM 上限（0=不限）。查库失败视为 0
// （fail-open：限流是保护性闸，不因配置读失败而拒绝全量受理；并发闸不受影响，
// 它始终取事务内权威值）。
func (s *Service) modelCreateRPM(model string) int {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := s.Now()
	if s.limitCache.rpm == nil || now.Sub(s.limitCache.loadedAt) >= modelLimitCacheTTL {
		rpm := map[string]int{}
		var rows []ModelConfig
		if err := s.DB.
			Table("video_model_configs").
			Select("public_model, create_rpm").
			Where("enabled = ? AND create_rpm > 0", true).
			Find(&rows).Error; err == nil {
			for _, r := range rows {
				rpm[r.PublicModel] = r.CreateRPM
			}
			s.limitCache.rpm = rpm
			s.limitCache.loadedAt = now
		}
	}
	if s.limitCache.rpm == nil {
		return 0
	}
	return s.limitCache.rpm[model]
}
