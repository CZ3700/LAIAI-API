package video

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
)

func TestQueryGenerationRequiresTrustedObservation(t *testing.T) {
	for _, name := range []string{"positive", "zero", "missing", "nocharge", "http_error", "negative_usage", "negative_total", "has_error", "wrong_target", "wrong_model", "unknown_status", "unknown_outcome", "missing_time"} {
		t.Run(name, func(t *testing.T) {
			row, st, receipt := querySettlementFixture(t, 0)
			row.Status = "running"
			observation, err := decodeQueryObservation(receipt.ObservationJSON)
			must(t, err)
			wantKind, wantTrusted := querySettlementUsage, true
			switch name {
			case "zero":
				zero := int64(0)
				observation.CompletionTokens = &zero
			case "missing":
				observation.CompletionTokens = nil
				wantKind = querySettlementNone
			case "nocharge":
				observation.Status, observation.CompletionTokens = ark.StatusCancelled, nil
				wantKind = querySettlementProviderNoCharge
			case "http_error":
				receipt.HTTPStatus = 500
			case "negative_usage":
				n := int64(-1)
				observation.CompletionTokens = &n
			case "negative_total":
				n := int64(-1)
				observation.TotalTokens = &n
			case "has_error":
				observation.HasError = true
			case "wrong_target":
				observation.TaskID += " "
			case "wrong_model":
				observation.Model += " "
			case "unknown_status":
				observation.Status = "future-status"
			case "unknown_outcome":
				receipt.Outcome = "unknown"
			case "missing_time":
				receipt.ReceivedAt = time.Time{}
			}
			if name != "positive" && name != "zero" && name != "missing" && name != "nocharge" {
				wantKind, wantTrusted = querySettlementNone, false
			}
			raw, err := json.Marshal(observation)
			must(t, err)
			receipt.ObservationJSON = string(raw)
			_, transition, kind, trusted, err := queryGenerationEvidence(row, st, receipt)
			must(t, err)
			if kind != wantKind || trusted != wantTrusted {
				t.Fatal("untrusted evidence selected a financial source")
			}
			if !trusted && (!transition.NeedsReview || transition.Status != ark.StatusRunning) {
				t.Fatal("untrusted V2 observation advanced generation status")
			}
			if trusted && transition.Status != observation.Status {
				t.Fatal("trusted generation was not advanced")
			}
		})
	}
}

func TestQueryGenerationFinancialComparisonUsesOriginalPin(t *testing.T) {
	_, _, pin := querySettlementPending(t)
	base, err := decodeQueryObservation(pin.ObservationJSON)
	must(t, err)
	for _, name := range []string{"same", "different_usage", "missing_usage", "different_status", "different_kind"} {
		t.Run(name, func(t *testing.T) {
			current, kind := base, querySettlementUsage
			switch name {
			case "different_usage":
				n := *base.CompletionTokens + 1
				current.CompletionTokens = &n
			case "missing_usage":
				current.CompletionTokens = nil
			case "different_status":
				current.Status = ark.StatusFailed
			case "different_kind":
				kind = querySettlementProviderNoCharge
			}
			if queryGenerationSourcesAgree(kind, current, pin, base) != (name == "same") {
				t.Fatal("financial source comparison did not use original nullable usage")
			}
		})
	}
}
