package relay

// SSRF 防护（商用迭代指令 §3.3；设计 §4.4）：仅作用于 advanced_custom 渠道——运营配置的
// 上游可能是不可信/易配错的目标，必须防它被指向内网/云 metadata 做服务端请求伪造。
//   - 配置期（ValidateAdvancedUpstreamURL）：scheme/凭证/字面 IP 校验，admin 写入即拦。
//   - 拨号期（ssrfControlFor 经 net.Dialer.Control）：对**解析后的真实 IP**复检——域名解析
//     到内网 IP（DNS rebinding）在建连前拦截，配置期校验无法覆盖。
// metadata IP（169.254.169.254 等）恒拦，不受 allow-private 开关影响。
// 自建 provider（ollama/vllm 用专属档案、走 localhost/内网）不经此守卫，不受影响。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"
)

// cloudMetadataIPs：云厂商 metadata 端点，任何配置下恒拦（永不是合法上游）。
var cloudMetadataIPs = map[string]bool{
	"169.254.169.254": true, // AWS/GCP/Azure/OpenStack IMDS
	"100.100.100.200": true, // 阿里云 metadata
	"192.0.0.192":     true, // Oracle Cloud metadata（对抗 review）
	"fd00:ec2::254":   true, // AWS IMDSv2 IPv6
}

// isMetadataIP：是否云 metadata（恒拦）。IPv4-mapped 归一后比较。
func isMetadataIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return cloudMetadataIPs[ip.String()]
}

// nat64Prefix：NAT64 well-known 前缀 64:ff9b::/96——可内嵌任意 IPv4（含内网），恒拦（对抗 review）。
var _, nat64Prefix, _ = net.ParseCIDR("64:ff9b::/96")

// isPrivateOrLocal：是否内网/本地/保留地址（allow-private 关闭时拦）。
// 覆盖：环回、私网(RFC1918+ULA)、link-local(含 169.254 metadata 网段)、unspecified(0.0.0.0/::)、
// multicast、CGNAT(100.64/10)、IPv4-mapped IPv6 一并按其 v4 语义判定。
func isPrivateOrLocal(ip net.IP) bool {
	// NAT64（64:ff9b::/96 内嵌 IPv4，可指内网）——在 v4 归一前用原始 IPv6 形态判（对抗 review）
	if nat64Prefix != nil && nat64Prefix.Contains(ip) {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // IPv4-mapped IPv6（::ffff:127.0.0.1）归一到 v4 判定
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // CGNAT 100.64.0.0/10（RFC6598）
			return true
		case v4[0] >= 240: // 240.0.0.0/4 保留（Class E）+ 255.255.255.255 广播（对抗 review）
			return true
		}
	}
	return false
}

// blockUpstreamIP：给定拨号目标 IP + 是否放行私网，返回拦截原因（空=放行）。
func blockUpstreamIP(ip net.IP, allowPrivate bool) string {
	if ip == nil {
		return "unresolvable host"
	}
	if isMetadataIP(ip) {
		return "cloud metadata endpoint is never allowed"
	}
	if !allowPrivate && isPrivateOrLocal(ip) {
		return "private/loopback/link-local address blocked (set advanced_custom_allow_private to permit)"
	}
	return ""
}

// ValidateAdvancedUpstreamURL：advanced_custom 渠道 base_url 的配置期校验（admin 写入前）。
// 拒非 http(s)、URL 内嵌凭证、字面 IP 命中拦截集。域名不在此解析（留给拨号期复检，避免
// 配置期 DNS 与拨号期不一致的 TOCTOU 空窗）。
func ValidateAdvancedUpstreamURL(rawURL string, allowPrivate bool) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid base_url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not contain embedded credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("base_url must have a host")
	}
	if ip := net.ParseIP(host); ip != nil { // 字面 IP：配置期即拦
		if reason := blockUpstreamIP(ip, allowPrivate); reason != "" {
			return fmt.Errorf("base_url host blocked: %s", reason)
		}
	}
	return nil
}

// IsMetadataURL：base_url 的字面 host 是否云 metadata IP（恒拦，任何 provider——probe/test
// 打云 metadata 永不合法且会回显响应，是数据外泄 oracle）。域名形态返回 false（留给拨号守卫）。
func IsMetadataURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && isMetadataIP(ip)
}

// ssrfControlFor：返回一个 net.Dialer.Control，对每次实际拨号的解析后 IP 复检（DNS-rebinding-safe）。
// Go 在此 hook 时 address 已是解析出的 ip:port，返回非 nil 即阻断该次连接。
func ssrfControlFor(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("ssrf: bad dial address")
		}
		ip := net.ParseIP(host)
		if reason := blockUpstreamIP(ip, allowPrivate); reason != "" {
			return fmt.Errorf("ssrf blocked upstream dial to %s: %s", host, reason)
		}
		return nil
	}
}

// isAdvancedCustom：渠道是否走 advanced_custom（provider 或 converter 命中）——决定是否套 SSRF 守卫。
func (c *Channel) isAdvancedCustom() bool {
	return c.Provider == ProviderAdvancedCustom || c.Converter != ""
}

// ssrfDialContext：advanced_custom 渠道用的带 SSRF 复检的 DialContext（默认拨号器 + Control）。
func ssrfDialContext(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Control: ssrfControlFor(allowPrivate)}
	return d.DialContext
}

// NewGuardedClient：给 relay 包外（task 产物代理等）复用 SSRF 拨号守卫的 HTTP 客户端。
// 对**上游返回的不可信 URI**（如 veo LRO 的产物 URI，可能被恶意/被攻陷渠道指向内网/metadata）
// 发服务端请求时必须用它：① 每跳拨号复检解析后 IP（DNS-rebinding-safe，metadata 恒拦）；
// ② 跨主机重定向剥离敏感头（绝不把渠道 key 带去攻击者主机）。
// 边界（可接受）：首跳仍把渠道凭证发给 URI 直接命名的主机——不是提权，因为产生该 URI 的渠道
// 本就在每次 submit/query 持有该凭证，拿不到它本无权访问的东西；且合法 veo files 端点需带 key。
// 内网/metadata 首跳由拨号守卫拦（凭证发不出去）；跨主机重定向到公网攻击者主机由 CheckRedirect 剥头。
func NewGuardedClient(timeout time.Duration, allowPrivate bool) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{DialContext: ssrfDialContext(allowPrivate)},
		CheckRedirect: ssrfCheckRedirect,
	}
}

// ssrfCheckRedirect：跨主机重定向剥离认证头（防把渠道/BYOK 凭证送去攻击者可控的公网主机；
// 内网/metadata 首跳由拨号守卫拦）。guardedClient（advanced_custom + BYOK）与 NewGuardedClient
// （task 产物代理）共用。
func ssrfCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("ssrf: stopped after 5 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[len(via)-1].URL.Host {
		req.Header.Del("Authorization")
		req.Header.Del("x-goog-api-key")
		req.Header.Del("api-key")
		req.Header.Del("x-api-key")
	}
	return nil
}

// AllowPrivateFromDB：读 ext_settings.advanced_custom_allow_private（默认 false）——供 task 产物代理
// 决定守卫客户端是否放行私网（可信内网上游/测试 httptest 用；metadata 恒拦不受影响）。
func AllowPrivateFromDB(db *gorm.DB) bool {
	var row struct{ Value string }
	db.Table("ext_settings").Where("name = ?", "advanced_custom_allow_private").Take(&row)
	return row.Value == "true"
}
