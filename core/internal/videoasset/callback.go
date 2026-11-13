package videoasset

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"strings"
	"time"
)

const PortraitCallbackPath = "/v1/video/portrait-callback"

func validPlatformCallback(raw string) bool {
	if !validHTTPS(raw, 2048) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Path == PortraitCallbackPath && u.RawQuery == "" && u.Fragment == ""
}

type callbackClaims struct {
	OrgID                        uint64
	SessionID, TargetID, Version string
	Expires                      int64
	// Redirect：创建会话时客户直接提交的 HTTPS 回跳地址（2026-11-13 起为
	// 主通道），随 HMAC 签名绑定进 state——回跳时直接重定向到这里，第三方
	// 无法篡改，state 一次性消费 + 30 分钟过期。旧版 state 无此字段（预
	// 登记时代），回退到 TargetID 登记解析。
	Redirect string
}

func (s *Service) callbackURL(ctx context.Context, op Operation, redirect string) (string, error) {
	if !validPlatformCallback(s.options.PlatformCallbackURL) {
		return "", ErrUnsupported
	}
	var token string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, op.OrgID); err != nil {
			return err
		}
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if p.SessionStatus != "creating" || p.CallbackTargetID == "" || len(s.options.HMACKeys[op.HMACKeyVersion]) == 0 {
			return ErrConflict
		}
		claims := callbackClaims{OrgID: op.OrgID, SessionID: p.ID, TargetID: p.CallbackTargetID, Version: op.HMACKeyVersion, Expires: op.UpdatedAt.Add(30 * time.Minute).Unix()}
		if validHTTPS(redirect, 2048) {
			claims.Redirect = redirect
		}
		payload := base64.RawURLEncoding.EncodeToString(bodyJSON(claims))
		signature := hex.EncodeToString(mac(s.options.HMACKeys[claims.Version], "portrait-callback-v1:"+payload))
		token = payload + "." + signature
		digest := s.digest(claims.Version, "callback-state", op.OrgID, p.ID, token)
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", op.OrgID, p.ID).Updates(map[string]any{"callback_state_hash": digest, "callback_state_version": claims.Version}).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, op.OrgID, "portrait", p.ID, op.ID, "callback_bound", "", op.LeaseOwner)
	})
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(s.options.PlatformCallbackURL)
	q := u.Query()
	q.Set("state", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (s *Service) parseCallbackState(raw string) (callbackClaims, error) {
	if len(raw) > 4096 {
		return callbackClaims{}, ErrInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return callbackClaims{}, ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return callbackClaims{}, ErrInvalid
	}
	var claims callbackClaims
	if json.Unmarshal(body, &claims) != nil || claims.OrgID == 0 || !validID(claims.SessionID) || !validID(claims.TargetID) || len(s.options.HMACKeys[claims.Version]) == 0 {
		return callbackClaims{}, ErrInvalid
	}
	signature, err := hex.DecodeString(parts[1])
	expected := mac(s.options.HMACKeys[claims.Version], "portrait-callback-v1:"+parts[0])
	if err != nil || !hmac.Equal(signature, expected) || claims.Expires <= s.now().Unix() {
		return callbackClaims{}, ErrInvalid
	}
	return claims, nil
}

// callbackHTML 渲染回调入口的浏览器友好页（火山 H5 跳转终点是浏览器，
// 绝不吐 API JSON 信封）。无外部资源（CSP default-src 'self' 下内联样式可用）。
func writeCallbackHTML(c *gin.Context, status int, heading, detail string) {
	c.Data(status, "text/html; charset=utf-8", []byte(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>`+heading+`</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;display:flex;min-height:100dvh;align-items:center;justify-content:center;margin:0;background:#f6f8fa;color:#17212f}
.card{background:#fff;border:1px solid #e4e7ec;border-radius:12px;padding:40px 32px;max-width:420px;text-align:center;box-shadow:0 8px 32px rgba(15,23,42,.06)}
h1{font-size:20px;margin:0 0 12px}p{font-size:14px;color:#64748b;line-height:1.7;margin:0}</style></head>
<body><div class="card"><h1>`+heading+`</h1><p>`+detail+`</p></div></body></html>`))
}

// HandlePortraitCallback must be mounted at PortraitCallbackPath OUTSIDE the
// Gateway Key middleware. Signed state permits one redirect/verification hint,
// never authentication success or a business license. Ignore provider/client
// resultCode, GroupId and arbitrary redirect parameters completely.
// Browser-facing exits render the friendly HTML page; the only redirect goes
// to the customer-submitted HTTPS address bound inside the signed state
// (legacy states fall back to the org's registered return page).
func (s *Service) HandlePortraitCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	raw := c.Query("state")
	c.Request.URL.RawQuery = ""
	invalidPage := func() {
		writeCallbackHTML(c, http.StatusBadRequest, "认证链接无效或已过期", "该认证回跳链接无效、已过期或已被使用。请返回控制台刷新查看认证状态，无需重复操作。")
	}
	unavailablePage := func() {
		writeCallbackHTML(c, http.StatusServiceUnavailable, "暂时无法处理认证回跳", "系统繁忙，本次回跳未能确认。请稍后返回控制台刷新查看认证状态。")
	}
	claims, err := s.parseCallbackState(raw)
	if err != nil || s.options.ResolveCallbackByID == nil {
		invalidPage()
		return
	}
	// 回跳目标（2026-11-13 起主通道）：state 内 HMAC 绑定的客户提交地址；
	// 旧版 state（预登记时代）无 Redirect，回退 TargetID 登记解析。
	targetURL := claims.Redirect
	if !validHTTPS(targetURL, 2048) {
		target, err := s.options.ResolveCallbackByID(c.Request.Context(), claims.OrgID, claims.TargetID)
		if err != nil || target.ID != claims.TargetID || !validHTTPS(target.URL, 2048) {
			invalidPage()
			return
		}
		targetURL = target.URL
	}
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, claims.OrgID); err != nil {
			return err
		}
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", claims.OrgID, claims.SessionID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if p.CallbackTargetID != claims.TargetID || p.CallbackStateVersion != claims.Version || p.CallbackUsedAt != nil || (p.SessionStatus != "pending" && p.SessionStatus != "succeeded") {
			return ErrConflict
		}
		expected := s.digest(claims.Version, "callback-state", claims.OrgID, p.ID, raw)
		if !hmac.Equal([]byte(expected), []byte(p.CallbackStateHash)) {
			return ErrInvalid
		}
		updated := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND callback_used_at IS NULL", claims.OrgID, p.ID).Update("callback_used_at", nowMillis(s))
		if updated.Error != nil {
			return ErrStorage
		}
		if updated.RowsAffected != 1 {
			return ErrConflict
		}
		return s.event(tx, claims.OrgID, "portrait", p.ID, p.CreateOperationID, "callback_consumed", "", "callback_browser")
	})
	if err != nil {
		// state 已消费（H5 偶发重复触发回调）：签名有效，幂等展示固定结束页
		//——不重复消费、不泄露任何信息、也不再跳转。
		if errors.Is(err, ErrConflict) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>真人认证</title><body><h1>真人认证</h1><p>认证流程已结束，请返回应用查询认证结果。</p></body></html>`))
			return
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
			invalidPage()
			return
		}
		unavailablePage()
		return
	}
	// Scheduled GetVisualValidateResult remains the only identity confirmation.
	// 成功消费后不再跳转（避免用户在控制台/应用间连环点击）：直接展示固定
	// 结束页，认证结果一律以平台查询接口为准。
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>真人认证</title><body><h1>真人认证</h1><p>认证流程已结束，请返回应用查询认证结果。</p></body></html>`))
}
