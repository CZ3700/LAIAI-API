package server

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/shopspring/decimal"
)

// P0-3：并发预留绝不超 quota_limit。50 个并发 reserve $1 打 quota=$10 的 key，恰好 10 个成功，
// used_amount 落在 10（条件 UPDATE 原子性；单连接 :memory: 串行化已能验证逻辑，MySQL 行锁同理）。
func TestAPIKeyQuotaConcurrentReserve(t *testing.T) {
	_, db := setupRelayServer(t)
	db.Exec(`INSERT INTO api_keys (id,org_id,name,prefix,key_hash,status,quota_limit,used_amount)
		VALUES (9001,1,'k','p','hash-9001',1,'10','0')`)

	const n = 50
	var wg sync.WaitGroup
	var ok int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if identity.ReserveKeyQuota(db, 9001, decimal.RequireFromString("1")) {
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()

	if ok != 10 {
		t.Fatalf("concurrent reserves succeeded = %d, want exactly 10 (quota $10 / $1 each)", ok)
	}
	var used string
	db.Raw(`SELECT used_amount FROM api_keys WHERE id = 9001`).Scan(&used)
	if decimal.RequireFromString(used).Cmp(decimal.RequireFromString("10")) != 0 {
		t.Fatalf("used_amount = %s, want 10 (never exceeds quota)", used)
	}
	// 无限额度（quota_limit=0）的 key：reserve 恒成功
	db.Exec(`INSERT INTO api_keys (id,org_id,name,prefix,key_hash,status,quota_limit,used_amount)
		VALUES (9002,1,'k2','p','hash-9002',1,'0','0')`)
	if !identity.ReserveKeyQuota(db, 9002, decimal.RequireFromString("9999")) {
		t.Fatal("unlimited-quota key reserve should always succeed")
	}
}

// P0-3：reserve→实际对账——一次成功调用后 used_amount 落到真实 actual（既不双计也不残留 hold）。
func TestQuotaUsedReconcilesToActual(t *testing.T) {
	srv, db := setupRelayServer(t)
	up := mockUpstreamJSON(t, 1000, 500) // 真实 usage：actual cost = 1000/1M*2 + 500/1M*6 = 0.005
	defer up.Close()
	setupChannelAndPrice(t, srv, up.URL)
	apiKey, orgID := seedUserWithCredit(t, srv, "qr@gmail.com", "10")

	// 传大 max_completion_tokens，使 hold（预留）远大于 actual，凸显「对账回落到 actual」
	w, out := srv.do(t, "POST", "/v1/chat/completions", apiKey, map[string]any{
		"model": "test-model", "max_completion_tokens": 50000,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if w.Code != 200 {
		t.Fatalf("relay = %d %v", w.Code, out)
	}
	var used string
	db.Raw(`SELECT used_amount FROM api_keys WHERE org_id = ?`, uint64(orgID)).Scan(&used)
	// 按生产列精度 DECIMAL(20,8) 比较（SQLite NUMERIC 存 float，分位对账会带极小浮点噪声；MySQL 精确）
	if decimal.RequireFromString(used).Round(8).Cmp(decimal.RequireFromString("0.005")) != 0 {
		t.Fatalf("key used_amount = %s, want 0.005 @8dp (reconciled to actual, hold released)", used)
	}
}
