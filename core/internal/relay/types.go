// Package relay：转发内核 P1（设计 §4.4）。M1 阶段以包形式跑在 core 进程内，
// M2/M3 拆独立二进制（数据面无状态化时）。
package relay

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrNoChannel      = errors.New("relay: no active channel serves this model")
	ErrModelNotPriced = errors.New("relay: model has no price configured")
)

type Channel struct {
	ID                 uint64 `gorm:"primaryKey"`
	Name               string `gorm:"column:name"`
	Provider           string `gorm:"column:provider"`
	BaseURL            string `gorm:"column:base_url"`
	CredentialEnc      string `gorm:"column:credential_enc"`
	Models             string `gorm:"column:models"` // 逗号分隔
	Weight             int    `gorm:"column:weight"`
	Priority           int    `gorm:"column:priority"`             // 大者先用（0004）
	MaxConcurrentTasks int    `gorm:"column:max_concurrent_tasks"` // 任务并发闸（0006，Seedance 公测 3）
	StrictResponse     int8   `gorm:"column:strict_response"`      // 1=校验上游响应 content-type（0008）
	ModelMapping       string `gorm:"column:model_mapping"`        // JSON {对外名:上游名}（0009，空=不映射）
	Proxy              string `gorm:"column:proxy"`                // 渠道级出网代理（0019，空=直连）
	Status             int8   `gorm:"column:status"`
}

// UpstreamModel 返回该渠道上游实际使用的模型名（无映射/解析失败 → 原名）。
// 计费、调用记录、对外响应一律用对外名；映射只影响上游请求体。
func (c *Channel) UpstreamModel(model string) string {
	if c.ModelMapping == "" {
		return model
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(c.ModelMapping), &m); err != nil {
		return model
	}
	if v, ok := m[model]; ok && v != "" {
		return v
	}
	return model
}

// mappedRawBody：embeddings/images 等透传 rawBody 的路径上应用模型映射。
// 返回（可能改写后的 body, 上游模型名）；无映射时零开销原样返回。
func mappedRawBody(ch *Channel, rawBody []byte, model string) ([]byte, string) {
	up := ch.UpstreamModel(model)
	if up == model {
		return rawBody, model
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return rawBody, model
	}
	m["model"] = up
	b, err := json.Marshal(m)
	if err != nil {
		return rawBody, model
	}
	return b, up
}

// EnforcesStrictResponse 返回是否启用上游响应校验（默认开；历史/手工行未设值视为开）。
func (c *Channel) EnforcesStrictResponse() bool { return c.StrictResponse != 0 }

// IsLLMContentType 判断上游响应 content-type 是否为合法的 LLM 响应类型
// （JSON 或 SSE）。HTML/纯文本等被判为上游异常（中转站返回首页、网关错误页等）。
func IsLLMContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "json") || strings.Contains(ct, "event-stream")
}

func (Channel) TableName() string { return "channels" }

func (c *Channel) ServesModel(model string) bool {
	for _, m := range strings.Split(c.Models, ",") {
		if strings.TrimSpace(m) == model {
			return true
		}
	}
	return false
}

type ModelPrice struct {
	ID                   uint64          `gorm:"primaryKey"`
	Model                string          `gorm:"column:model"`
	InputUSDPer1M        decimal.Decimal `gorm:"column:input_usd_per_1m"`
	OutputUSDPer1M       decimal.Decimal `gorm:"column:output_usd_per_1m"`
	ImageUSD             decimal.Decimal `gorm:"column:image_usd"`                  // 每张图 USD（0007，0=非图像模型）
	CachedInputUSDPer1M  decimal.Decimal `gorm:"column:cached_input_usd_per_1m"`    // 缓存命中输入价（0010，0=按 input 全价）
	PerCallUSD           decimal.Decimal `gorm:"column:per_call_usd"`               // 按次固定价（0027，>0 时 chat/embeddings 忽略 token 按次收）
	TTSPer1KChars        decimal.Decimal `gorm:"column:tts_usd_per_1k_chars"`       // TTS 每千字符 USD（0027）
	TranscriptionPerCall decimal.Decimal `gorm:"column:transcription_usd_per_call"` // 转录每次固定价 USD（0027）
}

func (ModelPrice) TableName() string { return "model_prices" }

var oneMillion = decimal.NewFromInt(1_000_000)

