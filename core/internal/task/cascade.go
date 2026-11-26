package task

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"

	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
)

const maxCascadeS2Resubmit = 3

// cascadeTaskMeta 持久化在 tasks.request_meta_json（json:"-"），绝不直吐给非管理员客户端。
type cascadeTaskMeta struct {
	Stage              int     `json:"stage"` // 1 = 一阶段生成底稿，2 = 二阶段超分增强
	Engine             string  `json:"engine"`
	TargetResolution   string  `json:"target_resolution"`
	BaseResolution     string  `json:"base_resolution"`
	ResMul             float64 `json:"res_mul"`
	SkipS2             bool    `json:"skip_s2,omitempty"`
	EnhanceMode        string  `json:"enhance_mode,omitempty"`
	EnhanceScene       string  `json:"enhance_scene,omitempty"`
	S2Model            string  `json:"s2_model,omitempty"`
	S2UpstreamPath     string  `json:"s2_upstream_path,omitempty"`
	S2TimeoutMins      uint64  `json:"s2_timeout_mins,omitempty"`
	FPS                int64   `json:"fps,omitempty"`
	S1VideoURL         string  `json:"s1_video_url,omitempty"`
	S1LastFrameURL     string  `json:"s1_last_frame_url,omitempty"`
	S1CompletionTokens int64   `json:"s1_completion_tokens,omitempty"`
	S2ChannelID        uint64  `json:"s2_channel_id,omitempty"`
	S2CredentialID     uint64  `json:"s2_credential_id,omitempty"`
	S2TaskID           string  `json:"s2_task_id,omitempty"`
	S2SubmittedAt      int64   `json:"s2_submitted_at,omitempty"`
	S2ResubmitCount    int     `json:"s2_resubmit_count,omitempty"`
}

// taskRequestMeta 封装 tasks.request_meta_json 的完整内部状态（向后兼容旧平铺 KV 字段）。
type taskRequestMeta struct {
	Duration      float64          `json:"duration,omitempty"`
	Resolution    string           `json:"resolution,omitempty"`
	Ratio         string           `json:"ratio,omitempty"`
	Size          string           `json:"size,omitempty"`
	FPS           float64          `json:"fps,omitempty"`
	GenerateAudio bool             `json:"generate_audio,omitempty"`
	Watermark     bool             `json:"watermark,omitempty"`
	ServiceTier   string           `json:"service_tier,omitempty"`
	Draft         bool             `json:"draft,omitempty"`
	LastFrameURL  string           `json:"last_frame_url,omitempty"`
	ViduType      string           `json:"vidu_type,omitempty"`
	Cascade       *cascadeTaskMeta `json:"cascade,omitempty"`
}

func parseTaskRequestMeta(raw string) *taskRequestMeta {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return &taskRequestMeta{}
	}
	var meta taskRequestMeta
	if err := json.Unmarshal([]byte(trimmed), &meta); err != nil {
		return &taskRequestMeta{}
	}
	return &meta
}

