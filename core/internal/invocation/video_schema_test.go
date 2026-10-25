package invocation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

type videoSchemaBlobs struct {
	*videoTestBlobs
	gets int
}

func (b *videoSchemaBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	b.gets++
	return b.videoTestBlobs.Get(ctx, key)
}

func videoSchemaInput(s *VideoStore, purpose string, schema uint32) VideoPayloadInput {
	in := videoInput(s)
	in.SchemaVersion, in.Purpose = schema, purpose
	if purpose == VideoPurposeDelivery {
		in.Direction = VideoUpstreamResponse
		in.Body = []byte(`{"video_url":"https://cdn.example.com/a.mp4?token=private"}`)
	}
	return in
}

func videoReadWithSchema(s *VideoStore, purpose string, org uint64, id string, schema uint32, access VideoAccess) ([]byte, error) {
	if purpose == VideoPurposeExecution {
		return s.ReadForExecutionSchema(context.Background(), org, id, schema, access)
	}
	return s.ReadForDeliverySchema(context.Background(), org, id, schema, access)
}

func videoSchemaAccess(purpose string) VideoAccess {
	if purpose == VideoPurposeExecution {
		return videoAccess(VideoRoleWorker)
	}
	return videoAccess(VideoRoleOrgReader)
}

func assertVideoSchemaAudit(t *testing.T, db *gorm.DB, wantOutcome, wantCode string) {
	t.Helper()
	var rows []VideoPayloadAccess
	if err := db.Order("phase DESC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Phase != "started" || rows[0].Outcome != "started" || rows[1].Phase != "finished" || rows[1].Outcome != wantOutcome || rows[1].SafeErrorCode != wantCode || rows[0].AccessID != rows[1].AccessID {
		t.Fatalf("expected one complete audited read, got %+v", rows)
	}
}

func TestVideoTypedReadsRejectUnsupportedSchemaBeforeCiphertext(t *testing.T) {
	for _, purpose := range []string{VideoPurposeExecution, VideoPurposeDelivery} {
		for _, stored := range []bool{false, true} {
			for _, tc := range []struct{ expected, actual uint32 }{{0, 1}, {2, 1}, {1, 2}, {2, 2}, {1, 0}} {
				t.Run(fmt.Sprintf("%s/stored=%v/expected=%d/actual=%d", purpose, stored, tc.expected, tc.actual), func(t *testing.T) {
					s, db, _ := videoSetup(t)
					actual := tc.actual
					if actual == 0 {
						actual = 1 // Zero is invalid at capture; simulate a malformed legacy row.
					}
					p := videoStage(t, s, db, videoSchemaInput(s, purpose, actual))
					blobs := &videoSchemaBlobs{videoTestBlobs: &videoTestBlobs{objects: map[string][]byte{}}}
					s.blobs = blobs
					if stored {
						if n, err := s.UploadPending(context.Background(), "schema-test", 10); err != nil || n != 1 {
							t.Fatalf("upload n=%d error=%v", n, err)
						}
					}
					if tc.actual == 0 {
						if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Update("schema_version", 0).Error; err != nil {
							t.Fatal(err)
						}
					}
					stageReads := 0
					if err := db.Callback().Query().Before("gorm:query").Register("schema_probe", func(tx *gorm.DB) {
						if tx.Statement.Table == "invocation_payload_staging" {
							stageReads++
						}
					}); err != nil {
						t.Fatal(err)
					}
					plain, err := videoReadWithSchema(s, purpose, 1, p.PayloadID(), tc.expected, videoSchemaAccess(purpose))
					if plain != nil || !errors.Is(err, ErrVideoSchemaUnsupported) {
						t.Fatalf("unsupported format returned bytes or wrong error: %v", err)
					}
					if stageReads != 0 || blobs.gets != 0 {
						t.Fatalf("denied format read ciphertext: staging=%d blobs=%d", stageReads, blobs.gets)
					}
					assertVideoSchemaAudit(t, db, "denied", "SCHEMA_UNSUPPORTED")
				})
			}
		}
	}
}

