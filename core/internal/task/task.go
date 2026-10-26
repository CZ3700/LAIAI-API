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
	"net/url"
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
	ResultRef        string          `gorm:"column:result_ref" json:"-"` // 产物内部引用（0038）：平台 content 代理取流用，绝不外泄（veo URI 带渠道 key）
	HoldID           uint64          `gorm:"column:hold_id" json:"-"`
	CredentialID     uint64          `gorm:"column:credential_id" json:"-"` // 提交时选定的 key（0035）；轮询固定用它
	EstTokens        int64           `gorm:"column:est_tokens" json:"-"`
	ActualTokens     int64           `gorm:"column:actual_tokens" json:"completion_tokens,omitempty"`
	CostUSD          decimal.Decimal `gorm:"column:cost_usd" json:"cost_usd"`
	PriceOutUSDPer1M decimal.Decimal `gorm:"column:price_out_usd_per_1m" json:"-"` // 计费快照（陷阱 2）
	PricePerCallUSD  decimal.Decimal `gorm:"column:price_per_call_usd" json:"-"`   // 按次计价快照（0048）：MJ 等 PerCallUSD 模型结算依据
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

// Notifier：任务终态通知（§4.9 Notify）。task 包不依赖 notify 包（避免跨 bounded context），
// 由 server 装配时注入 *notify.Dispatcher.Emit 的适配。nil = 不通知（best-effort，绝不影响结算）。
type Notifier interface {
	Emit(orgID uint64, event string, payload map[string]any)
}

// 通知事件名（与 notify 包常量对齐；此处独立声明避免包依赖倒置）。
const (
	eventTaskCompleted = "task.completed"
	eventTaskFailed    = "task.failed"
)

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
	Notify      Notifier             // 终态通知（§4.9）；nil=不通知（best-effort，绝不影响结算）
	pollCursor  uint64               // 轮询游标（B-4 饥饿修复）：上次轮询到的最大 task id，回绕轮转。
	// 单 leader 串行调用 PollOnce（main.go ticker），无并发，无需锁。
}

// emitNotify：best-effort 终态通知（payload 元数据-only，红线 7）。nil-safe。
func (s *Service) emitNotify(t *Task, event string) {
	if s.Notify == nil {
		return
	}
	s.Notify.Emit(t.OrgID, event, map[string]any{
		"task_id": t.TaskID, "model": t.Model, "status": t.Status,
		"cost_usd": t.CostUSD.StringFixed(8),
	})
}

