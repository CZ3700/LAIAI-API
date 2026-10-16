package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

// Run against the separately migrated local/CI database. This covers the actual
// server assembly and confirmed-rollback retries without contacting a provider.
func TestMySQLArkConsoleFundOperationIntegration(t *testing.T) {
	f := arkBillMySQLFixture(t, "running")
	db, err := ledger.EnableActivityTracking(f.db)
	arkBillMust(t, err)
	arkBillMust(t, db.Create(&identity.OrgMember{OrgID: f.org, UserID: f.users[0], Role: identity.RoleOwner}).Error)
	r, s := NewWithServer(db, Options{AuthSecret: f.s.secret, Keyring: f.s.keyring, AuthRateBurst: 1000})
	call := func(method, path, token string, body any, want int) map[string]json.RawMessage {
		t.Helper()
		raw, e := json.Marshal(body)
		arkBillMust(t, e)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d", method, path, w.Code, want)
		}
		out := map[string]json.RawMessage{}
		arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	call("GET", "/api/fund-activities", "", nil, 401)
	grant := grantReq{OrgID: f.org, Mode: "add", AmountUSD: "5", Reason: "integration", IdempotencyKey: "console-" + f.uid}
	call("POST", "/api/admin/credits", f.tokens[0], grant, 200)
	call("POST", "/api/admin/credits", f.tokens[0], grant, 200)
	grant.AmountUSD = "6"
	call("POST", "/api/admin/credits", f.tokens[0], grant, 409)
	grant.Mode, grant.AmountUSD, grant.IdempotencyKey = "subtract", "1000", "reject-"+f.uid
	call("POST", "/api/admin/credits", f.tokens[0], grant, 409)

	var failed ledger.Operation
	arkBillMust(t, db.Where("org_id = ? AND idempotency_key = ?", f.org, "admin-adjust:"+grant.IdempotencyKey).Take(&failed).Error)
	if failed.Status != "failed" || failed.FailureCode != "insufficient_balance" {
		t.Fatal("confirmed rollback did not retain the failed attempt")
	}
	var failedMovements int64
	arkBillMust(t, db.Model(&ledger.Movement{}).Where("operation_id = ?", failed.ID).Count(&failedMovements).Error)
	if failedMovements != 0 {
		t.Fatal("failed attempt created a balance movement")
	}
	response := call("GET", "/api/fund-activities?view=operations&status=failed", f.tokens[0], nil, 200)
	var operations []ledger.Operation
	arkBillMust(t, json.Unmarshal(response["items"], &operations))
	if len(operations) != 1 || operations[0].ID != failed.ID {
		t.Fatal("failed-operation filter lost the persisted attempt")
	}
	call("GET", fmt.Sprintf("/api/fund-activities/%d", failed.ID), f.tokens[0], nil, 200)
	call("GET", "/api/fund-summary", f.tokens[0], nil, 200)

	// A correctly verified later payment can recover after a confirmed failed
	// credit attempt, while the original topup journal remains the idempotency gate.
	order := TopupOrder{OrderNo: "console-" + f.uid, OrgID: f.org, Provider: "epay", AmountUSD: decimal.NewFromInt(10), PayAmount: decimal.NewFromInt(10), PayCurrency: "USD", Status: topupPending, CreatedAt: time.Now().Unix()}
	arkBillMust(t, db.Create(&order).Error)
	if _, e := s.creditPaidOrder("epay", order.OrderNo, "fixture-trade", decimal.NewFromInt(5), "USD"); e == nil {
		t.Fatal("underpaid order credited")
	}
	already, e := s.creditPaidOrder("epay", order.OrderNo, "fixture-trade", decimal.NewFromInt(10), "USD")
	arkBillMust(t, e)
	if already {
		t.Fatal("failed prior attempt prevented the first valid credit")
	}
	already, e = s.creditPaidOrder("epay", order.OrderNo, "fixture-trade", decimal.NewFromInt(10), "USD")
	arkBillMust(t, e)
	if !already {
		t.Fatal("duplicate payment was not identified")
	}
	var topupMovements int64
	arkBillMust(t, db.Model(&ledger.Movement{}).Where("org_id = ? AND type = ? AND ref_id = ?", f.org, "topup", order.OrderNo).Count(&topupMovements).Error)
	if topupMovements != 1 {
		t.Fatalf("topup movements = %d, want one", topupMovements)
	}
	posted, _, e := ledger.OrgBalances(db, f.org, ledger.AccountUserCredit)
	arkBillMust(t, e)
	if !posted.Equal(decimal.NewFromInt(115)) {
		t.Fatalf("posted = %s, want 115", posted)
	}

	// Even an individual superadmin session uses its active organization on
	// customer fund endpoints; a request cannot substitute another organization.
	other := identity.Organization{Slug: "console-other-" + f.uid, Name: "Other fixture", Status: 1, Group: "default"}
	arkBillMust(t, db.Create(&other).Error)
	arkBillMust(t, db.Create(&identity.OrgMember{OrgID: other.ID, UserID: f.users[0], Role: identity.RoleOwner}).Error)
	otherToken, _, e := identity.IssueSessionToken(f.s.secret, f.users[0], other.ID, time.Hour, time.Now())
	arkBillMust(t, e)
	call("GET", fmt.Sprintf("/api/fund-activities/%d", failed.ID), otherToken, nil, 404)
	response = call("GET", fmt.Sprintf("/api/fund-activities?view=operations&org_id=%d", f.org), otherToken, nil, 200)
	arkBillMust(t, json.Unmarshal(response["items"], &operations))
	if len(operations) != 0 {
		t.Fatal("organization query parameter leaked another tenant's funds")
	}
}
