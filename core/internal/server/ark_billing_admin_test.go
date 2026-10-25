package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestArkBillStrictDecoder(t *testing.T) {
	for _, body := range []string{
		`{"line_id":1,"line_id":2}`, `{"line_id":1,"tokens":999}`, `{"line_id":1.1}`,
		`{"line_id":18446744073709551616}`, `{"line_id":-1}`, `{"line_id":1} {}`,
		`null`, `[]`, `{"nested":{"x":1,"x":2}}`, `{"line_id":1`,
	} {
		var req struct {
			LineID uint64 `json:"line_id"`
		}
		if err := arkBillJSON([]byte(body), &req); err == nil {
			t.Fatalf("accepted invalid body %s", body)
		}
	}
	var good struct {
		LineID uint64 `json:"line_id"`
	}
	if err := arkBillJSON([]byte(`{"line_id":9007199254740993}`), &good); err != nil || good.LineID != 9007199254740993 {
		t.Fatalf("integer precision: %+v %v", good, err)
	}
}

func TestArkBillSharedTokenCannotReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{adminToken: []byte("shared-root-test"), now: time.Now}
	r := gin.New()
	su := r.Group("/api/admin", s.adminRequired(identity.PlatRoleAdmin), s.requireSuperadmin())
	s.registerArkBillingAdmin(su)
	for _, path := range []string{"/bill-evidence", "/bill-evidence/1/review", "/orgs/1/adjustments/1/review", "/orgs/1/adjustments/1/apply"} {
		req := httptest.NewRequest("POST", "/api/admin/video"+path, strings.NewReader(`{"reviewer_id":99}`))
		req.Header.Set("X-Admin-Token", "shared-root-test")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "individual_superadmin_session_required") {
			t.Fatalf("shared token %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

type arkBillFixture struct {
	s          *Server
	r          *gin.Engine
	db         *gorm.DB
	now        time.Time
	uid        string
	org        uint64
	scope      uint64
	taskID     string
	upstreamID string
	holdID     uint64
	tokens     [3]string
	users      [3]uint64
}

// Uses the separately migrated, dedicated MySQL database. It never resets or
// migrates tables and never connects to a paid provider endpoint.
func arkBillMySQLFixture(t *testing.T, status string) *arkBillFixture {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN required for bill admin MySQL integration")
	}
	if !strings.Contains(dsn, "/ark_test?") {
		t.Fatal("dedicated ark_test database required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	arkBillMust(t, err)
	sqlDB, err := db.DB()
	arkBillMust(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	var b [12]byte
	_, err = rand.Read(b[:])
	arkBillMust(t, err)
	f := &arkBillFixture{db: db, now: time.Now().UTC().Truncate(time.Millisecond), uid: hex.EncodeToString(b[:])}
	f.taskID = "task_bill_" + f.uid
	f.upstreamID = "upstream_bill_" + f.uid
	kr, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
	arkBillMust(t, err)
	f.s = &Server{db: db, keyring: kr, secret: []byte(strings.Repeat("s", 32)), adminToken: []byte("shared-root-test"), now: func() time.Time { return f.now }}
	org := identity.Organization{Slug: "bill-" + f.uid, Name: "Bill admin fixture", Group: "default", Status: 1}
	arkBillMust(t, db.Create(&org).Error)
	f.org = org.ID
	for i := range f.tokens {
		email := fmt.Sprintf("bill-%s-%d@example.test", f.uid, i)
		u := identity.User{Email: email, EmailCanonical: email, PasswordHash: "test-unused", Role: identity.PlatRoleSuperadmin, Status: identity.StatusActive}
		arkBillMust(t, db.Create(&u).Error)
		f.users[i] = u.ID
		f.tokens[i], _, err = identity.IssueSessionToken(f.s.secret, u.ID, org.ID, time.Hour, f.now)
		arkBillMust(t, err)
	}
	scope := video.ProviderScope{ScopeKey: "bill-" + f.uid, AccountRef: "bill-" + f.uid, Region: "cn-beijing", ProjectName: "test", Enabled: true, FXUSDPerCNY: "0.14", FXVersion: "test", FXObservedAt: f.now}
	arkBillMust(t, db.Create(&scope).Error)
	f.scope = scope.ID
	var key *identity.APIKey
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error {
		_, k, e := identity.IssueKey(tx, identity.IssueKeyInput{OrgID: org.ID, Name: "bill-test", Scopes: "video:*"})
		key = k
		return e
	}))
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error {
		a, e := ledger.EnsureAccount(tx, org.ID, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		_, _, e = ledger.Post(tx, ledger.PostInput{IdempotencyKey: "bill-seed-" + f.uid, AccountID: a.ID, Kind: ledger.KindCredit, Delta: decimal.NewFromInt(100), RefType: "test", RefID: f.uid})
		return e
	}))
	rule := videobilling.PricingRule{RuleID: "bill-" + f.uid, Version: "v1", PricingKey: videobilling.PricingKey{ProviderScopeID: scope.ID, Model: "seedance-2.0", Mode: "generate", Resolution: "720p", ServiceTier: "default"}, RetailUSDPer1M: decimal.NewFromInt(8), SupplierCNYPer1M: decimal.NewFromInt(46), RiskMultiplier: decimal.NewFromInt(1), FormulaVersion: "test-v1", SourceHash: strings.Repeat("a", 64), EffectiveFrom: f.now.Add(-time.Hour), Enabled: true}
	arkBillMust(t, db.Create(&rule).Error)
	quote, err := videobilling.Quote(rule, videobilling.MeteringInput{OutputDurationMS: 5000, Width: 1280, Height: 720, FPSNumerator: 24, FPSDenominator: 1, BoundsVerified: true, CapabilityVersion: "test-v1", EffectiveRatio: decimal.NewFromInt(1), FXUSDPerCNY: decimal.RequireFromString("0.14"), FXVersion: "test", FXObservedAt: f.now})
	arkBillMust(t, err)
	arkBillMust(t, db.Transaction(func(tx *gorm.DB) error {
		res, e := videobilling.ReserveTx(tx, videobilling.ReserveInput{BillingSchemaVersion: videoruntime.BillingV1, OrgID: org.ID, APIKeyID: key.ID, TaskID: f.taskID, RequestHMAC: strings.Repeat("a", 64), Quote: quote, Now: f.now})
		if e != nil {
			return e
		}
		f.holdID = res.Snapshot.HoldID
		tk := task.Task{TaskID: f.taskID, OrgID: org.ID, APIKeyID: key.ID, Model: "seedance-2.0", Provider: video.Provider, Status: status, UpstreamTaskID: f.upstreamID, HoldID: f.holdID, EstTokens: quote.EstimatedTokens, PriceOutUSDPer1M: rule.RetailUSDPer1M, HoldAmountUSD: quote.HoldUSD, GroupRatio: decimal.NewFromInt(1), SubmittedAt: f.now}
		if e = tx.Create(&tk).Error; e != nil {
			return e
		}
		state := video.State{TaskID: f.taskID, OrgID: org.ID, CreateRequestID: "request-" + f.uid, ProviderScopeID: scope.ID, BillingState: "pending_usage", SyncState: "healthy", ArtifactStatus: "pending", RowVersion: 1, BillingSnapshotID: res.Snapshot.ID, CreatedAt: f.now, UpdatedAt: f.now, NextPollAt: f.now.Add(time.Hour)}
		contract := videoruntime.LegacyV1()
		state.RuntimeVersion, state.AdapterVersion = contract.RuntimeVersion, contract.AdapterVersion
		state.ProtocolContractVersion, state.BillingSchemaVersion = contract.ProtocolContractVersion, contract.BillingSchemaVersion
		if e = tx.Create(&state).Error; e != nil {
			return e
		}
		return tx.Create(&video.UpstreamBinding{ProviderScopeID: scope.ID, UpstreamTaskID: f.upstreamID, OrgID: org.ID, TaskID: f.taskID, CreatedAt: f.now}).Error
	}))
	gin.SetMode(gin.TestMode)
	f.r = gin.New()
	su := f.r.Group("/api/admin", f.s.adminRequired(identity.PlatRoleAdmin), f.s.requireSuperadmin())
	f.s.registerArkBillingAdmin(su)
	return f
}
func arkBillMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *arkBillFixture) call(t *testing.T, actor int, method, path string, body any, want int) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	arkBillMust(t, err)
	req := httptest.NewRequest(method, "/api/admin/video"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens[actor])
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var result map[string]json.RawMessage
	arkBillMust(t, json.Unmarshal(w.Body.Bytes(), &result))
	return result
}
func arkBillNumber(t *testing.T, v json.RawMessage) uint64 {
	t.Helper()
	var n uint64
	arkBillMust(t, json.Unmarshal(v, &n))
	return n
}
func (f *arkBillFixture) row(n *int64, revision int64, cny string) arkBillSourceRow {
	return arkBillSourceRow{SourceRecordID: "row-" + f.uid, SourceVersion: fmt.Sprintf("r%d", revision), SourceRevision: revision, Granularity: videobilling.BillTaskTotal, UpstreamTaskID: f.upstreamID, Model: "seedance-2.0", PeriodStart: f.now.Add(-24 * time.Hour), PeriodEnd: f.now, CompletionTokens: n, AmountCNY: cny, ObservedAt: f.now}
}
func (f *arkBillFixture) upload(t *testing.T, rows ...arkBillSourceRow) uint64 {
	t.Helper()
	doc := arkBillDocument{Schema: "ark_supplier_bill_v1", ProviderScopeID: f.scope, OfficialBillID: "official-" + f.uid, SourceVersion: rows[0].SourceVersion, Currency: "CNY", Rows: rows}
	x := f.call(t, 0, "POST", "/bill-evidence", map[string]any{"source_key": "source-" + rows[0].SourceVersion, "document": doc}, 201)
	var src ArkBillEvidenceSource
	arkBillMust(t, json.Unmarshal(x["source"], &src))
	return src.ID
}
func (f *arkBillFixture) importSource(t *testing.T, source uint64, revision int64) uint64 {
	t.Helper()
	f.call(t, 1, "POST", fmt.Sprintf("/bill-evidence/%d/review", source), gin.H{"approved": true, "reason": "verified_official_export"}, 200)
	x := f.call(t, 0, "POST", "/bill-imports", gin.H{"source_id": source, "import_key": fmt.Sprintf("import-r%d", revision)}, 200)
	id := arkBillNumber(t, x["import_id"])
	x = f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/lines", id), gin.H{"row_number": 1}, 200)
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/complete", id), gin.H{}, 200)
	return arkBillNumber(t, x["line_id"])
}
func (f *arkBillFixture) taskPath(action string) string {
	return fmt.Sprintf("/orgs/%d/tasks/%s/%s", f.org, f.taskID, action)
}
func (f *arkBillFixture) balances(t *testing.T, posted, held string) {
	t.Helper()
	p, h, err := ledger.OrgBalances(f.db, f.org, ledger.AccountUserCredit)
	arkBillMust(t, err)
	if !p.Equal(decimal.RequireFromString(posted)) || !h.Equal(decimal.RequireFromString(held)) {
		t.Fatalf("posted=%s held=%s want %s/%s", p, h, posted, held)
	}
}

