// Package ark 实现火山方舟账号/接入点监控、分账对账、钱包熔断以及素材 URL→asset:// 转换引擎。
package ark

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/shopspring/decimal"
)

const (
	MaxRateLimitRetries        = 20
	DefaultRateLimitedConcur   = 50
	RateLimitThrottleWindow    = 10 * time.Second
	DefaultDeleteAssetGap      = 1 * time.Second
	DefaultArkAPIVersion       = "2024-01-01"
	DefaultBillingAPIVersion   = "2022-01-01"
	DefaultVolcRegion          = "cn-beijing"
	DefaultInternationalDomain = "byteplusapi.com"
	DefaultDomesticDomain      = "volcengineapi.com"
	maxResponseBodyBytes       = 4 << 20
	maxRateLimitJitterSteps    = 15
)

// ClientConfig 描述火山方舟 OpenAPI / 计费 / 素材调用凭证与域配置。
type ClientConfig struct {
	AccessKey       string
	SecretKey       string
	Region          string
	ProjectName     string
	AppID           string
	GroupID         string
	BaseURLOverride string
	ThrottleScope   string
}

// Client 封装带 Signature V4、自适应并发限流退避与 DeleteAsset 节流的火山方舟客户端。
type Client struct {
	cfg            ClientConfig
	http           *http.Client
	rateRetryScale time.Duration // 仅测试可覆盖，0 表示使用标准退避间隔
	deleteGap      time.Duration // 仅测试可覆盖，0 表示 1s
}

// NewClient 创建火山方舟客户端。
func NewClient(cfg ClientConfig, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	cfg.Region = cmp.Or(strings.TrimSpace(cfg.Region), DefaultVolcRegion)
	cfg.ProjectName = cmp.Or(strings.TrimSpace(cfg.ProjectName), "default")
	return &Client{
		cfg:       cfg,
		http:      httpClient,
		deleteGap: DefaultDeleteAssetGap,
	}
}

type adaptiveThrottle struct {
	sem             chan struct{}
	throttledUntilN atomic.Int64
}

var (
	throttleMu sync.Mutex
	throttles  = map[string]*adaptiveThrottle{}

	deletePaceMu sync.Mutex
)

// IsCreateAssetAction 判断是否为创建素材接口（仅该类写操作在限流后进入自适应并发排队）。
func IsCreateAssetAction(action string) bool {
	return action == "CreateAsset" ||
		action == "CreateAigcMaterial" ||
		(strings.HasPrefix(action, "POST ") && strings.HasSuffix(action, "/assets"))
}

func getOrCreateThrottle(scope string) *adaptiveThrottle {
	throttleMu.Lock()
	defer throttleMu.Unlock()
	t, ok := throttles[scope]
	if !ok {
		t = &adaptiveThrottle{sem: make(chan struct{}, DefaultRateLimitedConcur)}
		throttles[scope] = t
	}
	return t
}

// ActivateRateLimitThrottle 在触发限流时激活或顺延 10s 并发保护窗口。
func ActivateRateLimitThrottle(scope, action string) {
	if !IsCreateAssetAction(action) || strings.TrimSpace(scope) == "" {
		return
	}
	t := getOrCreateThrottle(scope)
	t.throttledUntilN.Store(time.Now().Add(RateLimitThrottleWindow).UnixNano())
}

