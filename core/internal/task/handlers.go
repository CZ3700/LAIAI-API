package task

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
)

// HTTP 层：POST /v1/video/generations（提交）+ GET /v1/video/generations/:id（查询）。
// 与 relay 同款 API Key 认证；org 隔离在 Get 的查询谓词强制。

type createReq struct {
	Model   string `json:"model" binding:"required"`
	Content []any  `json:"content" binding:"required"`
	// Params：model/content 之外的全部顶层参数，原样透传上游（薄透传纪律——Seedance 2.0 的
	// generate_audio/ratio/duration/watermark/resolution 等任务参数不做二次发明，真机验证 2026-07-05）。
	// 计费安全：任务按上游 usage 结算且 hold 上界按 duration/resolution 感知估算（EstimateOutputTokens），
	// 参数抬高成本会同步抬高预冻结，不会绕过资金闸。
	Params map[string]any `json:"-"`
}

// bindCreateReq：绑定 model/content 必填 + 收集其余顶层参数。gin 的 struct 绑定吃不到未知字段，
// 手动二次解析（body 已被 MaxBytesReader 限制）。
// 大小写语义（对抗 review CONFIRMED）：旧结构体绑定走 encoding/json 大小写不敏感匹配
// （{"Model":...} 曾被受理）——这里保持兼容：精确键优先，变体键回退；且**所有**大小写变体
// 从 Params 剔除（否则 "Model" 变体键随透传进上游体，first-wins 解析器会用它顶掉平台权威值）。
func bindCreateReq(c *gin.Context) (*createReq, error) {
	var all map[string]any
	if err := c.ShouldBindJSON(&all); err != nil {
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
	if req.Model == "" || len(req.Content) == 0 {
		return nil, errMissingModelContent
	}
	if len(all) > 0 {
		req.Params = all
	}
	return req, nil
}

var errMissingModelContent = errors.New("task: model and content are required")

func (s *Service) HandleCreate(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	// Entitlements 功能门（P0-2）：超管可按 org 关闭视频生成。
	if ent, eerr := identity.LoadEntitlements(s.DB, key.OrgID); eerr == nil && !ent.FeatureVideo {
		taskErr(c, http.StatusForbidden, "video generation is not enabled for this organization")
		return
	}
	req, err := bindCreateReq(c)
	if err != nil {
		// 路由挂 MaxBytesReader（指令 P0-2）：超限读中断 → 413 诚实错误，不做大内存分配
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			taskErr(c, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		taskErr(c, http.StatusBadRequest, "model and content are required")
		return
	}
	// 准入统一（指令 P0-1）：与 chat 同一套 Key 精细管控（IP 白名单/累计额度/模型白名单）
	// + (org,model) 模型级限流。Gate 未装配 = 部署错误，fail-closed 拒绝而非放行。
	if s.Gate == nil {
		s.Log.Error("task admission gate not wired — refusing request")
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
		OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID,
		Model: req.Model, Content: req.Content, Params: req.Params,
	})
	var rejected *UpstreamRejectedError
	switch {
	case errors.As(err, &rejected):
		// 上游 400 = 客户端任务参数问题（透传后常态错误面）：带上游解释转 400——
		// 只进客户端响应（与透传上游错误体同性质），不进日志（红线 7）。
		taskErr(c, http.StatusBadRequest, clipReason("upstream rejected task parameters: "+rejected.Message, 300))
	case errors.Is(err, relay.ErrModelNotPriced) || errors.Is(err, relay.ErrNoChannel):
		taskErr(c, http.StatusBadRequest, "model not available")
	case errors.Is(err, ErrConcurrencyLimit):
		c.Header("Retry-After", "10")
		taskErr(c, http.StatusTooManyRequests, "concurrency limit reached, retry later")
	case errors.Is(err, ErrKeyQuotaExceeded):
		taskErr(c, http.StatusPaymentRequired, "this api key has reached its quota limit") // chat 同文案
	case errors.Is(err, ledger.ErrInsufficientBalance):
		taskErr(c, http.StatusPaymentRequired, "insufficient balance")
	case err != nil:
		s.Log.Error("task submit failed", "err", err)
		taskErr(c, http.StatusBadGateway, "task submit failed")
	default:
		c.JSON(http.StatusAccepted, t)
	}
}

func (s *Service) HandleGet(c *gin.Context) {
	key, ok := s.authKey(c)
	if !ok {
		return
	}
	// IP 白名单对读取同样生效（对抗 review）：响应含 result_url/费用等租户数据，
	// 受 allow_ips 限制的 Key 泄露后不得从白名单外读取。模型白名单/quota 与读取无关，不检。
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
	c.JSON(http.StatusOK, t)
}

// HandleContent：GET /v1/video/generations/:id/content —— 产物代理下发（§3.8 批次 4）。
// sora 的产物是上游二进制流、veo 的产物 URI 带渠道 key（直接吐给用户=泄露凭证），
// 平台用任务钉住的渠道凭证实时取流转发。鉴权/org 隔离与 HandleGet 同款；产物下发不额外计费。
func (s *Service) HandleContent(c *gin.Context) {
	key, ok := s.authKey(c)
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
	if t.Status != StatusSucceeded {
		taskErr(c, http.StatusNotFound, "no content for this task")
		return
	}
	// SSRF 守卫（对抗 review CONFIRMED HIGH）：产物 URI 可能是上游返回的不可信绝对 URL——
	// 用带拨号复检 + 跨主机剥凭证的守卫客户端，绝不用裸 s.Client。metadata 恒拦。
	guarded := relay.NewGuardedClient(2*time.Minute, relay.AllowPrivateFromDB(s.DB))

	// 直链产物（seedance/hailuo/wan 等：ResultRef 空、ResultURL 是上游签名直链）——
	// 服务端代理取流 + 修正 Content-Type。修复 Playground 黑屏：TOS/对象存储签名 URL 常返
	// application/octet-stream，浏览器 <video> 认不出拒播；跨域直链前端又 fetch 不到（CORS）。
	// 同源代理统一绕开混合内容/CORS/Content-Type 三坑。
	if t.ResultRef == "" {
		if !strings.HasPrefix(t.ResultURL, "http://") && !strings.HasPrefix(t.ResultURL, "https://") {
			taskErr(c, http.StatusNotFound, "no content for this task")
			return
		}
		s.proxyDirectContent(c, t, guarded)
		return
	}

	ch := &relay.Channel{}
	if err := s.DB.First(ch, t.ChannelID).Error; err != nil {
		taskErr(c, http.StatusBadGateway, "channel unavailable")
		return
	}
	ad, err := adaptorFor(t.Provider)
	if err != nil {
		taskErr(c, http.StatusBadGateway, "provider unavailable")
		return
	}
	streamer, ok := ad.(ContentStreamer)
	if !ok {
		taskErr(c, http.StatusNotFound, "no proxied content for this task")
		return
	}
	// 凭证：任务钉住的 key（与轮询同口径，0035）
	credEnc, credOK := ch.CredentialEnc, ch.CredentialEnc != ""
	if t.CredentialID != 0 {
		credEnc, credOK = relay.CredentialByID(s.DB, t.CredentialID)
	}
	if !credOK {
		taskErr(c, http.StatusBadGateway, "channel credential unavailable")
		return
	}
	credential, err := s.Keyring.Decrypt(credEnc)
	if err != nil {
		taskErr(c, http.StatusBadGateway, "channel credential unavailable")
		return
	}
	rc, contentType, err := streamer.StreamResult(c.Request.Context(), guarded, ch, credential, t.ResultRef)
	if err != nil {
		// err 不进日志（veo URI 带渠道 key，*url.Error 会回显完整 URL——红线 7，对抗 review CONFIRMED）
		s.Log.Warn("task content stream failed", "task", t.TaskID)
		taskErr(c, http.StatusBadGateway, "content fetch failed, retry later")
		return
	}
	defer rc.Close()
	if contentType == "" {
		contentType = "video/mp4"
	}
	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=3600") // 产物含租户数据：private（对标 New API 的 public 有意收紧）
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		s.Log.Warn("task content stream interrupted", "task", t.TaskID, "err", err)
	}
}