// cachedInputPrice：缓存命中 token 的单价。未配置（0）则回退 input 全价——
// 向后兼容且永不少收：旧定价行没有缓存价时，缓存 token 仍按正常输入价计。
func (p *ModelPrice) cachedInputPrice() decimal.Decimal {
	if p.CachedInputUSDPer1M.IsPositive() {
		return p.CachedInputUSDPer1M
	}
	return p.InputUSDPer1M
}

// Cost 按 token 量计价（USD）。cachedTokens 是 inputTokens 中命中提示缓存的子集
// （OpenAI prompt_tokens_details.cached_tokens 语义），按折扣价单独计；
// 非缓存输入 = inputTokens - cachedTokens 按全价。修正此前缓存 token 按全价多收的问题。
func (p *ModelPrice) Cost(inputTokens, cachedTokens, outputTokens int64) decimal.Decimal {
	if cachedTokens > inputTokens {
		cachedTokens = inputTokens // 防御：上游异常数据不致算出负的非缓存量
	}
	nonCached := inputTokens - cachedTokens
	in := p.InputUSDPer1M.Mul(decimal.NewFromInt(nonCached)).Div(oneMillion)
	cached := p.cachedInputPrice().Mul(decimal.NewFromInt(cachedTokens)).Div(oneMillion)
	out := p.OutputUSDPer1M.Mul(decimal.NewFromInt(outputTokens)).Div(oneMillion)
	return in.Add(cached).Add(out)
}

var oneThousand = decimal.NewFromInt(1000)

// ChatCost：每请求计费。PerCallUSD>0 → 按次固定价（忽略 token，gap P2-F「按次固定价」）；
// 否则回退按 token 的 Cost。chat 与 embeddings 共用（embeddings 传 output=0）。
func (p *ModelPrice) ChatCost(inputTokens, cachedTokens, outputTokens int64) decimal.Decimal {
	if p.PerCallUSD.IsPositive() {
		return p.PerCallUSD
	}
	return p.Cost(inputTokens, cachedTokens, outputTokens)
}

// SpeechCost：TTS 按输入字符数计价（tts_usd_per_1k_chars × chars / 1000，gap P2-F）。
func (p *ModelPrice) SpeechCost(chars int64) decimal.Decimal {
	if chars < 0 {
		chars = 0
	}
	return p.TTSPer1KChars.Mul(decimal.NewFromInt(chars)).Div(oneThousand)
}

// ModelGroupRatio：分组×模型的二层倍率（gap P2-F）。命中 (group_name, model) 返回其 ratio，
// 否则回退 1。group_name 空（无分组）直接 1。
func ModelGroupRatio(db *gorm.DB, groupName, model string) decimal.Decimal {
	if groupName == "" {
		return decimal.NewFromInt(1)
	}
	var ratio decimal.Decimal
	err := db.Raw(`SELECT ratio FROM model_group_ratios WHERE group_name = ? AND model = ?`,
		groupName, model).Scan(&ratio).Error
	if err != nil || !ratio.IsPositive() {
		return decimal.NewFromInt(1)
	}
	return ratio
}

// EffectiveRatio：最终计费倍率 = 一层分组倍率 × 二层(分组,模型)倍率（gap P2-F「二层分组倍率」）。
// 取代裸 GroupRatio：hold 与 capture 必须用同一次取值（调用方取一次传两处）。各层查不到/异常回退 1。
func EffectiveRatio(db *gorm.DB, orgID uint64, model string) decimal.Decimal {
	var row struct {
		GroupName string
		Ratio     decimal.Decimal
	}
	err := db.Raw(`SELECT o.group_name AS group_name, COALESCE(gr.ratio, 1) AS ratio
		FROM organizations o LEFT JOIN group_ratios gr ON gr.name = o.group_name
		WHERE o.id = ?`, orgID).Scan(&row).Error
	base := decimal.NewFromInt(1)
	if err == nil && row.Ratio.IsPositive() {
		base = row.Ratio
	}
	return base.Mul(ModelGroupRatio(db, row.GroupName, model))
}

