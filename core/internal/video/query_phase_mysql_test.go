package video

import (
	"context"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"reflect"
	"testing"
)

func TestMySQLQueryPhaseRejectsInvalidGenerationDatesBeforeLimit(t *testing.T) {
	h := queryRecoveryNew(t)
	item := h.pending(t, 0, 0)
	h.arrange(t, &item, false, 1)
	before := h.finances(t)
	// This is ONLY a candidate/driver representation fixture. It is explicitly
	// not a V2 admission, execution capsule or permission to run a reducer.
	queryRecoveryCheck(t, h.db.Model(&State{}).Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).Updates(map[string]any{"runtime_version": videoruntime.RuntimeV2, "settlement_receipt_id": item.row.AttemptID}).Error)
	queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("org_id = ? AND task_id = ? AND attempt_id = ?", item.row.OrgID, item.row.TaskID, item.row.AttemptID).Updates(map[string]any{"runtime_version": videoruntime.RuntimeV2, "settlement_kind": querySettlementUsage, "settlement_evidence_sha256": "bad"}).Error)
	queryRecoveryCheck(t, h.db.WithContext(context.Background()).Connection(func(db *gorm.DB) error {
		// Keep the pinned connection while giving each chain a clean statement.
		db = db.Session(&gorm.Session{NewDB: true})
		var original string
		if err := db.Raw("SELECT @@session.sql_mode").Scan(&original).Error; err != nil {
			return err
		}
		if err := db.Exec("SET SESSION sql_mode = ''").Error; err != nil {
			return err
		}
		defer func() { queryRecoveryCheck(t, db.Exec("SET SESSION sql_mode = ?", original).Error) }()
		for _, tc := range []struct {
			value string
			want  int
		}{{"0000-00-00 00:00:00", 0}, {"0001-01-01 00:00:00", 0}, {"0999-12-31 23:59:59", 0}, {"1000-01-01 00:00:00", 1}, {"9999-12-31 23:59:59", 1}} {
			if err := db.Exec("UPDATE video_query_receipts SET generation_applied_at = ? WHERE org_id = ? AND task_id = ? AND attempt_id = ?", tc.value, item.row.OrgID, item.row.TaskID, item.row.AttemptID).Error; err != nil {
				return err
			}
			var raw string
			if err := db.Table("video_query_receipts").Select("DATE_FORMAT(generation_applied_at, '%Y-%m-%d %H:%i:%s')").Where("attempt_id = ?", item.row.AttemptID).Scan(&raw).Error; err != nil {
				return err
			}
			if raw != tc.value && !(tc.want == 0 && raw == "0000-00-00 00:00:00") {
				t.Fatal("permissive-session representation differs from intended date fixture")
			}
			var rows []QueryReceipt
			q := querySettlementEligible(db.Table("video_query_receipts AS r").Select("r.*").Joins("JOIN video_task_states v ON v.org_id=r.org_id AND v.task_id=r.task_id")).Order("r.recovery_due_at,r.received_at,r.attempt_id").Limit(40).Find(&rows)
			if q.Error != nil {
				return q.Error
			}
			if len(rows) != tc.want {
				t.Fatal("illegal generation occupied B candidate window")
			}
			var current QueryReceipt
			var st State
			if err := db.Where("attempt_id = ?", item.row.AttemptID).Take(&current).Error; err != nil {
				return err
			}
			if err := db.Where("task_id = ?", item.row.TaskID).Take(&st).Error; err != nil {
				return err
			}
			phase, err := queryPhaseOf(st, current)
			if (err == nil) != (tc.want == 1) || (tc.want == 1 && phase != queryPhaseSettlementV2) {
				t.Fatal("driver decoded date and SQL phase disagree")
			}
		}
		return nil
	}))
	after := h.finances(t)
	if before != after {
		t.Fatal("candidate-only date fixture changed money")
	}
}

