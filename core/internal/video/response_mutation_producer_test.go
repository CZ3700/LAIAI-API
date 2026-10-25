package video

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

func TestMutationProducerActionInputStrictJSON(t *testing.T) {
	c := videoruntime.LegacyV1()
	input := mutationActionInput{SchemaVersion: 2, Action: "cancel_task", TaskID: "task_fixture", OperationID: "op_fixture", TargetUpstreamTaskID: "upstream-fixture",
		ProviderScopeID: 9007199254740993, ProviderAccountRef: "synthetic-account", ProviderRegion: "cn-beijing", ProviderProject: "default",
		CredentialSnapshotID: 12, BillingSnapshotID: 13, ProviderBaseURL: "https://ark.cn-beijing.volces.com/api/v3", UpstreamModel: "synthetic-model",
		RuntimeVersion: c.RuntimeVersion, AdapterVersion: c.AdapterVersion, ProtocolContractVersion: c.ProtocolContractVersion, BillingSchemaVersion: c.BillingSchemaVersion}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeMutationAction(append(append([]byte(" \n"), body...), '\n')); err != nil || got != input {
		t.Fatal("canonical action input failed or lost uint64 precision", err, got.ProviderScopeID)
	}
	for _, tc := range []struct{ name, body string }{
		{"empty", ""}, {"array", "[]"}, {"null", "null"}, {"two_objects", string(body) + string(body)}, {"trailing_value", string(body) + " true"},
		{"unknown_field", strings.TrimSuffix(string(body), "}") + `,"credential":"never-allowed"}`},
		{"duplicate_field", strings.TrimSuffix(string(body), "}") + `,"action":"cancel_task"}`},
		{"escaped_duplicate", strings.TrimSuffix(string(body), "}") + `,"\u0061ction":"cancel_task"}`},
		{"wrong_case", strings.Replace(string(body), `"action":`, `"Action":`, 1)},
		{"missing_field", strings.Replace(string(body), `"schema_version":2,`, "", 1)},
		{"unknown_schema", strings.Replace(string(body), `"schema_version":2`, `"schema_version":3`, 1)},
		{"null_field", strings.Replace(string(body), `"action":"cancel_task"`, `"action":null`, 1)},
		{"string_number", strings.Replace(string(body), `"schema_version":2`, `"schema_version":"2"`, 1)},
		{"fractional_id", strings.Replace(string(body), `"credential_snapshot_id":12`, `"credential_snapshot_id":12.1`, 1)},
		{"overflow_id", strings.Replace(string(body), `"credential_snapshot_id":12`, `"credential_snapshot_id":18446744073709551616`, 1)},
		{"oversize", strings.Repeat(" ", 16<<10) + string(body)},
		{"invalid_utf8", strings.Replace(string(body), "synthetic-account", string([]byte{0xff}), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeMutationAction([]byte(tc.body)); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "never-allowed") {
				t.Fatal("malformed capsule accepted or reflected", err)
			}
		})
	}
}

func TestMutationProducerPreparationCooldownAndLocalOutcomes(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, status, code string
		updated            time.Time
		want               bool
	}{
		{"pending_current", "pending", mutationPreparationDeferredCode, now, true},
		{"exact_cutoff", "pending", mutationPreparationDeferredCode, now.Add(-30 * time.Second), false},
		{"one_ns_before_cutoff", "pending", mutationPreparationDeferredCode, now.Add(-30*time.Second + time.Nanosecond), true},
		{"old", "pending", mutationPreparationDeferredCode, now.Add(-31 * time.Second), false},
		{"future_clock", "pending", mutationPreparationDeferredCode, now.Add(time.Second), true},
		{"not_deferred", "pending", "", now, false}, {"claimed", "in_flight", mutationPreparationDeferredCode, now, false},
		{"terminal", "confirmed", mutationPreparationDeferredCode, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mutationPreparationDeferred(Operation{Status: tc.status, ErrorCode: tc.code, UpdatedAt: tc.updated}, now); got != tc.want {
				t.Fatal("cooldown boundary changed", got)
			}
		})
	}
	for _, tc := range []struct {
		taskStatus, operationStatus, code string
		wait                              bool
	}{{"queued", "", "", false}, {"cancelled", "confirmed", "", false}, {"running", "rejected", "invalid_state", false},
		{"succeeded", "rejected", "invalid_state", false}, {"failed", "rejected", "invalid_state", false}, {"accepted", "rejected", "invalid_state", false},
		{"submitting", "", "", true}, {"submission_unknown", "", "", true}} {
		status, code, wait := mutationLocalOutcome(task.Task{Status: tc.taskStatus}, Operation{Kind: "cancel_task", ErrorCode: mutationPreparationDeferredCode, UpdatedAt: now})
		if status != tc.operationStatus || code != tc.code || wait != tc.wait {
			t.Fatal("definitive local cancellation outcome depends on preparation", tc.taskStatus, status, code, wait)
		}
	}
	if status, _, wait := mutationLocalOutcome(task.Task{Status: "cancelled"}, Operation{Kind: "delete_task"}); status != "" || wait {
		t.Fatal("producer changed existing Delete API local-intent semantics")
	}
}

func TestMutationProducerPendingIdentityRejectsStaleAndOverflow(t *testing.T) {
	c := videoruntime.LegacyV1()
	st := State{OrgID: 1, TaskID: "task_fixed", RuntimeVersion: c.RuntimeVersion, AdapterVersion: c.AdapterVersion, ProtocolContractVersion: c.ProtocolContractVersion, BillingSchemaVersion: c.BillingSchemaVersion}
	op := Operation{OrgID: 1, ResourceID: st.TaskID, ID: "op_fixed", Kind: "cancel_task", Status: "pending", Version: 1}
	inheritOperationRuntime(st, &op)
	if err := mutationPendingIdentity(st, op, op); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Operation){func(o *Operation) { o.Version++ }, func(o *Operation) { o.Kind = "delete_task" },
		func(o *Operation) { o.OrgID++ }, func(o *Operation) { o.ID += "x" }, func(o *Operation) { o.ResourceID += "x" },
		func(o *Operation) { o.AttemptID = "att_other" }, func(o *Operation) { o.PayloadRef = "payload_other" }, func(o *Operation) { o.RuntimeVersion = "future" }} {
		changed := op
		mutate(&changed)
		if err := mutationPendingIdentity(st, op, changed); err == nil {
			t.Fatal("stale/cross-identity pending operation accepted")
		}
	}
	for _, version := range []uint64{0, math.MaxUint64} {
		op.Version = version
		if err := mutationPendingIdentity(st, op, op); !errors.Is(err, ErrConflict) {
			t.Fatal("claim version can wrap", version, err)
		}
	}
}

func TestMutationProducerMissingPoolNeverFallsBack(t *testing.T) {
	s := &Service{MutationResponseInboxEnabled: true}
	for _, kind := range []string{"cancel_task", "delete_task"} {
		if err := s.processMutation(context.Background(), Operation{Kind: kind}); !errors.Is(err, ErrUnavailable) {
			t.Fatal("new producer without response pool did not fail closed", err)
		}
	}
	s.responsePaused.Store(true)
	if err := s.processMutation(context.Background(), Operation{Kind: "cancel_task"}); !errors.Is(err, ErrDispatchPaused) {
		t.Fatal("producer bypassed save-failure latch", err)
	}
}