func encodeRequestMetaFull(in SubmitInput, cascade *cascadeTaskMeta) string {
	meta := taskRequestMeta{
		Cascade: cascade,
	}
	if strings.Contains(strings.ToLower(in.InboundPath), "/ent/v2/") {
		meta.ViduType = forward.InferViduType(in.InboundPath)
	}
	if in.Params != nil {
		if d, ok := anyFloat64(in.Params["duration"]); ok && d > 0 {
			meta.Duration = d
		}
		if r, ok := in.Params["resolution"].(string); ok {
			meta.Resolution = r
		}
		if rat, ok := in.Params["ratio"].(string); ok {
			meta.Ratio = rat
		} else if ar, ok := in.Params["aspect_ratio"].(string); ok {
			meta.Ratio = ar
		}
		if sz, ok := in.Params["size"].(string); ok {
			meta.Size = sz
		}
		if fps, ok := anyFloat64(firstAnyNonNil(in.Params["fps"], in.Params["framespersecond"])); ok && fps > 0 {
			meta.FPS = fps
		}
		meta.GenerateAudio = forward.JSONTruthy(in.Params["generate_audio"])
		meta.Watermark = forward.JSONTruthy(in.Params["watermark"])
		if st, ok := in.Params["service_tier"].(string); ok {
			meta.ServiceTier = st
		}
		meta.Draft = forward.JSONTruthy(in.Params["draft"])
	}
	if cascade != nil && cascade.TargetResolution != "" {
		meta.Resolution = cascade.TargetResolution
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func updateRequestMetaLastFrame(raw, lastFrameURL string) string {
	if strings.TrimSpace(lastFrameURL) == "" {
		return raw
	}
	meta := parseTaskRequestMeta(raw)
	meta.LastFrameURL = strings.TrimSpace(lastFrameURL)
	b, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	return string(b)
}

func firstAnyNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// resolveDraftTaskPinning 处理 Seedance 2.5 draft_task_id：将平台 task_id 映射并钉回原任务渠道+凭证，替换上游 id。
func (s *Service) resolveDraftTaskPinning(ctx context.Context, in SubmitInput) SubmitInput {
	draftID, _ := in.Params["draft_task_id"].(string)
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		for _, raw := range in.Content {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if ty, _ := m["type"].(string); ty == "draft_task" {
				if dt, ok := m["draft_task"].(map[string]any); ok {
					if idStr, ok := dt["id"].(string); ok && strings.TrimSpace(idStr) != "" {
						draftID = strings.TrimSpace(idStr)
						break
					}
				}
			}
		}
	}
	if draftID == "" || s.DB == nil {
		return in
	}
	var src Task
	if err := s.DB.WithContext(ctx).
		Where("org_id = ? AND task_id = ?", in.OrgID, draftID).
		First(&src).Error; err != nil || src.UpstreamTaskID == "" {
		return in
	}
	out := in
	out.PinChannelID = src.ChannelID
	out.PinCredentialID = src.CredentialID
	if out.Params != nil {
		cp := maps.Clone(out.Params)
		if _, exists := cp["draft_task_id"]; exists {
			cp["draft_task_id"] = src.UpstreamTaskID
		}
		out.Params = cp
	}
	if out.RawBody != nil {
		cp := maps.Clone(out.RawBody)
		if _, exists := cp["draft_task_id"]; exists {
			cp["draft_task_id"] = src.UpstreamTaskID
		}
		out.RawBody = cp
	}
	if len(out.Content) > 0 {
		newContent := make([]any, len(out.Content))
		for i, item := range out.Content {
			m, ok := item.(map[string]any)
			if !ok {
				newContent[i] = item
				continue
			}
			if ty, _ := m["type"].(string); ty == "draft_task" {
				cm := maps.Clone(m)
				if dt, ok := cm["draft_task"].(map[string]any); ok {
					dtCopy := maps.Clone(dt)
					dtCopy["id"] = src.UpstreamTaskID
					cm["draft_task"] = dtCopy
				}
				newContent[i] = cm
			} else {
				newContent[i] = m
			}
		}
		out.Content = newContent
	}
	return out
}

type cascadePrepareResult struct {
	TargetResolution string
	ResMul           float64
	Meta             *cascadeTaskMeta
}

func (s *Service) prepareCascadeSubmit(
	ctx context.Context,
	fwd *forward.ResolvedForward,
	bRule *pricing.BillingRule,
	in SubmitInput,
) (*cascadePrepareResult, SubmitInput, error) {
	isDraft := forward.JSONTruthy(in.Params["draft"])
	targetRes := ""
	if r, ok := in.Params["resolution"].(string); ok {
		targetRes = strings.ToLower(strings.TrimSpace(r))
	}
	if targetRes == "" {
		if isDraft {
			targetRes = "480p"
		} else {
			targetRes = "720p"
		}
	}

	if err := cascadeCheckResolution(targetRes, bRule); err != nil {
		return nil, in, err
	}

	// 检查 res_limit 并处理降级策略
	ratioStr := ""
	if rat, ok := in.Params["ratio"].(string); ok {
		ratioStr = strings.ToLower(strings.TrimSpace(rat))
	}
	if limitExceeded := s.checkCascadeResLimit(ctx, fwd, targetRes, ratioStr); limitExceeded {
		fb := strings.ToLower(strings.TrimSpace(fwd.ResLimitFallback))
		if fb != "" && fb != targetRes {
			targetRes = fb
		} else {
			return nil, in, ErrConcurrencyLimit
		}
	}

	baseRes := fwd.CascadeResolveBase(targetRes)
	resMul := fwd.LookupResMul(targetRes)
	skipS2 := isDraft || !fwd.IsResEnhanceEnabled(targetRes) || strings.EqualFold(baseRes, targetRes)

	mode, s2Path, s2Model := fwd.ResolveVolcEnhanceTarget(targetRes)
	var fps int64
	if f, ok := anyFloat64(firstAnyNonNil(in.Params["fps"], in.Params["framespersecond"])); ok && f > 0 {
		fps = int64(f)
	}

	meta := &cascadeTaskMeta{
		Stage:            1,
		Engine:           cmp.Or(fwd.CascadeEngine, "volc"),
		TargetResolution: targetRes,
		BaseResolution:   baseRes,
		ResMul:           resMul,
		SkipS2:           skipS2,
		EnhanceMode:      mode,
		EnhanceScene:     cmp.Or(fwd.ResScene, "aigc"),
		S2Model:          s2Model,
		S2UpstreamPath:   s2Path,
		S2TimeoutMins:    cmp.Or(fwd.S2TimeoutMins, uint64(10)),
		FPS:              fps,
	}

	stage1In := in
	stage1In.Params = maps.Clone(in.Params)
	if stage1In.Params == nil {
		stage1In.Params = map[string]any{}
	}
	stage1In.Params["resolution"] = baseRes
	if stage1In.RawBody != nil {
		stage1In.RawBody = maps.Clone(stage1In.RawBody)
		stage1In.RawBody["resolution"] = baseRes
	}
	return &cascadePrepareResult{
		TargetResolution: targetRes,
		ResMul:           resMul,
		Meta:             meta,
	}, stage1In, nil
}

func cascadeCheckResolution(targetRes string, bRule *pricing.BillingRule) error {
	if bRule == nil || strings.TrimSpace(bRule.ExtendedConfig) == "" || bRule.ExtendedConfig == "{}" {
		return nil
	}
	var ext struct {
		ResolutionRates map[string]any `json:"resolution_rates"`
	}
	if err := json.Unmarshal([]byte(bRule.ExtendedConfig), &ext); err != nil || len(ext.ResolutionRates) == 0 {
		return nil
	}
	for k := range ext.ResolutionRates {
		if strings.EqualFold(k, targetRes) {
			return nil
		}
	}
	return &UpstreamRejectedError{
		StatusCode: 400,
		Message:    fmt.Sprintf("unsupported resolution %q for billing rule", targetRes),
	}
}

func (s *Service) checkCascadeResLimit(ctx context.Context, fwd *forward.ResolvedForward, targetRes, ratio string) bool {
	if s.DB == nil || fwd == nil || len(fwd.ResLimit) == 0 {
		return false
	}
	limit := 0
	if ratio != "" {
		if v, ok := fwd.ResLimit[targetRes+"@"+ratio]; ok {
			limit = v
		}
	}
	if limit == 0 {
		if v, ok := fwd.ResLimit[targetRes]; ok {
			limit = v
		}
	}
	if limit <= 0 {
		return false
	}
	var active int64
	_ = s.DB.WithContext(ctx).Model(&Task{}).
		Where("forward_rule_eid = ? AND status IN ?", fwd.EID, []string{StatusQueued, StatusRunning}).
		Count(&active).Error
	return int(active) >= limit
}

func scaleTokensByMul(tokens int64, mul float64) int64 {
	if tokens <= 0 || mul <= 0 {
		return tokens
	}
	return int64(math.Round(float64(tokens) * mul))
}

func (s *Service) persistCascadeMeta(t *Task, meta *taskRequestMeta) {
	b, err := json.Marshal(meta)
	if err != nil {
		return
	}
	raw := string(b)
	_ = s.DB.Model(&Task{}).Where("id = ?", t.ID).Update("request_meta_json", raw).Error
	t.RequestMetaJSON = raw
}

func (s *Service) pollCascadeTask(
	ctx context.Context,
	t *Task,
	s1Channel *relay.Channel,
	fwd *forward.ResolvedForward,
	meta *taskRequestMeta,
) {
	cm := meta.Cascade
	if cm == nil {
		return
	}
	if cm.Stage <= 1 {
		if cm.S1VideoURL != "" {
			s.submitCascadeStage2(ctx, t, fwd, meta)
			return
		}
		ad, err := adaptorForTask(t.Provider, fwd)
		if err != nil {
			s.Log.Error("cascade stage1 adaptor missing", "task", t.TaskID, "provider", t.Provider)
			return
		}
		cred, ok := s.resolveChannelCredential(s1Channel, t.CredentialID)
		if !ok {
			s.Log.Error("cascade stage1 credential unavailable", "task", t.TaskID)
			return
		}
		upReq, err := buildTaskQueryRequest(ctx, ad, s1Channel, fwd, cred, t.UpstreamTaskID)
		if err != nil {
			return
		}
		resp, err := s.Client.Do(upReq)
		if err != nil {
			s.Log.Warn("cascade stage1 query unreachable", "task", t.TaskID, "err", err)
			return
		}
		defer resp.Body.Close()
		res, err := ad.ParseQuery(resp)
		if err != nil {
			s.Log.Warn("cascade stage1 query parse failed", "task", t.TaskID, "err", err)
			return
		}
		switch res.Status {
		case StatusRunning:
			if t.Status == StatusQueued {
				now := s.Now()
				s.DB.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusQueued).
					Updates(map[string]any{"status": StatusRunning, "started_at": now})
			}
		case StatusFailed:
			s.settleFailure(t, res.FailReason, true)
		case StatusSucceeded:
			if res.ResultURL == "" && res.ResultRef != "" {
				if fetcher, ok := ad.(ResultFetcher); ok {
					u, ferr := fetcher.FetchResultURL(ctx, s.Client, s1Channel, cred, res.ResultRef)
					if ferr != nil || u == "" {
						s.Log.Warn("cascade stage1 fetch result not ready", "task", t.TaskID)
						return
					}
					res.ResultURL = u
				}
			}
			if res.ResultURL == "" {
				s.Log.Warn("cascade stage1 empty result_url, retrying", "task", t.TaskID)
				return
			}
			baseTokens := res.CompletionTokens
			if baseTokens <= 0 {
				// 从估算中按 ResMul 还原未缩放底稿 token，再统一乘 ResMul 保持口径一致。
				if cm.ResMul > 0 && t.EstTokens > 0 {
					baseTokens = int64(math.Round(float64(t.EstTokens) / cm.ResMul))
				}
			}
			if cm.SkipS2 {
				scaled := scaleTokensByMul(baseTokens, cm.ResMul)
				s.scrubCascadeAndSettleSuccess(t, meta, res.ResultURL, res.LastFrameURL, scaled)
				return
			}
			// 进入阶段 2：保存 S1 临时 URL（绝不写入 ResultURL 暴露给客户端），状态保持 running。
			cm.S1VideoURL = res.ResultURL
			cm.S1LastFrameURL = res.LastFrameURL
			cm.S1CompletionTokens = baseTokens
			if t.Status == StatusQueued {
				now := s.Now()
				_ = s.DB.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusQueued).
					Updates(map[string]any{"status": StatusRunning, "started_at": now}).Error
				t.Status = StatusRunning
			}
			s.persistCascadeMeta(t, meta)
			s.submitCascadeStage2(ctx, t, fwd, meta)
		}
		return
	}

	// Stage 2: 轮询超分通道
	timeoutSecs := int64(cmp.Or(cm.S2TimeoutMins, uint64(10))) * 60
	if cm.S2SubmittedAt > 0 && s.Now().Unix()-cm.S2SubmittedAt > timeoutSecs {
		if cm.S2ResubmitCount < maxCascadeS2Resubmit {
			cm.S2ResubmitCount++
			cm.Stage = 1
			cm.S2TaskID = ""
			cm.S2ChannelID = 0
			cm.S2CredentialID = 0
			s.persistCascadeMeta(t, meta)
			s.Log.Warn("cascade stage2 timeout, resubmitting", "task", t.TaskID, "attempt", cm.S2ResubmitCount)
			s.submitCascadeStage2(ctx, t, fwd, meta)
			return
		}
		s.settleFailure(t, "cascade stage2 super-resolution timeout", false)
		return
	}

	s2Ch := &relay.Channel{}
	if err := s.DB.First(s2Ch, cm.S2ChannelID).Error; err != nil {
		s.settleFailure(t, "cascade stage2 channel missing", false)
		return
	}
	s2Cred, ok := s.resolveChannelCredential(s2Ch, cm.S2CredentialID)
	if !ok {
		s.Log.Warn("cascade stage2 credential unavailable", "task", t.TaskID)
		return
	}
	s2Fwd := buildCascadeS2Forward(cm)
	ad := s.stage2Adaptor(cm.Engine)
	upReq, err := buildTaskQueryRequest(ctx, ad, s2Ch, s2Fwd, s2Cred, cm.S2TaskID)
	if err != nil {
		return
	}
	resp, err := s.Client.Do(upReq)
	if err != nil {
		s.Log.Warn("cascade stage2 query unreachable", "task", t.TaskID, "err", err)
		return
	}
	defer resp.Body.Close()
	s2Res, err := ad.ParseQuery(resp)
	if err != nil {
		s.Log.Warn("cascade stage2 query parse failed", "task", t.TaskID, "err", err)
		return
	}
	switch s2Res.Status {
	case StatusSucceeded:
		if s2Res.ResultURL == "" {
			s.Log.Warn("cascade stage2 empty result_url, retrying", "task", t.TaskID)
			return
		}
		scaled := scaleTokensByMul(cm.S1CompletionTokens, cm.ResMul)
		s.scrubCascadeAndSettleSuccess(t, meta, s2Res.ResultURL, cm.S1LastFrameURL, scaled)
	case StatusFailed:
		s.settleFailure(t, "cascade stage2 failed: "+s2Res.FailReason, true)
	}
}

