package relay

// Diagnostics admin 丰富指纹（§4.8 v1.1，批次 8）——运维检测上游渠道质量：端点真实 IP/PTR、
// 全套签名响应头、key 平台指纹、官方验真（启发式）、中转判断+手数。
//
// 红线 7：被测 key 全程只在内存用于本次探测，绝不落库/日志/回显。
//   - key 平台指纹只输出「推断平台 + 匹配到的公开前缀常量」（全平台同款、非本 key 密文），
//     绝不 slice 用户 key 的任何随机段。
//   - 捕获的是**响应头**（不含请求 Authorization）；PeerIP/PTR 与 key 无关。
// 门控：仅 admin 端点调用（可信运营者）→ 上一轮对抗 review 的"公网侦察 oracle"顾虑对本档不成立。

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

// DetailedDiagResult：admin 丰富指纹结果。内嵌基础 DiagResult。**绝不含被测 key**。
type DetailedDiagResult struct {
	DiagResult
	PeerIP             string            `json:"peer_ip"`             // 实际应答的 TCP 对端 IP
	ReverseDNS         string            `json:"reverse_dns"`         // 该 IP 的 PTR（best-effort）
	AuthField          string            `json:"auth_field"`          // 鉴权字段："Authorization: Bearer" / "x-api-key" / "?key="
	KeyPlatform        string            `json:"key_platform"`        // 由前缀推断的发行平台
	KeyPrefixPattern   string            `json:"key_prefix_pattern"`  // 匹配到的公开前缀常量（非本 key 密文）
	Headers            map[string]string `json:"headers"`             // 感兴趣的签名响应头
	Authenticity       string            `json:"authenticity"`        // official_direct/official_via_cdn/relay_suspected/unknown
	AuthenticityReason string            `json:"authenticity_reason"` //
	RelayVerdict       string            `json:"relay_verdict"`       // direct/relayed/likely_relayed/inconclusive
	RelayHops          int               `json:"relay_hops"`          // Via 能数出的手数；-1=不可知（Via 被剥）
	RelayReason        string            `json:"relay_reason"`        //
}

// interestingHeaders：值得展示的签名响应头（网关/官方签名/限额线索）。canonical 大小写。
var interestingHeaders = []string{
	"Server", "Via", "X-Cache", "Cf-Ray", "Cf-Cache-Status", "X-Amz-Cf-Id",
	"Openai-Organization", "Openai-Version", "Openai-Processing-Ms", "X-Request-Id",
	"Anthropic-Organization-Id", "Anthropic-Ratelimit-Requests-Limit", "Anthropic-Ratelimit-Requests-Remaining",
	"X-Ratelimit-Limit-Requests", "X-Ratelimit-Remaining-Requests",
	"X-Ratelimit-Limit-Tokens", "X-Ratelimit-Remaining-Tokens",
	"Content-Type", "Date",
}

// CheckKeyDetailed：admin 丰富指纹探测。allowPrivate 随运营开关（operator 合法内网上游可探）。
func (r *Relay) CheckKeyDetailed(ctx context.Context, apiKey, rawURL, provider string, allowPrivate bool) *DetailedDiagResult {
	base := r.CheckKey(ctx, apiKey, rawURL, provider) // 复用基础探测拿 ok/status/base_url/latency/models
	out := &DetailedDiagResult{DiagResult: *base, RelayHops: -1}

	// key 平台指纹 + 鉴权字段（不依赖网络，恒可给）
	out.KeyPlatform, out.KeyPrefixPattern = fingerprintKeyPlatform(apiKey)
	out.AuthField = authFieldFor(probeFamily(provider))

	if !base.OK {
		out.Authenticity, out.AuthenticityReason = "unknown", "endpoint did not return a valid response"
		out.RelayVerdict, out.RelayReason = "inconclusive", "no successful response to fingerprint"
		return out
	}

	// 命中端点 → 用带 httptrace 的一次请求抓对端 IP + 全套响应头（复用命中的 base_url）
	cl := r.guardedClient(allowPrivate)
	family := probeFamily(provider)
	peerIP, hdr := r.fetchFingerprint(ctx, cl, base.BaseURL, apiKey, family)
	out.PeerIP = peerIP
	// 红线 7 兜底：丢弃任何值里含被测 key 的响应头（恶意/echo 端点可能把请求 Authorization
	// 反射进响应头——虽只回到提交者本人，但捕获含密文的头是坏卫生，一律不收）。
	out.Headers = redactHeadersContaining(pickHeaders(hdr), apiKey)
	if peerIP != "" {
		out.ReverseDNS = reverseDNS(ctx, peerIP)
	}

	// claimed 平台：用户声称的 provider 优先，openai-compatible/空时回退前缀推断
	claimed := provider
	if claimed == "" || claimed == "openai-compatible" {
		claimed = out.KeyPlatform
	}
	// FCrDNS 正向确认（对抗 review）：PTR 记录攻击者可控，正解回指 peerIP 才可信
	fcr := forwardConfirmed(ctx, out.ReverseDNS, peerIP)
	out.Authenticity, out.AuthenticityReason = assessAuthenticity(claimed, out.Headers, out.ReverseDNS, fcr)
	out.RelayVerdict, out.RelayHops, out.RelayReason = assessRelay(claimed, out.Headers, out.ReverseDNS, fcr)
	return out
}

