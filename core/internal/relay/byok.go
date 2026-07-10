package relay

// BYOK 计费因子（0044，设计 §4.4）：BYOK 渠道命中时结算 = 平台价 × byok_fee_percent。
// hold 仍按全价预扣（渠道命中在 hold 之后才知道），结算释放差额；capture≤hold 不变量
// 天然保持（费率 ∈ [0,1]）。费率读 ext_settings（默认 5%），60s 缓存。

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

var decimalOne = decimal.NewFromInt(1)

// ByokAllowPrivateForTest：**仅测试用**（与 RealtimeDialTimeout 同款导出可调惯例）——
// e2e 的 mock 上游在 127.0.0.1，放行私网拨号。生产路径永不置 true：BYOK 恒禁私网是
// 设计红线（§4.4 安全①），刻意不吃 advanced_custom_allow_private 等任何运行时开关
// （否则任意租户可在开关开启时探测平台内网）。
var ByokAllowPrivateForTest = false

// byokFeeRatio：ext_settings byok_fee_percent（默认 5，单位 %；0 合法=纯免费路由；
// 越界值回默认）→ 结算乘数（5 → 0.05）。60s 缓存（与 chanStats 同 TTL 风格）。
func (r *Relay) byokFeeRatio() decimal.Decimal {
	now := r.Now()
	r.byokMu.Lock()
	defer r.byokMu.Unlock()
	if r.byokLoaded && now.Sub(r.byokAt) < time.Minute {
		return r.byokRatio
	}
	pct := 5.0 // 默认 5%
	var row struct{ Value string }
	if err := r.DB.Table("ext_settings").Where("name = ?", "byok_fee_percent").
		Select("value").Take(&row).Error; err == nil {
		if v, e := strconv.ParseFloat(row.Value, 64); e == nil && v >= 0 && v <= 100 {
			pct = v
		}
	}
	r.byokRatio = decimal.NewFromFloat(pct / 100).Round(8)
	r.byokAt, r.byokLoaded = now, true
	return r.byokRatio
}

// byokFactor：结算乘数。非 BYOK 命中恒为 1；BYOK 命中（渠道归属==请求方 org）按费率。
// 防御：owner 与请求方不一致（隔离红线下不可达）按全价收，绝不给别人折扣。
func (r *Relay) byokFactor(ch *Channel, orgID uint64) decimal.Decimal {
	if ch == nil || ch.OwnerOrgID == 0 || ch.OwnerOrgID != orgID {
		return decimalOne
	}
	return r.byokFeeRatio()
}

// byokProviders：BYOK 允许的 provider 显式白名单（设计 §4.4 安全②）——只收已知云端
// chat 家族档案；排除 advanced_custom（任意端点是 admin 的职责）、TaskOnly（task 不参与
// BYOK v1）、自建家族（私网 base_url 语义与 BYOK 恒禁私网冲突）。
var byokProviders = map[string]bool{
	"openai": true, "anthropic": true, "gemini": true, "azure": true,
	"groq": true, "mistral": true, "xai": true, "deepseek": true,
	"moonshot": true, "zhipu": true, "dashscope-intl": true,
	"minimax": true, "siliconflow": true, "volcengine": true, "modelark": true,
}

// ByokProviderProfile：server 侧创建 BYOK 渠道时的白名单校验 + 默认 base_url。
// 返回 (默认 base_url, 是否允许)。默认 base_url 可为空（如 azure，必须用户提供）。
func ByokProviderProfile(provider string) (string, bool) {
	if !byokProviders[provider] {
		return "", false
	}
	p, ok := providerProfiles[provider]
	if !ok {
		return "", false
	}
	return p.DefaultBaseURL, true
}
