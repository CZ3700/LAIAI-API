package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// These are metadata-only plan fixtures, not decryptable execution inputs.
// Exercise the actual production SELECT and explain its captured SQL/bindings;
// the separate lifecycle tests prove erasure of valid encrypted capsules.
func TestMySQLVideoExecutionPurgeCandidatePlans(t *testing.T) {
	s, db, now := videoExecutionMySQLStore(t)
	var metadata []VideoPayload
	type expectedCandidate struct {
		id  string
		due time.Time
	}
	oracle := map[string][]expectedCandidate{}
	caseKey := func(execution, cleanup bool) string {
		return fmt.Sprintf("execution_%t_cleanup_%t", execution, cleanup)
	}
	// Large irrelevant expired history comes before all live candidates.
	// Cleanup purpose groups are deliberately not interleaved: one partition
	// must skip the other's earlier backlog via the index, not residual scans.
	for i := range 32768 {
		row := VideoPayload{
			PayloadID: fmt.Sprintf("plan-%08d", i), OrgID: 1,
			RequestID: fmt.Sprintf("plan-request-%08d", i), CorrelationKey: fmt.Sprintf("%064x", i+1),
			Direction: VideoEffectiveRequest, Purpose: VideoPurposeExecution, SchemaVersion: 1,
			BodyFormat: "json", Nonce: []byte{}, RedactionVersion: "plan-only",
			CaptureStatus: VideoCaptureStaged, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
		}
		if i%2 == 1 {
			row.Purpose, row.Direction = VideoPurposeHistory, VideoClientRequest
		}
		if i < 24576 {
			row.ExpiresAt = now.Add(-24 * time.Hour)
			row.CaptureStatus = VideoCaptureExpired
			deleted := now.Add(-time.Hour)
			row.ObjectDeletedAt = &deleted
		} else if i < 28672 {
			row.Purpose = VideoPurposeHistory
			cleanup := now.Add(-2 * time.Minute)
			if i >= 26624 {
				row.Purpose = VideoPurposeExecution
				cleanup = now.Add(-time.Minute)
			}
			row.CaptureStatus = VideoCaptureExpired
			row.ExpiresAt = now.Add(-time.Hour)
			row.CleanupAfter = &cleanup
			key := caseKey(i >= 26624, true)
			oracle[key] = append(oracle[key], expectedCandidate{row.PayloadID, cleanup})
		} else if i < 31232 {
			row.ExpiresAt = now.Add(-time.Minute)
			key := caseKey(i%2 == 0, false)
			oracle[key] = append(oracle[key], expectedCandidate{row.PayloadID, row.ExpiresAt})
		}
		metadata = append(metadata, row)
	}
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer setupCancel()
	if err := db.WithContext(setupCtx).CreateInBatches(metadata, 256).Error; err != nil {
		t.Fatal("seed metadata-only candidate plan fixture", err)
	}
	if err := db.Exec("ANALYZE TABLE invocation_payloads").Error; err != nil {
		t.Fatal("analyze private fixture table", err)
	}
	for key := range oracle {
		sort.Slice(oracle[key], func(i, j int) bool {
			a, b := oracle[key][i], oracle[key][j]
			if !a.due.Equal(b.due) {
				return a.due.Before(b.due)
			}
			return a.id < b.id
		})
	}
	for _, execution := range []bool{true, false} {
		for _, cleanup := range []bool{false, true} {
			t.Run(caseKey(execution, cleanup), func(t *testing.T) {
				var statement string
				var variables []any
				var calls int
				const callback = "test:purge-candidate-plan"
				if err := db.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
					if _, ok := q.Statement.Dest.(*[]videoPurgeCandidate); !ok {
						return
					}
					calls++
					statement, variables = q.Statement.SQL.String(), append([]any(nil), q.Statement.Vars...)
				}); err != nil {
					t.Fatal(err)
				}
				rows, err := s.videoPurgeCandidates(context.Background(), 2, execution, cleanup)
				db.Callback().Query().Remove(callback)
				if err != nil || calls != 1 || len(rows) != 2 || !strings.Contains(statement, "LIMIT ?") {
					t.Fatal("actual candidate query was not bounded", err, calls, len(rows))
				}
				want := oracle[caseKey(execution, cleanup)]
				for i, row := range rows {
					if row.OrgID != 1 || row.PayloadID != want[i].id {
						t.Fatal("candidate differs from independent sorted fixture oracle")
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var plan []map[string]any
				if err := db.WithContext(ctx).Raw("EXPLAIN "+statement, variables...).Scan(&plan).Error; err != nil || len(plan) == 0 {
					t.Fatal("explain actual candidate query", err)
				}
				index := "idx_video_payload_purge_due"
				if cleanup {
					index = "idx_video_payload_cleanup_partition"
				}
				if len(plan) != 1 || fmt.Sprint(plan[0]["key"]) != index || (fmt.Sprint(plan[0]["type"]) != "range" && fmt.Sprint(plan[0]["type"]) != "ref") || strings.Contains(fmt.Sprint(plan[0]["Extra"]), "filesort") {
					t.Fatal("actual purge candidate did not use the selective ordered index", plan)
				}
				record, err := json.Marshal(struct {
					SQL  string           `json:"sql"`
					Vars []any            `json:"vars"`
					Plan []map[string]any `json:"plan"`
				}{statement, variables, plan})
				if err != nil {
					t.Fatal(err)
				}
				// Estimate rows is not actual rows examined. Record the actual
				// bounded execution too; do not force an index or widen the budget.
				t.Log("purge_candidate_explain=" + string(record))
				analyzeCtx, analyzeCancel := context.WithTimeout(context.Background(), time.Second)
				defer analyzeCancel()
				var analysis []string
				if err := db.WithContext(analyzeCtx).Raw("EXPLAIN ANALYZE "+statement, variables...).Scan(&analysis).Error; err != nil || len(analysis) == 0 || !strings.Contains(strings.Join(analysis, "\n"), "actual time=") {
					t.Fatal("actual candidate execution plan unavailable", err)
				}
				t.Log("purge_candidate_analyze=" + strings.Join(analysis, "\n"))
				full, err := s.videoPurgeCandidates(context.Background(), 1000, execution, cleanup)
				if err != nil || len(full) != 1000 {
					t.Fatal("full legal batch was truncated or exceeded its budget", err, len(full))
				}
				var gotIDs, wantIDs []string
				for i, row := range full {
					gotIDs = append(gotIDs, row.PayloadID)
					wantIDs = append(wantIDs, want[i].id)
				}
				if !reflect.DeepEqual(gotIDs, wantIDs) {
					t.Fatal("full legal batch differs from independent oracle")
				}
			})
		}
	}
	// Metadata-only fixture update, not a production expiry/identity edit:
	// exhaust both qualifications and prove all four real queries return zero.
	if err := db.Model(&VideoPayload{}).Where("org_id = ?", 1).Updates(map[string]any{"capture_status": VideoCaptureExpired, "object_deleted_at": *now}).Error; err != nil {
		t.Fatal(err)
	}
	for _, execution := range []bool{true, false} {
		for _, cleanup := range []bool{false, true} {
			if rows, err := s.videoPurgeCandidates(context.Background(), 2, execution, cleanup); err != nil || len(rows) != 0 {
				t.Fatal("empty candidate partition returned rows", execution, cleanup, err)
			}
		}
	}
}
