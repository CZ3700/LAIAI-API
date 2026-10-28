package videoruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Literal bytes and hashes were independently fixed before the registry change.
// The V1 cases also keep the three previously published descriptor goldens.
func TestQueryApplicationDescriptorGoldens(t *testing.T) {
	cases := []struct {
		name                        string
		contract                    Contract
		descriptor, canonical, hash string
	}{
		{"V1/runtime", LegacyV1(), "runtime", `{"contract":{"runtime_version":"ark-official-legacy-v1","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"actions":["create","query","cancel","delete","delivery_refresh"],"execution_input_schema_version":1,"observation_schema_version":1,"delivery_schema_version":1}`, "77f6aa32417500ddd9e3e11a2a1036866cdda49237d86b7379ca67b92710f8bc"},
		{"V1/cancel_task", LegacyV1(), "cancel_task", `{"key":{"storage_protocol":"inbox-actions-v1","action":"cancel_task","contract":{"runtime_version":"ark-official-legacy-v1","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"execution_input_schema_version":2,"observation_schema_version":1,"delivery_schema_version":0},"input_reader":"video-action-input-v2","result_policy":"ark-delete-ack-v1"}`, "633b60e02549f2b2729ac4c81123a7976a2ebf985871fcc9025e0260332c4018"},
		{"V1/delete_task", LegacyV1(), "delete_task", `{"key":{"storage_protocol":"inbox-actions-v1","action":"delete_task","contract":{"runtime_version":"ark-official-legacy-v1","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"execution_input_schema_version":2,"observation_schema_version":1,"delivery_schema_version":0},"input_reader":"video-action-input-v2","result_policy":"ark-delete-ack-v1"}`, "0efc6174cf91c45fc91909621c1b92c462999d8991bfd9f597a8e5572e9269d4"},
		{"V2-proposal/runtime", GenerationSettlementV2(), "runtime", `{"contract":{"runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"actions":["create","query","cancel","delete","delivery_refresh"],"execution_input_schema_version":1,"observation_schema_version":1,"delivery_schema_version":1}`, "581017d370919f36c7fab3b085c5f55f2dfd5f810df25d4ab4d85db850324a89"},
		{"V2-proposal/cancel_task", GenerationSettlementV2(), "cancel_task", `{"key":{"storage_protocol":"inbox-actions-v1","action":"cancel_task","contract":{"runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"execution_input_schema_version":2,"observation_schema_version":1,"delivery_schema_version":0},"input_reader":"video-action-input-v2","result_policy":"ark-delete-ack-v1"}`, "023e1c01651b74a4515624a7926dca06e65441a3797f5240f5fd3e19172a5f96"},
		{"V2-proposal/delete_task", GenerationSettlementV2(), "delete_task", `{"key":{"storage_protocol":"inbox-actions-v1","action":"delete_task","contract":{"runtime_version":"ark-official-generation-settlement-v2","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"execution_input_schema_version":2,"observation_schema_version":1,"delivery_schema_version":0},"input_reader":"video-action-input-v2","result_policy":"ark-delete-ack-v1"}`, "b82d3db464fdc60b03d261cf275a593ff583b2ed7621a47032091a8b7ae9c0b9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sum := sha256.Sum256([]byte(tc.canonical))
			if hex.EncodeToString(sum[:]) != tc.hash {
				t.Fatal("invalid independent golden")
			}
			var got any
			var hash string
			if tc.descriptor == "runtime" {
				d, err := Resolve(tc.contract)
				if err != nil {
					t.Fatal(err)
				}
				hash = d.Hash
				got = struct {
					Contract                    Contract `json:"contract"`
					Actions                     []string `json:"actions"`
					ExecutionInputSchemaVersion uint32   `json:"execution_input_schema_version"`
					ObservationSchemaVersion    uint32   `json:"observation_schema_version"`
					DeliverySchemaVersion       uint32   `json:"delivery_schema_version"`
				}{d.Contract, append([]string(nil), d.Actions...), d.ExecutionInputSchemaVersion, d.ObservationSchemaVersion, d.DeliverySchemaVersion}
				d.Actions[0] = "caller-mutated"
				again, err := Resolve(tc.contract)
				if err != nil || again.Actions[0] != "create" || again.Hash != tc.hash {
					t.Fatal("registry aliases caller storage")
				}
			} else {
				key := ActionContractKey{Action: tc.descriptor, Contract: tc.contract, StorageProtocol: "inbox-actions-v1", ExecutionInputSchemaVersion: 2, ObservationSchemaVersion: 1, DeliverySchemaVersion: 0}
				d, err := ResolveActionContract(key)
				if err != nil {
					t.Fatal(err)
				}
				hash = d.Hash
				got = struct {
					Key          ActionContractKey `json:"key"`
					InputReader  string            `json:"input_reader"`
					ResultPolicy string            `json:"result_policy"`
				}{d.Key, d.InputReader, d.ResultPolicy}
			}
			raw, err := json.Marshal(got)
			if err != nil || string(raw) != tc.canonical || hash != tc.hash {
				t.Fatal("descriptor differs from independent literal")
			}
		})
	}
}

