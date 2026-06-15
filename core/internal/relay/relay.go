package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	defaultMaxTokens       = 4096
	maxOutputTokensCeiling = 1 << 20 // 1M：max_tokens 上限（审查 L-2），远超任何真实模型上下文
	maxCompletionChoices   = 128     // n 的 hold 上限（与 OpenAI n 上限一致），防 n 倍 hold 溢出/失真
	upstreamTimeout        = 5 * time.Minute
	maxErrorBodyBytes      = 2048 // 超大上游错误体截断（NewAPI rc.10 教训）
	maxRequestBodySize     = 10 << 20
	maxAttempts            = 3 // 1 次 + 2 次换渠道重试（知识库 §1）
)

type Relay struct {
	DB      *gorm.DB
	Keyring *crypto.Keyring
	Client  *http.Client
	Log     *slog.Logger
	Rec     *invocation.Recorder // nil = 不记录（测试或显式禁用）
	Now     func() time.Time

	// 渠道连续失败计数（内存态，重启清零 = 保守）。达到运营配置的阈值才自动禁用，
	// 避免单次瞬时抖动误禁；成功一次即清零。配置见 ext_settings.channel_auto_disable/threshold。
	failMu     sync.Mutex
	failCounts map[uint64]int

	// 渠道健康分（内存态 EWMA，M2 收尾）：每次 attempt 终态喂 1.0/0.0，α=0.3。
	// 作层内选路权重乘子（见 types.weightedPick），不影响 auto-disable 与计费。
	// 重启乐观置满分（未知=1.0），与 failCounts 同保守哲学。
	healthMu sync.Mutex
	health   map[uint64]float64

	// 渠道级代理客户端缓存（按 proxy URL 复用，避免每请求新建 Transport）。
	proxyMu      sync.Mutex
	proxyClients map[string]*http.Client

	// 模型级限流（gap #2）：每 (org, model) 固定 60s 窗口计数。策略(开关+RPM)从 ext_settings
	// 读，带 5s 缓存避免每请求打库。rate 非 nil（生产接入 Redis）→ 跨实例原子计数；
	// nil（单实例/未配 Redis）→ 退化为下面的进程内窗口。Redis 出错一律 fail-open（绝不因
	// 限流基础设施抖动挡住付费流量，P2-2 用户体验红线）。
	rate         rateCounter
	rateMu       sync.Mutex
	rateWindows  map[string]*rateWindow
	rateLastEvic time.Time
	ratePolMu    sync.Mutex
	ratePolAt    time.Time
	rateEnabled  bool
	rateRPM      int

	// 热读缓存（WS-2）：极少变、读极热的运营配置。命中即把每请求 DB 读从 ~5 降到 0。
	// 失效=本地写后同步 invalidate + 跨实例 Redis pub/sub（invalidate.go）；TTL 兜底。
	// 绝不缓存任何资金可变字段（used_amount/quota/balance 永远走 DB 原子写）。
	channels *snapCache[[]Channel]                // status=1 渠道全表快照（#1 读瓶颈，8s）
	prices   *ttlCache[string, *ModelPrice]       // model → 定价（45s）
	ratios   *ttlCache[ratioKey, decimal.Decimal] // (org,model) → 最终倍率（45s）
	invRedis *redis.Client                        // 跨实例失效 pub/sub（nil = 单实例 TTL 兜底）
	invStop  chan struct{}
}

// ratioKey：倍率缓存键（org × model 的最终倍率，含一层+二层合并）。
type ratioKey struct {
	org   uint64
	model string
}

