package relay

import (
	"net"
	"testing"
)

// blockUpstreamIP 分类：内网/环回/link-local/metadata/unspecified/CGNAT/IPv4-mapped 全拦；公网放行。
func TestBlockUpstreamIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.5.5.5", "::1", // loopback
		"10.0.0.1", "172.16.0.1", "192.168.1.1", "fc00::1", "fd12::1", // private/ULA
		"169.254.0.1", "fe80::1", // link-local
		"169.254.169.254", "100.100.100.200", "fd00:ec2::254", // metadata（恒拦）
		"0.0.0.0", "::", // unspecified
		"100.64.0.1", "100.127.255.255", // CGNAT
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", // IPv4-mapped IPv6 绕过尝试
		"224.0.0.1",                         // multicast
		"192.0.0.192",                       // Oracle Cloud metadata（对抗 review）
		"::ffff:169.254.169.254",            // IPv4-mapped metadata
		"64:ff9b::a00:1", "64:ff9b::7f00:1", // NAT64 内嵌 10.0.0.1 / 127.0.0.1（对抗 review）
		"255.255.255.255",        // 广播
		"240.0.0.1", "250.1.2.3", // 240/4 保留（Class E）
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if reason := blockUpstreamIP(ip, false); reason == "" {
			t.Fatalf("IP %s should be blocked but was allowed", s)
		}
	}
	public := []string{"8.8.8.8", "1.1.1.1", "104.18.0.1", "2606:4700::1111"}
	for _, s := range public {
		if reason := blockUpstreamIP(net.ParseIP(s), false); reason != "" {
			t.Fatalf("public IP %s should be allowed, got: %s", s, reason)
		}
	}
}

// allow-private=true：私网放行，但 metadata 恒拦。
func TestBlockUpstreamIPAllowPrivate(t *testing.T) {
	if r := blockUpstreamIP(net.ParseIP("10.0.0.1"), true); r != "" {
		t.Fatalf("private allowed when allowPrivate=true, got: %s", r)
	}
	if r := blockUpstreamIP(net.ParseIP("127.0.0.1"), true); r != "" {
		t.Fatalf("loopback allowed when allowPrivate=true, got: %s", r)
	}
	// metadata 即便 allowPrivate 也拦
	if blockUpstreamIP(net.ParseIP("169.254.169.254"), true) == "" {
		t.Fatal("metadata IP must be blocked even with allowPrivate")
	}
}

// ValidateAdvancedUpstreamURL：scheme/凭证/字面 IP 校验。
func TestValidateAdvancedUpstreamURL(t *testing.T) {
	ok := []string{"https://api.example.com/v1", "http://upstream.example:8080/x"}
	for _, u := range ok {
		if err := ValidateAdvancedUpstreamURL(u, false); err != nil {
			t.Fatalf("URL %q should pass: %v", u, err)
		}
	}
	bad := map[string]string{
		"ftp://x/y":                         "scheme",
		"https://user:pass@api.example.com": "credentials",
		"https://127.0.0.1/v1":              "loopback literal",
		"https://169.254.169.254/latest":    "metadata literal",
		"https://10.1.2.3:9000/v1":          "private literal",
		"not a url ::::":                    "malformed",
		"https://":                          "no host",
	}
	for u, why := range bad {
		if err := ValidateAdvancedUpstreamURL(u, false); err == nil {
			t.Fatalf("URL %q should fail (%s) but passed", u, why)
		}
	}
	// allow-private：私网字面 IP 放行，metadata 仍拦
	if err := ValidateAdvancedUpstreamURL("https://10.1.2.3/v1", true); err != nil {
		t.Fatalf("private literal should pass with allowPrivate: %v", err)
	}
	if err := ValidateAdvancedUpstreamURL("https://169.254.169.254/x", true); err == nil {
		t.Fatal("metadata literal must fail even with allowPrivate")
	}
}

// ssrfControl：拨号期对解析后 IP 复检（DNS-rebinding 语义）。
func TestSSRFControl(t *testing.T) {
	ctrl := ssrfControlFor(false)
	if err := ctrl("tcp", "10.0.0.5:443", nil); err == nil {
		t.Fatal("dial to private IP must be blocked")
	}
	if err := ctrl("tcp", "169.254.169.254:80", nil); err == nil {
		t.Fatal("dial to metadata must be blocked")
	}
	if err := ctrl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("dial to public IP must pass: %v", err)
	}
}

// upstreamURL / adapterForChannel / isAdvancedCustom 组合。
func TestChannelAdvancedRouting(t *testing.T) {
	// upstream_path 覆盖默认
	ch := &Channel{BaseURL: "https://x.example/api", UpstreamPath: "/custom/chat"}
	if got := ch.upstreamURL("/chat/completions"); got != "https://x.example/api/custom/chat" {
		t.Fatalf("custom path url = %s", got)
	}
	if got := (&Channel{BaseURL: "https://x/v1"}).upstreamURL("/chat/completions"); got != "https://x/v1/chat/completions" {
		t.Fatalf("default path url = %s", got)
	}
	// converter 分发
	if _, ok := adapterForChannel(&Channel{Converter: ConverterToGemini}).(geminiAdapter); !ok {
		t.Fatal("converter gemini did not dispatch to geminiAdapter")
	}
	if _, ok := adapterForChannel(&Channel{Converter: ConverterNone}).(openaiAdapter); !ok {
		t.Fatal("converter none did not dispatch to openaiAdapter")
	}
	// isAdvancedCustom
	if !(&Channel{Provider: ProviderAdvancedCustom}).isAdvancedCustom() {
		t.Fatal("advanced_custom provider not detected")
	}
	if !(&Channel{Converter: ConverterToAnthropic}).isAdvancedCustom() {
		t.Fatal("converter-set channel not detected as advanced")
	}
	if (&Channel{Provider: "openai"}).isAdvancedCustom() {
		t.Fatal("plain openai channel wrongly flagged advanced")
	}
}
