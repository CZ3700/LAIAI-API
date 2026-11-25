package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var taskCSTZone = time.FixedZone("CST", 8*3600)

func isMissingRuleTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist")
}

func (s *Service) resolveTaskBillingRule(ctx context.Context, model string) (*pricing.ModelRuleBinding, *pricing.BillingRule) {
	b, rule, err := pricing.ResolveModelRuleContext(ctx, s.DB, model)
	if err != nil {
		if !isMissingRuleTable(err) && s.Log != nil {
			s.Log.Warn("task billing rule lookup failed, falling back to model_prices", "model", model, "err", err)
		}
		return nil, nil
	}
	return b, rule
}

func (s *Service) resolveTaskForward(ctx context.Context, model, reqPath string) *forward.ResolvedForward {
	if s.Forward == nil || model == "" {
		return nil
	}
	fwd, err := s.Forward.ResolveForModel(ctx, s.DB, model, "视频", reqPath)
	if err != nil && !isMissingRuleTable(err) && s.Log != nil {
		s.Log.Warn("task forward rule resolve failed", "model", model, "err", err)
	}
	return fwd
}

func (s *Service) resolveTaskForwardByEID(ctx context.Context, eid, model string) *forward.ResolvedForward {
	eid = strings.TrimSpace(eid)
	if eid == "" || s.DB == nil {
		return nil
	}
	// 优先走内存快照缓存，避免轮询协程每轮查库
	if s.Forward != nil {
		fwd, err := s.Forward.ResolveByEID(ctx, s.DB, eid, "/v1/videos", model)
		if err == nil {
			return fwd
		}
		if !isMissingRuleTable(err) && s.Log != nil {
			s.Log.Warn("task forward EID cache lookup failed", "eid", eid, "err", err)
		}
	}
	var row forward.ForwardRule
	if err := s.DB.WithContext(ctx).Where("eid = ? AND is_active = 1", eid).First(&row).Error; err != nil {
		return nil
	}
	fwd, err := forward.ParseForwardConfig(row.ConfigJSON, "/v1/videos", "", row.EID, row.ID, row.Name, model)
	if err != nil {
		return nil
	}
	return &fwd
}

func extractTaskVideoFeatures(content []any, params map[string]any, timeMult decimal.Decimal) pricing.ExtractedFeatures {
	feat := pricing.ExtractedFeatures{TimeMultiplier: &timeMult}
	if params != nil {
		if r, ok := params["resolution"].(string); ok {
			feat.Resolution = r
		}
		if sz, ok := params["size"].(string); ok {
			feat.Size = sz
		}
		if st, ok := params["service_tier"].(string); ok {
			feat.ServiceTier = st
		}
		if snd, ok := params["sound"].(string); ok {
			feat.Sound = snd
		}
		if ga, ok := params["generate_audio"].(bool); ok {
			feat.HasAudio = ga
		}
		if d, ok := anyFloat64(params["duration"]); ok && d > 0 {
			dd := decimal.NewFromFloat(d)
			feat.DurationSeconds = &dd
		}
		if f, ok := anyFloat64(params["fps"]); ok && f > 0 {
			fd := decimal.NewFromFloat(f)
			feat.FPS = &fd
		} else if f, ok := anyFloat64(params["framespersecond"]); ok && f > 0 {
			fd := decimal.NewFromFloat(f)
			feat.FPS = &fd
		}
	}
	var imgCount int64
	for _, raw := range content {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		t := strings.ToLower(mapStringFold(m, "type"))
		switch t {
		case "video", "video_url":
			feat.HasVideo = true
		case "audio", "audio_url":
			feat.HasAudio = true
		case "image", "image_url":
			feat.HasImageRef = true
			imgCount++
		}
		if mapAnyFold(m, "image_url") != nil || mapStringFold(m, "image") != "" {
			if t != "video" && t != "video_url" {
				feat.HasImageRef = true
			}
		}
		if mapAnyFold(m, "video_url") != nil {
			feat.HasVideo = true
		}
	}
	if imgCount > 0 {
		feat.ImageRefCount = &imgCount
	}
	return feat
}

func anyFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func encodeFeaturesSnapshot(feat pricing.ExtractedFeatures) string {
	b, err := json.Marshal(feat)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ModalityForProvider maps task provider names to invocation modality values.
func ModalityForProvider(provider string) string {
	switch provider {
	case "suno":
		return "music"
	case "midjourney":
		return "image"
	default:
		return "video"
	}
}

func encodeRequestMeta(params map[string]any) string {
	if len(params) == 0 {
		return "{}"
	}
	meta := make(map[string]any, 6)
	for _, k := range []string{"duration", "resolution", "ratio", "size", "fps", "framespersecond", "generate_audio", "watermark", "service_tier"} {
		if v, ok := params[k]; ok {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return "{}"
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func computeTaskRuleHold(b *pricing.ModelRuleBinding, rule *pricing.BillingRule, estTokens int64, ratio decimal.Decimal, feat pricing.ExtractedFeatures) decimal.Decimal {
	disc := ratio.Mul(b.EffectiveDiscount())
	cost, _ := pricing.ComputeCost(rule, pricing.UsageTokens{
		Prompt:     estTokens,
		Completion: estTokens,
	}, disc, feat)
	if b != nil && b.PreDeduction.IsPositive() {
		pre := b.PreDeduction.Mul(ratio).Round(6)
		if pre.GreaterThan(cost) {
			cost = pre
		}
	}
	return cost
}

func computeTaskRuleActual(b *pricing.ModelRuleBinding, rule *pricing.BillingRule, completionTokens int64, ratio decimal.Decimal, featuresJSON string, timeMult decimal.Decimal) (decimal.Decimal, string) {
	var feat pricing.ExtractedFeatures
	if strings.TrimSpace(featuresJSON) != "" {
		_ = json.Unmarshal([]byte(featuresJSON), &feat)
	}
	if timeMult.IsPositive() {
		feat.TimeMultiplier = &timeMult
	}
	disc := ratio.Mul(b.EffectiveDiscount())
	return pricing.ComputeCost(rule, pricing.UsageTokens{
		Completion: completionTokens,
	}, disc, feat)
}

func adaptorForTask(provider string, fwd *forward.ResolvedForward) (Adaptor, error) {
	if fwd != nil && strings.TrimSpace(fwd.TargetType) != "" {
		if ad, err := adaptorFor(strings.TrimSpace(fwd.TargetType)); err == nil {
			return ad, nil
		}
	}
	return adaptorFor(provider)
}

func buildTaskSubmitRequest(ctx context.Context, ad Adaptor, ch *relay.Channel, fwd *forward.ResolvedForward, credential string, in SubmitInput) (*http.Request, error) {
	if fwd != nil && fwd.ContentToPrompt {
		prompt, _ := taskContentParts(in.Content)
		if prompt != "" {
			cp := maps.Clone(in.Params)
			if cp == nil {
				cp = make(map[string]any, 1)
			}
			if _, exists := cp["prompt"]; !exists {
				cp["prompt"] = prompt
			}
			in.Params = cp
		}
	}
	req, err := ad.SubmitRequest(ctx, ch, credential, in)
	if err != nil || fwd == nil {
		return req, err
	}
	if fwd.UpstreamPath != "" {
		expanded := forward.ExpandUpstreamPath(fwd.UpstreamPath, ch.UpstreamModel(in.Model), "")
		parsedURL, uerr := forward.JoinUpstreamURL(ch.BaseURL, expanded)
		if uerr != nil {
			return nil, uerr
		}
		req.URL = parsedURL
	}
	if fwd.AuthType != "" {
		forward.ApplyAuth(req, fwd.AuthType, credential)
	}
	return req, nil
}

func buildTaskQueryRequest(ctx context.Context, ad Adaptor, ch *relay.Channel, fwd *forward.ResolvedForward, credential, upstreamTaskID string) (*http.Request, error) {
	req, err := ad.QueryRequest(ctx, ch, credential, upstreamTaskID)
	if err != nil || fwd == nil {
		return req, err
	}
	if fwd.PollPath != "" {
		expanded := forward.ExpandUpstreamPath(fwd.PollPath, "", upstreamTaskID)
		parsedURL, uerr := forward.JoinUpstreamURL(ch.BaseURL, expanded)
		if uerr != nil {
			return nil, uerr
		}
		req.URL = parsedURL
	}
	if fwd.AuthType != "" {
		forward.ApplyAuth(req, fwd.AuthType, credential)
	}
	return req, nil
}

// ── Seedance / Volcengine Ark 原生视频任务入站端点（/api/v3/contents/generations/tasks） ──────

func bindArkNativeCreate(c *gin.Context) (*createReq, error) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, relay.MaxRequestBodySize))
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	req := &createReq{}
	req.Model, _ = all["model"].(string)
	req.Content, _ = all["content"].([]any)
	for k, v := range all {
		if strings.EqualFold(k, "model") {
			if req.Model == "" {
				req.Model, _ = v.(string)
			}
			delete(all, k)
		} else if strings.EqualFold(k, "content") {
			if req.Content == nil {
				req.Content, _ = v.([]any)
			}
			delete(all, k)
		}
	}
	// 兼容仅传顶层 prompt 的客户端请求
	if len(req.Content) == 0 {
		if p, ok := all["prompt"].(string); ok && strings.TrimSpace(p) != "" {
			req.Content = []any{map[string]any{"type": "text", "text": p}}
		}
	}
	if req.Model == "" || len(req.Content) == 0 {
		return nil, errMissingModelContent
	}
	if len(all) > 0 {
		req.Params = all
	}
	return req, nil
}

// HandleArkNativeCreate 处理 POST /api/v3/contents/generations/tasks（Seedance / 火山方舟原生接口）。
func (s *Service) HandleArkNativeCreate(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	if ent, eerr := identity.LoadEntitlements(s.DB, key.OrgID); eerr == nil && !ent.FeatureVideo {
		taskErr(c, http.StatusForbidden, "video generation is not enabled for this organization")
		return
	}
	req, err := bindArkNativeCreate(c)
	if err != nil {
		if _, isTooLarge := errors.AsType[*http.MaxBytesError](err); isTooLarge {
			taskErr(c, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		taskErr(c, http.StatusBadRequest, "model and content are required")
		return
	}
	if s.Gate == nil {
		taskErr(c, http.StatusInternalServerError, "server misconfigured")
		return
	}
	if code, msg, allowed := s.Gate.GateKey(key, c.ClientIP(), req.Model); !allowed {
		taskErr(c, code, msg)
		return
	}
	if allowed, _ := s.Gate.AllowModelRate(key.OrgID, req.Model); !allowed {
		c.Header("Retry-After", "60")
		taskErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return
	}
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID:       key.OrgID,
		UserID:      key.UserID,
		APIKeyID:    key.ID,
		Model:       req.Model,
		Content:     req.Content,
		Params:      req.Params,
		RequestPath: c.Request.URL.Path,
	})
	if rejected, ok := errors.AsType[*UpstreamRejectedError](err); ok {
		taskErr(c, http.StatusBadRequest, clipReason("upstream rejected task parameters: "+rejected.Message, 300))
		return
	}
	switch {
	case errors.Is(err, relay.ErrModelNotPriced) || errors.Is(err, relay.ErrNoChannel):
		taskErr(c, http.StatusBadRequest, "model not available")
	case errors.Is(err, ErrConcurrencyLimit):
		c.Header("Retry-After", "10")
		taskErr(c, http.StatusTooManyRequests, "concurrency limit reached, retry later")
	case errors.Is(err, ErrKeyQuotaExceeded):
		taskErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		taskErr(c, http.StatusPaymentRequired, "insufficient balance")
	case err != nil:
		s.Log.Error("ark native task submit failed", "err", summarizeSubmitErr(err))
		taskErr(c, http.StatusBadGateway, "task submit failed")
	default:
		c.JSON(http.StatusOK, gin.H{
			"id":               t.TaskID,
			"model":            t.Model,
			"status":           t.Status,
			"created_at":       t.SubmittedAt.Unix(),
			"forward_rule_eid": t.ForwardRuleEID,
			"billing_rule_pid": t.BillingRulePID,
		})
	}
}

// HandleArkNativeGet 处理 GET /api/v3/contents/generations/tasks/:id。
func (s *Service) HandleArkNativeGet(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	t, err := s.Get(key.OrgID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			taskErr(c, http.StatusNotFound, "task not found")
			return
		}
		taskErr(c, http.StatusInternalServerError, "task query failed")
		return
	}
	resp := gin.H{
		"id":               t.TaskID,
		"model":            t.Model,
		"status":           t.Status,
		"created_at":       t.SubmittedAt.Unix(),
		"cost_usd":         t.CostUSD,
		"billing_detail":   t.BillingDetail,
		"forward_rule_eid": t.ForwardRuleEID,
		"billing_rule_pid": t.BillingRulePID,
	}
	if t.ResultURL != "" {
		resp["content"] = gin.H{"video_url": t.ResultURL}
	}
	if t.ActualTokens > 0 {
		resp["usage"] = gin.H{"completion_tokens": t.ActualTokens}
	}
	if t.FailReason != "" {
		resp["error"] = gin.H{"code": "TaskFailed", "message": t.FailReason}
	}
	if t.FinishedAt != nil {
		resp["updated_at"] = t.FinishedAt.Unix()
	}
	c.JSON(http.StatusOK, resp)
}

// HandleArkNativeCancel 处理 DELETE /api/v3/contents/generations/tasks/:id（仅 queued 态可取消退款）。
func (s *Service) HandleArkNativeCancel(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	t, err := s.Get(key.OrgID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			taskErr(c, http.StatusNotFound, "task not found")
			return
		}
		taskErr(c, http.StatusInternalServerError, "task query failed")
		return
	}
	if t.Status != StatusQueued {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error": gin.H{
				"code":    "InvalidTaskState",
				"message": "only queued tasks can be cancelled",
			},
		})
		return
	}
	s.settleFailure(t, "cancelled by client", false)
	c.JSON(http.StatusOK, gin.H{"id": t.TaskID, "status": StatusFailed})
}
