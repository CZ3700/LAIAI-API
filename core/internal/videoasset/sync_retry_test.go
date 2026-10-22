package videoasset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"testing/iotest"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

func newReadBody(raw []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(raw)) }

func TestSyncReadOnlyRetriesAreBoundedAndRespectBackoff(t *testing.T) {
	s, db, now, _ := setup(t)
	_ = s.ConfigureConsole(ConsoleOptions{})
	calls := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("Action") != "ListAssetGroups" {
			t.Fatal("mutation or unexpected read sent")
		}
		return nil, errors.New("temporary connection failure with private detail")
	})})
	job, err := s.CreateSyncJob(context.Background(), adminContext("sync-retry"), "virtual", "owned")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err = s.processSyncJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, e := s.GetSyncJob(context.Background(), 1, job.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		if calls != attempt || got.Attempts != uint64(attempt) || got.HTTPStatus != 0 || got.ErrorCode != "provider_unavailable" {
			t.Fatalf("bad attempt facts: %#v calls=%d", got, calls)
		}
		if attempt < 3 {
			if got.Status != "pending" || got.NextAttemptAt == nil || got.RetryCount != uint64(attempt) {
				t.Fatal("missing durable backoff")
			}
			if err = s.processSyncJobs(context.Background()); err != nil || calls != attempt {
				t.Fatal("resent before backoff", err)
			}
			*now = *got.NextAttemptAt
		} else if got.Status != "failed" || got.LeaseUntil != nil || got.NextAttemptAt != nil {
			t.Fatal("retry limit did not terminate")
		}
	}
	*now = now.Add(time.Hour)
	if err = s.processSyncJobs(context.Background()); err != nil || calls != 3 {
		t.Fatal("failed sync was resent", err)
	}
	var rows []invocation.VideoPayload
	if err = db.Where("request_id = ?", adminContext("sync-retry").RequestID).Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatal("request evidence absent", len(rows), err)
	}
	for _, p := range rows {
		if p.Purpose != invocation.VideoPurposeHistory || p.Direction != invocation.VideoEffectiveRequest {
			t.Fatal("wrong evidence purpose")
		}
	}
}

func TestSyncRetriesTransientResponsesButNotInvalidOrForbidden(t *testing.T) {
	for _, tc := range []struct {
		status      int
		badIdentity bool
		code        string
		retry       bool
	}{
		{429, false, "provider_rate_limited", true}, {503, false, "provider_unavailable", true},
		{403, false, "provider_permission_denied", false}, {401, false, "provider_authentication_failed", false},
		{200, true, "provider_response_invalid", false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			s, _, _, _ := setup(t)
			_ = s.ConfigureConsole(ConsoleOptions{})
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				r := syncReply(req, map[string]any{"Items": []any{}})
				r.StatusCode = tc.status
				if tc.badIdentity {
					r = syncReply(req, map[string]any{"Items": []any{}})
					raw := bodyJSON(map[string]any{"ResponseMetadata": map[string]string{"RequestId": "bad", "Action": "wrong"}, "Result": map[string]any{"Items": []any{}}})
					r.Body = newReadBody(raw)
				}
				if tc.status >= 400 {
					r.Body = newReadBody(bodyJSON(map[string]any{"ResponseMetadata": map[string]any{"RequestId": "official-error", "Action": req.URL.Query().Get("Action"), "Version": managementVersion, "Service": "ark", "Region": "cn-beijing", "Error": map[string]string{"Code": "fixture", "Message": "private detail"}}, "Result": nil}))
				}
				return r, nil
			})})
			job, err := s.CreateSyncJob(context.Background(), adminContext("response"), "virtual", "owned")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.processSyncJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
			if err != nil || job.ErrorCode != tc.code || (job.NextAttemptAt != nil) != tc.retry || job.HTTPStatus != tc.status {
				t.Fatal("wrong classified retry", job.ErrorCode, err)
			}
		})
	}
}

func TestSyncPageEvidenceHonorsHistoryConsentAndRetryRecovers(t *testing.T) {
	for _, capture := range []bool{true, false} {
		t.Run(fmt.Sprint(capture), func(t *testing.T) {
			s, db, now, _ := setup(t)
			_ = s.ConfigureConsole(ConsoleOptions{})
			setHistoryPolicy(s, 7, capture)
			calls := 0
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, errors.New("temporary")
				}
				return syncReply(req, map[string]any{"Items": []any{}}), nil
			})})
			job, err := s.CreateSyncJob(context.Background(), adminContext("consent"), "virtual", "owned")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.processSyncJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(5 * time.Second)
			if err = s.processSyncJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Second)
			if err = s.processSyncJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
			if err != nil || job.Status != "succeeded" || job.Attempts != 2 || job.ErrorCode != "" || job.RetryCount != 0 {
				t.Fatal("successful retry not committed", job, err)
			}
			var rows []invocation.VideoPayload
			if err = db.Where("request_id = ?", adminContext("consent").RequestID).Find(&rows).Error; err != nil || len(rows) != 3 {
				t.Fatal("attempt evidence missing", err, len(rows))
			}
			for _, p := range rows {
				if p.Purpose != invocation.VideoPurposeHistory || !p.ExpiresAt.Equal(p.CreatedAt.Add(7*24*time.Hour)) {
					t.Fatal("retention mismatch")
				}
				if capture && p.WrappedKey == "" {
					t.Fatal("missing encrypted capture")
				}
				if !capture && (p.CaptureStatus != invocation.VideoCaptureDisabled || p.WrappedKey != "" || p.PlaintextBytes != 0) {
					t.Fatal("capture-disabled retained data")
				}
			}
		})
	}
}

func TestSyncPartialAndHTMLResponsesRetainRedactedEvidenceAndClassifyRetry(t *testing.T) {
	for _, tc := range []struct {
		status         int
		partial, retry bool
	}{{200, true, true}, {503, false, true}, {401, true, false}, {403, true, false}, {429, true, true}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			s, db, _, _ := setup(t)
			_ = s.ConfigureConsole(ConsoleOptions{})
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				var body io.Reader = bytes.NewBufferString("<html>temporary test-secret</html>")
				if tc.partial {
					body = io.MultiReader(bytes.NewBufferString(`{"partial":"test-secret"`), iotest.ErrReader(io.ErrUnexpectedEOF))
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(body), Header: http.Header{}, Request: req}, nil
			})})
			job, err := s.CreateSyncJob(context.Background(), adminContext("partial"), "virtual", "owned")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.processSyncJobs(context.Background()); err != nil {
				t.Fatal(err)
			}
			job, err = s.GetSyncJob(context.Background(), 1, job.ID, false)
			wantStatus := "failed"
			if tc.retry {
				wantStatus = "pending"
			}
			if err != nil || job.Status != wantStatus || (job.NextAttemptAt != nil) != tc.retry || job.HTTPStatus != tc.status || job.ErrorCode != "provider_unavailable" {
				t.Fatal("partial response retry classification incorrect", job.ErrorCode, err)
			}
			var payload invocation.VideoPayload
			if err = db.Where("payload_id = ?", job.LastResponsePayloadID).Take(&payload).Error; err != nil || payload.BodyFormat != invocation.VideoFormatText {
				t.Fatal("partial/HTML body not classified", err)
			}
			raw, err := s.evidence.ReadForHistory(context.Background(), 1, payload.PayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user1", ReasonCode: "request_debugging", OperationID: job.ID})
			if err != nil || bytes.Contains(raw, []byte("test-secret")) || !bytes.Contains(raw, []byte("original_bytes")) {
				t.Fatal("invalid redacted evidence", err)
			}
			clear(raw)
		})
	}
}
