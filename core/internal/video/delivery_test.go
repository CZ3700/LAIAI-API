package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"gorm.io/gorm"
)

const testDeliveryURL = "https://cdn.example.com/result.mp4?token=a%2Bb&signature=secret%2Fvalue"

func deliverySuccess(f *fixture) {
	f.response = func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			return reply(200, `{"id":"official_delivery"}`), nil
		}
		return reply(200, `{"id":"official_delivery","status":"succeeded","usage":{"completion_tokens":100},"content":{"video_url":"`+testDeliveryURL+`","last_frame_url":"https://cdn.example.com/frame.jpg?token=frame"}}`), nil
	}
}

func deliveryRouter(f *fixture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/video/generations", f.s.Middleware(), f.s.HandleList)
	r.GET("/v1/video/generations/:id", f.s.Middleware(), f.s.HandleGet)
	r.GET("/v1/video/generations/:id/content", f.s.Middleware(), f.s.HandleContent)
	return r
}

func deliveryRequest(f *fixture, r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+f.plain)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMySQLDeliveryReadyWithoutStorageAndHistory(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	must(t, f.s.DB.Model(&Policy{}).Where("org_id = ?", f.key.OrgID).Update("capture_history", false).Error)
	v := f.create(t, "direct-delivery")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	got, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if got.ArtifactStatus != "ready" || got.ContentURL != testDeliveryURL || got.Content == nil || got.BillingState != "settled" {
		t.Fatal("direct delivery not independently ready and settled")
	}
	var n int64
	must(t, f.s.DB.Model(&videoartifact.Record{}).Where("org_id = ?", f.key.OrgID).Count(&n).Error)
	if n != 0 {
		t.Fatal("generated artifact registered for storage")
	}
	f.s.ArtifactWorker = &videoartifact.Worker{MaxAttempts: 3}
	must(t, f.s.ProcessArtifacts(context.Background()))
	before := f.gets.Load()
	r := deliveryRouter(f)
	w := deliveryRequest(f, r, "/v1/video/generations/"+v.ID+"/content")
	var out struct{ URL, Kind, Source string }
	must(t, json.Unmarshal(w.Body.Bytes(), &out))
	if w.Code != 200 || out.URL != testDeliveryURL || out.Kind != "video" || out.Source != "upstream" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || w.Header().Get("Location") != "" {
		t.Fatal("content must be link JSON, never redirect or media bytes")
	}
	w = deliveryRequest(f, r, "/v1/video/generations/"+v.ID+"/content?kind=last_frame")
	must(t, json.Unmarshal(w.Body.Bytes(), &out))
	if w.Code != 200 || out.URL != "https://cdn.example.com/frame.jpg?token=frame" {
		t.Fatal("last-frame link lost")
	}
	w = deliveryRequest(f, r, "/v1/video/generations")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "content_url") || f.gets.Load() != before || f.posts.Load() != 1 {
		t.Fatal("cached API delivery unexpectedly calls provider")
	}
	var receipt QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
	if strings.Contains(receipt.ObservationJSON, "https:") || !strings.Contains(receipt.ObservationJSON, "delivery_payload_ref") {
		t.Fatal("receipt holds URL or lacks recoverable capsule")
	}
	var st State
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
	var meta invocation.VideoPayload
	must(t, f.s.DB.Where("payload_id = ?", st.ResultPayloadRef).First(&meta).Error)
	if meta.Purpose != invocation.VideoPurposeDelivery || meta.ExpiresAt.Sub(meta.CreatedAt) > 24*time.Hour {
		t.Fatal("wrong delivery purpose or retention")
	}
}

func TestMySQLDeliveryLegacyReadBackfillAndExpiry(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "legacy-delivery")
	f.dispatch(t, v.ID)
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	var receipt QueryReceipt
	must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&receipt).Error)
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("result_payload_ref", receipt.PayloadRef).Error)
	r := deliveryRouter(f)
	before := f.gets.Load()
	w := deliveryRequest(f, r, "/v1/video/generations")
	if w.Code != 200 || strings.Contains(w.Body.String(), "content_url") || f.gets.Load() != before {
		t.Fatal("list refreshed or returned legacy media")
	}
	// Same scope's query slot is occupied by completion; denied refresh must
	// remain local. Expiring this exact DB rate permits one official GET.
	w = deliveryRequest(f, r, "/v1/video/generations/"+v.ID)
	if w.Code != 200 || f.gets.Load() != before {
		t.Fatal("backfill bypassed query rate")
	}
	makeFixtureTaskRateDue(t, f, v.ID, "query")
	w = deliveryRequest(f, r, "/v1/video/generations/"+v.ID)
	var got TaskView
	must(t, json.Unmarshal(w.Body.Bytes(), &got))
	if w.Code != 200 || got.ContentURL != testDeliveryURL || f.gets.Load() != before+1 || f.posts.Load() != 1 {
		t.Fatal("backfill did not use one GET")
	}
	f.now = f.now.Add(25 * time.Hour)
	// Keep the real delivery-retention clock advance; scheduling is a
	// separate primary-time precondition, scoped to this task's provider.
	makeFixtureTaskRateDue(t, f, v.ID, "query")
	w = deliveryRequest(f, r, "/v1/video/generations")
	if w.Code != 200 || strings.Contains(w.Body.String(), "content_url") || f.gets.Load() != before+1 {
		t.Fatal("expired list capsule escaped")
	}
	w = deliveryRequest(f, r, "/v1/video/generations/"+v.ID)
	must(t, json.Unmarshal(w.Body.Bytes(), &got))
	if w.Code != 200 || got.ContentURL != testDeliveryURL || f.gets.Load() != before+2 {
		t.Fatal("expired capsule was not refreshed by detail GET")
	}
	var entries int64
	must(t, f.s.DB.Model(&videobilling.BillingEntry{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Count(&entries).Error)
	if entries != 1 || f.posts.Load() != 1 {
		t.Fatal("delivery refresh altered financial/generation state")
	}
	must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("deleted_at", f.now).Error)
	w = deliveryRequest(f, r, "/v1/video/generations/"+v.ID+"/content")
	if w.Code != 404 || f.gets.Load() != before+2 {
		t.Fatal("deleted task delivered or queried")
	}
}

