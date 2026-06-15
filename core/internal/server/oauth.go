package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

// OAuth 登录（GitHub / Google）。client id/secret 存运营设置（热更新）。
// 流程：start 重定向到 provider → callback 换 token 取 email → 找/建用户 → 跳回前端带 session。

func init() {
	for _, k := range []string{
		"github_client_id", "github_client_secret",
		"google_client_id", "google_client_secret",
		"discord_client_id", "discord_client_secret",
		// OIDC 通用（企业 SSO）：端点由运营按各家 Discovery 填
		"oidc_client_id", "oidc_client_secret",
		"oidc_auth_url", "oidc_token_url", "oidc_userinfo_url", "oidc_scope",
		// Telegram Login Widget（HMAC，非 OAuth2 跳转流；无 email，用合成身份）
		"telegram_bot_token", "telegram_bot_username",
	} {
		settingDefaults[k] = ""
	}
}

// oauthProviderNames：所有可能的 OAuth2 跳转类提供方（Telegram 走专用 widget 流，不在此列）。
var oauthProviderNames = []string{"github", "google", "discord", "oidc"}

type oauthProvider struct {
	authURL, tokenURL, userURL, scope string
	clientID, clientSecret            string
}

func (s *Server) oauthProvider(name string) (*oauthProvider, bool) {
	switch name {
	case "github":
		id := s.getSetting("github_client_id")
		if id == "" {
			return nil, false
		}
		return &oauthProvider{
			authURL:      "https://github.com/login/oauth/authorize",
			tokenURL:     "https://github.com/login/oauth/access_token",
			userURL:      "https://api.github.com/user/emails",
			scope:        "user:email",
			clientID:     id,
			clientSecret: s.getSetting("github_client_secret"),
		}, true
	case "google":
		id := s.getSetting("google_client_id")
		if id == "" {
			return nil, false
		}
		return &oauthProvider{
			authURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			tokenURL:     "https://oauth2.googleapis.com/token",
			userURL:      "https://www.googleapis.com/oauth2/v2/userinfo",
			scope:        "openid email",
			clientID:     id,
			clientSecret: s.getSetting("google_client_secret"),
		}, true
	case "discord":
		id := s.getSetting("discord_client_id")
		if id == "" {
			return nil, false
		}
		return &oauthProvider{
			authURL:      "https://discord.com/oauth2/authorize",
			tokenURL:     "https://discord.com/api/oauth2/token",
			userURL:      "https://discord.com/api/users/@me",
			scope:        "identify email",
			clientID:     id,
			clientSecret: s.getSetting("discord_client_secret"),
		}, true
	case "oidc":
		// 通用 OIDC：端点全部来自运营设置（各家 Discovery 不同）
		id := s.getSetting("oidc_client_id")
		auth := s.getSetting("oidc_auth_url")
		if id == "" || auth == "" || s.getSetting("oidc_token_url") == "" || s.getSetting("oidc_userinfo_url") == "" {
			return nil, false
		}
		scope := s.getSetting("oidc_scope")
		if scope == "" {
			scope = "openid email"
		}
		return &oauthProvider{
			authURL:      auth,
			tokenURL:     s.getSetting("oidc_token_url"),
			userURL:      s.getSetting("oidc_userinfo_url"),
			scope:        scope,
			clientID:     id,
			clientSecret: s.getSetting("oidc_client_secret"),
		}, true
	}
	return nil, false
}

// getOAuthProviders：GET /api/auth/oauth —— 已配置的 OAuth 提供方（前端登录页显示按钮）。
// Telegram 走专用 widget 流：配置齐全时回 telegram_bot 供前端渲染登录组件。
func (s *Server) getOAuthProviders(c *gin.Context) {
	providers := []string{}
	for _, p := range oauthProviderNames {
		if _, ok := s.oauthProvider(p); ok {
			providers = append(providers, p)
		}
	}
	out := gin.H{"providers": providers}
	if bot := s.getSetting("telegram_bot_username"); bot != "" && s.getSetting("telegram_bot_token") != "" {
		out["telegram_bot"] = bot
	}
	c.JSON(http.StatusOK, out)
}

// oauthStart：GET /api/auth/oauth/:provider/start —— 重定向到 provider 授权页（带防 CSRF state）。
func (s *Server) oauthStart(c *gin.Context) {
	name := c.Param("provider")
	p, ok := s.oauthProvider(name)
	if !ok {
		fail(c, http.StatusBadRequest, "oauth provider not configured")
		return
	}
	redirect := s.oauthRedirectURI(c, name)
	q := url.Values{}
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", p.scope)
	q.Set("state", s.makeOAuthState(name))
	q.Set("response_type", "code") // 所有标准 OAuth2 提供方通用（github 也接受）
	c.Redirect(http.StatusFound, p.authURL+"?"+q.Encode())
}

