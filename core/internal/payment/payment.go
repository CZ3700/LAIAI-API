// Package payment：支付引擎抽象（移植旧平台 gateway 架构，用 decimal 重实现）。
// 引擎只负责"创建支付"与"验签回调"；入金/订单状态机在 server 层（红线集中）。
package payment

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/shopspring/decimal"
)

// validatePayURL：跳转支付地址安全校验（对抗审计 CONFIRMED——custom/webhook 引擎从上游响应
// 点路径提取 URL 直接交前端 window.location 跳转；渠道配置被攻破/写错即成钓鱼跳板）。
// 只放行 http(s) 绝对 URL 且带 host——挡掉 javascript:/data:/相对路径等注入向量。
func validatePayURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("pay url not parseable")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("pay url scheme %q not allowed (http/https only)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("pay url missing host")
	}
	return nil
}

// CreateRequest：创建一笔支付。Amount 是实付金额（PayCurrency 单位）。
type CreateRequest struct {
	OrderNo     string
	Amount      decimal.Decimal
	Currency    string // USD / CNY ...
	Description string
	NotifyURL   string // 异步回调（webhook）
	ReturnURL   string // 用户支付完成跳回
	ClientIP    string
	PayMethod   string // alipay / wxpay / card ...（引擎自定）
}

// CreateResult：创建结果。PayURL 是跳转支付地址（重定向类引擎）；
// Requirements 是 x402 的支付要求 JSON（header-based 引擎，无跳转，前端钱包据此签名付款）。
type CreateResult struct {
	PayURL       string
	ProviderRef  string // 上游会话/订单引用
	Requirements string // x402 paymentRequirements JSON（非 x402 引擎留空）
}

// CallbackResult：验签后的回调结论（入金依据）。
type CallbackResult struct {
	OrderNo  string
	TradeNo  string          // 上游交易号
	Amount   decimal.Decimal // 用户实付
	Currency string
	Success  bool
}

// QueryRequest：主动对账查询入参。OrderNo=我方单号（epay 用 out_trade_no 查）；
// ProviderRef=网关侧引用（Stripe Checkout Session id，按会话查）。引擎按各自需要取用。
type QueryRequest struct {
	OrderNo     string
	ProviderRef string
}

// QueryResult：主动查询到的支付状态（reconcile 兜底 callback 漏发的依据）。
type QueryResult struct {
	Found    bool            // 网关侧能否查到此单（false=查不到/不存在，区别于查到但未付）
	Paid     bool            // 是否已支付成功
	TradeNo  string          // 网关交易号
	Amount   decimal.Decimal // 实付金额（网关币种口径，与 callback 同）
	Currency string
}

// Engine：一个支付引擎（stripe/epay/...）。
type Engine interface {
	Name() string
	// CreatePayment 创建支付会话，返回跳转 URL。
	CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error)
	// VerifyCallback 验签并解析回调；Success=false 表示验签失败或非成功通知。
	VerifyCallback(rawBody []byte, headers, query map[string]string) (*CallbackResult, error)
	// AckBody 返回应回给上游的确认响应体（如 epay 要求回 "success"）。
	AckBody() string
	// QueryOrder 主动查询订单支付状态（红线 #5：对账兜底 callback 漏发）。
	QueryOrder(ctx context.Context, req QueryRequest) (*QueryResult, error)
}
