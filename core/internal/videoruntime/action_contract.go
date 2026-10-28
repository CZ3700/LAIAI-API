package videoruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	ActionStorageProtocolV1      = "inbox-actions-v1"
	ActionCancelTask             = "cancel_task"
	ActionDeleteTask             = "delete_task"
	ActionExecutionInputSchemaV2 = uint32(2)
)

// ActionContractKey fixes the interpretation of a stored action independently
// of the existing runtime Descriptor. No field has an implicit default.
type ActionContractKey struct {
	StorageProtocol             string   `json:"storage_protocol"`
	Action                      string   `json:"action"`
	Contract                    Contract `json:"contract"`
	ExecutionInputSchemaVersion uint32   `json:"execution_input_schema_version"`
	ObservationSchemaVersion    uint32   `json:"observation_schema_version"`
	DeliverySchemaVersion       uint32   `json:"delivery_schema_version"`
}

// ActionDescriptor names compiled input and result policies, not plugins or
// callbacks. Hash covers the fixed-order JSON descriptor excluding Hash.
type ActionDescriptor struct {
	Key          ActionContractKey `json:"key"`
	InputReader  string            `json:"input_reader"`
	ResultPolicy string            `json:"result_policy"`
	Hash         string            `json:"hash"`
}

// ResolveActionContract only interprets the two mutation formats delivered by
// A2a. Existing create contracts remain exclusively in Resolve; delivery and
// unknown formats must retain their evidence without being guessed or applied.
func ResolveActionContract(key ActionContractKey) (ActionDescriptor, error) {
	if key.StorageProtocol != ActionStorageProtocolV1 ||
		(key.Action != ActionCancelTask && key.Action != ActionDeleteTask) ||
		(key.Contract != LegacyV1() && key.Contract != GenerationSettlementV2()) || key.ExecutionInputSchemaVersion != ActionExecutionInputSchemaV2 ||
		key.ObservationSchemaVersion != ObservationSchemaV1 || key.DeliverySchemaVersion != 0 {
		return ActionDescriptor{}, ErrUnsupported
	}
	d := ActionDescriptor{Key: key, InputReader: "video-action-input-v2", ResultPolicy: "ark-delete-ack-v1"}
	canonical, _ := json.Marshal(struct {
		Key          ActionContractKey `json:"key"`
		InputReader  string            `json:"input_reader"`
		ResultPolicy string            `json:"result_policy"`
	}{d.Key, d.InputReader, d.ResultPolicy})
	hash := sha256.Sum256(canonical)
	d.Hash = hex.EncodeToString(hash[:])
	return d, nil
}
