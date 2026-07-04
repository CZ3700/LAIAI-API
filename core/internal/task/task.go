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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
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
	CredentialID     uint64          `gorm:"column:credential_id" json:"-"` // 提交时选定的 key（0035）；轮询固定用它
	EstTokens        int64           `gorm:"column:est_tokens" json:"-"`
	ActualTokens     int64           `gorm:"column:actual_tokens" json:"completion_tokens,omitempty"`
	CostUSD          decimal.Decimal `gorm:"column:cost_usd" json:"cost_usd"`
	PriceOutUSDPer1M decimal.Decimal `gorm:"column:price_out_usd_per_1m" json:"-"` // 计费快照（陷阱 2）
	HoldAmountUSD    decimal.Decimal `gorm:"column:hold_amount_usd" json:"-"`      // 冻结额快照 = key quota 预留额（0032）；0=旧行无预留
	GroupRatio       decimal.Decimal `gorm:"column:group_ratio" json:"-"`          // 计费倍率快照（0032）：hold 与结算同值纪律
	SubmittedAt      time.Time       `gorm:"column:submitted_at;autoCreateTime:milli" json:"submitted_at"`
	StartedAt        *time.Time      `gorm:"column:started_at" json:"started_at,omitempty"`
	FinishedAt       *time.Time      `gorm:"column:finished_at" json:"finished_at,omitempty"`
}

func (Task) TableName() string { return "tasks" }

// Gate：数据面准入原语（keyGate 三项 + (org,model) 模型级限流），由 *relay.Relay 实现。
// task 不自建白名单/限流逻辑——与 chat 同一实例、同一 Redis/进程内窗口、同一语义（指令 P0-1）。
type Gate interface {
	GateKey(key *identity.APIKey, clientIP, model string) (code int, msg string, ok bool)
	AllowModelRate(orgID uint64, model string) (ok bool, rpm int)
}

// Service：任务编排服务。M1 驻 core 进程，与 relay 同享 DB/Keyring。
type Service struct {
	DB          *gorm.DB
	Keyring     *crypto.Keyring
	Client      *http.Client
	Log         *slog.Logger
	Now         func() time.Time
	TaskTimeout time.Duration        // 超过此时长未终态 → failed + 全退（陷阱 8：可配置，不硬编码）
	Gate        Gate                 // 准入（P0-1）：server 装配时注入 relay 实例；HandleCreate 缺装配即拒绝
	Rec         *invocation.Recorder // 任务终态落 Invocation Store（请求日志可见视频消费）；nil=不记录
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
	// Params：model/content 之外的顶层任务参数（Seedance 2.0：generate_audio/ratio/duration/
	// watermark/resolution 等），原样透传上游。同时喂给 EstimateOutputTokens 做
	// duration/resolution 感知的 hold 上界（§3.8 "按 duration、resolution 预扣"）。
	Params map[string]any
}

var (
	ErrConcurrencyLimit = fmt.Errorf("task: channel concurrency limit reached")
	ErrNoTaskAdaptor    = fmt.Errorf("task: provider has no task adaptor")
	ErrKeyQuotaExceeded = fmt.Errorf("task: api key quota exceeded")
)

// UpstreamRejectedError：上游同步拒绝提交（400 参数类错误——参数透传后客户端自己的问题）。
// handler 转 400 + 上游解释文本给客户端（与向客户端透传上游错误体同性质，非日志面）。
type UpstreamRejectedError struct {
	StatusCode int
	Message    string
}

