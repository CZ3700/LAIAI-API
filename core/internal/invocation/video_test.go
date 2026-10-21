package invocation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const videoTestSchema = `
CREATE TABLE invocation_payloads (
 payload_id TEXT PRIMARY KEY,org_id INTEGER NOT NULL,request_id TEXT NOT NULL,attempt_id TEXT NOT NULL,
 correlation_key TEXT UNIQUE NOT NULL,direction TEXT NOT NULL,purpose TEXT NOT NULL,schema_version INTEGER NOT NULL,
 body_format TEXT NOT NULL,capture_representation TEXT NOT NULL DEFAULT 'legacy_unknown',encryption_key_id TEXT NOT NULL,wrapped_key TEXT NOT NULL,nonce BLOB NOT NULL,
 ciphertext_sha256 TEXT NOT NULL,plaintext_bytes INTEGER NOT NULL,redaction_version TEXT NOT NULL,
 capture_status TEXT NOT NULL,object_ref TEXT NOT NULL,created_at DATETIME NOT NULL,expires_at DATETIME NOT NULL,
 cleanup_after DATETIME,object_deleted_at DATETIME);
CREATE TABLE invocation_payload_staging (
 payload_id TEXT PRIMARY KEY,ciphertext BLOB NOT NULL,attempts INTEGER NOT NULL,next_attempt_at DATETIME NOT NULL,
 lease_owner TEXT NOT NULL,lease_expires_at DATETIME,fencing_token INTEGER NOT NULL,created_at DATETIME NOT NULL);
CREATE TABLE invocation_payload_accesses (
 access_id TEXT NOT NULL,phase TEXT NOT NULL,org_id INTEGER NOT NULL,payload_id TEXT NOT NULL,
 actor_role TEXT NOT NULL,actor_id TEXT NOT NULL,reason_code TEXT NOT NULL,operation_id TEXT NOT NULL,
 outcome TEXT NOT NULL,safe_error_code TEXT NOT NULL,created_at DATETIME NOT NULL,PRIMARY KEY(access_id,phase));
CREATE TABLE api_request_logs (
 request_id TEXT PRIMARY KEY,trace_id TEXT NOT NULL,org_id INTEGER,user_id INTEGER,api_key_id INTEGER,
 auth_outcome TEXT NOT NULL,method TEXT NOT NULL,route TEXT NOT NULL,operation TEXT NOT NULL,
 resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,request_bytes INTEGER NOT NULL,request_payload_id TEXT NOT NULL,
 started_at DATETIME NOT NULL,http_status INTEGER NOT NULL,response_bytes INTEGER NOT NULL,response_payload_id TEXT NOT NULL,
 safe_error_code TEXT NOT NULL,outcome TEXT NOT NULL,finished_at DATETIME);
`

func videoKeys(t *testing.T, active string) *crypto.Keyring {
	t.Helper()
	spec := "old:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + ",new:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	k, err := crypto.NewKeyring(spec, active)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func videoSetup(t *testing.T) (*VideoStore, *gorm.DB, *time.Time) {
	t.Helper()
	db := setup(t)
	if err := db.Exec(videoTestSchema).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC)
	s, err := NewVideoStore(db, videoKeys(t, "old"), VideoStoreOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return s, db, &now
}

func videoInput(s *VideoStore) VideoPayloadInput {
	return VideoPayloadInput{OrgID: 1, RequestID: "req1", AttemptID: "att1", Direction: VideoEffectiveRequest, Purpose: VideoPurposeExecution, SchemaVersion: 1, Body: []byte(`{"model":"seedance","prompt":"make a calm sea"}`), CaptureEnabled: true, ExpiresAt: s.now().Add(time.Hour)}
}
func videoAccess(role string) VideoAccess {
	return VideoAccess{ActorRole: role, ActorID: "worker1", ReasonCode: "task_recovery", OperationID: "op1"}
}
func videoStage(t *testing.T, s *VideoStore, db *gorm.DB, input VideoPayloadInput) *PreparedVideoPayload {
	t.Helper()
	p, err := s.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := s.StageTx(tx, p); return err }); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVideoStageRollbackAndIdempotency(t *testing.T) {
	s, db, _ := videoSetup(t)
	p, err := s.Prepare(videoInput(s))
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("rollback")
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.StageTx(tx, p); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	for _, model := range []any{&VideoPayload{}, &VideoPayloadStaging{}} {
		var count int64
		db.Model(model).Count(&count)
		if count != 0 {
			t.Fatal("partial transaction evidence")
		}
	}
	for range 2 {
		if err := db.Transaction(func(tx *gorm.DB) error { _, err := s.StageTx(tx, p); return err }); err != nil {
			t.Fatal(err)
		}
	}
	p2, _ := s.Prepare(videoInput(s))
	if _, err := s.StageTx(db, p2); !errors.Is(err, ErrVideoConflict) {
		t.Fatalf("fresh correlation: %v", err)
	}
	plain, err := s.ReadForExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
	if err != nil || !bytes.Equal(plain, videoInput(s).Body) {
		t.Fatalf("read: %s %v", plain, err)
	}
	var rows int64
	db.Model(&VideoPayloadAccess{}).Count(&rows)
	if rows != 2 {
		t.Fatalf("audits %d", rows)
	}
	var stored VideoPayloadStaging
	db.Take(&stored)
	if bytes.Contains(stored.Ciphertext, []byte("calm sea")) {
		t.Fatal("plaintext persisted")
	}
}

