package video

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestExecutionInputHandoffProtocolContract(t *testing.T) {
	base := `{"protocol":"execution-clock-handoff-v1","run_id":"0123456789abcdef","case_id":"N02","instance":"writer-old","cmd_id":1,"command":"run_once","barrier":"receipt_committed"}`
	if c, err := decodeExecutionHandoffCommand([]byte(base)); err != nil || c.ID != 1 || c.Barrier != "receipt_committed" {
		t.Fatal("valid real-commit command rejected", err)
	}
	invalid := map[string]string{
		"duplicate":         strings.Replace(base, `"cmd_id":1`, `"cmd_id":1,"cmd_id":2`, 1),
		"unknown":           strings.Replace(base, `"cmd_id":1`, `"cmd_id":1,"sql":"UPDATE tasks"`, 1),
		"null":              strings.Replace(base, `"barrier":"receipt_committed"`, `"barrier":null`, 1),
		"fraction":          strings.Replace(base, `"cmd_id":1`, `"cmd_id":1.5`, 1),
		"negative":          strings.Replace(base, `"cmd_id":1`, `"cmd_id":-1`, 1),
		"zero":              strings.Replace(base, `"cmd_id":1`, `"cmd_id":0`, 1),
		"unknown_protocol":  strings.Replace(base, executionHandoffProtocol, "simulated-v1", 1),
		"unknown_case":      strings.Replace(base, `"N02"`, `"N99"`, 1),
		"path":              strings.Replace(base, `"writer-old"`, `"../writer"`, 1),
		"bad_barrier":       strings.Replace(base, "receipt_committed", "before_fake_commit", 1),
		"trailing":          base + `{}`,
		"oversize":          base + strings.Repeat(" ", 4096),
		"run_owner":         strings.Replace(base, `"cmd_id":1`, `"cmd_id":1,"owner_cmd_id":1`, 1),
		"arbitrary_command": strings.Replace(base, "run_once", "apply_private_reducer", 1),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeExecutionHandoffCommand([]byte(raw)); err == nil {
				t.Fatal("unsafe control accepted")
			}
		})
	}
	for _, command := range []string{"seed", "action", "inspect", "read_action", "purge", "stop", "clock", "cancel_active", "unknown"} {
		t.Run(command, func(t *testing.T) {
			c := executionHandoffCommand{Protocol: executionHandoffProtocol, Run: "0123456789abcdef", Case: "N02", Instance: "writer-old", ID: 2, Command: command}
			switch command {
			case "clock":
				x := int64(-86370000)
				c.ClockOffsetMS = &x
			case "cancel_active":
				c.Owner = 1
			case "unknown":
				c.Fault = "schema"
			}
			raw, _ := json.Marshal(c)
			if _, err := decodeExecutionHandoffCommand(raw); err != nil {
				t.Fatal("valid command rejected", err)
			}
			if command == "clock" {
				x := int64(-86400001)
				c.ClockOffsetMS = &x
			} else {
				c.Barrier = "receipt_committed"
			}
			raw, _ = json.Marshal(c)
			if _, err := decodeExecutionHandoffCommand(raw); err == nil {
				t.Fatal("invalid command fields accepted")
			}
		})
	}
}

func TestExecutionInputHandoffRequiresRealBuildIdentity(t *testing.T) {
	old := executionHandoffCompiledSource
	defer func() { executionHandoffCompiledSource = old }()
	executionHandoffCompiledSource = executionHandoffOldSource
	values := map[string]string{"B1B_PROTOCOL": executionHandoffProtocol, "B1B_RUN_ID": "0123456789abcdef", "B1B_CASE_ID": "N01", "B1B_INSTANCE": "writer-old", "B1B_ROLE": "old", "B1B_SOURCE_SHA": executionHandoffOldSource, "B1B_CASE_DIR": t.TempDir(), "B1B_MOCK_ORIGIN": "http://127.0.0.1:12345", "B1B_UPSTREAM_ID": "b1b-synthetic-task", "B1B_PROVIDER_TOKEN": "synthetic-credential-not-a-real-secret"}
	for k, v := range values {
		t.Setenv(k, v)
	}
	if _, err := executionHandoffEnvironment(); err != nil {
		t.Fatal("fixed-source control rejected", err)
	}
	for _, field := range []string{"B1B_SOURCE_SHA", "B1B_ROLE", "B1B_MOCK_ORIGIN", "B1B_CASE_DIR"} {
		t.Run(field, func(t *testing.T) {
			previous := os.Getenv(field)
			defer os.Setenv(field, previous)
			value := "other"
			if field == "B1B_SOURCE_SHA" {
				value = strings.Repeat("f", 40)
			}
			if field == "B1B_ROLE" {
				value = "new"
			}
			if field == "B1B_MOCK_ORIGIN" {
				value = "http://example.com:12345"
			}
			if err := os.Setenv(field, value); err != nil {
				t.Fatal(err)
			}
			if _, err := executionHandoffEnvironment(); err == nil {
				t.Fatal("unmatched source or unsafe endpoint accepted")
			}
		})
	}
	t.Setenv("B1B_UNDECLARED", "1")
	if _, err := executionHandoffEnvironment(); err == nil {
		t.Fatal("undeclared control variable accepted")
	}
}
