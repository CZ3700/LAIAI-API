package identity

import (
	"errors"
	"testing"
)

// dummyHash 必须是合法的 argon2id PHC 串：VerifyPassword 对它应跑到 argon2 比较步骤并返回
// ErrPasswordMismatch（而非任何 "malformed" 解析错误）。否则 VerifyDummyPassword 会在解析处提前
// 返回、不跑 argon2，重开 login 的时延型用户枚举缺口（防回归）。
func TestDummyHashRunsArgon2(t *testing.T) {
	if err := VerifyPassword("any-wrong-password", dummyHash); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("dummyHash must be a valid PHC that reaches argon2 (want ErrPasswordMismatch), got: %v", err)
	}
	VerifyDummyPassword("any-wrong-password") // 不应 panic
}

// HashPassword/VerifyPassword 往返：正确密码通过、错误密码 mismatch、过短密码报错。
func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("longenough1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword("longenough1", h); err != nil {
		t.Fatalf("correct password should verify: %v", err)
	}
	if err := VerifyPassword("wrongpassword", h); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("wrong password should mismatch, got %v", err)
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("password < 8 chars must be rejected")
	}
}
