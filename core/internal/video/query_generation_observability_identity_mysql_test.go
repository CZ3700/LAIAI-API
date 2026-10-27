package video

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// The indexed comparison on tasks uses its historical unicode collation, but
// that lookup must never authorize a case/padding alias or another organization.
// These are aggregation-only rows, never provider or financial evidence.
func TestMySQLQueryGenerationObservationExactTaskIdentity(t *testing.T) {
	h := queryRecoveryNew(t)
	f := h.users[0]
	now, err := authorityFixtureNow(h.db)
	must(t, err)
	past := now.Add(-time.Hour)
	must(t, h.db.Transaction(func(tx *gorm.DB) error {
		for _, population := range []string{"active", "terminal"} {
			for _, identity := range []string{"exact", "case", "trailing", "other_org"} {
				id := "identity-" + population + "-" + identity
				stateID, stateOrg := id, f.key.OrgID
				switch identity {
				case "case":
					stateID = strings.ToUpper(id)
				case "trailing":
					stateID += " "
				case "other_org":
					stateOrg = h.users[1].key.OrgID
				}
				status := "running"
				if population == "terminal" {
					status = "succeeded"
				}
				row := task.Task{OrgID: f.key.OrgID, TaskID: id, Provider: Provider, Status: status, SubmittedAt: past}
				state := State{OrgID: stateOrg, TaskID: stateID, BillingState: "reserved", RuntimeVersion: "synthetic-unsupported", AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1, NextPollAt: past, CreatedAt: past, UpdatedAt: past}
				op := Operation{ID: "op-" + id, OrgID: stateOrg, ResourceID: stateID, Kind: "create_task", Status: "pending", Version: 1, CreatedAt: past, UpdatedAt: past}
				inheritOperationRuntime(state, &op)
				receipt := QueryReceipt{OrgID: stateOrg, TaskID: stateID, AttemptID: "receipt-" + id, Sequence: 1, RuntimeVersion: state.RuntimeVersion, AdapterVersion: state.AdapterVersion, ProtocolContractVersion: state.ProtocolContractVersion, ObservationSchemaVersion: 1, Outcome: "accepted", ObservationJSON: "{}", ReceivedAt: past, RecoveryDueAt: &past}
				for _, value := range []any{&row, &state, &op, &receipt} {
					if err := tx.Create(value).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	}))
	s := h.service(t)
	h.forbidNetwork(t, s)
	funds := h.finances(t)
	out := s.WorkerObservability(context.Background())
	if !out.Database.Available || !out.Database.Complete || out.Database.ErrorCode != "" || out.Database.Snapshot == nil {
		t.Fatal("exact task identity aggregation unavailable")
	}
	snapshot := out.Database.Snapshot
	if snapshot.TaskCounts.Running != 1 || snapshot.TaskCounts.TerminalReserved != 1 || workerOperationTotal(snapshot, "create_task", "pending") != 2 {
		t.Fatal("task or operation aggregation accepted a case/padding/organization alias")
	}
	// All four active tasks remain candidates when their state identity is
	// missing or unsupported; only the exact terminal pending pair joins.
	// Corrupt receipt rows remain visible in the raw pending backlog.
	if snapshot.BlockedContracts.Tasks != 5 || snapshot.BlockedContracts.Operations != 2 || snapshot.BlockedContracts.QueryReceipts != 2 || snapshot.QueryReceipts.RetryablePending != 8 {
		t.Fatal("blocked-contract aggregation hid corrupt work or joined an identity alias")
	}
	if funds != h.finances(t) || h.blocked.Load() != 0 || h.readKeys.Load() != 0 {
		t.Fatal("identity aggregation changed funds or accessed provider credentials/network")
	}
}
