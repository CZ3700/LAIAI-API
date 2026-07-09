package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// 音频模态（gap P2-F）。两个 OpenAI 兼容端点：
//   /v1/audio/speech          —— TTS，JSON {model,input,voice}；按 input 字符数计价（SpeechCost）
//   /v1/audio/transcriptions  —— STT，multipart(file+model)；按次固定价（TranscriptionPerCall，时长不可靠）
// 资金语义与 image 同构：hold→capture 走 ledger，差额自动解冻。anthropic/gemini 不支持，回退报错。

var errAudioUnsupported = fmt.Errorf("relay: provider does not serve openai-compatible audio")

// audioUpstreamURL：音频端点查 (format×mode) 矩阵（contract v2，mode.go）。
// 返回 (url, authHeader, authValuePrefix, err)——保持既有调用方签名。
func audioUpstreamURL(ch *Channel, model, suffix string) (string, string, string, error) {
	var mode RelayMode
	switch suffix {
	case "speech":
		mode = ModeAudioSpeech
	case "transcriptions":
		mode = ModeAudioTranscriptions
	default: // translations
		mode = ModeAudioTranslations
	}
	spec, err := modeUpstreamSpec(ch, mode, model)
	if err != nil {
		return "", "", "", err
	}
	return spec.URL, spec.AuthHeader, spec.AuthPrefix, nil
}

// HandleAudioSpeech：POST /v1/audio/speech —— TTS。按输入字符数计价，响应为二进制音频透传。
func (r *Relay) HandleAudioSpeech(c *gin.Context) {
	t0 := r.Now()
	key, inv, finish, ok := r.audioPreamble(c, t0)
	if !ok {
		return
	}
	inv.Modality = invocation.ModalityAudioSpeech

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		relayErr(c, http.StatusBadRequest, "cannot read request body")
		return
	}
	var req struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}
	if e := json.Unmarshal(body, &req); e != nil || req.Model == "" || req.Input == "" {
		relayErr(c, http.StatusBadRequest, "model and input are required")
		return
	}
	if code, msg, gok := r.gateKey(key, c.ClientIP(), req.Model); !gok {
		relayErr(c, code, msg)
		return
	}
	if rok, _ := r.allowModelRate(key.OrgID, req.Model); !rok {
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
	if !price.TTSPer1KChars.IsPositive() {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not priced for text-to-speech", req.Model))
		return
	}

	chars := int64(len([]rune(req.Input)))
	groupRatio := r.cachedRatio(key.OrgID, req.Model)
	cost := price.SpeechCost(chars).Mul(groupRatio) // TTS 确定性计价：字符数已知，hold=capture
	hold, ok := r.audioHold(c, key, req.Model, cost, finish)
	if !ok {
		return
	}
	// API Key 额度原子预留（P0-3）；cost 确定性，成功即 settled，失败由 defer 退还
	if !identity.ReserveKeyQuota(r.DB, key.ID, cost) {
		r.release(hold.ID)
		finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "api key quota exceeded")
		relayErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
		return
	}
	quotaSettled := false
	defer func() {
		if !quotaSettled {
			_ = identity.AddKeyUsageSigned(r.DB, key.ID, cost.Neg())
		}
	}()

	// 上游请求构建器（带模型映射）
	build := func(ctx context.Context, ch *Channel, cred string) (*http.Request, error) {
		url, ah, av, e := audioUpstreamURL(ch, ch.UpstreamModel(req.Model), "speech")
		if e != nil {
			return nil, e
		}
		upBody, _ := mappedRawBody(ch, body, req.Model)
		hr, e := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(upBody))
		if e != nil {
			return nil, e
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set(ah, av+cred)
		// 渠道级 header override（contract v2 作用域扩展）
		applyHeaderOverrideForMode(hr, ch.HeaderOverride, cred, ModeAudioSpeech)
		return hr, nil
	}
	resp, _, ferr := r.forwardWithFailover(c, inv, finish, forwardOpts{
		model: req.Model, hold: hold, unsupported: errAudioUnsupported, modality: "audio", logLabel: "audio", build: build,
	})
	if ferr {
		return
	}
	defer resp.Body.Close()
	if rid := resp.Header.Get("x-request-id"); rid != "" {
		inv.UpstreamRequestID = rid
	}
	inv.FRTMs = int(r.Now().Sub(t0).Milliseconds())

	// 二进制音频透传（content-type 来自上游，如 audio/mpeg）。不做 JSON content-type 校验。
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/mpeg"
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		r.release(hold.ID)
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream response body unreadable")
		relayErr(c, http.StatusBadGateway, "upstream read failed")
		return
	}
	c.Data(http.StatusOK, ct, raw)

	r.audioSettle(c, key, req.Model, hold, cost, fmt.Sprintf("tts model=%s chars=%d ×%s", req.Model, chars, groupRatio))
	quotaSettled = true      // 预留即等于 cost（确定性计价），成功无需对账
	inv.PromptTokens = chars // 字符数记入 prompt 槽（语义：计价单位量）
	inv.CostUSD = cost
	finish(invocation.StatusSuccess, http.StatusOK, "")
}

