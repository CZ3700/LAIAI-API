package video

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"github.com/shopspring/decimal"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Frozen independent literal proposal, reviewed before the implementation.
// No fixture generates its expected digest with a production encoder.
var querySettlementGoldens = []struct{ name, kind, receipt, canonical, sha string }{
	{`usage_100`, `usage`, `{"AttemptID":"query-s2-1","OrgID":9007199254740993,"TaskID":"task-s2-1","Sequence":2,"TaskRowVersion":3,"RuntimeVersion":"ark-official-generation-settlement-v2","AdapterVersion":"ark-v3-v1","ProtocolContractVersion":"video-contract-v1","ObservationSchemaVersion":1,"HTTPStatus":200,"Outcome":"accepted","ProviderRequestID":"request-\u003ctest\u003e\u0026\u2028","ObservationJSON":"{\"task_id\":\"up-s2-1\",\"model\":\"synthetic-upstream\",\"status\":\"succeeded\",\"completion_tokens\":100,\"total_tokens\":100,\"has_error\":false,\"delivery_payload_ref\":\"delivery-s2-1\"}","HasVideo":true,"HasLastFrame":false,"PayloadRef":"query-payload-s2-1","ReceivedAt":"2026-10-27T02:03:04.123Z","AppliedAt":null,"ApplyErrorCode":"","RecoveryDueAt":null,"RecoveryVersion":0}`, `{"domain":"query-settlement-evidence-v1","org_id":9007199254740993,"task_id":"task-s2-1","attempt_id":"query-s2-1","runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1","billing_snapshot_id":9007199254740994,"provider_scope_id":17,"credential_snapshot_id":19,"channel_id":23,"model":"synthetic-public-model","upstream_task_id":"up-s2-1","upstream_model":"synthetic-upstream","create_request_id":"create-s2-1","no_charge_statuses":"failed,cancelled","settlement_kind":"usage","receipt_evidence_sha256":"06a83e226a7ee5a23115a5d010edd26ef36074600edf74403a244e42118cf8ab"}`, `a761859e885dedb1d8225e90ab67f1268ab4990e91836c9fb65963c63615cf13`},
	{`usage_zero`, `usage`, `{"AttemptID":"query-s2-1","OrgID":9007199254740993,"TaskID":"task-s2-1","Sequence":2,"TaskRowVersion":3,"RuntimeVersion":"ark-official-generation-settlement-v2","AdapterVersion":"ark-v3-v1","ProtocolContractVersion":"video-contract-v1","ObservationSchemaVersion":1,"HTTPStatus":200,"Outcome":"accepted","ProviderRequestID":"request-\u003ctest\u003e\u0026\u2028","ObservationJSON":"{\"task_id\":\"up-s2-1\",\"model\":\"synthetic-upstream\",\"status\":\"succeeded\",\"completion_tokens\":0,\"total_tokens\":0,\"has_error\":false,\"delivery_payload_ref\":\"delivery-s2-1\"}","HasVideo":true,"HasLastFrame":false,"PayloadRef":"query-payload-s2-1","ReceivedAt":"2026-10-27T02:03:04.123Z","AppliedAt":null,"ApplyErrorCode":"","RecoveryDueAt":null,"RecoveryVersion":0}`, `{"domain":"query-settlement-evidence-v1","org_id":9007199254740993,"task_id":"task-s2-1","attempt_id":"query-s2-1","runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1","billing_snapshot_id":9007199254740994,"provider_scope_id":17,"credential_snapshot_id":19,"channel_id":23,"model":"synthetic-public-model","upstream_task_id":"up-s2-1","upstream_model":"synthetic-upstream","create_request_id":"create-s2-1","no_charge_statuses":"failed,cancelled","settlement_kind":"usage","receipt_evidence_sha256":"33b69bcb3715d15ed88b22f71d553eb642a56fcb745aa8901bcb0a67da5f4360"}`, `f589baa06ecb3781a9c35d5c3b6468d8650cb1c850a8570be269c88954daac0f`},
	{`missing_usage`, `none`, `{"AttemptID":"query-s2-1","OrgID":9007199254740993,"TaskID":"task-s2-1","Sequence":2,"TaskRowVersion":3,"RuntimeVersion":"ark-official-generation-settlement-v2","AdapterVersion":"ark-v3-v1","ProtocolContractVersion":"video-contract-v1","ObservationSchemaVersion":1,"HTTPStatus":200,"Outcome":"accepted","ProviderRequestID":"request-\u003ctest\u003e\u0026\u2028","ObservationJSON":"{\"task_id\":\"up-s2-1\",\"model\":\"synthetic-upstream\",\"status\":\"succeeded\",\"completion_tokens\":null,\"total_tokens\":null,\"has_error\":false,\"delivery_payload_ref\":\"delivery-s2-1\"}","HasVideo":true,"HasLastFrame":false,"PayloadRef":"query-payload-s2-1","ReceivedAt":"2026-10-27T02:03:04.123Z","AppliedAt":null,"ApplyErrorCode":"","RecoveryDueAt":null,"RecoveryVersion":0}`, `{"domain":"query-settlement-evidence-v1","org_id":9007199254740993,"task_id":"task-s2-1","attempt_id":"query-s2-1","runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1","billing_snapshot_id":9007199254740994,"provider_scope_id":17,"credential_snapshot_id":19,"channel_id":23,"model":"synthetic-public-model","upstream_task_id":"up-s2-1","upstream_model":"synthetic-upstream","create_request_id":"create-s2-1","no_charge_statuses":"failed,cancelled","settlement_kind":"none","receipt_evidence_sha256":"2ab370b3efacb12043079eec64d30ba4182901dc783efd12d8be77fa00aae211"}`, `e56b3fdf544777184edb9f04efa3748fc4b49eaa20d363f22de255d4663eeebe`},
	{`provider_nocharge`, `provider_nocharge`, `{"AttemptID":"query-s2-1","OrgID":9007199254740993,"TaskID":"task-s2-1","Sequence":2,"TaskRowVersion":3,"RuntimeVersion":"ark-official-generation-settlement-v2","AdapterVersion":"ark-v3-v1","ProtocolContractVersion":"video-contract-v1","ObservationSchemaVersion":1,"HTTPStatus":200,"Outcome":"accepted","ProviderRequestID":"request-\u003ctest\u003e\u0026\u2028","ObservationJSON":"{\"task_id\":\"up-s2-1\",\"model\":\"synthetic-upstream\",\"status\":\"cancelled\",\"completion_tokens\":null,\"total_tokens\":null,\"has_error\":false}","HasVideo":false,"HasLastFrame":false,"PayloadRef":"query-payload-s2-1","ReceivedAt":"2026-10-27T02:03:04.123Z","AppliedAt":null,"ApplyErrorCode":"","RecoveryDueAt":null,"RecoveryVersion":0}`, `{"domain":"query-settlement-evidence-v1","org_id":9007199254740993,"task_id":"task-s2-1","attempt_id":"query-s2-1","runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1","billing_snapshot_id":9007199254740994,"provider_scope_id":17,"credential_snapshot_id":19,"channel_id":23,"model":"synthetic-public-model","upstream_task_id":"up-s2-1","upstream_model":"synthetic-upstream","create_request_id":"create-s2-1","no_charge_statuses":"failed,cancelled","settlement_kind":"provider_nocharge","receipt_evidence_sha256":"a689fe7341351370d088bbc06c380e0f0ba15dfe00de89922fac8370e08dbd0d"}`, `7b41a320c22723036feae5649efdb46a170f4917ed6d7ecb2f28994aab3786f2`},
}

