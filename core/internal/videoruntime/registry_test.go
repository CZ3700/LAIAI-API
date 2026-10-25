package videoruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveExactContractAndStableDescriptor(t *testing.T) {
	d, err := Resolve(LegacyV1())
	if err != nil {
		t.Fatal(err)
	}
	if d.Contract != LegacyV1() || !reflect.DeepEqual(d.Actions, []string{"create", "query", "cancel", "delete", "delivery_refresh"}) || d.ExecutionInputSchemaVersion != 1 || d.ObservationSchemaVersion != 1 || d.DeliverySchemaVersion != 1 {
		t.Fatal("legacy descriptor does not cover the execution and delivery contract")
	}
	// This literal pins the published descriptor's byte representation. Changes
	// to its semantic fields or order require an explicit contract review.
	const canonical = `{"contract":{"runtime_version":"ark-official-legacy-v1","adapter_version":"ark-v3-v1","protocol_contract_version":"video-contract-v1","billing_schema_version":"usd-decimal-v1"},"actions":["create","query","cancel","delete","delivery_refresh"],"execution_input_schema_version":1,"observation_schema_version":1,"delivery_schema_version":1}`
	want := sha256.Sum256([]byte(canonical))
	if d.Hash != hex.EncodeToString(want[:]) {
		t.Fatal("descriptor hash changed without updating the contract")
	}
	d.Actions[0] = "arbitrary_plugin"
	d.Contract.RuntimeVersion = "latest"
	d.Hash = "modified"
	again, err := Resolve(LegacyV1())
	if err != nil || again.Actions[0] != "create" || again.Hash != hex.EncodeToString(want[:]) || again.Contract != LegacyV1() {
		t.Fatal("a caller mutated the compiled registry")
	}
}

func TestResolveRejectsEmptyUnknownAndMixedContracts(t *testing.T) {
	cases := []Contract{{}}
	for field, registered := range []string{RuntimeV1, AdapterV1, ProtocolV1, BillingV1} {
		for _, value := range []string{"", "latest", "v2", "private-input-sentinel", " " + registered, registered + " ", strings.ToUpper(registered), registered + "\x00"} {
			c := LegacyV1()
			switch field {
			case 0:
				c.RuntimeVersion = value
			case 1:
				c.AdapterVersion = value
			case 2:
				c.ProtocolContractVersion = value
			case 3:
				c.BillingSchemaVersion = value
			}
			cases = append(cases, c)
		}
	}
	mixed := LegacyV1()
	mixed.AdapterVersion, mixed.ProtocolContractVersion = mixed.ProtocolContractVersion, mixed.AdapterVersion
	cases = append(cases, mixed)
	for i, contract := range cases {
		d, err := Resolve(contract)
		if !errors.Is(err, ErrUnsupported) || err.Error() != "runtime_contract_unsupported" || !reflect.DeepEqual(d, Descriptor{}) {
			t.Fatalf("case %d did not reject without fallback or input disclosure", i)
		}
	}
}

func TestValidateBillingSchemaRequiresExplicitVersion(t *testing.T) {
	if err := ValidateBillingSchema(BillingV1); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "latest", "usd-decimal-v2", RuntimeV1, " " + BillingV1, strings.ToUpper(BillingV1)} {
		if err := ValidateBillingSchema(value); !errors.Is(err, ErrUnsupported) {
			t.Fatal("billing accepted an empty or unregistered schema")
		}
	}
}
