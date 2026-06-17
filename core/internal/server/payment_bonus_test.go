package server

import (
	"testing"

	"github.com/shopspring/decimal"
)

// computeRechargeBonus：选「amount≥min_usd 的最高档」rate，bonus=amount×rate；非法配置/无匹配→0。
func TestComputeRechargeBonus(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('recharge_bonus_tiers', ?)`,
		`[{"min_usd":"50","rate":"0.05"},{"min_usd":"200","rate":"0.1"}]`)
	// 四舍五入到 2 位（USD 分位），与前端 toFixed(2) 预览一致，保证「预览=实到」
	if got := srv.srv.computeRechargeBonus(decimal.RequireFromString("33")); !got.Equal(decimal.RequireFromString("0")) {
		t.Fatalf("33 below first tier bonus = %s, want 0", got)
	}
	db.Exec(`UPDATE ext_settings SET value=? WHERE name='recharge_bonus_tiers'`, `[{"min_usd":"10","rate":"0.333"}]`)
	if got := srv.srv.computeRechargeBonus(decimal.RequireFromString("33")); !got.Equal(decimal.RequireFromString("10.99")) {
		t.Fatalf("33×0.333 rounded = %s, want 10.99 (matches frontend toFixed(2))", got)
	}
	db.Exec(`UPDATE ext_settings SET value=? WHERE name='recharge_bonus_tiers'`,
		`[{"min_usd":"50","rate":"0.05"},{"min_usd":"200","rate":"0.1"}]`)
	for _, c := range []struct{ amount, want string }{
		{"20", "0"}, // 低于首档 → 无优惠
		{"49.99", "0"},
		{"50", "2.5"}, // 首档 5%
		{"100", "5"},
		{"199", "9.95"},
		{"200", "20"}, // 顶档 10%（取更高档，不是 5%）
		{"500", "50"},
	} {
		got := srv.srv.computeRechargeBonus(decimal.RequireFromString(c.amount))
		if !got.Equal(decimal.RequireFromString(c.want)) {
			t.Fatalf("bonus(%s) = %s, want %s", c.amount, got, c.want)
		}
	}
	// 非法 JSON → 0（静默不赠，不阻断）
	db.Exec(`UPDATE ext_settings SET value='not-json' WHERE name='recharge_bonus_tiers'`)
	if g := srv.srv.computeRechargeBonus(decimal.RequireFromString("100")); !g.IsZero() {
		t.Fatalf("invalid tiers bonus = %s, want 0", g)
	}
	// 负 rate 档被跳过
	db.Exec(`UPDATE ext_settings SET value=? WHERE name='recharge_bonus_tiers'`,
		`[{"min_usd":"10","rate":"-0.5"}]`)
	if g := srv.srv.computeRechargeBonus(decimal.RequireFromString("100")); !g.IsZero() {
		t.Fatalf("negative-rate bonus = %s, want 0", g)
	}
}

// 充值入账时按阶梯赠额单列入账（KindGrant / ref_type=topup_bonus），且幂等（重复入账不翻倍）。
func TestRechargeBonusCredited(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('recharge_bonus_tiers', ?)`,
		`[{"min_usd":"50","rate":"0.1"}]`)
	// 注册拿 orgID
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "rb@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "rb@gmail.com", "password": "longenough1"})
	orgID := uint64(login["org_id"].(float64))

	order := TopupOrder{
		OrderNo: "RB1", OrgID: orgID, Provider: "stripe",
		AmountUSD: decimal.RequireFromString("100"), PayAmount: decimal.RequireFromString("100"),
		PayCurrency: "USD", Status: topupPending, CreatedAt: srv.srv.now().Unix(),
	}
	if e := db.Create(&order).Error; e != nil {
		t.Fatalf("create order: %v", e)
	}
	already, err := srv.srv.creditPaidOrder("stripe", "RB1", "trade1", decimal.RequireFromString("100"), "USD")
	if err != nil || already {
		t.Fatalf("first credit = already:%v err:%v", already, err)
	}

	bonusSum := func() decimal.Decimal {
		var s string
		db.Raw(`SELECT COALESCE(SUM(j.delta),0) FROM ledger_journal j
			JOIN ledger_accounts a ON a.id=j.account_id
			WHERE a.org_id=? AND j.ref_type='topup_bonus'`, orgID).Scan(&s)
		return decimal.RequireFromString(s)
	}
	if got := bonusSum(); !got.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("bonus credited = %s, want 10", got)
	}
	// 幂等：再次入账（如 reconcile 重跑）→ already，bonus 不翻倍
	already, err = srv.srv.creditPaidOrder("stripe", "RB1", "trade1", decimal.RequireFromString("100"), "USD")
	if err != nil || !already {
		t.Fatalf("second credit = already:%v err:%v, want already=true", already, err)
	}
	if got := bonusSum(); !got.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("bonus after re-credit = %s, want 10 (idempotent)", got)
	}

	// 无优惠配置时不赠
	db.Exec(`UPDATE ext_settings SET value='' WHERE name='recharge_bonus_tiers'`)
	order2 := TopupOrder{
		OrderNo: "RB2", OrgID: orgID, Provider: "stripe",
		AmountUSD: decimal.RequireFromString("100"), PayAmount: decimal.RequireFromString("100"),
		PayCurrency: "USD", Status: topupPending, CreatedAt: srv.srv.now().Unix(),
	}
	db.Create(&order2)
	srv.srv.creditPaidOrder("stripe", "RB2", "trade2", decimal.RequireFromString("100"), "USD")
	if got := bonusSum(); !got.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("bonus total after no-tier topup = %s, want still 10", got)
	}
}

