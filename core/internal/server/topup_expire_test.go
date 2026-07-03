package server

// 充值单过期标记 + expired 入金回归（指令 P1-3；红线 3「入金接受 pending|expired 双状态」）。
// 此前 topupExpired 全库无写入点、且没有测试覆盖"对 expired 订单入金成功"分支——两者都是
// uid=967 事故语义的护栏，这里补齐。

import (
	"context"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/payment"
	"github.com/shopspring/decimal"
)

func TestTopupExpireSweepAndExpiredCredit(t *testing.T) {
	srv, db := setupRelayServer(t)
	mock := &mockPayEngine{}
	engineForTest = func(p string) payment.Engine {
		if p == "stripe" {
			return mock
		}
		return nil
	}
	defer func() { engineForTest = nil }()

	_, orgID := seedUserWithCredit(t, srv, "expire@example.com", "")
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "expire@example.com", "password": "longenough1"})
	token := login["token"].(string)

	mkOrder := func(amount string) string {
		w, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "stripe", "amount_usd": amount})
		if w.Code != 200 {
			t.Fatalf("create topup = %d %v", w.Code, out)
		}
		return out["order_no"].(string)
	}
	staleNo := mkOrder("50")   // 将被回拨到 48h 前 → 应过期
	freshNo := mkOrder("60")   // 新单 → 不动
	settledNo := mkOrder("70") // 模拟 x402 已结算待重放（trade_no 非空）→ 不标过期
	db.Exec("UPDATE topup_orders SET created_at = ? WHERE order_no IN (?, ?)",
		time.Now().Add(-48*time.Hour).Unix(), staleNo, settledNo)
	db.Exec("UPDATE topup_orders SET provider = 'x402', trade_no = '0xabc' WHERE order_no = ?", settledNo)

	n, err := srv.srv.ExpireStaleTopups(context.Background(), 24*time.Hour, 200)
	if err != nil || n != 1 {
		t.Fatalf("expire sweep = (%d, %v), want exactly 1", n, err)
	}
	var status int8
	db.Raw("SELECT status FROM topup_orders WHERE order_no = ?", staleNo).Scan(&status)
	if status != topupExpired {
		t.Fatalf("stale order status = %d, want expired(4)", status)
	}
	db.Raw("SELECT status FROM topup_orders WHERE order_no = ?", freshNo).Scan(&status)
	if status != topupPending {
		t.Fatalf("fresh order status = %d, want pending(0)", status)
	}
	db.Raw("SELECT status FROM topup_orders WHERE order_no = ?", settledNo).Scan(&status)
	if status != topupPending {
		t.Fatalf("settled x402 order status = %d, must stay pending for reconcile replay", status)
	}

	// 红线 3：expired 单收到极晚 callback 仍必须入金（绝不是终态）
	mock.orderNo, mock.tradeNo, mock.amount, mock.success = staleNo, "pi_late", decimal.RequireFromString("50"), true
	if w, _ := srv.do(t, "POST", "/api/payment/callback/stripe", "", nil); w.Code != 200 {
		t.Fatalf("late callback on expired order = %d, want 200", w.Code)
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", uint64(orgID), ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("balance after expired-order credit = %s, want 50", acc.PostedBalance)
	}
	db.Raw("SELECT status FROM topup_orders WHERE order_no = ?", staleNo).Scan(&status)
	if status != topupPaid {
		t.Fatalf("expired order after credit = %d, want paid(1)", status)
	}
}
