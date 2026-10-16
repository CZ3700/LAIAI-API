package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskFailureNotificationOnlyPromisesConfirmedRelease(t *testing.T) {
	for _, state := range []string{"pending_usage", "reserved", "settled", "adjusted", "released", ""} {
		raw, _ := json.Marshal(map[string]string{"task_id": "task_video", "billing_state": state})
		_, body := emailTemplate(&Outbox{Event: EventTaskFailed, Payload: string(raw)})
		if strings.Contains(body, "fully released") != (state == "released") {
			t.Fatalf("state %q misrepresents funds: %s", state, body)
		}
		if strings.Contains(body, "model )") || strings.Contains(body, "Cost: $") {
			t.Fatalf("missing data rendered as amount: %s", body)
		}
	}
}

func TestCompletedVideoNotificationDoesNotInventPrice(t *testing.T) {
	_, body := emailTemplate(&Outbox{Event: EventTaskCompleted, Payload: `{"task_id":"task_video","billing_state":"pending_usage"}`})
	if strings.Contains(body, "Cost:") || !strings.Contains(body, "awaiting verified usage") {
		t.Fatal(body)
	}
	_, body = emailTemplate(&Outbox{Event: EventTaskCompleted, Payload: `{"task_id":"task_video","model":"seedance-2.0","cost_usd":"0.0123","billing_state":"settled"}`})
	if !strings.Contains(body, "Cost: $0.0123") || !strings.Contains(body, "Model: seedance-2.0") {
		t.Fatal(body)
	}
}
