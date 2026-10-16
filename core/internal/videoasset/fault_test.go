package videoasset

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAcceptedThenDatabaseFailureDoesNotResubmit(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	group, _, err := s.CreateGroup(ctx, createContext("db-fault"), GroupCreate{Kind: KindVirtual, Name: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_confirmation BEFORE UPDATE ON video_asset_operations WHEN NEW.status='confirmed' BEGIN SELECT RAISE(FAIL,'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	var g Group
	db.Where("group_id = ?", group.ID).Take(&g)
	if g.ProviderID != nil || g.Status != StatusPending {
		t.Fatal("resource escaped failed response transaction")
	}
	db.Exec("DROP TRIGGER reject_confirmation")
	*now = now.Add(3 * time.Minute)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.calls["CreateAssetGroup"] != 1 {
		t.Fatal("paid-side acceptance resubmitted after DB fault")
	}
	var op Operation
	db.Where("resource_id = ?", group.ID).Take(&op)
	if op.Status != "unknown" {
		t.Fatal(op.Status)
	}
}

func TestConcurrentIdempotencyAcrossKeyVersions(t *testing.T) {
	s, db, _, _ := setup(t)
	options := s.options
	options.ActiveHMACKey = "h2"
	rotated, err := NewService(db, s.keys, s.evidence, options)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	errs := make(chan error, 2)
	for _, service := range []*Service{s, rotated} {
		wg.Add(1)
		go func(service *Service) {
			defer wg.Done()
			dto, _, err := service.CreateGroup(context.Background(), createContext("same"), GroupCreate{Kind: KindVirtual, Name: "Group"})
			ids <- dto.ID
			errs <- err
		}(service)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		} else if first != id {
			t.Fatal("rotation bypassed unique intent")
		}
	}
	var count int64
	db.Model(&Operation{}).Count(&count)
	if count != 1 {
		t.Fatal("multiple operations", count)
	}
}

func TestPortraitAuditAndSealedIdentityBinding(t *testing.T) {
	s, db, _, _ := setup(t)
	ctx := context.Background()
	c := createContext("portrait")
	dto, _, err := s.CreatePortrait(ctx, c, PortraitCreate{CallbackURL: "https://customer.example/return"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_h5_audit BEFORE INSERT ON video_asset_events WHEN NEW.kind='h5_url_read' BEGIN SELECT RAISE(FAIL,'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPortrait(ctx, 1, dto.ID, c.KeyID, true)
	if !errors.Is(err, ErrStorage) || got.AuthorizationURL != "" {
		t.Fatal("failed audit released H5", err)
	}
	db.Exec("DROP TRIGGER reject_h5_audit")
	var secret Secret
	db.Where("session_id = ?", dto.ID).Take(&secret)
	raw, err := s.keys.Decrypt(secret.ValueCipher)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(raw, `"OrgID":1`, `"OrgID":2`, 1)
	sealed, err := s.keys.Encrypt(tampered)
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&Secret{}).Where("secret_id = ?", secret.ID).Update("value_cipher", sealed)
	if got, err := s.GetPortrait(ctx, 1, dto.ID, c.KeyID, true); !errors.Is(err, ErrSecret) || got.AuthorizationURL != "" {
		t.Fatal("cross-tenant sealed value accepted", err)
	}
}

func TestUnverifiedMutableMediaAndRevokedLicenseRejectedBeforeSubmit(t *testing.T) {
	s, _, _, provider := setup(t)
	group := readyGroup(t, s, "group")
	s.options.VerifyMedia = func(context.Context, uint64, string, string) (MediaEvidence, error) {
		return MediaEvidence{ImmutableObjectRef: "looks-frozen", ObjectSHA256: strings.Repeat("a", 64), MIME: "image/png", ByteSize: 4096, Width: 640, Height: 480, Version: 1, VerifiedAt: s.now()}, nil
	}
	if _, _, err := s.CreateAsset(context.Background(), createContext("mutable"), AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://customer.example/mutable.png"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing approved immutable URL accepted", err)
	}
	if provider.calls["CreateAsset"] != 0 {
		t.Fatal("invalid media reached provider")
	}
	credentials := Credentials{AccessKey: "access-secret", SecretKey: "signing-secret", SessionToken: "session-secret"}
	if strings.Contains(fmt.Sprintf("%+v %#v", credentials, credentials), "signing-secret") {
		t.Fatal("credential formatter leaks")
	}
}

func TestBindingsRollbackOnOuterFinancialFailure(t *testing.T) {
	s, db, now, _ := setup(t)
	group := readyGroup(t, s, "group")
	asset := readyAsset(t, s, now, group.ID, "asset")
	abort := errors.New("ledger failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.ResolveBindingsTx(tx, 1, testScope(), "task-failed", []ReferenceInput{{AssetID: asset.ID, Role: "reference_image", ContentIndex: 0}}); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	var count int64
	db.Model(&TaskReference{}).Where("task_id = ?", "task-failed").Count(&count)
	if count != 0 {
		t.Fatal("binding survived financial rollback")
	}
}

func TestFixedCallbackStateSingleUseAndCannotGrantLicense(t *testing.T) {
	s, db, _, provider := setup(t)
	ctx := context.Background()
	portrait, _, err := s.CreatePortrait(ctx, createContext("callback"), PortraitCreate{CallbackURL: "https://customer.example/return"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse(provider.callbackURL)
	if err != nil || upstream.Host != "gateway.example" || upstream.Path != PortraitCallbackPath || upstream.Query().Get("state") == "" {
		t.Fatal("customer URL used as official callback")
	}
	state := upstream.Query().Get("state")
	var op Operation
	db.Where("resource_id = ?", portrait.ID).Take(&op)
	for _, payloadID := range []string{op.PayloadID, op.ResponsePayloadID} {
		var raw []byte
		access := invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "test", ReasonCode: "test", OperationID: op.ID}
		if payloadID == op.PayloadID {
			raw, err = s.evidence.ReadForExecution(ctx, 1, payloadID, access)
		} else {
			raw, err = s.evidence.ReadForRecovery(ctx, 1, payloadID, access)
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), state) {
			t.Fatal("callback credential persisted in invocation payload")
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET(PortraitCallbackPath, s.HandlePortraitCallback)
	forged := httptest.NewRequest(http.MethodGet, PortraitCallbackPath+"?state=forged&resultCode=10000&GroupId=evil", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, forged)
	if rec.Code != 400 {
		t.Fatal("forged callback accepted")
	}
	requestURL := PortraitCallbackPath + "?state=" + url.QueryEscape(state) + "&resultCode=10000&GroupId=evil&redirect=https://evil.example"
	valid := httptest.NewRequest(http.MethodGet, requestURL, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, valid)
	if rec.Code != 303 || rec.Header().Get("Location") != "https://customer.example/return" {
		t.Fatal(rec.Code, rec.Header())
	}
	var p Portrait
	db.Where("session_id = ?", portrait.ID).Take(&p)
	if p.SessionStatus != "pending" || p.LicenseStatus != "unknown" || p.GroupID != nil || p.CallbackUsedAt == nil {
		t.Fatal("callback granted identity or license")
	}
	replay := httptest.NewRequest(http.MethodGet, requestURL, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, replay)
	if rec.Code != 400 {
		t.Fatal("callback state replayed")
	}
}

func TestConsentDisabledBeforeDispatch(t *testing.T) {
	s, _, _, provider := setup(t)
	ctx := context.Background()
	group, _, err := s.CreateGroup(ctx, createContext("consent"), GroupCreate{Kind: KindVirtual, Name: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	resolver := s.options.ResolveScope
	s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
		runtime, err := resolver(ctx, org, scope)
		runtime.ExecutionRetentionConsent = false
		return runtime, err
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	dto, err := s.GetGroup(ctx, 1, group.ID)
	if err != nil || dto.Status != "failed" || provider.calls["CreateAssetGroup"] != 0 {
		t.Fatal("disabled consent reached provider", dto, err)
	}
}