// 被邀请赠额：携有效邀请码注册才赠（invite_grant），无码/无效码不赠。
func TestInviteGrant(t *testing.T) {
	srv, db := setupRelayServer(t)
	db.Exec(`INSERT INTO ext_settings (name,value) VALUES ('invite_grant_usd','3')`)

	// 邀请人注册 + 取 aff_code
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "inviter@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "inviter@gmail.com", "password": "longenough1"})
	_, aff := srv.do(t, "GET", "/api/affiliate", login["token"].(string), nil)
	code := aff["aff_code"].(string)
	if code == "" {
		t.Fatal("inviter aff_code empty")
	}

	grantTotal := func(email string) decimal.Decimal {
		var s string
		db.Raw(`SELECT COALESCE(SUM(j.delta),0) FROM ledger_journal j
			JOIN ledger_accounts a ON a.id=j.account_id
			JOIN org_members om ON om.org_id=a.org_id
			JOIN users u ON u.id=om.user_id
			WHERE u.email_canonical=? AND j.ref_type='invite_grant'`, email).Scan(&s)
		return decimal.RequireFromString(s)
	}

	// 携有效邀请码 → 被邀请人得 3
	w, _ := srv.do(t, "POST", "/api/auth/register", "", map[string]any{
		"email": "invitee@gmail.com", "password": "longenough1", "inviter_code": code,
	})
	if w.Code != 201 {
		t.Fatalf("invited register = %d, want 201", w.Code)
	}
	if g := grantTotal("invitee@gmail.com"); !g.Equal(decimal.RequireFromString("3")) {
		t.Fatalf("invitee grant = %s, want 3", g)
	}

	// 无邀请码 → 不赠
	srv.do(t, "POST", "/api/auth/register", "", map[string]any{"email": "solo@gmail.com", "password": "longenough1"})
	if g := grantTotal("solo@gmail.com"); !g.IsZero() {
		t.Fatalf("solo grant = %s, want 0", g)
	}

	// 无效邀请码 → 不赠（Register 静默忽略无效码，InviterOrgID=0）
	srv.do(t, "POST", "/api/auth/register", "", map[string]any{
		"email": "badcode@gmail.com", "password": "longenough1", "inviter_code": "NONEXISTENT",
	})
	if g := grantTotal("badcode@gmail.com"); !g.IsZero() {
		t.Fatalf("bad-code grant = %s, want 0", g)
	}
}

// P2-8：网关回调币种与订单不符 → 拒收（金额数字相同但币种更便宜＝隐性少付）。
func TestCreditRejectsCurrencyMismatch(t *testing.T) {
	srv, db := setupRelayServer(t)
	srv.do(t, "POST", "/api/auth/register", "", map[string]string{"email": "cm@gmail.com", "password": "longenough1"})
	_, login := srv.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "cm@gmail.com", "password": "longenough1"})
	orgID := uint64(login["org_id"].(float64))

	// 一张 73 CNY 的订单
	order := TopupOrder{
		OrderNo: "CM1", OrgID: orgID, Provider: "epay",
		AmountUSD: decimal.RequireFromString("10"), PayAmount: decimal.RequireFromString("73"),
		PayCurrency: "CNY", Status: topupPending, CreatedAt: srv.srv.now().Unix(),
	}
	if e := db.Create(&order).Error; e != nil {
		t.Fatalf("create order: %v", e)
	}

	// 回调报 73 USD（币种不符）→ 必须拒收
	if _, err := srv.srv.creditPaidOrder("epay", "CM1", "t1", decimal.RequireFromString("73"), "USD"); err == nil {
		t.Fatal("currency mismatch (CNY order, USD callback) must be rejected")
	}
	var st int8
	db.Raw(`SELECT status FROM topup_orders WHERE order_no='CM1'`).Scan(&st)
	if st != topupPending {
		t.Fatalf("order status = %d, want still pending (not credited)", st)
	}
	// 同币种 73 CNY → 入账成功
	if already, err := srv.srv.creditPaidOrder("epay", "CM1", "t1", decimal.RequireFromString("73"), "CNY"); err != nil || already {
		t.Fatalf("matching currency credit = already:%v err:%v, want success", already, err)
	}
	// 币种空（网关未报）→ 跳过校验，正常入账（用另一单验证）
	order2 := TopupOrder{
		OrderNo: "CM2", OrgID: orgID, Provider: "epay",
		AmountUSD: decimal.RequireFromString("10"), PayAmount: decimal.RequireFromString("73"),
		PayCurrency: "CNY", Status: topupPending, CreatedAt: srv.srv.now().Unix(),
	}
	db.Create(&order2)
	if _, err := srv.srv.creditPaidOrder("epay", "CM2", "t2", decimal.RequireFromString("73"), ""); err != nil {
		t.Fatalf("empty-currency credit should skip check, got err %v", err)
	}
}