func TestArkBillMySQLReviewedImportSettlementAndAdjustment(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	n := int64(54000)
	source := f.upload(t, f.row(&n, 1, "2.48400000"))
	f.call(t, 0, "POST", "/bill-imports", gin.H{"source_id": source, "import_key": "before-review"}, 409)
	f.call(t, 0, "POST", fmt.Sprintf("/bill-evidence/%d/review", source), gin.H{"approved": true, "reason": "verified_official_export"}, 409)
	line := f.importSource(t, source, 1)
	f.balances(t, "100", "0.864")
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line, "completion_tokens": 1}, 422)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	f.balances(t, "99.568", "0")
	f.call(t, 1, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	f.balances(t, "99.568", "0")
	var tk task.Task
	arkBillMust(t, f.db.Where("task_id = ? AND org_id = ?", f.taskID, f.org).Take(&tk).Error)
	if tk.ActualTokens != 54000 || !tk.CostUSD.Equal(decimal.RequireFromString("0.432")) {
		t.Fatalf("projection %+v", tk)
	}
	n = 81000
	line2 := f.importSource(t, f.upload(t, f.row(&n, 2, "3.726")), 2)
	proposal := gin.H{"proposal_key": "correction-2", "line_id": line2, "reason": "verified_invoice_correction", "supplier_only": false, "expires_at": f.now.Add(time.Hour)}
	x := f.call(t, 0, "POST", f.taskPath("adjustments"), proposal, 201)
	pid := arkBillNumber(t, x["proposal_id"])
	path := fmt.Sprintf("/orgs/%d/adjustments/%d", f.org, pid)
	f.call(t, 1, "GET", path, nil, 200)
	f.call(t, 0, "POST", f.taskPath("adjustments"), proposal, 201)
	f.call(t, 0, "POST", path+"/apply", gin.H{}, 409)
	f.call(t, 0, "POST", path+"/review", gin.H{"approved": true, "reason": "verified_bill"}, 409)
	f.call(t, 1, "POST", path+"/review", gin.H{"approved": true, "reason": "verified_bill"}, 200)
	f.call(t, 1, "POST", path+"/review", gin.H{"approved": true, "reason": "verified_bill"}, 200)
	f.call(t, 2, "POST", path+"/apply", gin.H{}, 200)
	f.balances(t, "99.352", "0")
	f.call(t, 0, "POST", path+"/apply", gin.H{}, 200)
	f.balances(t, "99.352", "0")
	var apps int64
	arkBillMust(t, f.db.Model(&videobilling.AdjustmentApplication{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&apps).Error)
	if apps != 1 {
		t.Fatal("duplicate adjustment", apps)
	}
	var events, outbox int64
	arkBillMust(t, f.db.Model(&video.Event{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&events).Error)
	arkBillMust(t, f.db.Model(&video.Outbox{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&outbox).Error)
	if events != outbox || events < 4 {
		t.Fatalf("events=%d outbox=%d", events, outbox)
	}
	if events != 4 {
		t.Fatalf("replayed proposal/review emitted duplicate events: %d", events)
	}
	arkBillMust(t, f.db.Model(&ArkBillEvidenceSource{}).Where("provider_scope_id = ?", f.scope).Update("expires_at", f.now.Add(-time.Minute)).Error)
	f.call(t, 0, "POST", path+"/apply", gin.H{}, 200)
	f.balances(t, "99.352", "0")
	// Cross-Org paths cannot bind a known official line to another account.
	f.call(t, 0, "POST", fmt.Sprintf("/orgs/%d/adjustments/%d/apply", f.org+1000000, pid), gin.H{}, 404)
}

func TestArkBillMySQLUnsupportedRuntimeKeepsFunds(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	n := int64(54000)
	line := f.importSource(t, f.upload(t, f.row(&n, 1, "2.484")), 1)
	contract := videoruntime.LegacyV1()
	versions := []struct{ column, supported string }{
		{"runtime_version", contract.RuntimeVersion}, {"adapter_version", contract.AdapterVersion},
		{"protocol_contract_version", contract.ProtocolContractVersion}, {"billing_schema_version", contract.BillingSchemaVersion},
	}
	// Snapshot all task-local financial effects, not just a cached balance.
	snapshot := func(t *testing.T) string {
		t.Helper()
		var accounts []ledger.Account
		arkBillMust(t, f.db.Where("org_id = ?", f.org).Order("id").Find(&accounts).Error)
		var accountIDs []uint64
		for _, account := range accounts {
			accountIDs = append(accountIDs, account.ID)
		}
		var journal []ledger.JournalEntry
		arkBillMust(t, f.db.Where("account_id IN ?", accountIDs).Order("id").Find(&journal).Error)
		var hold ledger.Hold
		arkBillMust(t, f.db.First(&hold, f.holdID).Error)
		var tk task.Task
		arkBillMust(t, f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Take(&tk).Error)
		var st video.State
		arkBillMust(t, f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Take(&st).Error)
		var entries []videobilling.BillingEntry
		arkBillMust(t, f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Order("id").Find(&entries).Error)
		var events []video.Event
		arkBillMust(t, f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Order("id").Find(&events).Error)
		var outbox []video.Outbox
		arkBillMust(t, f.db.Where("org_id = ? AND task_id = ?", f.org, f.taskID).Order("event_id").Find(&outbox).Error)
		raw, err := json.Marshal([]any{accounts, journal, hold, tk, st, entries, events, outbox})
		arkBillMust(t, err)
		return string(raw)
	}
	for _, version := range versions {
		for _, bad := range []string{"", "future-unsupported-v999"} {
			name := version.column + "/unknown"
			if bad == "" {
				name = version.column + "/empty"
			}
			t.Run(name, func(t *testing.T) {
				arkBillMust(t, f.db.Model(&video.State{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Update(version.column, bad).Error)
				t.Cleanup(func() {
					arkBillMust(t, f.db.Model(&video.State{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Update(version.column, version.supported).Error)
				})
				before := snapshot(t)
				response := f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 409)
				var failure struct {
					Code string `json:"code"`
				}
				arkBillMust(t, json.Unmarshal(response["error"], &failure))
				if failure.Code != "runtime_contract_unsupported" || snapshot(t) != before {
					t.Fatal("unsupported contract did not preserve task, hold, journal and outbox")
				}
				f.balances(t, "100", "0.864")
			})
		}
	}
	// The same reviewed evidence becomes usable when the exact supported
	// contract is restored, and replay must still settle only once.
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	f.balances(t, "99.568", "0")
	settled := snapshot(t)
	f.call(t, 1, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	if snapshot(t) != settled {
		t.Fatal("settlement replay changed financial effects")
	}
}

func TestArkBillMySQLUnknownUsageAndUnknownSubmissionRemainHeld(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	line := f.importSource(t, f.upload(t, f.row(nil, 1, "0")), 1)
	x := f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	if string(x["billing_state"]) != `"pending_usage"` {
		t.Fatalf("unknown usage: %s", x["billing_state"])
	}
	f.balances(t, "100", "0.864")
	zero := int64(0)
	line2 := f.importSource(t, f.upload(t, f.row(&zero, 2, "0")), 2)
	arkBillMust(t, f.db.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Update("status", "submission_unknown").Error)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line2}, 409)
	f.balances(t, "100", "0.864")
	arkBillMust(t, f.db.Model(&task.Task{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Update("status", "succeeded").Error)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line2}, 200)
	f.balances(t, "100", "0")
}

func TestArkBillMySQLAuditFailureRollsBackMoneyAndOutbox(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	n := int64(54000)
	line := f.importSource(t, f.upload(t, f.row(&n, 1, "2.484")), 1)
	callback := "ark-bill-fail-audit-" + f.uid
	arkBillMust(t, f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			tx.AddError(fmt.Errorf("injected audit failure"))
		}
	}))
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 503)
	arkBillMust(t, f.db.Callback().Create().Remove(callback))
	f.balances(t, "100", "0.864")
	var count int64
	arkBillMust(t, f.db.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&count).Error)
	if count != 0 {
		t.Fatal("entry escaped rollback")
	}
	arkBillMust(t, f.db.Model(&video.Outbox{}).Where("org_id = ? AND task_id = ?", f.org, f.taskID).Count(&count).Error)
	if count != 0 {
		t.Fatal("outbox escaped rollback")
	}
	callback = "ark-bill-fail-outbox-" + f.uid
	arkBillMust(t, f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "video_outbox" {
			tx.AddError(fmt.Errorf("injected outbox failure"))
		}
	}))
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 503)
	arkBillMust(t, f.db.Callback().Create().Remove(callback))
	f.balances(t, "100", "0.864")
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	f.balances(t, "99.568", "0")
}

