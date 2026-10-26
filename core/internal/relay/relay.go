package relay

import (
	"bytes"
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

	// geminiImplicitThinkingHoldTokens：gemini 系模型未显式给 thinkingBudget 时 hold 的隐式思考
	// 预算上浮（2.5 系默认开思考、dynamic budget 上限 24576，thoughts 不受 maxOutputTokens 约束）。
	// 只影响冻结额度不影响实收（settle 按真实 usage 退差）。
	geminiImplicitThinkingHoldTokens = 24576

	// MaxRequestBodySize：数据面统一请求体上限的导出别名。relay 各端点自身用 io.LimitReader，
	// task/video 等非本包数据面路由挂 MaxBytesReader 时复用同一上限（指令 P0-2）。
	MaxRequestBodySize = maxRequestBodySize
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

	// key 级失败计数（0034 Multi-key，内存态）：与 channel 失败计数同哲学，但键为 credential id。
	// 达阈值禁该 key（禁后无 active key 才禁 channel），该 key 成功即清零。
	keyFailMu     sync.Mutex
	keyFailCounts map[uint64]int

	// 渠道健康分（内存态 EWMA，M2 收尾）：每次 attempt 终态喂 1.0/0.0，α=0.3。
	// 作层内选路权重乘子（见 types.weightedPick），不影响 auto-disable 与计费。
	// 重启乐观置满分（未知=1.0），与 failCounts 同保守哲学。
	healthMu sync.Mutex
	health   map[uint64]float64

	// 渠道熔断器（内存态，breaker.go）：连续 5xx/不可达 → 选路硬摘除 + 半开探活自愈。
	// 重启清零（乐观），与健康分同哲学；语义见 breaker.go 文件头。
	breakerMu     sync.Mutex
	breakerStates map[uint64]*breakerState

	// 渠道禁用建议（B-2，内存 set 追踪"当前有建议"的渠道，避免健康渠道每次成功都多余 UPDATE）。
	// 硬故障→记建议（落库+入 set），成功→清建议（出 set+清库）。不自动禁用，运营手动决定。
	suggestMu sync.Mutex
	suggested map[uint64]bool

	// 渠道级代理客户端缓存（按 proxy URL 复用，避免每请求新建 Transport）。
	proxyMu      sync.Mutex
	proxyClients map[string]*http.Client
	// advanced_custom 的 SSRF 守卫客户端（0036，按 allow-private 策略缓存两份；proxyMu 共用保护）。
	ssrfClients map[bool]*http.Client

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

	// (model×channel) 统计底座（设计 §4.4 策略化选路 A3）：60s TTL 惰性刷新的近 1h 聚合，
	// 服务 latency/throughput 路由策略。进程内视角（多副本各自聚合，与健康分同哲学）。
	chanStatsMu  sync.Mutex
	chanStatsAt  time.Time
	chanStatsMap map[string]map[uint64]*ChanStat

	// BYOK 费率缓存（0044，byok.go）
	byokMu     sync.Mutex
	byokAt     time.Time
	byokRatio  decimal.Decimal
	byokLoaded bool
	rateRPM    int

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
	// BYOK（0044）：base_url 用户可控 = SSRF 面——恒走拨号守卫且**绝不放行私网**
	// （不受 advanced_custom_allow_private 开关影响，严于 advanced_custom）。
	if ch != nil && ch.OwnerOrgID != 0 {
		return r.guardedClient(ByokAllowPrivateForTest) // 生产恒 false（变量仅测试可设）
	}
	// advanced_custom（0036）：SSRF 拨号守卫——对解析后真实 IP 复检（防 DNS rebinding）。
	// 独立于 proxy 缓存（其目标是上游而非代理；advanced_custom 一般不配 proxy）。
	if ch != nil && ch.isAdvancedCustom() {
		return r.ssrfClientFor(ch)
	}
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
	cl := NewUpstreamClient(upstreamTimeout)
	cl.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
	r.proxyClients[ch.Proxy] = cl
	return cl
}

// ssrfClientFor：advanced_custom 渠道的 SSRF 守卫客户端。
func (r *Relay) ssrfClientFor(ch *Channel) *http.Client {
	return r.guardedClient(r.advancedAllowPrivate())
}

// guardedClient：带 SSRF 拨号守卫的客户端（按 allow-private 策略缓存两份）。probe 路径复用。
func (r *Relay) guardedClient(allowPrivate bool) *http.Client {
	r.proxyMu.Lock()
	defer r.proxyMu.Unlock()
	if r.ssrfClients == nil {
		r.ssrfClients = map[bool]*http.Client{}
	}
	if cl, ok := r.ssrfClients[allowPrivate]; ok {
		return cl
	}
	cl := NewGuardedClient(upstreamTimeout, allowPrivate)
	r.ssrfClients[allowPrivate] = cl
	return cl
}

// Use a value copy to retain the guarded connection pool without weakening the
// cached API client's policy. Media redirects never use a cookie jar.
func (r *Relay) guardedMediaClient(allowPrivate bool) *http.Client {
	client := *r.guardedClient(allowPrivate)
	client.CheckRedirect = mediaCheckRedirect
	client.Jar = nil
	return &client
}