func querySettlementFixture(t *testing.T, index int) (task.Task, State, QueryReceipt) {
	t.Helper()
	var r QueryReceipt
	if err := json.Unmarshal([]byte(querySettlementGoldens[index].receipt), &r); err != nil {
		t.Fatal(err)
	}
	row := task.Task{OrgID: r.OrgID, TaskID: r.TaskID, Provider: Provider, ChannelID: 23, Model: "synthetic-public-model", UpstreamTaskID: "up-s2-1"}
	st := State{OrgID: r.OrgID, TaskID: r.TaskID, RuntimeVersion: videoruntime.RuntimeV2, AdapterVersion: videoruntime.AdapterV1, ProtocolContractVersion: videoruntime.ProtocolV1, BillingSchemaVersion: videoruntime.BillingV1, BillingSnapshotID: 9007199254740994, ProviderScopeID: 17, CredentialSnapshotID: 19, UpstreamModel: "synthetic-upstream", CreateRequestID: "create-s2-1", NoChargeStatuses: "failed,cancelled"}
	due := r.ReceivedAt.Add(time.Second)
	r.RecoveryDueAt = &due
	return row, st, r
}
func querySettlementPending(t *testing.T) (task.Task, State, QueryReceipt) {
	row, st, r := querySettlementFixture(t, 0)
	pin := r.AttemptID
	st.SettlementReceiptID = &pin
	generated := r.ReceivedAt.Add(time.Second)
	r.GenerationAppliedAt = &generated
	r.SettlementKind = querySettlementUsage
	r.SettlementEvidenceSHA256 = querySettlementGoldens[0].sha
	return row, st, r
}