// fetchFingerprint：对命中端点再发一次 GET /models，用 httptrace 抓 TCP 对端 IP + 返回响应头。
// 错误只吞掉（指纹是尽力而为的增量；主判定已由 CheckKey 给出）——绝不回显 key。
func (r *Relay) fetchFingerprint(ctx context.Context, cl *http.Client, base, apiKey, family string) (peerIP string, hdr http.Header) {
	ctx, cancel := context.WithTimeout(ctx, diagPerCandidateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return "", nil
	}
	switch family {
	case "anthropic":
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	trace := &httptrace.ClientTrace{
		GotConn: func(ci httptrace.GotConnInfo) {
			if ci.Conn != nil {
				if host, _, e := net.SplitHostPort(ci.Conn.RemoteAddr().String()); e == nil {
					peerIP = host
				}
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := cl.Do(req)
	if err != nil {
		return peerIP, nil // 对端 IP 可能已在 GotConn 拿到；err 绝不回显（含带 key 的 url.Error）
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排空复用连接
	return peerIP, resp.Header
}

// reverseDNS：PTR 反查（best-effort，2s 超时）。取第一条，去尾点。
func reverseDNS(ctx context.Context, ip string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// pickHeaders：从响应头挑感兴趣的子集（present 才收）。
func pickHeaders(h http.Header) map[string]string {
	if h == nil {
		return nil
	}
	out := map[string]string{}
	for _, k := range interestingHeaders {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// redactHeadersContaining：丢弃 value 含 secret 的响应头（红线 7 兜底，防 echo 端点反射 key）。
func redactHeadersContaining(headers map[string]string, secret string) map[string]string {
	if headers == nil || secret == "" {
		return headers
	}
	for k, v := range headers {
		if strings.Contains(v, secret) {
			delete(headers, k)
		}
	}
	return headers
}

// authFieldFor：协议族 → 鉴权字段描述。
func authFieldFor(family string) string {
	switch family {
	case "anthropic":
		return "x-api-key header"
	case "gemini":
		return "?key= query param"
	default:
		return "Authorization: Bearer header"
	}
}

// fingerprintKeyPlatform：由 key 前缀推断发行平台。**只返回推断平台 + 匹配到的公开前缀常量**
// （全平台同款、非本 key 密文），绝不 slice 用户 key 的随机段（红线 7）。
func fingerprintKeyPlatform(key string) (platform, pattern string) {
	// 顺序敏感：更具体的前缀在前（sk-ant- 先于 sk-、sk-or- 先于 sk-）
	table := []struct{ prefix, platform string }{
		{"sk-ant-", "anthropic"},
		{"sk-or-v1-", "openrouter"},
		{"sk-or-", "openrouter"},
		{"sk-proj-", "openai"},
		{"AIza", "google"},
		{"gsk_", "groq"},
		{"xai-", "xai"},
		{"sk-", "openai-compatible"}, // 通用 sk-（openai 兼容系，含众多中转）
	}
	for _, e := range table {
		if strings.HasPrefix(key, e.prefix) {
			return e.platform, e.prefix
		}
	}
	return "unknown", ""
}

// hasAnyHeader：headers（canonical 键）里是否含指定任一前缀（不区分大小写）。
func hasHeaderPrefix(headers map[string]string, prefix string) bool {
	lp := strings.ToLower(prefix)
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), lp) {
			return true
		}
	}
	return false
}

// platformDomainSuffix：平台官方端点的域名后缀（PTR 验真用）。
var platformDomainSuffix = map[string][]string{
	"openai":    {"openai.com", "azure.com", "oaiusercontent.com"},
	"anthropic": {"anthropic.com"},
	"google":    {"google.com", "googleapis.com", "1e100.net", "googleusercontent.com"},
	"groq":      {"groq.com"},
	"xai":       {"x.ai"},
}

// viaOfficialTokens：官方基础设施合法使用的 Via 代理 token（出现≠第三方中转）。
// 对抗 review 修：Google Front End 恒发 "Via: 1.1 google"，此前被误判 relayed 且与 authenticity 自相矛盾。
var viaOfficialTokens = map[string]bool{"google": true, "varnish": true, "cloudfront": true}

// ptrHasDomainSuffix：带域名标签边界 + 大小写归一的后缀匹配（对抗 review 修：裸 HasSuffix
// 会把 api.myopenai.com 仿冒域判成 openai.com 官方）。ptr==suf 或以 ".suf" 结尾才算命中。
func ptrHasDomainSuffix(ptr, suf string) bool {
	ptr = strings.ToLower(strings.TrimSuffix(ptr, "."))
	suf = strings.ToLower(suf)
	return ptr == suf || strings.HasSuffix(ptr, "."+suf)
}

// ptrMatchesPlatform：PTR 是否命中该平台的官方域（带边界）。
func ptrMatchesPlatform(claimed, ptr string) bool {
	if ptr == "" {
		return false
	}
	for _, suf := range platformDomainSuffix[claimed] {
		if ptrHasDomainSuffix(ptr, suf) {
			return true
		}
	}
	return false
}

// forwardConfirmed：FCrDNS 正向确认——把 PTR 名正解回 A/AAAA，要求含 peerIP（对抗 review 修：
// PTR 记录攻击者可控，中转站即便把反解设成 api.openai.com，正解也不会回指到它的 IP）。
// 无 peerIP / 解析失败 → false（宁可降级为 inconclusive，绝不据未确认的 PTR 判官方）。
func forwardConfirmed(ctx context.Context, ptr, peerIP string) bool {
	if ptr == "" || peerIP == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, strings.TrimSuffix(ptr, "."))
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip == peerIP {
			return true
		}
	}
	return false
}

// assessAuthenticity：官方验真（启发式）。fcrConfirmed = PTR 已经 FCrDNS 正向确认（peer 身份可信）。
// 核心纪律（对抗 review）：只有 **FCrDNS 确认的 peer 身份命中官方域** 才判 official_direct；
// 转发签名头（透明反代可原样透传）只作 official_via_cdn 的弱证，绝不升级为 direct。
func assessAuthenticity(claimed string, headers map[string]string, ptr string, fcrConfirmed bool) (verdict, reason string) {
	sigHeader := hasHeaderPrefix(headers, "openai-") || hasHeaderPrefix(headers, "anthropic-") ||
		hasHeaderPrefix(headers, "x-ratelimit-")
	cdn := headers["Cf-Ray"] != "" || strings.Contains(strings.ToLower(headers["Server"]), "cloudflare") || headers["X-Amz-Cf-Id"] != ""
	ptrMatch := ptrMatchesPlatform(claimed, ptr)
	switch {
	case ptrMatch && fcrConfirmed:
		// FCrDNS 确认的 peer 身份命中官方域——唯一能下"官方直连"定论的情形
		return "official_direct", "endpoint IP forward-confirms to the platform's official domain (FCrDNS verified)"
	case ptrMatch && !fcrConfirmed:
		// PTR 命中但正解未回指——可疑（攻击者可控 PTR），不判官方
		return "unknown", "reverse-DNS claims the official domain but forward-confirmation failed — do not trust as official"
	case sigHeader && cdn:
		return "official_via_cdn", "official signature headers present behind a CDN — likely official, but a transparent relay can forward these; not peer-verified"
	case sigHeader:
		return "official_via_cdn", "response carries official signature headers, but peer identity is unverified (a relay forwards these verbatim)"
	case ptr != "" && !ptrMatch:
		return "relay_suspected", "endpoint reverse-DNS does not match the claimed platform's official domain — likely a relay/reseller"
	default:
		return "unknown", "not enough signals to confirm official origin"
	}
}

// assessRelay：中转判断 + 手数。fcrConfirmed = PTR 已 FCrDNS 正向确认。
// 核心纪律（对抗 review）：direct 只认 **FCrDNS 确认的 peer 身份**，绝不认转发签名头
// （透明反代恒透传官方头且无 Via）；Via 官方 token 不算第三方中转。
func assessRelay(claimed string, headers map[string]string, ptr string, fcrConfirmed bool) (verdict string, hops int, reason string) {
	hops = -1
	if via := strings.TrimSpace(headers["Via"]); via != "" {
		// 剔除官方基础设施的合法 Via（google/varnish/cloudfront）——不算第三方中转
		parts := strings.Split(via, ",")
		thirdParty := 0
		for _, p := range parts {
			lp := strings.ToLower(strings.TrimSpace(p))
			if lp == "" {
				continue // 空/尾逗号段不计（对抗 review LOW：防手数过计）
			}
			official := false
			for tok := range viaOfficialTokens {
				if strings.Contains(lp, tok) {
					official = true
					break
				}
			}
			if !official {
				thirdParty++
			}
		}
		if thirdParty > 0 {
			return "relayed", thirdParty, fmt.Sprintf("Via header advertises %d third-party proxy hop(s): %s", thirdParty, via)
		}
		// 仅官方 Via（如 Google Front End "1.1 google"）→ 落到下方 peer 身份判定，不误判中转
	}
	ptrMatch := ptrMatchesPlatform(claimed, ptr)
	switch {
	case ptrMatch && fcrConfirmed:
		return "direct", -1, "endpoint IP forward-confirms to the platform's official domain (direct connection, FCrDNS verified)"
	case ptr != "" && !ptrMatch:
		return "likely_relayed", -1, "endpoint reverse-DNS does not match the platform — relayed, hop count not advertised (Via stripped)"
	case hasHeaderPrefix(headers, "openai-") || hasHeaderPrefix(headers, "anthropic-"):
		// 有官方签名头但 peer 身份未确认——透明反代恒透传这些头，无法据此判直连
		return "inconclusive", -1, "official signature headers present but peer identity unverified — a transparent relay forwards these verbatim, so directness cannot be confirmed"
	default:
		return "inconclusive", -1, "no proxy headers and insufficient fingerprint to judge"
	}
}