func TestVideoTypedReadsKeepAuthorizationBeforeSchemaChecks(t *testing.T) {
	for _, purpose := range []string{VideoPurposeExecution, VideoPurposeDelivery} {
		for _, tc := range []struct {
			name, code string
			want       error
		}{
			{"wrong role", "ROLE_DENIED", ErrVideoAccessDenied},
			{"wrong organization", "NOT_FOUND", ErrVideoNotFound},
			{"wrong purpose", "PURPOSE_DENIED", ErrVideoAccessDenied},
		} {
			t.Run(purpose+"/"+tc.name, func(t *testing.T) {
				s, db, _ := videoSetup(t)
				in := videoSchemaInput(s, purpose, 2)
				if tc.name == "wrong purpose" {
					in.Purpose, in.Direction = VideoPurposeHistory, VideoUpstreamResponse
				}
				p := videoStage(t, s, db, in)
				access := videoSchemaAccess(purpose)
				if tc.name == "wrong role" {
					if purpose == VideoPurposeExecution {
						access.ActorRole = VideoRoleOrgReader
					} else {
						access.ActorRole = VideoRoleWorker
					}
				}
				org := uint64(1)
				if tc.name == "wrong organization" {
					org = 2
				}
				stageReads := 0
				if err := db.Callback().Query().Before("gorm:query").Register("schema_probe", func(tx *gorm.DB) {
					if tx.Statement.Table == "invocation_payload_staging" {
						stageReads++
					}
				}); err != nil {
					t.Fatal(err)
				}
				plain, err := videoReadWithSchema(s, purpose, org, p.PayloadID(), 1, access)
				if plain != nil || !errors.Is(err, tc.want) || stageReads != 0 {
					t.Fatalf("unauthorized format read: err=%v staging=%d", err, stageReads)
				}
				assertVideoSchemaAudit(t, db, "denied", tc.code)
			})
		}
	}
}

