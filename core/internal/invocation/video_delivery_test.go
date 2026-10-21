package invocation

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestVideoDeliveryCapsuleIsolation(t *testing.T) {
	s, db, _ := videoSetup(t)
	in := videoInput(s)
	in.Purpose, in.Direction = VideoPurposeDelivery, VideoUpstreamResponse
	in.Body = []byte(`{"video_url":"https://cdn.example.com/a.mp4?token=a%2Bb&signature=X%2FZ&x=1"}`)
	p := videoStage(t, s, db, in)
	plain, err := s.ReadForDelivery(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleOrgReader))
	if err != nil || !bytes.Contains(plain, []byte(`token=a%2Bb`)) || !bytes.Contains(plain, []byte(`signature=X%2FZ`)) {
		t.Fatalf("signature changed or inaccessible: %v", err)
	}
	for _, role := range []string{VideoRoleWorker} {
		if _, err = s.ReadForDelivery(context.Background(), 1, p.PayloadID(), videoAccess(role)); !errors.Is(err, ErrVideoAccessDenied) {
			t.Fatal("worker can read delivery")
		}
	}
	if _, err = s.ReadForDelivery(context.Background(), 2, p.PayloadID(), videoAccess(VideoRoleOrgReader)); !errors.Is(err, ErrVideoNotFound) {
		t.Fatal("cross-organization delivery")
	}
	if _, err = s.ReadForHistory(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleOrgReader)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal("delivery exposed as history")
	}
	if _, err = s.ReadForRecovery(context.Background(), 1, p.PayloadID(), videoAccess(VideoRoleWorker)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal("delivery exposed as recovery")
	}
	in.RequestID = "history"
	in.Purpose = VideoPurposeHistory
	history := videoStage(t, s, db, in)
	if _, err = s.ReadForDelivery(context.Background(), 1, history.PayloadID(), videoAccess(VideoRoleOrgReader)); !errors.Is(err, ErrVideoAccessDenied) {
		t.Fatal("history exposed as delivery")
	}
}

func TestVideoDeliveryValidation(t *testing.T) {
	s, _, _ := videoSetup(t)
	for _, raw := range []string{
		`{"video_url":"https://cdn.example.com/a","extra":"private prompt"}`,
		`{"video_url":"http://cdn.example.com/a"}`,
		`{"video_url":"https://user:pwd@cdn.example.com/a"}`,
		`{"video_url":"https://127.0.0.1/a"}`,
		`{"video_url":"https://127.1/a"}`,
		`{"video_url":"https://0x7f.0.0.1/a"}`,
		`{"video_url":"https://0177.0.0.1/a"}`,
		`{"video_url":"https://cdn.0x7f000001/a"}`,
		`{"video_url":"https://foo.local/a"}`,
		`{"video_url":"https://cdn.example.com/a?token=official-test-secret"}`,
		`{"video_url":"https://cdn.example.com/a?token=official%2Dtest%2Dsecret"}`,
		`{}`, `{"video_url":"https://cdn.example.com/a"} {}`,
	} {
		in := videoInput(s)
		in.Purpose, in.Direction = VideoPurposeDelivery, VideoUpstreamResponse
		in.Body = []byte(raw)
		in.SecretValues = []string{"official-test-secret"}
		if _, err := s.Prepare(in); !errors.Is(err, ErrVideoInvalid) {
			t.Fatalf("unsafe capsule accepted: %v", err)
		}
	}
	in := videoInput(s)
	in.Purpose, in.Direction = VideoPurposeDelivery, VideoUpstreamResponse
	in.Body = []byte(`{"video_url":"https://cdn.example.com/a"}`)
	in.ExpiresAt = s.now().Add(25 * time.Hour)
	if _, err := s.Prepare(in); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatal("delivery TTL over 24h")
	}
	in.ExpiresAt = s.now().Add(time.Hour)
	in.CaptureEnabled = false
	if _, err := s.Prepare(in); !errors.Is(err, ErrVideoRetentionDenied) {
		t.Fatal("delivery metadata-only accepted")
	}
}