func TestQuerySettlementEvidenceIndependentLiterals(t *testing.T) {
	for i, golden := range querySettlementGoldens {
		t.Run(golden.name, func(t *testing.T) {
			row, st, r := querySettlementFixture(t, i)
			digest, err := settlementEvidenceDigest(row, st, r, golden.kind)
			if err != nil || hex.EncodeToString(digest[:]) != golden.sha {
				t.Fatal("independent outer SHA changed", err)
			}
			inner, err := queryEvidenceDigest(r)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(querySettlementCanonical(row, st, r, golden.kind, inner))
			if err != nil || string(raw) != golden.canonical {
				t.Fatal("nineteen-field literal bytes changed")
			}
			old, err := queryReceiptOldCanonical(t, r)
			if err != nil || string(old) != golden.receipt {
				t.Fatal("old21 evidence literal changed")
			}
			pin := r.AttemptID
			st.SettlementReceiptID = &pin
			at := r.ReceivedAt
			r.GenerationAppliedAt = &at
			r.SettlementKind = golden.kind
			r.SettlementEvidenceSHA256 = golden.sha
			if golden.kind == querySettlementNone {
				r.AppliedAt = &at
			}
			// Mutable financial/progress projections do not create a different source.
			row.CostUSD = decimal.NewFromInt(99)
			st.BillingState = "settled"
			st.SyncState = "fresh"
			r.ApplyErrorCode = "changed"
			r.RecoveryVersion = 9
			after, err := settlementEvidenceDigest(row, st, r, golden.kind)
			if err != nil || after != digest {
				t.Fatal("mutable projection or self digest entered canonical")
			}
			if golden.kind != querySettlementNone {
				if _, err := readQuerySettlementEvidence(row, st, r); err != nil {
					t.Fatal("valid pending evidence rejected", err)
				}
			}
		})
	}
}

