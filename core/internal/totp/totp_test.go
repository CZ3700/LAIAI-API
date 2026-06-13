package totp

import (
	"testing"
	"time"
)

func TestTOTPRoundtrip(t *testing.T) {
	secret := GenerateSecret()
	now := time.Unix(1700000000, 0)
	code := generate(secret, now.Unix()/stepSeconds)
	if !Validate(secret, code, now) {
		t.Fatal("fresh code should validate")
	}
	// ±1 窗口容差
	if !Validate(secret, generate(secret, now.Unix()/stepSeconds-1), now) {
		t.Fatal("prev-window code should validate (drift tolerance)")
	}
	// 错码拒
	if Validate(secret, "000000", now) && generate(secret, now.Unix()/stepSeconds) != "000000" {
		t.Fatal("wrong code should not validate")
	}
	// 过期窗口（2 步外）拒
	if Validate(secret, generate(secret, now.Unix()/stepSeconds-3), now) {
		t.Fatal("far-past code should not validate")
	}
	// 已知向量：RFC 6238 secret "12345678901234567890" base32 = GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ at T=59 → 287082
	if generate("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 59/30) != "287082" {
		t.Fatalf("RFC 6238 test vector mismatch: %s", generate("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 59/30))
	}
}
