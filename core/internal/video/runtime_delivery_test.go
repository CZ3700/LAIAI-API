package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

func TestRuntimeDeliveryUnknownNeverUsesStoreOrProvider(t *testing.T) {
	// No DB, encryption store, or HTTP client is installed. Unsupported
	// interpretation must be rejected before reaching any of them.
	s := &Service{}
	st := State{RuntimeVersion: "future-runtime", ResultPayloadRef: "protected-capsule"}
	row := task.Task{TaskID: "task_history", Status: "succeeded"}
	_, err := s.prepareDelivery(1, "attempt", st, &ark.Observation{Status: ark.StatusSucceeded, VideoURL: testDeliveryURL}, "", time.Now())
	if !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("unknown producer contract prepared a delivery", err)
	}
	if _, err = s.readDelivery(context.Background(), row, st, "reader"); !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("unknown task contract was decoded", err)
	}
	if err = s.refreshDelivery(context.Background(), row, st); !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("unknown task contract reached refresh", err)
	}
	v, err := s.deliveryView(context.Background(), row, st, "reader", true)
	if err != nil || v.ID != row.TaskID || v.Status != row.Status || v.ArtifactStatus != "runtime_contract_unsupported" || v.Content != nil || v.ContentURL != "" {
		t.Fatal("unknown contract hid persisted task history or exposed a link", err)
	}
}

func TestMySQLRuntimeDeliveryHistoryRemainsReadable(t *testing.T) {
	for _, mode := range []string{"unknown_runtime", "empty_protocol", "unknown_snapshot_schema", "unknown_capsule_schema"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			deliverySuccess(f)
			v := f.create(t, "runtime-delivery-history")
			f.dispatch(t, v.ID)
			must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
			var state State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
			if state.ResultPayloadRef == "" {
				t.Fatal("test requires a completed delivery capsule")
			}
			switch mode {
			case "unknown_runtime":
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
			case "empty_protocol":
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("protocol_contract_version", "").Error)
			case "unknown_snapshot_schema":
				must(t, f.s.DB.Model(&videobilling.Snapshot{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("billing_schema_version", "future-billing").Error)
			case "unknown_capsule_schema":
				// A future producer may legitimately encrypt another capsule
				// format. It must not be interpreted as today's JSON DTO.
				payload, err := f.s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: f.key.OrgID, RequestID: f.requestID("future-delivery"), Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeDelivery, SchemaVersion: 2, Body: []byte(`{"video_url":"` + testDeliveryURL + `"}`), ExpiresAt: f.now.Add(time.Hour), CaptureEnabled: true})
				must(t, err)
				var ref string
				must(t, f.s.DB.Transaction(func(tx *gorm.DB) error {
					var err error
					ref, err = f.s.Evidence.StageTx(tx, payload)
					return err
				}))
				must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("result_payload_ref", ref).Error)
			}
			before := mutationRuntimeSnapshot(t, f, v.ID)
			calls := f.gets.Load()
			router := deliveryRouter(f)
			w := deliveryRequest(f, router, "/v1/video/generations/"+v.ID)
			var got officialTaskQueryResponse
			must(t, json.Unmarshal(w.Body.Bytes(), &got))
			internal, err := f.s.Get(f.key.OrgID, v.ID)
			must(t, err)
			if w.Code != 200 || got.ID != v.ID || got.Status != "succeeded" || got.Content != nil ||
				internal.BillingState != "settled" || internal.ArtifactStatus != "runtime_contract_unsupported" || internal.ContentURL != "" || internal.Content != nil ||
				strings.Contains(w.Body.String(), "billing_state") || strings.Contains(w.Body.String(), "artifact_status") {
				t.Fatal("unsupported delivery failed to retain the persisted task projection", w.Code)
			}
			w = deliveryRequest(f, router, "/v1/video/generations")
			if w.Code != 200 || !strings.Contains(w.Body.String(), v.ID) || strings.Contains(w.Body.String(), "cdn.example.com") {
				t.Fatal("unsupported delivery broke history listing or leaked its URL", w.Code)
			}
			w = deliveryRequest(f, router, "/v1/video/generations/"+v.ID+"/content")
			if w.Code != 409 || !strings.Contains(w.Body.String(), "runtime_contract_unsupported") || strings.Contains(w.Body.String(), "cdn.example.com") {
				t.Fatal("content endpoint guessed an unknown format", w.Code)
			}
			if f.gets.Load() != calls || mutationRuntimeSnapshot(t, f, v.ID) != before {
				t.Fatal("unknown delivery refreshed upstream or altered task/accounting state")
			}
		})
	}
}

