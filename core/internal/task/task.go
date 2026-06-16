// Package task：异步任务编排（设计 §4.4 Task API；蓝本知识库 §6.2）。
// Seedance 类视频/图像生成的"提交→轮询→取产物"模式。
//
// 资金不变量：提交时 CreateHold（按计费快照估算）→ SUCCESS 时 SettleHoldExact（按上游
// 真实 completion_tokens）→ FAILURE/超时 Release 全退。终态转换 + 结算在同一事务内，
// CAS（WHERE status IN ...）防双重结算（NewAPI 任务系统陷阱 1）。
package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"

	pollBatch          = 50
	defaultTaskTimeout = 30 * time.Minute
)

type Task struct {
	ID               uint64          `gorm:"primaryKey" json:"-"`
	TaskID           string          `gorm:"column:task_id" json:"id"`
	OrgID            uint64          `gorm:"column:org_id" json:"-"`
	UserID           *uint64         `gorm:"column:user_id" json:"-"`
	APIKeyID         uint64          `gorm:"column:api_key_id" json:"-"`
	Model            string          `gorm:"column:model" json:"model"`
	ChannelID        uint64          `gorm:"column:channel_id" json:"-"`
	Provider         string          `gorm:"column:provider" json:"-"`
	Status           string          `gorm:"column:status" json:"status"`
	FailReason       string          `gorm:"column:fail_reason" json:"fail_reason,omitempty"`
	UpstreamTaskID   string          `gorm:"column:upstream_task_id" json:"-"`
	ResultURL        string          `gorm:"column:result_url" json:"result_url,omitempty"`
	HoldID           uint64          `gorm:"column:hold_id" json:"-"`
	EstTokens        int64           `gorm:"column:est_tokens" json:"-"`
	ActualTokens     int64           `gorm:"column:actual_tokens" json:"completion_tokens,omitempty"`
	CostUSD          decimal.Decimal `gorm:"column:cost_usd" json:"cost_usd"`
	PriceOutUSDPer1M decimal.Decimal `gorm:"column:price_out_usd_per_1m" json:"-"` // 计费快照（陷阱 2）
	SubmittedAt      time.Time       `gorm:"column:submitted_at;autoCreateTime:milli" json:"submitted_at"`
	StartedAt        *time.Time      `gorm:"column:started_at" json:"started_at,omitempty"`
	FinishedAt       *time.Time      `gorm:"column:finished_at" json:"finished_at,omitempty"`
}

func (Task) TableName() string { return "tasks" }

// Service：任务编排服务。M1 驻 core 进程，与 relay 同享 DB/Keyring。
type Service struct {
	DB          *gorm.DB
	Keyring     *crypto.Keyring
	Client      *http.Client
	Log         *slog.Logger
	Now         func() time.Time
	TaskTimeout time.Duration // 超过此时长未终态 → failed + 全退（陷阱 8：可配置，不硬编码）
}

func NewService(db *gorm.DB, kr *crypto.Keyring, log *slog.Logger) *Service {
	return &Service{
		DB:          db,
		Keyring:     kr,
		Client:      &http.Client{Timeout: 2 * time.Minute},
		Log:         log,
		Now:         time.Now,
		TaskTimeout: defaultTaskTimeout,
	}
}

type SubmitInput struct {
	OrgID    uint64
	UserID   *uint64
	APIKeyID uint64
	Model    string
	Content  []any // 透传上游的 content 数组（薄透传，不做参数二次发明）
}

var (
	ErrConcurrencyLimit = fmt.Errorf("task: channel concurrency limit reached")
	ErrNoTaskAdaptor    = fmt.Errorf("task: provider has no task adaptor")
)