// AdvancedAllowPrivate：导出 allow-private 策略（admin 层 probe 校验用）。
func (r *Relay) AdvancedAllowPrivate() bool { return r.advancedAllowPrivate() }

// advancedAllowPrivate：ext_settings 读 advanced_custom_allow_private（默认 false）。
func (r *Relay) advancedAllowPrivate() bool {
	var row struct{ Value string }
	r.DB.Table("ext_settings").Where("name = ?", "advanced_custom_allow_private").Take(&row)
	return row.Value == "true"
}

func New(db *gorm.DB, kr *crypto.Keyring, rec *invocation.Recorder, log *slog.Logger) *Relay {
	now := time.Now
	r := &Relay{
		DB:            db,
		Keyring:       kr,
		Client:        NewUpstreamClient(upstreamTimeout),
		Log:           log,
		Rec:           rec,
		Now:           now,
		failCounts:    map[uint64]int{},
		keyFailCounts: map[uint64]int{},
		health:        map[uint64]float64{},
		breakerStates: map[uint64]*breakerState{},
		suggested:     map[uint64]bool{},
		rateWindows:   map[string]*rateWindow{},
		// 热读缓存 TTL：渠道快照短(8s，禁用/重启用要较快跟随)；价目/倍率长(45s，纯运营低频改)。
		channels: newSnapCache[[]Channel](8*time.Second, now),
		prices:   newTTLCache[string, *ModelPrice](45*time.Second, now),
		ratios:   newTTLCache[ratioKey, decimal.Decimal](45*time.Second, now),
	}
	r.rehydrateSuggested() // 启动回填禁用建议内存 set（对抗 review：重启后成功请求才能清陈旧建议）
	return r
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
// 失败路径才调用（低频），不缓存以支持热更新。
// 缺省：**关闭**（手动管理渠道开关，用户要求）——新渠道首调抖动一次不再被误自动下线；
// 运营如需"坏渠道自动剔除"可把 channel_auto_disable 置 true（并调 channel_disable_threshold 容忍瞬抖）。
func (r *Relay) autoDisablePolicy() (enabled bool, threshold int) {
	enabled, threshold = false, 1
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

// noteSuggestDisable：渠道硬故障 → 记录**禁用建议**（B-2，不自动禁用）。落库覆盖式（最新原因+
// 时间），admin 渠道列表显示，运营手动决定。内存 set 去重（同一渠道反复硬故障只在首次落库，
// 避免每次失败都打 DB）。reason 是分类器合成串（无 provider/自由文本，红线 7 安全）。
func (r *Relay) noteSuggestDisable(channelID uint64, reason string) {
	r.suggestMu.Lock()
	already := r.suggested[channelID]
	r.suggestMu.Unlock()
	if already {
		return // 已记过建议（内存标记＝已落库），不重复打 DB
	}
	go func() {
		// 落库**成功后**才置内存标记（对抗 review：mark-before-write 下首次 UPDATE 失败会永久压制
		// 重试，坏渠道永不产生建议）。失败不置标记 → 下次硬故障重试落库。
		if err := r.DB.Model(&Channel{}).Where("id = ?", channelID).
			Updates(map[string]any{"suggest_disable_reason": clipMeta(reason, 255), "suggest_disabled_at": r.Now()}).Error; err != nil {
			r.Log.Warn("note suggest-disable failed", "channel", channelID)
			return
		}
		r.suggestMu.Lock()
		r.suggested[channelID] = true
		r.suggestMu.Unlock()
	}()
}

// rehydrateSuggested：启动时从 DB 回填有禁用建议的渠道到内存 set（对抗 review：进程重启后
// 内存 set 清空，已落库建议的渠道恢复后 clearSuggestDisable 因 had=false 空转、建议永不清除）。
// 回填后成功请求能正确清除陈旧建议。异步、best-effort，失败不致命。
func (r *Relay) rehydrateSuggested() {
	go func() {
		var ids []uint64
		if err := r.DB.Model(&Channel{}).Where("suggest_disable_reason <> ''").Pluck("id", &ids).Error; err != nil {
			r.Log.Warn("rehydrate suggested set failed", "err", err)
			return
		}
		r.suggestMu.Lock()
		for _, id := range ids {
			r.suggested[id] = true
		}
		r.suggestMu.Unlock()
	}()
}

// clearSuggestDisable：渠道一次成功 → 撤销禁用建议（渠道又好了）。仅对内存 set 里有建议的渠道
// 打 DB（健康渠道零多余 UPDATE）。
func (r *Relay) clearSuggestDisable(channelID uint64) {
	r.suggestMu.Lock()
	had := r.suggested[channelID]
	delete(r.suggested, channelID)
	r.suggestMu.Unlock()
	if !had {
		return
	}
	go func() {
		if err := r.DB.Model(&Channel{}).Where("id = ? AND suggest_disable_reason <> ''", channelID).
			Updates(map[string]any{"suggest_disable_reason": "", "suggest_disabled_at": nil}).Error; err != nil {
			r.Log.Warn("clear suggest-disable failed", "channel", channelID)
		}
	}()
}

// keyGate：API Key 精细管控校验（0012）。返回 (HTTP 码, 文案, 是否放行)。
// IP 白名单 / 累计额度 / 模型白名单，任一不过即拒（被拒请求不计费不入上游）。
// 数据面入口请调 r.gateKey（含 key 级 RPM）；本函数保留为纯校验单元。
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

// gateKey：数据面 key 级准入唯一入口 = keyGate 三项 + key 级 RPM（0041）。
// 全部数据面模态（chat/embeddings/images/edits/rerank/audio/realtime）与 task（经 GateKey）
// 共用本入口——未来新增 key 级门（如 key 级并发）也加在这里，不散落 handler。
// 429 的 Retry-After 头由 relayErr 统一设置。
func (r *Relay) gateKey(key *identity.APIKey, clientIP, model string) (int, string, bool) {
	if code, msg, ok := keyGate(key, clientIP, model); !ok {
		return code, msg, false
	}
	if !r.allowKeyRate(key.ID, key.RPMLimit) {
		return http.StatusTooManyRequests, "this api key has exceeded its rpm limit, slow down", false
	}
	return 0, "", true
}

// GateKey / AllowModelRate：准入原语的导出封装（指令 P0-1 统一 admission）。
// task/video 等非本包数据面入口复用同一实例——同一 gateKey 语义、同一 (org,model)
// 限流窗口（Redis 或进程内），不另建第二套状态。
func (r *Relay) GateKey(key *identity.APIKey, clientIP, model string) (code int, msg string, ok bool) {
	return r.gateKey(key, clientIP, model)
}

func (r *Relay) AllowModelRate(orgID uint64, model string) (ok bool, rpm int) {
	return r.allowModelRate(orgID, model)
}

// outputCapAliases：所有能声明"输出 token 上限"的字段（标准 + 方言别名）。
// hold 估算、转发体 ceiling 钳制、param_override 受保护键三处共用同一清单——
// openai 系 adapter 整 map 透传，TGI（max_new_tokens）/ Ollama（num_predict）/
// Anthropic legacy（max_tokens_to_sample）/ Responses（max_output_tokens）方言上游
// 会认这些别名，漏掉任何一个都等于绕过输出冻结上限（P0-2 同类负余额漏洞，对抗 review）。
var outputCapAliases = [...]string{
	"max_tokens", "max_completion_tokens",
	"max_output_tokens", "max_new_tokens", "num_predict", "max_tokens_to_sample",
}

// requestedMaxOutput：取请求声明的输出 token 上限（全部别名取较大者），用于 hold 估算。
// 确保 hold 不低于上游可能产出——堵住「只传别名字段绕过输出冻结上限、借 overage 把余额
// 刷成负数」的 P0-2 漏洞。都没声明返回 0（调用方回退默认）。
func requestedMaxOutput(req map[string]any) int64 {
	var m int64
	for _, k := range outputCapAliases {
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
	// 缓存命中输入（OpenAI 兼容：prompt_tokens 的子集，按缓存价折扣计费）+ 音频输入
	// （prompt_tokens 的子集，gpt-4o-audio 等；≈16× 文本价，按 audio 价补收差额，见 AudioSurcharge）
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
		AudioTokens  int64 `json:"audio_tokens"`
	} `json:"prompt_tokens_details"`
	// 推理/思考 token（completion_tokens 的子集，OpenAI o 系列口径）。仅观测用：
	// 各家的思考 token 都已折入 CompletionTokens 按 output 价计费，这里不再参与计价。
	// 音频输出（completion_tokens 的子集）：同输入，按 audio 输出价补收差额。
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
		AudioTokens     int64 `json:"audio_tokens"`
	} `json:"completion_tokens_details"`
	// Anthropic 写缓存 token（prompt_tokens 的子集，上游实收 1.25x/2x 输入价）。
	// 内部计费字段，由 anthropic adapter 填充，不来自 OpenAI JSON（见 CacheWriteSurcharge）。
	CacheWrite5m int64 `json:"-"`
	CacheWrite1h int64 `json:"-"`
	// 真实 finish_reason（排障闭环 0045，观测用）：各 adapter pipe 显式赋值，不来自 usage JSON。
	FinishReason string `json:"-"`
}

// Cached 返回命中提示缓存的输入 token 数（钳到 PromptTokens 内）。
func (u usage) Cached() int64 {
	c := u.PromptTokensDetails.CachedTokens
	if c > u.PromptTokens {
		return u.PromptTokens
	}
	return c
}

// AudioIn/AudioOut 返回音频输入/输出 token（各自钳到 prompt/completion 内，防上游异常数据算飞）。
func (u usage) AudioIn() int64 {
	a := u.PromptTokensDetails.AudioTokens
	if a < 0 {
		return 0
	}
	if a > u.PromptTokens {
		return u.PromptTokens
	}
	return a
}

func (u usage) AudioOut() int64 {
	a := u.CompletionTokensDetails.AudioTokens
	if a < 0 {
		return 0
	}
	if a > u.CompletionTokens {
		return u.CompletionTokens
	}
	return a
}

// CacheWrites 返回写缓存 token（5m, 1h），总量钳到 PromptTokens 内（防上游异常数据把溢价算飞）。
func (u usage) CacheWrites() (int64, int64) {
	w5m, w1h := u.CacheWrite5m, u.CacheWrite1h
	if w5m < 0 {
		w5m = 0
	}
	if w1h < 0 {
		w1h = 0
	}
	if w5m+w1h > u.PromptTokens {
		if w5m > u.PromptTokens {
			w5m = u.PromptTokens
		}
		w1h = u.PromptTokens - w5m
	}
	return w5m, w1h
}

// HandleChatCompletions：POST /v1/chat/completions
// 计费时序 = hold（按估算上限冻结）→ 转发 → capture（按真实 usage 结算，差额自动解冻）。
// 上游失败 → release 全额解冻。结构性消灭"预检黑盒"类问题（TrustQuota 事故）。
// setClientMeta 记录访客侧元数据（请求日志详情 CLIENT 栏）。真实 IP 走 gin ClientIP——core 仅经
// Caddy 内网可达、X-Forwarded-For 由 Caddy 设置（不可被客户端伪造），与 keyGate 的 IP 白名单同源。
// UA/Referer 截断防滥长。绝不存 payload/PII 正文（仅请求元数据）。
func setClientMeta(inv *invocation.Invocation, c *gin.Context) {
	inv.ClientIP = clipMeta(c.ClientIP(), 64)
	inv.UserAgent = clipMeta(c.GetHeader("User-Agent"), 256)
	inv.Referer = clipMeta(c.GetHeader("Referer"), 256)
}

// clipMeta 按字节截断并去除尾部半个 rune（防 utf8mb4 列存进非法 UTF-8）。
func clipMeta(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxBytes], "")
}

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
		Modality:  invocation.ModalityChat, // anthropic/responses 入站方言同走本链路，同标 chat
		Protocol:  ingressProtocol(c),      // 入站方言真实记录（排障闭环 0045）：openai|anthropic|responses
	}
	setClientMeta(inv, c)
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
	routePolicy := resolveRoutePolicy(c, key)                      // 路由策略（0043）：X-LAIAI-Route 头 > key 默认 > order
	if code, msg, ok := r.gateKey(key, c.ClientIP(), model); !ok { // Key 精细管控（0012）
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, model); !ok { // 模型级限流（gap #2）
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	stream, _ := req["stream"].(bool)
	pref := parseProviderPref(req) // provider preference（设计 §4.4）：解析并从转发体剥除
	// 模型级 fallback 链（设计 §4.4，2026-07-11；顶级审计修排名 7，对标 OpenRouter models:[a,b,c]）：
	// 主模型"此路不通"类失败（无渠道/上游耗尽/余额或额度不足）自动降级下一模型；
	// 客户端 400 类与已开始写响应的失败绝不换模型。每个模型独立 hold（serveChatModel 自建自释）。
	chain := parseModelChain(req, model)
	for i, m := range chain {
		if i > 0 {
			// 降级模型重新过模型白名单 + 模型级限流（IP/quota/key RPM 已在入口过、与模型无关）
			if !key.AllowsModel(m) {
				continue
			}
			if ok, _ := r.allowModelRate(key.OrgID, m); !ok {
				continue
			}
			req["model"] = m
			r.Log.Warn("model fallback: trying next model", "model", m, "chain_pos", i)
		}
		if r.serveChatModel(c, key, inv, finish, req, body, m, stream, routePolicy, pref, i, i == len(chain)-1) == chatDone {
			return
		}
	}
	// 角落：链尾模型全被白名单/限流跳过 → 此前的 chatNextModel 都没写响应，统一失败收尾
	finish(invocation.StatusNoChannel, http.StatusServiceUnavailable, "model fallback chain exhausted")
	relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
}