func TestQuerySettlementEvidenceBindingsAndValidation(t *testing.T) {
	row, st, r := querySettlementPending(t)
	original, err := settlementEvidenceDigest(row, st, r, r.SettlementKind)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*task.Task, *State, *QueryReceipt){
		"org":            func(a *task.Task, b *State, c *QueryReceipt) { a.OrgID++; b.OrgID++; c.OrgID++ },
		"task":           func(a *task.Task, b *State, c *QueryReceipt) { a.TaskID += "x"; b.TaskID += "x"; c.TaskID += "x" },
		"attempt":        func(_ *task.Task, _ *State, c *QueryReceipt) { c.AttemptID += "x" },
		"snapshot":       func(_ *task.Task, b *State, _ *QueryReceipt) { b.BillingSnapshotID++ },
		"scope":          func(_ *task.Task, b *State, _ *QueryReceipt) { b.ProviderScopeID++ },
		"credential":     func(_ *task.Task, b *State, _ *QueryReceipt) { b.CredentialSnapshotID++ },
		"channel":        func(a *task.Task, _ *State, _ *QueryReceipt) { a.ChannelID++ },
		"public_model":   func(a *task.Task, _ *State, _ *QueryReceipt) { a.Model += "x" },
		"target":         func(a *task.Task, _ *State, _ *QueryReceipt) { a.UpstreamTaskID += "x" },
		"upstream_model": func(_ *task.Task, b *State, _ *QueryReceipt) { b.UpstreamModel += "x" },
		"request":        func(_ *task.Task, b *State, _ *QueryReceipt) { b.CreateRequestID += "x" },
		"nocharge":       func(_ *task.Task, b *State, _ *QueryReceipt) { b.NoChargeStatuses += "x" },
		"kind":           func(_ *task.Task, _ *State, c *QueryReceipt) { c.SettlementKind = querySettlementNone },
		"raw_evidence":   func(_ *task.Task, _ *State, c *QueryReceipt) { c.ProviderRequestID += "x" },
	} {
		t.Run(name, func(t *testing.T) {
			a, b, c := row, st, r
			change(&a, &b, &c)
			sum, err := settlementEvidenceDigest(a, b, c, c.SettlementKind)
			if err != nil || sum == original {
				t.Fatal("fixed binding omitted", err)
			}
		})
	}
	for name, change := range map[string]func(*task.Task, *State, *QueryReceipt){
		"bad_sha": func(_ *task.Task, _ *State, c *QueryReceipt) { c.SettlementEvidenceSHA256 = "" },
		"uppercase_sha": func(_ *task.Task, _ *State, c *QueryReceipt) {
			c.SettlementEvidenceSHA256 = strings.ToUpper(c.SettlementEvidenceSHA256)
		},
		"zero_snapshot": func(_ *task.Task, b *State, _ *QueryReceipt) { b.BillingSnapshotID = 0 },
		"invalid_utf8":  func(_ *task.Task, _ *State, c *QueryReceipt) { c.ProviderRequestID = string([]byte{255}) },
	} {
		t.Run(name, func(t *testing.T) {
			a, b, c := row, st, r
			change(&a, &b, &c)
			o, err := readQuerySettlementEvidence(a, b, c)
			if classifyQueryRecoveryCause(err) != queryCauseSettlementEvidence || !reflect.DeepEqual(o, queryObservation{}) {
				t.Fatal("invalid B evidence did not return zero typed rejection")
			}
		})
	}
	// A matching digest is not interpretation or financial permission.
	for name, raw := range map[string]string{"missing": `{"status":"succeeded"}`, "target": `{"task_id":"other","status":"succeeded","completion_tokens":1}`, "negative": `{"task_id":"up-s2-1","status":"succeeded","completion_tokens":-1}`, "error": `{"task_id":"up-s2-1","status":"succeeded","completion_tokens":1,"has_error":true}`, "queued": `{"task_id":"up-s2-1","status":"running","completion_tokens":1}`} {
		t.Run(name, func(t *testing.T) {
			c := r
			c.ObservationJSON = raw
			sum, err := settlementEvidenceDigest(row, st, c, c.SettlementKind)
			if err != nil {
				t.Fatal(err)
			}
			c.SettlementEvidenceSHA256 = hex.EncodeToString(sum[:])
			if _, err = readQuerySettlementEvidence(row, st, c); classifyQueryRecoveryCause(err) != queryCauseSettlementEvidence {
				t.Fatal("matching SHA authorized invalid observation")
			}
		})
	}
}

func TestQuerySettlementEvidenceClosedErrors(t *testing.T) {
	a, b, c := querySettlementPending(t)
	c.SettlementEvidenceSHA256 = "invalid"
	_, actual := readQuerySettlementEvidence(a, b, c)
	if classifyQueryRecoveryCause(actual) != queryCauseSettlementEvidence {
		t.Fatal("missing actual validator failure")
	}
	var typedNil *querySettlementEvidenceError
	for name, err := range map[string]error{"nil": nil, "typed_nil": typedNil, "sentinel": errQuerySettlementEvidenceInvalid, "kind": &querySettlementEvidenceError{kind: 99}, "wrapper": &queryUnknownWrapper{actual}, "fmt": fmt.Errorf("wrapped: %w", actual), "join": errors.Join(actual, actual), "ctx": safeRecoveryErrors(actual, context.Canceled), "sql": safeRecoveryErrors(actual, &mysql.MySQLError{Number: 1054}), "authority": safeRecoveryErrors(actual, authorityClockFailure(actual))} {
		t.Run(name, func(t *testing.T) {
			if classifyQueryRecoveryCause(err) != queryCauseTemporary {
				t.Fatal("unknown or mixed error became permanent")
			}
		})
	}
	if classifyQueryRecoveryCause(safeRecoveryErrors(actual, safeRecoveryErrors(actual))) != queryCauseSettlementEvidence {
		t.Fatal("known closed aggregate lost cause")
	}
}

func TestQuerySettlementNewPhaseExcludedFromV1Digest(t *testing.T) {
	r := queryReceiptDigestFixture(t)
	before, err := queryEvidenceDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(8, 0)
	r.GenerationAppliedAt = &at
	r.SettlementKind = "arbitrary-unknown"
	r.SettlementEvidenceSHA256 = "not-a-hash"
	after, err := queryEvidenceDigest(r)
	old, e := queryReceiptOldCanonical(t, r)
	if err != nil || e != nil || after != before || string(old) != queryReceiptLiteral {
		t.Fatal("S2 ORM fields contaminated V1 evidence")
	}
}