func (e *UpstreamRejectedError) Error() string {
	return fmt.Sprintf("task: upstream rejected submit (%d): %s", e.StatusCode, e.Message)
}

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

	// 选 key（0034 Multi-key）：任务渠道同样支持多 key（低 QPS，直查 DB）。task 不做 key 级
	// 自动禁用（异步轮询与提交分离，禁用语义留给 relay 主链路的健康信号）。
	// 提交选定的 credID 钉在任务行（0035）：轮询固定用同一把 key，绝不与提交用不同 key
	// 导致上游查不到任务（对抗 review 修复）。
	credID, credEnc, credOK := relay.SelectCredential(s.DB, ch)
	if !credOK {
		return nil, fmt.Errorf("task: channel has no usable credential")
	}
	credential, err := s.Keyring.Decrypt(credEnc)
	if err != nil {
		return nil, fmt.Errorf("task: channel credential: %w", err)
	}

	// 原子预占（审查 M-2）：渠道行 FOR UPDATE 串行化同渠道的所有 Submit →
	// 同事务内重新计数 → hold + 插入 queued 任务行（即"占住槽位"，下一个 Submit 数得到它）。
	// 消灭 count-then-act 竞态：N 个并发不会全部过闸。
	estTokens := ad.EstimateOutputTokens(in.Model, in.Content, in.Params)
	// 计费倍率快照（0011 一层 × 0027 二层）：hold 与结算取同一次值（chat 同款纪律）。
	// 提交 QPS 低，直读 DB 不走 relay 热读缓存。
	ratio := relay.EffectiveRatio(s.DB, in.OrgID, in.Model)
	holdAmount := price.Cost(0, 0, estTokens).Mul(ratio)
	// API Key 额度原子预留（P0-1，chat 的 P0-3 同款）：防并发提交超 key quota。
	// 退还全覆盖：下方事务失败立即退；任务行落地后由 settle 终态按 hold_amount_usd 快照对账/退还。
	if !identity.ReserveKeyQuota(s.DB, in.APIKeyID, holdAmount) {
		return nil, ErrKeyQuotaExceeded
	}
	t := &Task{
		TaskID: "task_" + randHex(12),
		OrgID:  in.OrgID, UserID: in.UserID, APIKeyID: in.APIKeyID,
		Model: in.Model, ChannelID: ch.ID, Provider: ch.Provider,
		Status: StatusQueued, EstTokens: estTokens,
		PriceOutUSDPer1M: price.OutputUSDPer1M,
		HoldAmountUSD:    holdAmount, GroupRatio: ratio,
		CredentialID:     credID, // 钉住提交用的 key（0035），轮询固定用它
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
		// 用锁内重读的并发上限（而非锁外 ch 旧值）：与 FOR UPDATE 重读初衷一致，即便上限被并发下调也按最新值判闸。
		limit := lockCh.MaxConcurrentTasks
		if limit <= 0 {
			limit = 3 // 防御：历史行/手工插入未设值时不至于把渠道闸死
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
		// 任务行未落地：这里是 key quota 预留退还的唯一同步出口（落地后统一由 settle 终态退/调）
		_ = identity.AddKeyUsageSigned(s.DB, in.APIKeyID, holdAmount.Neg())
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
		var rej *UpstreamRejectedError
		if errors.As(err, &rej) {
			// 上游 400 参数拒绝：上游原文按 upstreamOrigin 纪律只进 fail_reason（org 隔离 API 数据），
			// 日志/invocation 用合成摘要（红线 7）。
			s.settleFailure(t, "submit rejected: "+rej.Message, true)
		} else {
			s.abortSubmit(t, "upstream rejected submit")
		}
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
	s.settleFailure(t, "submit failed: "+reason, false)
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
	if t.UpstreamTaskID == "" {
		// 提交后回填 upstream_task_id 之前的在途窗口被 poller 捞到：空 id 查询注定 404，直接跳过；
		// 若回填永久失败，sweepTimedOut 会按超时把该 queued 任务 settleFailure 全退（资金不卡）。
		s.Log.Warn("task has empty upstream_task_id, skipping poll (sweep will time out)", "task", t.TaskID)
		return
	}
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
	// 轮询固定用提交时选定的 key（0035，对抗 review）：多 key 渠道下提交/轮询 key 不一致会
	// 让上游查不到任务。按 credential_id 取那把 key（即使后来被禁也用它查在途任务，不孤儿化）；
	// credID=0（legacy 单 key）回退 channels.credential_enc。
	credEnc, credOK := ch.CredentialEnc, ch.CredentialEnc != ""
	if t.CredentialID != 0 {
		credEnc, credOK = relay.CredentialByID(s.DB, t.CredentialID)
	}
	if !credOK {
		s.Log.Error("task channel has no usable credential", "task", t.TaskID)
		return
	}
	credential, err := s.Keyring.Decrypt(credEnc)
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
		s.settleFailure(t, result.FailReason, true) // FailReason 是上游原文（可能回显 prompt 片段）
	}
}

