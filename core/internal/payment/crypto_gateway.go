package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CryptoGateway：通用加密货币支付网关骨架（WS：加密通道，处理商无关）。
//
// 适配主流「托管发票 + HMAC 签名 webhook + 状态查询」三段式处理商（EBPay/OxaPay/NOWPayments/
// Cryptomus/Plisio 这一类）。处理商**无关的框架**已就绪：Engine 5 方法 + HMAC 验签 + 复用
// creditPaidOrder 幂等（topup:<order_no>）+ ReconcileTopups 主动对账，零新增入金代码。
//
// 通过 admin 后台 payment-channel 热配置（AES 加密存渠道 config）即可接入一个处理商：
//
//	processor / api_base / api_key / webhook_secret / sign_algo(sha256|sha512) / create_path / query_path
//
// 🚨 红线（同 coinbase/x402 纪律，默认禁用、验真后才 enable）：
//   - 验签算法 HMAC(raw body) → hex 是 OxaPay/NOWPayments/Coinbase 一类的主流方案，逻辑可信、可测；
//     但**具体处理商的签名口径与字段名各异**（如 Cryptomus 用 md5(base64(body)+key) 的排序参数方案），
//     上线某处理商前**必须接其真实环境抓 raw webhook/响应写 fixture 验真**，确认：签名口径、
//     create-charge 响应里支付地址/URL 字段、webhook 里 order_no/实付金额/成功状态枚举。绝不凭文档臆造。
//   - 入金金额取**实付**（webhook 的 paid/received 金额），不取应付价——underpaid 由 creditPaidOrder
//     的「实付不得少于应付」拦截（参见 coinbase 同类红线）。
//   - 只在终态成功状态入金（paid/confirmed/completed/finished），绝不在 pending/waiting/confirming 入金
//     （加密确认延迟，提前入金=未确认付款被 reorg 即资金黑洞）。
type CryptoGateway struct {
	Processor     string // 处理商标识（ebpay/oxapay/…），仅日志与区分
	APIBase       string // 处理商 API 基址
	APIKey        string // 商户 API key（鉴权头）
	WebhookSecret string // webhook 验签密钥（部分处理商 == APIKey）
	SignAlgo      string // sha256 | sha512（webhook HMAC 算法；默认 sha256）
	CreatePath    string // 创建发票端点（默认 /payment）
	QueryPath     string // 查询状态端点（默认 /payment/{order}）
	Client        *http.Client
}

func (c *CryptoGateway) Name() string {
	if c.Processor != "" {
		return c.Processor
	}
	return "crypto"
}

func (c *CryptoGateway) apiBase() string { return strings.TrimRight(c.APIBase, "/") }

func (c *CryptoGateway) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *CryptoGateway) newHMAC() hash.Hash {
	if strings.EqualFold(c.SignAlgo, "sha512") {
		return hmac.New(sha512.New, []byte(c.WebhookSecret))
	}
	return hmac.New(sha256.New, []byte(c.WebhookSecret)) // 默认 sha256
}

// cryptoCharge：webhook/query 共用的处理商响应解析（处理商无关的最小公共子集）。
// ⚠ 字段名按各处理商不同，上线前以真实 raw 校正（这里取主流命名作骨架）。
type cryptoCharge struct {
	OrderNo  string `json:"order_id"`    // 我方单号回传（部分处理商用 order_no/merchant_order_id）
	TradeNo  string `json:"payment_id"`  // 处理商交易号（部分用 uuid/txn_id）
	Status   string `json:"status"`      // 状态枚举（paid/confirmed/completed/pending/…）
	Paid     string `json:"amount_paid"` // **实付**金额（部分用 paid_amount/received/value）
	Amount   string `json:"amount"`      // 应付金额（仅核对，不作入金额）
	Currency string `json:"currency"`    // 计价币种（USD/USDT…）
	PayURL   string `json:"payment_url"` // 托管支付页（部分用 url/pay_url/invoice_url）
	Address  string `json:"pay_address"` // 或链上收款地址（无托管页时）
}

// cryptoPaidStatuses：视为终态成功的状态枚举（处理商各异，取主流并集；⚠ 上线以真实枚举校正）。
var cryptoPaidStatuses = map[string]bool{
	"paid": true, "confirmed": true, "completed": true, "finished": true, "success": true,
}

