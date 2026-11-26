package task

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

// authKeyFlexible 支持 Bearer <key>、Token <key>（Vidu SDK）及 x-api-key 请求头。
func (s *Service) authKeyFlexible(c *gin.Context) (*identity.APIKey, bool) {
	raw := ""
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	switch {
	case strings.HasPrefix(auth, "Bearer "):
		raw = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	case strings.HasPrefix(auth, "Token "):
		raw = strings.TrimSpace(strings.TrimPrefix(auth, "Token "))
	case strings.TrimSpace(c.GetHeader("x-api-key")) != "":
		raw = strings.TrimSpace(c.GetHeader("x-api-key"))
	}
	if raw == "" {
		taskErr(c, http.StatusUnauthorized, "missing api key")
		return nil, false
	}
	key, err := identity.VerifyKey(s.DB, raw, s.Now())
	if err != nil {
		taskErr(c, http.StatusUnauthorized, "invalid api key")
		return nil, false
	}
	return key, true
}

func (s *Service) gateVideoCreate(c *gin.Context, key *identity.APIKey, model string) bool {
	if ent, err := identity.LoadEntitlements(s.DB, key.OrgID); err == nil && !ent.FeatureVideo {
		taskErr(c, http.StatusForbidden, "video generation is not enabled for this organization")
		return false
	}
	if s.Gate == nil {
		s.Log.Error("task admission gate not wired — refusing request")
		taskErr(c, http.StatusInternalServerError, "server misconfigured")
		return false
	}
	if code, msg, allowed := s.Gate.GateKey(key, c.ClientIP(), model); !allowed {
		taskErr(c, code, msg)
		return false
	}
	if allowed, _ := s.Gate.AllowModelRate(key.OrgID, model); !allowed {
		c.Header("Retry-After", "60")
		taskErr(c, http.StatusTooManyRequests, "model rate limit exceeded, slow down")
		return false
	}
	return true
}

// AuthKeyFlexible 对外暴露多 header API Key 鉴权（Bearer / Token / x-api-key）。
func (s *Service) AuthKeyFlexible(c *gin.Context) (*identity.APIKey, bool) {
	return s.authKeyFlexible(c)
}

func (s *Service) writeSubmitError(c *gin.Context, err error) {
	if assetErr, ok := errors.AsType[*AssetConvertError](err); ok {
		taskErr(c, http.StatusBadRequest, clipReason(assetErr.Message, 300))
		return
	}
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
	default:
		s.Log.Error("native task submit failed", "err", summarizeSubmitErr(err))
		taskErr(c, http.StatusBadGateway, "task submit failed")
	}
}