// maxModelChain：fallback 链长上限（与重试预算同哲学，防长链放大尾延迟）。
const maxModelChain = 3

// parseModelChain：解析 OpenRouter 兼容的 models:[a,b,c] 并从转发体剥除。
// 返回 [主模型, 备选...]（去空/去重/钳 maxModelChain）；无 models 字段 → [主模型]。
func parseModelChain(req map[string]any, model string) []string {
	raw, has := req["models"]
	chain := []string{model}
	if !has {
		return chain
	}
	delete(req, "models") // 上游不认识该字段
	arr, ok := raw.([]any)
	if !ok {
		return chain
	}
	seen := map[string]bool{model: true}
	for _, v := range arr {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		chain = append(chain, s)
		if len(chain) >= maxModelChain {
			break
		}
	}
	return chain
}

// chatOutcome：serveChatModel 的收尾语义。
type chatOutcome int

const (
	chatDone      chatOutcome = iota // 已写响应并收尾（成功或终态失败）
	chatNextModel                    // fallback：本模型此路不通且**未写任何响应**——调用方切下一模型
)

// serveChatModel：单模型的完整服务周期（估算→hold→quota 预留→换渠道重试→回传→结算），
// 由 HandleChatCompletions 提取（模型级 fallback 链按模型重入）。last=false 时"此路不通"
// 类失败不写响应、返回 chatNextModel——返回前 hold 已释放、quota 预留由 defer 退还。
func (r *Relay) serveChatModel(c *gin.Context, key *identity.APIKey, inv *invocation.Invocation, finish func(string, int, string), req map[string]any, body []byte, model string, stream bool, routePolicy string, pref *providerPref, chainIdx int, last bool) chatOutcome {
	maxTokens := int64(defaultMaxTokens)
	if v := requestedMaxOutput(req); v > 0 { // 同时识别 max_tokens 与 max_completion_tokens（P0-2）
		maxTokens = v
		if maxTokens > maxOutputTokensCeiling {
			maxTokens = maxOutputTokensCeiling // 钳上限（审查 L-2）：防超大值把 hold 撑成误导性"余额不足"
		}
	}
	// 把**转发体**的输出上限也钳到 ceiling：上面只钳了 hold 估算用的局部变量 maxTokens，转发体里客户端
	// 原始的超大 max_tokens 仍会被各 adapter 原样转发；若上游真按其产出 >hold 口径，capture 的 actual 会
	// 超 hold、走 overage 把 org 余额刷负（与下方 n 倍同类资金红线）。只钳「已存在且确超 ceiling」的字段——
	// 不给未带该字段的请求平添字段，也不误抬小字段。方言别名同钳（outputCapAliases，对抗 review）。
	for _, f := range outputCapAliases {
		if v, ok := req[f].(float64); ok && v > float64(maxOutputTokensCeiling) {
			req[f] = float64(maxOutputTokensCeiling)
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
	// Gemini 思考（thinkingConfig 方言）：thoughts 计入 output 计费（顶级审计修排名 3），且 Gemini 的
	// thoughts **不受 maxOutputTokens 约束**（与 Anthropic 含在 max_tokens 内不同）→ hold 须在输出上限
	// 之外**加上**思考预算，否则默认开思考的 2.5 系借 overage 刷负余额（对抗 review CONFIRMED，
	// 与上方 Anthropic thinking 同类红线）。显式 thinkingBudget≥0 精确取值（0=关思考）；未显式/动态(-1)
	// 按隐式上限保守上浮。按模型名前缀识别（hold 在选渠道前，无 provider 可用）；多冻结由 settle 退差。
	if strings.HasPrefix(strings.ToLower(model), "gemini") {
		thinkBudget := int64(geminiImplicitThinkingHoldTokens)
		if tc, ok := req["thinkingConfig"].(map[string]any); ok {
			if bt, ok := tc["thinkingBudget"].(float64); ok && bt >= 0 {
				thinkBudget = int64(bt)
			}
		}
		maxTokens += thinkBudget
		if maxTokens > maxOutputTokensCeiling {
			maxTokens = maxOutputTokensCeiling
		}
	}
	// 多候选 n（OpenAI 上限 128）：输出 token 随 n 倍增、usage 计入全部候选，故 hold 也须按 n 倍冻结，
	// 否则 n 大 + max_completion_tokens 大可绕过余额闸、借 overage 把余额刷成负（P0-2 同类）。
	// best_of（vLLM 系方言）：服务端生成 best_of 个候选、usage 计全部——与 n 同口径纳入倍增与夹紧
	// （对抗 review：openai 系 adapter 整 map 透传，漏掉 best_of 即同类洞）。
	holdOutput := maxTokens
	candidates := int64(1)
	for _, f := range [...]string{"n", "best_of"} {
		if nv, ok := req[f].(float64); ok && nv > 1 {
			cand := int64(nv)
			if cand > maxCompletionChoices {
				cand = maxCompletionChoices
				req[f] = float64(cand) // 同步夹紧**转发体**：否则上游按原始值产出全部候选，
				// capture 按真实倍数结算 > 夹紧后 hold，超出走 AllowNegative overage 刷负余额（资金红线）。
			}
			if cand > candidates {
				candidates = cand
			}
		}
	}
	if candidates > 1 {
		holdOutput = maxTokens * candidates
	}
	inv.Model, inv.IsStream = model, stream
	// fallback 链重入（设计 §4.4）：inv 是跨模型共享的同一指针。重置上一模型可能写下的诊断字段，
	// 否则本模型若在进入换渠道循环体前就终态失败（无渠道/余额不足/quota 不足），记录会残留上一模型的
	// channel_id/provider/attempts/错误码（对抗 review：观测数据失真，误把 no_channel 归因到无关渠道）。
	inv.ChannelID, inv.Provider, inv.Attempts, inv.UpstreamErrorCode = 0, "", 0, ""

	// 3. 定价（热读缓存：命中即免 DB 查；hold/capture 仍取同一个 *price 局部变量）
	price, err := r.cachedPrice(model)
	if err != nil {
		if !last {
			return chatNextModel // 该模型未定价/不存在 → 降级下一模型
		}
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", model))
		return chatDone
	}

	// 4. Hold：按 tiktoken 估算输入 + max_tokens 上限 冻结（整个重试周期只 hold 一次）
	estIn := CountChatTokens(req, len(body))
	// Anthropic 写缓存溢价（1h=2x 输入价，结算点 CacheWriteSurcharge 补收）必须进 hold，否则带
	// cache_control 的大 prompt 结算必超 hold、借 AllowNegative overage 刷负余额（对抗 review
	// CONFIRMED，与 thinking budget 同类红线）。检测走原始 body 字节（覆盖块/工具/system 任何形态；
	// 正文里恰好出现该字符串只是多冻结——保守方向，settle 退差）。
	holdIn := estIn
	if bytes.Contains(body, []byte(`"cache_control"`)) {
		holdIn = estIn * 2 // 最坏情形：全部输入按 1h 写缓存 2x
	}
	// 计费倍率（0011 一层 + 0027 二层 (分组,模型)）：hold/capture 取同一次值，避免中途改配置导致 release 算错
	groupRatio := r.cachedRatio(key.OrgID, model)
	holdAmount := price.ChatCost(holdIn, 0, holdOutput).Mul(groupRatio) // holdOutput 含 n 倍；按次固定价时忽略 token（gap P2-F）
	// fallback 链：每模型独立幂等键（绝不共用 hold 跨模型结算错价，设计 §4.4）；
	// chainIdx=0 保持历史键形（零回归）。
	idem := "relay:" + inv.RequestID
	if chainIdx > 0 {
		idem = fmt.Sprintf("relay:%s#%d", inv.RequestID, chainIdx)
	}
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		// 分片感知 hold（CreateHoldSharded）：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），
		// 巨客户配 shardCount>1 后按 RequestID 散列打散写热点、不足跨片回退——届时只升级配置即可。
		h, _, err := ledger.CreateHoldSharded(tx, key.OrgID, ledger.AccountUserCredit, inv.RequestID, ledger.HoldInput{
			IdempotencyKey: idem, // 每请求唯一 16-hex（非时间戳，杜绝纳秒碰撞共用 hold）
			Amount:         holdAmount,
			RefType:        "usage",
			RefID:          model,
		})
		hold = h
		return err
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			if !last {
				return chatNextModel // 备选模型可能更便宜 → 降级（设计 §4.4）
			}
			finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "insufficient balance")
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return chatDone
		}
		r.Log.Error("hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return chatDone
	}

	// 4b. API Key 额度原子预留（P0-3：消解并发超 quota）。按 hold 估算预留；capture 对账到 actual，
	// 失败由下方 defer 退还（统一覆盖所有 release 路径）。预留失败＝会超 quota → 释放 hold + 402。
	if !identity.ReserveKeyQuota(r.DB, key.ID, holdAmount) {
		r.release(hold.ID)
		if !last {
			return chatNextModel // quota 预留是金额维度，备选模型可能更便宜 → 降级
		}
		finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "api key quota exceeded")
		relayErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
		return chatDone
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
	var credID uint64 // 本次终选的 credential id（0034 Multi-key）：成功清其失败计数，失败按 key 禁用
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	// 渠道快照取一次（8s 缓存），3 次重试复用同一份；exclude 走内存（WS-2 #1 读瓶颈消除）。
	channelSnap, snapErr := r.cachedChannels()
	breaker := r.newBreakerPicker() // 熔断闸（breaker.go）：open 渠道硬摘除；全被挡则降级放行
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if snapErr != nil {
			break // 快照加载失败：当作无渠道，落循环后失败处理
		}
		var pickErr error
		ch, pickErr = r.pickSnapshotPolicy(channelSnap, model, attempt, exclude, routePolicy, key.OrgID, pref)
		if pickErr != nil {
			if !breaker.reopen(exclude) {
				break // 无渠道可用：落到循环后的失败处理
			}
			ch, pickErr = r.pickSnapshotPolicy(channelSnap, model, attempt, exclude, routePolicy, key.OrgID, pref)
			if pickErr != nil {
				break
			}
		}
		if !breaker.admit(ch.ID, exclude) {
			attempt-- // 熔断摘除不烧重试预算（exclude 增长保证循环有界）
			continue
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		attemptStart = r.Now()
		// 选 key（0034 Multi-key）：按渠道模式从启用 key 挑一个；无 key → 换渠道。
		credEnc, credOK := "", false
		credID, credEnc, credOK = r.selectCredential(ch)
		if !credOK {
			r.Log.Error("channel has no usable credential", "channel", ch.ID)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		credential, err := r.Keyring.Decrypt(credEnc)
		if err != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "cred", credID, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue // 我方配置错误（知识库 channel: 前缀类）→ 换渠道
		}
		ad := adapterForChannel(ch)
		// 模型映射（0009）：每次 attempt 按当前渠道覆盖写，无跨渠道残留；
		// 计费/记录/对外响应仍用对外名（inv.Model / price 已定）。
		req["model"] = ch.UpstreamModel(model)
		// 渠道级 param override（0033，指令 §3.4）：在**本 attempt 的副本**上深合并（无 override
		// 零拷贝返回原 map）——换渠道重试各用各的 override，绝不残留。资金钳制键受保护（overrides.go）。
		upReq, err := ad.buildRequest(c.Request.Context(), ch, credential,
			applyParamOverride(req, ch.ParamOverride), stream)
		if err != nil {
			r.release(hold.ID)
			finish(invocation.StatusUpstreamError, http.StatusInternalServerError, "upstream request build failed")
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return chatDone // 请求本身不合法，换渠道/换模型都无意义
		}
		// 渠道级 header override（0033）：adapter 默认头之后应用（运营优先级最高），
		// {{credential}} 占位符注入解密凭证——自定义认证头不需要明文 key 进配置。
		applyHeaderOverride(upReq, ch.HeaderOverride, credential)
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			// 下游客户端断连/超时（ctx 取消）不是渠道故障——绝不喂健康分/熔断（对抗 review CONFIRMED：
			// 否则客户端批量中止可硬熔断健康渠道），且换渠道重试也是继续对已死 ctx 打水漂，直接终止。
			if c.Request.Context().Err() != nil {
				r.release(hold.ID)
				finish(invocation.StatusUpstreamError, 499, "client disconnected")
				return chatDone
			}
			r.Log.Warn("upstream unreachable", "channel", ch.ID, "attempt", attempt, "err", err)
			r.observe(ch.ID, false)     // 健康分：网络不可达 = 渠道可靠性失败
			r.breakerNote(ch.ID, false) // 熔断：不可达计一次
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
		r.breakerNote(ch.ID, resp.StatusCode < 500) // 熔断口径：仅 5xx 计失败（<500=渠道活着）
		disp := classifyUpstreamError(ch.Provider, resp.StatusCode, errBody)
		inv.UpstreamErrorCode = upstreamErrorCode(disp.Reason, errBody) // 排障闭环（0045）：白名单枚举，末次为准
		if healthPenalty(disp, resp.StatusCode) {
			r.observe(ch.ID, false) // 健康分：仅渠道整体可靠性失败（5xx/429/硬故障）；渠道特有 4xx 不计
		}
		// 不记原始上游错误体（审查 M-5）：可能回显请求片段/org 标识/prompt 碎片，
		// 违反"操作日志禁 payload/PII"红线。只记分类后的 reason。
		r.Log.Warn("upstream error", "channel", ch.ID, "attempt", attempt, "status", resp.StatusCode,
			"disable", disp.DisableChannel, "retryable", disp.Retryable, "reason", disp.Reason)
		if disp.DisableChannel {
			// 硬故障 → 记禁用建议（B-2，不自动禁用，运营手动决定）
			r.noteSuggestDisable(ch.ID, disp.Reason)
			// 多 key（0034）：鉴权/配额硬故障是 key 级问题，禁当前 key 而非整个渠道；
			// 禁到渠道无 active key 才连带禁渠道（受 channel_auto_disable 开关，默认关=不自动禁）。
			r.noteKeyFailure(credID, ch.ID, disp.Reason)
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
			if !last {
				return chatNextModel // 该模型无可用渠道 → 降级下一模型
			}
			finish(invocation.StatusNoChannel, http.StatusServiceUnavailable, "no upstream available")
			relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
			return chatDone
		}
		// 上游耗尽：仅"上游自身不行"类（5xx/429）降级下一模型；客户端 400 类绝不换模型
		// （换了也一样错，且掩盖真错误——设计 §4.4）。
		if !last && (lastStatus >= 500 || lastStatus == http.StatusTooManyRequests) {
			return chatNextModel
		}
		// error_brief 只存状态摘要，不存上游错误体（红线 8：禁 payload 回流元数据）
		finish(invocation.StatusUpstreamError, lastStatus, fmt.Sprintf("upstream status %d after %d attempts", lastStatus, inv.Attempts))
		c.Data(lastStatus, "application/json", lastBody)
		return chatDone
	}
	defer resp.Body.Close()
	ad := adapterForChannel(ch)

	// 上游响应校验（部署冒烟暴露）：200 但 content-type 非 JSON/SSE（如中转站返回 HTML 首页）
	// 判为上游异常——不透传垃圾、不误计费成 success。渠道级开关可放宽。
	if ch.EnforcesStrictResponse() && !IsLLMContentType(resp.Header.Get("Content-Type")) {
		r.release(hold.ID)
		r.Log.Warn("upstream returned non-LLM content-type, rejecting",
			"channel", ch.ID, "content_type", resp.Header.Get("Content-Type"))
		if !last {
			return chatNextModel // 上游返回垃圾 = 渠道/上游问题，未写响应 → 降级
		}
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream returned invalid response type")
		relayErr(c, http.StatusBadGateway, "upstream returned an invalid response (check channel base_url)")
		return chatDone
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
	r.enableChannelIfDisabled(ch) // 内含 clearSuggestDisable（B-2 撤销建议）
	r.resetKeyFailures(credID)    // 该 key 一次成功 → 清其失败计数（0034 Multi-key）
	r.breakerNote(ch.ID, true)    // 熔断：成功即闭合（半开探活成功走这里恢复全量）

	// 7. 响应回传（adapter 转换）+ usage 抽取 + capture
	// 对外 model 名存 context：上游响应不含 model 名的 adapter（bedrock Converse）回显时读它，
	// 避免回给客户端 {"model":""}（对抗 review）。其它 adapter 用上游 resp.Model，忽略此值。
	c.Set(respModelKey, model)
	if um, ok := req["model"].(string); ok { // 上游名（映射后）：Vertex 响应侧据此选 anthropic/gemini 解析器
		c.Set(respUpstreamModelKey, um)
	}
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

	// BYOK（0044）：命中自有渠道按费率结算（非 BYOK 因子恒为 1）
	byok := r.byokFactor(ch, key.OrgID)
	cw5m, cw1h := u.CacheWrites()          // Anthropic 写缓存溢价（1.25x/2x 输入价，差额补收）
	aIn, aOut := u.AudioIn(), u.AudioOut() // 音频 token 价差补收（gpt-4o-audio 等，≈16× 文本价）
	actual := price.ChatCost(u.PromptTokens, u.Cached(), u.CompletionTokens).
		Add(price.CacheWriteSurcharge(cw5m, cw1h)).
		Add(price.AudioSurcharge(aIn, aOut)).Mul(groupRatio).Mul(byok).Round(8)
	reason := fmt.Sprintf("model=%s in=%d cached=%d out=%d ×%s", model, u.PromptTokens, u.Cached(), u.CompletionTokens, groupRatio)
	if cw5m > 0 || cw1h > 0 {
		reason += fmt.Sprintf(" cw5m=%d cw1h=%d", cw5m, cw1h)
	}
	if aIn > 0 || aOut > 0 {
		reason += fmt.Sprintf(" audioIn=%d audioOut=%d", aIn, aOut)
	}
	if !byok.Equal(decimalOne) {
		reason += " byok×" + byok.String()
	}
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
	// 排障闭环（0045）：真实 finish_reason + 缓存/思考 token 拆分进观测层。
	// finish_reason 截断到列宽内（对抗 review：中转渠道可回传任意超长值，否则整批 invocation
	// 插入在 MySQL 严格模式下失败、丢计费审计元数据）。成功路径清 UpstreamErrorCode——
	// 换渠道重试成功后不该携带前次失败尝试的错误码（对抗 review：与胜出渠道 request_id 错配）。
	inv.FinishReason = clipMeta(u.FinishReason, 31)
	inv.CachedTokens, inv.ReasoningTokens = u.Cached(), u.CompletionTokensDetails.ReasoningTokens
	inv.UpstreamErrorCode = ""
	// 预留→实际对账（P0-3）：used_amount 已 +holdAmount，这里 +(actual-hold) 使其落到真实 actual；
	// 标记 settled 让上面的退还 defer 不再触发。已交付的部分 token 照实计费（actual），不退。
	_ = identity.AddKeyUsageSigned(r.DB, key.ID, actual.Sub(holdAmount))
	quotaSettled = true
	// P1-5：流中途 stall/读错误 → 已发 200 头的截断流，照实计费但**不可记为 success**。
	// 已开始写响应，绝不换模型（设计 §4.4）。
	if streamErr != nil {
		r.Log.Warn("upstream stream ended abnormally — recording as upstream_error",
			"channel", ch.ID, "model", model, "err", streamErr)
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream stream ended abnormally")
		return chatDone
	}
	finish(invocation.StatusSuccess, http.StatusOK, "")
	return chatDone
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
	r.observe(ch.ID, true)       // 健康分：一次成功（三类 relay 共用此成功钩子）
	r.clearSuggestDisable(ch.ID) // 渠道又好了 → 撤销禁用建议（B-2，共用钩子覆盖 chat/failover/realtime）
	// 不预读 ch.Status——它来自 status=1 活跃快照恒为 1，预读判定(if ch.Status!=2)会让本函数永不重启用（旧 BUG）。
	// 直接用条件 UPDATE 作原子闸：仅当当前确为 auto-disabled(status=2) 才翻回 1（对 status=1 是零行 no-op）。
	// 这样在另一实例持陈旧快照、把刚被禁的渠道选中并成功时仍能自愈。广播只在真发生翻转(RowsAffected>0)时发，
	// 不对每个成功请求都广播失效。
	go func() {
		res := r.DB.Model(&Channel{}).Where("id = ? AND status = 2", ch.ID).Update("status", 1)
		if res.Error == nil && res.RowsAffected > 0 {
			r.broadcastInvalidate(KindChannel, "") // 重启用的渠道即时回到快照可被选
			r.Log.Info("channel auto-re-enabled after success", "channel", ch.ID)
		}
	}()
}

// ---- 热读缓存包裹（WS-2）----
// 一律用 r.DB（裸库）加载，不用请求级 rdb：缓存值跨请求共享，绑某个请求 context 会让该请求
// 断连时取消别人正复用的加载查询。代价：填充那一次查询不随客户端断连取消（极快小表，净收益正）。

// cachedChannels 返回 status=1 活跃渠道快照（8s TTL）。返回只读 slice，调用方绝不可原地改元素。
// 一并填充每渠道的启用 key（0034 Multi-key）：一次批量查全部 active credential 按 channel 分组，
// 挂到 ch.Credentials——热路径选 key 零查询；key 禁用/新增触发快照失效重载。
func (r *Relay) cachedChannels() ([]Channel, error) {
	return r.channels.getOrLoad(func() ([]Channel, error) {
		var chs []Channel
		if err := r.DB.Where("status = 1").Order("priority DESC, id").Find(&chs).Error; err != nil {
			return nil, err
		}
		var creds []ChannelCredential
		if err := r.DB.Where("status = ?", CredentialActive).Order("channel_id, id").Find(&creds).Error; err != nil {
			return nil, err
		}
		byChannel := make(map[uint64][]ChannelCredential, len(chs))
		for _, c := range creds {
			byChannel[c.ChannelID] = append(byChannel[c.ChannelID], c)
		}
		for i := range chs {
			chs[i].Credentials = byChannel[chs[i].ID]
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
	if code == http.StatusTooManyRequests {
		c.Header("Retry-After", "60") // 限流窗口 60s 固定，统一出口设置（文档承诺 429 带 Retry-After）
	}
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_relay_error"}})
}
