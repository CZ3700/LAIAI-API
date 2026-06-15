package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// P0-4：未对真实 facilitator 校验合约前，x402 默认禁用——即便三项配置齐全，
// 缺显式总开关 x402_enabled=true 时 engineFor 返回 nil，建单直接 400，绝不进真金路径。
func TestX402DisabledWithoutEnableFlag(t *testing.T) {
	srv, db := setupRelayServer(t)
	// 三项配置齐全，但**不**置 x402_enabled
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('x402_facilitator_url','http://fac.invalid'),('x402_pay_to','0xMerchant'),('x402_asset','0xUSDC')`)

	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "x402off@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "x402off@gmail.com", "password": "longenough1"})
	token := login["token"].(string)

	if w, _ := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "x402", "amount_usd": "25"}); w.Code != 400 {
		t.Fatalf("x402 create while disabled = %d, want 400 (engine nil)", w.Code)
	}
	// 翻开总开关后即可建单
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('x402_enabled','true')`)
	if w, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "x402", "amount_usd": "25"}); w.Code != 200 || out["requirements"] == nil {
		t.Fatalf("x402 create after enable = %d %v, want 200 + requirements", w.Code, out)
	}
}

// P0-4：trade_no(=txHash) 是链上已结算的持久化证据。重复 settle 同一订单时，
// 已有证据 → 跳过 facilitator（链上不可逆，重发会双花），仅幂等重放入账。
// 断言 facilitator /settle 恰好被调用一次，余额只入一次。
func TestX402ReSettleSkipsFacilitatorNoDoubleSpend(t *testing.T) {
	srv, db := setupRelayServer(t)
	var settleCalls int64
	fac := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/verify":
			_, _ = w.Write([]byte(`{"isValid":true}`))
		case "/settle":
			atomic.AddInt64(&settleCalls, 1)
			_, _ = w.Write([]byte(`{"success":true,"transaction":"0xONCHAINTX"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer fac.Close()
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('x402_enabled','true'),('x402_facilitator_url',?),('x402_pay_to','0xMerchant'),('x402_asset','0xUSDC')`, fac.URL)

	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "x402re@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "x402re@gmail.com", "password": "longenough1"})
	token := login["token"].(string)
	orgID := uint64(login["org_id"].(float64))

	_, out := srv.do(t, "POST", "/api/payment/create", token, map[string]any{"provider": "x402", "amount_usd": "25"})
	orderNo := out["order_no"].(string)

	xpay := base64.StdEncoding.EncodeToString([]byte(`{"x402Version":1,"scheme":"exact","payload":{}}`))
	settle := func() int {
		req := httptest.NewRequest("POST", "/api/payment/x402/settle/"+orderNo, nil)
		req.Header.Set("X-PAYMENT", xpay)
		rec := httptest.NewRecorder()
		srv.engine.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := settle(); c != 200 {
		t.Fatalf("first settle = %d, want 200", c)
	}
	if c := settle(); c != 200 { // 第二次：trade_no 已存，应跳过 facilitator
		t.Fatalf("re-settle = %d, want 200", c)
	}
	if n := atomic.LoadInt64(&settleCalls); n != 1 {
		t.Fatalf("facilitator /settle called %d times, want exactly 1 (no on-chain double-spend on retry)", n)
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", orgID, ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("25")) {
		t.Fatalf("balance = %s, want 25 (credited once)", acc.PostedBalance)
	}
	// 证据已落库
	var tradeNo string
	db.Raw(`SELECT trade_no FROM topup_orders WHERE order_no = ?`, orderNo).Scan(&tradeNo)
	if tradeNo != "0xONCHAINTX" {
		t.Fatalf("trade_no = %q, want 0xONCHAINTX (proof persisted)", tradeNo)
	}
}

// P0-4：settle 成功但本地入账当时失败（订单残留 pending 且已带 trade_no 证据）→
// ReconcileTopups 据 trade_no 重放统一入账自动补回，无需客户端回访；二次对账幂等不翻倍。
// 同时：未结算（trade_no 空）的 pending x402 单不被误入账。
func TestX402ReconcileRepairsSettledUncredited(t *testing.T) {
	srv, db := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "x402rec@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "x402rec@gmail.com", "password": "longenough1"})
	orgID := uint64(login["org_id"].(float64))
	past := srv.srv.now().Add(-time.Hour).Unix()

	// 已结算未入账（带 txHash 证据，仍 pending）
	settled := TopupOrder{
		OrderNo: "X402REC1", OrgID: orgID, Provider: "x402",
		AmountUSD: decimal.RequireFromString("10"), PayAmount: decimal.RequireFromString("10"),
		PayCurrency: "USDC", Status: topupPending, TradeNo: "0xSETTLEDTX", CreatedAt: past,
	}
	if e := db.Create(&settled).Error; e != nil {
		t.Fatalf("create settled order: %v", e)
	}
	// 未结算（无证据）的 pending 单：对账不得入账
	unsettled := TopupOrder{
		OrderNo: "X402REC2", OrgID: orgID, Provider: "x402",
		AmountUSD: decimal.RequireFromString("99"), PayAmount: decimal.RequireFromString("99"),
		PayCurrency: "USDC", Status: topupPending, TradeNo: "", CreatedAt: past,
	}
	db.Create(&unsettled)

	checked, credited, err := srv.srv.ReconcileTopups(context.Background(), time.Minute, 100)
	if err != nil {
		t.Fatalf("reconcile err: %v", err)
	}
	if credited != 1 {
		t.Fatalf("reconcile credited = %d (checked=%d), want 1 (only the settled order)", credited, checked)
	}
	acc := &ledger.Account{}
	db.Where("org_id = ? AND type = ?", orgID, ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("balance after repair = %s, want 10", acc.PostedBalance)
	}
	var st1, st2 int8
	db.Raw(`SELECT status FROM topup_orders WHERE order_no='X402REC1'`).Scan(&st1)
	db.Raw(`SELECT status FROM topup_orders WHERE order_no='X402REC2'`).Scan(&st2)
	if st1 != topupPaid {
		t.Fatalf("settled order status = %d, want paid", st1)
	}
	if st2 != topupPending {
		t.Fatalf("unsettled order status = %d, want still pending (no proof, no credit)", st2)
	}
	// 二次对账幂等：不再补、余额不翻倍
	_, credited2, _ := srv.srv.ReconcileTopups(context.Background(), time.Minute, 100)
	if credited2 != 0 {
		t.Fatalf("second reconcile credited = %d, want 0 (idempotent)", credited2)
	}
	db.Where("org_id = ? AND type = ?", orgID, ledger.AccountUserCredit).First(acc)
	if !acc.PostedBalance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("balance after 2nd reconcile = %s, want still 10", acc.PostedBalance)
	}
}
