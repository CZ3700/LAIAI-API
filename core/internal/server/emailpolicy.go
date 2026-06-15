package server

import (
	"errors"
	"strings"

	"github.com/laiai/laiai-next/core/internal/identity"
)

// 邮箱域名白名单/别名限制（gap P2-E）：运营可配的注册准入策略，作用于密码注册 / 发验证码 / OAuth。
//   - email_domain_whitelist：非空时仅允许列内域名（逗号分隔，大小写不敏感；gmail 别名/googlemail 已归一）
//   - email_domain_blocklist：列内域名直接拒（常用于挡一次性邮箱域）
//   - email_alias_block：开启则拒绝本地名带 "+别名"（在 canonical 去重之上再挡同箱多注册的薅羊毛）
// canonical 去重见 identity.CanonicalizeEmail；本策略是其之上的「准入」层，二者职责不同。

var (
	errEmailAliasBlocked     = errors.New("email aliases (+tag) are not allowed")
	errEmailDomainNotAllowed = errors.New("email domain is not in the allowed list")
	errEmailDomainBlocked    = errors.New("email domain is blocked")
)

// csvDomainSet：逗号分隔域名串 → 小写去空集合。**无有效域名时返回 nil**（表示「不限制」）。
// 关键：空串或只含分隔符/空格（如运营误填 ", ,"）都返回 nil，绝不返回非 nil 空集——
// 否则白名单分支 `wl != nil && !wl[domain]` 会对所有域名为真，把所有注册一并锁死。
func csvDomainSet(raw string) map[string]bool {
	set := map[string]bool{}
	for _, d := range strings.Split(raw, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			set[d] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// checkEmailDomainPolicy：邮箱准入校验。通过返回 nil；违规返回上面的哨兵错误（调用点转 400/403）。
// 非法邮箱本身不在此判（返回 nil），交由各调用点自身的 CanonicalizeEmail 校验拒绝。
func (s *Server) checkEmailDomainPolicy(rawEmail string) error {
	if s.getBoolSetting("email_alias_block") {
		e := strings.ToLower(strings.TrimSpace(rawEmail))
		if at := strings.LastIndex(e, "@"); at > 0 && strings.Contains(e[:at], "+") {
			return errEmailAliasBlocked
		}
	}
	canonical, err := identity.CanonicalizeEmail(rawEmail)
	if err != nil {
		return nil // 非法邮箱交由调用点的 CanonicalizeEmail 拒
	}
	domain := canonical[strings.LastIndex(canonical, "@")+1:]
	if wl := csvDomainSet(s.getSetting("email_domain_whitelist")); wl != nil && !wl[domain] {
		return errEmailDomainNotAllowed
	}
	if bl := csvDomainSet(s.getSetting("email_domain_blocklist")); bl[domain] {
		return errEmailDomainBlocked
	}
	return nil
}