func (s *Service) submitCascadeStage2(
	ctx context.Context,
	t *Task,
	fwd *forward.ResolvedForward,
	meta *taskRequestMeta,
) {
	cm := meta.Cascade
	if cm == nil || cm.S1VideoURL == "" {
		return
	}
	s2Model := cmp.Or(cm.S2Model, "vve-sd")
	s2Ch, err := s.pickStage2Channel(s2Model, cm.Engine)
	if err != nil {
		s.settleFailure(t, "cascade stage2 channel unavailable", false)
		return
	}
	credID, credEnc, ok := relay.SelectCredential(s.DB, s2Ch)
	if !ok {
		s.settleFailure(t, "cascade stage2 channel has no credential", false)
		return
	}
	cred, err := s.Keyring.Decrypt(credEnc)
	if err != nil {
		s.settleFailure(t, "cascade stage2 credential decrypt failed", false)
		return
	}
	s2Fwd := buildCascadeS2Forward(cm)
	ad := s.stage2Adaptor(cm.Engine)
	params := map[string]any{
		"video_url":  cm.S1VideoURL,
		"mode":       cmp.Or(cm.EnhanceMode, "standard"),
		"scene":      cmp.Or(cm.EnhanceScene, "aigc"),
		"resolution": cm.TargetResolution,
	}
	if cm.FPS > 0 {
		params["fps"] = cm.FPS
	}
	upReq, err := buildTaskSubmitRequest(ctx, ad, s2Ch, s2Fwd, cred, SubmitInput{
		Model:   s2Model,
		Params:  params,
		RawBody: params,
	})
	if err != nil {
		s.settleFailure(t, "cascade stage2 build request failed", false)
		return
	}
	resp, err := s.Client.Do(upReq)
	if err != nil {
		s.Log.Warn("cascade stage2 submit unreachable, will retry", "task", t.TaskID, "err", err)
		return
	}
	defer resp.Body.Close()
	s2TaskID, err := ad.ParseSubmit(resp)
	if err != nil {
		var rej *UpstreamRejectedError
		if errors.As(err, &rej) {
			s.settleFailure(t, "cascade stage2 rejected: "+rej.Message, true)
			return
		}
		s.Log.Warn("cascade stage2 submit failed, will retry", "task", t.TaskID, "err", err)
		return
	}
	cm.Stage = 2
	cm.S2ChannelID = s2Ch.ID
	cm.S2CredentialID = credID
	cm.S2TaskID = s2TaskID
	cm.S2SubmittedAt = s.Now().Unix()
	s.persistCascadeMeta(t, meta)
	_ = fwd
}