func readJSONBodyMap(c *gin.Context) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, relay.MaxRequestBodySize))
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	var m map[string]any
	if err := c.ShouldBindJSON(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func taskToOfficialView(t *Task) forward.OfficialTaskView {
	meta := parseTaskRequestMeta(t.RequestMetaJSON)
	return forward.OfficialTaskView{
		TaskID:           t.TaskID,
		Model:            t.Model,
		Status:           t.Status,
		Progress:         t.Progress,
		ResultURL:        t.ResultURL,
		LastFrameURL:     meta.LastFrameURL,
		CompletionTokens: t.ActualTokens,
		CostUSD:          t.CostUSD,
		BillingDetail:    t.BillingDetail,
		ForwardRuleEID:   t.ForwardRuleEID,
		BillingRulePID:   t.BillingRulePID,
		FailReason:       t.FailReason,
		SubmittedAt:      t.SubmittedAt,
		StartedAt:        t.StartedAt,
		FinishedAt:       t.FinishedAt,
		Resolution:       meta.Resolution,
		Ratio:            meta.Ratio,
		Duration:         int64(meta.Duration),
		Draft:            meta.Draft,
		GenerateAudio:    meta.GenerateAudio,
		ServiceTier:      meta.ServiceTier,
		ViduType:         meta.ViduType,
	}
}

func contentFromAnyMap(all map[string]any) []any {
	if c, ok := all["content"].([]any); ok && len(c) > 0 {
		return c
	}
	if contents, ok := all["contents"].([]any); ok && len(contents) > 0 {
		out := make([]any, 0, len(contents))
		for _, raw := range contents {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ty, _ := m["type"].(string)
			u, _ := m["url"].(string)
			switch ty {
			case "prompt", "text":
				if txt, ok := m["text"].(string); ok {
					out = append(out, map[string]any{"type": "text", "text": txt})
				}
			case "first_frame", "last_frame", "middle_frame", "refer_image", "image_url":
				if u != "" {
					out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}, "role": ty})
				}
			case "base_video", "feature_video", "video_url":
				if u != "" {
					out = append(out, map[string]any{"type": "video_url", "video_url": map[string]any{"url": u}, "role": ty})
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var out []any
	if p, ok := all["prompt"].(string); ok && strings.TrimSpace(p) != "" {
		out = append(out, map[string]any{"type": "text", "text": strings.TrimSpace(p)})
	}
	if inp, ok := all["input"].(map[string]any); ok {
		if p, ok := inp["prompt"].(string); ok && strings.TrimSpace(p) != "" {
			out = append(out, map[string]any{"type": "text", "text": strings.TrimSpace(p)})
		}
		if img, ok := inp["img_url"].(string); ok && strings.TrimSpace(img) != "" {
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": strings.TrimSpace(img)}})
		}
	}
	for _, u := range forward.CollectURLs(all, []string{"images", "image_urls", "image_url", "image"}) {
		out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
	}
	for _, u := range forward.CollectURLs(all, []string{"videos", "video_urls", "video_url"}) {
		out = append(out, map[string]any{"type": "video_url", "video_url": map[string]any{"url": u}})
	}
	if len(out) == 0 {
		out = []any{map[string]any{"type": "text", "text": ""}}
	}
	return out
}

// ── 火山方舟 /api/v3/contents/generations/tasks 列表查询 ──────────────────

// HandleArkNativeList 处理 GET /api/v3/contents/generations/tasks（租户隔离分页列表）。
func (s *Service) HandleArkNativeList(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	pageNum, _ := strconv.Atoi(cmp.Or(c.Query("page_num"), "1"))
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("page_size"), "20"))
	pageNum = max(1, pageNum)
	pageSize = min(100, max(1, pageSize))

	q := s.DB.WithContext(c.Request.Context()).Model(&Task{}).Where("org_id = ?", key.OrgID)
	if status := strings.TrimSpace(c.Query("filter.status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if model := strings.TrimSpace(c.Query("filter.model")); model != "" {
		q = q.Where("model = ?", model)
	}
	var total int64
	_ = q.Count(&total).Error
	rows := []Task{}
	if err := q.Order("id DESC").Offset((pageNum - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		taskErr(c, http.StatusInternalServerError, "task list failed")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		items = append(items, forward.WrapVolcengineQuery(taskToOfficialView(&rows[i])))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// ── Vidu 官方 /ent/v2/* ──────────────────

// HandleViduNativeCreate 处理 POST /ent/v2/{text2video,img2video,start-end2video,reference2video}。
func (s *Service) HandleViduNativeCreate(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	all, err := readJSONBodyMap(c)
	if err != nil {
		taskErr(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	model, _ := all["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		taskErr(c, http.StatusBadRequest, "model is required")
		return
	}
	if !s.gateVideoCreate(c, key, model) {
		return
	}
	rawCopy := maps.Clone(all)
	delete(all, "model")
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model:       model,
		Content:     contentFromAnyMap(all),
		Params:      all,
		RawBody:     rawCopy,
		RequestPath: c.Request.URL.Path,
		InboundPath: c.Request.URL.Path,
	})
	if err != nil {
		s.writeSubmitError(c, err)
		return
	}
	view := taskToOfficialView(t)
	view.ViduType = forward.InferViduType(c.Request.URL.Path)
	c.JSON(http.StatusOK, forward.WrapViduCreate(view, rawCopy))
}

// HandleViduNativeGet 处理 GET /ent/v2/tasks/:id/creations。
func (s *Service) HandleViduNativeGet(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
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
	c.JSON(http.StatusOK, forward.WrapViduQuery(taskToOfficialView(t)))
}

// ── MiniMax V2 原生 /v2/video_generation & /v2/query/video_generation/:id ──────────────────

// HandleMiniMaxV2NativeCreate 处理 POST /v2/video_generation。
func (s *Service) HandleMiniMaxV2NativeCreate(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	all, err := readJSONBodyMap(c)
	if err != nil {
		taskErr(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	model, _ := all["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		taskErr(c, http.StatusBadRequest, "model is required")
		return
	}
	if !s.gateVideoCreate(c, key, model) {
		return
	}
	rawCopy := maps.Clone(all)
	delete(all, "model")
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model:       model,
		Content:     contentFromAnyMap(all),
		Params:      all,
		RawBody:     rawCopy,
		RequestPath: c.Request.URL.Path,
		InboundPath: c.Request.URL.Path,
	})
	if err != nil {
		s.writeSubmitError(c, err)
		return
	}
	c.JSON(http.StatusOK, forward.WrapMiniMaxV2Create(taskToOfficialView(t)))
}

// HandleMiniMaxV2NativeGet 处理 GET /v2/query/video_generation/:id 与 ?task_id=...。
func (s *Service) HandleMiniMaxV2NativeGet(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	id := cmp.Or(strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Query("task_id")))
	t, err := s.Get(key.OrgID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			taskErr(c, http.StatusNotFound, "task not found")
			return
		}
		taskErr(c, http.StatusInternalServerError, "task query failed")
		return
	}
	c.JSON(http.StatusOK, forward.WrapMiniMaxV2Query(taskToOfficialView(t)))
}

// ── 可灵 3.0 / Omni 原生 /{text-to-video,image-to-video,omni-video}/:model_id & /tasks ──────────────────

// HandleKlingV3NativeCreate 处理 POST /text-to-video/:model_id 等可灵 3.0 原生入口。
func (s *Service) HandleKlingV3NativeCreate(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	all, err := readJSONBodyMap(c)
	if err != nil {
		taskErr(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	model := cmp.Or(strings.TrimSpace(c.Param("model_id")), firstStrFromMap(all, "model"))
	if model == "" {
		taskErr(c, http.StatusBadRequest, "model is required")
		return
	}
	if !s.gateVideoCreate(c, key, model) {
		return
	}
	rawCopy := maps.Clone(all)
	delete(all, "model")
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model:       model,
		Content:     contentFromAnyMap(all),
		Params:      all,
		RawBody:     rawCopy,
		RequestPath: c.Request.URL.Path,
		InboundPath: c.Request.URL.Path,
	})
	if err != nil {
		s.writeSubmitError(c, err)
		return
	}
	c.JSON(http.StatusOK, forward.WrapKlingV3Create(taskToOfficialView(t)))
}

// HandleKlingV3NativeList 处理 GET /tasks?task_ids=id1,id2。
func (s *Service) HandleKlingV3NativeList(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	rawIDs := cmp.Or(strings.TrimSpace(c.Query("task_ids")), strings.TrimSpace(c.Query("id")))
	if rawIDs == "" {
		taskErr(c, http.StatusBadRequest, "task_ids query parameter is required")
		return
	}
	ids := make([]string, 0, 4)
	for seg := range strings.SplitSeq(rawIDs, ",") {
		if trimmed := strings.TrimSpace(seg); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	if len(ids) == 0 {
		taskErr(c, http.StatusBadRequest, "task_ids query parameter is required")
		return
	}
	rows := []Task{}
	if err := s.DB.WithContext(c.Request.Context()).
		Where("org_id = ? AND task_id IN ?", key.OrgID, ids).
		Find(&rows).Error; err != nil {
		taskErr(c, http.StatusInternalServerError, "task query failed")
		return
	}
	if len(rows) == 0 {
		taskErr(c, http.StatusNotFound, "task not found")
		return
	}
	views := make([]forward.OfficialTaskView, 0, len(rows))
	for i := range rows {
		views = append(views, taskToOfficialView(&rows[i]))
	}
	c.JSON(http.StatusOK, forward.WrapKlingV3Query(views))
}

// ── 阿里百炼 DashScope 原生 /api/v1/services/aigc/video-generation/video-synthesis & /api/v1/tasks/:id ──────────

// HandleDashScopeNativeCreate 处理 POST /api/v1/services/aigc/video-generation/video-synthesis。
func (s *Service) HandleDashScopeNativeCreate(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	all, err := readJSONBodyMap(c)
	if err != nil {
		taskErr(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	model := firstStrFromMap(all, "model")
	if model == "" {
		taskErr(c, http.StatusBadRequest, "model is required")
		return
	}
	if !s.gateVideoCreate(c, key, model) {
		return
	}
	rawCopy := maps.Clone(all)
	params := maps.Clone(all)
	if dsParams, ok := all["parameters"].(map[string]any); ok {
		for k, v := range dsParams {
			params[k] = v
		}
	}
	delete(params, "model")
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model:       model,
		Content:     contentFromAnyMap(all),
		Params:      params,
		RawBody:     rawCopy,
		RequestPath: c.Request.URL.Path,
		InboundPath: c.Request.URL.Path,
	})
	if err != nil {
		s.writeSubmitError(c, err)
		return
	}
	c.JSON(http.StatusOK, forward.WrapDashScopeCreate(taskToOfficialView(t)))
}

// HandleDashScopeNativeGet 处理 GET /api/v1/tasks/:id（按任务所属原始入站路径自动回包 DashScope / OpenAI）。
func (s *Service) HandleDashScopeNativeGet(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
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
	c.JSON(http.StatusOK, forward.WrapDashScopeQuery(taskToOfficialView(t)))
}

// HandleMediaKitNativeCreate 处理 POST /api/v1/tools/enhance-video*（火山 MediaKit 画质增强）。
func (s *Service) HandleMediaKitNativeCreate(c *gin.Context) {
	key, ok := s.authKeyFlexible(c)
	if !ok {
		return
	}
	all, err := readJSONBodyMap(c)
	if err != nil {
		taskErr(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	model := cmp.Or(firstStrFromMap(all, "model"), "vve-sd")
	if !s.gateVideoCreate(c, key, model) {
		return
	}
	rawCopy := maps.Clone(all)
	delete(all, "model")
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model:       model,
		Content:     contentFromAnyMap(all),
		Params:      all,
		RawBody:     rawCopy,
		RequestPath: c.Request.URL.Path,
		InboundPath: c.Request.URL.Path,
	})
	if err != nil {
		s.writeSubmitError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"task_id": t.TaskID, "status": "pending"})
}

func firstStrFromMap(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// HandleVideosWildcardGet 分发 GET /v1/videos/*rest（兼容 /v1/videos/:id、/v1/videos/:id/content、/v1/videos/generations/:id），
// 避免 Gin 静态子路径 /generations 与通配参数 /:id 在同一前缀冲突。
func (s *Service) HandleVideosWildcardGet(c *gin.Context) {
	rest := strings.Trim(c.Param("rest"), "/")
	if rest == "" {
		taskErr(c, http.StatusNotFound, "task not found")
		return
	}
	rest = strings.TrimPrefix(rest, "generations/")
	if id, ok := strings.CutSuffix(rest, "/content"); ok && id != "" {
		c.Params = append(c.Params, gin.Param{Key: "id", Value: id})
		s.HandleContent(c)
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "id", Value: rest})
	s.HandleGet(c)
}

// HandleVideosWildcardDelete 分发 DELETE /v1/videos/*rest（兼容 /v1/videos/:id 与 /v1/videos/generations/:id）。
func (s *Service) HandleVideosWildcardDelete(c *gin.Context) {
	rest := strings.Trim(c.Param("rest"), "/")
	rest = strings.TrimPrefix(rest, "generations/")
	if rest == "" {
		taskErr(c, http.StatusNotFound, "task not found")
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "id", Value: rest})
	s.HandleCancel(c)
}
