package totp

import (
	"strings"
	"testing"
)

// 修复回归（用户报 2FA QR 错）：otpauth URI 必须符合 Google Authenticator Key URI Format——
// 多词品牌名空格统一 %20（label 与 issuer query 一致）、含冒号品牌名内部冒号 %3A 不与分隔冒号混淆、
// account 的 @ → %40、绝不出现 '+'。
func TestProvisioningURIKeyURIFormat(t *testing.T) {
	uri := ProvisioningURI("ABC23456ABC23456", "user@example.com", "LAIAI Next")
	if !strings.HasPrefix(uri, "otpauth://totp/LAIAI%20Next:user%40example.com?") {
		t.Fatalf("label wrong (space must be %%20, @ must be %%40): %s", uri)
	}
	if !strings.Contains(uri, "issuer=LAIAI%20Next") {
		t.Fatalf("issuer query must equal label issuer prefix (both %%20): %s", uri)
	}
	if strings.Contains(uri, "+") {
		t.Fatalf("uri must not contain '+' (space encodes as %%20, not +): %s", uri)
	}
	if !strings.Contains(uri, "algorithm=SHA1") || !strings.Contains(uri, "digits=6") || !strings.Contains(uri, "period=30") {
		t.Fatalf("missing standard params: %s", uri)
	}

	// 品牌名含冒号：内部冒号必须 %3A（不与 label 分隔冒号混淆，否则验证器切错 issuer/account）
	uri2 := ProvisioningURI("ABC23456ABC23456", "a@b.com", "ACME: AI")
	if !strings.HasPrefix(uri2, "otpauth://totp/ACME%3A%20AI:a%40b.com?") {
		t.Fatalf("issuer-with-colon label wrong: %s", uri2)
	}
}
