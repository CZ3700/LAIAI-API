// Package relay：转发内核 P1（设计 §4.4）。M1 阶段以包形式跑在 core 进程内，
// M2/M3 拆独立二进制（数据面无状态化时）。
package relay

import (
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
	ID            uint64 `gorm:"primaryKey"`
	Name          string `gorm:"column:name"`
	Provider      string `gorm:"column:provider"`
	BaseURL       string `gorm:"column:base_url"`
	CredentialEnc string `gorm:"column:credential_enc"`
	Models        string `gorm:"column:models"` // 逗号分隔
	Weight        int    `gorm:"column:weight"`
	Priority      int    `gorm:"column:priority"`             // 大者先用（0004）
	MaxConcurrentTasks int `gorm:"column:max_concurrent_tasks"` // 任务并发闸（0006，Seedance 公测 3）
	Status        int8   `gorm:"column:status"`
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
	ID             uint64          `gorm:"primaryKey"`
	Model          string          `gorm:"column:model"`
	InputUSDPer1M  decimal.Decimal `gorm:"column:input_usd_per_1m"`
	OutputUSDPer1M decimal.Decimal `gorm:"column:output_usd_per_1m"`
	ImageUSD       decimal.Decimal `gorm:"column:image_usd"` // 每张图 USD（0007，0=非图像模型）
}

func (ModelPrice) TableName() string { return "model_prices" }

var oneMillion = decimal.NewFromInt(1_000_000)

// Cost 按 token 量计价（USD）。
func (p *ModelPrice) Cost(inputTokens, outputTokens int64) decimal.Decimal {
	in := p.InputUSDPer1M.Mul(decimal.NewFromInt(inputTokens)).Div(oneMillion)
	out := p.OutputUSDPer1M.Mul(decimal.NewFromInt(outputTokens)).Div(oneMillion)
	return in.Add(out)
}

// PickChannelForAttempt 选择服务该模型的活跃渠道（知识库 §3，NewAPI channel_cache.go:96 算法）：
//   - 优先级分层：attempt 0 用最高 priority 层，attempt n 用第 n 层，超出钳到最低层（保证重试永远有渠道可用）
//   - 层内加权随机；**零权重语义**：weight=0 不是"别用我"，全零层均分，混合时低权重平滑
//   - exclude：本次请求已失败的渠道（重试时排除）
//
// M2 后续：abilities 物化索引（渠道规模化后）、健康分（Diagnostics 探针喂）。
func PickChannelForAttempt(db *gorm.DB, model string, attempt int, exclude map[uint64]bool) (*Channel, error) {
	var channels []Channel
	if err := db.Where("status = 1").Order("priority DESC, id").Find(&channels).Error; err != nil {
		return nil, err
	}
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
	return weightedPick(tiers[tier]), nil
}

// weightedPick 层内加权随机（零权重均分 + 低均值平滑，知识库 §3 烧出来的语义）。
func weightedPick(tier []*Channel) *Channel {
	if len(tier) == 1 {
		return tier[0]
	}
	sum := 0
	for _, ch := range tier {
		sum += ch.Weight
	}
	smoothing := 0
	if sum == 0 {
		smoothing = 100 // 全零：均分
		sum = len(tier) * 100
	} else if sum/len(tier) < 10 {
		smoothing = 100 // 低均值：平滑，防低权重渠道几乎永不被选
		sum += len(tier) * 100
	}
	r := rand.IntN(sum)
	for _, ch := range tier {
		r -= ch.Weight + smoothing
		if r < 0 {
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
