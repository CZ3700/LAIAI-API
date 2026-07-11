package task

// /mj/* 方言 HTTP 层（丝滑度批 C-4）。MJ 客户端生态说 midjourney-proxy 协议：
//   POST /mj/submit/:action        → {code:1, description, result:"<平台 task id>"}
//   GET  /mj/task/:id/fetch        → mjproxy 任务对象（上游透传 + id 改写为平台 id）
//   POST /mj/task/list-by-condition → {ids:[...]} 批量状态（DB 合成，不放大上游）
//   GET  /mj/image/:id             → 产物图代理（Discord CDN 区域不可达时同源下发）
//
// 鉴权：mj-api-secret 头（方言标准）或 Authorization Bearer（平台通用），同一把平台 API Key。
// 错误面：鉴权失败走 HTTP 401；业务失败回 HTTP 200 + code≠1 + description（MJ 客户端
// 生态判 code===1，非 200 处理不一）。资金语义与任务骨架一致：提交失败无任务不收费。

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// mjResp：mjproxy 提交类响应形态。
func mjResp(c *gin.Context, code int, description, result string) {
	c.JSON(http.StatusOK, gin.H{"code": code, "description": description, "result": result})
}

// authKeyMJ：mj-api-secret 头优先（方言标准），回退 Bearer。
func (s *Service) authKeyMJ(c *gin.Context) (*identity.APIKey, bool) {
	if secret := c.GetHeader("mj-api-secret"); secret != "" {
		key, err := identity.VerifyKey(s.DB, secret, s.Now())
		if err != nil {
			taskErr(c, http.StatusUnauthorized, "invalid api key")
			return nil, false
		}
		return key, true
	}
	return s.authKey(c)
}

// HandleMJSubmit：POST /mj/submit/:action。
func (s *Service) HandleMJSubmit(c *gin.Context) {
	action := c.Param("action")
	if !mjActions[action] {
		mjResp(c, 4, "unknown action: "+action, "")
		return
	}
	key, ok := s.authKeyMJ(c)
	if !ok {
		return
	}
	// MJ 是图像生成：与 images 同一功能门。
	if ent, eerr := identity.LoadEntitlements(s.DB, key.OrgID); eerr == nil && !ent.FeatureImages {
		mjResp(c, 4, "image generation is not enabled for this organization", "")
		return
	}
	var params map[string]any
	if err := c.ShouldBindJSON(&params); err != nil {
		mjResp(c, 4, "request body must be a json object", "")
		return
	}
	model := mjModelPrefix + action
	if s.Gate == nil {
		s.Log.Error("task admission gate not wired — refusing request")
		mjResp(c, 4, "server misconfigured", "")
		return
	}
	if _, msg, allowed := s.Gate.GateKey(key, c.ClientIP(), model); !allowed {
		mjResp(c, 4, msg, "")
		return
	}
	if allowed, _ := s.Gate.AllowModelRate(key.OrgID, model); !allowed {
		mjResp(c, 23, "rate limit exceeded, slow down", "") // 23=队列已满（方言语义最近）
		return
	}
	// change/modal 引用既有任务：客户端带的是平台 task id，换成上游 MJ id 再透传
	// （org 隔离谓词内查询——不能拿别家 org 的任务续操作，红线 6）。
	// 同时钉回原任务渠道（对抗 review CONFIRMED）：上游任务 id 是单个 mjproxy 网关本地的，
	// 多渠道部署下独立选路会把续操作投给不持有该任务的网关。
	var pinChannel uint64
	if tid, ok := params["taskId"].(string); ok && tid != "" {
		ref, err := s.Get(key.OrgID, tid)
		if err != nil || ref.UpstreamTaskID == "" {
			mjResp(c, 4, "referenced task not found", "")
			return
		}
		params["taskId"] = ref.UpstreamTaskID
		pinChannel = ref.ChannelID
	}
	t, err := s.Submit(c.Request.Context(), SubmitInput{
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model: model, Params: params, PinChannelID: pinChannel,
	})
	var rejected *UpstreamRejectedError
	switch {
	case errors.As(err, &rejected):
		mjResp(c, 4, clipReason("upstream rejected: "+rejected.Message, 300), "")
	case errors.Is(err, relay.ErrModelNotPriced) || errors.Is(err, relay.ErrNoChannel):
		mjResp(c, 4, "action not available (model "+model+" not priced or no channel)", "")
	case errors.Is(err, ErrConcurrencyLimit):
		mjResp(c, 23, "concurrency limit reached, retry later", "")
	case errors.Is(err, ErrKeyQuotaExceeded):
		mjResp(c, 4, "this api key has reached its quota limit", "")
	case errors.Is(err, ledger.ErrInsufficientBalance):
		mjResp(c, 4, "insufficient balance", "")
	case err != nil:
		s.Log.Error("mj submit failed", "err", summarizeSubmitErr(err))
		mjResp(c, 4, "submit failed", "")
	default:
		mjResp(c, 1, "提交成功", t.TaskID)
	}
}