// AcquireRateLimitPermitIfActive 若当前作用域处于限流窗口则排队等待槽位，否则零阻塞放行。
func AcquireRateLimitPermitIfActive(ctx context.Context, scope, action string) (func(), error) {
	if !IsCreateAssetAction(action) || strings.TrimSpace(scope) == "" {
		return func() {}, nil
	}
	throttleMu.Lock()
	t, ok := throttles[scope]
	throttleMu.Unlock()
	if !ok || time.Now().UnixNano() >= t.throttledUntilN.Load() {
		return func() {}, nil
	}
	select {
	case t.sem <- struct{}{}:
		return func() { <-t.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// IsAPIRateLimited 判断响应是否属于限流错误。
func IsAPIRateLimited(status int, body string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "flow control") ||
		strings.Contains(lower, "flowcontrol") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "ratelimit") ||
		strings.Contains(lower, "request limit") ||
		strings.Contains(lower, "limitexceeded")
}

// IsNotFound 判断错误信息是否表示目标素材已不存在。
func IsNotFound(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	return strings.Contains(lower, "notfound") || strings.Contains(lower, "404 not found")
}

// IsGroupNotFoundErr 判断错误是否为 GroupId 不存在/失效。
func IsGroupNotFoundErr(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	return strings.Contains(lower, "group") &&
		(strings.Contains(lower, "notfound") ||
			strings.Contains(lower, "not found") ||
			strings.Contains(lower, "invalidparameter") ||
			strings.Contains(lower, "does not exist"))
}

// IsResponseBusinessFailed 判断 2xx 响应体是否包含业务级错误信封。
func IsResponseBusinessFailed(raw []byte) bool {
	var env struct {
		ResponseMetadata *struct {
			Error json.RawMessage `json:"Error"`
		} `json:"ResponseMetadata"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	if env.ResponseMetadata != nil && len(bytes.TrimSpace(env.ResponseMetadata.Error)) > 0 && string(bytes.TrimSpace(env.ResponseMetadata.Error)) != "null" {
		return true
	}
	return len(bytes.TrimSpace(env.Error)) > 0 && string(bytes.TrimSpace(env.Error)) != "null"
}

func cryptoRandRange(minVal, maxVal int64) int64 {
	if maxVal <= minVal {
		return minVal
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return minVal
	}
	span := uint64(maxVal - minVal + 1)
	return minVal + int64(binary.BigEndian.Uint64(buf[:])%span)
}

func (c *Client) sleepRateLimitJitter(ctx context.Context, attempt int) error {
	if c.rateRetryScale > 0 {
		timer := time.NewTimer(c.rateRetryScale)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	step := min(int64(attempt), maxRateLimitJitterSteps)
	minMS := 2000 + step*250
	maxMS := 5000 + step*600
	sleepMS := cryptoRandRange(minMS, maxMS)
	timer := time.NewTimer(time.Duration(sleepMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunDeleteAssetPaced 全局串行节流执行 DeleteAsset，每次结束后休眠 DeleteAssetGap。
func RunDeleteAssetPaced(ctx context.Context, gap time.Duration, fn func(context.Context)) error {
	deletePaceMu.Lock()
	defer deletePaceMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fn(ctx)
	if gap <= 0 {
		gap = DefaultDeleteAssetGap
	}
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) buildOpenAPIURL(service, region, action, version string) (fullURL, host, rawQuery string, err error) {
	q := url.Values{}
	q.Set("Action", action)
	q.Set("Version", version)
	rawQuery = "Action=" + url.QueryEscape(action) + "&Version=" + url.QueryEscape(version)

	if strings.TrimSpace(c.cfg.BaseURLOverride) != "" {
		u, perr := url.Parse(strings.TrimRight(strings.TrimSpace(c.cfg.BaseURLOverride), "/"))
		if perr != nil || u.Scheme == "" || u.Host == "" {
			return "", "", "", fmt.Errorf("ark: invalid base_url_override %q", c.cfg.BaseURLOverride)
		}
		if u.Path == "" {
			u.Path = "/"
		}
		u.RawQuery = rawQuery
		return u.String(), u.Host, rawQuery, nil
	}

	domain := DefaultDomesticDomain
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(region)), "ap-") {
		domain = DefaultInternationalDomain
	}
	if service == "ark" || service == "billing" {
		host = "open." + domain
	} else {
		host = service + "." + domain
	}
	return fmt.Sprintf("https://%s/?%s", host, rawQuery), host, rawQuery, nil
}

// CallAPI 调用火山引擎 Signature V4 OpenAPI 并解析 Result 或完整响应。
func (c *Client) CallAPI(
	ctx context.Context,
	method string,
	service string,
	region string,
	action string,
	version string,
	body any,
	out any,
) ([]byte, int, error) {
	reg := cmp.Or(strings.TrimSpace(region), c.cfg.Region, DefaultVolcRegion)
	fullURL, host, _, err := c.buildOpenAPIURL(service, reg, action, version)
	if err != nil {
		return nil, 0, err
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("ark: marshal %s request: %w", action, err)
		}
	}
	scope := cmp.Or(c.cfg.ThrottleScope, c.cfg.AccessKey)

	var status int
	var respBytes []byte
	for attempt := 0; attempt <= MaxRateLimitRetries; attempt++ {
		release, perr := AcquireRateLimitPermitIfActive(ctx, scope, action)
		if perr != nil {
			return nil, 0, perr
		}

		var bodyReader io.Reader
		if len(payload) > 0 {
			bodyReader = bytes.NewReader(payload)
		}
		req, rerr := http.NewRequestWithContext(ctx, cmp.Or(method, http.MethodPost), fullURL, bodyReader)
		if rerr != nil {
			release()
			return nil, 0, rerr
		}
		req.Host = host
		if serr := forward.SignVolcengineV4(req, c.cfg.AccessKey, c.cfg.SecretKey, service, reg, payload, time.Now()); serr != nil {
			release()
			return nil, 0, serr
		}

		resp, derr := c.http.Do(req)
		if derr != nil {
			release()
			return nil, 0, derr
		}
		respBytes, rerr = io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
		_ = resp.Body.Close()
		status = resp.StatusCode
		release()
		if rerr != nil {
			return nil, status, rerr
		}

		if status < 200 || status >= 300 {
			if service == "ark" && attempt < MaxRateLimitRetries && IsAPIRateLimited(status, string(respBytes)) {
				ActivateRateLimitThrottle(scope, action)
				if jerr := c.sleepRateLimitJitter(ctx, attempt+1); jerr != nil {
					return respBytes, status, jerr
				}
				continue
			}
			return respBytes, status, fmt.Errorf("volcengine API %s error %d: %s", action, status, strings.TrimSpace(string(respBytes)))
		}
		break
	}

	var envelope struct {
		ResponseMetadata *struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
		Result json.RawMessage `json:"Result"`
	}
	if uerr := json.Unmarshal(respBytes, &envelope); uerr == nil {
		if envelope.ResponseMetadata != nil && envelope.ResponseMetadata.Error != nil {
			return respBytes, status, fmt.Errorf("volcengine %s failed (%s): %s",
				action, envelope.ResponseMetadata.Error.Code, envelope.ResponseMetadata.Error.Message)
		}
	}

	if out != nil {
		if len(bytes.TrimSpace(envelope.Result)) > 0 && string(bytes.TrimSpace(envelope.Result)) != "null" {
			if uerr := json.Unmarshal(envelope.Result, out); uerr == nil {
				return respBytes, status, nil
			}
		}
		if uerr := json.Unmarshal(respBytes, out); uerr != nil {
			return respBytes, status, fmt.Errorf("ark: decode %s response: %w", action, uerr)
		}
	}
	return respBytes, status, nil
}

// BalanceInfo 火山引擎账户资金详情。
type BalanceInfo struct {
	AccountID        *int64  `json:"account_id,omitempty"`
	AvailableBalance float64 `json:"available_balance"`
	CashBalance      float64 `json:"cash_balance"`
	FreezeAmount     float64 `json:"freeze_amount"`
	ArrearsBalance   float64 `json:"arrears_balance"`
	CreditLimit      float64 `json:"credit_limit"`
}

func parseFlexibleFloat(raw map[string]any, key string) float64 {
	v, ok := raw[key]
	if !ok || v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(val), 64)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}

// QueryBalanceDetail 查询火山引擎账户余额详情 (QueryBalanceAcct 2022-01-01)。
func (c *Client) QueryBalanceDetail(ctx context.Context) (*BalanceInfo, error) {
	var result map[string]any
	if _, _, err := c.CallAPI(ctx, http.MethodGet, "billing", DefaultVolcRegion, "QueryBalanceAcct", DefaultBillingAPIVersion, nil, &result); err != nil {
		return nil, err
	}
	cash := parseFlexibleFloat(result, "CashBalance")
	avail := parseFlexibleFloat(result, "AvailableBalance")
	if avail == 0 && cash != 0 {
		avail = cash
	}
	info := &BalanceInfo{
		AvailableBalance: avail,
		CashBalance:      cash,
		FreezeAmount:     parseFlexibleFloat(result, "FreezeAmount"),
		ArrearsBalance:   parseFlexibleFloat(result, "ArrearsBalance"),
		CreditLimit:      parseFlexibleFloat(result, "CreditLimit"),
	}
	if idRaw, ok := result["AccountID"]; ok {
		switch v := idRaw.(type) {
		case float64:
			id := int64(v)
			info.AccountID = &id
		case string:
			if id, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil {
				info.AccountID = &id
			}
		}
	}
	return info, nil
}

// SplitBillItem 表示火山引擎 ListSplitBillDetail 返回的一条分账账单明细。
type SplitBillItem struct {
	BillPeriod    string          `json:"BillPeriod"`
	ExpenseTime   string          `json:"ExpenseTime"`
	Product       string          `json:"Product"`
	InstanceNo    string          `json:"InstanceNo"`
	Count         float64         `json:"Count"`
	PayableAmount decimal.Decimal `json:"PayableAmount"`
	Raw           map[string]any  `json:"-"`
}

// ListSplitBillDetail 拉取指定账期的火山方舟分账明细 (ListSplitBillDetail 2022-01-01)。
func (c *Client) ListSplitBillDetail(ctx context.Context, billPeriod string, limit, offset int) ([]SplitBillItem, int, error) {
	if limit <= 0 {
		limit = 100
	}
	reqBody := map[string]any{
		"BillPeriod": billPeriod,
		"Limit":      limit,
		"Offset":     offset,
	}
	var raw map[string]any
	if _, _, err := c.CallAPI(ctx, http.MethodPost, "billing", DefaultVolcRegion, "ListSplitBillDetail", DefaultBillingAPIVersion, reqBody, &raw); err != nil {
		return nil, 0, err
	}
	total := int(parseFlexibleFloat(raw, "Total"))
	listRaw, _ := raw["List"].([]any)
	items := make([]SplitBillItem, 0, len(listRaw))
	for _, entry := range listRaw {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		item := SplitBillItem{
			BillPeriod:  strField(m, "BillPeriod", "bill_period"),
			ExpenseTime: strField(m, "ExpenseTime", "UseTime", "ExpenseBeginTime", "expense_time"),
			Product:     strField(m, "Product", "ProductZh", "Element"),
			InstanceNo:  strField(m, "InstanceNo", "SplitItemID", "ResourceID"),
			Count:       parseFlexibleFloat(m, "Count"),
			Raw:         m,
		}
		if item.Count == 0 {
			item.Count = parseFlexibleFloat(m, "count")
		}
		for _, pk := range []string{"PayableAmount", "DiscountBizPreferentialPrice", "OriginalBillAmount", "payable_amount"} {
			if s := strField(m, pk); s != "" {
				if d, derr := decimal.NewFromString(s); derr == nil {
					item.PayableAmount = d
					break
				}
			}
			if f := parseFlexibleFloat(m, pk); f != 0 {
				item.PayableAmount = decimal.NewFromFloat(f)
				break
			}
		}
		items = append(items, item)
	}
	return items, total, nil
}

// StopEndpoint 调用方舟 StopEndpoint 熔断停用接入点。
func (c *Client) StopEndpoint(ctx context.Context, endpointID string) error {
	body := map[string]any{"Id": strings.TrimSpace(endpointID)}
	_, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "StopEndpoint", DefaultArkAPIVersion, body, nil)
	return err
}

// StartEndpoint 调用方舟 StartEndpoint 恢复接入点。
func (c *Client) StartEndpoint(ctx context.Context, endpointID string) error {
	body := map[string]any{"Id": strings.TrimSpace(endpointID)}
	_, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "StartEndpoint", DefaultArkAPIVersion, body, nil)
	return err
}

// CreateAssetGroup 调用方舟 CreateAssetGroup。
func (c *Client) CreateAssetGroup(ctx context.Context, name, description string) (string, error) {
	body := map[string]any{
		"Name":        ClampArkName(name),
		"Description": description,
		"GroupType":   "AIGC",
	}
	if c.cfg.ProjectName != "" {
		body["ProjectName"] = c.cfg.ProjectName
	}
	var res struct {
		Id string `json:"Id"`
	}
	if _, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "CreateAssetGroup", DefaultArkAPIVersion, body, &res); err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Id) == "" {
		return "", errors.New("ark: CreateAssetGroup response missing Id")
	}
	return res.Id, nil
}

// CreateAsset 调用方舟 CreateAsset 注册公网媒体为素材 ID。
func (c *Client) CreateAsset(ctx context.Context, groupID, mediaURL, assetType, name string, skipModeration bool) (string, error) {
	body := map[string]any{
		"GroupId":   groupID,
		"URL":       mediaURL,
		"AssetType": assetType,
		"Name":      ClampCreateAssetName(cmp.Or(name, DeriveCreateAssetName(mediaURL, assetType))),
	}
	if c.cfg.ProjectName != "" {
		body["ProjectName"] = c.cfg.ProjectName
	}
	if skipModeration {
		body["Moderation"] = map[string]any{"Strategy": "Skip"}
	}
	var res struct {
		Id string `json:"Id"`
	}
	if _, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "CreateAsset", DefaultArkAPIVersion, body, &res); err != nil {
		return "", err
	}
	if strings.TrimSpace(res.Id) == "" {
		return "", errors.New("ark: CreateAsset response missing Id")
	}
	return res.Id, nil
}

// AssetStatus 表示 GetAsset 查询到的方舟素材状态。
type AssetStatus struct {
	Id         string `json:"Id"`
	Status     string `json:"Status"`
	FailCode   string `json:"FailCode"`
	FailReason string `json:"FailReason"`
	Error      *struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	} `json:"Error"`
	GroupID   string `json:"GroupId"`
	AssetType string `json:"AssetType"`
	URL       string `json:"URL"`
}

// GetAsset 调用方舟 GetAsset 查询素材处理状态。
func (c *Client) GetAsset(ctx context.Context, assetID string) (*AssetStatus, error) {
	body := map[string]any{"Id": NormalizeArkAssetID(assetID)}
	if c.cfg.ProjectName != "" {
		body["ProjectName"] = c.cfg.ProjectName
	}
	var res AssetStatus
	if _, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "GetAsset", DefaultArkAPIVersion, body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DeleteAsset 调用方舟 DeleteAsset 删除云端素材，404 视为幂等成功。
func (c *Client) DeleteAsset(ctx context.Context, assetID string) error {
	cleanID := NormalizeArkAssetID(assetID)
	if cleanID == "" {
		return nil
	}
	body := map[string]any{"Id": cleanID}
	if c.cfg.ProjectName != "" && c.cfg.ProjectName != "default" {
		body["ProjectName"] = c.cfg.ProjectName
	}
	_, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "DeleteAsset", DefaultArkAPIVersion, body, nil)
	if err != nil && IsNotFound(err.Error()) {
		return nil
	}
	return err
}

// GetModerationResult 查询素材或任务的风控审核拦截原因 (GetModerationResult 2024-01-01)。
func (c *Client) GetModerationResult(ctx context.Context, id, idType string) (json.RawMessage, error) {
	body := map[string]any{
		"Id":   strings.TrimSpace(id),
		"Type": cmp.Or(strings.TrimSpace(idType), "asset_id"),
	}
	raw, _, err := c.CallAPI(ctx, http.MethodPost, "ark", c.cfg.Region, "GetModerationResult", DefaultArkAPIVersion, body, nil)
	return raw, err
}

// RemoteVideoTask 表示从方舟 ListVideos API 拉取的单个视频任务快照。
type RemoteVideoTask struct {
	TaskID      string
	Model       string
	Status      string
	Duration    float64
	Resolution  string
	CreatedTime string
	TotalTokens int64
	RawJSON     string
}

// PullEndpointVideoTasks 调用方舟 ListVideos API (`GET /api/v3/contents/generations/tasks`) 拉取接入点任务。
func (c *Client) PullEndpointVideoTasks(ctx context.Context, arkBaseURL, apiKey, endpointID string, pageNum, pageSize int) ([]RemoteVideoTask, error) {
	base := cmp.Or(strings.TrimRight(strings.TrimSpace(arkBaseURL), "/"), "https://ark.cn-beijing.volces.com")
	if pageNum <= 0 {
		pageNum = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	u, err := forward.JoinUpstreamURL(base, "/api/v3/contents/generations/tasks")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("page_num", strconv.Itoa(pageNum))
	q.Set("page_size", strconv.Itoa(pageSize))
	if strings.TrimSpace(endpointID) != "" {
		q.Set("filter.model", strings.TrimSpace(endpointID))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	forward.ApplyAuth(req, forward.AuthBearer, apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ark ListVideos error %d: %s", resp.StatusCode, strings.TrimSpace(string(rawBytes)))
	}
	var payload map[string]any
	if err := json.Unmarshal(rawBytes, &payload); err != nil {
		return nil, err
	}
	itemsRaw, _ := payload["items"].([]any)
	if len(itemsRaw) == 0 {
		if dataArr, ok := payload["data"].([]any); ok {
			itemsRaw = dataArr
		}
	}
	out := make([]RemoteVideoTask, 0, len(itemsRaw))
	for _, entry := range itemsRaw {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		itemBytes, _ := json.Marshal(m)
		var totalTokens int64
		if usage, ok := m["usage"].(map[string]any); ok {
			totalTokens = int64(math.Round(parseFlexibleFloat(usage, "total_tokens")))
			if totalTokens == 0 {
				totalTokens = int64(math.Round(parseFlexibleFloat(usage, "completion_tokens")))
			}
		}
		createdStr := strField(m, "created_at", "created_time", "CreatedAt")
		if createdStr == "" {
			if f := parseFlexibleFloat(m, "created_at"); f > 0 {
				createdStr = strconv.FormatInt(int64(f), 10)
			}
		}
		out = append(out, RemoteVideoTask{
			TaskID:      strField(m, "id", "task_id"),
			Model:       strField(m, "model"),
			Status:      strings.ToLower(strField(m, "status")),
			Duration:    parseFlexibleFloat(m, "duration"),
			Resolution:  strField(m, "resolution"),
			CreatedTime: createdStr,
			TotalTokens: totalTokens,
			RawJSON:     string(itemBytes),
		})
	}
	return out, nil
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
