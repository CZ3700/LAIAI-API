package identity

import (
	"strings"
	"testing"
)

// 备用码域逻辑：生成 10 个唯一码、消费一次即焚、重用/错码/归一化、计数、重置删旧。
func TestBackupCodes(t *testing.T) {
	db := setupDB(t)

	codes, err := GenerateBackupCodes(db, 1, 1000)
	if err != nil || len(codes) != backupCodeCount {
		t.Fatalf("generate = %v codes, err %v", len(codes), err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate code %q", c)
		}
		seen[c] = true
		if len(c) != 11 || c[5] != '-' { // xxxxx-xxxxx
			t.Fatalf("code format = %q", c)
		}
	}

	// 消费 codes[0] → 成功；重用 → 失败
	if ok, _ := ConsumeBackupCode(db, 1, codes[0], 2000); !ok {
		t.Fatal("first consume should succeed")
	}
	if ok, _ := ConsumeBackupCode(db, 1, codes[0], 2001); ok {
		t.Fatal("reused code must fail")
	}
	// 错码 → 失败
	if ok, _ := ConsumeBackupCode(db, 1, "zzzzz-zzzzz", 2002); ok {
		t.Fatal("wrong code must fail")
	}
	// 空码 → 失败（不误命中）
	if ok, _ := ConsumeBackupCode(db, 1, "", 2003); ok {
		t.Fatal("empty code must fail")
	}
	// 归一化变体均应命中（用户可能任意大小写/去横杠粘贴）：
	if ok, _ := ConsumeBackupCode(db, 1, strings.ToUpper(codes[1]), 2004); !ok {
		t.Fatal("uppercase form should normalize and match")
	}
	if ok, _ := ConsumeBackupCode(db, 1, strings.ToLower(codes[2]), 2005); !ok {
		t.Fatal("lowercase form should match")
	}
	if ok, _ := ConsumeBackupCode(db, 1, strings.ReplaceAll(strings.ToLower(codes[3]), "-", ""), 2006); !ok {
		t.Fatal("no-dash lowercase form should match")
	}
	if ok, _ := ConsumeBackupCode(db, 1, "  "+codes[4]+"  ", 2007); !ok {
		t.Fatal("surrounding whitespace should be tolerated")
	}
	// 跨用户隔离：user 2 用 user 1 的码 → 失败
	if ok, _ := ConsumeBackupCode(db, 2, codes[5], 2008); ok {
		t.Fatal("other user must not consume code")
	}
	// 剩余未用 = 10 - 5 = 5
	if n, _ := CountUnusedBackupCodes(db, 1); n != 5 {
		t.Fatalf("unused = %d, want 5", n)
	}

	// 重置：删旧建新，未用数回到 10，旧码失效
	if _, err := GenerateBackupCodes(db, 1, 3000); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountUnusedBackupCodes(db, 1); n != 10 {
		t.Fatalf("after regen unused = %d, want 10", n)
	}
	if ok, _ := ConsumeBackupCode(db, 1, codes[3], 3001); ok {
		t.Fatal("old code after regen must fail")
	}

	// 关闭清空
	if err := DeleteBackupCodes(db, 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountUnusedBackupCodes(db, 1); n != 0 {
		t.Fatalf("after delete unused = %d, want 0", n)
	}
}
