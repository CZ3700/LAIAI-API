package videoasset

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func setHistoryPolicy(s *Service, days int, capture bool) {
	resolve := s.options.ResolveScope
	s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
		r, err := resolve(ctx, org, scope)
		r.RetentionDays, r.CaptureHistory = days, capture
		return r, err
	}
}

func assertHistoryEvidence(t *testing.T, s *Service, db *gorm.DB, p invocation.VideoPayload, days int, capture bool) {
	t.Helper()
	if p.Purpose != invocation.VideoPurposeHistory || !p.ExpiresAt.Equal(p.CreatedAt.Add(time.Duration(days)*24*time.Hour)) {
		t.Fatalf("history retention differs from policy: purpose=%s lifetime=%v", p.Purpose, p.ExpiresAt.Sub(p.CreatedAt))
	}
	var staged int64
	if err := db.Table("invocation_payload_staging").Where("payload_id = ?", p.PayloadID).Count(&staged).Error; err != nil {
		t.Fatal(err)
	}
	if !capture {
		if p.CaptureStatus != invocation.VideoCaptureDisabled || p.WrappedKey != "" || p.PlaintextBytes != 0 || staged != 0 {
			t.Fatal("disabled history retained response content")
		}
		return
	}
	if p.WrappedKey == "" || staged != 1 {
		t.Fatal("enabled history missing encrypted evidence")
	}
	raw, err := s.evidence.ReadForHistory(context.Background(), 1, p.PayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user1", ReasonCode: "request_debugging", OperationID: "history-test"})
	if err != nil || !bytes.Contains(raw, []byte("ResponseMetadata")) {
		t.Fatal("cannot read retained official response", err)
	}
	clear(raw)
}

func TestAssetWorkerHistoryUsesOrganizationPolicy(t *testing.T) {
	for _, tc := range []struct {
		days    int
		capture bool
	}{{1, true}, {30, true}, {1, false}, {30, false}} {
		t.Run(fmt.Sprintf("days_%d_capture_%t", tc.days, tc.capture), func(t *testing.T) {
			s, db, _, _ := setup(t)
			setHistoryPolicy(s, tc.days, tc.capture)
			g := readyGroup(t, s, "retention")
			if _, _, err := s.DeleteGroup(context.Background(), createContext("delete-retention"), g.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			var rows []invocation.VideoPayload
			if err := db.Where("purpose = ?", invocation.VideoPurposeHistory).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			want := 2 // deletion response and empty-group precheck metadata
			if tc.capture {
				want++ // ordinary worker response capture remains optional
			}
			if len(rows) != want {
				t.Fatalf("history rows=%d, want %d", len(rows), want)
			}
			for _, p := range rows {
				assertHistoryEvidence(t, s, db, p, tc.days, tc.capture)
			}
			var execution []invocation.VideoPayload
			if err := db.Where("purpose = ?", invocation.VideoPurposeExecution).Find(&execution).Error; err != nil || len(execution) != 2 {
				t.Fatal("required execution evidence lost", err)
			}
			for _, p := range execution {
				if p.WrappedKey == "" || !p.ExpiresAt.Equal(p.CreatedAt.Add(24*time.Hour)) {
					t.Fatal("history policy changed required execution lifetime")
				}
			}
		})
	}
}

func TestImportedMediaHistoryUsesOrganizationPolicy(t *testing.T) {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed; media verification requires deployment probe")
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 640, 480))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		days    int
		capture bool
	}{{1, true}, {30, true}, {1, false}} {
		t.Run(fmt.Sprintf("days_%d_capture_%t", tc.days, tc.capture), func(t *testing.T) {
			s, db, now, _ := setup(t)
			g := readyGroup(t, s, "media-retention")
			a := readyAsset(t, s, now, g.ID, "media-retention")
			setHistoryPolicy(s, tc.days, tc.capture)
			if err := db.Model(&Asset{}).Where("asset_id = ?", a.ID).Updates(map[string]any{"immutable_object_ref": "", "review_state": ReviewRequired}).Error; err != nil {
				t.Fatal(err)
			}
			media, err := NewMediaStore(MediaStoreConfig{Root: t.TempDir(), PublicBaseURL: "https://media.example/api/video/media-source", FFProbePath: probe, SigningKey: bytes.Repeat([]byte{8}, 32), ScanCommand: []string{os.Args[0], "-test.run=^TestAssetMediaScanProcess$", "--", "--asset-test-scanner"}})
			if err != nil {
				t.Fatal(err)
			}
			media.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "private.example" {
					t.Fatal("untrusted media source")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pngBytes.Bytes())), Header: http.Header{}, Request: req}, nil
			})}
			if err := s.ConfigureConsole(ConsoleOptions{Media: media}); err != nil {
				t.Fatal(err)
			}
			if err := s.VerifyImportedAsset(context.Background(), 1, a.ID); err != nil {
				t.Fatal(err)
			}
			var asset Asset
			if err := db.Where("asset_id = ?", a.ID).Take(&asset).Error; err != nil || asset.ReviewState != ReviewClear || asset.ImmutableObjectRef == "" || asset.Width != 640 || asset.Height != 480 {
				t.Fatal("verified media facts missing", err)
			}
			var history invocation.VideoPayload
			if err := db.Where("payload_id = ?", asset.SourcePayloadID).Take(&history).Error; err != nil {
				t.Fatal(err)
			}
			assertHistoryEvidence(t, s, db, history, tc.days, tc.capture)
			rec := httptest.NewRecorder()
			if err := s.Preview(context.Background(), 1, a.ID, rec, httptest.NewRequest("GET", "/preview", nil)); err != nil || rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes.Bytes()) {
				t.Fatal("verified preview incorrectly requires history capture", err)
			}
		})
	}
}