func TestVideoTypedReadsRecheckSchemaAfterUploadRace(t *testing.T) {
	for _, purpose := range []string{VideoPurposeExecution, VideoPurposeDelivery} {
		for _, mode := range []string{"stored", "expired", "disabled", "wrong_purpose", "wrong_direction"} {
			t.Run(purpose+"/"+mode, func(t *testing.T) {
				s, db, _ := videoSetup(t)
				p := videoStage(t, s, db, videoSchemaInput(s, purpose, 1))
				blobs := &videoSchemaBlobs{videoTestBlobs: &videoTestBlobs{objects: map[string][]byte{}}}
				s.blobs = blobs
				want, code := ErrVideoSchemaUnsupported, "SCHEMA_UNSUPPORTED"
				updates := map[string]any{"capture_status": VideoCaptureStored, "object_ref": "capsule", "schema_version": 2}
				switch mode {
				case "expired":
					updates["capture_status"], updates["expires_at"] = VideoCaptureExpired, s.now()
				case "disabled":
					updates["capture_status"] = VideoCaptureDisabled
				case "wrong_purpose":
					updates["purpose"] = VideoPurposeHistory
					want, code = ErrVideoAccessDenied, "PURPOSE_DENIED"
				case "wrong_direction":
					updates["direction"] = VideoClientRequest
					want, code = ErrVideoAccessDenied, "PURPOSE_DENIED"
				}
				raceInjected := false
				if err := db.Callback().Query().Before("gorm:query").Register("simulate_upload", func(tx *gorm.DB) {
					if raceInjected || tx.Statement.Table != "invocation_payload_staging" {
						return
					}
					raceInjected = true
					if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(updates).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error; err != nil {
						t.Fatal(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
				plain, err := videoReadWithSchema(s, purpose, 1, p.PayloadID(), 1, videoSchemaAccess(purpose))
				if plain != nil || !errors.Is(err, want) || !raceInjected || blobs.gets != 0 {
					t.Fatalf("metadata reload bypassed schema: err=%v injected=%v blobReads=%d", err, raceInjected, blobs.gets)
				}
				assertVideoSchemaAudit(t, db, "denied", code)
			})
		}
	}
}

func TestVideoTypedReadsAllowV1AndKeepLegacyAPIs(t *testing.T) {
	for _, purpose := range []string{VideoPurposeExecution, VideoPurposeDelivery} {
		t.Run(purpose, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			p := videoStage(t, s, db, videoSchemaInput(s, purpose, 1))
			plain, err := videoReadWithSchema(s, purpose, 1, p.PayloadID(), 1, videoSchemaAccess(purpose))
			if err != nil || len(plain) == 0 {
				t.Fatalf("supported format unavailable: %v", err)
			}
			assertVideoSchemaAudit(t, db, "succeeded", "")
			if purpose == VideoPurposeExecution {
				_, err = s.ReadForExecution(context.Background(), 1, p.PayloadID(), videoSchemaAccess(purpose))
			} else {
				_, err = s.ReadForDelivery(context.Background(), 1, p.PayloadID(), videoSchemaAccess(purpose))
			}
			if err != nil {
				t.Fatalf("legacy v1 read changed: %v", err)
			}
		})
	}
}

func TestVideoTypedReadsRejectUnknownSchemaBeforeRetentionSemantics(t *testing.T) {
	for _, purpose := range []string{VideoPurposeExecution, VideoPurposeDelivery} {
		for _, mode := range []string{"expired_time", "expired_status", "disabled"} {
			t.Run(purpose+"/"+mode, func(t *testing.T) {
				s, db, _ := videoSetup(t)
				p := videoStage(t, s, db, videoSchemaInput(s, purpose, 2))
				updates := map[string]any{}
				switch mode {
				case "expired_time":
					updates["expires_at"] = s.now()
				case "expired_status":
					updates["capture_status"] = VideoCaptureExpired
				case "disabled":
					updates["capture_status"] = VideoCaptureDisabled
				}
				if err := db.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ?", 1, p.PayloadID()).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				plain, err := videoReadWithSchema(s, purpose, 1, p.PayloadID(), 1, videoSchemaAccess(purpose))
				if plain != nil || !errors.Is(err, ErrVideoSchemaUnsupported) {
					t.Fatal("unknown format was misclassified as a known expiry/retention outcome", err)
				}
				assertVideoSchemaAudit(t, db, "denied", "SCHEMA_UNSUPPORTED")
			})
		}
	}
}

func TestValidateVideoExecutionSchemaTxIsReadOnlyScopedAndIgnoresKnownExpiry(t *testing.T) {
	for _, tc := range []struct {
		name             string
		org              uint64
		expected, actual uint32
		mode             string
		want             error
	}{
		{"supported", 1, 1, 1, "", nil},
		{"supported_expired", 1, 1, 1, "expired", nil},
		{"supported_disabled", 1, 1, 1, "disabled", nil},
		{"unknown_expired", 1, 1, 2, "expired", ErrVideoSchemaUnsupported},
		{"unknown_disabled", 1, 1, 2, "disabled", ErrVideoSchemaUnsupported},
		{"unknown_stored", 1, 1, 2, "", ErrVideoSchemaUnsupported},
		{"zero_expected", 1, 0, 1, "", ErrVideoSchemaUnsupported},
		{"unknown_expected", 1, 2, 1, "", ErrVideoSchemaUnsupported},
		{"wrong_organization", 2, 1, 2, "", ErrVideoNotFound},
		{"wrong_purpose", 1, 1, 2, "history", ErrVideoAccessDenied},
		{"missing_metadata", 1, 1, 1, "missing", ErrVideoNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			input := videoSchemaInput(s, VideoPurposeExecution, tc.actual)
			if tc.mode == "history" {
				input.Purpose, input.Direction = VideoPurposeHistory, VideoUpstreamResponse
			}
			p := videoStage(t, s, db, input)
			ref := p.PayloadID()
			switch tc.mode {
			case "expired", "disabled":
				updates := map[string]any{"capture_status": VideoCaptureDisabled}
				if tc.mode == "expired" {
					updates = map[string]any{"expires_at": s.now(), "capture_status": VideoCaptureExpired}
				}
				if err := db.Model(&VideoPayload{}).Where("org_id = ? AND payload_id = ?", 1, ref).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
			case "missing":
				ref = "missing_execution_payload"
			}
			if err := ValidateVideoExecutionSchemaTx(db, tc.org, ref, tc.expected); !errors.Is(err, ErrVideoTransactionRequired) {
				t.Fatal("schema validation accepted a nontransactional DB", err)
			}
			if err := db.Exec("PRAGMA query_only=ON").Error; err != nil {
				t.Fatal(err)
			}
			queries := 0
			if err := db.Callback().Query().Before("gorm:query").Register("metadata_only", func(tx *gorm.DB) {
				queries++
				if tx.Statement.Table != "invocation_payloads" {
					t.Error("schema check read content or another domain")
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				return ValidateVideoExecutionSchemaTx(tx, tc.org, ref, tc.expected)
			})
			if !errors.Is(err, tc.want) || queries != 1 {
				t.Fatalf("metadata check error=%v want=%v queries=%d", err, tc.want, queries)
			}
		})
	}
}
