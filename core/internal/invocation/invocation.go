// Package invocation：Invocation Store 元数据层（设计 §6.1）。
//
// 写入纪律（红线）：
//   - 绝不阻塞 relay 热路径——Record() 非阻塞投递，队列满则丢弃并计数（"宁缺不卡"）
//   - error_brief 等字段禁 payload/PII 明文；payload 体后续走 R2 信封加密，本表只持指针
//   - 元数据是计费审计的一部分，per-org zero-retention 只豁免 payload，不豁免元数据
package invocation

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// 终态状态值
const (
	StatusSuccess             = "success"
	StatusUpstreamError       = "upstream_error"
	StatusNoChannel           = "no_channel"
	StatusInsufficientBalance = "insufficient_balance"

	UsageSourceUpstream  = "upstream"
	UsageSourceEstimated = "estimated"
)

type Invocation struct {
	ID                uint64          `gorm:"primaryKey" json:"id"`
	OrgID             uint64          `gorm:"column:org_id" json:"-"`
	UserID            *uint64         `gorm:"column:user_id" json:"user_id,omitempty"`
	APIKeyID          uint64          `gorm:"column:api_key_id" json:"api_key_id"`
	RequestID         string          `gorm:"column:request_id" json:"request_id"`
	Model             string          `gorm:"column:model" json:"model"`
	ChannelID         uint64          `gorm:"column:channel_id" json:"-"` // 渠道归属是平台内部信息
	Provider          string          `gorm:"column:provider" json:"-"`
	Status            string          `gorm:"column:status" json:"status"`
	StatusCode        int             `gorm:"column:status_code" json:"status_code"`
	IsStream          bool            `gorm:"column:is_stream" json:"is_stream"`
	Attempts          int             `gorm:"column:attempts" json:"-"`
	PromptTokens      int64           `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionTokens  int64           `gorm:"column:completion_tokens" json:"completion_tokens"`
	CostUSD           decimal.Decimal `gorm:"column:cost_usd" json:"cost_usd"`
	UsageSource       string          `gorm:"column:usage_source" json:"usage_source,omitempty"`
	FRTMs             int             `gorm:"column:frt_ms" json:"frt_ms"`
	DurationMs        int             `gorm:"column:duration_ms" json:"duration_ms"`
	UpstreamRequestID string          `gorm:"column:upstream_request_id" json:"-"`
	ErrorBrief        string          `gorm:"column:error_brief" json:"error_brief,omitempty"`
	PayloadObjectKey  string          `gorm:"column:payload_object_key" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime:milli" json:"created_at"`
}

func (Invocation) TableName() string { return "invocations" }

const (
	defaultBuffer = 4096
	batchSize     = 64
	flushInterval = 250 * time.Millisecond
)

// Recorder：异步批量写入器。单 worker 顺序消费，批量 INSERT；
// 写库失败只告警不重试（元数据尽力而为；计费事实源是 journal，不依赖本表）。
type Recorder struct {
	db      *gorm.DB
	log     *slog.Logger
	ch      chan *Invocation
	wg      sync.WaitGroup
	dropped atomic.Int64
	closed  atomic.Bool
}

func NewRecorder(db *gorm.DB, log *slog.Logger) *Recorder {
	r := &Recorder{
		db:  db,
		log: log.With("component", "invocation"),
		ch:  make(chan *Invocation, defaultBuffer),
	}
	r.wg.Add(1)
	go r.worker()
	return r
}

// Record 非阻塞投递。队列满 → 丢弃 + 计数告警（绝不阻塞 relay 热路径）。
func (r *Recorder) Record(inv *Invocation) {
	if r.closed.Load() {
		return
	}
	select {
	case r.ch <- inv:
	default:
		if n := r.dropped.Add(1); n%100 == 1 {
			r.log.Warn("invocation queue full, records dropped", "total_dropped", n)
		}
	}
}

// Dropped 返回累计丢弃数（暴露给指标）。
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }

// Close 停止接收并排空队列（优雅停机路径调用；幂等）。
func (r *Recorder) Close() {
	if r.closed.Swap(true) {
		return
	}
	close(r.ch)
	r.wg.Wait()
}

func (r *Recorder) worker() {
	defer r.wg.Done()
	batch := make([]*Invocation, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := r.db.CreateInBatches(batch, batchSize).Error; err != nil {
			r.log.Error("invocation batch insert failed", "count", len(batch), "err", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case inv, ok := <-r.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, inv)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// PurgeOlderThan 删除 cutoff 之前的记录，分批进行（避免长事务锁表）。
// 返回删除总数。SQLite/MySQL 双方言兼容（先选 id 再删，不依赖 DELETE...LIMIT）。
func PurgeOlderThan(db *gorm.DB, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1000
	}
	var total int64
	for {
		var ids []uint64
		if err := db.Model(&Invocation{}).Where("created_at < ?", cutoff).
			Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.Where("id IN ?", ids).Delete(&Invocation{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if len(ids) < batch {
			return total, nil
		}
	}
}
