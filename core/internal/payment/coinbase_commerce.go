package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CoinbaseCommerce（加密货币充值主通道，WS-4）：托管型「加密版 Stripe」——hosted_url 跳转 +
// webhook 验签 + 主动 GET /charges 查单对账，与 Stripe/EPay 架构同构，可原样接入 payment.Engine。
// 商户不托管私钥/不跑链上节点（资金由 Coinbase 托管结算），契合当前体量。
//
// 🚨 上线前红线（与 x402 同纪律——默认禁用，验真后才 enable）：
//   - 验签算法 HMAC-SHA256(raw body) → hex，与 X-CC-Webhook-Signature 比对：已据官方
//     coinbase-commerce-node/lib/Webhook.js 源码确认（confidence: confirmed），逻辑可信、可测。
//   - 但以下「字段层级/枚举」仅据官方文档与 SDK 推导，**必须接真实账号抓 raw 写进 fixture 核对**
//     再置 enable（标 ⚠待真实验证）：① create-charge 响应是否 data{} 包裹、hosted_url/code 字段；
//     ② webhook envelope 的 event.data 充值对象嵌套、metadata.order_no 是否原样回传、实付取
//     pricing.local 还是 payments[]；③ GET /charges/{code} 判「已确认」的 timeline.status 枚举；
//     ④ X-CC-Version 当前有效值。绝不在未验真前进真金路径（红线：不凭文档臆造，epay 方言教训）。
//   - 只在 charge:confirmed / charge:resolved 入金，绝不在 pending/delayed 入金（加密确认可达 1h+，
//     提前入金=未确认付款被 reorg 即资金黑洞）。
type CoinbaseCommerce struct {
	APIKey        string // X-CC-Api-Key（Settings>API keys）
	WebhookSecret string // Settings>Notifications 的 shared secret（与 APIKey 独立的另一密钥）
	APIBase       string // 默认 https://api.commerce.coinbase.com；测试注入 httptest
	Client        *http.Client
}

// ccAPIVersion ⚠待真实验证：官方文档推荐固定 API 版本以稳定 schema；值疑为 2018-03-22。
const ccAPIVersion = "2018-03-22"

func (c *CoinbaseCommerce) Name() string { return "coinbase" }

func (c *CoinbaseCommerce) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://api.commerce.coinbase.com"
}

func (c *CoinbaseCommerce) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *CoinbaseCommerce) setHeaders(r *http.Request) {
	r.Header.Set("X-CC-Api-Key", c.APIKey)
	r.Header.Set("X-CC-Version", ccAPIVersion)
	r.Header.Set("Content-Type", "application/json")
}

