package identity

import "testing"

// 载荷不变量：无配置行 = 平台默认（permissive）；写入后整行覆盖回读一致。
func TestEntitlementsDefaultAndUpsert(t *testing.T) {
	db := setupDB(t)

	// 无行 → 默认：全功能开、不限量。
	got, err := LoadEntitlements(db, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FeatureImages || !got.FeatureVideo || got.MaxMembers != 0 || got.MaxAPIKeys != 0 {
		t.Fatalf("default = %+v, want all-on/unlimited", got)
	}

	// 写入收紧配置 → 回读一致。
	want := Entitlements{OrgID: 42, FeatureImages: false, FeatureVideo: true, MaxMembers: 3, MaxAPIKeys: 5}
	if err := UpsertEntitlements(db, want); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadEntitlements(db, 42)
	if got != want {
		t.Fatalf("after upsert = %+v, want %+v", got, want)
	}

	// 再次 upsert 同 org → 更新而非重复行（整行覆盖）。
	want.FeatureVideo = false
	want.MaxMembers = 0
	if err := UpsertEntitlements(db, want); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadEntitlements(db, 42)
	if got != want {
		t.Fatalf("after re-upsert = %+v, want %+v", got, want)
	}
	var n int64
	db.Model(&Entitlements{}).Where("org_id = ?", 42).Count(&n)
	if n != 1 {
		t.Fatalf("rows for org 42 = %d, want 1 (upsert must not duplicate)", n)
	}
}
