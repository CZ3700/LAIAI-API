package videoasset

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSchema = `
PRAGMA foreign_keys=ON;
CREATE TABLE video_asset_owners(org_id INTEGER NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,owner_user_id INTEGER NOT NULL DEFAULT 0,owner_key_id INTEGER NOT NULL DEFAULT 0,manage_all INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(org_id,resource_type,resource_id));
CREATE TABLE invocation_payloads(payload_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,request_id TEXT NOT NULL,attempt_id TEXT NOT NULL,correlation_key TEXT UNIQUE NOT NULL,direction TEXT NOT NULL,purpose TEXT NOT NULL,schema_version INTEGER NOT NULL,body_format TEXT NOT NULL,encryption_key_id TEXT NOT NULL,wrapped_key TEXT NOT NULL,nonce BLOB NOT NULL,ciphertext_sha256 TEXT NOT NULL,plaintext_bytes INTEGER NOT NULL,redaction_version TEXT NOT NULL,capture_status TEXT NOT NULL,capture_representation TEXT NOT NULL DEFAULT 'legacy_unknown',object_ref TEXT NOT NULL,created_at DATETIME NOT NULL,expires_at DATETIME NOT NULL,cleanup_after DATETIME,object_deleted_at DATETIME);
CREATE TABLE invocation_payload_staging(payload_id TEXT PRIMARY KEY,ciphertext BLOB NOT NULL,attempts INTEGER NOT NULL,next_attempt_at DATETIME NOT NULL,lease_owner TEXT NOT NULL,lease_expires_at DATETIME,fencing_token INTEGER NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE invocation_payload_accesses(access_id TEXT NOT NULL,phase TEXT NOT NULL,org_id INTEGER NOT NULL,payload_id TEXT NOT NULL,actor_role TEXT NOT NULL,actor_id TEXT NOT NULL,reason_code TEXT NOT NULL,operation_id TEXT NOT NULL,outcome TEXT NOT NULL,safe_error_code TEXT NOT NULL,created_at DATETIME NOT NULL,PRIMARY KEY(access_id,phase));
CREATE TABLE video_asset_tenant_locks(org_id INTEGER PRIMARY KEY,version INTEGER NOT NULL);
CREATE TABLE video_asset_groups(group_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,account_id TEXT NOT NULL,region TEXT NOT NULL,project TEXT NOT NULL,provider_id TEXT,status TEXT NOT NULL,review_state TEXT NOT NULL,portrait_id TEXT NOT NULL,create_operation_id TEXT NOT NULL,delete_operation_id TEXT NOT NULL,row_version INTEGER NOT NULL,deleted_at DATETIME,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,group_id),UNIQUE(scope_id,provider_id));
CREATE TABLE video_assets(asset_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,group_id TEXT NOT NULL,media_type TEXT NOT NULL,name TEXT NOT NULL,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,provider_id TEXT,status TEXT NOT NULL,review_state TEXT NOT NULL,source_payload_id TEXT NOT NULL,immutable_object_ref TEXT NOT NULL,object_sha256 TEXT NOT NULL,mime TEXT NOT NULL,byte_size INTEGER NOT NULL,duration_ms INTEGER NOT NULL,width INTEGER NOT NULL,height INTEGER NOT NULL,fps_num INTEGER NOT NULL,fps_den INTEGER NOT NULL,metadata_version INTEGER NOT NULL,verified_at DATETIME,create_operation_id TEXT NOT NULL,safe_error_code TEXT NOT NULL,row_version INTEGER NOT NULL,deleted_at DATETIME,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,asset_id),UNIQUE(scope_id,provider_id),FOREIGN KEY(org_id,group_id)REFERENCES video_asset_groups(org_id,group_id));
CREATE TABLE video_task_asset_refs(org_id INTEGER NOT NULL,task_id TEXT NOT NULL,asset_id TEXT NOT NULL,group_id TEXT NOT NULL,content_index INTEGER NOT NULL,role TEXT NOT NULL,asset_metadata_version INTEGER NOT NULL,authorization_version INTEGER NOT NULL,released_at DATETIME,created_at DATETIME NOT NULL,PRIMARY KEY(org_id,task_id,content_index),FOREIGN KEY(org_id,asset_id)REFERENCES video_assets(org_id,asset_id));
CREATE TABLE video_portrait_authorizations(session_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,owner_user_id INTEGER NOT NULL,created_by_key_id INTEGER NOT NULL,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,account_id TEXT NOT NULL,region TEXT NOT NULL,project TEXT NOT NULL,session_status TEXT NOT NULL,license_status TEXT NOT NULL,create_operation_id TEXT NOT NULL,callback_target_id TEXT NOT NULL,secret_ref TEXT NOT NULL,session_expires_at DATETIME,group_id TEXT UNIQUE,valid_from DATETIME,valid_until DATETIME,license_scope TEXT NOT NULL,license_version INTEGER NOT NULL,license_evidence_ref TEXT NOT NULL,license_source TEXT NOT NULL,last_verified_at DATETIME,reverify_due_at DATETIME,revoked_at DATETIME,row_version INTEGER NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,UNIQUE(org_id,session_id));
CREATE TABLE video_portrait_secrets(secret_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,session_id TEXT NOT NULL,value_cipher TEXT NOT NULL,expires_at DATETIME NOT NULL,destroyed_at DATETIME,created_at DATETIME NOT NULL,UNIQUE(org_id,session_id),FOREIGN KEY(org_id,session_id)REFERENCES video_portrait_authorizations(org_id,session_id));
ALTER TABLE video_portrait_authorizations ADD COLUMN callback_state_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE video_portrait_authorizations ADD COLUMN callback_state_version TEXT NOT NULL DEFAULT '';
ALTER TABLE video_portrait_authorizations ADD COLUMN callback_used_at DATETIME;
CREATE TABLE video_asset_operations(operation_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,action TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,request_id TEXT NOT NULL,canonical_path TEXT NOT NULL,hmac_key_version TEXT NOT NULL,idempotency_hash TEXT NOT NULL,body_hash TEXT NOT NULL,scope_id TEXT NOT NULL,scope_version INTEGER NOT NULL,account_id TEXT NOT NULL,region TEXT NOT NULL,project TEXT NOT NULL,status TEXT NOT NULL,payload_id TEXT NOT NULL,response_payload_id TEXT NOT NULL,lease_owner TEXT NOT NULL,lease_expires_at DATETIME,fence INTEGER NOT NULL,http_status INTEGER NOT NULL,upstream_request_id TEXT NOT NULL,safe_error_code TEXT NOT NULL,receipt TEXT NOT NULL,created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,next_attempt_at DATETIME NOT NULL,UNIQUE(org_id,canonical_path,idempotency_hash));
CREATE TABLE video_asset_events(event_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,operation_id TEXT NOT NULL,kind TEXT NOT NULL,evidence_ref TEXT NOT NULL,actor_id TEXT NOT NULL,created_at DATETIME NOT NULL);
`

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeProvider struct {
	callbackURL          string
	mu                   sync.Mutex
	calls                map[string]int
	unknown              bool
	assetStatus, project string
	groupByAsset         map[string]string
}