// clientFor：渠道无代理 → 共享 Client；有代理 → 按 proxy URL 复用一个带代理 Transport 的 Client。
// 代理串无法解析时安全降级为直连（不阻断请求）。
func (r *Relay) clientFor(ch *Channel) *http.Client {
	if ch == nil || ch.Proxy == "" {
		return r.Client
	}
	r.proxyMu.Lock()
	defer r.proxyMu.Unlock()
	if r.proxyClients == nil {
		r.proxyClients = map[string]*http.Client{}
	}
	if cl, ok := r.proxyClients[ch.Proxy]; ok {
		return cl
	}
	pu, err := url.Parse(ch.Proxy)
	if err != nil || pu.Host == "" {
		r.Log.Warn("invalid channel proxy, using direct", "channel", ch.ID, "proxy", ch.Proxy)
		r.proxyClients[ch.Proxy] = r.Client // 缓存降级结果，避免反复解析
		return r.Client
	}
	cl := &http.Client{Timeout: upstreamTimeout, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	r.proxyClients[ch.Proxy] = cl
	return cl
}

func New(db *gorm.DB, kr *crypto.Keyring, rec *invocation.Recorder, log *slog.Logger) *Relay {
	now := time.Now
	return &Relay{
		DB:          db,
		Keyring:     kr,
		Client:      &http.Client{Timeout: upstreamTimeout},
		Log:         log,
		Rec:         rec,
		Now:         now,
		failCounts:  map[uint64]int{},
		health:      map[uint64]float64{},
		rateWindows: map[string]*rateWindow{},
		// 热读缓存 TTL：渠道快照短(8s，禁用/重启用要较快跟随)；价目/倍率长(45s，纯运营低频改)。
		channels: newSnapCache[[]Channel](8*time.Second, now),
		prices:   newTTLCache[string, *ModelPrice](45*time.Second, now),
		ratios:   newTTLCache[ratioKey, decimal.Decimal](45*time.Second, now),
	}
}

// 健康分 EWMA 参数（M2 收尾）：α 越大越灵敏；floor 保证不健康渠道不被硬排除。
const (
	healthEWMAAlpha = 0.3
	healthFloor     = 0.1
)

// observe：把一次 attempt 结果喂进渠道健康 EWMA（成功 1.0 / 失败 0.0）。
// 内存态、并发安全；map 懒初始化（兼容不经 New 构造的测试实例）。
func (r *Relay) observe(channelID uint64, ok bool) {
	sample := 0.0
	if ok {
		sample = 1.0
	}
	r.healthMu.Lock()
	if r.health == nil {
		r.health = map[uint64]float64{}
	}
	h, seen := r.health[channelID]
	if !seen {
		h = 1.0 // 乐观默认：新/未知渠道满分，先给正常流量再凭实绩调整
	}
	r.health[channelID] = healthEWMAAlpha*sample + (1-healthEWMAAlpha)*h
	r.healthMu.Unlock()
}

// healthFactor：渠道健康因子∈[floor,1]，作 weightedPick 层内权重乘子。未知=1.0。
func (r *Relay) healthFactor(channelID uint64) float64 {
	r.healthMu.Lock()
	h, seen := r.health[channelID]
	r.healthMu.Unlock()
	if !seen {
		return 1.0
	}
	if h < healthFloor {
		return healthFloor
	}
	return h
}

// autoDisablePolicy：从 ext_settings 读取自动禁用策略（开关 + 阈值）。
// 失败路径才调用（低频），不缓存以支持热更新。缺省：开启、阈值 1（= 沿用既有"判定性错误即禁用"行为）。
// 运营可调高阈值以容忍瞬时抖动，或关闭开关交人工处理。
func (r *Relay) autoDisablePolicy() (enabled bool, threshold int) {
	enabled, threshold = true, 1
	var rows []struct{ Name, Value string }
	r.DB.Table("ext_settings").Where("name IN ?",
		[]string{"channel_auto_disable", "channel_disable_threshold"}).Find(&rows)
	for _, row := range rows {
		switch row.Name {
		case "channel_auto_disable":
			enabled = row.Value == "true"
		case "channel_disable_threshold":
			if n, err := strconv.Atoi(row.Value); err == nil && n > 0 {
				threshold = n
			}
		}
	}
	return
}

// noteFailure：渠道一次"应禁用"级失败 → 累加计数；达阈值才真正禁用（受总开关约束）。
func (r *Relay) noteFailure(channelID uint64, reason string) {
	enabled, threshold := r.autoDisablePolicy()
	if !enabled {
		return // 运营关闭了自动禁用：交人工处理
	}
	r.failMu.Lock()
	r.failCounts[channelID]++
	n := r.failCounts[channelID]
	r.failMu.Unlock()
	// 不在此处清零：disableChannel 是异步且可能失败的 DB 写。计数保留，
	// 若禁用没落库，下次失败会再次触发禁用；真正清零只在成功请求时（resetFailures）。
	if n >= threshold {
		r.disableChannel(channelID, fmt.Sprintf("%s (%d consecutive failures)", reason, n))
	}
}

// resetFailures：渠道一次成功 → 清零连续失败计数。
func (r *Relay) resetFailures(channelID uint64) {
	r.failMu.Lock()
	delete(r.failCounts, channelID)
	r.failMu.Unlock()
}

// keyGate：API Key 精细管控校验（0012）。返回 (HTTP 码, 文案, 是否放行)。
// IP 白名单 / 累计额度 / 模型白名单，任一不过即拒（被拒请求不计费不入上游）。
func keyGate(key *identity.APIKey, clientIP, model string) (int, string, bool) {
	if !key.AllowsIP(clientIP) {
		return http.StatusForbidden, "this api key is not allowed from your ip address", false
	}
	if key.QuotaExhausted() {
		return http.StatusPaymentRequired, "this api key has reached its quota limit", false
	}
	if !key.AllowsModel(model) {
		return http.StatusForbidden, "this api key is not allowed to use model " + model, false
	}
	return 0, "", true
}

// requestedMaxOutput：取请求声明的输出 token 上限，用于 hold 估算与上游 max_tokens 映射。
// 同时识别 max_tokens（经典 / Anthropic 必填）与 max_completion_tokens（OpenAI 新推理模型字段）；
// 两者都给取较大者，确保 hold 不低于上游可能产出——堵住「只传 max_completion_tokens 绕过输出冻结
// 上限、借 overage 把余额刷成负数」的 P0-2 漏洞。都没声明返回 0（调用方回退默认）。
func requestedMaxOutput(req map[string]any) int64 {
	var m int64
	for _, k := range [...]string{"max_tokens", "max_completion_tokens"} {
		if v, ok := req[k].(float64); ok && v > 0 && int64(v) > m {
			m = int64(v)
		}
	}
	return m
}

// newRequestID：平台级请求 id（16 hex），回传 X-Request-Id 给客户端作支持工单锚点。
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// firstByteReader 记录首字节到达时刻（frt，NewAPI logs 表的黄金指标），adapter 无感知。
type firstByteReader struct {
	inner   io.Reader
	once    sync.Once
	onFirst func()
}

func (f *firstByteReader) Read(p []byte) (int, error) {
	n, err := f.inner.Read(p)
	if n > 0 {
		f.once.Do(f.onFirst)
	}
	return n, err
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	// 缓存命中输入（OpenAI 兼容：prompt_tokens 的子集，按缓存价折扣计费）
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Cached 返回命中提示缓存的输入 token 数（钳到 PromptTokens 内）。
func (u usage) Cached() int64 {
	c := u.PromptTokensDetails.CachedTokens
	if c > u.PromptTokens {
		return u.PromptTokens
	}
	return c
}

// HandleChatCompletions：POST /v1/chat/completions
// 计费时序 = hold（按估算上限冻结）→ 转发 → capture（按真实 usage 结算，差额自动解冻）。
// 上游失败 → release 全额解冻。结构性消灭"预检黑盒"类问题（TrustQuota 事故）。
func (r *Relay) HandleChatCompletions(c *gin.Context) {
	t0 := r.Now()
	// 纯读预检绑请求 context（审查 M-6）：客户端断连/超时即取消这些查询、卸载 DB 负载。
	// 结算事务仍走裸 r.DB（必须完成，断连不可取消——否则资金冻死）。
	rdb := r.DB.WithContext(c.Request.Context())

	// 1. API Key 认证
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(rdb, strings.TrimPrefix(auth, "Bearer "), r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}

	// 调用记录（Invocation Store 元数据，设计 §6.1）；finish 在每个终态出口调用一次
	inv := &invocation.Invocation{
		OrgID:     key.OrgID,
		UserID:    key.UserID,
		APIKeyID:  key.ID,
		RequestID: newRequestID(),
	}
	c.Header("X-Request-Id", inv.RequestID)
	finish := func(status string, code int, brief string) {
		if r.Rec == nil {
			return
		}
		inv.Status, inv.StatusCode, inv.ErrorBrief = status, code, brief
		inv.DurationMs = int(r.Now().Sub(t0).Milliseconds())
		r.Rec.Record(inv)
	}

	// 2. 读请求体，取 model / stream / max_tokens
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		relayErr(c, http.StatusBadRequest, "invalid json")
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		relayErr(c, http.StatusBadRequest, "model is required")
		return
	}
	if code, msg, ok := keyGate(key, c.ClientIP(), model); !ok { // Key 精细管控（0012）
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, model); !ok { // 模型级限流（gap #2）
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	stream, _ := req["stream"].(bool)
	maxTokens := int64(defaultMaxTokens)
	if v := requestedMaxOutput(req); v > 0 { // 同时识别 max_tokens 与 max_completion_tokens（P0-2）
		maxTokens = v
		if maxTokens > maxOutputTokensCeiling {
			maxTokens = maxOutputTokensCeiling // 钳上限（审查 L-2）：防超大值把 hold 撑成误导性"余额不足"
		}
	}
	// 扩展思考（Anthropic thinking）：thinking token 计入 output 计费，anthropic 适配器会把上游
	// max_tokens 上调到 budget+4096。hold 必须同口径计入，否则低余额可借 overage 把 org 余额刷负
	// （与下方 n 倍同类资金红线）。对非 anthropic 渠道无害（只多冻结、settle 按真实 usage 退差）。
	if th, ok := req["thinking"].(map[string]any); ok {
		if bt, ok := th["budget_tokens"].(float64); ok && bt > 0 {
			if want := int64(bt) + int64(defaultMaxTokens); want > maxTokens {
				maxTokens = want
				if maxTokens > maxOutputTokensCeiling {
					maxTokens = maxOutputTokensCeiling
				}
			}
		}
	}
	// 多候选 n（OpenAI 上限 128）：输出 token 随 n 倍增、usage 计入全部候选，故 hold 也须按 n 倍冻结，
	// 否则 n 大 + max_completion_tokens 大可绕过余额闸、借 overage 把余额刷成负（P0-2 同类）。
	holdOutput := maxTokens
	if nv, ok := req["n"].(float64); ok && nv > 1 {
		n := int64(nv)
		if n > maxCompletionChoices {
			n = maxCompletionChoices
			req["n"] = float64(n) // 同步夹紧**转发体**：否则上游按原始 n>128 产出全部候选，
			// capture 按真实 ×n 结算 > ×128 hold，超出走 AllowNegative overage 把 org 余额刷负（资金红线）。
			// 与 images.go 对转发张数 N 的夹紧同款防护。
		}
		holdOutput = maxTokens * n
	}
	inv.Model, inv.IsStream = model, stream

	// 3. 定价（热读缓存：命中即免 DB 查；hold/capture 仍取同一个 *price 局部变量）
	price, err := r.cachedPrice(model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", model))
		return
	}

	// 4. Hold：按 tiktoken 估算输入 + max_tokens 上限 冻结（整个重试周期只 hold 一次）
	estIn := CountChatTokens(req, len(body))
	// 计费倍率（0011 一层 + 0027 二层 (分组,模型)）：hold/capture 取同一次值，避免中途改配置导致 release 算错
	groupRatio := r.cachedRatio(key.OrgID, model)
	holdAmount := price.ChatCost(estIn, 0, holdOutput).Mul(groupRatio) // holdOutput 含 n 倍；按次固定价时忽略 token（gap P2-F）
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, key.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		h, _, err := ledger.CreateHold(tx, ledger.HoldInput{
			IdempotencyKey: "relay:" + inv.RequestID, // 每请求唯一 16-hex（非时间戳，杜绝纳秒碰撞共用 hold）
			AccountID:      acc.ID,
			Amount:         holdAmount,
			RefType:        "usage",
			RefID:          model,
		})
		hold = h
		return err
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "insufficient balance")
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return
		}
		r.Log.Error("hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return
	}

	// 4b. API Key 额度原子预留（P0-3：消解并发超 quota）。按 hold 估算预留；capture 对账到 actual，
	// 失败由下方 defer 退还（统一覆盖所有 release 路径）。预留失败＝会超 quota → 释放 hold + 402。
	if !identity.ReserveKeyQuota(r.DB, key.ID, holdAmount) {
		r.release(hold.ID)
		finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "api key quota exceeded")
		relayErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
		return
	}
	quotaSettled := false
	defer func() {
		if !quotaSettled { // 未走到 capture（任何失败/释放路径）→ 退还预留
			_ = identity.AddKeyUsageSigned(r.DB, key.ID, holdAmount.Neg())
		}
	}()

	// 5. 换渠道重试循环（知识库 §1/§2：分类驱动；hold 跨重试只此一份）。
	// 请求体已在内存（req map），每次 attempt 由 adapter 重建上游请求——天然可重放，
	// 不需要 NewAPI 的磁盘 BufStorage。
	var resp *http.Response
	var ch *Channel
	var attemptStart time.Time
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	// 渠道快照取一次（8s 缓存），3 次重试复用同一份；exclude 走内存（WS-2 #1 读瓶颈消除）。
	channelSnap, snapErr := r.cachedChannels()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if snapErr != nil {
			break // 快照加载失败：当作无渠道，落循环后失败处理
		}
		var pickErr error
		ch, pickErr = pickFromSnapshot(channelSnap, model, attempt, exclude, r.healthFactor)
		if pickErr != nil {
			break // 无渠道可用：落到循环后的失败处理
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		attemptStart = r.Now()
		credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
		if err != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue // 我方配置错误（知识库 channel: 前缀类）→ 换渠道
		}
		ad := adapterFor(ch.Provider)
		// 模型映射（0009）：每次 attempt 按当前渠道覆盖写，无跨渠道残留；
		// 计费/记录/对外响应仍用对外名（inv.Model / price 已定）。
		req["model"] = ch.UpstreamModel(model)
		upReq, err := ad.buildRequest(c.Request.Context(), ch, credential, req, stream)
		if err != nil {
			r.release(hold.ID)
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return // 请求本身不合法，换渠道无意义
		}
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			r.Log.Warn("upstream unreachable", "channel", ch.ID, "attempt", attempt, "err", err)
			r.observe(ch.ID, false) // 健康分：网络不可达 = 渠道可靠性失败
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusBadGateway, []byte(`{"error":{"message":"upstream unreachable"}}`)
			resp = nil
			continue // 网络不可达 → 换渠道
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			break // 成功
		}
		// 非 2xx：分类 → 禁用/重试决策
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		disp := classifyUpstreamError(ch.Provider, resp.StatusCode, errBody)
		if disp.Retryable || disp.DisableChannel {
			r.observe(ch.ID, false) // 健康分：渠道自身可靠性失败（客户端 400 类不计）
		}
		// 不记原始上游错误体（审查 M-5）：可能回显请求片段/org 标识/prompt 碎片，
		// 违反"操作日志禁 payload/PII"红线。只记分类后的 reason。
		r.Log.Warn("upstream error", "channel", ch.ID, "attempt", attempt, "status", resp.StatusCode,
			"disable", disp.DisableChannel, "retryable", disp.Retryable, "reason", disp.Reason)
		if disp.DisableChannel {
			// 连续失败累加，达运营阈值才异步禁用（不阻塞响应；不能用请求 context）
			r.noteFailure(ch.ID, disp.Reason)
		}
		lastStatus, lastBody = resp.StatusCode, errBody
		resp = nil
		if !disp.Retryable {
			break // 400/408/504/524 等：重试无意义，直接透传
		}
		exclude[ch.ID] = true
	}
	if resp == nil {
		// 所有尝试失败：release 全额解冻，透传最后一个上游错误（或无渠道）
		r.release(hold.ID)
		if lastStatus == 0 {
			finish(invocation.StatusNoChannel, http.StatusServiceUnavailable, "no upstream available")
			relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
			return
		}
		// error_brief 只存状态摘要，不存上游错误体（红线 8：禁 payload 回流元数据）
		finish(invocation.StatusUpstreamError, lastStatus, fmt.Sprintf("upstream status %d after %d attempts", lastStatus, inv.Attempts))
		c.Data(lastStatus, "application/json", lastBody)
		return
	}
	defer resp.Body.Close()
	ad := adapterFor(ch.Provider)

	// 上游响应校验（部署冒烟暴露）：200 但 content-type 非 JSON/SSE（如中转站返回 HTML 首页）
	// 判为上游异常——不透传垃圾、不误计费成 success。渠道级开关可放宽。
	if ch.EnforcesStrictResponse() && !IsLLMContentType(resp.Header.Get("Content-Type")) {
		r.release(hold.ID)
		r.Log.Warn("upstream returned non-LLM content-type, rejecting",
			"channel", ch.ID, "content_type", resp.Header.Get("Content-Type"))
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream returned invalid response type")
		relayErr(c, http.StatusBadGateway, "upstream returned an invalid response (check channel base_url)")
		return
	}

	// 上游 request-id（找厂商开工单的唯一凭证，rc.6）；frt = 最后一次尝试开始 → 首字节
	if rid := resp.Header.Get("x-request-id"); rid != "" {
		inv.UpstreamRequestID = rid
	} else if rid := resp.Header.Get("request-id"); rid != "" {
		inv.UpstreamRequestID = rid
	}
	respBody := &firstByteReader{inner: resp.Body, onFirst: func() {
		inv.FRTMs = int(r.Now().Sub(attemptStart).Milliseconds())
	}}

	// 成功响应：若渠道曾被 auto-disabled，自动重启用（知识库 §2.2）
	r.enableChannelIfDisabled(ch)

	// 7. 响应回传（adapter 转换）+ usage 抽取 + capture
	var u usage
	var outBytes int
	var streamErr error
	if stream {
		u, outBytes, streamErr = ad.pipeSSE(c, respBody)
	} else {
		u, outBytes, streamErr = ad.pipeJSON(c, respBody)
	}
	inv.UsageSource = invocation.UsageSourceUpstream
	// 第三档兜底（**按字段独立判定**）：上游缺某项 usage 就按估算补（宁可少收不多收）。
	// 必须分字段而非 &&：Anthropic 流式 message_start 早置 prompt、completion 仅末帧给，
	// 客户端中途断连会留下 prompt>0 & completion=0；若合并判定会用 completion=0 结算，
	// 把已下发的输出近乎免费计费（BUG-H2 收入泄漏）。
	if u.PromptTokens == 0 {
		u.PromptTokens = estIn
		inv.UsageSource = invocation.UsageSourceEstimated
	}
	if u.CompletionTokens == 0 {
		u.CompletionTokens = EstimateTokens(outBytes)
		inv.UsageSource = invocation.UsageSourceEstimated
	}
	if inv.UsageSource == invocation.UsageSourceEstimated {
		r.Log.Warn("usage missing/partial from upstream, estimated", "channel", ch.ID, "model", model)
	}

	actual := price.ChatCost(u.PromptTokens, u.Cached(), u.CompletionTokens).Mul(groupRatio)
	reason := fmt.Sprintf("model=%s in=%d cached=%d out=%d ×%s", model, u.PromptTokens, u.Cached(), u.CompletionTokens, groupRatio)
	var overage = decimal.Zero
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		overage, err = ledger.SettleHoldExact(tx, hold.ID, actual, "usage", model, reason)
		return err
	}); err != nil {
		// 响应已发出，结算失败只能告警人工对账（journal 漂移自检会兜住）
		r.Log.Error("SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
	if overage.IsPositive() {
		r.Log.Warn("usage overage beyond hold", "hold", hold.Amount, "actual", actual, "overage", overage)
	}

	inv.PromptTokens, inv.CompletionTokens, inv.CostUSD = u.PromptTokens, u.CompletionTokens, actual
	// 预留→实际对账（P0-3）：used_amount 已 +holdAmount，这里 +(actual-hold) 使其落到真实 actual；
	// 标记 settled 让上面的退还 defer 不再触发。已交付的部分 token 照实计费（actual），不退。
	_ = identity.AddKeyUsageSigned(r.DB, key.ID, actual.Sub(holdAmount))
	quotaSettled = true
	// P1-5：流中途 stall/读错误 → 已发 200 头的截断流，照实计费但**不可记为 success**。
	if streamErr != nil {
		r.Log.Warn("upstream stream ended abnormally — recording as upstream_error",
			"channel", ch.ID, "model", model, "err", streamErr)
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream stream ended abnormally")
		return
	}
	finish(invocation.StatusSuccess, http.StatusOK, "")
}

func (r *Relay) release(holdID uint64) {
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		return ledger.Release(tx, holdID)
	}); err != nil {
		r.Log.Error("RELEASE FAILED — funds stuck in hold", "hold", holdID, "err", err)
	}
}

