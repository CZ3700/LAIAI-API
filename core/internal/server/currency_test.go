package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/payment"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type currencyPaymentEngine struct {
	mockPayEngine
	request *payment.CreateRequest
}

func TestMySQLCurrencyPreferencesPreserveReservations(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	r, _ := NewWithServer(f.db, Options{AuthSecret: f.s.secret, Keyring: f.s.keyring, AuthRateBurst: 1000})
	var user identity.User
	arkBillMust(t, f.db.First(&user, f.users[0]).Error)
	if user.DisplayCurrency != "USD" {
		t.Fatal("new user default", user.DisplayCurrency)
	}
	snapshot := func() string {
		all := map[string]any{}
		for _, table := range []string{"ledger_accounts", "ledger_journal", "ledger_holds", "api_keys", "video_billing_snapshots"} {
			var rows []map[string]any
			// Ledger journal is joined through its account because it has no org_id.
			q := f.db.Table(table)
			if table == "ledger_journal" {
				q = q.Where("account_id IN (SELECT id FROM ledger_accounts WHERE org_id = ?)", f.org)
			} else if table == "ledger_holds" {
				q = q.Where("account_id IN (SELECT id FROM ledger_accounts WHERE org_id = ?)", f.org)
			} else {
				q = q.Where("org_id = ?", f.org)
			}
			arkBillMust(t, q.Order("id").Find(&rows).Error)
			all[table] = rows
		}
		b, _ := json.Marshal(all)
		return string(b)
	}
	before := snapshot()
	for _, cur := range []string{"CNY", "USD", "CNY"} {
		req := httptest.NewRequest("PUT", "/api/me/preferences", strings.NewReader(`{"display_currency":"`+cur+`"}`))
		req.Header.Set("Authorization", "Bearer "+f.tokens[0])
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if snapshot() != before {
		t.Fatal("preference modified financial snapshot")
	}
	arkBillMust(t, f.db.First(&user, f.users[0]).Error)
	if user.DisplayCurrency != "CNY" {
		t.Fatal(user.DisplayCurrency)
	}
	// Exercise the original reservation at the original USD amount after changing
	// preference; capture releases the remainder and preserves ledger drift zero.
	arkBillMust(t, f.db.Transaction(func(tx *gorm.DB) error {
		_, err := ledger.Capture(tx, f.holdID, decimal.RequireFromString("0.01"), "currency-regression")
		return err
	}))
	posted, held, err := ledger.OrgBalances(f.db, f.org, ledger.AccountUserCredit)
	arkBillMust(t, err)
	if !posted.Equal(decimal.RequireFromString("99.99")) || !held.IsZero() {
		t.Fatal(posted, held)
	}
	drifts, err := ledger.CheckDrift(f.db)
	arkBillMust(t, err)
	for _, d := range drifts {
		if d.OrgID == f.org {
			t.Fatal("currency fixture ledger drift", d)
		}
	}
}

func (m *currencyPaymentEngine) CreatePayment(ctx context.Context, req *payment.CreateRequest) (*payment.CreateResult, error) {
	copy := *req
	m.request = &copy
	return m.mockPayEngine.CreatePayment(ctx, req)
}

func TestCurrencyEPayRateAndLedgerInvariant(t *testing.T) {
	s, db := setupRelayServer(t)
	token := registerAndLogin(t, s, "currency-payment@example.com")
	engine := &currencyPaymentEngine{}
	old := engineForTest
	engineForTest = func(provider string) payment.Engine {
		if provider == "epay" {
			return engine
		}
		return nil
	}
	t.Cleanup(func() { engineForTest = old })
	for _, rate := range []string{"", "6.65", "corrupted"} {
		db.Save(&Setting{Name: "cny_per_usd", Value: rate})
		_, config := s.do(t, "GET", "/api/config", "", nil)
		_, methods := s.do(t, "GET", "/api/payment/methods", token, nil)
		expected := decimal.RequireFromString("7.3")
		if rate == "6.65" {
			expected = decimal.RequireFromString(rate)
		}
		if config["cny_per_usd"] != expected.String() || methods["cny_rate"] != expected.String() {
			t.Fatal(config, methods)
		}
		for _, cur := range []string{"USD", "CNY"} {
			s.do(t, "PUT", "/api/me/preferences", token, map[string]string{"display_currency": cur})
			w, out := s.do(t, "POST", "/api/payment/create", token, map[string]string{"provider": "epay", "amount_usd": "10"})
			if w.Code != 200 {
				t.Fatal(w.Code, out)
			}
			if engine.request.Currency != "CNY" || !engine.request.Amount.Equal(expected.Mul(decimal.NewFromInt(10))) {
				t.Fatal(engine.request)
			}
			var order TopupOrder
			if err := db.Where("order_no = ?", out["order_no"]).Take(&order).Error; err != nil {
				t.Fatal(err)
			}
			if !order.AmountUSD.Equal(decimal.NewFromInt(10)) || order.PayCurrency != "CNY" || !order.PayAmount.Equal(engine.request.Amount) {
				t.Fatal(order)
			}
			if _, err := s.srv.creditPaidOrder("epay", order.OrderNo, "fixture", order.PayAmount, "USD"); err == nil {
				t.Fatal("wrong callback currency credited")
			}
			if already, err := s.srv.creditPaidOrder("epay", order.OrderNo, "fixture", order.PayAmount, "CNY"); err != nil || already {
				t.Fatal(already, err)
			}
			if already, err := s.srv.creditPaidOrder("epay", order.OrderNo, "fixture", order.PayAmount, "CNY"); err != nil || !already {
				t.Fatal(already, err)
			}
		}
	}
	_, me := s.do(t, "GET", "/api/me", token, nil)
	posted, held, err := ledger.OrgBalances(db, uint64(me["org_id"].(float64)), ledger.AccountUserCredit)
	if err != nil || !posted.Equal(decimal.NewFromInt(60)) || !held.IsZero() {
		t.Fatal(posted, held, err)
	}
	drift, err := ledger.CheckDrift(db)
	if err != nil || len(drift) != 0 {
		t.Fatal(drift, err)
	}
}

func TestCurrencyPreferencesIsolationAndRollback(t *testing.T) {
	s, db := setupRelayServer(t)
	a := registerAndLogin(t, s, "currency-a@example.com")
	b := registerAndLogin(t, s, "currency-b@example.com")
	_, me := s.do(t, "GET", "/api/me", a, nil)
	if me["display_currency"] != "USD" || me["currency_display_available"] != false {
		t.Fatal(me)
	}
	uid, org := uint64(me["user_id"].(float64)), uint64(me["org_id"].(float64))
	_, other := s.do(t, "GET", "/api/me", b, nil)
	db.Create(&identity.OrgMember{OrgID: org, UserID: uint64(other["user_id"].(float64)), Role: identity.RoleMember})
	_, switched := s.do(t, "POST", "/api/auth/switch", b, map[string]any{"org_id": org})
	b = switched["token"].(string)
	before := func() string {
		// Snapshot financial tables: preferences must not touch accounts, journals,
		// holds, key quotas, usage records or configured prices.
		all := map[string]any{}
		for _, table := range []string{"ledger_accounts", "ledger_journal", "ledger_holds", "api_keys", "usage_records", "model_prices"} {
			var rows []map[string]any
			if db.Migrator().HasTable(table) {
				if err := db.Table(table).Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				all[table] = rows
			}
		}
		body, _ := json.Marshal(all)
		return string(body)
	}
	snapshot := before()
	s.do(t, "PATCH", "/api/me", a, map[string]string{"display_name": "Currency user"})
	for _, body := range []any{map[string]string{}, map[string]string{"display_currency": "EUR"}, map[string]string{"display_currency": "cny"}, map[string]any{"display_currency": "CNY", "user_id": other["user_id"]}} {
		if w, _ := s.do(t, "PUT", "/api/me/preferences", a, body); w.Code != 400 {
			t.Fatalf("invalid body accepted: %d", w.Code)
		}
	}
	if w, _ := s.do(t, "PUT", "/api/me/preferences", "", map[string]string{"display_currency": "CNY"}); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w, out := s.do(t, "PUT", "/api/me/preferences", a, map[string]string{"display_currency": "CNY"}); w.Code != 200 {
		t.Fatal(w.Code, out)
	}
	_, me = s.do(t, "GET", "/api/me", a, nil)
	if me["display_currency"] != "CNY" || me["display_name"] != "Currency user" {
		t.Fatal(me)
	}
	_, other = s.do(t, "GET", "/api/me", b, nil)
	if other["display_currency"] != "USD" {
		t.Fatal("same-org preference leaked", other)
	}
	if snapshot != before() {
		t.Fatal("display preference modified financial data")
	}
	if w, out := s.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"currency_display_enabled": "true", "currency_display_percent": "0", "currency_display_user_ids": fmt.Sprint(uid)}); w.Code != 200 {
		t.Fatal(w.Code, out)
	}
	_, me = s.do(t, "GET", "/api/me", a, nil)
	if me["currency_display_available"] != true {
		t.Fatal(me)
	}
	_, other = s.do(t, "GET", "/api/me", b, nil)
	if other["currency_display_available"] != false {
		t.Fatal(other)
	}
	_, team := s.do(t, "POST", "/api/orgs", a, map[string]string{"name": "Currency team"})
	_, switched = s.do(t, "POST", "/api/auth/switch", a, map[string]any{"org_id": team["org_id"]})
	_, me = s.do(t, "GET", "/api/me", switched["token"].(string), nil)
	if me["display_currency"] != "CNY" {
		t.Fatal(me)
	}
	_, logged := s.do(t, "POST", "/api/auth/login", "", map[string]string{"email": "currency-a@example.com", "password": "longenough1"})
	_, me = s.do(t, "GET", "/api/me", logged["token"].(string), nil)
	if me["display_currency"] != "CNY" {
		t.Fatal(me)
	}
	s.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"currency_display_enabled": "false"})
	_, me = s.do(t, "GET", "/api/me", a, nil)
	if me["display_currency"] != "CNY" || me["currency_display_available"] != false {
		t.Fatal(me)
	}
	db.Model(&identity.User{}).Where("id = ?", uid).Update("status", identity.StatusDisabled)
	if w, _ := s.do(t, "PUT", "/api/me/preferences", a, map[string]string{"display_currency": "USD"}); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestCurrencyRateValidationAndAtomicAudit(t *testing.T) {
	s, db := setupRelayServer(t)
	for _, raw := range []string{"0", "-7.3", "NaN", "Infinity", "1e1", "7.123456789", "100.01", "0.001", "junk"} {
		if w, _ := s.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"cny_per_usd": raw, "site_name": "must-not-save"}); w.Code != 400 {
			t.Fatalf("accepted %q", raw)
		}
		if s.srv.getSetting("site_name") == "must-not-save" {
			t.Fatal("partially applied invalid patch")
		}
	}
	for _, raw := range []string{"", "  ", "6.65000000", "7.3"} {
		if w, out := s.doAdmin(t, "PUT", "/api/admin/settings", map[string]string{"cny_per_usd": raw}); w.Code != 200 {
			t.Fatal(out)
		}
	}
	var logs []AuditLog
	db.Where("action = ? AND target = ?", "currency_setting_update", "cny_per_usd").Order("id").Find(&logs)
	if len(logs) != 2 || logs[0].Detail != `{"new":"6.65","old":""}` || logs[1].Detail != `{"new":"7.3","old":"6.65"}` {
		t.Fatal(logs)
	}
	for _, raw := range []string{"", "bad", "0", "-2", "1e4", "7.123456789"} {
		db.Save(&Setting{Name: "cny_per_usd", Value: raw})
		v, str := s.srv.effectiveCNYPerUSD()
		if str != "7.3" || !v.Mul(decimal.NewFromInt(10)).Equal(decimal.NewFromInt(73)) {
			t.Fatal(raw, str)
		}
	}
}

func TestCurrencyStableRollout(t *testing.T) {
	s, db := setupServer(t)
	db.Save(&Setting{Name: "currency_display_enabled", Value: "true"})
	for _, pct := range []int{0, 5, 25, 100} {
		db.Save(&Setting{Name: "currency_display_percent", Value: fmt.Sprint(pct)})
		for id := uint64(1); id < 1000; id++ {
			want := int(currencyBucket(id)) < pct
			if s.srv.currencyDisplayAvailable(id, 1) != want || s.srv.currencyDisplayAvailable(id, 2) != want {
				t.Fatal("unstable cohort")
			}
		}
	}
}