// HandleMJFetch：GET /mj/task/:id/fetch。上游实时透传（保 buttons/progress 等全量字段，
// MJ 客户端交互依赖），id 字段改写为平台 id；上游不可达/未提交时按任务行合成兜底。
func (s *Service) HandleMJFetch(c *gin.Context) {
	key, ok := s.authKeyMJ(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	t, err := s.Get(key.OrgID, c.Param("id")) // org 隔离谓词（红线 6）
	if err != nil {
		taskErr(c, http.StatusNotFound, "task not found")
		return
	}
	if body, ok := s.mjProxyFetch(c, t); ok {
		c.Data(http.StatusOK, "application/json", body)
		return
	}
	c.JSON(http.StatusOK, mjTaskShape(t))
}

// mjProxyFetch：用任务钉住的渠道凭证实时拉上游 fetch，id 改写为平台 id。失败 (nil,false)。
func (s *Service) mjProxyFetch(c *gin.Context, t *Task) ([]byte, bool) {
	if t.UpstreamTaskID == "" {
		return nil, false
	}
	ch := &relay.Channel{}
	if err := s.DB.First(ch, t.ChannelID).Error; err != nil {
		return nil, false
	}
	credEnc, credOK := ch.CredentialEnc, ch.CredentialEnc != ""
	if t.CredentialID != 0 {
		credEnc, credOK = relay.CredentialByID(s.DB, t.CredentialID)
	}
	if !credOK {
		return nil, false
	}
	credential, err := s.Keyring.Decrypt(credEnc)
	if err != nil {
		return nil, false
	}
	req, err := midjourneyTask{}.QueryRequest(c.Request.Context(), ch, credential, t.UpstreamTaskID)
	if err != nil {
		return nil, false
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return nil, false
	}
	obj["id"] = t.TaskID // 对外恒平台 id（客户端拿它继续 change/modal，绝不泄上游 id 命名域）
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}

// mjTaskShape：任务行 → mjproxy 任务对象（上游不可达时的兜底合成；字段够客户端轮询判终态）。
func mjTaskShape(t *Task) map[string]any {
	status := map[string]string{
		StatusQueued: "SUBMITTED", StatusRunning: "IN_PROGRESS",
		StatusSucceeded: "SUCCESS", StatusFailed: "FAILURE",
	}[t.Status]
	progress := ""
	if t.Status == StatusSucceeded {
		progress = "100%"
	}
	shape := map[string]any{
		"id": t.TaskID, "status": status, "progress": progress,
		"imageUrl": t.ResultURL, "failReason": t.FailReason,
		"submitTime": t.SubmittedAt.UnixMilli(),
	}
	if t.FinishedAt != nil {
		shape["finishTime"] = t.FinishedAt.UnixMilli()
	}
	return shape
}

// HandleMJList：POST /mj/task/list-by-condition {ids:[...]}。DB 合成（不放大上游），上限 30。
func (s *Service) HandleMJList(c *gin.Context) {
	key, ok := s.authKeyMJ(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusOK, []any{})
		return
	}
	if len(req.IDs) > 30 {
		req.IDs = req.IDs[:30]
	}
	out := make([]map[string]any, 0, len(req.IDs))
	for _, id := range req.IDs {
		if t, err := s.Get(key.OrgID, id); err == nil { // org 隔离逐条强制
			out = append(out, mjTaskShape(t))
		}
	}
	c.JSON(http.StatusOK, out)
}

// HandleMJImage：GET /mj/image/:id——产物图同源代理（Discord CDN 部分区域不可达/防混合内容，
// 与视频 content 代理同款 SSRF 守卫）。仅 succeeded 且有直链的任务。
func (s *Service) HandleMJImage(c *gin.Context) {
	key, ok := s.authKeyMJ(c)
	if !ok {
		return
	}
	if !key.AllowsIP(c.ClientIP()) {
		taskErr(c, http.StatusForbidden, "this api key is not allowed from your ip address")
		return
	}
	t, err := s.Get(key.OrgID, c.Param("id"))
	if err != nil {
		taskErr(c, http.StatusNotFound, "task not found")
		return
	}
	if t.Status != StatusSucceeded || (!strings.HasPrefix(t.ResultURL, "http://") && !strings.HasPrefix(t.ResultURL, "https://")) {
		taskErr(c, http.StatusNotFound, "no image for this task")
		return
	}
	guarded := relay.NewGuardedClient(2*time.Minute, relay.AllowPrivateFromDB(s.DB))
	s.proxyDirectContent(c, t, guarded)
}
