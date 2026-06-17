package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// D1-b：图生图（/v1/images/edits）与变体（/v1/images/variations）。multipart(image[+mask]+prompt+n+model)，
// 与 /images/generations 同款 ImageUSD 按张计价/hold/对账/换渠道重试；仅上游端点后缀与请求体（multipart）不同。

// imageUpstreamURL：拼接 images 端点（generations/edits/variations）。返回 (url, authHeader, authValuePrefix, err)。
func imageUpstreamURL(ch *Channel, model, suffix string) (string, string, string, error) {
	adapterName := ch.Provider
	if p, ok := ProfileFor(ch.Provider); ok {
		adapterName = p.Adapter
	}
	switch adapterName {
	case "azure":
		return strings.TrimRight(ch.BaseURL, "/") + "/openai/deployments/" + model + "/images/" + suffix +
			"?api-version=" + AzureAPIVersion, "api-key", "", nil
	case "anthropic", "gemini":
		return "", "", "", errImagesUnsupported
	default: // openai 兼容
		return strings.TrimRight(ch.BaseURL, "/") + "/images/" + suffix, "Authorization", "Bearer ", nil
	}
}

// HandleImageEdits：POST /v1/images/edits —— 图生图编辑（multipart）。
func (r *Relay) HandleImageEdits(c *gin.Context) { r.imageMultipartHandler(c, "edits") }

// HandleImageVariations：POST /v1/images/variations —— 图像变体（multipart）。
func (r *Relay) HandleImageVariations(c *gin.Context) { r.imageMultipartHandler(c, "variations") }

// imageMultipartHandler：edits/variations 共用实现（multipart 输入，按返回张数 ImageUSD 计价）。
func (r *Relay) imageMultipartHandler(c *gin.Context, suffix string) {
	t0 := r.Now()
	rdb := r.DB.WithContext(c.Request.Context())

	auth := c.GetHeader("Authorization")
	if len(auth) < 7 || auth[:7] != "Bearer " {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return
	}
	key, err := identity.VerifyKey(rdb, auth[7:], r.Now())
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

	// Entitlements 功能门（P0-2）：与生成共用 FeatureImages 开关。
	if ent, eerr := identity.LoadEntitlements(rdb, key.OrgID); eerr == nil && !ent.FeatureImages {
		finish("forbidden", http.StatusForbidden, "image editing disabled")
		relayErr(c, http.StatusForbidden, "image editing is not enabled for this organization")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	ctHeader := c.GetHeader("Content-Type")
	model := multipartField(body, ctHeader, "model")
	if model == "" {
		relayErr(c, http.StatusBadRequest, "multipart form with a model field is required")
		return
	}
	n := 1
	if ns := multipartField(body, ctHeader, "n"); ns != "" {
		if v, e := strconv.Atoi(ns); e == nil && v > 0 {
			n = v
		}
	}
	if n > 10 { // 同生成：防按张 hold 被恶意撑爆
		relayErr(c, http.StatusBadRequest, "n must be between 1 and 10")
		return
	}
	if code, msg, ok := keyGate(key, c.ClientIP(), model); !ok {
		relayErr(c, code, msg)
		return
	}
	if ok, _ := r.allowModelRate(key.OrgID, model); !ok {
		c.Header("Retry-After", "60")
		relayErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	inv.Model = model

	price, err := r.cachedPrice(model)
	if err != nil {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not available", model))
		return
	}
	if !price.ImageUSD.IsPositive() {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not priced for image generation", model))
		return
	}

	groupRatio := r.cachedRatio(key.OrgID, model)
	holdAmount := price.ImageUSD.Mul(decimal.NewFromInt(int64(n))).Mul(groupRatio)
	var hold *ledger.Hold
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		// 分片感知 hold：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），分片后按 RequestID 散列打散写热点。
		h, _, e := ledger.CreateHoldSharded(tx, key.OrgID, ledger.AccountUserCredit, inv.RequestID, ledger.HoldInput{
			IdempotencyKey: "imgedit:" + inv.RequestID,
			Amount:         holdAmount, RefType: "usage", RefID: model,
		})
		hold = h
		return e
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "insufficient balance")
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return
		}
		r.Log.Error("image-edit hold failed", "err", err)
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

	// 换渠道重试循环（同生成款分类驱动）
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
		ch, pickErr = pickFromSnapshot(channelSnap, model, attempt, exclude, r.healthFactor)
		if pickErr != nil {
			break
		}
		inv.Attempts = attempt + 1
		inv.ChannelID, inv.Provider = ch.ID, ch.Provider
		cred, derr := r.Keyring.Decrypt(ch.CredentialEnc)
		if derr != nil {
			r.Log.Error("channel credential decrypt failed", "channel", ch.ID, "err", derr)
			exclude[ch.ID] = true
			lastStatus, lastBody = http.StatusServiceUnavailable, []byte(`{"error":{"message":"upstream configuration error"}}`)
			continue
		}
		url, ah, av, uerr := imageUpstreamURL(ch, ch.UpstreamModel(model), suffix)
		if uerr != nil {
			r.release(hold.ID)
			finish(invocation.StatusUpstreamError, http.StatusBadRequest, "provider lacks image editing")
			relayErr(c, http.StatusBadRequest, fmt.Sprintf("provider %q does not support image editing", ch.Provider))
			return
		}
		upBody, upCT := mappedMultipartBody(ch, body, ctHeader, model) // 重写 multipart 的 model 字段
		upReq, berr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(upBody))
		if berr != nil {
			r.release(hold.ID) // 此分支原既不 release 也不 finish：hold 泄漏 + 漏记调用
			finish(invocation.StatusUpstreamError, http.StatusInternalServerError, "upstream request build failed")
			relayErr(c, http.StatusInternalServerError, "upstream request build failed")
			return
		}
		upReq.Header.Set("Content-Type", upCT)
		upReq.Header.Set(ah, av+cred)
		resp, err = r.clientFor(ch).Do(upReq)
		if err != nil {
			r.Log.Warn("image-edit upstream unreachable", "channel", ch.ID, "err", err)
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

	// 按实际返回张数结算；解析失败按请求张数收（保守 + 日志）
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	actualN := n
	inv.UsageSource = invocation.UsageSourceUpstream
	if e := json.Unmarshal(raw, &parsed); e == nil && len(parsed.Data) > 0 {
		actualN = len(parsed.Data)
		if actualN > n {
			actualN = n // 永不收超过请求张数（上游半可信）
		}
	} else {
		inv.UsageSource = invocation.UsageSourceEstimated
		r.Log.Warn("image-edit response without data array, charged by requested n", "model", model)
	}

	c.Data(http.StatusOK, "application/json", raw)

	actual := price.ImageUSD.Mul(decimal.NewFromInt(int64(actualN))).Mul(groupRatio)
	defer func() {
		_ = identity.AddKeyUsageSigned(r.DB, key.ID, actual.Sub(holdAmount))
		quotaSettled = true
	}()
	reason := fmt.Sprintf("images-%s model=%s n=%d ×%s", suffix, model, actualN, groupRatio)
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		_, e := ledger.SettleHoldExact(tx, hold.ID, actual, "usage", model, reason)
		return e
	}); err != nil {
		r.Log.Error("IMAGE-EDIT SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
	finish(invocation.StatusSuccess, http.StatusOK, "")
}
