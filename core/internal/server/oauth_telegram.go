package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

// Telegram 登录（Login Widget，gap P1-C）。**非 OAuth2 跳转流**：前端嵌 Telegram 组件，
// 用户在 Telegram 内授权后，组件把签名数据回传，后端用 bot_token 验 HMAC。
// **Telegram 不提供 email**——用合成身份 tg<id>@telegram.local 唯一标识（运营须知该用户无真实邮箱）。
// 限流复用 authRateLimit（防伪造数据暴力）。

const telegramAuthMaxAge = 300 // 授权数据有效期（秒）：用户刚授权，5 分钟足够并收窄重放窗口

// telegramSyntheticDomain：Telegram 登录给无邮箱用户分配的合成邮箱域，**内部保留**。
const telegramSyntheticDomain = "telegram.local"

// isReservedSyntheticEmail：判断邮箱是否落在内部保留的合成域。
// 公开注册 / 第三方 OAuth 一律禁止占用——否则攻击者可预占 tg<id>@telegram.local，
// 待真实 Telegram 用户登录时被导入攻击者预建的账号（账号预劫持）。telegramLogin 自身
// 直连 identity.Register 铸号，不经此守卫，故是该域唯一合法来源。
func isReservedSyntheticEmail(email string) bool {
	canonical, err := identity.CanonicalizeEmail(email)
	if err != nil {
		return false // 非法邮箱交由各自校验逻辑拒绝
	}
	at := strings.LastIndex(canonical, "@")
	return at >= 0 && canonical[at+1:] == telegramSyntheticDomain
}

// tgFieldString：把 widget 字段值还原成 Telegram 签名时的字符串形态。
// 整数(id/auth_date 及未来任何整型新字段)按整数格式化，避免 JSON float64 的科学计数法。
func tgFieldString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

// verifyTelegramAuth：按 Telegram 规范校验 widget 回传数据的 HMAC。
//   secret = SHA256(bot_token)；data_check_string = **收到的所有字段(除 hash)** 按 key 排序的
//   "k=v" 以 \n 连接；期望 hash = hex(HMAC_SHA256(data_check_string, secret))。
//   用全量字段（而非硬编码子集）才符合规范——Telegram 未来新增字段不会令验签失败。
func verifyTelegramAuth(data map[string]any, botToken string) bool {
	hashVal, _ := data["hash"].(string)
	if hashVal == "" {
		return false
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tgFieldString(data[k]))
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(hashVal))
}

// telegramLogin：POST /api/auth/telegram —— 验 widget 数据 → 找/建合成身份用户 → 发 session（JSON）。
func (s *Server) telegramLogin(c *gin.Context) {
	botToken := s.getSetting("telegram_bot_token")
	if botToken == "" {
		fail(c, http.StatusBadRequest, "telegram login not configured")
		return
	}
	var data map[string]any
	if err := c.ShouldBindJSON(&data); err != nil || data == nil {
		fail(c, http.StatusBadRequest, "invalid telegram payload")
		return
	}
	if !verifyTelegramAuth(data, botToken) {
		fail(c, http.StatusUnauthorized, "telegram signature verification failed")
		return
	}
	idF, _ := data["id"].(float64)
	tgID := int64(idF)
	if tgID == 0 {
		fail(c, http.StatusBadRequest, "invalid telegram id")
		return
	}
	authDateF, _ := data["auth_date"].(float64)
	// 防重放：授权数据不得过旧
	if s.now().Unix()-int64(authDateF) > telegramAuthMaxAge {
		fail(c, http.StatusUnauthorized, "telegram authorization expired, please retry")
		return
	}

	email := fmt.Sprintf("tg%d@telegram.local", tgID) // 合成身份（Telegram 无 email）
	// 邮箱准入策略（gap P2-E）：配了域名白名单时，合成域 telegram.local 不在白名单内 → 拒绝
	// Telegram 登录。即「设了白名单=锁定可注册域名」时，Telegram 这条无邮箱旁路也被一并关上。
	if e := s.checkEmailDomainPolicy(email); e != nil {
		fail(c, http.StatusForbidden, e.Error())
		return
	}
	user, err := identity.FindUserByEmail(s.db, email)
	if err != nil {
		username, _ := data["username"].(string)
		name := username
		if name == "" {
			first, _ := data["first_name"].(string)
			last, _ := data["last_name"].(string)
			name = strings.TrimSpace(first + " " + last)
		}
		err = s.db.Transaction(func(tx *gorm.DB) error {
			u, _, e := identity.Register(tx, identity.RegisterInput{
				Email: email, Password: randomPassword(), DisplayName: name,
			})
			user = u
			return e
		})
		if err != nil {
			// 并发首登竞态：另一个请求已抢先建号 → 复用即可，不报错
			if u, e := identity.FindUserByEmail(s.db, email); e == nil {
				user = u
			} else {
				fail(c, http.StatusInternalServerError, "could not create account")
				return
			}
		}
	}
	if user.Status != identity.StatusActive {
		fail(c, http.StatusForbidden, "account disabled")
		return
	}
	s.issueSessionFor(c, user)
}
