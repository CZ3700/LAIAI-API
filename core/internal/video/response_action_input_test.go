package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

func actionPreparationFixture(t *testing.T) (*Service, ExecutionAttempt) {
	t.Helper()
	s, a, _ := inboxPreparationFixture(t)
	target := "official-existing-task"
	a.Action, a.StorageProtocol, a.ExecutionSchemaVersion, a.TargetUpstreamTaskID = "cancel_task", videoruntime.ActionStorageProtocolV1, 2, &target
	return s, a
}

func TestActionExtensionPreservesCreateV1EvidenceGolden(t *testing.T) {
	s, attempt, _ := inboxPreparationFixture(t)
	result := ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, HTTPStatus: 202, TaskID: "official-task", RequestID: "official-request", Body: []byte(`{"id":"official-task"}`)}
	p, err := s.prepareResponseReceipt(attempt, result, "credential")
	if err != nil {
		t.Fatal(err)
	}
	// Independently calculated from the pre-A2 ExecutionAttempt/ResponseInbox
	// JSON field order, synthetic 0x07 key and the fixed fixture above.
	const previousV1 = "bea4338b9e5caeeafc58753613a49b670c2e9a00138e2554ccd785047e44e90c"
	if p.receipt.EvidenceHMAC != previousV1 {
		t.Fatal("create-v1 evidence canonical or HMAC changed")
	}
	encoded, _ := json.Marshal(attempt)
	if bytes.Contains(encoded, []byte("TargetUpstreamTaskID")) {
		t.Fatal("nil action field changed historical create canonical")
	}
	s, attempt = actionPreparationFixture(t)
	ack := ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: 204}
	first, err := s.prepareMutationResponse(attempt, ack, "")
	if err != nil {
		t.Fatal(err)
	}
	target := *attempt.TargetUpstreamTaskID + "-other"
	attempt.TargetUpstreamTaskID = &target
	second, err := s.prepareMutationResponse(attempt, ack, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.receipt.EvidenceHMAC == second.receipt.EvidenceHMAC {
		t.Fatal("action target absent from evidence identity")
	}
}

func TestActionResponsePreparationKeepsEmptyReturnedIDAndOwnsTarget(t *testing.T) {
	s, attempt := actionPreparationFixture(t)
	for _, status := range []int{200, 201, 202, 204} {
		p, err := s.prepareMutationResponse(attempt, ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: status}, "synthetic-credential")
		if err != nil {
			t.Fatal(err)
		}
		if p.receipt.Outcome != string(ark.OutcomeAccepted) || p.receipt.UpstreamTaskID != "" || p.receipt.Action != "cancel_task" {
			t.Fatal("DELETE acknowledgement was given a fabricated returned ID or downgraded")
		}
		if p.attempt.TargetUpstreamTaskID == attempt.TargetUpstreamTaskID || *p.attempt.TargetUpstreamTaskID != *attempt.TargetUpstreamTaskID {
			t.Fatal("prepared response did not capture its own target value")
		}
		copy := attempt
		value := *attempt.TargetUpstreamTaskID
		copy.TargetUpstreamTaskID = &value
		if !sameExecutionAttempt(copy, attempt) {
			t.Fatal("immutable identity compared pointer addresses")
		}
		value += "-changed"
		if sameExecutionAttempt(copy, attempt) {
			t.Fatal("different target passed immutable identity")
		}
	}
	legacy := attempt
	legacy.TargetUpstreamTaskID = nil
	empty := ""
	invalid := legacy
	invalid.TargetUpstreamTaskID = &empty
	if sameExecutionAttempt(legacy, invalid) {
		t.Fatal("NULL target collapsed into empty action target")
	}
}

func TestActionResponsePreparationRejectsWrongTransportAndUnsafeTarget(t *testing.T) {
	s, attempt := actionPreparationFixture(t)
	for _, target := range []string{"", " x", "x ", "x/y", "https://example.invalid", "synthetic-credential"} {
		attempt.TargetUpstreamTaskID = &target
		if _, err := s.prepareMutationResponse(attempt, ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: 200}, "synthetic-credential"); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe target accepted")
		}
	}
	s, attempt = actionPreparationFixture(t)
	if _, err := s.prepareMutationResponse(attempt, ark.Result{Action: ark.ActionSubmit}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("POST result accepted as DELETE evidence")
	}
	if _, err := s.prepareResponseReceipt(attempt, ark.Result{Action: ark.ActionSubmit}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("action target accepted by create preparer")
	}
	result := ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: 200, TaskID: "synthetic-credential", RequestID: "prefix-synthetic-credential"}
	p, err := s.prepareMutationResponse(attempt, result, "synthetic-credential")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(p.receipt)
	if bytes.Contains(encoded, []byte("synthetic-credential")) || p.receipt.Outcome != string(ark.OutcomeUnknown) {
		t.Fatal("reflected credential entered typed evidence or authorized deletion")
	}
}

