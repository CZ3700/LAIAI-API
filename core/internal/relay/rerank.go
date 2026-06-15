package relay

import (
	"bytes"
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
	"gorm.io/gorm"
)

// Rerank 模态（WS-1，RAG 重排序；对标 NewAPI /v1/rerank）。Jina/SiliconFlow/Cohere(v2)/Qwen 等
// 提供该端点，请求/响应形态高度一致：{model, query, documents[, top_n, return_documents]} →
// {results:[{index, relevance_score, document?}], usage|meta}。本实现是 OpenAI 风格透传 + 输入侧计费
// （hold 按 query+documents 估算，capture 按上游 usage.total_tokens，缺失则按估算——同 embeddings 纪律）。
// anthropic/gemini 无该端点 → 明确报错。复用渠道选择/重试/自动禁用/SettleHoldExact/调用记录全套。

var errRerankUnsupported = fmt.Errorf("relay: provider does not serve a rerank endpoint")

// rerankUpstreamURL：base + /rerank（渠道 base_url 须指向供应商的 rerank 基址，如 Jina/SiliconFlow 的
// /v1、Cohere 的 /v2）。anthropic/gemini 不支持。
func rerankUpstreamURL(ch *Channel, credential string) (string, string, string, error) {
	adapterName := ch.Provider
	if p, ok := ProfileFor(ch.Provider); ok {
		adapterName = p.Adapter
	}
	switch adapterName {
	case "anthropic", "gemini":
		return "", "", "", errRerankUnsupported
	default:
		return strings.TrimRight(ch.BaseURL, "/") + "/rerank", "Authorization", "Bearer " + credential, nil
	}
}

// countRerankTokens：query + 各 document 的 token 估算（无 tiktoken 时按原始字节估）。
func countRerankTokens(query string, documents []any, rawLen int) int64 {
	e := encoder()
	if e == nil {
		return EstimateTokens(rawLen)
	}
	total := countText(e, query)
	for _, d := range documents {
		switch x := d.(type) {
		case string:
			total += countText(e, x)
		default:
			if b, err := json.Marshal(d); err == nil {
				total += EstimateTokens(len(b))
			}
		}
	}
	if total < 1 {
		total = 1
	}
	return total
}

func (r *Relay) HandleRerank(c *gin.Context) {
	t0 := r.Now()
	rdb := r.DB.WithContext(c.Request.Context())

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
	inv := &invocation.Invocation{OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID, RequestID: newRequestID()}
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
		Model     string `json:"model"`
		Query     string `json:"query"`
		Documents []any  `json:"documents"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" || req.Query == "" || len(req.Documents) == 0 {
		relayErr(c, http.StatusBadRequest, "model, query and documents are required")
		return
	}
	if code, msg, ok := keyGate(key, c.ClientIP(), req.Model); !ok {
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, req.Model); !ok {
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	inv.Model = req.Model

	price, err := r.cachedPrice(req.Model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", req.Model))
		return
	}

	estIn := countRerankTokens(req.Query, req.Documents, len(body))
	groupRatio := r.cachedRatio(key.OrgID, req.Model)
	holdAmount := price.ChatCost(estIn, 0, 0).Mul(groupRatio)
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		acc, err := ledger.EnsureAccount(tx, key.OrgID, ledger.AccountUserCredit)
		if err != nil {
			return err
		}
		h, _, err := ledger.CreateHold(tx, ledger.HoldInput{
			IdempotencyKey: "rerank:" + inv.RequestID,
			AccountID:      acc.ID, Amount: holdAmount, RefType: "usage", RefID: req.Model,
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
		r.Log.Error("rerank hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return
	}
	if !identity.ReserveKeyQuota(r.DB, key.ID, holdAmount) {
		r.release(hold.ID)
		finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "api key quota exceeded")
		relayErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
		return
	}
	quotaSettled := false
	defer func() {
		if !quotaSettled {
			_ = identity.AddKeyUsageSigned(r.DB, key.ID, holdAmount.Neg())
		}
	}()

	var resp *http.Response
	var ch *Channel
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	channelSnap, snapErr := r.cachedChannels()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if snapErr != nil {
			break
		}
		var pickErr error
		ch, pickErr = pickFromSnapshot(channelSnap, req.Model, attempt, exclude, r.healthFactor)
		if pickErr != nil {
			break
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		credential, derr := r.Keyring.Decrypt(ch.CredentialEnc)
		if derr != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", derr)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		url, ah, av, uerr := rerankUpstreamURL(ch, credential)
		if uerr != nil {
			r.release(hold.ID)
			finish(invocation.StatusUpstreamError, http.StatusBadRequest, "provider lacks rerank")
			relayErr(c, http.StatusBadRequest, fmt.Sprintf("provider %q does not support rerank", ch.Provider))
			return
		}
		upBody, _ := mappedRawBody(ch, body, req.Model) // 模型映射（0009）
		upReq, berr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(upBody))
		if berr != nil {
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return
		}
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set(ah, av)
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			r.Log.Warn("rerank upstream unreachable", "channel", ch.ID, "err", err)
			r.observe(ch.ID, false)
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
			r.observe(ch.ID, false)
		}
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
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		r.release(hold.ID)
		relayErr(c, http.StatusBadGateway, "upstream read failed")
		return
	}
	inv.FRTMs = int(r.Now().Sub(t0).Milliseconds())

	// usage 解析：兼容 Jina/SiliconFlow 的 usage.total_tokens / usage.prompt_tokens 与
	// Cohere 的 meta.billed_units.search_units（无 token 时回退估算，宁少不多）。
	var parsed struct {
		Usage struct {
			TotalTokens  int64 `json:"total_tokens"`
			PromptTokens int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &parsed)
	inTokens := parsed.Usage.TotalTokens
	if inTokens == 0 {
		inTokens = parsed.Usage.PromptTokens
	}
	inv.UsageSource = invocation.UsageSourceUpstream
	if inTokens == 0 {
		inTokens = estIn
		inv.UsageSource = invocation.UsageSourceEstimated
		r.Log.Warn("rerank usage missing from upstream, estimated", "model", req.Model)
	}

	c.Data(http.StatusOK, "application/json", raw)

	actual := price.ChatCost(inTokens, 0, 0).Mul(groupRatio)
	defer func() {
		_ = identity.AddKeyUsageSigned(r.DB, key.ID, actual.Sub(holdAmount))
		quotaSettled = true
	}()
	reason := fmt.Sprintf("rerank model=%s in=%d ×%s", req.Model, inTokens, groupRatio)
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		_, e := ledger.SettleHoldExact(tx, hold.ID, actual, "usage", req.Model, reason)
		return e
	}); err != nil {
		r.Log.Error("RERANK SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
	inv.PromptTokens, inv.CostUSD = inTokens, actual
	finish(invocation.StatusSuccess, http.StatusOK, "")
}