func TestMySQLRuntimeDeliveryRefreshRechecksAfterCredentialRead(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "runtime-delivery-presend")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("result_payload_ref", "").Error)
	makeFixtureTaskRateDue(t, f, v.ID, "query")
	row, state, err := loadTask(f.s.DB, f.key.OrgID, v.ID, false)
	must(t, err)
	calls := f.gets.Load()
	holdBefore := f.hold(t, v.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	entered, resume := pauseVideoCredentialRead(t, f, cancel, stopped)
	done := make(chan error, 1)
	go func() {
		defer close(stopped)
		done <- f.s.refreshDelivery(ctx, row, state)
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("refresh did not reach credential boundary", err)
	case <-ctx.Done():
		t.Fatal("refresh did not reach credential boundary")
	}
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("adapter_version", "future-adapter").Error)
	resume()
	select {
	case err = <-done:
		if !errors.Is(err, videoruntime.ErrUnsupported) {
			t.Fatal("pre-HTTP refresh guard missed contract replacement", err)
		}
	case <-ctx.Done():
		t.Fatal("refresh did not finish")
	}
	holdAfter := f.hold(t, v.ID)
	var responses int64
	must(t, f.s.DB.Model(&Attempt{}).Where("org_id = ? AND task_id = ? AND phase = ?", f.key.OrgID, v.ID, "delivery_query_response").Count(&responses).Error)
	if f.gets.Load() != calls || responses != 0 || holdAfter.Status != holdBefore.Status || !holdAfter.CapturedAmount.Equal(holdBefore.CapturedAmount) {
		t.Fatal("refresh dispatched or changed accounting after contract rejection")
	}
}

func TestMySQLRuntimeDeliveryResponseRetainedWhenApplicationBlocked(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "runtime-delivery-response")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("result_payload_ref", "").Error)
	makeFixtureTaskRateDue(t, f, v.ID, "query")
	row, state, err := loadTask(f.s.DB, f.key.OrgID, v.ID, false)
	must(t, err)
	holdBefore := f.hold(t, v.ID)
	var calls atomic.Int32
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatal("delivery refresh made a mutation request")
		}
		calls.Add(1)
		must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("runtime_version", "future-runtime").Error)
		return reply(200, `{"id":"official_delivery","status":"succeeded","content":{"video_url":"`+testDeliveryURL+`"}}`), nil
	}
	err = f.s.refreshDelivery(context.Background(), row, state)
	if !errors.Is(err, videoruntime.ErrUnsupported) {
		t.Fatal("delivery response was applied with a replaced contract", err)
	}
	var response Attempt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ? AND phase = ?", f.key.OrgID, v.ID, "delivery_query_response").First(&response).Error)
	var payload invocation.VideoPayload
	must(t, f.s.DB.Where("org_id = ? AND payload_id = ?", f.key.OrgID, response.EvidenceRef).First(&payload).Error)
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&state).Error)
	holdAfter := f.hold(t, v.ID)
	if response.EvidenceRef == "" || response.HTTPStatus != http.StatusOK || payload.SchemaVersion != 1 || payload.CaptureStatus != invocation.VideoCaptureStaged || state.ResultPayloadRef != "" || state.BillingState != "settled" || calls.Load() != 1 || holdAfter.Status != holdBefore.Status || !holdAfter.CapturedAmount.Equal(holdBefore.CapturedAmount) {
		t.Fatal("blocked delivery application lost its response evidence or changed accounting")
	}
}
