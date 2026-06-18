package server

import (
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"
)

func insertTopup(t *testing.T, db *gorm.DB, orgID uint64, orderNo string, amountUSD float64, status int8, createdAt int64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO topup_orders
		(org_id, order_no, provider, amount_usd, pay_amount, pay_currency, status, trade_no, created_at, paid_at)
		VALUES (?, ?, 'stripe', ?, ?, 'USD', ?, '', ?, ?)`,
		orgID, orderNo, amountUSD, amountUSD, status, createdAt, createdAt).Error; err != nil {
		t.Fatalf("insert topup: %v", err)
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return -1
}

// 仪表盘本月/近7日充值聚合：服务端求和（避免分页明细本地漏算），近7日与 sumDays(-6,7) 同窗，org 隔离。
func TestRechargeStats(t *testing.T) {
	srv, db := setupRelayServer(t)
	tok := registerAndLogin(t, srv, "rs@example.com")
	_, me := srv.do(t, "GET", "/api/me", tok, nil)
	orgID := uint64(me["org_id"].(float64))
	now := time.Now().UTC()
	insertTopup(t, db, orgID, "o1", 30, 1, now.Unix())                    // 今天 已到账 → 本月 + 近7日
	insertTopup(t, db, orgID, "o2", 100, 1, now.AddDate(0, 0, -40).Unix()) // 40 天前 已到账 → 本月外 + 近7日外
	insertTopup(t, db, orgID, "o3", 50, 0, now.Unix())                     // 今天 pending → 不计

	_, out := srv.do(t, "GET", "/api/payment/recharge-stats", tok, nil)
	if f := toFloat(out["month_usd"]); f != 30 {
		t.Fatalf("month_usd = %v, want 30", out["month_usd"])
	}
	if f := toFloat(out["last7_usd"]); f != 30 {
		t.Fatalf("last7_usd = %v, want 30", out["last7_usd"])
	}

	// org 隔离（红线 5）
	tok2 := registerAndLogin(t, srv, "rs2@example.com")
	_, out2 := srv.do(t, "GET", "/api/payment/recharge-stats", tok2, nil)
	if f := toFloat(out2["month_usd"]); f != 0 {
		t.Fatalf("org isolation breach: month_usd = %v, want 0", out2["month_usd"])
	}
}