// Submit：计费快照 → 并发闸 → hold → 上游提交 → 落任务行。
// 上游提交失败 → release，不留任务行。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*Task, error) {
	price, err := relay.GetPrice(s.DB, in.Model)
	if err != nil {
		return nil, err
	}
	// 健康分传 nil：异步任务（视频）独立于 relay 的内存健康态，沿用纯权重选路。
	ch, err := relay.PickChannelForAttempt(s.DB, in.Model, 0, nil, nil)
	if err != nil {
		return nil, err
	}
	ad, err := adaptorFor(ch.Provider)
	if err != nil {
		return nil, err
	}

	limit := ch.MaxConcurrentTasks
	if limit <= 0 {
		limit = 3 // 防御：历史行/手工插入未设值时不至于把渠道闸死
	}
	credential, err := s.Keyring.Decrypt(ch.CredentialEnc)
	if err != nil {
		return nil, fmt.Errorf("task: channel credential: %w", err)
	}

	// 原子预占（审查 M-2）：渠道行 FOR UPDATE 串行化同渠道的所有 Submit →
	// 同事务内重新计数 → hold + 插入 queued 任务行（即"占住槽位"，下一个 Submit 数得到它）。
	// 消灭 count-then-act 竞态：N 个并发不会全部过闸。
	estTokens := ad.EstimateOutputTokens(in.Model, in.Content)
	holdAmount := price.Cost(0, 0, estTokens)
	t := &Task{
		TaskID: "task_" + randHex(12),
		OrgID:  in.OrgID, UserID: in.UserID, APIKeyID: in.APIKeyID,
		Model: in.Model, ChannelID: ch.ID, Provider: ch.Provider,
		Status: StatusQueued, EstTokens: estTokens,
		PriceOutUSDPer1M: price.OutputUSDPer1M,
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		// 锁渠道行（MySQL FOR UPDATE；SQLite 测试靠单连接串行）
		lockTx := tx
		if tx.Dialector.Name() == "mysql" {
			lockTx = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		lockCh := &relay.Channel{}
		if err := lockTx.First(lockCh, ch.ID).Error; err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&Task{}).
			Where("channel_id = ? AND status IN ?", ch.ID, []string{StatusQueued, StatusRunning}).
			Count(&active).Error; err != nil {
			return err
		}
		if int(active) >= limit {
			return ErrConcurrencyLimit
		}
		// 分片感知 hold：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），分片后按 TaskID 散列打散写热点。
		h, _, err := ledger.CreateHoldSharded(tx, in.OrgID, ledger.AccountUserCredit, t.TaskID, ledger.HoldInput{
			IdempotencyKey: "task:" + t.TaskID,
			Amount:         holdAmount,
			RefType:        "task", RefID: t.TaskID,
		})
		if err != nil {
			return err
		}
		t.HoldID = h.ID
		return tx.Create(t).Error // 占住槽位
	}); err != nil {
		return nil, err
	}

	// 上游提交（槽位已占；失败 → release hold + 标记任务 failed，槽位随终态退出闸门）
	upReq, err := ad.SubmitRequest(ctx, ch, credential, in)
	if err != nil {
		s.abortSubmit(t, "request build failed")
		return nil, err
	}
	resp, err := s.Client.Do(upReq)
	if err != nil {
		s.abortSubmit(t, "upstream unreachable")
		return nil, fmt.Errorf("task: upstream unreachable: %w", err)
	}
	defer resp.Body.Close()
	upstreamID, err := ad.ParseSubmit(resp)
	if err != nil {
		s.abortSubmit(t, "upstream rejected submit")
		return nil, err
	}
	// 提交成功：回填上游 id
	if err := s.DB.Model(&Task{}).Where("id = ?", t.ID).
		Update("upstream_task_id", upstreamID).Error; err != nil {
		s.Log.Error("task upstream_task_id backfill failed (poller will not find it)",
			"task", t.TaskID, "upstream_id", upstreamID, "err", err)
	}
	t.UpstreamTaskID = upstreamID
	return t, nil
}

// abortSubmit：预占后上游提交失败——CAS 把任务转 failed 并释放 hold（与轮询失败同款语义，
// 保证槽位释放 + 资金退还，且不与并发轮询重复结算）。
func (s *Service) abortSubmit(t *Task, reason string) {
	s.settleFailure(t, "submit failed: "+reason)
}

