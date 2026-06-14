package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Embeddings 模态（RAG 工作负载的高频入口）。
// 协议上是 OpenAI 兼容透传（请求/响应原样），只有 Azure 走 deployments 路径变体；
// 计费仅输入侧：hold 按 tiktoken 估算输入，capture 按上游 usage.prompt_tokens。
// 复用全部既有设施：渠道选择/错误分类/自动禁用/重试/SettleHoldExact/调用记录。

var errEmbeddingsUnsupported = fmt.Errorf("relay: provider does not serve openai-compatible embeddings")

// buildEmbeddingsRequest：embeddings 上游请求（透传 body）。
// anthropic/gemini 无 OpenAI 兼容 embeddings 端点 → 明确报错（不静默 404 浪费重试）。
func buildEmbeddingsRequest(ctx context.Context, ch *Channel, credential string, rawBody []byte, model string) (*http.Request, error) {
	adapterName := ch.Provider
	if p, ok := ProfileFor(ch.Provider); ok {
		adapterName = p.Adapter
	}
	var url string
	authHeader, authValue := "Authorization", "Bearer "+credential
	switch adapterName {
	case "azure":
		url = strings.TrimRight(ch.BaseURL, "/") +
			"/openai/deployments/" + model + "/embeddings?api-version=" + AzureAPIVersion
		authHeader, authValue = "api-key", credential
	case "anthropic", "gemini":
		return nil, errEmbeddingsUnsupported
	default: // openai 兼容（含 volcengine /api/v3/embeddings）
		url = strings.TrimRight(ch.BaseURL, "/") + "/embeddings"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authHeader, authValue)
	return req, nil
}

// countEmbeddingsTokens：输入 token 估算（string | []string | token 数组）。
func countEmbeddingsTokens(input any, rawLen int) int64 {
	e := encoder()
	if e == nil {
		return EstimateTokens(rawLen)
	}
	switch v := input.(type) {
	case string:
		n := countText(e, v)
		if n < 1 {
			n = 1
		}
		return n
	case []any:
		var total int64
		for _, item := range v {
			switch x := item.(type) {
			case string:
				total += countText(e, x)
			case float64: // 已是 token id 数组：一个元素 = 一个 token
				total++
				_ = x
			default:
				if b, err := json.Marshal(item); err == nil {
					total += EstimateTokens(len(b))
				}
			}
		}
		if total < 1 {
			total = 1
		}
		return total
	default:
		return EstimateTokens(rawLen)
	}
}

