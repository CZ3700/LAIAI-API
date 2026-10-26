package video

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
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
			prepared := false
			injections, permissionReads := 0, 0
			countAfterInjection := false
			name := fmt.Sprintf("test:runtime-final-metadata-%d", f.key.OrgID)
			must(t, f.s.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				sql := strings.ReplaceAll(strings.ToLower(tx.Statement.SQL.String()), "`", "")
				if strings.Contains(sql, "count(*)") && strings.Contains(sql, "from video_operations as o") && strings.Contains(sql, "join video_task_states v") {
					permissionReads++
					count, ok := tx.Statement.Dest.(*int64)
					countAfterInjection = injections == 1 && ok && *count == 0 && tx.Error == nil
					return
				}
				if tx.Statement.Table == "video_credential_snapshots" {
					prepared = tx.Error == nil && tx.RowsAffected == 1
					return
				}
				// After clientFor has loaded the credential, the actual final
				// window performs a current FOR SHARE metadata read. Earlier
				// plaintext/current reads precede that credential, and the
				// metadata-only identity lookups have no FOR SHARE clause.
				metadata, ok := tx.Statement.Dest.(*invocation.VideoPayload)
				if !prepared || injections != 0 || tx.Statement.Table != "invocation_payloads" || !strings.Contains(sql, "for share") || tx.Error != nil || tx.RowsAffected != 1 || !ok || metadata.OrgID != op.OrgID || metadata.PayloadID != op.PayloadRef {
					return
				}
				// Observe the committed claim and completed typed-read audit
				// on a separate connection before mutating this locked copy.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				outside := f.s.DB.Session(&gorm.Session{NewDB: true, Context: ctx})
				var committed Operation
				if err := outside.Where("org_id = ? AND id = ?", op.OrgID, op.ID).Take(&committed).Error; err != nil {
					tx.AddError(err)
					return
				}
				var audits int64
				if err := outside.Model(&invocation.VideoPayloadAccess{}).Where("org_id = ? AND payload_id = ? AND operation_id = ? AND phase = ? AND outcome = ?", op.OrgID, op.PayloadRef, op.ID, "finished", "succeeded").Count(&audits).Error; err != nil {
					tx.AddError(err)
					return
				}
				var current Operation
				clean := tx.Session(&gorm.Session{NewDB: true})
				if err := clean.Where("org_id = ? AND id = ?", f.key.OrgID, op.ID).First(&current).Error; err != nil {
					tx.AddError(err)
					return
				}
				if current.Status != "in_flight" || current.AttemptID == "" || current.LeaseUntil == nil || current.Version != op.Version+1 || current.PayloadRef != op.PayloadRef || committed.Status != current.Status || committed.Version != current.Version || committed.AttemptID != current.AttemptID || committed.PayloadRef != current.PayloadRef || audits != 1 {
					tx.AddError(fmt.Errorf("fault injection did not reach a claimed operation"))
					return
				}
				injections++
				changes := map[string]any{}
				switch invalidation {
				case "lease_expired":
					// Set only this exact locked claim to database-expired.
					expireFixtureOperationLease(t, clean, current)
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
					changed := clean.Model(&Operation{}).Where("org_id = ? AND id = ? AND version = ? AND attempt_id = ?", f.key.OrgID, op.ID, current.Version, current.AttemptID).Updates(changes)
					if changed.Error != nil {
						tx.AddError(changed.Error)
					} else if changed.RowsAffected != 1 {
						tx.AddError(fmt.Errorf("fault injection did not update the exact claim"))
					}
				}
			}))
			t.Cleanup(func() { f.s.DB.Callback().Query().Remove(name) })
			err := f.s.processOperation(context.Background(), op)
			if !prepared || injections != 1 || permissionReads != 1 || !countAfterInjection {
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
