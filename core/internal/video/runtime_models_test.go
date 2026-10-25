package video

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestRuntimeModelFieldsDoNotDefaultEmptyValues(t *testing.T) {
	for _, model := range []any{State{}, Operation{}, QueryReceipt{}} {
		s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"RuntimeVersion", "AdapterVersion", "ProtocolContractVersion"} {
			if f := s.FieldsByName[field]; f == nil || f.HasDefaultValue {
				t.Fatalf("%s.%s must preserve explicit empty values for fail-closed checks", s.Name, field)
			}
		}
		for _, field := range []string{"BillingSchemaVersion", "ObservationSchemaVersion"} {
			if f := s.FieldsByName[field]; f != nil && f.HasDefaultValue {
				t.Fatalf("%s.%s must not silently inherit a supported schema", s.Name, field)
			}
		}
	}
}

func TestOperationDoesNotExposeInternalRuntimeIdentity(t *testing.T) {
	raw, err := json.Marshal(Operation{ID: "op_test", RuntimeVersion: "runtime-private", AdapterVersion: "adapter-private", ProtocolContractVersion: "protocol-private"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "Version") || strings.Contains(string(raw), "runtime_version") {
		t.Fatal("operation serialized internal contract identity")
	}
}
