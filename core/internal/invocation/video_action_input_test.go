package invocation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func actionExecutionInput(s *VideoStore) VideoPayloadInput {
	input := videoInput(s)
	input.SchemaVersion = VideoActionInputSchemaVersion
	input.Body = []byte(`{"action":"cancel_task","operation_id":"op1","schema_version":2,"task_id":"task1"}`)
	input.ExpiresAt = s.now().Add(24 * time.Hour)
	return input
}

func TestVideoActionInputReadsAuditedEncryptedSchema2(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(fmt.Sprintf("stored=%t", stored), func(t *testing.T) {
			s, db, _ := videoSetup(t)
			input := actionExecutionInput(s)
			p := videoStage(t, s, db, input)
			var stage VideoPayloadStaging
			if err := db.Where("payload_id = ?", p.PayloadID()).Take(&stage).Error; err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stage.Ciphertext, []byte("cancel_task")) {
				t.Fatal("action input persisted in plaintext")
			}
			if stored {
				s.blobs = &videoTestBlobs{objects: map[string][]byte{}}
				if count, err := s.UploadPending(context.Background(), "test-upload", 1); err != nil || count != 1 {
					t.Fatal("upload failed", count, err)
				}
			}
			plain, err := s.ReadForActionExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
			if err != nil || !bytes.Equal(plain, input.Body) {
				t.Fatal("fixed action input unavailable", err)
			}
			clear(plain)
			assertVideoSchemaAudit(t, db, "succeeded", "")
		})
	}
}

func TestVideoActionInputKeepsLegacySchemaBoundary(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, actionExecutionInput(s))
	for _, schema := range []uint32{0, 1, 2, 3} {
		plain, err := s.ReadForExecutionSchema(context.Background(), 1, p.PayloadID(), schema, videoAccess(VideoRoleWorker))
		if plain != nil || !errors.Is(err, ErrVideoSchemaUnsupported) {
			t.Fatal("old create reader learned action input", schema, err)
		}
		err = db.Transaction(func(tx *gorm.DB) error {
			if e := ValidateVideoExecutionSchemaTx(tx, 1, p.PayloadID(), schema); !errors.Is(e, ErrVideoSchemaUnsupported) {
				return errors.New("old create validator learned action input")
			}
			_, e := ReadVideoExecutionIdentityTx(tx, 1, p.PayloadID(), schema)
			return e
		})
		if !errors.Is(err, ErrVideoSchemaUnsupported) {
			t.Fatal("old create metadata boundary expanded", schema, err)
		}
	}
}

func TestVideoActionInputDeniesBeforeReadingCiphertext(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		want       error
	}{
		{"schema1", "SCHEMA_UNSUPPORTED", ErrVideoSchemaUnsupported},
		{"schema3_expired", "SCHEMA_UNSUPPORTED", ErrVideoSchemaUnsupported},
		{"schema0", "SCHEMA_UNSUPPORTED", ErrVideoSchemaUnsupported},
		{"org", "NOT_FOUND", ErrVideoNotFound},
		{"root", "ROLE_DENIED", ErrVideoAccessDenied},
		{"org_reader", "ROLE_DENIED", ErrVideoAccessDenied},
		{"history", "PURPOSE_DENIED", ErrVideoAccessDenied},
		{"direction", "PURPOSE_DENIED", ErrVideoAccessDenied},
		{"expired", "EXPIRED", ErrVideoExpired},
		{"disabled", "RETENTION_DISABLED", ErrVideoRetentionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			input := actionExecutionInput(s)
			if tc.name == "schema1" {
				input.SchemaVersion = 1
			}
			if tc.name == "schema3_expired" {
				input.SchemaVersion = 3
			}
			p := videoStage(t, s, db, input)
			updates := map[string]any{}
			org, access := uint64(1), videoAccess(VideoRoleWorker)
			switch tc.name {
			case "schema0":
				updates["schema_version"] = 0
			case "schema3_expired", "expired":
				updates["expires_at"] = s.now()
			case "org":
				org = 2
			case "root":
				access.ActorRole = VideoRoleRoot
			case "org_reader":
				access.ActorRole = VideoRoleOrgReader
			case "history":
				updates["purpose"] = VideoPurposeHistory
			case "direction":
				updates["direction"] = VideoClientRequest
			case "disabled":
				updates["capture_status"] = VideoCaptureDisabled
			}
			if len(updates) > 0 {
				if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			}
			stageReads := 0
			if err := db.Callback().Query().Before("gorm:query").Register("action-no-ciphertext", func(tx *gorm.DB) {
				if tx.Statement.Table == "invocation_payload_staging" {
					stageReads++
				}
			}); err != nil {
				t.Fatal(err)
			}
			plain, err := s.ReadForActionExecution(context.Background(), org, p.PayloadID(), access)
			if plain != nil || !errors.Is(err, tc.want) || stageReads != 0 {
				t.Fatal("action read bypassed access/schema/lifecycle", err, stageReads)
			}
			assertVideoSchemaAudit(t, db, "denied", tc.code)
		})
	}
}