// PickChannelForAttempt 选择服务该模型的活跃渠道（知识库 §3，NewAPI channel_cache.go:96 算法）：
//   - 优先级分层：attempt 0 用最高 priority 层，attempt n 用第 n 层，超出钳到最低层（保证重试永远有渠道可用）
//   - 层内加权随机；**零权重语义**：weight=0 不是"别用我"，全零层均分，混合时低权重平滑
//   - exclude：本次请求已失败的渠道（重试时排除）
//
// health(channelID)∈[0.1,1] 是层内权重乘子（M2 收尾）；nil = 不启用健康分（全 1.0），
// 保持与历史行为完全一致（测试/非 relay 调用方传 nil）。
// M2 后续：abilities 物化索引（渠道规模化后）。
func PickChannelForAttempt(db *gorm.DB, model string, attempt int, exclude map[uint64]bool, health func(uint64) float64) (*Channel, error) {
	var channels []Channel
	if err := db.Where("status = 1").Order("priority DESC, id").Find(&channels).Error; err != nil {
		return nil, err
	}
	return pickFromSnapshot(channels, model, attempt, exclude, health)
}

// pickFromSnapshot：从内存中的 status=1 渠道快照按同款算法选路（WS-2 #1 读瓶颈优化）。
// 与 PickChannelForAttempt 行为逐字等价，但输入已是内存 slice（不查 DB），供 relay 在 3 次
// 重试循环外取一次快照、循环内复用——把每请求 1~3 次全表扫降为命中时 0 次。
// 调用方只读 channels（exclude/健康分都走外部），绝不原地改快照元素。
func pickFromSnapshot(channels []Channel, model string, attempt int, exclude map[uint64]bool, health func(uint64) float64) (*Channel, error) {
	// 过滤：服务该模型且未被排除，按 priority 分层（保持降序）
	var tiers [][]*Channel
	var curPriority int
	for i := range channels {
		ch := &channels[i]
		if exclude[ch.ID] || !ch.ServesModel(model) {
			continue
		}
		if len(tiers) == 0 || ch.Priority != curPriority {
			tiers = append(tiers, nil)
			curPriority = ch.Priority
		}
		tiers[len(tiers)-1] = append(tiers[len(tiers)-1], ch)
	}
	if len(tiers) == 0 {
		return nil, ErrNoChannel
	}
	tier := attempt
	if tier >= len(tiers) {
		tier = len(tiers) - 1 // 钳到最低层
	}
	return weightedPick(tiers[tier], health), nil
}

// weightedPick 层内加权随机：基础权重（零权重均分 + 低均值平滑，知识库 §3 烧出来的语义）
// 再乘以健康因子 health(id)∈[0.1,1]（nil=全 1.0）——健康渠道分到更多流量，但 floor>0
// 保证不健康渠道永不被硬排除（保留恢复机会；硬下线是 auto-disable 的职责）。
func weightedPick(tier []*Channel, health func(uint64) float64) *Channel {
	if len(tier) == 1 {
		return tier[0] // lone 渠道：无可重分配对象，健康分不参与
	}
	sum := 0
	for _, ch := range tier {
		sum += ch.Weight
	}
	smoothing := 0
	if sum == 0 || sum/len(tier) < 10 {
		smoothing = 100 // 全零均分 / 低均值平滑：防低权重渠道几乎永不被选
	}
	// 有效权重 = (weight+smoothing) × 健康因子；float 累加（健康分是乘子）。
	weights := make([]float64, len(tier))
	var total float64
	for i, ch := range tier {
		h := 1.0
		if health != nil {
			h = health(ch.ID)
		}
		weights[i] = float64(ch.Weight+smoothing) * h
		total += weights[i]
	}
	if total <= 0 {
		return tier[len(tier)-1] // floor>0 下理论不可达；兜底防除零
	}
	pick := rand.Float64() * total
	for i, ch := range tier {
		pick -= weights[i]
		if pick < 0 {
			return ch
		}
	}
	return tier[len(tier)-1]
}

func GetPrice(db *gorm.DB, model string) (*ModelPrice, error) {
	p := &ModelPrice{}
	if err := db.Where("model = ?", model).First(p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrModelNotPriced
		}
		return nil, err
	}
	return p, nil
}

// EstimateTokens：无 tokenizer 的保守估算（chars/4），仅用于 hold 预冻结；
// capture 永远以上游返回的真实 usage 为准（三档抽取的最后兜底也用它）。
func EstimateTokens(byteLen int) int64 {
	n := int64(byteLen / 4)
	if n < 16 {
		n = 16
	}
	return n
}