func (f *fakeProvider) roundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	action := r.URL.Query().Get("Action")
	f.calls[action]++
	if f.unknown {
		return nil, errors.New("connection lost after possible acceptance")
	}
	var wire map[string]any
	var body = map[string]string{}
	raw, _ := io.ReadAll(r.Body)
	if json.Unmarshal(raw, &wire) != nil {
		return nil, errors.New("invalid fixture")
	}
	for k, v := range wire {
		if text, ok := v.(string); ok {
			body[k] = text
		}
	}
	result := map[string]any{}
	switch action {
	case "ListAssets", "ListAssetGroups":
		result["Items"] = []any{}
	case "CreateAssetGroup":
		result["Id"] = "upstream-group-" + strconv.Itoa(f.calls[action])
	case "CreateAsset":
		id := "upstream-asset-" + strconv.Itoa(f.calls[action])
		result["Id"] = id
		f.groupByAsset[id] = body["GroupId"]
	case "GetAsset":
		result = map[string]any{"Id": body["Id"], "GroupId": f.groupByAsset[body["Id"]], "ProjectName": f.project, "AssetType": "Image", "Status": f.assetStatus, "URL": "https://private.example/signed?signature=private-media-signature", "Error": map[string]any{"Code": "ContentRestricted", "Message": "untrusted raw response secret"}}
	case "UpdateAsset", "UpdateAssetGroup":
		result["Id"] = body["Id"]
	case "CreateVisualValidateSession":
		f.callbackURL = body["CallbackURL"]
		result = map[string]any{"BytedToken": "byted-fixture-secret", "H5Link": "https://verify.volcengine.com/session?ticket=h5-fixture-secret", "CallbackURL": body["CallbackURL"]}
	case "GetVisualValidateResult":
		if body["BytedToken"] != "byted-fixture-secret" {
			return nil, errors.New("wrong portrait token")
		}
		result["GroupId"] = "official-real-group"
	}
	data := bodyJSON(map[string]any{"ResponseMetadata": map[string]any{"RequestId": "official-request", "Action": action, "Version": managementVersion, "Service": "ark", "Region": "cn-beijing"}, "Result": result})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}, Request: r}, nil
}
func testScope() Scope {
	return Scope{ID: "scope1", Version: 1, AccountID: "account1", Region: "cn-beijing", Project: "project1"}
}
func setup(t *testing.T) (*Service, *gorm.DB, *time.Time, *fakeProvider) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(testSchema + consoleTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC)
	keys, err := crypto.NewKeyring("k1:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "k1")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := invocation.NewVideoStore(db, keys, invocation.VideoStoreOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{calls: map[string]int{}, assetStatus: "Active", project: "project1", groupByAsset: map[string]string{}}
	options := Options{Now: func() time.Time { return now }, HMACKeys: map[string][]byte{"h1": bytes.Repeat([]byte{3}, 32), "h2": bytes.Repeat([]byte{4}, 32)}, ActiveHMACKey: "h1", Client: NewManagementClient(&http.Client{Transport: transportFunc(provider.roundTrip)}),
		ResolveScope: func(_ context.Context, _ uint64, scope Scope) (RuntimeScope, error) {
			if scope.ID != "" && !sameScope(scope, testScope()) {
				return RuntimeScope{}, ErrUnsupported
			}
			return RuntimeScope{Scope: testScope(), Credentials: Credentials{AccessKey: "AKTEST", SecretKey: "test-secret"}, Capabilities: Capabilities{VirtualGroups: true, Images: true, Videos: true, Audio: true, Portrait: true, DeleteVirtualEmptyGroups: true, Sync: true, Update: true, DeleteAssets: true, ExclusiveProjectWrites: true}, ExecutionRetentionConsent: true, CaptureHistory: true, RetentionDays: 30}, nil
		},
		VerifyMedia: func(_ context.Context, _ uint64, _ string, _ string) (MediaEvidence, error) {
			return MediaEvidence{ImmutableObjectRef: "immutable1", FetchURL: "https://media.example/immutable.png", ObjectSHA256: strings.Repeat("a", 64), MIME: "image/png", ByteSize: 4096, Width: 640, Height: 480, Version: 1, VerifiedAt: now}, nil
		},
		ResolveCallback: func(_ context.Context, _ uint64, url string) (CallbackTarget, error) {
			if url != "https://customer.example/return" {
				return CallbackTarget{}, ErrInvalid
			}
			return CallbackTarget{ID: "callback1", URL: url}, nil
		},
	}
	options.PlatformCallbackURL = "https://gateway.example" + PortraitCallbackPath
	options.ResolveCallbackByID = func(_ context.Context, _ uint64, id string) (CallbackTarget, error) {
		if id != "callback1" {
			return CallbackTarget{}, ErrInvalid
		}
		return CallbackTarget{ID: id, URL: "https://customer.example/return"}, nil
	}
	service, err := NewService(db, keys, evidence, options)
	if err != nil {
		t.Fatal(err)
	}
	return service, db, &now, provider
}
func createContext(id string) CreateContext {
	return CreateContext{OrgID: 1, UserID: 2, KeyID: 3, RequestID: "req-" + id, IdempotencyKey: "idem-" + id}
}
func adminContext(id string) CreateContext {
	c := createContext(id)
	c.CallerKind, c.KeyID, c.Superadmin = "session_user", 0, true
	return c
}
func readyGroup(t *testing.T, s *Service, id string) GroupDTO {
	t.Helper()
	group, _, err := s.CreateGroup(context.Background(), createContext(id), GroupCreate{Kind: KindVirtual, Name: "A useful group"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	group, err = s.GetGroup(context.Background(), 1, group.ID)
	if err != nil || group.Status != "ready" {
		t.Fatal(group, err)
	}
	return group
}
func readyAsset(t *testing.T, s *Service, now *time.Time, group string, id string) AssetDTO {
	t.Helper()
	c := createContext(id)
	var owner Ownership
	if err := s.db.Where("org_id = ? AND resource_type = ? AND resource_id = ?", 1, "asset_group", group).Take(&owner).Error; err != nil {
		t.Fatal(err)
	}
	c.UserID, c.KeyID = owner.OwnerUserID, owner.OwnerKeyID
	if c.UserID > 0 {
		c.CallerKind, c.KeyID = "session_user", 0
	}
	asset, _, err := s.CreateAsset(context.Background(), c, AssetCreate{GroupID: group, MediaType: "image", URL: "https://media.example/immutable.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	asset, err = s.GetAsset(context.Background(), 1, asset.ID)
	if err != nil || asset.Status != "ready" {
		t.Fatal(asset, err)
	}
	return asset
}

func TestSignedManagementRequestAndBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC)
	body := []byte(`{"ProjectName":"project1","Id":"asset-test"}`)
	req, err := signedRequest(context.Background(), testScope(), Credentials{AccessKey: "AKTEST", SecretKey: "test-secret"}, "GetAsset", body, now)
	if err != nil {
		t.Fatal(err)
	}
	// Independent Python hashlib/hmac fixture derived from the official V4
	// canonical-header and signing-key specification, fixed timestamp/body.
	expected := "HMAC-SHA256 Credential=AKTEST/20261016/cn-beijing/ark/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=39ebf6bbff96df6287247d5b5e8b268d18b29212b5d00778ad3155ae1ec36dcd"
	if req.Header.Get("Authorization") != expected || req.URL.Host != managementHost || req.GetBody != nil {
		t.Fatal("signature or replay policy mismatch")
	}
	if _, err := signedRequest(context.Background(), testScope(), Credentials{AccessKey: "AKTEST", SecretKey: "test-secret"}, "UnverifiedAction", body, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("unverified action signed")
	}
	wrong := testScope()
	wrong.Region = "ap-southeast-1"
	if _, err := signedRequest(context.Background(), wrong, Credentials{AccessKey: "AKTEST", SecretKey: "test-secret"}, "GetAsset", body, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("unverified region signed")
	}
	calls := 0
	client := NewManagementClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
	})})
	result := client.Call(context.Background(), testScope(), Credentials{AccessKey: "AKTEST", SecretKey: "test-secret"}, "GetAsset", body, now)
	if calls != 1 || result.Outcome != "unknown" {
		t.Fatal("redirect/retry followed")
	}
}