func NewService(db *gorm.DB, kr *crypto.Keyring, log *slog.Logger) *Service {
	return &Service{
		DB:          db,
		Keyring:     kr,
		Client:      relay.NewUpstreamClient(2 * time.Minute),
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
	// PinChannelID：引用类动作（MJ change/modal）钉回原任务渠道。上游任务 id 是单个 mjproxy
	// 网关本地的——按权重独立选路/换渠道故障转移都会把续操作投给不持有该任务的网关（对抗
	// review CONFIRMED）。非 0 时跳过选路与 failover。
	PinChannelID uint64
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
	var ch *relay.Channel
	if in.PinChannelID != 0 {
		// 渠道亲和（MJ change/modal）：引用任务的上游 id 只在原网关存在，强制钉回。
		ch = &relay.Channel{}
		if err := s.DB.First(ch, in.PinChannelID).Error; err != nil || ch.Status != 1 {
			return nil, relay.ErrNoChannel
		}
	} else {
		var err error
		ch, err = relay.PickChannelForAttempt(s.DB, in.Model, 0, nil, nil)
		if err != nil {
			return nil, err
		}
	}
	ad, err := adaptorFor(ch.Provider)
	if officialArkChannel(ch) {
		return nil, relay.ErrNoChannel
	}
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
	// ChatCost = 按次计价感知（PerCallUSD>0 直接按次，MJ 等；否则回退 token 口径——视频模型不变）
	holdAmount := price.ChatCost(0, 0, estTokens).Mul(ratio)
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
		PricePerCallUSD:  price.PerCallUSD, // 0048：按次计价快照（MJ 等），settle 与 hold 同口径

		HoldAmountUSD: holdAmount, GroupRatio: ratio,
		CredentialID: credID, // 钉住提交用的 key（0035），轮询固定用它
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

	// 上游提交（槽位已占）+ 换渠道故障转移（顶级审计修 2026-07 排名 1）：
	// 可重试失败（构建失败/不可达/5xx 解析失败）→ 换下一渠道重试（hold/任务行不动，只改钉渠道）；
	// 上游 400 参数拒绝 → 终止不换渠道（换渠道也 400，客户端自己的问题）；
	// 候选耗尽 → release hold + 任务 failed（与旧单渠道行为一致的收尾语义）。
	tried := map[uint64]bool{}
	var lastErr error
	for attempt := 0; attempt < maxSubmitAttempts; attempt++ {
		if attempt > 0 {
			if in.PinChannelID != 0 {
				break // 钉渠道（MJ 引用类动作）：换渠道无意义——目标网关不持有该任务
			}
			next, nextAd, nextCred, ok := s.nextSubmitCandidate(in.Model, attempt, tried, t)
			if !ok {
				break // 无更多可用候选（或任务已被并发结算）
			}
			ch, ad, credential = next, nextAd, nextCred
			// prev_err 只落合成摘要：*url.Error 会回显完整上游 URL，未来 adapter 若在
			// query 携凭证即泄漏（红线 7，与 handlers 的 StreamResult 同款纪律）。
			s.Log.Warn("task submit failover to another channel",
				"task", t.TaskID, "channel", ch.ID, "attempt", attempt, "prev_err", summarizeSubmitErr(lastErr))
		}
		tried[ch.ID] = true

		upReq, err := ad.SubmitRequest(ctx, ch, credential, in)
		if err != nil {
			lastErr = err
			continue // 构建失败：换渠道（不同 provider 构建路径不同）
		}
		resp, err := s.Client.Do(upReq)
		if err != nil {
			lastErr = fmt.Errorf("task: upstream unreachable: %w", err)
			continue // 网络不可达：换渠道
		}
		upstreamID, err := ad.ParseSubmit(resp)
		resp.Body.Close()
		if err != nil {
			var rej *UpstreamRejectedError
			if errors.As(err, &rej) {
				// 上游 400 参数拒绝：上游原文按 upstreamOrigin 纪律只进 fail_reason（org 隔离 API 数据），
				// 日志/invocation 用合成摘要（红线 7）。确定性客户端错误，换渠道无意义。
				s.settleFailure(t, "submit rejected: "+rej.Message, true)
				return nil, err
			}
			lastErr = err
			continue // 5xx/意外响应体：换渠道
		}
		// 去重（对抗 review CONFIRMED：mjproxy code 21"任务已存在"返回旧任务 id，上游没起新生成）：
		// 同 org+渠道+上游 id 已有非 failed 任务 → 本次是重复提交（客户端重试/重复点击），
		// 释放本次 hold + 删本占位行 + 退 key quota，把已有任务返回给客户端——绝不对同一次生成收两次。
		// 视频类厂商每次提交必发新 id，此查询恒空，零行为变化。
		var dup Task
		if err := s.DB.Where("org_id = ? AND channel_id = ? AND upstream_task_id = ? AND status <> ? AND id <> ?",
			t.OrgID, ch.ID, upstreamID, StatusFailed, t.ID).First(&dup).Error; err == nil {
			if derr := s.DB.Transaction(func(tx *gorm.DB) error {
				if err := ledger.Release(tx, t.HoldID); err != nil {
					return err
				}
				return tx.Delete(&Task{}, t.ID).Error
			}); derr != nil {
				s.Log.Error("task dedup release failed — manual reconcile needed", "task", t.TaskID, "err", derr)
				// release 失败时保留原任务行走正常轮询（宁可重复计费告警人工对账，不丢资金记录）
			} else {
				_ = identity.AddKeyUsageSigned(s.DB, in.APIKeyID, holdAmount.Neg())
				s.Log.Info("task dedup: upstream returned existing task, reusing",
					"task", dup.TaskID, "upstream_id", upstreamID)
				return &dup, nil
			}
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
	if lastErr == nil {
		lastErr = relay.ErrNoChannel
	}
	s.abortSubmit(t, fmt.Sprintf("submit failed after %d channel(s): %s", len(tried), summarizeSubmitErr(lastErr)))
	return nil, lastErr
}

// maxSubmitAttempts：提交阶段最多尝试的渠道数（与 relay 主链路 1+2 重试预算对齐）。
const maxSubmitAttempts = 3

// summarizeSubmitErr：abortSubmit 的合成摘要（进日志/invocation，红线 7：不带上游原文/URL）。
// 只对 *url.Error 类（会回显带凭证的上游 URL）折叠为语义摘要；DB/keyring 等我方内部错误
// 原样保留（对抗 review：内部错误诊断信息不应被无差别伪标为 upstream rejected）。
func summarizeSubmitErr(err error) string {
	if err == nil {
		return "upstream error"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if strings.Contains(err.Error(), "unreachable") || urlErr.Timeout() {
			return "upstream unreachable"
		}
		return "upstream request failed"
	}
	if strings.Contains(err.Error(), "unreachable") {
		return "upstream unreachable"
	}
	return err.Error() // 我方内部错误（DB/凭证/构建）：保留原文供运维诊断
}

// submitErrorIsClientFault：200-信封 provider 的业务失败是否属**客户端**问题（参数非法/内容审查）——
// 命中才短路故障转移（UpstreamRejectedError）；否则视为**渠道侧**故障（欠费/限流/鉴权/内部错误）走换渠道
// （对抗 review CONFIRMED：此前四家把任何业务失败码都判客户端错误，导致渠道欠费/限流误短路 failover）。
// 双向判定：渠道侧特征词**优先**排除（凭证/额度/限流/内部/容量）→ 走 failover；再看客户端特征
// （invalid/参数/内容）。都不含 → 默认 failover（保守方向：误判渠道侧为客户端会短路故障转移=有害，
// 误判客户端为渠道侧只是多试渠道后仍失败=无害）。关键词跨家统一，比硬编码各家码表鲁棒。
func submitErrorIsClientFault(msg string) bool {
	m := strings.ToLower(msg)
	// 渠道侧特征：命中即**非**客户端错误（走换渠道），优先级高于下方客户端词
	// （"invalid api key" 含 invalid 但属渠道凭证问题）。
	for _, kw := range []string{
		"api key", "api_key", "apikey", "token", "credential", "unauthor", "authenticat",
		"authoriz", "signature", "sign is", "sign check", "sign error", "invalid sign", "验签", "签名",
		"permission denied", "access denied", "balance", "insufficient", "arrears", "欠费", "余额",
		"quota", "throttl", "rate limit", "ratelimit", "too many", "frequency", "限流", "频繁",
		"internal error", "internal server", "server error", "service unavailable", "unavailable",
		"timeout", "timed out", "overload", "try again", "busy", "504", "503", "502", "500",
	} {
		if strings.Contains(m, kw) {
			return false
		}
	}
	// 客户端特征：参数/内容/动作非法。
	for _, kw := range []string{
		"invalid", "sensitive", "violat", "policy", "prohibit", "illegal", "risk", "moderation",
		"unsafe", "blocked", "content", "bad request", "unsupported", "not support", "malformed",
		"not allowed", "missing", "required", "参数", "违规", "敏感", "非法", "不支持",
	} {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false // 无法判定 → 保守走 failover（渠道侧）
}

// nextSubmitCandidate：故障转移候选——排除已试渠道重新选路，解出凭证，并把任务行改钉到
// 新渠道（新渠道并发闸内重新过闸；满则跳过该候选继续找）。返回 ok=false 表示候选耗尽。
func (s *Service) nextSubmitCandidate(model string, attempt int, tried map[uint64]bool, t *Task) (*relay.Channel, Adaptor, string, bool) {
	for {
		ch, err := relay.PickChannelForAttempt(s.DB, model, attempt, tried, nil)
		if err != nil {
			return nil, nil, "", false // 无更多渠道
		}
		if officialArkChannel(ch) {
			tried[ch.ID] = true
			continue
		}
		ad, err := adaptorFor(ch.Provider)
		if err != nil {
			tried[ch.ID] = true
			continue
		}
		credID, credEnc, credOK := relay.SelectCredential(s.DB, ch)
		if !credOK {
			tried[ch.ID] = true
			continue
		}
		credential, err := s.Keyring.Decrypt(credEnc)
		if err != nil {
			tried[ch.ID] = true
			continue
		}
		switch s.repinTask(t, ch, credID) {
		case repinOK:
			return ch, ad, credential, true
		case repinChannelFull:
			tried[ch.ID] = true
			continue
		default: // repinTaskGone：任务已被并发结算（如超时清扫）——停止重试
			return nil, nil, "", false
		}
	}
}

type repinResult int

// Official Ark traffic must use the durable gateway even when an operator
// configured a historical model alias on a legacy channel.
func officialArkChannel(ch *relay.Channel) bool {
	u, err := url.Parse(ch.BaseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasPrefix(host, "ark.") && strings.HasSuffix(host, ".volces.com")
}

const (
	repinOK repinResult = iota
	repinChannelFull
	repinTaskGone
)

// repinTask：换渠道重试前把任务行改钉到新渠道。并发闸在**新渠道**上重新过闸（锁渠道行 +
// 事务内重数，与 Submit 首次过闸同款纪律）；hold/额度预留不动（同模型同价，估算快照沿用）。
// CAS 守卫 status=queued：任务若已被清扫器/并发轮询结算，绝不复活。
func (s *Service) repinTask(t *Task, next *relay.Channel, credID uint64) repinResult {
	res := repinTaskGone
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		lockTx := tx
		if tx.Dialector.Name() == "mysql" {
			lockTx = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		lockCh := &relay.Channel{}
		if err := lockTx.First(lockCh, next.ID).Error; err != nil {
			return err
		}
		limit := lockCh.MaxConcurrentTasks
		if limit <= 0 {
			limit = 3
		}
		var active int64
		if err := tx.Model(&Task{}).
			Where("channel_id = ? AND status IN ?", next.ID, []string{StatusQueued, StatusRunning}).
			Count(&active).Error; err != nil {
			return err
		}
		if int(active) >= limit {
			res = repinChannelFull
			return nil
		}
		upd := tx.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusQueued).
			Updates(map[string]any{"channel_id": next.ID, "provider": next.Provider, "credential_id": credID})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 1 {
			res = repinOK
		}
		return nil
	})
	if err != nil {
		s.Log.Error("task repin failed", "task", t.TaskID, "channel", next.ID, "err", err)
		return repinChannelFull // 数据库错误：跳过该候选（不终止整个故障转移）
	}
	if res == repinOK {
		t.ChannelID, t.Provider, t.CredentialID = next.ID, next.Provider, credID
	}
	return res
}

// abortSubmit：预占后上游提交失败——CAS 把任务转 failed 并释放 hold（与轮询失败同款语义，
// 保证槽位释放 + 资金退还，且不与并发轮询重复结算）。
func (s *Service) abortSubmit(t *Task, reason string) {
	s.settleFailure(t, "submit failed: "+reason, false)
}

// Get：org 隔离读取（红线 5）。
func (s *Service) Get(orgID uint64, taskID string) (*Task, error) {
	t := &Task{}
	// Official video owns its authorization, deletion and evidence semantics.
	// This guard also applies when that runtime is deliberately disabled.
	if err := s.DB.Where("task_id = ? AND org_id = ? AND provider <> ?", taskID, orgID, "ark_official").First(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// PollOnce：轮询一轮——超时清扫 + 未终态任务逐个查上游并转移。
// main 的轮询循环与测试都调用它（可测性：循环与逻辑分离）。
func (s *Service) PollOnce(ctx context.Context) {
	s.sweepTimedOut()

	// 游标轮转（B-4 饥饿修复）：从上次轮询到的 id 之后取一批，到尾回绕（cursor→0）。
	// 此前固定 `Order(id) LIMIT 50` 在总在途 >50 时让尾部较新任务永远轮不到、只能靠 30min
	// 超时误判失败退款（用户拿不到已生成的产物）。游标保证每个在途任务轮流被处理。
	var tasks []Task
	if err := s.DB.Where("status IN ? AND id > ? AND provider <> ?", []string{StatusQueued, StatusRunning}, s.pollCursor, "ark_official").
		Order("id").Limit(pollBatch).Find(&tasks).Error; err != nil {
		s.Log.Error("task poll query failed", "err", err)
		return
	}
	if len(tasks) < pollBatch {
		s.pollCursor = 0 // 本轮不足一批＝已到在途尾部 → 下轮从头，完成回绕
	} else {
		s.pollCursor = tasks[len(tasks)-1].ID
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
		// 产物二跳取件（接口缺口②）：succeeded 但只给了中间引用（hailuo file_id）时，用同一 key
		// 发第二请求换真实产物 URL。取件失败=本轮未就绪，return 下轮重试（绝不误判任务失败——
		// 视频已生成，只是取件抖动；sweepTimedOut 兜底防永久卡住）。
		if result.ResultURL == "" && result.ResultRef != "" {
			switch fetcher := ad.(type) {
			case ContentStreamer:
				// 产物经平台代理下发（sora 二进制/veo 带 key URI）：对外 URL=代理路径，ResultRef 落库
				// 供 HandleContent 用渠道凭证实时取流。绝不把上游引用直接暴露给用户。
				_ = fetcher
				result.ResultURL = "/v1/video/generations/" + t.TaskID + "/content"
			case ResultFetcher:
				url, ferr := fetcher.FetchResultURL(ctx, s.Client, ch, credential, result.ResultRef)
				if ferr != nil || url == "" {
					s.Log.Warn("task result fetch not ready, will retry", "task", t.TaskID, "err", ferr)
					return
				}
				result.ResultURL = url
			}
		}
		// succeeded 但产物 URL 仍空（对抗 review CONFIRMED）：视频/音乐类任务 succeeded 必有可下载产物，
		// 空 URL = 上游"状态先到产物后到"窗口 / 缺 ResultFetcher / 二跳未就绪——本轮重试，绝不为取不回的
		// 产物按次全额结算（B 类会收满 est）。sweepTimedOut 30min 兜底：真·无产物则超时失败退款。
		// 豁免按次计价任务（PerCallUSD>0，MJ 等）：费用固定与产物 URL 无关，且 describe/shorten 类
		// 动作产出本就是文本（imageUrl 恒空）——不豁免会永卡至超时全退，而客户端已经过 /mj fetch
		// 透传拿走产物 = 成功却分文未收（对抗 review CONFIRMED HIGH，资金红线）。
		if result.ResultURL == "" && !t.PricePerCallUSD.IsPositive() {
			s.Log.Warn("task reported succeeded but result URL empty, will retry", "task", t.TaskID)
			return
		}
		s.settleSuccess(t, result)
	case StatusFailed:
		s.settleFailure(t, result.FailReason, true) // FailReason 是上游原文（可能回显 prompt 片段）
	}
}

// settleSuccess：CAS 终态转移 + 资金结算在同一事务（赢者结算，输者无操作）。
func (s *Service) settleSuccess(t *Task, result QueryResult) {
	// 计费快照，不读实时价。0048：快照含 PerCallUSD——按次模型（MJ）此前重建价缺该字段，
	// Cost 走 token 路径算出 0（成功却收 0，资金 bug）。
	price := &relay.ModelPrice{OutputUSDPer1M: t.PriceOutUSDPer1M, PerCallUSD: t.PricePerCallUSD}
	ratio := t.GroupRatio
	if !ratio.IsPositive() {
		ratio = decimal.NewFromInt(1) // 0032 前旧行/异常快照：按 1 兜底（与历史行为一致）
	}
	actual := decimal.Zero
	usageSource := invocation.UsageSourceUpstream
	switch {
	case price.PerCallUSD.IsPositive():
		// 按次固定价：usage 与金额无关（MJ 等不报 token），无需"缺 usage"告警
		actual = price.PerCallUSD.Mul(ratio)
	case result.CompletionTokens > 0:
		actual = price.Cost(0, 0, result.CompletionTokens).Mul(ratio)
	default:
		// 上游没给 usage：按预估收（保守；上游 usage 缺失会在日志可见）
		actual = price.Cost(0, 0, t.EstTokens).Mul(ratio)
		usageSource = invocation.UsageSourceEstimated
		s.Log.Warn("task usage missing from upstream, charged by estimate", "task", t.TaskID)
	}
	// capture 永不超过 hold（预授权语义 + 资金红线①防护，对抗 review CONFIRMED）：
	// A 类任务（kling/vidu）上游用量单位量纲无法先验封顶，若 EstimateOutputTokens 低估、真实
	// usage 超出预冻结，结算超出部分由平台吸收（记 warn 供标定），绝不 overage 把用户余额打成负。
	// 对已标定的 seedance（估算是宽上界）此 cap 恒不触发，纯防御。旧行（hold_amount_usd=0）跳过。
	if t.HoldAmountUSD.IsPositive() && actual.GreaterThan(t.HoldAmountUSD) {
		s.Log.Warn("task actual exceeds hold — capping at hold (platform absorbs excess, estimate needs calibration)",
			"task", t.TaskID, "model", t.Model, "actual", actual.String(), "hold", t.HoldAmountUSD.String())
		actual = t.HoldAmountUSD
	}
	won := false
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		now := s.Now()
		res := tx.Model(&Task{}).
			Where("id = ? AND status IN ?", t.ID, []string{StatusQueued, StatusRunning}).
			Updates(map[string]any{
				"status": StatusSucceeded, "result_url": result.ResultURL,
				"result_ref":    result.ResultRef, // 代理下发厂商的内部引用（0038；其余厂商为空/审计留痕）
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
	t.Status, t.CostUSD = StatusSucceeded, actual
	s.recordInvocation(t, invocation.StatusSuccess, http.StatusOK, result.CompletionTokens, actual, usageSource, "")
	s.emitNotify(t, eventTaskCompleted) // 终态通知（§4.9）：CAS 赢者唯一 emit 点，天然不重
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
	t.Status = StatusFailed
	s.recordInvocation(t, invocation.StatusUpstreamError, http.StatusBadGateway, 0, decimal.Zero, "", brief)
	s.emitNotify(t, eventTaskFailed) // 终态通知（§4.9）：CAS 赢者唯一 emit 点
	s.Log.Warn("task failed", "task", t.TaskID, "reason", brief)
}

// recordInvocation：任务终态落 Invocation Store（视频消费进请求日志/用量口径，P0-1 配套）。
// request_id 复用对外 task_id（uk_inv_request 唯一键 + CAS 赢者唯一，天然不重）。
func (s *Service) recordInvocation(t *Task, status string, code int, tokens int64, cost decimal.Decimal, usageSource, brief string) {
	if s.Rec == nil {
		return
	}
	modality := invocation.ModalityVideo
	switch t.Provider { // 非视频 task 厂商的模态归类（usage/控制台按模态分账依赖）
	case "suno":
		modality = invocation.ModalityMusic
	case "midjourney":
		modality = invocation.ModalityImage
	}
	s.Rec.Record(&invocation.Invocation{
		OrgID: t.OrgID, UserID: t.UserID, APIKeyID: t.APIKeyID,
		RequestID: t.TaskID, Model: t.Model, Modality: modality,
		ChannelID: t.ChannelID, Provider: t.Provider,
		Status: status, StatusCode: code,
		CompletionTokens: tokens, CostUSD: cost, UsageSource: usageSource,
		DurationMs: int(s.Now().Sub(t.SubmittedAt).Milliseconds()),
		ErrorBrief: brief,
	})
}

// taskContentParts：把 OpenAI 风格 content 数组拆成 prompt 文本 + 图片 URL 列表，供不吃 content
// 数组、要平铺 prompt/image 字段的厂商 adaptor（kling/vidu 等）用。ModelArk 系（doubao）原生吃
// content 数组，走 payload["content"]=in.Content 透传，不用本函数。
// content 项形如 {type:"text",text:...} / {type:"image_url",image_url:{url:...}}（大小写变体键容忍）。
func taskContentParts(content []any) (prompt string, images []string) {
	var texts []string
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t := mapStringFold(m, "text"); t != "" {
			texts = append(texts, t)
		}
		// image_url.url（嵌套）或直接 image 字段
		if iu, ok := mapAnyFold(m, "image_url").(map[string]any); ok {
			if u := mapStringFold(iu, "url"); u != "" {
				images = append(images, u)
			}
		} else if u := mapStringFold(m, "image"); u != "" {
			images = append(images, u)
		}
	}
	return strings.Join(texts, "\n"), images
}

func mapAnyFold(m map[string]any, key string) any {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

func mapStringFold(m map[string]any, key string) string {
	if s, ok := mapAnyFold(m, key).(string); ok {
		return s
	}
	return ""
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
	if err := s.DB.Where("status IN ? AND submitted_at < ? AND provider <> ?",
		[]string{StatusQueued, StatusRunning}, cutoff, "ark_official").Limit(100).Find(&tasks).Error; err != nil {
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