func (r *Relay) HandleEmbeddings(c *gin.Context) {
	t0 := r.Now()
	rdb := r.DB.WithContext(c.Request.Context()) // 纯读预检绑 ctx（审查 M-6）

	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(rdb, strings.TrimPrefix(auth, "Bearer "), r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return
	}

	inv := &invocation.Invocation{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		RequestID: newRequestID(),
	}
	c.Header("X-Request-Id", inv.RequestID)
	finish := func(status string, code int, brief string) {
		if r.Rec == nil {
			return
		}
		inv.Status, inv.StatusCode, inv.ErrorBrief = status, code, brief
		inv.DurationMs = int(r.Now().Sub(t0).Milliseconds())
		r.Rec.Record(inv)
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	var req struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" || req.Input == nil {
		relayErr(c, http.StatusBadRequest, "model and input are required")
		return
	}
	if code, msg, ok := keyGate(key, c.ClientIP(), req.Model); !ok { // Key 精细管控（0012）
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, req.Model); !ok { // 模型级限流（gap #2）
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	inv.Model = req.Model

	price, err := GetPrice(rdb, req.Model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", req.Model))
		return
	}

	estIn := countEmbeddingsTokens(req.Input, len(body))
	groupRatio := GroupRatio(rdb, key.OrgID) // 分组倍率（0011），hold/capture 同值
	holdAmount := price.Cost(estIn, 0, 0).Mul(groupRatio)
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, key.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		h, _, err := ledger.CreateHold(tx, ledger.HoldInput{
			IdempotencyKey: fmt.Sprintf("emb:%d:%d", key.ID, r.Now().UnixNano()),
			AccountID:      acc.ID,
			Amount:         holdAmount,
			RefType:        "usage",
			RefID:          req.Model,
		})
		hold = h
		return err
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "insufficient balance")
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return
		}
		r.Log.Error("embeddings hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return
	}

	// 换渠道重试循环（与 chat 同款分类驱动）
	var resp *http.Response
	var ch *Channel
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var pickErr error
		ch, pickErr = PickChannelForAttempt(rdb, req.Model, attempt, exclude, r.healthFactor)
		if pickErr != nil {
			break
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
		if err != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		upBody, upModel := mappedRawBody(ch, body, req.Model) // 模型映射（0009）
		upReq, err := buildEmbeddingsRequest(c.Request.Context(), ch, credential, upBody, upModel)
		if err != nil {
			r.release(hold.ID)
			if err == errEmbeddingsUnsupported {
				finish(invocation.StatusUpstreamError, http.StatusBadRequest, "provider lacks embeddings")
				relayErr(c, http.StatusBadRequest,
					fmt.Sprintf("provider %q does not support embeddings", ch.Provider))
				return
			}
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return
		}
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			r.Log.Warn("embeddings upstream unreachable", "channel", ch.ID, "err", err)
			r.observe(ch.ID, false) // 健康分：网络不可达
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusBadGateway, []byte(`{"error":{"message":"upstream unreachable"}}`)
			resp = nil
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			r.enableChannelIfDisabled(ch)
			break
		}
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		disp := classifyUpstreamError(ch.Provider, resp.StatusCode, errBody)
		if disp.Retryable || disp.DisableChannel {
			r.observe(ch.ID, false) // 健康分：渠道自身可靠性失败
		}
		r.Log.Warn("embeddings upstream error", "channel", ch.ID, "status", resp.StatusCode,
			"disable", disp.DisableChannel, "retryable", disp.Retryable)
		if disp.DisableChannel {
			r.noteFailure(ch.ID, disp.Reason)
		}
		lastStatus, lastBody = resp.StatusCode, errBody
		resp = nil
		if !disp.Retryable {
			break
		}
		exclude[ch.ID] = true
	}
	if resp == nil {
		r.release(hold.ID)
		if lastStatus == 0 {
			finish(invocation.StatusNoChannel, http.StatusServiceUnavailable, "no upstream available")
			relayErr(c, http.StatusServiceUnavailable, "no upstream available for this model")
			return
		}
		finish(invocation.StatusUpstreamError, lastStatus, fmt.Sprintf("upstream status %d after %d attempts", lastStatus, inv.Attempts))
		c.Data(lastStatus, "application/json", lastBody)
		return
	}
	defer resp.Body.Close()

	if ch.EnforcesStrictResponse() && !IsLLMContentType(resp.Header.Get("Content-Type")) {
		r.release(hold.ID)
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream returned invalid response type")
		relayErr(c, http.StatusBadGateway, "upstream returned an invalid response (check channel base_url)")
		return
	}
	if rid := resp.Header.Get("x-request-id"); rid != "" {
		inv.UpstreamRequestID = rid
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		r.release(hold.ID)
		relayErr(c, http.StatusBadGateway, "upstream read failed")
		return
	}
	inv.FRTMs = int(r.Now().Sub(t0).Milliseconds())

	var parsed struct {
		Usage struct {
			PromptTokens int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &parsed)
	inTokens := parsed.Usage.PromptTokens
	inv.UsageSource = invocation.UsageSourceUpstream
	if inTokens == 0 {
		inTokens = estIn // 兜底估算（宁可少收不多收的同款纪律）
		inv.UsageSource = invocation.UsageSourceEstimated
		r.Log.Warn("embeddings usage missing from upstream, estimated", "model", req.Model)
	}

	c.Data(http.StatusOK, "application/json", raw)

	actual := price.Cost(inTokens, 0, 0).Mul(groupRatio)
	defer func() { _ = identity.AddKeyUsage(r.DB, key.ID, actual) }() // per-key 已用额度（0012）
	reason := fmt.Sprintf("embeddings model=%s in=%d ×%s", req.Model, inTokens, groupRatio)
	var overage = decimal.Zero
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		overage, err = ledger.SettleHoldExact(tx, hold.ID, actual, "usage", req.Model, reason)
		return err
	}); err != nil {
		r.Log.Error("EMBEDDINGS SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
	if overage.IsPositive() {
		r.Log.Warn("embeddings overage beyond hold", "overage", overage)
	}

	inv.PromptTokens, inv.CostUSD = inTokens, actual
	finish(invocation.StatusSuccess, http.StatusOK, "")
}
