package video

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func inboxPreparationFixture(t *testing.T) (*Service, ExecutionAttempt, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	keyring, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)), "test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Secret: bytes.Repeat([]byte{7}, 32), Now: func() time.Time { return now }}
	s.Evidence, err = invocation.NewVideoStore(&gorm.DB{}, keyring, invocation.VideoStoreOptions{Now: s.Now})
	if err != nil {
		t.Fatal(err)
	}
	a := ExecutionAttempt{AttemptID: "att_test", OrgID: 1, TaskID: "task_test", OperationID: "op_test", Action: "create_task", StorageProtocol: responseInboxProtocol,
		ProviderScopeID: 1, CredentialSnapshotID: 2, BillingSnapshotID: 3, InputPayloadRef: "vpl_test", InputIdentityHMAC: strings.Repeat("a", 64), InputHMACVersion: "v1", EvidenceHMACVersion: "v1",
		RuntimeVersion: videoruntime.RuntimeV1, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1,
		ExecutionSchemaVersion: 1, ObservationSchemaVersion: 1, OperationClaimVersion: 2, CaptureHistory: true, RetentionDays: 30, RegisteredAt: now}
	return s, a, &now
}

func TestResponseInboxPreparationIdentityExcludesArrivalAndRandomEnvelope(t *testing.T) {
	s, attempt, now := inboxPreparationFixture(t)
	result := ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, HTTPStatus: 202, TaskID: "official-task", RequestID: "official-request", Body: []byte(`{"id":"official-task"}`)}
	first, err := s.prepareResponseReceipt(attempt, result, "credential")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	second, err := s.prepareResponseReceipt(attempt, result, "credential")
	if err != nil {
		t.Fatal(err)
	}
	if first.receipt.EvidenceHMAC != second.receipt.EvidenceHMAC || first.receipt.ReceiptID == second.receipt.ReceiptID || first.payload.PayloadID() == second.payload.PayloadID() {
		t.Fatal("evidence identity included random or arrival values")
	}
	result.Body = []byte(`{"id":"official-task", "extra":1}`)
	third, err := s.prepareResponseReceipt(attempt, result, "credential")
	if err != nil {
		t.Fatal(err)
	}
	if third.receipt.EvidenceHMAC == first.receipt.EvidenceHMAC {
		t.Fatal("distinct bounded response evidence was collapsed")
	}
	oldSeal := first.seal
	first.receipt.TaskID = "another-task"
	if s.responsePreparationSeal(first) == oldSeal {
		t.Fatal("prepared identity substitution was not authenticated")
	}
}

func TestResponseInboxPreparationRetainsUnknownContractAndBoundsUnsafeFields(t *testing.T) {
	s, attempt, _ := inboxPreparationFixture(t)
	attempt.RuntimeVersion, attempt.ObservationSchemaVersion, attempt.CaptureHistory = "future-runtime", 2, false
	result := ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, HTTPStatus: 200, TaskID: "https://secret.invalid/task?token=secret", RequestID: "Bearer secret",
		Body: bytes.Repeat([]byte("x"), int(ark.DefaultMaxResponseBytes)+1), Error: &ark.Error{Code: "credential=secret", Message: "sensitive upstream message"}}
	p, err := s.prepareResponseReceipt(attempt, result, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if p.receipt.RuntimeVersion != "future-runtime" || p.receipt.ObservationSchemaVersion != 2 || p.receipt.Outcome != string(ark.OutcomeUnknown) || !p.receipt.Truncated || p.receipt.CapturedBodyBytes != ark.DefaultMaxResponseBytes {
		t.Fatal("unknown contract or bounded evidence was incorrectly interpreted")
	}
	if p.receipt.UpstreamTaskID != "" || p.receipt.ProviderRequestID != "" || p.receipt.SafeErrorCode != "invalid_upstream_response" {
		t.Fatal("unsafe fields crossed the typed receipt boundary")
	}
	encoded, err := json.Marshal(p.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret")) || bytes.Contains(encoded, []byte("sensitive")) {
		t.Fatal("receipt contains raw provider data")
	}
}

func TestResponseInboxPreparationRejectsCredentialReflectedInTypedIDs(t *testing.T) {
	for _, mode := range []string{"task", "request", "both"} {
		t.Run(mode, func(t *testing.T) {
			s, attempt, _ := inboxPreparationFixture(t)
			credential := "synthetic-official-credential"
			result := ark.Result{Action: ark.ActionSubmit, Outcome: ark.OutcomeAccepted, HTTPStatus: 200,
				TaskID: "official-task", RequestID: "official-request", Body: []byte(`{"id":"official-task"}`)}
			if mode == "task" || mode == "both" {
				result.TaskID = credential
			}
			if mode == "request" || mode == "both" {
				result.RequestID = "prefix-" + credential + "-suffix"
			}
			p, err := s.prepareResponseReceipt(attempt, result, credential)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(p.receipt)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(credential)) {
				t.Fatal("credential reflection crossed typed metadata boundary")
			}
			if mode == "request" {
				if p.receipt.Outcome != string(ark.OutcomeAccepted) || p.receipt.UpstreamTaskID != "official-task" || p.receipt.ProviderRequestID != "" {
					t.Fatal("safe task identity lost or unsafe request identity retained")
				}
			} else if p.receipt.Outcome != string(ark.OutcomeUnknown) || p.receipt.UpstreamTaskID != "" {
				t.Fatal("unsafe accepted task identity was used for binding")
			}
		})
	}
}