func TestQueryApplicationRequiresExactContractAndSchema(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract Contract
		mode     QueryApplication
	}{
		{"atomic_v1", LegacyV1(), QueryApplicationAtomicV1},
		{"generation_then_settlement_v2", GenerationSettlementV2(), QueryApplicationGenerationSettlementV2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, err := ResolveQueryApplication(tc.contract, 1)
			if err != nil || mode != tc.mode || string(mode) != tc.name {
				t.Fatal("wrong query policy")
			}
			for _, schema := range []uint32{0, 2, math.MaxUint32} {
				mode, err = ResolveQueryApplication(tc.contract, schema)
				if err != ErrUnsupported || mode != "" {
					t.Fatal("unsupported observation schema selected a policy")
				}
			}
			for field := 0; field < 4; field++ {
				initial := []string{tc.contract.RuntimeVersion, tc.contract.AdapterVersion, tc.contract.ProtocolContractVersion, tc.contract.BillingSchemaVersion}[field]
				for _, value := range []string{"", "latest", initial + " ", " " + initial, strings.ToUpper(initial), initial + "\x00", "private-input-sentinel"} {
					changed := tc.contract
					parts := []*string{&changed.RuntimeVersion, &changed.AdapterVersion, &changed.ProtocolContractVersion, &changed.BillingSchemaVersion}
					*parts[field] = value
					mode, err = ResolveQueryApplication(changed, 1)
					d, de := Resolve(changed)
					key := mutationContractKey(ActionCancelTask)
					key.Contract = changed
					a, ae := ResolveActionContract(key)
					if err != ErrUnsupported || mode != "" || de != ErrUnsupported || !reflect.DeepEqual(d, Descriptor{}) || ae != ErrUnsupported || a != (ActionDescriptor{}) {
						t.Fatal("non-exact contract accepted or returned partial result")
					}
				}
			}
		})
	}
	if mode, err := ResolveQueryApplication(Contract{}, 1); err != ErrUnsupported || mode != "" {
		t.Fatal("zero contract defaulted")
	}
	mixed := GenerationSettlementV2()
	mixed.AdapterVersion = RuntimeV1
	if mode, err := ResolveQueryApplication(mixed, 1); err != ErrUnsupported || mode != "" {
		t.Fatal("mixed contract defaulted")
	}
}

func TestQueryApplicationV2ActionFormatsStayClosed(t *testing.T) {
	base := mutationContractKey(ActionCancelTask)
	base.Contract = GenerationSettlementV2()
	reject := func(key ActionContractKey) {
		t.Helper()
		got, err := ResolveActionContract(key)
		if err != ErrUnsupported || got != (ActionDescriptor{}) {
			t.Fatal("unregistered V2 action format accepted")
		}
	}
	for _, action := range []string{"", "create_task", "query", "delivery_refresh", "cancel", "delete", "CANCEL_TASK", "cancel_task ", " cancel_task", "cancel_task\x00"} {
		key := base
		key.Action = action
		reject(key)
	}
	for _, protocol := range []string{"", "inbox-v1", "inbox-actions-v2", "INBOX-ACTIONS-V1", "inbox-actions-v1 ", " inbox-actions-v1", "inbox-actions-v1\x00"} {
		key := base
		key.StorageProtocol = protocol
		reject(key)
	}
	for _, schemas := range [][3]uint32{{0, 1, 0}, {1, 1, 0}, {3, 1, 0}, {2, 0, 0}, {2, 2, 0}, {2, 1, 1}, {2, 1, 2}, {math.MaxUint32, 1, 0}} {
		key := base
		key.ExecutionInputSchemaVersion, key.ObservationSchemaVersion, key.DeliverySchemaVersion = schemas[0], schemas[1], schemas[2]
		reject(key)
	}
}