func (s *Service) pickStage2Channel(s2Model, engine string) (*relay.Channel, error) {
	candidates := []string{s2Model}
	if engine == "tencent" {
		candidates = append(candidates, "tencent-mps-enhance", "tencent_mps")
	} else {
		candidates = append(candidates, "volcengine_media_enhance", "vve-sd", "vve-ft", "vve-pf", "vve-gt")
	}
	for _, m := range candidates {
		if m == "" {
			continue
		}
		if ch, err := relay.PickChannelForAttempt(s.DB, m, 0, nil, nil); err == nil {
			return ch, nil
		}
	}
	var fallback relay.Channel
	providerFilter := "volcengine_media_enhance"
	if engine == "tencent" {
		providerFilter = "tencent_mps"
	}
	if err := s.DB.Where("provider = ? AND status = 1 AND owner_org_id = 0", providerFilter).
		Order("priority DESC, id ASC").First(&fallback).Error; err == nil {
		return &fallback, nil
	}
	return nil, relay.ErrNoChannel
}

func buildCascadeS2Forward(cm *cascadeTaskMeta) *forward.ResolvedForward {
	if cm.Engine == "tencent" {
		return &forward.ResolvedForward{
			TargetType:   "volcengine_media_enhance",
			UpstreamPath: cmp.Or(cm.S2UpstreamPath, "/"),
			PollPath:     "/",
			AuthType:     forward.AuthBearer,
		}
	}
	return &forward.ResolvedForward{
		TargetType:   "volcengine_media_enhance",
		UpstreamPath: cmp.Or(cm.S2UpstreamPath, "/api/v1/tools/enhance-video"),
		PollPath:     "/api/v1/tasks/${task_id}",
		AuthType:     forward.AuthBearer,
	}
}

