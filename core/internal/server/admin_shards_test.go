package server

import (
	"fmt"
	"testing"
)

// 规模化分片配置端点（superadmin）：校验入参、org 存在性、落库与 upsert 改数。
func TestAdminSetOrgShards(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "shardadmin@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := uint64(me["org_id"].(float64))

	// 入参校验：shard_count 必须 1..64
	if w, _ := srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/orgs/%d/shards", orgID), map[string]any{"shard_count": 0}); w.Code != 400 {
		t.Fatalf("shard_count=0 = %d, want 400", w.Code)
	}
	if w, _ := srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/orgs/%d/shards", orgID), map[string]any{"shard_count": 100}); w.Code != 400 {
		t.Fatalf("shard_count=100 = %d, want 400", w.Code)
	}
	// 不存在的 org → 404（不给孤儿 org 写分片配置）
	if w, _ := srv.doAdmin(t, "PUT", "/api/admin/orgs/999999/shards", map[string]any{"shard_count": 4}); w.Code != 404 {
		t.Fatalf("ghost org = %d, want 404", w.Code)
	}

	// 合法设置 → 200 + 落库（type 缺省 user_credit）
	w, out := srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/orgs/%d/shards", orgID), map[string]any{"shard_count": 4})
	if w.Code != 200 || out["shard_count"].(float64) != 4 || out["type"].(string) != "user_credit" {
		t.Fatalf("set shards = %d %v", w.Code, out)
	}
	var cnt int64
	db.Table("ledger_account_shards").Where("org_id = ? AND type = ? AND shard_count = 4", orgID, "user_credit").Count(&cnt)
	if cnt != 1 {
		t.Fatalf("ledger_account_shards row = %d, want 1", cnt)
	}

	// upsert 改数 → 仍一行、count 更新为 2
	if w, _ := srv.doAdmin(t, "PUT", fmt.Sprintf("/api/admin/orgs/%d/shards", orgID), map[string]any{"shard_count": 2}); w.Code != 200 {
		t.Fatalf("upsert shards = %d", w.Code)
	}
	var rows, two int64
	db.Table("ledger_account_shards").Where("org_id = ?", orgID).Count(&rows)
	db.Table("ledger_account_shards").Where("org_id = ? AND shard_count = 2", orgID).Count(&two)
	if rows != 1 || two != 1 {
		t.Fatalf("after upsert rows=%d shard_count2=%d, want 1/1", rows, two)
	}
}