func TestVideoStageSavepointProtectsHandledError(t *testing.T) {
	s, db, _ := videoSetup(t)
	p, _ := s.Prepare(videoInput(s))
	if err := db.Exec(`CREATE TRIGGER reject_stage BEFORE INSERT ON invocation_payload_staging BEGIN SELECT RAISE(FAIL,'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.StageTx(tx, p); !errors.Is(err, ErrVideoStorage) {
			t.Fatalf("stage %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&VideoPayload{}).Count(&count)
	if count != 0 {
		t.Fatal("metadata escaped failed staging")
	}
}

func TestVideoAccessTenantPurposeRecovery(t *testing.T) {
	s, db, _ := videoSetup(t)
	execution := videoStage(t, s, db, videoInput(s))
	ctx := context.Background()
	for _, tc := range []struct {
		org  uint64
		role string
		want error
	}{{2, VideoRoleWorker, ErrVideoNotFound}, {1, VideoRoleRoot, ErrVideoAccessDenied}, {1, VideoRoleOrgReader, ErrVideoAccessDenied}} {
		plain, err := s.ReadForExecution(ctx, tc.org, execution.PayloadID(), videoAccess(tc.role))
		if !errors.Is(err, tc.want) || plain != nil {
			t.Fatalf("access: %s %v", plain, err)
		}
	}
	input := videoInput(s)
	input.Purpose = VideoPurposeHistory
	input.Direction = VideoUpstreamResponse
	input.Body = []byte(`{"id":"official-task","content":{"video_url":"https://media.example/out.mp4?signature=signed"}}`)
	receipt := videoStage(t, s, db, input)
	if _, err := s.ReadForRecovery(ctx, 1, receipt.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadForHistory(ctx, 1, receipt.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal(err)
	}
	if _, err := s.ReadForHistory(ctx, 1, receipt.PayloadID(), videoAccess(VideoRoleOrgReader)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadForRecovery(ctx, 1, execution.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal(err)
	}
	input.Direction = VideoClientRequest
	client := videoStage(t, s, db, input)
	if _, err := s.ReadForRecovery(ctx, 1, client.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal(err)
	}
}

func TestVideoAADTampering(t *testing.T) {
	mutations := map[string]func(*VideoPayload){
		"org": func(v *VideoPayload) { v.OrgID = 2 }, "id": func(v *VideoPayload) { v.PayloadID = "other" },
		"request": func(v *VideoPayload) { v.RequestID = "other" }, "attempt": func(v *VideoPayload) { v.AttemptID = "other" },
		"direction": func(v *VideoPayload) { v.Direction = VideoClientRequest }, "purpose": func(v *VideoPayload) { v.Purpose = VideoPurposeHistory },
		"schema": func(v *VideoPayload) { v.SchemaVersion++ }, "redaction": func(v *VideoPayload) { v.RedactionVersion = "other" },
		"format": func(v *VideoPayload) { v.BodyFormat = VideoFormatText }, "key_id": func(v *VideoPayload) { v.EncryptionKeyID = "new" },
		"ttl": func(v *VideoPayload) { v.ExpiresAt = v.ExpiresAt.Add(time.Hour) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, _, _ := videoSetup(t)
			p, _ := s.Prepare(videoInput(s))
			meta := p.metadata
			mutate(&meta)
			plain, err := s.decrypt(meta, p.staging.Ciphertext)
			if !errors.Is(err, ErrVideoCorrupt) || plain != nil {
				t.Fatalf("tamper %v", err)
			}
		})
	}
}

func TestVideoRandomDEKAndKeyRotation(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, videoInput(s))
	p2, _ := s.Prepare(videoInput(s))
	if p.metadata.WrappedKey == p2.metadata.WrappedKey || bytes.Equal(p.staging.Ciphertext, p2.staging.Ciphertext) {
		t.Fatal("DEK/ciphertext reused")
	}
	rotated, _ := NewVideoStore(db, videoKeys(t, "new"), VideoStoreOptions{Now: s.now})
	if _, err := rotated.ReadForExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal(err)
	}
	input := videoInput(s)
	input.RequestID = "req2"
	newPayload := videoStage(t, rotated, db, input)
	if newPayload.metadata.EncryptionKeyID != "new" {
		t.Fatal("old key used for new payload")
	}
	newOnly, _ := crypto.NewKeyring("new:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "new")
	removed, _ := NewVideoStore(db, newOnly, VideoStoreOptions{Now: s.now})
	if plain, err := removed.ReadForExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoCorrupt) || plain != nil {
		t.Fatal("removed key decrypts")
	}
}

func TestVideoAuditFailureNeverReturnsPlaintext(t *testing.T) {
	for _, phase := range []string{"started", "finished"} {
		t.Run(phase, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			p := videoStage(t, s, db, videoInput(s))
			if err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON invocation_payload_accesses WHEN NEW.phase = '" + phase + "' BEGIN SELECT RAISE(FAIL,'injected'); END").Error; err != nil {
				t.Fatal(err)
			}
			plain, err := s.ReadForExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
			if !errors.Is(err, ErrVideoAuditUnavailable) || plain != nil {
				t.Fatalf("audit failure released bytes: %s %v", plain, err)
			}
		})
	}
}

func TestVideoSecretRedactionAndNonJSON(t *testing.T) {
	s, db, _ := videoSetup(t)
	input := videoInput(s)
	input.Purpose = VideoPurposeHistory
	input.Direction = VideoUpstreamResponse
	input.Body = []byte(`{"authorization":"Bearer top-secret","nested":{"password":"pass-secret","BytedToken":"byted-secret","H5Link":"https://secret.example"},"message":"known-secret Bearer bearer-secret","url":"https://name:pwd@media.example/v?api_key=url-secret&signature=media-signature","completion_tokens":90}`)
	input.SecretValues = []string{"known-secret"}
	p := videoStage(t, s, db, input)
	plain, err := s.ReadForRecovery(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"top-secret", "pass-secret", "byted-secret", "secret.example", "known-secret", "bearer-secret", "url-secret", "name:pwd"} {
		if bytes.Contains(plain, []byte(secret)) {
			t.Fatalf("secret retained: %s", secret)
		}
	}
	if !bytes.Contains(plain, []byte("completion_tokens")) || !bytes.Contains(plain, []byte("media-signature")) {
		t.Fatal("usage/media URL removed")
	}
	input.RequestID = "req-text"
	input.Body = []byte("<html>503 password=text-secret Authorization: Bearer auth-secret token=token-secret known-secret</html>\xff")
	p = videoStage(t, s, db, input)
	plain, err = s.ReadForRecovery(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Format, Text string }
	if err := json.Unmarshal(plain, &envelope); err != nil || envelope.Format != VideoFormatText || !strings.Contains(envelope.Text, "503") {
		t.Fatalf("text envelope %s %v", plain, err)
	}
	for _, secret := range []string{"text-secret", "auth-secret", "token-secret", "known-secret"} {
		if bytes.Contains(plain, []byte(secret)) {
			t.Fatalf("text secret retained: %s", secret)
		}
	}
	exec := videoInput(s)
	exec.Body = []byte(`{"prompt":"do this","api_key":"illegal"}`)
	if _, err := s.Prepare(exec); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatalf("execution changed silently: %v", err)
	}
}

func TestVideoRetentionAndTTLPurge(t *testing.T) {
	s, db, now := videoSetup(t)
	input := videoInput(s)
	input.CaptureEnabled = false
	if _, err := s.Prepare(input); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatal(err)
	}
	input.Purpose = VideoPurposeHistory
	input.Direction = VideoClientRequest
	input.Body = []byte("not stored")
	disabled := videoStage(t, s, db, input)
	var count int64
	db.Model(&VideoPayloadStaging{}).Count(&count)
	if count != 0 {
		t.Fatal("disabled payload staged")
	}
	if _, err := s.ReadForHistory(context.Background(), 1, disabled.PayloadID(), videoAccess(VideoRoleOrgReader)); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatal(err)
	}
	input = videoInput(s)
	input.RequestID = "live"
	p := videoStage(t, s, db, input)
	*now = now.Add(time.Hour)
	if plain, err := s.ReadForExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoExpired) || plain != nil {
		t.Fatal("expired readable")
	}
	n, err := s.PurgeExpired(context.Background(), 100)
	if err != nil || n != 2 {
		t.Fatalf("purge %d %v", n, err)
	}
	var meta VideoPayload
	db.Where("payload_id = ?", p.PayloadID()).Take(&meta)
	if meta.WrappedKey != "" || len(meta.Nonce) != 0 || meta.CaptureStatus != VideoCaptureExpired {
		t.Fatal("key not erased")
	}
	db.Model(&VideoPayloadStaging{}).Count(&count)
	if count != 0 {
		t.Fatal("staging not erased")
	}
	if n, err := s.PurgeExpired(context.Background(), 100); err != nil || n != 0 {
		t.Fatalf("repeat purge %d %v", n, err)
	}
	input = videoInput(s)
	input.ExpiresAt = now.Add(31 * 24 * time.Hour)
	if _, err := s.Prepare(input); !errors.Is(err, ErrVideoInvalid) {
		t.Fatal("excess TTL accepted")
	}
}

type videoTestBlobs struct {
	mu         sync.Mutex
	objects    map[string][]byte
	puts       int
	failPut    bool
	failDelete bool
	afterPut   func()
}

func (b *videoTestBlobs) Put(ctx context.Context, key string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	b.puts++
	b.objects[key] = append([]byte(nil), raw...)
	fail := b.failPut
	b.failPut = false
	b.mu.Unlock()
	if b.afterPut != nil {
		b.afterPut()
	}
	if fail {
		return errors.New("ambiguous upload")
	}
	return nil
}
func (b *videoTestBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, ok := b.objects[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), raw...), ctx.Err()
}
func (b *videoTestBlobs) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failDelete {
		b.failDelete = false
		return errors.New("retry")
	}
	delete(b.objects, key)
	return ctx.Err()
}

func TestVideoBlobRestartSafeUploadAndPurge(t *testing.T) {
	s, db, now := videoSetup(t)
	blobs := &videoTestBlobs{objects: map[string][]byte{}, failPut: true}
	s.blobs = blobs
	p := videoStage(t, s, db, videoInput(s))
	ctx := context.Background()
	if _, err := s.UploadPending(ctx, "worker", 10); !errors.Is(err, ErrVideoStorage) {
		t.Fatal(err)
	}
	if _, err := s.ReadForExecution(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal("ambiguous put lost SQL staging", err)
	}
	*now = now.Add(2 * time.Minute)
	if n, err := s.UploadPending(ctx, "restarted-worker", 10); err != nil || n != 1 {
		t.Fatalf("retry %d %v", n, err)
	}
	if blobs.puts != 2 || len(blobs.objects) != 1 {
		t.Fatal("idempotent object key not used")
	}
	if _, err := s.ReadForExecution(ctx, 1, p.PayloadID(), videoAccess(VideoRoleWorker)); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&VideoPayloadStaging{}).Count(&count)
	if count != 0 {
		t.Fatal("stage not ACKed")
	}
	*now = now.Add(time.Hour)
	if n, err := s.PurgeExpired(ctx, 10); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if len(blobs.objects) != 1 {
		t.Fatal("ignored in-flight upload grace")
	}
	*now = now.Add(videoUploadLease + videoBlobTimeout)
	blobs.failDelete = true
	if _, err := s.PurgeExpired(ctx, 10); !errors.Is(err, ErrVideoStorage) {
		t.Fatal(err)
	}
	if _, err := s.PurgeExpired(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if len(blobs.objects) != 0 {
		t.Fatal("orphan ciphertext")
	}
}

func TestVideoUploadFenceAndExpiry(t *testing.T) {
	for _, scenario := range []string{"stolen_fence", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, now := videoSetup(t)
			blobs := &videoTestBlobs{objects: map[string][]byte{}}
			s.blobs = blobs
			p := videoStage(t, s, db, videoInput(s))
			ctx := context.Background()
			blobs.afterPut = func() {
				if scenario == "expired" {
					*now = now.Add(2 * time.Hour)
					if _, err := s.PurgeExpired(ctx, 10); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := db.Model(&VideoPayloadStaging{}).Where("payload_id = ?", p.PayloadID()).Update("fencing_token", 99).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			if n, err := s.UploadPending(ctx, "old-worker", 10); err != nil || n != 0 {
				t.Fatal("stale worker committed", n, err)
			}
			var meta VideoPayload
			db.Where("payload_id = ?", p.PayloadID()).Take(&meta)
			if meta.CaptureStatus == VideoCaptureStored {
				t.Fatal("stale worker ACK")
			}
			if scenario == "expired" {
				*now = now.Add(videoUploadLease + videoBlobTimeout)
				if _, err := s.PurgeExpired(ctx, 10); err != nil {
					t.Fatal(err)
				}
				if len(blobs.objects) != 0 {
					t.Fatal("post-expiry upload orphan")
				}
			}
		})
	}
}

func TestVideoRequestLifecycleAndRecordTx(t *testing.T) {
	s, db, now := videoSetup(t)
	org := uint64(1)
	start := VideoRequestStart{RequestID: "req-log", OrgID: &org, AuthOutcome: "authenticated", Method: "POST", Route: "/v1/videos", Operation: "video_create"}
	stop := errors.New("rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := s.RequestStartTx(tx, start); err != nil {
			return err
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	var count int64
	db.Model(&VideoRequestLog{}).Count(&count)
	if count != 0 {
		t.Fatal("request log escaped rollback")
	}
	for range 2 {
		if err := s.RequestStartTx(db, start); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Second)
	}
	finish := VideoRequestFinish{RequestID: start.RequestID, OrgID: &org, HTTPStatus: 202, ResponseBytes: 42, Outcome: "completed"}
	wrong := uint64(2)
	finish.OrgID = &wrong
	if err := s.RequestFinishTx(db, finish); !errors.Is(err, ErrVideoNotFound) {
		t.Fatal(err)
	}
	finish.OrgID = &org
	for range 2 {
		if err := s.RequestFinishTx(db, finish); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Second)
	}
	finish.HTTPStatus = 500
	if err := s.RequestFinishTx(db, finish); !errors.Is(err, ErrVideoConflict) {
		t.Fatal(err)
	}
	inv := &Invocation{OrgID: org, APIKeyID: 4, RequestID: start.RequestID, Model: "seedance", Modality: ModalityVideo, Status: StatusSuccess, StatusCode: 200, UsageSource: UsageSourceUpstream, CompletionTokens: 100, CostUSD: decimal.RequireFromString("0.01")}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := RecordTx(tx, inv); err != nil {
			return err
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	db.Model(&Invocation{}).Count(&count)
	if count != 0 {
		t.Fatal("summary escaped rollback")
	}
	for range 2 {
		if err := RecordTx(db, inv); err != nil {
			t.Fatal(err)
		}
	}
	inv.CompletionTokens = 101
	if err := RecordTx(db, inv); !errors.Is(err, ErrVideoConflict) {
		t.Fatal(err)
	}
	inv.ErrorBrief = "raw error with credential"
	if err := RecordTx(db, inv); !errors.Is(err, ErrVideoInvalid) {
		t.Fatal("free text error accepted", err)
	}
}
