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

// 图像生成模态（按张计价——Catalog 多单位计费第一例，设计 §4.3）。
// 协议 OpenAI 兼容透传（dall-e/gpt-image/CogView/SiliconFlow/方舟 images 均同形态）。
// 资金语义与 token 同构：hold = 请求张数 × 单价；capture = 实际返回张数 × 单价——
// "请求 4 张实出 1 张"的差额自动解冻，多出（不应发生）走 overage。

var errImagesUnsupported = fmt.Errorf("relay: provider does not serve openai-compatible image generation")

func buildImagesRequest(ctx context.Context, ch *Channel, credential string, rawBody []byte, model string) (*http.Request, error) {
	adapterName := ch.Provider
	if p, ok := ProfileFor(ch.Provider); ok {
		adapterName = p.Adapter
	}
	var url string
	authHeader, authValue := "Authorization", "Bearer "+credential
	switch adapterName {
	case "azure":
		url = strings.TrimRight(ch.BaseURL, "/") +
			"/openai/deployments/" + model + "/images/generations?api-version=" + AzureAPIVersion
		authHeader, authValue = "api-key", credential
	case "anthropic", "gemini":
		return nil, errImagesUnsupported
	default: // openai 兼容（含方舟 /api/v3/images/generations）
		url = strings.TrimRight(ch.BaseURL, "/") + "/images/generations"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authHeader, authValue)
	return req, nil
}

func (r *Relay) HandleImages(c *gin.Context) {
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

	// Entitlements 功能门（P0-2）：超管可按 org 关闭图像生成。
	if ent, eerr := identity.LoadEntitlements(rdb, key.OrgID); eerr == nil && !ent.FeatureImages {
		finish("forbidden", http.StatusForbidden, "image generation disabled")
		relayErr(c, http.StatusForbidden, "image generation is not enabled for this organization")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
		N      int    `json:"n"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" || req.Prompt == "" {
		relayErr(c, http.StatusBadRequest, "model and prompt are required")
		return
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > 10 { // OpenAI 上限同款；防按张 hold 被恶意撑爆
		relayErr(c, http.StatusBadRequest, "n must be between 1 and 10")
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

	price, err := r.cachedPrice(req.Model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", req.Model))
		return
	}
	if !price.ImageUSD.IsPositive() {
		relayErr(c, http.StatusBadRequest,
			fmt.Sprintf("model %q is not priced for image generation", req.Model))
		return
	}

	groupRatio := r.cachedRatio(key.OrgID, req.Model) // 一层+二层倍率（0011/0027），hold/capture 同值
	holdAmount := price.ImageUSD.Mul(decimal.NewFromInt(int64(n))).Mul(groupRatio)
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		// 分片感知 hold：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），分片后按 RequestID 散列打散写热点。
		h, _, err := ledger.CreateHoldSharded(tx, key.OrgID, ledger.AccountUserCredit, inv.RequestID, ledger.HoldInput{
			IdempotencyKey: "img:" + inv.RequestID, // 每请求唯一 16-hex（非时间戳，杜绝纳秒碰撞共用 hold）
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
		r.Log.Error("images hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return
	}
	// API Key 额度原子预留（P0-3）；失败由下方 defer 退还
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

	// 换渠道重试循环（同款分类驱动）
	var resp *http.Response
	var ch *Channel
	exclude := map[uint64]bool{}
	var lastStatus int
	var lastBody []byte
	channelSnap, snapErr := r.cachedChannels() // 取一次快照，重试复用（WS-2）
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
		credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
		if err != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", err)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		upBody, upModel := mappedRawBody(ch, body, req.Model) // 模型映射（0009）
		upReq, err := buildImagesRequest(c.Request.Context(), ch, credential, upBody, upModel)
		if err != nil {
			r.release(hold.ID)
			if err == errImagesUnsupported {
				finish(invocation.StatusUpstreamError, http.StatusBadRequest, "provider lacks image generation")
				relayErr(c, http.StatusBadRequest,
					fmt.Sprintf("provider %q does not support image generation", ch.Provider))
				return
			}
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return
		}
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			r.Log.Warn("images upstream unreachable", "channel", ch.ID, "err", err)
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
		r.Log.Warn("images upstream error", "channel", ch.ID, "status", resp.StatusCode,
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

	// 按实际返回张数结算（data 数组长度）；解析失败按请求张数收（保守且有日志）
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	actualN := n
	inv.UsageSource = invocation.UsageSourceUpstream
	if err := json.Unmarshal(raw, &parsed); err == nil && len(parsed.Data) > 0 {
		actualN = len(parsed.Data)
		if actualN > n {
			// 永不收超过用户请求的张数（审查 L-1：上游半可信，钳到 n 防恶意/异常 data 撑爆余额）
			actualN = n
		}
	} else {
		inv.UsageSource = invocation.UsageSourceEstimated
		r.Log.Warn("images response without data array, charged by requested n", "model", req.Model)
	}

	c.Data(http.StatusOK, "application/json", raw)

	actual := price.ImageUSD.Mul(decimal.NewFromInt(int64(actualN))).Mul(groupRatio)
	// 预留→实际对账（P0-3）：+(actual-hold) 落到真实 actual 并标记 settled
	defer func() {
		_ = identity.AddKeyUsageSigned(r.DB, key.ID, actual.Sub(holdAmount))
		quotaSettled = true
	}()
	reason := fmt.Sprintf("images model=%s n=%d ×%s", req.Model, actualN, groupRatio)
	var overage = decimal.Zero
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		overage, err = ledger.SettleHoldExact(tx, hold.ID, actual, "usage", req.Model, reason)
		return err
	}); err != nil {
		r.Log.Error("IMAGES SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
	if overage.IsPositive() {
		r.Log.Warn("images overage beyond hold", "overage", overage)
	}

	inv.CompletionTokens = int64(actualN) // 调用记录里张数记入 completion 槽位（语义：产出单位数）
	inv.CostUSD = actual
	finish(invocation.StatusSuccess, http.StatusOK, "")
}