// disableChannel 异步禁用渠道（知识库 §2.2）。一次成功请求会自动重启用（见 enableChannelIfDisabled）。
// 用独立 goroutine + 新 context，不依赖已可能结束的请求 context。
func (r *Relay) disableChannel(channelID uint64, reason string) {
	go func() {
		if err := r.DB.Model(&Channel{}).Where("id = ? AND status = 1", channelID).
			Update("status", 2).Error; err != nil { // 2 = auto-disabled
			r.Log.Error("channel auto-disable failed", "channel", channelID, "err", err)
			return
		}
		r.broadcastInvalidate(KindChannel, "") // 渠道快照即时失效（本地 + 跨实例），坏渠道不再被选
		r.Log.Warn("channel auto-disabled", "channel", channelID, "reason", reason)
	}()
}

// enableChannelIfDisabled：成功请求后清零失败计数；若渠道处于 auto-disabled 则自动重启用（知识库 §2.2）。
func (r *Relay) enableChannelIfDisabled(ch *Channel) {
	r.resetFailures(ch.ID)
	r.observe(ch.ID, true) // 健康分：一次成功（三类 relay 共用此成功钩子）
	if ch.Status != 2 {
		return
	}
	go func() {
		if err := r.DB.Model(&Channel{}).Where("id = ? AND status = 2", ch.ID).
			Update("status", 1).Error; err == nil {
			r.broadcastInvalidate(KindChannel, "") // 重启用的渠道即时回到快照可被选
			r.Log.Info("channel auto-re-enabled after success", "channel", ch.ID)
		}
	}()
}

