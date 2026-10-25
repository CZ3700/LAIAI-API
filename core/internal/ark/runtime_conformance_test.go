package ark

import (
	"fmt"
	"net/http"
	"testing"
)

// These are synthetic protocol-conformance cases, not evidence that an Ark
// account or model has returned all three statuses in a paid invocation.
func TestRuntimeSubmitSuccessfulHTTPStatusConformance(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		for _, tc := range []struct {
			name, body string
			accepted   bool
		}{
			{"valid_id", `{"id":"cgt-runtime"}`, true},
			{"missing_id", `{}`, false},
			{"empty_id", `{"id":""}`, false},
			{"null_id", `{"id":null}`, false},
		} {
			t.Run(fmt.Sprintf("%d/%s", status, tc.name), func(t *testing.T) {
				result := ParseResponse(ActionSubmit, response(status, tc.body), 0)
				if result.Action != ActionSubmit || result.HTTPStatus != status || string(result.Body) != tc.body {
					t.Fatal("response identity or bounded evidence changed")
				}
				transition := ApplyObservation(StatusSubmitting, result)
				if tc.accepted {
					if result.Outcome != OutcomeAccepted || result.TaskID != "cgt-runtime" || result.Error != nil || transition.Status != StatusQueued || transition.NeedsReview {
						t.Fatal("successful HTTP status changed valid creation semantics")
					}
				} else if result.Outcome != OutcomeUnknown || result.TaskID != "" || result.Error == nil || transition.Status != StatusSubmissionUnknown || !transition.NeedsReview {
					t.Fatal("HTTP success without a task ID became confirmed creation or rejection")
				}
			})
		}
	}
}

func TestRuntimeQuerySuccessfulHTTPStatusPreservesUsage(t *testing.T) {
	zero, seven, eleven := int64(0), int64(7), int64(11)
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		for _, tc := range []struct {
			name, usage string
			completion  *int64
			total       *int64
		}{
			{"missing_usage", "", nil, nil},
			{"null_usage", `,"usage":null`, nil, nil},
			{"empty_usage", `,"usage":{}`, nil, nil},
			{"null_tokens", `,"usage":{"completion_tokens":null,"total_tokens":null}`, nil, nil},
			{"explicit_zero", `,"usage":{"completion_tokens":0,"total_tokens":0}`, &zero, &zero},
			{"total_without_completion", `,"usage":{"total_tokens":11}`, nil, &eleven},
			{"distinct_counts", `,"usage":{"completion_tokens":7,"total_tokens":11}`, &seven, &eleven},
		} {
			t.Run(fmt.Sprintf("%d/%s", status, tc.name), func(t *testing.T) {
				body := `{"id":"cgt-runtime","status":"succeeded"` + tc.usage + `}`
				result := ParseResponse(ActionQuery, response(status, body), 0)
				if result.Outcome != OutcomeAccepted || result.Error != nil || result.HTTPStatus != status || result.Observation == nil || result.Observation.Status != StatusSucceeded || string(result.Body) != body {
					t.Fatal("successful query status changed task observation or evidence")
				}
				for _, count := range []struct {
					name      string
					got, want *int64
				}{{"completion", result.Observation.Usage.CompletionTokens, tc.completion}, {"total", result.Observation.Usage.TotalTokens, tc.total}} {
					if (count.got == nil) != (count.want == nil) || count.got != nil && *count.got != *count.want {
						t.Fatalf("%s token presence or value changed", count.name)
					}
				}
				transition := ApplyObservation(StatusRunning, result)
				if transition.Status != StatusSucceeded || transition.NeedsReview || transition.UsagePending != (tc.completion == nil) {
					t.Fatal("missing completion usage became zero or total tokens substituted for completion")
				}
			})
		}
	}
}

func TestRuntimeQuerySuccessfulHTTPStatusKeepsUnknownAndSingleArtifacts(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		t.Run(fmt.Sprintf("%d/missing_id", status), func(t *testing.T) {
			result := ParseResponse(ActionQuery, response(status, `{"status":"succeeded","usage":{"completion_tokens":0}}`), 0)
			if result.Outcome != OutcomeUnknown || result.Observation != nil || result.Error == nil {
				t.Fatal("successful HTTP status authorized a query without task identity")
			}
		})
		t.Run(fmt.Sprintf("%d/unknown_status", status), func(t *testing.T) {
			result := ParseResponse(ActionQuery, response(status, `{"id":"cgt-runtime","status":"future-provider-state"}`), 0)
			if result.Observation == nil || result.Observation.RawStatus != "future-provider-state" || result.Observation.Status != "" {
				t.Fatal("unknown provider state was discarded or inferred")
			}
			transition := ApplyObservation(StatusRunning, result)
			if transition.Status != StatusRunning || transition.Changed || !transition.NeedsReview {
				t.Fatal("unknown provider state advanced or finalized the task")
			}
		})
		t.Run(fmt.Sprintf("%d/single_video_and_frame", status), func(t *testing.T) {
			result := ParseResponse(ActionQuery, response(status, `{"id":"cgt-runtime","status":"succeeded","content":{"video_url":"https://media.example/video.mp4","last_frame_url":"https://media.example/frame.png"}}`), 0)
			if result.Outcome != OutcomeAccepted || result.Observation == nil || result.Observation.VideoURL != "https://media.example/video.mp4" || result.Observation.LastFrameURL != "https://media.example/frame.png" {
				t.Fatal("supported single video and last-frame slots were lost")
			}
		})
		// The fixed contract has scalar artifact slots, not a multi-video API.
		// Do not silently choose one artifact from a provider's array shape.
		for _, field := range []string{"video_url", "last_frame_url"} {
			t.Run(fmt.Sprintf("%d/array_%s", status, field), func(t *testing.T) {
				body := fmt.Sprintf(`{"id":"cgt-runtime","status":"succeeded","content":{%q:["https://media.example/a.mp4","https://media.example/b.mp4"]}}`, field)
				result := ParseResponse(ActionQuery, response(status, body), 0)
				if result.Outcome != OutcomeUnknown || result.Observation != nil || result.Error == nil || string(result.Body) != body {
					t.Fatal("unsupported artifact array was guessed or its evidence discarded")
				}
			})
		}
	}
}