func TestMySQLDeliveryUnsafeURLDoesNotPreventSettlement(t *testing.T) {
	f := mysqlFixture(t)
	deliverySuccess(f)
	v := f.create(t, "unsafe-delivery")
	f.dispatch(t, v.ID)
	f.response = func(*http.Request) (*http.Response, error) {
		return reply(200, `{"id":"official_delivery","status":"succeeded","usage":{"completion_tokens":100},"content":{"video_url":"https://cdn.example.com/result?token=official-test-credential"}}`), nil
	}
	must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
	got, e := f.s.Get(f.key.OrgID, v.ID)
	must(t, e)
	if got.ContentURL != "" || got.ArtifactStatus == "ready" || got.BillingState != "settled" {
		t.Fatal("unsafe URL exposed or blocked accounting")
	}
}

func TestMySQLDeliveryRefreshDeletionAndIdentityFences(t *testing.T) {
	for _, mode := range []string{"deleted", "wrong_task", "wrong_model"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			deliverySuccess(f)
			v := f.create(t, "fenced-delivery")
			f.dispatch(t, v.ID)
			must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
			must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("result_payload_ref", "").Error)
			makeFixtureTaskRateDue(t, f, v.ID, "query")
			f.response = func(*http.Request) (*http.Response, error) {
				if mode == "deleted" {
					must(t, f.s.DB.Model(&State{}).Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).Update("deleted_at", f.now).Error)
				}
				id, model := "official_delivery", ""
				if mode == "wrong_task" {
					id = "different_task"
				}
				if mode == "wrong_model" {
					model = "different_model"
				}
				return reply(200, `{"id":"`+id+`","model":"`+model+`","status":"succeeded","content":{"video_url":"`+testDeliveryURL+`"}}`), nil
			}
			w := deliveryRequest(f, deliveryRouter(f), "/v1/video/generations/"+v.ID)
			if strings.Contains(w.Body.String(), "content_url") {
				t.Fatal("unowned or deleted delivery escaped")
			}
			var st State
			must(t, f.s.DB.Where("org_id = ? AND task_id = ?", f.key.OrgID, v.ID).First(&st).Error)
			if st.ResultPayloadRef != "" || st.BillingState != "settled" || f.posts.Load() != 1 {
				t.Fatal("refresh resurrected a link or changed billing")
			}
		})
	}
}

func TestMySQLDeliveryAuditFailureNeverReleasesURL(t *testing.T) {
	for _, mode := range []string{"decrypt_audit", "response_finish"} {
		t.Run(mode, func(t *testing.T) {
			f := mysqlFixture(t)
			deliverySuccess(f)
			v := f.create(t, "delivery-audit-fence")
			f.dispatch(t, v.ID)
			must(t, f.s.pollTask(context.Background(), f.key.OrgID, v.ID))
			name := "test:delivery-audit-fence"
			if mode == "decrypt_audit" {
				must(t, f.s.DB.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "invocation_payload_accesses" {
						tx.AddError(errors.New("injected audit failure"))
					}
				}))
				defer f.s.DB.Callback().Create().Remove(name)
			} else {
				must(t, f.s.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "api_request_logs" {
						tx.AddError(errors.New("injected finish failure"))
					}
				}))
				defer f.s.DB.Callback().Update().Remove(name)
			}
			w := deliveryRequest(f, deliveryRouter(f), "/v1/video/generations/"+v.ID+"/content")
			if w.Code != 503 || strings.Contains(w.Body.String(), "cdn.example.com") || w.Header().Get("Location") != "" {
				t.Fatal("unaudited delivery URL escaped")
			}
		})
	}
}
