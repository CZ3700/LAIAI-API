// Package videoruntime defines the immutable interpretation contracts supported
// by this binary. It has no database, network, credential, or accounting access.
package videoruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const (
	RuntimeV1              = "ark-official-legacy-v1"
	AdapterV1              = "ark-v3-v1"
	ProtocolV1             = "video-contract-v1"
	BillingV1              = "usd-decimal-v1"
	ObservationSchemaV1    = uint32(1)
	ExecutionInputSchemaV1 = uint32(1)
	DeliverySchemaV1       = uint32(1)
)

// ErrUnsupported is deliberately independent of the submitted version values.
// It is not evidence that a request was never sent or that funds may be released.
var ErrUnsupported = errors.New("runtime_contract_unsupported")

// Contract fixes semantic versions, not the source revision or image digest.
// Zero values are invalid: legacy compatibility is an explicit database
// migration/default for old writers, never a resolver fallback.
type Contract struct {
	RuntimeVersion          string `json:"runtime_version"`
	AdapterVersion          string `json:"adapter_version"`
	ProtocolContractVersion string `json:"protocol_contract_version"`
	BillingSchemaVersion    string `json:"billing_schema_version"`
}

// Descriptor identifies one compiled contract. Schema identities are separate
// from pricing/capability versions and the Invocation Store encryption format.
// Hash is SHA-256 of the fixed-order JSON descriptor without the Hash field.
// Resolve returns an independent Actions slice, never shared registry storage.
type Descriptor struct {
	Contract                    Contract `json:"contract"`
	Actions                     []string `json:"actions"`
	ExecutionInputSchemaVersion uint32   `json:"execution_input_schema_version"`
	ObservationSchemaVersion    uint32   `json:"observation_schema_version"`
	DeliverySchemaVersion       uint32   `json:"delivery_schema_version"`
	Hash                        string   `json:"hash"`
}

func LegacyV1() Contract {
	return Contract{
		RuntimeVersion: RuntimeV1, AdapterVersion: AdapterV1,
		ProtocolContractVersion: ProtocolV1, BillingSchemaVersion: BillingV1,
	}
}

// Resolve requires the exact four-part contract. There is no latest, partial
// match, alias, trimming, case folding, or dynamic registration path.
func Resolve(contract Contract) (Descriptor, error) {
	if contract != LegacyV1() {
		return Descriptor{}, ErrUnsupported
	}
	d := Descriptor{
		Contract: contract, Actions: []string{"create", "query", "cancel", "delete", "delivery_refresh"},
		ExecutionInputSchemaVersion: ExecutionInputSchemaV1,
		ObservationSchemaVersion:    ObservationSchemaV1,
		DeliverySchemaVersion:       DeliverySchemaV1,
	}
	// A struct fixes field order, and the action list has a fixed order. All
	// fields have JSON-safe scalar types, so encoding cannot fail.
	canonical, _ := json.Marshal(struct {
		Contract                    Contract `json:"contract"`
		Actions                     []string `json:"actions"`
		ExecutionInputSchemaVersion uint32   `json:"execution_input_schema_version"`
		ObservationSchemaVersion    uint32   `json:"observation_schema_version"`
		DeliverySchemaVersion       uint32   `json:"delivery_schema_version"`
	}{d.Contract, d.Actions, d.ExecutionInputSchemaVersion, d.ObservationSchemaVersion, d.DeliverySchemaVersion})
	digest := sha256.Sum256(canonical)
	d.Hash = hex.EncodeToString(digest[:])
	return d, nil
}

// ValidateBillingSchema protects accounting entry points which may run without
// the task worker (for example imported provider bill reconciliation).
func ValidateBillingSchema(version string) error {
	if version != BillingV1 {
		return ErrUnsupported
	}
	return nil
}
