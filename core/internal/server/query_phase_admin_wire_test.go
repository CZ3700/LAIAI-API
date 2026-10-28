package server

// Staging only. This common file must compile in the fixed N archive as well
// as C/P. New production members belong only in the separate P adapter.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
)

const phaseProtocol = "query-phase-handoff-v1"
const phaseOldSource = "a8d86cd0348e97e87e376bb267e9ddd1aa33ddc6"
const phaseEventPrefix = "QUERY_PHASE_HANDOFF_EVENT "

// Both values are injected by the reviewed compiler, not selected by env.
var queryPhaseCompiledSource, queryPhaseCompiledRole string

var errPhaseHarness = errors.New("isolated query phase handoff failed")

type phaseSettlementConsumer interface{ ProcessSettlementOnce(context.Context) error }
type phaseEvidenceConsumer interface{ ProcessPersistedEvidenceOnce(context.Context) error }

type phaseCommand struct {
	Protocol string `json:"protocol"`
	Run      string `json:"run_id"`
	Case     string `json:"case_id"`
	Instance string `json:"instance"`
	ID       string `json:"cmd_id"`
	Command  string `json:"command"`
	Task     string `json:"task,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Request  string `json:"request,omitempty"`
	Lane     string `json:"lane,omitempty"`
	Action   string `json:"action,omitempty"`
	Target   string `json:"target,omitempty"`
	Label    string `json:"label,omitempty"`
	Boundary string `json:"boundary,omitempty"`
	Winner   string `json:"winner,omitempty"`
}

// This file is PRIVATE, never a safe-export candidate. Video is its sole
// writer. Server reads and validates it; it never manufactures task identity.
type phaseSeed struct {
	Protocol    string                   `json:"protocol"`
	Run         string                   `json:"run_id"`
	Case        string                   `json:"case_id"`
	Profile     string                   `json:"profile"`
	OrgID       string                   `json:"org_id"`
	KeyID       string                   `json:"key_id"`
	UserID      string                   `json:"user_id"`
	PlainAPIKey string                   `json:"plain_api_key"`
	ScopeID     string                   `json:"provider_scope_id"`
	ChannelID   string                   `json:"channel_id"`
	Actors      [3]string                `json:"admin_actor_ids"`
	Tasks       map[string]phaseSeedTask `json:"tasks"`
}
type phaseSeedTask struct {
	TaskID          string `json:"task_id"`
	OperationID     string `json:"create_operation_id"`
	CreateRequestID string `json:"create_request_id"`
	UpstreamID      string `json:"upstream_task_id"`
	QueryAttemptID  string `json:"query_attempt_id"`
	BodySHA         string `json:"request_body_sha256"`
	ProviderBodySHA string `json:"provider_request_body_sha256"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type phaseTableProof struct {
	SHA  string `json:"sha256"`
	Rows int    `json:"row_count"`
}
type phaseHTTPCounts struct {
	POST            int `json:"post"`
	GET             int `json:"get"`
	DELETE          int `json:"delete"`
	Other           int `json:"other"`
	CredentialReads int `json:"credential_reads"`
}

// Proof details are explicit at the physical SQL boundary. Amounts and IDs
// are strings. Physical rows are separately hashed and never printed in logs.
type phaseSnapshot struct {
	DatabaseUTC   string                     `json:"database_utc"`
	SchemaVersion string                     `json:"schema_version"`
	Tables        map[string]phaseTableProof `json:"tables"`
	FinancialSHA  string                     `json:"financial_sha256"`
	Tasks         map[string]map[string]any  `json:"tasks"`
	Money         map[string]string          `json:"money"`
	Network       phaseHTTPCounts            `json:"network"`
}

func phaseCaseValid(value string) bool {
	switch value {
	case "F01_inbox", "F01_legacy", "F02", "F03_create_pending", "F03_generation_pending", "F03_settlement_pending", "F03_settled_delivery", "F04_inbox", "F04_legacy", "F05_delivery", "F05_admin_first", "F05_settlement_first", "F05_admin_adjust_delete_then_fresh_B", "F06":
		return true
	}
	return false
}
func phaseHandleValid(value string) bool {
	switch value {
	case "primary", "old_before", "old_after", "c_default", "p_off", "p_on", "legacy":
		return true
	}
	return false
}
func phaseID(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
		return 0, errPhaseHarness
	}
	return n, nil
}
func phaseStrictObject(raw []byte, destination any) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nil, errPhaseHarness
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errPhaseHarness
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, e := d.Token()
		k, ok := token.(string)
		if e != nil || !ok || fields[k] != nil {
			return nil, errPhaseHarness
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, errPhaseHarness
		}
		fields[k] = v
	}
	if last, e := d.Token(); e != nil || last != json.Delim('}') {
		return nil, errPhaseHarness
	}
	if _, e := d.Token(); e != io.EOF {
		return nil, errPhaseHarness
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(destination) != nil {
		return nil, errPhaseHarness
	}
	return fields, nil
}
func decodePhaseCommand(raw []byte) (phaseCommand, error) {
	var c phaseCommand
	fields, e := phaseStrictObject(raw, &c)
	if e != nil {
		return c, e
	}
	allowed := map[string]bool{}
	for _, k := range []string{"protocol", "run_id", "case_id", "instance", "cmd_id", "command"} {
		allowed[k] = true
		if fields[k] == nil {
			return c, errPhaseHarness
		}
	}
	if c.Protocol != phaseProtocol || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(c.Run) || !phaseCaseValid(c.Case) || !regexp.MustCompile(`^(N|C|P|D)-[a-z0-9-]{1,32}$`).MatchString(c.Instance) {
		return c, errPhaseHarness
	}
	if _, e = phaseID(c.ID); e != nil {
		return c, e
	}
	require := func(names ...string) bool {
		for _, k := range names {
			allowed[k] = true
			if fields[k] == nil {
				return false
			}
		}
		return true
	}
	switch c.Command {
	case "seed_config":
		if !require("profile") || (c.Profile != "inbox" && c.Profile != "legacy") {
			return c, errPhaseHarness
		}
	case "create":
		if !require("task", "request") || (c.Request != "new" && c.Request != "same_body" && c.Request != "changed_body") {
			return c, errPhaseHarness
		}
	case "cycle":
		if !require("lane") || (c.Lane != "dispatch" && c.Lane != "evidence" && c.Lane != "settlement") {
			return c, errPhaseHarness
		}
	case "api":
		if !require("task", "action") {
			return c, errPhaseHarness
		}
		switch c.Action {
		case "get", "list", "content", "billing", "events", "operation", "cancel", "delete":
		default:
			return c, errPhaseHarness
		}
	case "direct":
		if !require("task", "target") {
			return c, errPhaseHarness
		}
		switch c.Target {
		case "create_receipt", "query_receipt", "mutation_receipt", "delivery_refresh":
		case "purge_history":
			if c.Case != "F05_delivery" || c.Task != "primary" {
				return c, errPhaseHarness
			}
		default:
			return c, errPhaseHarness
		}
	case "inspect":
		if !require("label") || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(c.Label) {
			return c, errPhaseHarness
		}
	case "barrier":
		if !require("task", "action", "boundary") || (c.Action != "arm" && c.Action != "release") || (c.Boundary != "receipt_committed_before_G" && c.Boundary != "G_committed_before_next_command") {
			return c, errPhaseHarness
		}
	case "admin":
		if !require("task", "action") || (c.Action != "prepare_initial_100" && c.Action != "settle_100" && c.Action != "adjust_90") {
			return c, errPhaseHarness
		}
	case "race_admin_B":
		if !require("task", "winner") || (c.Winner != "admin" && c.Winner != "settlement") {
			return c, errPhaseHarness
		}
	case "stop":
	default:
		return c, errPhaseHarness
	}
	if c.Task != "" && !phaseHandleValid(c.Task) {
		return c, errPhaseHarness
	}
	for k := range fields {
		if !allowed[k] {
			return c, errPhaseHarness
		}
	}
	return c, nil
}