func TestArkBillMySQLAggregateIncompleteImportAndEvidenceTamper(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	n := int64(54000)
	aggregate := f.row(nil, 1, "-0.50000000")
	aggregate.SourceRecordID = "aggregate-" + f.uid
	aggregate.Granularity = videobilling.BillAggregate
	aggregate.UpstreamTaskID = ""
	source := f.upload(t, f.row(&n, 1, "2.484"), aggregate)
	f.call(t, 1, "POST", fmt.Sprintf("/bill-evidence/%d/review", source), gin.H{"approved": true, "reason": "verified_official_export"}, 200)
	x := f.call(t, 0, "POST", "/bill-imports", gin.H{"source_id": source, "import_key": "two-rows"}, 200)
	id := arkBillNumber(t, x["import_id"])
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/complete", id), gin.H{}, 409)
	x = f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/lines", id), gin.H{"row_number": 1}, 200)
	line := arkBillNumber(t, x["line_id"])
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/complete", id), gin.H{}, 409)
	x = f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/lines", id), gin.H{"row_number": 2}, 200)
	aggregateID := arkBillNumber(t, x["line_id"])
	if string(x["state"]) != `"quarantined"` {
		t.Fatalf("aggregate state %s", x["state"])
	}
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/lines", id), gin.H{"row_number": 2}, 200)
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/complete", id), gin.H{}, 200)
	f.call(t, 0, "POST", fmt.Sprintf("/bill-lines/%d/bind", aggregateID), gin.H{}, 409)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": aggregateID}, 409)
	f.balances(t, "100", "0.864")
	// A modified projection is never trusted over the reviewed encrypted row.
	arkBillMust(t, f.db.Model(&videobilling.BillLine{}).Where("id = ?", line).Update("completion_tokens", 1).Error)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 409)
	f.balances(t, "100", "0.864")
}