// Real candidate SQL over tracked schema, with synthetic marker/format damage.
// This deliberately does not claim V2 admission, G/B execution or a release API.
func TestMySQLQueryPhaseClosedMarkerExcludesBothGenerationModes(t *testing.T) {
	h := queryRecoveryNew(t)
	blocked, healthy := h.pending(t, 0, 0), h.pending(t, 1, 1)
	h.arrange(t, &blocked, false, 2)
	h.arrange(t, &healthy, false, 1)
	funds, evidence := h.finances(t), h.evidenceStorage(t)
	posts, gets := h.users[0].posts.Load()+h.users[1].posts.Load(), h.users[0].gets.Load()+h.users[1].gets.Load()
	marker := blocked.row
	marker.AttemptID += "-barrier"
	marker.Sequence++
	marker.RuntimeVersion = "unknown-marker"
	marker.ObservationSchemaVersion = 99
	marker.ObservationJSON = "damaged marker is not parsed"
	marker.SettlementKind = "invalid-phase"
	marker.SettlementEvidenceSHA256 = "bad"
	marker.ApplyErrorCode = queryFinancialConflictCode
	marker.AppliedAt = &marker.ReceivedAt
	queryRecoveryCheck(t, h.db.Omit("RecoveryDueAt").Create(&marker).Error)
	queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("attempt_id = ?", marker.AttemptID).UpdateColumn("recovery_due_at", nil).Error)
	readCandidates := func() []QueryReceipt {
		t.Helper()
		before := queryRecoveryHash(t, queryRecoveryProjection(t, h.db, "video_query_receipts", ""))
		var rows []QueryReceipt
		q := queryGenerationEligible(h.db.Table("video_query_receipts AS r").Select("r.*").Joins("JOIN video_task_states v ON v.org_id=r.org_id AND v.task_id=r.task_id")).Order("r.recovery_due_at,r.received_at,r.attempt_id").Limit(40).Find(&rows)
		queryRecoveryCheck(t, q.Error)
		after := queryRecoveryHash(t, queryRecoveryProjection(t, h.db, "video_query_receipts", ""))
		if before != after {
			t.Fatal("candidate read changed persisted marker or evidence")
		}
		return rows
	}
	for _, runtime := range []string{videoruntime.RuntimeV1, videoruntime.RuntimeV2, videoruntime.RuntimeV1} {
		// V1 after V2 is an explicit damaged-contract fallback fixture, not a
		// supported migration of a running task or a changed execution capsule.
		for _, item := range []queryRecoveryPending{blocked, healthy} {
			queryRecoveryCheck(t, h.db.Model(&State{}).Where("org_id = ? AND task_id = ?", item.row.OrgID, item.row.TaskID).UpdateColumn("runtime_version", runtime).Error)
			queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("attempt_id = ?", item.row.AttemptID).UpdateColumn("runtime_version", runtime).Error)
		}
		rows := readCandidates()
		if len(rows) != 1 || rows[0].AttemptID != healthy.row.AttemptID {
			t.Fatal("closed marker allowed same-task receipt or blocked another org")
		}
	}
	// Exact old code is deliberately not a task barrier. This fixture switch
	// only proves selector semantics; production has no unpause/marker edit API.
	queryRecoveryCheck(t, h.db.Model(&QueryReceipt{}).Where("attempt_id = ?", marker.AttemptID).UpdateColumn("apply_error_code", "settlement_conflict").Error)
	rows := readCandidates()
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AttemptID)
	}
	if !reflect.DeepEqual(ids, []string{blocked.row.AttemptID, healthy.row.AttemptID}) {
		t.Fatal("ordinary old code became a task-level financial barrier")
	}
	if h.finances(t) != funds || h.evidenceStorage(t) != evidence || h.users[0].posts.Load()+h.users[1].posts.Load() != posts || h.users[0].gets.Load()+h.users[1].gets.Load() != gets {
		t.Fatal("candidate fixture changed money, capsule evidence or HTTP count")
	}
}