func (s *Service) stage2Adaptor(engine string) Adaptor {
	_ = engine
	return volcMediaEnhanceTask{}
}

func (s *Service) resolveChannelCredential(ch *relay.Channel, credID uint64) (string, bool) {
	if ch == nil || s.Keyring == nil {
		return "", false
	}
	credEnc, credOK := ch.CredentialEnc, ch.CredentialEnc != ""
	if credID != 0 {
		credEnc, credOK = relay.CredentialByID(s.DB, credID)
	}
	if !credOK {
		return "", false
	}
	plain, err := s.Keyring.Decrypt(credEnc)
	if err != nil {
		return "", false
	}
	return plain, true
}

func (s *Service) scrubCascadeAndSettleSuccess(
	t *Task,
	meta *taskRequestMeta,
	finalVideoURL string,
	lastFrameURL string,
	scaledTokens int64,
) {
	// 终态落库前清空内部 S1 临时链接与 S2 渠道信息，只保留对外安全字段（target_resolution/res_mul/last_frame_url）。
	if meta.Cascade != nil {
		meta.Cascade.S1VideoURL = ""
		meta.Cascade.S2ChannelID = 0
		meta.Cascade.S2CredentialID = 0
		meta.Cascade.S2TaskID = ""
	}
	if lastFrameURL != "" {
		meta.LastFrameURL = lastFrameURL
	}
	if b, err := json.Marshal(meta); err == nil {
		_ = s.DB.Model(&Task{}).Where("id = ?", t.ID).Update("request_meta_json", string(b)).Error
		t.RequestMetaJSON = string(b)
	}
	s.settleSuccess(t, QueryResult{
		Status:           StatusSucceeded,
		ResultURL:        finalVideoURL,
		LastFrameURL:     lastFrameURL,
		CompletionTokens: scaledTokens,
	})
}