// CreatePayment：建 fixed_price charge，返回 hosted_url（跳转）+ code（ProviderRef，供对账查单）。
// local_price.amount 是 string、USD 口径（与 epay money 同口径，禁浮点）。
func (c *CoinbaseCommerce) CreatePayment(ctx context.Context, req *CreateRequest) (*CreateResult, error) {
	cur := strings.ToUpper(req.Currency)
	if cur == "" {
		cur = "USD"
	}
	body, _ := json.Marshal(map[string]any{
		"name":         req.Description,
		"description":  req.Description,
		"pricing_type": "fixed_price",
		"local_price":  map[string]string{"amount": req.Amount.StringFixed(2), "currency": cur},
		"metadata":     map[string]string{"order_no": req.OrderNo},
		"redirect_url": req.ReturnURL,
		"cancel_url":   req.ReturnURL,
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+"/charges", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(hreq)
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("coinbase create charge: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coinbase create charge %d: %s", resp.StatusCode, excerpt(b))
	}
	// ⚠待真实验证：data{} 包裹与字段名（hosted_url / code）。
	var out struct {
		Data struct {
			Code      string `json:"code"`
			HostedURL string `json:"hosted_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("coinbase create charge decode: %w", err)
	}
	if out.Data.HostedURL == "" {
		return nil, fmt.Errorf("coinbase create charge: empty hosted_url (schema mismatch — verify fixture)")
	}
	return &CreateResult{PayURL: out.Data.HostedURL, ProviderRef: out.Data.Code}, nil
}

// VerifyCallback：验 X-CC-Webhook-Signature = HMAC-SHA256(raw body) hex，
// 仅 charge:confirmed / charge:resolved 视为成功（最终态；resolved 的 underpaid 由 creditPaidOrder
// 的「实付不得少于应付」自动拦截）。
func (c *CoinbaseCommerce) VerifyCallback(rawBody []byte, headers, _ map[string]string) (*CallbackResult, error) {
	sig := headers["X-Cc-Webhook-Signature"]
	if sig == "" {
		sig = headers["x-cc-webhook-signature"]
	}
	if sig == "" {
		sig = headers["X-CC-Webhook-Signature"]
	}
	if !c.verifySig(rawBody, sig) {
		return nil, fmt.Errorf("coinbase signature verification failed")
	}
	// ⚠待真实验证：event.data 充值对象嵌套、pricing.local 字段、metadata.order_no 回传。
	var env struct {
		Event struct {
			Type string `json:"type"`
			Data struct {
				Code     string            `json:"code"`
				Metadata map[string]string `json:"metadata"`
				Pricing  struct {
					Local struct {
						Amount   string `json:"amount"`
						Currency string `json:"currency"`
					} `json:"local"`
				} `json:"pricing"`
			} `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return nil, fmt.Errorf("coinbase webhook decode: %w", err)
	}
	d := env.Event.Data
	amount, _ := decimal.NewFromString(d.Pricing.Local.Amount)
	return &CallbackResult{
		OrderNo:  d.Metadata["order_no"],
		TradeNo:  d.Code,
		Amount:   amount,
		Currency: strings.ToUpper(d.Pricing.Local.Currency),
		Success:  env.Event.Type == "charge:confirmed" || env.Event.Type == "charge:resolved",
	}, nil
}

func (c *CoinbaseCommerce) AckBody() string { return "" } // Coinbase 只看 2xx（同 Stripe）

// QueryOrder：GET /charges/{code}，按 timeline 判「已确认」（ReconcileTopups 兜底，x402 给不了的对账腿）。
func (c *CoinbaseCommerce) QueryOrder(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.ProviderRef == "" {
		return &QueryResult{Found: false}, nil
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.apiBase()+"/charges/"+url.PathEscape(req.ProviderRef), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(hreq)
	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("coinbase query charge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &QueryResult{Found: false}, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coinbase query %d: %s", resp.StatusCode, excerpt(b))
	}
	// ⚠待真实验证：timeline.status 判「已支付」的枚举字符串。
	var out struct {
		Data struct {
			Code    string `json:"code"`
			Pricing struct {
				Local struct {
					Amount   string `json:"amount"`
					Currency string `json:"currency"`
				} `json:"local"`
			} `json:"pricing"`
			Timeline []struct {
				Status string `json:"status"`
			} `json:"timeline"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("coinbase query decode: %w", err)
	}
	paid := false
	for _, t := range out.Data.Timeline {
		switch strings.ToUpper(t.Status) {
		case "COMPLETED", "CONFIRMED", "RESOLVED":
			paid = true
		}
	}
	amount, _ := decimal.NewFromString(out.Data.Pricing.Local.Amount)
	return &QueryResult{
		Found: true, Paid: paid, TradeNo: out.Data.Code,
		Amount: amount, Currency: strings.ToUpper(out.Data.Pricing.Local.Currency),
	}, nil
}

// verifySig：HMAC-SHA256(raw body) → hex，与 header 常数时间比较。
// 必须用接收到的原始 raw body，绝不 Unmarshal 后再 Marshal（字节变化即验签失败）。
func (c *CoinbaseCommerce) verifySig(rawBody []byte, sig string) bool {
	if c.WebhookSecret == "" || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.WebhookSecret))
	mac.Write(rawBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}