// ---- 热读缓存包裹（WS-2）----
// 一律用 r.DB（裸库）加载，不用请求级 rdb：缓存值跨请求共享，绑某个请求 context 会让该请求
// 断连时取消别人正复用的加载查询。代价：填充那一次查询不随客户端断连取消（极快小表，净收益正）。

// cachedChannels 返回 status=1 活跃渠道快照（8s TTL）。返回只读 slice，调用方绝不可原地改元素。
func (r *Relay) cachedChannels() ([]Channel, error) {
	return r.channels.getOrLoad(func() ([]Channel, error) {
		var chs []Channel
		if err := r.DB.Where("status = 1").Order("priority DESC, id").Find(&chs).Error; err != nil {
			return nil, err
		}
		return chs, nil
	})
}

// cachedPrice 返回模型定价（45s TTL）。GetPrice 是纯函数，原样复用。
func (r *Relay) cachedPrice(model string) (*ModelPrice, error) {
	return r.prices.getOrLoad(model, func() (*ModelPrice, error) {
		return GetPrice(r.DB, model)
	})
}

// cachedRatio 返回 (org,model) 最终倍率（45s TTL）。整体作一个值缓存，保证 hold/capture 取同值
// （不拆一层/二层，不破坏合并语义）。任何异常回退 1（绝不返回 0 倍率致免费/错算钱）。
func (r *Relay) cachedRatio(org uint64, model string) decimal.Decimal {
	v, err := r.ratios.getOrLoad(ratioKey{org, model}, func() (decimal.Decimal, error) {
		return EffectiveRatio(r.DB, org, model), nil // EffectiveRatio 内部 fail-to-1，永不 err
	})
	if err != nil || !v.IsPositive() {
		return decimal.NewFromInt(1)
	}
	return v
}

func relayErr(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_relay_error"}})
}
