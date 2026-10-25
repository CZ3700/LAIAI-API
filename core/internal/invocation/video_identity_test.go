package invocation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestVideoExecutionIdentityMetadataSurvivesLifecycleWithoutDecrypt(t *testing.T) {
	s, db, now := videoSetup(t)
	p := videoStage(t, s, db, videoInput(s))
	read := func() VideoExecutionIdentity {
		t.Helper()
		var identity VideoExecutionIdentity
		if err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			identity, err = ReadVideoExecutionIdentityTx(tx, 1, p.PayloadID(), 1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return identity
	}
	before := read()
	if before.PayloadID != p.PayloadID() || before.CiphertextSHA256 == "" || before.PlaintextBytes <= 0 {
		t.Fatal("immutable capsule identity missing")
	}
	if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Updates(map[string]any{"capture_status": VideoCaptureExpired, "wrapped_key": "invalid", "nonce": []byte("invalid"), "object_ref": "removed"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	s.keyring = nil // Identity validation must not rely on decrypt-capable services.
	var reads []string
	if err := db.Callback().Query().Before("gorm:query").Register("identity-read-only", func(tx *gorm.DB) {
		reads = append(reads, tx.Statement.Table)
		if tx.Statement.Table != "invocation_payloads" {
			tx.AddError(errors.New("identity read crossed metadata boundary"))
			return
		}
		for _, field := range tx.Statement.Selects {
			if field == "*" || field == "wrapped_key" || field == "nonce" || field == "ciphertext" {
				tx.AddError(errors.New("identity selected encrypted bytes"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("identity-read-only") })
	after := read()
	if before != after || len(reads) != 1 {
		t.Fatal("expiry or secret material changed immutable metadata identity")
	}
	_ = db.Callback().Query().Remove("identity-read-only")
	var accesses int64
	if err := db.Model(&VideoPayloadAccess{}).Count(&accesses).Error; err != nil {
		t.Fatal(err)
	}
	if accesses != 0 {
		t.Fatal("metadata lookup attempted plaintext access")
	}
}

func TestVideoExecutionIdentityRejectsTenantPurposeSchemaAndMissingTransaction(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, videoInput(s))
	if _, err := ReadVideoExecutionIdentityTx(db, 1, p.PayloadID(), 1); !errors.Is(err, ErrVideoTransactionRequired) {
		t.Fatalf("nontransaction: %v", err)
	}
	for _, tc := range []struct {
		name   string
		org    uint64
		schema uint32
		want   error
	}{
		{"tenant", 2, 1, ErrVideoNotFound}, {"empty_schema", 1, 0, ErrVideoSchemaUnsupported}, {"unknown_schema", 1, 2, ErrVideoSchemaUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				_, err := ReadVideoExecutionIdentityTx(tx, tc.org, p.PayloadID(), tc.schema)
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Update("purpose", VideoPurposeHistory).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error { _, err := ReadVideoExecutionIdentityTx(tx, 1, p.PayloadID(), 1); return err })
	if !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatalf("history accepted as execution: %v", err)
	}
}

func TestVideoReceiptReferenceDisabledAndExpiredRemainOwned(t *testing.T) {
	for _, capture := range []bool{false, true} {
		name := "disabled"
		if capture {
			name = "staged"
		}
		t.Run(name, func(t *testing.T) {
			s, db, _ := videoSetup(t)
			input := videoInput(s)
			input.RequestID, input.Direction, input.Purpose, input.CaptureEnabled = "receipt1", VideoUpstreamResponse, VideoPurposeHistory, capture
			p := videoStage(t, s, db, input)
			expected := VideoPayloadReference{OrgID: 1, PayloadID: p.PayloadID(), RequestID: "receipt1", AttemptID: "att1", Direction: VideoUpstreamResponse, Purpose: VideoPurposeHistory, SchemaVersion: 1}
			validate := func(ref VideoPayloadReference) error {
				return db.Transaction(func(tx *gorm.DB) error { return ValidateVideoPayloadReferenceTx(tx, ref) })
			}
			if err := validate(expected); err != nil {
				t.Fatal(err)
			}
			if err := ValidateVideoPayloadReferenceTx(db, expected); !errors.Is(err, ErrVideoTransactionRequired) {
				t.Fatalf("nontransaction: %v", err)
			}
			for _, field := range []string{"org", "request", "attempt", "purpose", "schema"} {
				bad := expected
				switch field {
				case "org":
					bad.OrgID = 2
				case "request":
					bad.RequestID = "other_receipt"
				case "attempt":
					bad.AttemptID = "other_attempt"
				case "purpose":
					bad.Purpose = VideoPurposeExecution
				case "schema":
					bad.SchemaVersion = 2
				}
				if err := validate(bad); err == nil {
					t.Fatalf("forged %s accepted", field)
				}
			}
			if capture {
				if err := db.Where("payload_id = ?", p.PayloadID()).Delete(&VideoPayloadStaging{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := validate(expected); !errors.Is(err, ErrVideoCorrupt) {
					t.Fatalf("missing staging accepted: %v", err)
				}
			}
			if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Update("capture_status", VideoCaptureExpired).Error; err != nil {
				t.Fatal(err)
			}
			if err := validate(expected); err != nil {
				t.Fatalf("expired ownership lost: %v", err)
			}
			if err := db.Model(&VideoPayload{}).Where("payload_id = ?", p.PayloadID()).Update("correlation_key", strings.Repeat("0", 64)).Error; err != nil {
				t.Fatal(err)
			}
			if err := validate(expected); !errors.Is(err, ErrVideoCorrupt) {
				t.Fatalf("correlation substitution accepted: %v", err)
			}
			var accesses int64
			if err := db.Model(&VideoPayloadAccess{}).Count(&accesses).Error; err != nil {
				t.Fatal(err)
			}
			if accesses != 0 {
				t.Fatal("ownership verification decrypted historical content")
			}
		})
	}
}