func TestVideoActionInputRechecksSchemaAfterUploadRace(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, actionExecutionInput(s))
	blobs := &videoSchemaBlobs{videoTestBlobs: &videoTestBlobs{objects: map[string][]byte{}}}
	s.blobs = blobs
	injected := false
	if err := db.Callback().Query().Before("gorm:query").Register("action-upload-race", func(tx *gorm.DB) {
		if injected || tx.Statement.Table != "invocation_payload_staging" {
			return
		}
		injected = true
		if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(map[string]any{"schema_version": 3, "capture_status": VideoCaptureStored, "object_ref": "capsule"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error; err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	plain, err := s.ReadForActionExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
	if plain != nil || !errors.Is(err, ErrVideoSchemaUnsupported) || !injected || blobs.gets != 0 {
		t.Fatal("metadata reload bypassed action schema", err, injected, blobs.gets)
	}
	assertVideoSchemaAudit(t, db, "denied", "SCHEMA_UNSUPPORTED")
}

func TestVideoActionInputAuditFailureDoesNotReturnPlaintext(t *testing.T) {
	for _, phase := range []string{"started", "finished"} {
		t.Run(phase, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			p := videoStage(t, s, db, actionExecutionInput(s))
			if err := db.Exec("CREATE TRIGGER action_audit_fail BEFORE INSERT ON invocation_payload_accesses WHEN NEW.phase='" + phase + "' BEGIN SELECT RAISE(FAIL,'test-only'); END").Error; err != nil {
				t.Fatal(err)
			}
			plain, err := s.ReadForActionExecution(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker))
			if plain != nil || !errors.Is(err, ErrVideoAuditUnavailable) {
				t.Fatal("failed audit released action plaintext", err)
			}
		})
	}
}

func TestVideoActionInputIdentitySurvivesExpiryWithoutPlaintext(t *testing.T) {
	s, db, now := videoSetup(t)
	p := videoStage(t, s, db, actionExecutionInput(s))
	read := func() VideoExecutionIdentity {
		t.Helper()
		var identity VideoExecutionIdentity
		if err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			identity, err = ReadVideoActionInputIdentityTx(tx, 1, p.PayloadID())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return identity
	}
	before := read()
	if before.SchemaVersion != 2 || before.RequestID != "req1" || before.AttemptID != "att1" || before.CiphertextSHA256 == "" {
		t.Fatal("fixed identity incomplete")
	}
	if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(map[string]any{"capture_status": VideoCaptureExpired, "wrapped_key": "removed", "nonce": []byte{}, "object_ref": "removed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(25 * time.Hour)
	s.keyring = nil
	if err := db.Exec("PRAGMA query_only=ON").Error; err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := db.Callback().Query().Before("gorm:query").Register("action-metadata-only", func(tx *gorm.DB) {
		queries++
		if tx.Statement.Table != "invocation_payloads" {
			tx.AddError(errors.New("action identity crossed metadata boundary"))
		}
		for _, field := range tx.Statement.Selects {
			if field == "*" || field == "wrapped_key" || field == "nonce" || field == "ciphertext" {
				tx.AddError(errors.New("action identity selected encrypted bytes"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if after := read(); before != after || queries != 1 {
		t.Fatal("expired action input lost immutable identity or performed extra reads")
	}
}

func TestVideoActionInputIdentityRejectsWrongIdentity(t *testing.T) {
	for _, mode := range []string{"org", "schema1", "schema3", "purpose", "direction", "correlation", "bare_db"} {
		t.Run(mode, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			input := actionExecutionInput(s)
			if mode == "schema1" {
				input.SchemaVersion = 1
			}
			if mode == "schema3" {
				input.SchemaVersion = 3
			}
			p := videoStage(t, s, db, input)
			org, want := uint64(1), ErrVideoSchemaUnsupported
			updates := map[string]any{}
			switch mode {
			case "org":
				org, want = 2, ErrVideoNotFound
			case "purpose":
				updates["purpose"], want = VideoPurposeHistory, ErrVideoAccessDenied
			case "direction":
				updates["direction"], want = VideoUpstreamResponse, ErrVideoAccessDenied
			case "correlation":
				updates["correlation_key"], want = strings.Repeat("0", 64), ErrVideoCorrupt
			case "bare_db":
				want = ErrVideoTransactionRequired
			}
			if len(updates) != 0 {
				if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			}
			var got VideoExecutionIdentity
			var err error
			if mode == "bare_db" {
				got, err = ReadVideoActionInputIdentityTx(db, org, p.PayloadID())
			} else {
				err = db.Transaction(func(tx *gorm.DB) error {
					var e error
					got, e = ReadVideoActionInputIdentityTx(tx, org, p.PayloadID())
					return e
				})
			}
			if !errors.Is(err, want) || got != (VideoExecutionIdentity{}) {
				t.Fatal("action metadata identity accepted wrong boundary", err)
			}
		})
	}
}
