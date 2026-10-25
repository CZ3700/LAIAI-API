package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestInvocationQueryGateBoundsReleaseAndIdentity(t *testing.T) {
	var gate invocationQueryGate
	var releases []func()
	for _, subject := range []string{"user:7", "user:7", "user:8", "root-token"} {
		release, ok := gate.tryAcquire(subject)
		if !ok {
			t.Fatal("capacity rejected too soon")
		}
		releases = append(releases, release)
	}
	for _, subject := range []string{"user:7", "user:9", ""} {
		if _, ok := gate.tryAcquire(subject); ok {
			t.Fatal("global capacity bypass")
		}
	}
	releases[3]()
	if _, ok := gate.tryAcquire("user:7"); ok {
		t.Fatal("per-subject capacity bypass")
	}
	for _, release := range releases {
		release()
		release()
	}
	if gate.active != 0 || len(gate.subjects) != 0 {
		t.Fatal("completed subjects leaked")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, ok := gate.tryAcquire("user:1"); ok {
				release()
				release()
			}
		}()
	}
	wg.Wait()
	if gate.active != 0 || len(gate.subjects) != 0 {
		t.Fatal("concurrent release leaked")
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("claims", &identity.SessionClaims{UserID: 7, OrgID: 1})
	a, org, ok := invocationQueryIdentity(c, false)
	if !ok || org != 1 {
		t.Fatal("session scope lost")
	}
	c.Set("claims", &identity.SessionClaims{UserID: 7, OrgID: 2})
	b, org, ok := invocationQueryIdentity(c, false)
	if !ok || org != 2 || a != b {
		t.Fatal("organization switch changed principal bucket")
	}
	c.Set("actor_role", identity.PlatRoleSuperadmin)
	c.Set("actor_id", uint64(7))
	b, _, ok = invocationQueryIdentity(c, true)
	if !ok || a != b {
		t.Fatal("named admin received another principal bucket")
	}
	c.Set("actor_id", uint64(0))
	if _, _, ok := invocationQueryIdentity(c, true); ok {
		t.Fatal("anonymous admin accepted as root")
	}
	c.Set("actor_label", "root-token")
	if who, _, ok := invocationQueryIdentity(c, true); !ok || who != "root-token" {
		t.Fatal("root bucket not fixed")
	}
}