// HandleAudioTranscription：POST /v1/audio/transcriptions —— STT 转写。
func (r *Relay) HandleAudioTranscription(c *gin.Context) { r.sttHandler(c, "transcriptions") }

// HandleAudioTranslation：POST /v1/audio/translations —— STT 翻译到英文（D1）。
// 与转写同款流程/计价（TranscriptionPerCall），仅上游端点后缀不同。
func (r *Relay) HandleAudioTranslation(c *gin.Context) { r.sttHandler(c, "translations") }

// sttHandler：STT 共用实现。multipart(file+model)，按次固定价（时长不可靠，按次最稳妥），响应透传。
func (r *Relay) sttHandler(c *gin.Context, suffix string) {
	t0 := r.Now()
	key, inv, finish, ok := r.audioPreamble(c, t0)
	if !ok {
		return
	}
	inv.Modality = invocation.ModalityAudioSTT

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
	if code, msg, gok := r.gateKey(key, c.ClientIP(), model); !gok {
		relayErr(c, code, msg)
		return
	}
	if rok, _ := r.allowModelRate(key.OrgID, model); !rok {
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
	if !price.TranscriptionPerCall.IsPositive() {
		relayErr(c, http.StatusBadRequest, fmt.Sprintf("model %q is not priced for transcription", model))
		return
	}

	groupRatio := r.cachedRatio(key.OrgID, model)
	cost := price.TranscriptionPerCall.Mul(groupRatio) // 按次固定，hold=capture
	hold, ok := r.audioHold(c, key, model, cost, finish)
	if !ok {
		return
	}
	// API Key 额度原子预留（P0-3）；cost 确定性，成功即 settled，失败由 defer 退还
	if !identity.ReserveKeyQuota(r.DB, key.ID, cost) {
		r.release(hold.ID)
		finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "api key quota exceeded")
		relayErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
		return
	}
	quotaSettled := false
	defer func() {
		if !quotaSettled {
			_ = identity.AddKeyUsageSigned(r.DB, key.ID, cost.Neg())
		}
	}()

	// P2-11：应用渠道模型映射——URL 用上游名（Azure deployment 形态），multipart 的 model
	// 字段也重写为上游名（OpenAI 兼容 STT 模型在表单字段里）。无映射时原样透传。
	build := func(ctx context.Context, ch *Channel, cred string) (*http.Request, error) {
		url, ah, av, e := audioUpstreamURL(ch, ch.UpstreamModel(model), suffix)
		if e != nil {
			return nil, e
		}
		upBody, upCT := mappedMultipartBody(ch, body, ctHeader, model)
		hr, e := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(upBody))
		if e != nil {
			return nil, e
		}
		hr.Header.Set("Content-Type", upCT) // 映射后用新 boundary；无映射则原 boundary
		hr.Header.Set(ah, av+cred)
		// 渠道级 header override（contract v2 作用域扩展）：multipart 模态 Content-Type 防御性跳过
		hoMode := ModeAudioTranscriptions
		if suffix == "translations" {
			hoMode = ModeAudioTranslations
		}
		applyHeaderOverrideForMode(hr, ch.HeaderOverride, cred, hoMode)
		return hr, nil
	}
	resp, _, ferr := r.forwardWithFailover(c, inv, finish, forwardOpts{
		model: model, hold: hold, unsupported: errAudioUnsupported, modality: "audio", logLabel: "audio", build: build,
	})
	if ferr {
		return
	}
	defer resp.Body.Close()
	if rid := resp.Header.Get("x-request-id"); rid != "" {
		inv.UpstreamRequestID = rid
	}
	inv.FRTMs = int(r.Now().Sub(t0).Milliseconds())
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		r.release(hold.ID)
		finish(invocation.StatusUpstreamError, http.StatusBadGateway, "upstream response body unreadable")
		relayErr(c, http.StatusBadGateway, "upstream read failed")
		return
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(http.StatusOK, ct, raw)

	r.audioSettle(c, key, model, hold, cost, fmt.Sprintf("%s model=%s ×%s", suffix, model, groupRatio))
	quotaSettled = true // 预留即等于 cost（确定性计价），成功无需对账
	inv.CostUSD = cost
	finish(invocation.StatusSuccess, http.StatusOK, "")
}