func TestCreateIdempotencyRotationAndConcurrentWorkers(t *testing.T) {
	s, db, _, provider := setup(t)
	ctx := context.Background()
	c := createContext("group")
	input := GroupCreate{Kind: KindVirtual, Name: "Group"}
	group, replayed, err := s.CreateGroup(ctx, c, input)
	if err != nil || replayed {
		t.Fatal(err)
	}
	s.options.ActiveHMACKey = "h2"
	c.KeyID = 9
	same, replayed, err := s.CreateGroup(ctx, c, input)
	if err != nil || !replayed || same.ID != group.ID {
		t.Fatal("rotated key missed idempotency", err)
	}
	input.Name = "Different"
	if _, _, err := s.CreateGroup(ctx, c, input); !errors.Is(err, ErrConflict) {
		t.Fatal("different request reused key", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- s.ProcessOnce(ctx) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls["CreateAssetGroup"] != 1 {
		t.Fatalf("duplicate create %d", provider.calls["CreateAssetGroup"])
	}
	var count int64
	db.Model(&Group{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate group")
	}
	if _, err := s.GetGroup(ctx, 2, group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-org read")
	}
}

func TestUnknownCreateNeverResubmitsAndMetadataIsSafe(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	provider.unknown = true
	group, _, err := s.CreateGroup(ctx, createContext("lost"), GroupCreate{Kind: KindVirtual, Name: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Minute)
	for range 2 {
		if err := s.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if provider.calls["CreateAssetGroup"] != 1 {
		t.Fatal("unknown mutation resent")
	}
	var operation Operation
	db.Where("resource_id = ?", group.ID).Take(&operation)
	if operation.Status != "unknown" {
		t.Fatal(operation.Status)
	}
	dto, _, err := s.CreateGroup(ctx, createContext("lost"), GroupCreate{Kind: KindVirtual, Name: "Group"})
	if err != nil || dto.ID != group.ID {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(dto)
	for _, private := range []string{`"provider_id"`, `"scope_id"`, `"scope_version"`, `"account_id"`, `"project"`, `"payload_id"`, "AKTEST", "test-secret"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("internal routing disclosed", private)
		}
	}
	if dto.CreateOperation == nil || dto.CreateOperation.Status != "unknown" || dto.CreateOperation.Error == nil || dto.CreateOperation.Error.Code != "provider_unavailable" {
		t.Fatal("safe operation diagnostic missing", dto)
	}
}

func TestIntentRollbackAndStaleLeaseNoSubmit(t *testing.T) {
	s, db, now, provider := setup(t)
	if err := db.Exec(`CREATE TRIGGER reject_intent BEFORE INSERT ON video_asset_events BEGIN SELECT RAISE(FAIL,'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateGroup(context.Background(), createContext("fail"), GroupCreate{Kind: KindVirtual, Name: "Group"}); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	for _, model := range []any{&Group{}, &Operation{}, &invocation.VideoPayload{}, &invocation.VideoPayloadStaging{}} {
		var count int64
		db.Model(model).Count(&count)
		if count != 0 {
			t.Fatal("partial intent escaped rollback")
		}
	}
	db.Exec("DROP TRIGGER reject_intent")
	group, _, err := s.CreateGroup(context.Background(), createContext("stale"), GroupCreate{Kind: KindVirtual, Name: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&Operation{}).Where("resource_id = ?", group.ID).Updates(map[string]any{"status": "in_flight", "lease_owner": "crashed", "lease_expires_at": now.Add(-time.Second), "fence": 1})
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls["CreateAssetGroup"] != 0 {
		t.Fatal("expired sending intent resent")
	}
}

func TestAssetProcessingFailureScopeAndReferences(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	group := readyGroup(t, s, "group")
	input := AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/immutable.png"}
	asset, _, err := s.CreateAsset(ctx, createContext("asset"), input)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Status != "creating" {
		t.Fatal(asset.Status)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	asset, _ = s.GetAsset(ctx, 1, asset.ID)
	if asset.Status != "processing" || asset.Reference != "" {
		t.Fatal("created asset prematurely ready")
	}
	*now = now.Add(16 * time.Second)
	provider.project = "other-project"
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	asset, _ = s.GetAsset(ctx, 1, asset.ID)
	if asset.Status != "processing" {
		t.Fatal("foreign project receipt applied")
	}
	*now = now.Add(16 * time.Second)
	provider.project = "project1"
	provider.assetStatus = "Failed"
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	asset, _ = s.GetAsset(ctx, 1, asset.ID)
	if asset.Status != "failed" {
		t.Fatal("HTTP 200 failed treated as ready")
	}
	provider.assetStatus = "Active"
	ready := readyAsset(t, s, now, group.ID, "asset2")
	refs := []ReferenceInput{{AssetID: ready.ID, Role: "reference_image", ContentIndex: 0}}
	if _, err := s.ResolveBindingsTx(db, 2, testScope(), "task1", refs); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant reference", err)
	}
	wrong := testScope()
	wrong.Project = "other"
	if _, err := s.ResolveBindingsTx(db, 1, wrong, "task1", refs); !errors.Is(err, ErrConflict) {
		t.Fatal("scope drift reference", err)
	}
	bindings, err := s.ResolveBindingsTx(db, 1, testScope(), "task1", refs)
	if err != nil || len(bindings) != 1 || !strings.HasPrefix(bindings[0].ProviderAssetID, "upstream-") {
		t.Fatal(bindings, err)
	}
	bad := append(refs, ReferenceInput{AssetID: "asset-missing", Role: "reference_image", ContentIndex: 1})
	if _, err := s.ResolveBindingsTx(db, 1, testScope(), "task2", bad); err == nil {
		t.Fatal("missing asset accepted")
	}
	var count int64
	db.Model(&TaskReference{}).Where("task_id = ?", "task2").Count(&count)
	if count != 0 {
		t.Fatal("partial references escaped savepoint")
	}
	if err := s.ReleaseBindingsTx(db, 1, "task1"); err != nil {
		t.Fatal(err)
	}
	db.Model(&TaskReference{}).Where("task_id = ? AND released_at IS NOT NULL", "task1").Count(&count)
	if count != 1 {
		t.Fatal("historical refs lost")
	}
}

func TestDeleteBarrierAndUnsupportedAssetDelete(t *testing.T) {
	s, _, _, provider := setup(t)
	ctx := context.Background()
	group := readyGroup(t, s, "group")
	operation, done, err := s.DeleteGroup(ctx, createContext("delete"), group.ID, false)
	if err != nil || done {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAsset(ctx, createContext("blocked"), AssetCreate{GroupID: group.ID, URL: "https://media.example/x.png", MediaType: "image"}); !errors.Is(err, ErrConflict) {
		t.Fatal("delete barrier not enforced", err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	dto, err := s.GetOperation(ctx, 1, operation.ID)
	if err != nil || dto.Status != "confirmed" {
		t.Fatal(dto, err)
	}
	if _, err := s.GetGroup(ctx, 1, group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted group still visible")
	}
	if _, done, err := s.DeleteGroup(ctx, createContext("delete-again"), group.ID, false); err != nil || !done {
		t.Fatal(err)
	}
	if provider.calls["DeleteAssetGroup"] != 1 {
		t.Fatal("duplicate group deletion")
	}
}

func TestPortraitAuthenticationIsNotLicenseAndSecretsExpire(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	c := createContext("portrait")
	portrait, _, err := s.CreatePortrait(ctx, c, PortraitCreate{CallbackURL: "https://customer.example/return"})
	if err != nil || portrait.Status != "creating" || portrait.ExpiresAt != nil {
		t.Fatal(portrait, err)
	}
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	portrait, err = s.GetPortrait(ctx, 1, portrait.ID, c.KeyID, true)
	if err != nil || portrait.Status != "pending" || !strings.Contains(portrait.AuthorizationURL, "h5-fixture-secret") {
		t.Fatal(portrait, err)
	}
	if _, err := s.GetPortrait(ctx, 1, portrait.ID, 999, true); !errors.Is(err, ErrSecret) {
		t.Fatal("unowned key received H5")
	}
	list, _, err := s.ListPortraits(ctx, 1, 20, "")
	if err != nil || list[0].AuthorizationURL != "" {
		t.Fatal("list disclosed H5")
	}
	var secret Secret
	db.Take(&secret)
	if strings.Contains(secret.ValueCipher, "byted-fixture-secret") || strings.Contains(secret.ValueCipher, "h5-fixture-secret") {
		t.Fatal("plaintext secret persisted")
	}
	var operation Operation
	db.Where("resource_id = ?", portrait.ID).Take(&operation)
	body, err := s.evidence.ReadForRecovery(ctx, 1, operation.ResponsePayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "test", ReasonCode: "test", OperationID: operation.ID})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("fixture-secret")) {
		t.Fatal("portrait secret copied into history")
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	portrait, err = s.GetPortrait(ctx, 1, portrait.ID, c.KeyID, true)
	if err != nil || portrait.Status != "succeeded" || portrait.GroupID == nil || portrait.AuthorizationURL != "" {
		t.Fatal(portrait, err)
	}
	group, _ := s.GetGroup(ctx, 1, *portrait.GroupID)
	if group.Kind != "real_person" || group.AuthorizationStatus != "needs_review" {
		t.Fatal("H5 granted license", group)
	}
	input := AssetCreate{GroupID: group.ID, MediaType: "image", URL: "https://media.example/person.png"}
	if _, _, err := s.CreateAsset(ctx, createContext("unlicensed"), input); !errors.Is(err, ErrAuthorization) {
		t.Fatal("H5 alone allowed import", err)
	}
	license := LicenseEvidence{ActorID: "reviewer", EvidenceRef: "evidence1", Source: "reviewed_contract", Purpose: "video_generation", ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(2 * time.Hour), VerifiedAt: *now, ReverifyDueAt: now.Add(time.Hour)}
	if err := s.VerifyLicenseTx(db, 1, portrait.ID, license); err != nil {
		t.Fatal(err)
	}
	group, _ = s.GetGroup(ctx, 1, group.ID)
	if group.AuthorizationStatus != "authorized" {
		t.Fatal(group)
	}
	asset := readyAsset(t, s, now, group.ID, "real-asset")
	queued, _, err := s.CreateAsset(ctx, createContext("queued-before-revoke"), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveBindingsTx(db, 1, testScope(), "task-real", []ReferenceInput{{AssetID: asset.ID, Role: "reference_image", ContentIndex: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLicenseTx(db, 1, portrait.ID, "revocation1", "reviewer"); err != nil {
		t.Fatal(err)
	}
	callsBefore := provider.calls["CreateAsset"]
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rejected, _ := s.GetAsset(ctx, 1, queued.ID)
	if rejected.Status != "failed" || provider.calls["CreateAsset"] != callsBefore {
		t.Fatal("revoked license dispatched queued import")
	}
	if _, err := s.ResolveBindingsTx(db, 1, testScope(), "task-new", []ReferenceInput{{AssetID: asset.ID, Role: "reference_image", ContentIndex: 0}}); !errors.Is(err, ErrAuthorization) {
		t.Fatal("revoked license usable")
	}
	*now = now.Add(time.Hour)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	portrait, _ = s.GetPortrait(ctx, 1, portrait.ID, c.KeyID, true)
	if portrait.Status != "succeeded" {
		t.Fatal("H5 TTL rewrote completed identity")
	}
	db.Where("secret_id = ?", secret.ID).Take(&secret)
	if secret.ValueCipher != "" || secret.DestroyedAt == nil {
		t.Fatal("secret retained after success")
	}
	if provider.calls["CreateVisualValidateSession"] != 1 {
		t.Fatal("duplicate portrait create")
	}
}

func TestLicenseAuditFailureRollsBackAndFreshnessRequired(t *testing.T) {
	s, db, now, _ := setup(t)
	ctx := context.Background()
	portrait, _, err := s.CreatePortrait(ctx, createContext("portrait"), PortraitCreate{CallbackURL: "https://customer.example/return"})
	if err != nil {
		t.Fatal(err)
	}
	s.ProcessOnce(ctx)
	*now = now.Add(16 * time.Second)
	s.ProcessOnce(ctx)
	evidence := LicenseEvidence{ActorID: "reviewer", EvidenceRef: "evidence1", Source: "reviewed_contract", Purpose: "video_generation", ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), VerifiedAt: *now, ReverifyDueAt: now.Add(30 * time.Minute)}
	db.Exec(`CREATE TRIGGER deny_license_audit BEFORE INSERT ON video_asset_events WHEN NEW.kind='license_verified' BEGIN SELECT RAISE(FAIL,'injected'); END`)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := s.VerifyLicenseTx(tx, 1, portrait.ID, evidence); !errors.Is(err, ErrStorage) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var p Portrait
	db.Where("session_id = ?", portrait.ID).Take(&p)
	if p.LicenseStatus != "unknown" {
		t.Fatal("license escaped failed audit")
	}
	db.Exec("DROP TRIGGER deny_license_audit")
	if err := s.VerifyLicenseTx(db, 1, portrait.ID, evidence); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(31 * time.Minute)
	db.Where("session_id = ?", portrait.ID).Take(&p)
	if s.licenseStatus(p, "video_generation") != "needs_review" {
		t.Fatal("stale license accepted")
	}
}

func TestHTTPContractRejectsForgeryAndUnknownFields(t *testing.T) {
	s, _, _, _ := setup(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/v1/video")
	group.Use(func(c *gin.Context) {
		uid := uint64(2)
		c.Set("video_key", &identity.APIKey{ID: 3, OrgID: 1, UserID: &uid, Scopes: "video:read video:write"})
		c.Set("video_request_id", "req-http")
		c.Next()
	})
	s.RegisterRoutes(group)
	for _, body := range []string{`{"kind":"virtual","name":"one","name":"two"}`, `{"kind":"virtual","Name":"one","name":"two"}`, `{"kind":"virtual","name":"one","provider_id":"forged"}`, `{"kind":"real_person","name":"one"}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/video/asset-groups", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "http-create")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("unsafe body %d %s", rec.Code, rec.Body.String())
		}
	}
	portrait, _, err := s.CreatePortrait(context.Background(), createContext("portrait"), PortraitCreate{CallbackURL: "https://customer.example/return"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/video/portrait-authorizations/"+portrait.ID+"?GroupId=forged&resultCode=0", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"creating"`) || strings.Contains(rec.Body.String(), "forged") {
		t.Fatal("client callback forged success", rec.Body.String())
	}
	if _, _, err := s.CreatePortrait(context.Background(), createContext("evil"), PortraitCreate{CallbackURL: "https://evil.example/return"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unregistered callback accepted")
	}
}