func TestInvocationQueryDispatchKeepsExplicitInvalidVersionOutOfLegacy(t *testing.T) {
	s := &Server{now: time.Now}
	for _, raw := range []string{"filter_version=", "filter_version=2", "filter_version=1&filter_version=1", "filter_version=%zz", "filter%5fversion=9", "filter_version=1&secret=do-not-echo"} {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/?"+raw, nil)
		if !s.dispatchInvocationQueryV1(ctx, false) || w.Code != 422 || strings.Contains(w.Body.String(), "do-not-echo") {
			t.Fatal("invalid version widened into legacy or exposed input", raw, w.Code)
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?model=x", nil)
	if s.dispatchInvocationQueryV1(c, false) {
		t.Fatal("legacy unversioned query was intercepted")
	}
}

func invocationSettingsTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE ext_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	return &Server{db: db, now: func() time.Time { return time.Date(2026, 10, 26, 1, 2, 3, 0, time.UTC) }}
}

func TestInvocationQueryFXSnapshotFailsClosedAndPreservesMicroAmounts(t *testing.T) {
	s := invocationSettingsTestServer(t)
	money, source, err := s.invocationMoneySnapshot(context.Background(), "USD")
	if err != nil || source != "default_missing" || money.CNYPerUSD != defaultCNYPerUSD {
		t.Fatal("missing default", source, err)
	}
	for _, test := range []struct{ raw, source, rate string }{{"", "default_empty", defaultCNYPerUSD}, {"  ", "default_empty", defaultCNYPerUSD}, {"6.65000000", "setting", "6.65"}} {
		if err := s.db.Save(&Setting{Name: "cny_per_usd", Value: test.raw}).Error; err != nil {
			t.Fatal(err)
		}
		money, source, err = s.invocationMoneySnapshot(context.Background(), "CNY")
		if err != nil || source != test.source || money.CNYPerUSD != test.rate {
			t.Fatal("incorrect fixed FX", source, err)
		}
	}
	if got := money.amount(decimal.RequireFromString("0.00000001")); got != "0.0000000665" {
		t.Fatal("micro amount rounded away", got)
	}
	if err := s.db.Save(&Setting{Name: "cny_per_usd", Value: "7.3"}).Error; err != nil {
		t.Fatal(err)
	}
	if money.amount(decimal.NewFromInt(1)) != "6.65" {
		t.Fatal("fixed snapshot changed after setting update")
	}
	for _, bad := range []string{"malformed-secret-value", "0", "101", "1e1"} {
		if err := s.db.Save(&Setting{Name: "cny_per_usd", Value: bad}).Error; err != nil {
			t.Fatal(err)
		}
		money, source, err := s.invocationMoneySnapshot(context.Background(), "USD")
		if err == nil || money != nil || source != "" || strings.Contains(err.Error(), bad) {
			t.Fatal("invalid setting became successful fallback", err)
		}
	}
	if err := s.db.Callback().Query().Before("gorm:query").Register("test:fx_failure", func(tx *gorm.DB) { tx.AddError(errors.New("private-driver-diagnostic")) }); err != nil {
		t.Fatal(err)
	}
	if money, _, err := s.invocationMoneySnapshot(context.Background(), "CNY"); err == nil || money != nil || strings.Contains(err.Error(), "private-driver") {
		t.Fatal("DB failure became a default or leaked", err)
	}
}

func TestInvocationQueryAdminProjectionExplicitlyOmitsProtectedFields(t *testing.T) {
	items := invocationAdminItems([]invocation.Invocation{{ID: 1, OrgID: 2, ChannelID: 3, PayloadObjectKey: "protected", ClientIP: "private-ip", UserAgent: "private-agent", Referer: "private-ref"}})
	if len(items) != 1 || items[0]["org_id"] != uint64(2) || items[0]["channel_id"] != uint64(3) {
		t.Fatal("admin metadata missing")
	}
	for _, key := range []string{"payload_object_key", "client_ip", "user_agent", "referer", "key_hash", "api_key"} {
		if _, ok := items[0][key]; ok {
			t.Fatal("admin projection widened", key)
		}
	}
}

func TestInvocationQueryFXSuppressesInheritedWarnSQLLog(t *testing.T) {
	s := invocationSettingsTestServer(t)
	var captured bytes.Buffer
	s.db = s.db.Session(&gorm.Session{Logger: logger.New(log.New(&captured, "", 0), logger.Config{LogLevel: logger.Warn, SlowThreshold: time.Nanosecond})})
	const diagnostic = "private-fx-driver-diagnostic"
	if err := s.db.Callback().Query().After("gorm:query").Register("test:fx_warn_failure", func(tx *gorm.DB) { tx.AddError(errors.New(diagnostic)) }); err != nil {
		t.Fatal(err)
	}
	// Positive control: the inherited Warn logger does emit this failed query.
	var row Setting
	if err := s.db.Where("name = ?", "cny_per_usd").Take(&row).Error; err == nil || !strings.Contains(captured.String(), diagnostic) {
		t.Fatal("positive control did not capture inherited SQL logging")
	}
	captured.Reset()
	if money, _, err := s.invocationMoneySnapshot(context.Background(), "CNY"); err == nil || money != nil || strings.Contains(err.Error(), diagnostic) {
		t.Fatal("failed FX query exposed diagnostic or returned data")
	}
	if captured.Len() != 0 {
		t.Fatal("v1 FX query inherited SQL logging")
	}
	// The local v1 session must not mutate the legacy/global logger policy.
	_ = s.db.Where("name = ?", "cny_per_usd").Take(&row).Error
	if !strings.Contains(captured.String(), diagnostic) {
		t.Fatal("v1 FX query changed the parent logger")
	}
}
