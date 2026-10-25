package videoruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mutationContractKey(action string) ActionContractKey {
	return ActionContractKey{StorageProtocol: ActionStorageProtocolV1, Action: action, Contract: LegacyV1(),
		ExecutionInputSchemaVersion: 2, ObservationSchemaVersion: 1, DeliverySchemaVersion: 0}
}

func TestActionContractStableIndependentDescriptor(t *testing.T) {
	legacy, err := Resolve(LegacyV1())
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, action := range []string{ActionCancelTask, ActionDeleteTask} {
		key := mutationContractKey(action)
		d, err := ResolveActionContract(key)
		if err != nil || d.Key != key || d.InputReader != "video-action-input-v2" || d.ResultPolicy != "ark-delete-ack-v1" {
			t.Fatal("wrong fixed action contract", err)
		}
		canonical := `{"key":{"storage_protocol":"inbox-actions-v1","action":"` + action + `","contract":{"runtime_version":"ark-official-legacy-v1","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"execution_input_schema_version":2,"observation_schema_version":1,"delivery_schema_version":0},"input_reader":"video-action-input-v2","result_policy":"ark-delete-ack-v1"}`
		want := sha256.Sum256([]byte(canonical))
		if d.Hash != hex.EncodeToString(want[:]) || d.Hash == legacy.Hash {
			t.Fatal("published action hash changed or aliases legacy runtime hash")
		}
		hashes = append(hashes, d.Hash)
		d.Key.Action, d.InputReader, d.Hash = "changed", "changed", "changed"
		again, err := ResolveActionContract(key)
		if err != nil || again.Key != key || again.Hash != hex.EncodeToString(want[:]) {
			t.Fatal("caller changed registry state")
		}
	}
	if hashes[0] == hashes[1] {
		t.Fatal("action is absent from descriptor identity")
	}
	after, err := Resolve(LegacyV1())
	if err != nil || !reflect.DeepEqual(after, legacy) {
		t.Fatal("action resolution mutated the legacy descriptor")
	}
}

func TestActionContractRejectsEveryUnregisteredCombination(t *testing.T) {
	cases := []ActionContractKey{{}}
	base := mutationContractKey(ActionCancelTask)
	for _, value := range []string{"", "inbox-v1", "inbox-actions-v2", "private-filter-sentinel", "INBOX-ACTIONS-V1", "inbox-actions-v1 ", " inbox-actions-v1", "inbox-actions-v1\x00"} {
		key := base
		key.StorageProtocol = value
		cases = append(cases, key)
	}
	for _, value := range []string{"", "create_task", "delivery_refresh", "cancel", "delete", "CANCEL_TASK", "cancel_task ", " cancel_task", "cancel_task\x00"} {
		key := base
		key.Action = value
		cases = append(cases, key)
	}
	for field := 0; field < 4; field++ {
		for _, value := range []string{"", "latest", "unknown-v2", "private-filter-sentinel"} {
			key := base
			fields := []*string{&key.Contract.RuntimeVersion, &key.Contract.AdapterVersion, &key.Contract.ProtocolContractVersion, &key.Contract.BillingSchemaVersion}
			*fields[field] = value
			cases = append(cases, key)
		}
		for _, mode := range []string{"upper", "space", "leading", "nul"} {
			key := base
			fields := []*string{&key.Contract.RuntimeVersion, &key.Contract.AdapterVersion, &key.Contract.ProtocolContractVersion, &key.Contract.BillingSchemaVersion}
			value := *fields[field]
			switch mode {
			case "upper":
				value = strings.ToUpper(value)
			case "space":
				value += " "
			case "leading":
				value = " " + value
			case "nul":
				value += "\x00"
			}
			*fields[field] = value
			cases = append(cases, key)
		}
	}
	for _, values := range [][3]uint32{{0, 1, 0}, {1, 1, 0}, {3, 1, 0}, {2, 0, 0}, {2, 2, 0}, {2, 1, 1}, {2, 1, 2}} {
		key := base
		key.ExecutionInputSchemaVersion, key.ObservationSchemaVersion, key.DeliverySchemaVersion = values[0], values[1], values[2]
		cases = append(cases, key)
	}
	for i, key := range cases {
		d, err := ResolveActionContract(key)
		if !errors.Is(err, ErrUnsupported) || err.Error() != "runtime_contract_unsupported" || d != (ActionDescriptor{}) {
			t.Fatalf("case %d accepted unknown format or disclosed input", i)
		}
	}
}