// —— 音频处理共用件 ——

// audioPreamble：auth + invocation 初始化 + finish 闭包（与 images 同构）。
func (r *Relay) audioPreamble(c *gin.Context, t0 time.Time) (*identity.APIKey, *invocation.Invocation, func(string, int, string), bool) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		relayErr(c, http.StatusUnauthorized, "missing api key")
		return nil, nil, nil, false
	}
	key, err := identity.VerifyKey(r.DB.WithContext(c.Request.Context()), strings.TrimPrefix(auth, "Bearer "), r.Now())
	if err != nil {
		relayErr(c, http.StatusUnauthorized, "invalid api key")
		return nil, nil, nil, false
	}
	inv := &invocation.Invocation{OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID, RequestID: newRequestID()}
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
	return key, inv, finish, true
}

// audioHold：建 hold（余额不足/失败已转译响应）。
func (r *Relay) audioHold(c *gin.Context, key *identity.APIKey, model string, amount decimal.Decimal, finish func(string, int, string)) (*ledger.Hold, bool) {
	var hold *ledger.Hold
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		reqID := c.Writer.Header().Get("X-Request-Id")
		// 分片感知 hold：org 未分片(n≤1)时与 CreateHold 逐字一致（零回归），分片后按 RequestID 散列打散写热点。
		h, _, e := ledger.CreateHoldSharded(tx, key.OrgID, ledger.AccountUserCredit, reqID, ledger.HoldInput{
			IdempotencyKey: "audio:" + reqID, // 每请求唯一 16-hex（非时间戳，杜绝纳秒碰撞共用 hold）
			Amount:         amount, RefType: "usage", RefID: model,
		})
		hold = h
		return e
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientBalance) {
			finish(invocation.StatusInsufficientBalance, http.StatusPaymentRequired, "insufficient balance")
			relayErr(c, http.StatusPaymentRequired, "insufficient balance")
			return nil, false
		}
		r.Log.Error("audio hold failed", "err", err)
		relayErr(c, http.StatusInternalServerError, "billing error")
		return nil, false
	}
	return hold, true
}

// audioSettle：确定性计价结算（cost 即 hold，正常无 overage）。
// 注意：per-key 额度统计不在此处——调用方用 P0-3 的 reserve(cost)+quotaSettled 记账，避免双计。
func (r *Relay) audioSettle(c *gin.Context, key *identity.APIKey, model string, hold *ledger.Hold, cost decimal.Decimal, reason string) {
	_ = c
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		_, e := ledger.SettleHoldExact(tx, hold.ID, cost, "usage", model, reason)
		return e
	}); err != nil {
		r.Log.Error("AUDIO SETTLE FAILED — manual reconcile needed", "hold", hold.ID, "err", err)
	}
}

// multipartField：从 multipart body 提取某个非文件表单字段值（用于取 transcription 的 model）。
// 解析失败/无该字段返回 ""。只读 boundary 解析，不消费原始 body（调用方仍持原 body 透传）。
func multipartField(body []byte, contentType, field string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	boundary := params["boundary"]
	if boundary == "" {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, e := mr.NextPart()
		if e != nil {
			return ""
		}
		if part.FormName() == field && part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, 1<<16))
			return strings.TrimSpace(string(val))
		}
		_ = part.Close()
	}
}

// mappedMultipartBody：渠道对该模型有映射时，重写 multipart 的 model 字段为上游名，
// 其余 part（含音频文件）原样复制并保留 header，用新 boundary 重新编码；返回新 body 与
// 新 Content-Type。无映射/解析失败 → 原样返回（不破坏透传）。修复 P2-11：STT 模型映射缺失。
func mappedMultipartBody(ch *Channel, body []byte, contentType, platformModel string) ([]byte, string) {
	upstream := ch.UpstreamModel(platformModel)
	if upstream == platformModel {
		return body, contentType // 无映射，原样透传
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return body, contentType
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for {
		p, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return body, contentType // 解析失败，回退原样透传
		}
		if p.FormName() == "model" && p.FileName() == "" {
			_ = p.Close()
			if w, we := mw.CreateFormField("model"); we != nil {
				return body, contentType
			} else if _, we := io.WriteString(w, upstream); we != nil {
				return body, contentType
			}
			continue
		}
		w, we := mw.CreatePart(p.Header) // 保留 Content-Disposition/Content-Type（文件名等）
		if we == nil {
			_, we = io.Copy(w, p)
		}
		_ = p.Close()
		if we != nil {
			return body, contentType
		}
	}
	if mw.Close() != nil {
		return body, contentType
	}
	return buf.Bytes(), mw.FormDataContentType()
}
