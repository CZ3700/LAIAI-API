package relay

// SigV4 签名正确性——AWS 官方测试套件 aws-sig-v4-test-suite 的 get-vanilla 标准向量。
// 无真机也能确保签名算法正确（对抗 Bedrock 鉴权失败的最强前置保证）。

import (
	"net/http"
	"testing"
	"time"
)

func TestSigV4GetVanilla(t *testing.T) {
	// AWS 官方向量：GET / ，空 payload，signed headers = host;x-amz-date
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	sigV4Sign(req, "service", "us-east-1",
		"AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "", nil, when)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization mismatch:\n got=%s\nwant=%s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// 带 content-type + session token 的签名（Bedrock POST 路径形态）：验证多头排序 + 安全令牌头。
func TestSigV4PostWithContentTypeAndToken(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/converse",
		nil)
	req.Header.Set("Content-Type", "application/json")
	when := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	sigV4Sign(req, "bedrock", "us-east-1", "AK", "SK", "session-tok", []byte(`{"a":1}`), when)

	auth := req.Header.Get("Authorization")
	// content-type/host/x-amz-date/x-amz-security-token 按字典序进 SignedHeaders
	if !contains(auth, "SignedHeaders=content-type;host;x-amz-date;x-amz-security-token") {
		t.Fatalf("signed headers order wrong: %s", auth)
	}
	if req.Header.Get("X-Amz-Security-Token") != "session-tok" {
		t.Fatal("security token header not set")
	}
	if !contains(auth, "Credential=AK/20230101/us-east-1/bedrock/aws4_request") {
		t.Fatalf("credential scope wrong: %s", auth)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