// proxyDirectContent：直链产物（ResultRef 空、ResultURL 是上游签名直链）的同源代理下发。
// 服务端 GET 直链（守卫客户端，SSRF-safe）→ 转发字节，Content-Type 修正为 video/mp4（对象存储
// 常返 octet-stream，浏览器 <video> 拒播的根因）。err 不回显（直链带签名，红线 7）。
func (s *Service) proxyDirectContent(c *gin.Context, t *Task, guarded *http.Client) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, t.ResultURL, nil)
	if err != nil {
		taskErr(c, http.StatusBadGateway, "content fetch failed")
		return
	}
	resp, err := guarded.Do(req)
	if err != nil {
		s.Log.Warn("task direct content fetch failed", "task", t.TaskID) // 不带 err（直链含签名，红线 7）
		taskErr(c, http.StatusBadGateway, "content fetch failed, retry later")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		taskErr(c, http.StatusBadGateway, "content unavailable (upstream may have expired)")
		return
	}
	ct := resp.Header.Get("Content-Type")
	// 对象存储签名 URL 常返 application/octet-stream/空 → 浏览器 <video> 认不出；强制为可播视频类型。
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") || strings.HasPrefix(ct, "binary/") {
		ct = "video/mp4"
	}
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "private, max-age=3600")
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		s.Log.Warn("task direct content stream interrupted", "task", t.TaskID, "err", err)
	}
}

func (s *Service) authKey(c *gin.Context) (*identity.APIKey, bool) {
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		taskErr(c, http.StatusUnauthorized, "missing api key")
		return nil, false
	}
	key, err := identity.VerifyKey(s.DB, strings.TrimPrefix(auth, "Bearer "), s.Now())
	if err != nil {
		taskErr(c, http.StatusUnauthorized, "invalid api key")
		return nil, false
	}
	return key, true
}

func taskErr(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": gin.H{"message": msg, "type": "laiai_task_error"}})
}
