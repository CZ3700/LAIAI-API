package task

import (
	"errors"
	"net/http"
	"strings"

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
