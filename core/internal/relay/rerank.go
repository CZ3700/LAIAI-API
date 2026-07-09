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
	"gorm.io/gorm"
)

// Rerank 模态（WS-1，RAG 重排序；对标 NewAPI /v1/rerank）。Jina/SiliconFlow/Cohere(v2)/Qwen 等
// 提供该端点，请求/响应形态高度一致：{model, query, documents[, top_n, return_documents]} →
// {results:[{index, relevance_score, document?}], usage|meta}。本实现是 OpenAI 风格透传 + 输入侧计费
// （hold 按 query+documents 估算，capture 按上游 usage.total_tokens，缺失则按估算——同 embeddings 纪律）。
// anthropic/gemini 无该端点 → 明确报错。复用渠道选择/重试/自动禁用/SettleHoldExact/调用记录全套。

var errRerankUnsupported = fmt.Errorf("relay: provider does not serve a rerank endpoint")

// rerankUpstreamURL：rerank 端点查 (format×mode) 矩阵（contract v2，mode.go——cohere 剥
// /compatibility/v1 归一原生 /v1/rerank 的方言也在表里）。anthropic/gemini 不支持。
// 返回 (url, authHeader, 完整 authValue, err)——保持既有调用方签名。
func rerankUpstreamURL(ch *Channel, credential string) (string, string, string, error) {
	spec, err := modeUpstreamSpec(ch, ModeRerank, "")
	if err != nil {
		return "", "", "", err
	}
	return spec.URL, spec.AuthHeader, spec.AuthPrefix + credential, nil
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
	inv := &invocation.Invocation{OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID, RequestID: newRequestID(), Modality: invocation.ModalityRerank}
	setClientMeta(inv, c)
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
	if code, msg, ok := r.gateKey(key, c.ClientIP(), req.Model); !ok {
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
		// 分片感知 hold：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），分片后按 RequestID 散列打散写热点。
		h, _, err := ledger.CreateHoldSharded(tx, key.OrgID, ledger.AccountUserCredit, inv.RequestID, ledger.HoldInput{
			IdempotencyKey: "rerank:" + inv.RequestID,
			Amount:         holdAmount, RefType: "usage", RefID: req.Model,
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

	// 换渠道重试（Adapter contract v2 第二期：统一 forwardWithFailover）
	build := func(ctx context.Context, ch *Channel, credential string) (*http.Request, error) {
		url, ah, av, uerr := rerankUpstreamURL(ch, credential)
		if uerr != nil {
			// 任意 URL 构造失败=该 provider 不支持 rerank→400（保真旧"uerr!=nil→400"语义，
			// 不依赖 mode.go"只返哨兵或 nil"不变量；对抗 review 记档的隐性耦合封堵）。
			return nil, errRerankUnsupported
		}
		upBody, _ := mappedRawBody(ch, body, req.Model) // 模型映射（0009）
		upReq, berr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(upBody))
		if berr != nil {
			return nil, berr
		}
		upReq.Header.Set("Content-Type", "application/json")
		upReq.Header.Set(ah, av)
		// 渠道级 header override（contract v2 作用域扩展）
		applyHeaderOverrideForMode(upReq, ch.HeaderOverride, credential, ModeRerank)
		return upReq, nil
	}
	resp, ch, aborted := r.forwardWithFailover(c, inv, finish, forwardOpts{
		model: req.Model, hold: hold, unsupported: errRerankUnsupported, modality: "rerank", logLabel: "rerank", build: build,
	})
	if aborted {
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