// Get：org 隔离读取（红线 5）。
func (s *Service) Get(orgID uint64, taskID string) (*Task, error) {
	t := &Task{}
	if err := s.DB.Where("task_id = ? AND org_id = ?", taskID, orgID).First(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// PollOnce：轮询一轮——超时清扫 + 未终态任务逐个查上游并转移。
// main 的轮询循环与测试都调用它（可测性：循环与逻辑分离）。
func (s *Service) PollOnce(ctx context.Context) {
	s.sweepTimedOut()

	var tasks []Task
	if err := s.DB.Where("status IN ?", []string{StatusQueued, StatusRunning}).
		Order("id").Limit(pollBatch).Find(&tasks).Error; err != nil {
		s.Log.Error("task poll query failed", "err", err)
		return
	}
	for i := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.pollOne(ctx, &tasks[i])
	}
}

func (s *Service) pollOne(ctx context.Context, t *Task) {
	ch := &relay.Channel{}
	if err := s.DB.First(ch, t.ChannelID).Error; err != nil {
		s.Log.Error("task channel missing", "task", t.TaskID, "err", err)
		return
	}
	ad, err := adaptorFor(t.Provider)
	if err != nil {
		s.Log.Error("task adaptor missing", "task", t.TaskID, "provider", t.Provider)
		return
	}
	credential, err := s.Keyring.Decrypt(ch.CredentialEnc)
	if err != nil {
		s.Log.Error("task channel credential", "task", t.TaskID, "err", err)
		return
	}
	upReq, err := ad.QueryRequest(ctx, ch, credential, t.UpstreamTaskID)
	if err != nil {
		return
	}
	resp, err := s.Client.Do(upReq)
	if err != nil {
		s.Log.Warn("task upstream query unreachable", "task", t.TaskID, "err", err)
		return // 下轮重试
	}
	defer resp.Body.Close()
	result, err := ad.ParseQuery(resp)
	if err != nil {
		s.Log.Warn("task upstream query parse failed", "task", t.TaskID, "err", err)
		return
	}

	switch result.Status {
	case StatusRunning:
		if t.Status == StatusQueued {
			now := s.Now()
			// 非终态转移也走 CAS，输了说明并发轮询已处理
			s.DB.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusQueued).
				Updates(map[string]any{"status": StatusRunning, "started_at": now})
		}
	case StatusSucceeded:
		s.settleSuccess(t, result)
	case StatusFailed:
		s.settleFailure(t, result.FailReason)
	}
}

// settleSuccess：CAS 终态转移 + 资金结算在同一事务（赢者结算，输者无操作）。
func (s *Service) settleSuccess(t *Task, result QueryResult) {
	actual := decimal.Zero
	price := &relay.ModelPrice{OutputUSDPer1M: t.PriceOutUSDPer1M} // 计费快照，不读实时价
	if result.CompletionTokens > 0 {
		actual = price.Cost(0, 0, result.CompletionTokens)
	} else {
		// 上游没给 usage：按预估收（保守；上游 usage 缺失会在日志可见）
		actual = price.Cost(0, 0, t.EstTokens)
		s.Log.Warn("task usage missing from upstream, charged by estimate", "task", t.TaskID)
	}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		now := s.Now()
		res := tx.Model(&Task{}).
			Where("id = ? AND status IN ?", t.ID, []string{StatusQueued, StatusRunning}).
			Updates(map[string]any{
				"status": StatusSucceeded, "result_url": result.ResultURL,
				"actual_tokens": result.CompletionTokens, "cost_usd": actual,
				"finished_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // CAS 输了：另一个轮询已结算，本事务什么都不做（陷阱 1）
		}
		_, err := ledger.SettleHoldExact(tx, t.HoldID, actual, "task", t.TaskID,
			fmt.Sprintf("task model=%s tokens=%d", t.Model, result.CompletionTokens))
		return err
	})
	if err != nil {
		s.Log.Error("TASK SETTLE FAILED — manual reconcile needed", "task", t.TaskID, "err", err)
		return
	}
	s.Log.Info("task succeeded", "task", t.TaskID, "cost", actual)
}

func (s *Service) settleFailure(t *Task, reason string) {
	if reason == "" {
		reason = "upstream reported failure"
	}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		now := s.Now()
		res := tx.Model(&Task{}).
			Where("id = ? AND status IN ?", t.ID, []string{StatusQueued, StatusRunning}).
			Updates(map[string]any{"status": StatusFailed, "fail_reason": reason, "finished_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // CAS 输了
		}
		return ledger.Release(tx, t.HoldID) // 失败全退
	})
	if err != nil {
		s.Log.Error("TASK FAIL-RELEASE FAILED — funds stuck", "task", t.TaskID, "err", err)
		return
	}
	s.Log.Warn("task failed", "task", t.TaskID, "reason", reason)
}

// sweepTimedOut：僵尸任务清扫（CAS + 全退；上限每轮 100，知识库 §6.2）。
func (s *Service) sweepTimedOut() {
	cutoff := s.Now().Add(-s.TaskTimeout)
	var tasks []Task
	if err := s.DB.Where("status IN ? AND submitted_at < ?",
		[]string{StatusQueued, StatusRunning}, cutoff).Limit(100).Find(&tasks).Error; err != nil {
		return
	}
	for i := range tasks {
		s.settleFailure(&tasks[i], fmt.Sprintf("task timeout (%s)", s.TaskTimeout))
	}
}

func (s *Service) releaseHold(holdID uint64) {
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		return ledger.Release(tx, holdID)
	}); err != nil {
		s.Log.Error("TASK RELEASE FAILED — funds stuck in hold", "hold", holdID, "err", err)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