// settleSuccess：CAS 终态转移 + 资金结算在同一事务（赢者结算，输者无操作）。
func (s *Service) settleSuccess(t *Task, result QueryResult) {
	price := &relay.ModelPrice{OutputUSDPer1M: t.PriceOutUSDPer1M} // 计费快照，不读实时价
	ratio := t.GroupRatio
	if !ratio.IsPositive() {
		ratio = decimal.NewFromInt(1) // 0032 前旧行/异常快照：按 1 兜底（与历史行为一致）
	}
	actual := decimal.Zero
	usageSource := invocation.UsageSourceUpstream
	if result.CompletionTokens > 0 {
		actual = price.Cost(0, 0, result.CompletionTokens).Mul(ratio)
	} else {
		// 上游没给 usage：按预估收（保守；上游 usage 缺失会在日志可见）
		actual = price.Cost(0, 0, t.EstTokens).Mul(ratio)
		usageSource = invocation.UsageSourceEstimated
		s.Log.Warn("task usage missing from upstream, charged by estimate", "task", t.TaskID)
	}
	won := false
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
		won = true
		_, err := ledger.SettleHoldExact(tx, t.HoldID, actual, "task", t.TaskID,
			fmt.Sprintf("task model=%s tokens=%d", t.Model, result.CompletionTokens))
		return err
	})
	if err != nil {
		s.Log.Error("TASK SETTLE FAILED — manual reconcile needed", "task", t.TaskID, "err", err)
		return
	}
	if !won {
		return
	}
	// key 额度对账到实收（chat capture 同款，best-effort）：0032 前旧行（快照=0）无预留、不调整。
	if t.HoldAmountUSD.IsPositive() {
		_ = identity.AddKeyUsageSigned(s.DB, t.APIKeyID, actual.Sub(t.HoldAmountUSD))
	}
	s.recordInvocation(t, invocation.StatusSuccess, http.StatusOK, result.CompletionTokens, actual, usageSource, "")
	s.Log.Info("task succeeded", "task", t.TaskID, "cost", actual)
}

// settleFailure：CAS 终态转移 + 全额退款。upstreamOrigin=true 表示 reason 是上游原文
// （审核类失败 message 可能回显 prompt 片段）：完整 clipped 原文只落 tasks.fail_reason
// （org 隔离、用户可见的 API 数据，与向客户端转发上游错误同性质）；invocation error_brief
// 与 stdout 日志一律用合成摘要——上游可控文本不进操作日志/请求日志（红线 7，对抗 review）。
func (s *Service) settleFailure(t *Task, reason string, upstreamOrigin bool) {
	if reason == "" {
		reason = "upstream reported failure"
	}
	reason = clipReason(reason, 255) // fail_reason VARCHAR(255) 上限
	brief := reason
	if upstreamOrigin {
		brief = "upstream reported failure"
	}
	won := false
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
		won = true
		return ledger.Release(tx, t.HoldID) // 失败全退
	})
	if err != nil {
		s.Log.Error("TASK FAIL-RELEASE FAILED — funds stuck", "task", t.TaskID, "err", err)
		return
	}
	if !won {
		return
	}
	if t.HoldAmountUSD.IsPositive() {
		_ = identity.AddKeyUsageSigned(s.DB, t.APIKeyID, t.HoldAmountUSD.Neg()) // 预留全退（与 ledger Release 同向）
	}
	s.recordInvocation(t, invocation.StatusUpstreamError, http.StatusBadGateway, 0, decimal.Zero, "", brief)
	s.Log.Warn("task failed", "task", t.TaskID, "reason", brief)
}

// recordInvocation：任务终态落 Invocation Store（视频消费进请求日志/用量口径，P0-1 配套）。
// request_id 复用对外 task_id（uk_inv_request 唯一键 + CAS 赢者唯一，天然不重）。
func (s *Service) recordInvocation(t *Task, status string, code int, tokens int64, cost decimal.Decimal, usageSource, brief string) {
	if s.Rec == nil {
		return
	}
	s.Rec.Record(&invocation.Invocation{
		OrgID: t.OrgID, UserID: t.UserID, APIKeyID: t.APIKeyID,
		RequestID: t.TaskID, Model: t.Model,
		ChannelID: t.ChannelID, Provider: t.Provider,
		Status: status, StatusCode: code,
		CompletionTokens: tokens, CostUSD: cost, UsageSource: usageSource,
		DurationMs: int(s.Now().Sub(t.SubmittedAt).Milliseconds()),
		ErrorBrief: brief,
	})
}

// clipReason：按字节截断并去除尾部半个 rune（utf8mb4 列防非法 UTF-8，与 relay.clipMeta 同款）。
func clipReason(sv string, maxBytes int) string {
	if len(sv) <= maxBytes {
		return sv
	}
	return strings.ToValidUTF8(sv[:maxBytes], "")
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
		s.settleFailure(&tasks[i], fmt.Sprintf("task timeout (%s)", s.TaskTimeout), false)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