func (c *CryptoGateway) verifySig(rawBody []byte, sig string) bool {
	if c.WebhookSecret == "" || sig == "" {
		return false
	}
	mac := c.newHMAC()
	mac.Write(rawBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(sig))))
}

// CreatePayment：创建托管发票，返回支付页 URL（或链上地址）。⚠ 请求/响应字段按处理商验真。
func (c *CryptoGateway) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	cur := strings.ToUpper(req.Currency)
	if cur == "" {
		cur = "USD"
	}
	path := c.CreatePath
	if path == "" {
		path = "/payment"
	}
	body, _ := json.Marshal(map[string]any{
		"order_id":     req.OrderNo,
		"amount":       req.Amount.StringFixed(2),
		"currency":     cur,
		"callback_url": req.NotifyURL,
		"return_url":   req.ReturnURL,
		"description":  req.Description,
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+c.APIKey) // ⚠ 部分处理商用 X-Api-Key / merchant header
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("%s create payment: %w", c.Name(), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s create %d: %s", c.Name(), resp.StatusCode, excerpt(b))
	}
	// 处理商常把结果包在 data/result 里；先试顶层再试 data 包裹。
	ch := parseCryptoCharge(b)
	payTarget := ch.PayURL
	if payTarget == "" {
		payTarget = ch.Address // 无托管页则返回链上地址（前端展示收款地址 + 金额）
	}
	if payTarget == "" {
		return nil, fmt.Errorf("%s create: no payment_url/pay_address in response (schema mismatch — verify fixture)", c.Name())
	}
	return &CreateResult{PayURL: payTarget, ProviderRef: ch.TradeNo}, nil
}

// VerifyCallback：HMAC 验签 + 解析。成功态仅 cryptoPaidStatuses；金额取实付。
func (c *CryptoGateway) VerifyCallback(rawBody []byte, headers, _ map[string]string) (*CallbackResult, error) {
	sig := firstHeader(headers, "X-Signature", "x-signature", "Sign", "signature")
	if !c.verifySig(rawBody, sig) {
		return nil, fmt.Errorf("%s signature verification failed", c.Name())
	}
	ch := parseCryptoCharge(rawBody)
	paid, _ := decimal.NewFromString(ch.Paid)
	return &CallbackResult{
		OrderNo:  ch.OrderNo,
		TradeNo:  ch.TradeNo,
		Amount:   paid, // 实付（非应付），underpaid 由 creditPaidOrder 拦
		Currency: strings.ToUpper(ch.Currency),
		Success:  cryptoPaidStatuses[strings.ToLower(ch.Status)],
	}, nil
}

func (c *CryptoGateway) AckBody() string { return "" } // 多数处理商只看 2xx

// QueryOrder：主动查状态（ReconcileTopups 兜底）。
func (c *CryptoGateway) QueryOrder(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.OrderNo == "" && req.ProviderRef == "" {
		return &QueryResult{Found: false}, nil
	}
	path := c.QueryPath
	if path == "" {
		path = "/payment/"
	}
	ref := req.ProviderRef
	if ref == "" {
		ref = req.OrderNo
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+path+url.PathEscape(ref), nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("%s query: %w", c.Name(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &QueryResult{Found: false}, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s query %d: %s", c.Name(), resp.StatusCode, excerpt(b))
	}
	ch := parseCryptoCharge(b)
	paid, _ := decimal.NewFromString(ch.Paid)
	return &QueryResult{
		Found:    true,
		Paid:     cryptoPaidStatuses[strings.ToLower(ch.Status)],
		TradeNo:  ch.TradeNo,
		Amount:   paid,
		Currency: strings.ToUpper(ch.Currency),
	}, nil
}

// parseCryptoCharge：兼容顶层与 data/result 包裹两种常见形态。
func parseCryptoCharge(b []byte) cryptoCharge {
	var flat cryptoCharge
	_ = json.Unmarshal(b, &flat)
	if flat.OrderNo != "" || flat.Status != "" || flat.PayURL != "" || flat.Address != "" {
		return flat
	}
	var wrapped struct {
		Data   cryptoCharge `json:"data"`
		Result cryptoCharge `json:"result"`
	}
	_ = json.Unmarshal(b, &wrapped)
	if wrapped.Data.Status != "" || wrapped.Data.OrderNo != "" || wrapped.Data.PayURL != "" {
		return wrapped.Data
	}
	return wrapped.Result
}

func firstHeader(h map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := h[k]; v != "" {
			return v
		}
	}
	return ""
}
