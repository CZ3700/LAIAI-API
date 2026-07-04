package relay

import (
	"testing"
)

func cred(id, chID uint64, weight int) ChannelCredential {
	return ChannelCredential{ID: id, ChannelID: chID, CredentialEnc: "enc", Status: CredentialActive, ModeWeight: weight}
}

// 选 key 三模式 + lone key。空 active 列表不 fallback（legacy 回退由 legacyCredential 单独判）。
func TestSelectCredentialFrom(t *testing.T) {
	ch := &Channel{ID: 7, CredentialMode: CredentialModeRandom, CredentialEnc: "legacy"}

	// 空 active 列表 → 不可用（不再直接回退 credential_enc——防"全禁用"复活被禁 key）
	if _, _, ok := selectCredentialFrom(ch, nil); ok {
		t.Fatal("empty active list must not fall back here (legacyCredential decides)")
	}
	// lone key → 直返该 key（credID 非 0）
	single := []ChannelCredential{cred(11, 7, 1)}
	if credID, _, ok := selectCredentialFrom(ch, single); !ok || credID != 11 {
		t.Fatalf("lone = %d %v", credID, ok)
	}

	// random：多次都落在集合内
	multi := []ChannelCredential{cred(11, 7, 1), cred(12, 7, 1), cred(13, 7, 1)}
	seen := map[uint64]bool{}
	for i := 0; i < 60; i++ {
		credID, _, _ := selectCredentialFrom(ch, multi)
		if credID != 11 && credID != 12 && credID != 13 {
			t.Fatalf("random picked out-of-set: %d", credID)
		}
		seen[credID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("random never rotated: %v", seen)
	}
}

// polling：连续调用严格轮询覆盖全部 key（无热点、无遗漏）。
func TestPollingRotation(t *testing.T) {
	credPollCounters.Delete(uint64(9)) // 隔离计数器
	ch := &Channel{ID: 9, CredentialMode: CredentialModePolling}
	creds := []ChannelCredential{cred(21, 9, 1), cred(22, 9, 1), cred(23, 9, 1)}
	got := map[uint64]int{}
	for i := 0; i < 9; i++ {
		credID, _, _ := selectCredentialFrom(ch, creds)
		got[credID]++
	}
	// 3 key × 3 轮 = 每 key 恰好 3 次
	for _, id := range []uint64{21, 22, 23} {
		if got[id] != 3 {
			t.Fatalf("polling uneven: key %d hit %d times (want 3): %v", id, got[id], got)
		}
	}
}

// weighted：权重悬殊时高权重 key 明显占多数（统计而非精确）。
func TestWeightedBias(t *testing.T) {
	ch := &Channel{ID: 5, CredentialMode: CredentialModeWeighted}
	creds := []ChannelCredential{cred(31, 5, 1), cred(32, 5, 19)} // 1:19
	hi := 0
	for i := 0; i < 2000; i++ {
		credID, _, _ := selectCredentialFrom(ch, creds)
		if credID == 32 {
			hi++
		}
	}
	// 期望 ~95%；宽松断言 >80% 防偶发 flake
	if hi < 1600 {
		t.Fatalf("weighted bias too weak: high-weight picked %d/2000", hi)
	}
}

func TestCredentialFingerprint(t *testing.T) {
	fp := CredentialFingerprint("sk-secret-key")
	if len(fp) != 12 {
		t.Fatalf("fingerprint len = %d, want 12", len(fp))
	}
	// 稳定、不含明文
	if fp != CredentialFingerprint("sk-secret-key") {
		t.Fatal("fingerprint not stable")
	}
	if fp == CredentialFingerprint("sk-other-key") {
		t.Fatal("fingerprint collision on different keys")
	}
}
