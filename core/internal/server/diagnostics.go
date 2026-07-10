package server

// Diagnostics Key 检测工具站（§4.8，批次 7）：用户提交自己的厂商 Key → 检测有效性/延迟/模型列表。
//
// 红线 7：被测 key 只在内存中用于本次探测，**绝不落库、绝不进日志、绝不回显**。
//   - 本 handler 不 log 请求体/key；错误只用通用文案（key/上游响应体不入 message）。
//   - relay.CheckKey 全程同步无落盘；返回的 DiagResult 结构里没有 key 字段。
// SSRF：provider 白名单 + custom base_url 强制 https + ValidateAdvancedUpstreamURL(false)；
//   转发恒经拨号守卫（绝不放行私网）。abuse：authed 门 + authRateLimit（per-IP）+ body 上限。

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
)

// checkKey：POST /api/diagnostics/check {provider, api_key, base_url?} —— 同步检测。
func (s *Server) checkKey(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		BaseURL  string `json:"base_url"`
	}
	// 绑定失败只回通用错误——绝不 log 请求体（含 key）。
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.Provider == "" || req.APIKey == "" {
		fail(c, http.StatusBadRequest, "provider and api_key are required")
		return
	}
	// custom base_url：强制 https + 端口 443-only + SSRF 配置期校验（与 BYOK 同口径，绝不 allow-private）。
	// 443-only（对抗 review）：诊断合法场景是标准 HTTPS 端点/区域域名，无需非标端口——直接消灭任意端口扫描。
	if req.BaseURL != "" {
		u, err := url.Parse(req.BaseURL)
		if err != nil || u.Scheme != "https" {
			fail(c, http.StatusBadRequest, "base_url must be https")
			return
		}
		if p := u.Port(); p != "" && p != "443" {
			fail(c, http.StatusBadRequest, "base_url must use the standard https port (443)")
			return
		}
		if err := relay.ValidateAdvancedUpstreamURL(req.BaseURL, false); err != nil {
			fail(c, http.StatusBadRequest, "base_url rejected: "+err.Error())
			return
		}
	}
	// per-user 配额（对抗 review）：per-IP authRateLimit 对代理池轮换无效——以 userID 为键的账号侧封顶，
	// 挡"用平台出口 IP 洗白撞库/规模化 key 有效性探测"。claims 已由 authRequired 注入。
	claims := mustClaims(c)
	if !s.diagLimiter.allow("u:"+strconv.FormatUint(claims.UserID, 10), s.now()) {
		c.Header("Retry-After", "30")
		fail(c, http.StatusTooManyRequests, "diagnostics quota exceeded, slow down")
		return
	}
	// 探测超时封顶（handler 侧兜底，防单请求长挂）。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	res := s.relay.CheckKey(ctx, req.APIKey, req.BaseURL, req.Provider)
	// req.APIKey 在此后不再被引用；不落库、不入日志、不回显（DiagResult 无 key 字段）。
	c.JSON(http.StatusOK, res)
}
