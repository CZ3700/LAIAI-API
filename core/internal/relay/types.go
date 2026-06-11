// Package relay：转发内核 P1（设计 §4.4）。M1 阶段以包形式跑在 core 进程内，
// M2/M3 拆独立二进制（数据面无状态化时）。
package relay

import (
	"errors"
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
}

func (ModelPrice) TableName() string { return "model_prices" }

var oneMillion = decimal.NewFromInt(1_000_000)

// Cost 按 token 量计价（USD）。
func (p *ModelPrice) Cost(inputTokens, outputTokens int64) decimal.Decimal {
	in := p.InputUSDPer1M.Mul(decimal.NewFromInt(inputTokens)).Div(oneMillion)
	out := p.OutputUSDPer1M.Mul(decimal.NewFromInt(outputTokens)).Div(oneMillion)
	return in.Add(out)
}

// PickChannel 选择服务该模型的活跃渠道（权重大者优先；M2 引入健康分与会话黏性）。
func PickChannel(db *gorm.DB, model string) (*Channel, error) {
	var channels []Channel
	if err := db.Where("status = 1").Order("weight DESC, id").Find(&channels).Error; err != nil {
		return nil, err
	}
	for i := range channels {
		if channels[i].ServesModel(model) {
			return &channels[i], nil
		}
	}
	return nil, ErrNoChannel
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
