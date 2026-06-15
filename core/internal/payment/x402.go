package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// X402（设计 §5.2 第一档）：USDC(Base) 链上微支付作为充值通道。
// 与 stripe/epay 的根本不同：x402 是 header-based（HTTP 402 + X-PAYMENT 头），无跳转 URL；
// 链上结算交给外部 facilitator（verify→settle），我方只编排 + 入账。
//
// ⚠️ 合约校验状态（2026-06-14）：facilitator /verify、/settle 的请求/响应形态按 x402 v1
//
//	**公开规范**实现，尚未对真实/测试网 facilitator 跑通。**上线动真金前必须**：
//	① 对接真实 facilitator（或 Coinbase x402 facilitator）抓真实 raw response 写进 fixture；
//	② 校验 EIP-3009 authorization 字段与 USDC 合约地址；③ 测试网端到端走通一次。
//	未配置 FacilitatorURL/PayTo/Asset 时引擎不构建（engineFor 返回 nil）→ 默认禁用，
//	绝不在未验证状态下进入真实资金路径。
type X402 struct {
	FacilitatorURL string // facilitator 基址（verify/settle）
	PayTo          string // 收款地址 0x...
	Asset          string // USDC 合约地址（Base）0x...
	Network        string // 链网络，默认 "base"
	Client         *http.Client
}

func (x *X402) Name() string { return "x402" }

func (x *X402) network() string {
	if x.Network != "" {
		return x.Network
	}
	return "base"
}

func (x *X402) httpClient() *http.Client {
	if x.Client != nil {
		return x.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// usdcAtomic：USD 金额 → USDC 原子单位（6 位小数）整数串。
// 先 Truncate(6) 截到 USDC 精度再 ×1e6——绝不 Round：向上取整会要求客户多付（资金红线），
// 截断只可能少要求 1 个原子单位（不会拒正确付款）。USD 充值额通常 ≤2 位小数，此处恒精确。
func usdcAtomic(amount decimal.Decimal) string {
	return amount.Truncate(6).Mul(decimal.NewFromInt(1_000_000)).String()
}

// paymentRequirements：x402 v1 的 accepts 条目（"exact" scheme）。
type paymentRequirements struct {
	Scheme            string         `json:"scheme"`
	Network           string         `json:"network"`
	MaxAmountRequired string         `json:"maxAmountRequired"` // USDC 原子单位
	Resource          string         `json:"resource"`
	Description       string         `json:"description"`
	MimeType          string         `json:"mimeType"`
	PayTo             string         `json:"payTo"`
	MaxTimeoutSeconds int            `json:"maxTimeoutSeconds"`
	Asset             string         `json:"asset"`
	Extra             map[string]any `json:"extra,omitempty"`
}

func (x *X402) requirementsFor(orderNo string, amount decimal.Decimal, description string) paymentRequirements {
	return paymentRequirements{
		Scheme:            "exact",
		Network:           x.network(),
		MaxAmountRequired: usdcAtomic(amount),
		Resource:          "topup:" + orderNo,
		Description:       description,
		MimeType:          "",
		PayTo:             x.PayTo,
		MaxTimeoutSeconds: 600,
		Asset:             x.Asset,
		Extra:             map[string]any{"name": "USDC", "version": "2"},
	}
}

// CreatePayment：不跳转，返回 x402 paymentRequirements JSON（前端钱包据此构造 X-PAYMENT）。
func (x *X402) CreatePayment(_ context.Context, req *CreateRequest) (*CreateResult, error) {
	reqs := x.requirementsFor(req.OrderNo, req.Amount, req.Description)
	body, err := json.Marshal(map[string]any{"x402Version": 1, "accepts": []paymentRequirements{reqs}})
	if err != nil {
		return nil, err
	}
	return &CreateResult{ProviderRef: req.OrderNo, Requirements: string(body)}, nil
}

// VerifyCallback：x402 不走通用 callback（用专用 /x402/settle 端点 + Settle）。占位拒绝。
func (x *X402) VerifyCallback(_ []byte, _, _ map[string]string) (*CallbackResult, error) {
	return nil, fmt.Errorf("x402 settles via the dedicated /x402/settle endpoint, not callback")
}

func (x *X402) AckBody() string { return "" }

// QueryOrder：x402 结算是同步的（settle 即时返回 txHash），无标准异步查询端点 →
// 不参与主动对账（Found=false）。若 settle 成功但入账失败属罕见，靠 hold 回收兜底。
func (x *X402) QueryOrder(_ context.Context, _ QueryRequest) (*QueryResult, error) {
	return &QueryResult{Found: false}, nil
}

// X402SettleResult：Settle 结果（专用 settle 端点用）。
type X402SettleResult struct {
	Success bool
	TxHash  string
	Payer   string
}

// facilitator 响应形态（x402 v1 公开规范；⚠️ 待真实 facilitator 校验）。
type facilitatorVerifyResp struct {
	IsValid       bool   `json:"isValid"`
	InvalidReason string `json:"invalidReason"`
	Payer         string `json:"payer"`
}
type facilitatorSettleResp struct {
	Success     bool   `json:"success"`
	ErrorReason string `json:"errorReason"`
	Transaction string `json:"transaction"`
	Network     string `json:"network"`
	Payer       string `json:"payer"`
}

// Settle：解码 X-PAYMENT 头 → facilitator /verify → /settle，返回链上结算结论。
// xPaymentB64 = 客户端 X-PAYMENT 头（base64 的 paymentPayload JSON）。
// amount 用订单应付额重建 requirements（防客户端篡改金额）。
func (x *X402) Settle(ctx context.Context, xPaymentB64, orderNo string, amount decimal.Decimal, description string) (*X402SettleResult, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(xPaymentB64))
	if err != nil {
		return nil, fmt.Errorf("x402: bad X-PAYMENT encoding: %w", err)
	}
	var paymentPayload map[string]any
	if err := json.Unmarshal(raw, &paymentPayload); err != nil {
		return nil, fmt.Errorf("x402: bad X-PAYMENT json: %w", err)
	}
	reqs := x.requirementsFor(orderNo, amount, description) // 服务端重建：金额以订单为准
	envelope := map[string]any{
		"x402Version":         1,
		"paymentPayload":      paymentPayload,
		"paymentRequirements": reqs,
	}

	// 1. verify
	var vr facilitatorVerifyResp
	if err := x.facilitatorPost(ctx, "/verify", envelope, &vr); err != nil {
		return nil, err
	}
	if !vr.IsValid {
		return &X402SettleResult{Success: false}, fmt.Errorf("x402 verify rejected: %s", vr.InvalidReason)
	}
	// 2. settle（真正链上结算）
	var sr facilitatorSettleResp
	if err := x.facilitatorPost(ctx, "/settle", envelope, &sr); err != nil {
		return nil, err
	}
	if !sr.Success {
		return &X402SettleResult{Success: false, Payer: sr.Payer}, fmt.Errorf("x402 settle failed: %s", sr.ErrorReason)
	}
	return &X402SettleResult{Success: true, TxHash: sr.Transaction, Payer: sr.Payer}, nil
}

func (x *X402) facilitatorPost(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(x.FacilitatorURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := x.httpClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("x402 facilitator %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("x402 facilitator %s http %d: %s", path, resp.StatusCode, excerpt(respBody))
	}
	return json.Unmarshal(respBody, out)
}