func TestActionResponseCapturePreservesUnknownProducerContract(t *testing.T) {
	s, attempt := actionPreparationFixture(t)
	attempt.StorageProtocol, attempt.RuntimeVersion, attempt.ExecutionSchemaVersion, attempt.ObservationSchemaVersion = "future-actions", "future-runtime", 3, 7
	p, err := s.prepareMutationResponse(attempt, ark.Result{Action: ark.ActionDelete, Outcome: ark.OutcomeAccepted, HTTPStatus: 204}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.receipt.StorageProtocol != attempt.StorageProtocol || p.receipt.RuntimeVersion != attempt.RuntimeVersion || p.receipt.ExecutionSchemaVersion != 3 || p.receipt.ObservationSchemaVersion != 7 {
		t.Fatal("late producer evidence was interpreted using this consumer's registry")
	}
}

func TestActionInputHMACBindsMetadataAndFixedExecutionFacts(t *testing.T) {
	s, _ := actionPreparationFixture(t)
	identity := invocation.VideoExecutionIdentity{PayloadID: "vpl_synthetic", OrgID: 1, RequestID: "op_synthetic", AttemptID: "att_synthetic", SchemaVersion: 2}
	input := mutationActionInput{SchemaVersion: 2, Action: "cancel_task", TaskID: "task_synthetic", OperationID: "op_synthetic", TargetUpstreamTaskID: "official-synthetic",
		ProviderScopeID: 1, ProviderAccountRef: "account-synthetic", ProviderRegion: "cn-beijing", ProviderProject: "default", CredentialSnapshotID: 2, BillingSnapshotID: 3,
		ProviderBaseURL: "https://ark.cn-beijing.volces.com/api/v3", UpstreamModel: "synthetic-model", RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1}
	baseline := s.mutationInputHMAC(identity, input)
	for name, change := range map[string]func(*mutationActionInput){
		"action": func(v *mutationActionInput) { v.Action = "delete_task" }, "target": func(v *mutationActionInput) { v.TargetUpstreamTaskID += "X" },
		"task": func(v *mutationActionInput) { v.TaskID += "X" }, "operation": func(v *mutationActionInput) { v.OperationID += "X" },
		"scope": func(v *mutationActionInput) { v.ProviderScopeID++ }, "account": func(v *mutationActionInput) { v.ProviderAccountRef += "X" },
		"region": func(v *mutationActionInput) { v.ProviderRegion += "X" }, "project": func(v *mutationActionInput) { v.ProviderProject += "X" },
		"credential": func(v *mutationActionInput) { v.CredentialSnapshotID++ }, "billing": func(v *mutationActionInput) { v.BillingSnapshotID++ },
		"base": func(v *mutationActionInput) { v.ProviderBaseURL += "X" }, "model": func(v *mutationActionInput) { v.UpstreamModel += "X" },
		"runtime": func(v *mutationActionInput) { v.RuntimeVersion += "X" }, "adapter": func(v *mutationActionInput) { v.AdapterVersion += "X" },
		"protocol": func(v *mutationActionInput) { v.ProtocolContractVersion += "X" }, "billing_schema": func(v *mutationActionInput) { v.BillingSchemaVersion += "X" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			change(&changed)
			if s.mutationInputHMAC(identity, changed) == baseline {
				t.Fatal("fixed identity fact was not authenticated")
			}
		})
	}
	identity.AttemptID += "X"
	if s.mutationInputHMAC(identity, input) == baseline {
		t.Fatal("input metadata not authenticated")
	}
}

func TestActionEvidenceClassificationDoesNotTreatIncomplete404AsACK(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		status  int
		want    string
	}{
		{string(ark.OutcomeRejected), 404, "ack"}, {string(ark.OutcomeAccepted), 204, "ack"}, {string(ark.OutcomeUnknown), 404, ""},
		{string(ark.OutcomeUnknown), 200, ""}, {string(ark.OutcomeNotSent), 404, "not_sent"}, {string(ark.OutcomeRejected), 400, "rejected"},
	} {
		if got := mutationEvidenceClass(tc.outcome, tc.status); got != tc.want {
			t.Fatalf("outcome %s status %d: %q", tc.outcome, tc.status, got)
		}
	}
}
