package relay

// admin 丰富指纹纯函数单测（§4.8 v1.1）：key 平台指纹、官方验真、中转判断——含红线 7（指纹绝不含 key 密文）。

import (
	"strings"
	"testing"
)

func TestFingerprintKeyPlatform(t *testing.T) {
	cases := []struct{ key, platform, pattern string }{
		{"sk-ant-api03-abcdefg", "anthropic", "sk-ant-"},
		{"sk-or-v1-xxxx", "openrouter", "sk-or-v1-"},
		{"sk-or-xxxx", "openrouter", "sk-or-"},
		{"sk-proj-abc123", "openai", "sk-proj-"},
		{"AIzaSyABCDEF", "google", "AIza"},
		{"gsk_abcdef", "groq", "gsk_"},
		{"xai-abcdef", "xai", "xai-"},
		{"sk-abcdef123", "openai-compatible", "sk-"},
		{"weird-token", "unknown", ""},
	}
	for _, c := range cases {
		p, pat := fingerprintKeyPlatform(c.key)
		if p != c.platform || pat != c.pattern {
			t.Errorf("fingerprintKeyPlatform(%q) = (%q,%q), want (%q,%q)", c.key, p, pat, c.platform, c.pattern)
		}
		// 红线 7：pattern 只能是公开前缀常量，绝不含 key 的随机段
		if pat != "" && !strings.HasPrefix(c.key, pat) {
			t.Errorf("pattern %q not a prefix constant of key", pat)
		}
		if pat != "" && len(pat) < len(c.key) {
			// pattern 必须是我表里的固定常量，不能是从 key 切出的更长串
			body := strings.TrimPrefix(c.key, pat)
			if strings.Contains(pat, body) && body != "" {
				t.Errorf("RED LINE 7: pattern leaked key body")
			}
		}
	}
}

// ptrHasDomainSuffix 标签边界（对抗 review 修：仿冒域不得命中官方）。
func TestPtrHasDomainSuffix(t *testing.T) {
	yes := [][2]string{{"api.openai.com", "openai.com"}, {"openai.com", "openai.com"}, {"API.OpenAI.Com.", "openai.com"}}
	for _, c := range yes {
		if !ptrHasDomainSuffix(c[0], c[1]) {
			t.Errorf("want match: %v", c)
		}
	}
	no := [][2]string{{"api.myopenai.com", "openai.com"}, {"netflix.ai", "x.ai"}, {"x1e100.net", "1e100.net"}, {"openai.com.evil.io", "openai.com"}}
	for _, c := range no {
		if ptrHasDomainSuffix(c[0], c[1]) {
			t.Errorf("RED: lookalike matched: %v", c)
		}
	}
}

func TestAssessAuthenticity(t *testing.T) {
	// 官方直连：PTR 命中官方域 + FCrDNS 确认——唯一能下 official_direct 定论的情形
	v, _ := assessAuthenticity("openai", map[string]string{"Openai-Version": "x"}, "api.openai.com", true)
	if v != "official_direct" {
		t.Errorf("ptr+fcr = %q, want official_direct", v)
	}
	// PTR 命中但 FCrDNS 未确认（攻击者伪造 PTR）→ 绝不判官方
	v, _ = assessAuthenticity("openai", map[string]string{}, "api.openai.com", false)
	if v != "unknown" {
		t.Errorf("ptr-no-fcr = %q, want unknown (伪造 PTR 不可信)", v)
	}
	// 官方经 CDN：签名头 + cf-ray（弱证，非 peer 确认）→ official_via_cdn
	v, _ = assessAuthenticity("openai", map[string]string{"Openai-Organization": "org-x", "Cf-Ray": "abc"}, "", false)
	if v != "official_via_cdn" {
		t.Errorf("sig+cdn = %q, want official_via_cdn", v)
	}
	// 疑似中转：PTR 不匹配官方域 + 无签名头
	v, _ = assessAuthenticity("openai", map[string]string{"Server": "nginx"}, "some-vps.reseller.io", false)
	if v != "relay_suspected" {
		t.Errorf("ptr-mismatch = %q, want relay_suspected", v)
	}
	v, _ = assessAuthenticity("openai", map[string]string{}, "", false)
	if v != "unknown" {
		t.Errorf("no-signal = %q, want unknown", v)
	}
}

func TestAssessRelay(t *testing.T) {
	// 第三方 Via → relayed + 手数
	v, hops, _ := assessRelay("openai", map[string]string{"Via": "1.1 vegur, 1.1 proxy2"}, "", false)
	if v != "relayed" || hops != 2 {
		t.Errorf("via = (%q,%d), want (relayed,2)", v, hops)
	}
	// 空段/尾逗号不过计（对抗 review LOW）：仍是 1 手
	v, hops, _ = assessRelay("openai", map[string]string{"Via": "1.1 proxy,"}, "", false)
	if v != "relayed" || hops != 1 {
		t.Errorf("malformed via = (%q,%d), want (relayed,1)", v, hops)
	}
	// 官方 Via token（Google Front End）不算第三方中转——落 peer 身份判定（对抗 review 修）
	v, _, _ = assessRelay("google", map[string]string{"Via": "1.1 google"}, "x.1e100.net", true)
	if v != "direct" {
		t.Errorf("official-via = %q, want direct (google Via 非中转)", v)
	}
	// FCrDNS 确认命中官方 → direct
	v, hops, _ = assessRelay("openai", map[string]string{"Openai-Version": "x"}, "api.openai.com", true)
	if v != "direct" || hops != -1 {
		t.Errorf("fcr-match = (%q,%d), want (direct,-1)", v, hops)
	}
	// 透明反代：透传官方签名头 + 无可用 PTR + peer 身份未确认 → inconclusive
	// （绝不据透传头误判 direct，对抗 review 核心修；有非匹配 PTR 时则更准地判 likely_relayed，见下）
	v, _, _ = assessRelay("openai", map[string]string{"Openai-Version": "x", "Server": "nginx"}, "", false)
	if v != "inconclusive" {
		t.Errorf("transparent-relay(no-ptr) = %q, want inconclusive (透传头不证直连)", v)
	}
	// 透明反代但 PTR 暴露是中转站域 → likely_relayed（比 inconclusive 更准）
	v, _, _ = assessRelay("openai", map[string]string{"Openai-Version": "x"}, "edge.reseller.io", false)
	if v != "likely_relayed" {
		t.Errorf("transparent-relay(reseller-ptr) = %q, want likely_relayed", v)
	}
	// PTR 不匹配 + 无 Via → 疑似中转
	v, hops, _ = assessRelay("openai", map[string]string{"Server": "openresty"}, "vps.reseller.io", false)
	if v != "likely_relayed" || hops != -1 {
		t.Errorf("mismatch = (%q,%d), want (likely_relayed,-1)", v, hops)
	}
	v, _, _ = assessRelay("openai", map[string]string{}, "", false)
	if v != "inconclusive" {
		t.Errorf("nosignal = %q, want inconclusive", v)
	}
}

func TestAuthFieldFor(t *testing.T) {
	if authFieldFor("anthropic") != "x-api-key header" || authFieldFor("gemini") != "?key= query param" || authFieldFor("openai") != "Authorization: Bearer header" {
		t.Error("authFieldFor mismatch")
	}
}
