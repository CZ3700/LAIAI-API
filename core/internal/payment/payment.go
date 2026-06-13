// Package payment：支付引擎抽象（移植旧平台 gateway 架构，用 decimal 重实现）。
// 引擎只负责"创建支付"与"验签回调"；入金/订单状态机在 server 层（红线集中）。
package payment

import (
	"context"

	"github.com/shopspring/decimal"
)

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

// CreateResult：创建结果，PayURL 是让用户跳转去支付的地址。
type CreateResult struct {
	PayURL      string
	ProviderRef string // 上游会话/订单引用
}

// CallbackResult：验签后的回调结论（入金依据）。
type CallbackResult struct {
	OrderNo  string
	TradeNo  string          // 上游交易号
	Amount   decimal.Decimal // 用户实付
	Currency string
	Success  bool
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
}
