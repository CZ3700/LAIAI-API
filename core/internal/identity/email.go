package identity

import (
	"fmt"
	"strings"
)

// CanonicalizeEmail 把邮箱折叠为注册去重用的 canonical 形态（旧平台薅羊毛事故 v15.2 的结构性消解：
// canonical 形态在 users.email_canonical 上有 UNIQUE 约束，去重由 schema 层兜底）。
//
// 规则：
//   - 全小写 + 去首尾空白
//   - 所有域名：local part 去 "+alias" 后缀（aliasing 是各家通用的注册薅羊毛入口）
//   - gmail.com / googlemail.com：额外折叠 local part 中的 "."（gmail 忽略点号），
//     且 googlemail.com 归一为 gmail.com
//
// 注意：canonical 形态只用于查重，不用于发信——发信永远用用户原始 email。
func CanonicalizeEmail(email string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return "", fmt.Errorf("identity: invalid email %q", email)
	}
	local, domain := e[:at], e[at+1:]
	if strings.ContainsAny(domain, " @") || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("identity: invalid email domain %q", domain)
	}
	if i := strings.Index(local, "+"); i >= 0 {
		local = local[:i]
	}
	if domain == "googlemail.com" {
		domain = "gmail.com"
	}
	if domain == "gmail.com" {
		local = strings.ReplaceAll(local, ".", "")
	}
	if local == "" {
		return "", fmt.Errorf("identity: empty local part after canonicalization of %q", email)
	}
	return local + "@" + domain, nil
}
