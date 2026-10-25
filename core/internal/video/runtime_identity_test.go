package video

import (
	"context"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

func TestMySQLRuntimeExecutionRemainsBoundToAcceptedTask(t *testing.T) {
	f := mysqlFixture(t)
	first := f.create(t, "runtime-input-original")
	second := f.create(t, "runtime-input-other-task")
	firstOp := runtimeWorkerCreateOperation(t, f, first.ID)
	secondOp := runtimeWorkerCreateOperation(t, f, second.ID)
	// The candidate and current operation agree and the replacement is a valid
	// v1 capsule in the same organization. Only the acceptance binding detects
	// that it is the wrong input for the task whose funds were reserved.
	firstOp.PayloadRef = secondOp.PayloadRef
	must(t, f.s.DB.Model(&Operation{}).Where("org_id = ? AND id = ?", firstOp.OrgID, firstOp.ID).Update("payload_ref", firstOp.PayloadRef).Error)
	if err := f.s.processOperation(context.Background(), firstOp); !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatalf("wrong accepted input was allowed: %v", err)
	}
	var attempts int64
	must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, first.ID).Count(&attempts).Error)
	if attempts != 0 || f.posts.Load() != 0 || f.gets.Load() != 0 {
		t.Fatal("wrong input was claimed or sent")
	}
	for _, id := range []string{first.ID, second.ID} {
		h := f.hold(t, id)
		if h.Status != ledger.HoldActive || !h.CapturedAmount.IsZero() {
			t.Fatal("input mismatch changed the reservation")
		}
	}
}
