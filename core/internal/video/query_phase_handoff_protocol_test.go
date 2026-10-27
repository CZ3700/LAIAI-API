package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQueryPhaseHandoffClosedCommands(t *testing.T) {
	valid := `{"protocol":"query-phase-handoff-v1","run_id":"0123456789abcdef","case_id":"F05_delivery","instance":"C-reader","cmd_id":"1","command":"direct","task":"primary","target":"purge_history"}`
	if _, err := decodePhaseCommand([]byte(valid)); err != nil {
		t.Fatal("valid closed command rejected")
	}
	for _, raw := range []string{
		valid + `{}`, strings.Replace(valid, `"cmd_id":"1"`, `"cmd_id":1`, 1), strings.Replace(valid, `"cmd_id":"1"`, `"cmd_id":"01"`, 1), strings.Replace(valid, `"F05_delivery"`, `"F02"`, 1), strings.Replace(valid, `"primary"`, `"../task"`, 1), strings.Replace(valid, `"target":"purge_history"`, `"target":"purge_history","target":"query_receipt"`, 1), strings.Replace(valid, `"target":"purge_history"`, `"target":"query_receipt","sql":"UPDATE tasks"`, 1), strings.Repeat(" ", 65537),
	} {
		if _, err := decodePhaseCommand([]byte(raw)); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
}

func TestQueryPhaseHandoffHistoricalDigestProjection(t *testing.T) {
	stamp := time.Date(2026, 10, 27, 1, 2, 3, 4000000, time.UTC)
	row := QueryReceipt{AttemptID: "att_test", OrgID: 9007199254740993, TaskID: "task_test", Sequence: 8, TaskRowVersion: 9, RuntimeVersion: "ark-official-v1", AdapterVersion: "ark-v3-v1", ProtocolContractVersion: "ark-v3-v1", ObservationSchemaVersion: 1, HTTPStatus: 200, Outcome: "accepted", ObservationJSON: `{"completion_tokens":100}`, ReceivedAt: stamp, AppliedAt: &stamp, ApplyErrorCode: "temporary", RecoveryDueAt: &stamp, RecoveryVersion: 17}
	raw, got, err := phaseCanonical(row)
	if err != nil {
		t.Fatal("canonical failed")
	}
	sum := sha256.Sum256([]byte(raw))
	if got != hex.EncodeToString(sum[:]) {
		t.Fatal("canonical digest differs")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 21 || string(fields["OrgID"]) != "9007199254740993" || string(fields["AppliedAt"]) != "null" || string(fields["RecoveryDueAt"]) != "null" || string(fields["RecoveryVersion"]) != "0" || string(fields["ApplyErrorCode"]) != `""` {
		t.Fatal("historical projection changed")
	}
	changed := row
	changed.ObservationJSON = `{"completion_tokens":101}`
	_, other, err := phaseCanonical(changed)
	if err != nil || other == got {
		t.Fatal("evidence change escaped digest")
	}
}

func TestQueryPhaseHandoffBarrierReceiptKinds(t *testing.T) {
	create := phaseSnapshot{Tasks: map[string]map[string]any{"primary": {"receipts": []map[string]any{}, "storage": map[string]phaseTableProof{"video_response_inbox": {Rows: 1}}, "operations": []map[string]any{{"receipt_applied_at": nil}}, "task_status": "submitting"}}}
	if !phasePendingProof(create, "primary", true) || phasePendingProof(create, "primary", false) {
		t.Fatal("create receipt confused with query boundary")
	}
	query := phaseSnapshot{Tasks: map[string]map[string]any{"primary": {"receipts": []map[string]any{{"applied_at": nil, "generation_applied_at": nil, "recovery_version": "0"}}}}}
	if !phasePendingProof(query, "primary", false) || phasePendingProof(query, "primary", true) {
		t.Fatal("query receipt confused with create boundary")
	}
}
