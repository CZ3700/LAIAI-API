package payment

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// EPay（易支付/上海富友等彩虹系）：CNY-only，MD5 签名。
// 移植旧平台血泪：创建用 submit.php，回调验签字段方言——
// **回调用 trade_status，查询用 status**（同 vendor 两 API 字段名不同，文档不写）。
type EPay struct {
	PID    string // 商户 ID
	Key    string // 商户密钥
	APIURL string // 网关地址，如 https://pay.example.com
	Client *http.Client
}

func (e *EPay) Name() string { return "epay" }

func (e *EPay) httpClient() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (e *EPay) CreatePayment(_ context.Context, req *CreateRequest) (*CreateResult, error) {
	if req.Currency != "" && !strings.EqualFold(req.Currency, "CNY") {
		return nil, fmt.Errorf("epay only supports CNY, got %s", req.Currency)
	}
	payType := "alipay"
	switch req.PayMethod {
	case "wechat", "wxpay":
		payType = "wxpay"
	case "qqpay":
		payType = "qqpay"
	case "bank":
		payType = "bank"
	}
	params := map[string]string{
		"pid":          e.PID,
		"type":         payType,
		"out_trade_no": req.OrderNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         req.Description,
		"money":        req.Amount.StringFixed(2),
	}
	params["sign"] = epaySign(params, e.Key)
	params["sign_type"] = "MD5"
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	return &CreateResult{
		PayURL:      strings.TrimRight(e.APIURL, "/") + "/submit.php?" + v.Encode(),
		ProviderRef: req.OrderNo,
	}, nil
}

func (e *EPay) VerifyCallback(rawBody []byte, _, query map[string]string) (*CallbackResult, error) {
	params := url.Values{}
	for k, v := range query {
		params.Set(k, v)
	}
	if len(rawBody) > 0 {
		if form, err := url.ParseQuery(string(rawBody)); err == nil {
			for k, v := range form {
				if len(v) > 0 {
					params.Set(k, v[0])
				}
			}
		}
	}
	got := params.Get("sign")
	if got == "" {
		return nil, fmt.Errorf("missing sign")
	}
	if !strings.EqualFold(got, epaySignFromValues(params, e.Key)) {
		return nil, fmt.Errorf("signature mismatch")
	}
	amount, _ := decimal.NewFromString(params.Get("money"))
	return &CallbackResult{
		OrderNo:  params.Get("out_trade_no"),
		TradeNo:  params.Get("trade_no"),
		Amount:   amount,
		Currency: "CNY",
		Success:  params.Get("trade_status") == "TRADE_SUCCESS", // 回调用 trade_status（方言）
	}, nil
}

// AckBody：EPay 要求回调成功后回纯文本 "success"。
func (e *EPay) AckBody() string { return "success" }

// QueryOrder：彩虹易支付查询 API `api.php?act=order`（红线 #5 对账兜底）。
// **字段方言**：查询用 `status`（1=已支付/0=未支付），与回调的 `trade_status` 不同——
// 这是旧平台 reconcile 第一次跑动才暴露的坑，单元测试 fixture 必须用真实 raw response。
// 查询凭 pid+key 明文鉴权（key 即凭证，无需另签）；按 out_trade_no(=我方 order_no) 查。
func (e *EPay) QueryOrder(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	q := url.Values{}
	q.Set("act", "order")
	q.Set("pid", e.PID)
	q.Set("key", e.Key)
	q.Set("out_trade_no", req.OrderNo)
	endpoint := strings.TrimRight(e.APIURL, "/") + "/api.php?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("epay query: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("epay query http %d", resp.StatusCode)
	}
	// status/code 用 json.Number 容错（不同网关回字符串或数字）
	var out struct {
		Code    json.Number `json:"code"`
		Status  json.Number `json:"status"` // 方言：1=已支付
		TradeNo string      `json:"trade_no"`
		Money   string      `json:"money"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("epay query decode: %w", err)
	}
	if out.Code.String() != "1" { // code!=1：查询失败/订单不存在
		return &QueryResult{Found: false}, nil
	}
	amount, _ := decimal.NewFromString(out.Money)
	return &QueryResult{
		Found:    true,
		Paid:     out.Status.String() == "1",
		TradeNo:  out.TradeNo,
		Amount:   amount,
		Currency: "CNY",
	}, nil
}

// epaySign：按 key 字典序拼 k=v&...+商户密钥 取 MD5（排除 sign/sign_type/空值）。
func epaySign(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" || k == "sign_type" || params[k] == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+params[k])
	}
	return fmt.Sprintf("%x", md5.Sum([]byte(strings.Join(pairs, "&")+key)))
}

func epaySignFromValues(params url.Values, key string) string {
	m := make(map[string]string, len(params))
	for k := range params {
		m[k] = params.Get(k)
	}
	return epaySign(m, key)
}
