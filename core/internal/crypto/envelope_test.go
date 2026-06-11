package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
)

func b64key(seed byte) string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	kr, err := NewKeyring("k1:"+b64key(1), "k1")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := kr.Encrypt("sk-upstream-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "enc:v1:k1:") {
		t.Fatalf("ciphertext format: %q", ct)
	}
	if strings.Contains(ct, "sk-upstream") {
		t.Fatal("plaintext visible in ciphertext")
	}
	plain, err := kr.Decrypt(ct)
	if err != nil || plain != "sk-upstream-secret-key" {
		t.Fatalf("roundtrip: %q %v", plain, err)
	}
	// 同一明文两次加密密文不同（随机 nonce）
	ct2, _ := kr.Encrypt("sk-upstream-secret-key")
	if ct == ct2 {
		t.Fatal("nonce not random")
	}
}

// 核心：轮换无顺序依赖——旧密文用旧 key 解、新写入用新 key，新旧并存
func TestKeyRotationNoOrderDependency(t *testing.T) {
	old, _ := NewKeyring("k1:"+b64key(1), "k1")
	ctOld, _ := old.Encrypt("legacy-credential")

	rotated, err := NewKeyring("k1:"+b64key(1)+",k2:"+b64key(7), "k2")
	if err != nil {
		t.Fatal(err)
	}
	// 旧密文仍可解
	if plain, err := rotated.Decrypt(ctOld); err != nil || plain != "legacy-credential" {
		t.Fatalf("old ciphertext after rotation: %q %v", plain, err)
	}
	// 新写入走新 key
	ctNew, _ := rotated.Encrypt("new-credential")
	if !strings.HasPrefix(ctNew, "enc:v1:k2:") {
		t.Fatalf("new ciphertext should use k2: %q", ctNew)
	}
	// 未知 key_id 显式报错
	orphan, _ := NewKeyring("k3:"+b64key(9), "k3")
	if _, err := orphan.Decrypt(ctOld); err == nil {
		t.Fatal("unknown key_id must error")
	}
}

func TestKeyringValidation(t *testing.T) {
	if _, err := NewKeyring("", "k1"); err == nil {
		t.Fatal("empty spec accepted")
	}
	if _, err := NewKeyring("k1:"+b64key(1), "k2"); err == nil {
		t.Fatal("active not in ring accepted")
	}
	if _, err := NewKeyring("k1:short", "k1"); err == nil {
		t.Fatal("short key accepted")
	}
}