func TestArkBillMySQLRefreshExpiredEvidenceWithoutInventedRevision(t *testing.T) {
	f := arkBillMySQLFixture(t, "succeeded")
	n := int64(54000)
	row := f.row(&n, 1, "2.484")
	source := f.upload(t, row)
	line := f.importSource(t, source, 1)
	var old ArkBillEvidenceSource
	arkBillMust(t, f.db.Where("id = ?", source).Take(&old).Error)
	arkBillMust(t, f.db.Model(&ArkBillEvidenceSource{}).Where("id = ?", source).Update("expires_at", f.now.Add(-time.Minute)).Error)
	arkBillMust(t, f.db.Table("invocation_payloads").Where("payload_id = ? AND org_id = ?", old.PayloadRef, old.StorageOrgID).Update("capture_status", "expired").Error)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 409)
	f.balances(t, "100", "0.864")
	doc := arkBillDocument{Schema: "ark_supplier_bill_v1", ProviderScopeID: f.scope, OfficialBillID: "official-" + f.uid, SourceVersion: "r1", Currency: "CNY", Rows: []arkBillSourceRow{row}}
	x := f.call(t, 0, "POST", "/bill-evidence", gin.H{"source_key": "refreshed-evidence", "document": doc}, 201)
	var fresh ArkBillEvidenceSource
	arkBillMust(t, json.Unmarshal(x["source"], &fresh))
	f.call(t, 1, "POST", fmt.Sprintf("/bill-evidence/%d/review", fresh.ID), gin.H{"approved": true, "reason": "verified_official_export"}, 200)
	x = f.call(t, 0, "POST", "/bill-imports", gin.H{"source_id": fresh.ID, "import_key": "refresh-import"}, 200)
	iid := arkBillNumber(t, x["import_id"])
	x = f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/lines", iid), gin.H{"row_number": 1}, 200)
	if arkBillNumber(t, x["line_id"]) != line {
		t.Fatal("refresh invented another supplier line")
	}
	f.call(t, 0, "POST", fmt.Sprintf("/bill-imports/%d/complete", iid), gin.H{}, 200)
	f.call(t, 0, "POST", f.taskPath("settle-bill"), gin.H{"line_id": line}, 200)
	f.balances(t, "99.568", "0")
	var persisted videobilling.BillLine
	arkBillMust(t, f.db.Where("id = ?", line).Take(&persisted).Error)
	if persisted.SourceRevision != 1 || persisted.PayloadRef != old.PayloadRef {
		t.Fatal("immutable bill evidence was rewritten")
	}
	var event video.Event
	arkBillMust(t, f.db.Where("org_id = ? AND task_id = ? AND type = ?", f.org, f.taskID, "billing.settled").Take(&event).Error)
	if event.EvidenceRef != fresh.PayloadRef {
		t.Fatal("settlement did not record freshly verified source")
	}
}
