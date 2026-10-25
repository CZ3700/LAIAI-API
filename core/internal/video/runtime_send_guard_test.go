package video

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

// The execution-format lookup is part of preparation. If it follows the
// permission COUNT, a claim can expire or change while that lookup runs and
// the earlier allowed result can still dispatch. Invalidate the claim after
// the final metadata read so only a subsequent permission check can stop POST.
func TestMySQLRuntimeFinalMetadataMustPrecedeSendPermission(t *testing.T) {
	for _, invalidation := range []string{"lease_expired", "status_unknown", "attempt_replaced", "version_changed"} {
		t.Run(invalidation, func(t *testing.T) {
			f := mysqlFixture(t)
			v := f.create(t, "final-metadata-send-guard")
			op := runtimeWorkerCreateOperation(t, f, v.ID)
			financialState := func() string {
				hold := f.hold(t, v.ID)
				var account ledger.Account
				var journal []ledger.JournalEntry
				var entries []videobilling.BillingEntry
				must(t, f.s.DB.Where("org_id = ? AND id = ?", f.key.OrgID, hold.AccountID).First(&account).Error)
				must(t, f.s.DB.Where("account_id = ?", hold.AccountID).Order("id").Find(&journal).Error)
				must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Order("id").Find(&entries).Error)
				data, err := json.Marshal([]any{hold, account, journal, entries})
				must(t, err)
				return string(data)
			}
			before := financialState()
			prepared, invalidated := false, false
			name := fmt.Sprintf("test:runtime-final-metadata-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "video_credential_snapshots" {
					prepared = true
					return
				}
				// The transaction helper selects only these five metadata
				// fields. Exclude the earlier typed payload read and claim.
				if !prepared || invalidated || tx.Statement.Table != "invocation_payloads" || len(tx.Statement.Selects) != 5 || tx.Error != nil {
					return
				}
				var current Operation
				clean := tx.Session(&gorm.Session{NewDB: true})
				if err := clean.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&current).Error; err != nil {
					tx.AddError(err)
					return
				}
				if current.Status != "in_flight" || current.AttemptID == "" || current.LeaseUntil == nil {
					tx.AddError(fmt.Errorf("fault injection did not reach a claimed operation"))
					return
				}
				invalidated = true
				changes := map[string]any{}
				switch invalidation {
				case "lease_expired":
					// Equality is expired; no wall-clock sleep or concurrent
					// mutation of the fixture clock is required.
					f.now = current.LeaseUntil.UTC()
				case "status_unknown":
					changes["status"] = "unknown"
				case "attempt_replaced":
					changes["attempt_id"] = "att_replaced_" + v.ID
				case "version_changed":
					changes["version"] = current.Version + 1
				}
				if len(changes) > 0 {
					// Use the same transaction because the helper already
					// locks the operation. This deterministic injection tests
					// the guard ordering without a competing lock timeout.
					if err := clean.Model(&Operation{}).Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).Updates(changes).Error; err != nil {
						tx.AddError(err)
					}
				}
			}))
			t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
			err := f.s.processOperation(context.Background(), op)
			if !prepared || !invalidated {
				t.Fatal("final execution metadata fault injection did not run", err)
			}
			if f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("permission checked before slow metadata allowed an invalidated claim to send")
			}
			must(t, err)
			after := runtimeWorkerCreateOperation(t, f, v.ID)
			if after.ReceiptRef != "" || after.ReceiptAppliedAt != nil || after.Status == "confirmed" || after.Status == "rejected" || financialState() != before {
				t.Fatal("invalidated final permission fabricated a receipt, terminal outcome or financial change")
			}
		})
	}
}