// oauthCallback：GET /api/auth/oauth/:provider/callback?code=&state=
// 验 state → 换 access_token → 取 email → 找/建用户 → 跳回前端 /oauth?token=...
func (s *Server) oauthCallback(c *gin.Context) {
	name := c.Param("provider")
	p, ok := s.oauthProvider(name)
	if !ok {
		fail(c, http.StatusBadRequest, "oauth provider not configured")
		return
	}
	if !s.verifyOAuthState(name, c.Query("state")) {
		fail(c, http.StatusBadRequest, "invalid oauth state")
		return
	}
	code := c.Query("code")
	if code == "" {
		fail(c, http.StatusBadRequest, "missing code")
		return
	}
	ctx := c.Request.Context()
	token, err := s.oauthExchangeToken(ctx, p, code, s.oauthRedirectURI(c, name))
	if err != nil {
		s.Log().Warn("oauth token exchange failed", "provider", name, "err", err)
		fail(c, http.StatusBadGateway, "oauth exchange failed")
		return
	}
	email, err := s.oauthFetchEmail(ctx, p, name, token)
	if err != nil || email == "" {
		s.Log().Warn("oauth email fetch failed", "provider", name, "err", err)
		fail(c, http.StatusBadGateway, "could not get email from "+name)
		return
	}
	// 第三方 IdP 不得返回内部保留合成域（防冒充 Telegram 合成身份）
	if isReservedSyntheticEmail(email) {
		s.Log().Warn("oauth returned reserved synthetic email", "provider", name)
		fail(c, http.StatusBadRequest, "reserved email domain")
		return
	}
	// 邮箱准入策略（gap P2-E）：OAuth 邮箱同受域名白/黑名单约束（如限本公司域）
	if e := s.checkEmailDomainPolicy(email); e != nil {
		s.Log().Warn("oauth email rejected by domain policy", "provider", name)
		fail(c, http.StatusForbidden, e.Error())
		return
	}

	// 找/建用户（OAuth 用户用随机密码注册，之后可在设置改）
	user, err := identity.FindUserByEmail(s.db, email)
	if err != nil {
		err = s.db.Transaction(func(tx *gorm.DB) error {
			u, _, e := identity.Register(tx, identity.RegisterInput{
				Email: email, Password: randomPassword(),
			})
			user = u
			return e
		})
		if err != nil {
			fail(c, http.StatusInternalServerError, "could not create account")
			return
		}
	}
	if user.Status != identity.StatusActive {
		fail(c, http.StatusForbidden, "account disabled")
		return
	}
	// 签发 session，跳回前端 /oauth 带 token（前端立即存并清 URL）
	m := &identity.OrgMember{}
	if e := s.db.Where("user_id = ? AND role = ?", user.ID, identity.RoleOwner).Order("id").First(m).Error; e != nil {
		fail(c, http.StatusInternalServerError, "no organization")
		return
	}
	sess, claims, e := identity.IssueSessionToken(s.secret, user.ID, m.OrgID, sessionTTL, s.now())
	if e != nil {
		fail(c, http.StatusInternalServerError, "token issue failed")
		return
	}
	base := "https://" + c.Request.Host
	c.Redirect(http.StatusFound, fmt.Sprintf("%s/oauth?token=%s&exp=%d&org=%d&email=%s",
		base, url.QueryEscape(sess), claims.Exp, m.OrgID, url.QueryEscape(user.Email)))
}

func (s *Server) oauthRedirectURI(c *gin.Context, provider string) string {
	return "https://" + c.Request.Host + "/api/auth/oauth/" + provider + "/callback"
}

func (s *Server) oauthExchangeToken(ctx context.Context, p *oauthProvider, code, redirect string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("grant_type", "authorization_code")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}
	return tok.AccessToken, nil
}

func (s *Server) oauthFetchEmail(ctx context.Context, p *oauthProvider, name, token string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch name {
	case "github":
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := json.Unmarshal(body, &emails); err != nil {
			return "", err
		}
		for _, e := range emails {
			if e.Primary && e.Verified {
				return e.Email, nil
			}
		}
		for _, e := range emails {
			if e.Verified {
				return e.Email, nil
			}
		}
		return "", fmt.Errorf("no verified email")
	case "discord":
		var info struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
		}
		if err := json.Unmarshal(body, &info); err != nil {
			return "", err
		}
		if info.Email == "" {
			return "", fmt.Errorf("no email (grant the email scope)")
		}
		if !info.Verified {
			return "", fmt.Errorf("email not verified")
		}
		return info.Email, nil
	case "oidc":
		// 安全：email 必有 + email_verified 必须**显式为真**（防恶意/未验证 IdP 冒充他人 email 接管账号）。
		// 缺省/非真一律拒——OIDC 规范要求该 claim 存在；安全优先于兼容。兼容 bool 与字符串("true"/"1")。
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return "", err
		}
		email, _ := m["email"].(string)
		if email == "" {
			return "", fmt.Errorf("no email in oidc userinfo")
		}
		verified := false
		switch v := m["email_verified"].(type) {
		case bool:
			verified = v
		case string:
			verified = v == "true" || v == "1"
		case float64:
			verified = v == 1 // 部分 IdP 把 email_verified 发成 JSON 数字 1/0
		}
		if !verified {
			return "", fmt.Errorf("email not verified (email_verified must be true)")
		}
		return email, nil
	default: // google
		var info struct {
			Email         string `json:"email"`
			VerifiedEmail bool   `json:"verified_email"`
		}
		if err := json.Unmarshal(body, &info); err != nil {
			return "", err
		}
		if !info.VerifiedEmail {
			return "", fmt.Errorf("email not verified")
		}
		return info.Email, nil
	}
}

// makeOAuthState / verifyOAuthState：HMAC 签的 10 分钟短期 state（防 CSRF）。
func (s *Server) makeOAuthState(provider string) string {
	exp := s.now().Unix() + 600
	payload := fmt.Sprintf("%s.%d", provider, exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("oauth:" + payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyOAuthState(provider, state string) bool {
	parts := strings.Split(state, ".")
	if len(parts) != 3 || parts[0] != provider {
		return false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("oauth:" + payload))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[2])) {
		return false
	}
	exp, _ := strconv.ParseInt(parts[1], 10, 64)
	return s.now().Unix() <= exp
}

// randomPassword：OAuth 用户的占位密码（满足长度，用户可改）。
func randomPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
