package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

func TestPublicAssetSourceURLStructuralBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"https://media.example/picture.png?token=signed-source", true},
		{"https://cdn.example:443/media", true},
		{"https://8.8.8.8/media", true},
		{"https://[2606:4700:4700::1111]/media", true},
		{"http://media.example/image", false},
		{"https://user:password@media.example/image", false},
		{"https://media.example/image#fragment", false},
		{"https://localhost/image", false},
		{"https://localhost./image", false},
		{"https://x.localhost/image", false},
		{"https://x.local/image", false},
		{"https://x.internal/image", false},
		{"https://x.lan/image", false},
		{"https://x.home.arpa/image", false},
		{"https://printer/image", false},
		{"https://127.0.0.1/image", false},
		{"https://127.1/image", false},
		{"https://2130706433/image", false},
		{"https://0x7f.0.0.1/image", false},
		{"https://10.0.0.1/image", false},
		{"https://172.16.0.1/image", false},
		{"https://192.168.1.1/image", false},
		{"https://169.254.169.254/image", false},
		{"https://[::1]/image", false},
		{"https://[::ffff:127.0.0.1]/image", false},
		{"https://[fe80::1]/image", false},
		{"https://media.example:8080/image", false},
		{"https://media.example/image\n", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := publicAssetSourceURL(tc.raw); got != tc.valid {
				t.Fatalf("valid=%v want=%v", got, tc.valid)
			}
		})
	}
}

func TestDirectProviderImageRegistrationEncryptedAsyncAndReferenceable(t *testing.T) {
	s, db, now, provider := setup(t)
	ctx := context.Background()
	g := readyGroup(t, s, "provider-url")
	s.options.VerifyMedia = nil
	const source = "https://media.example/direct.png?signature=source-token-fixture"
	in := AssetCreate{GroupID: g.ID, MediaType: "image", Name: "provider image", URL: source}
	c := createContext("direct-image")
	a, replayed, err := s.CreateAsset(ctx, c, in)
	if err != nil || replayed || a.Reference != "" || provider.calls["CreateAsset"] != 0 {
		t.Fatal("direct URL admission or asynchronous boundary failed", err)
	}
	var row Asset
	var op Operation
	if err := db.Where("asset_id = ?", a.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("operation_id = ?", row.CreateOperationID).Take(&op).Error; err != nil {
		t.Fatal(err)
	}
	if row.ImmutableObjectRef != "" || row.ObjectSHA256 != "" || row.ByteSize != 0 || row.DurationMS != 0 || row.Width != 0 || row.Height != 0 || row.MetadataVersion != 0 || !row.VerifiedAt.IsZero() {
		t.Fatal("direct URL invented platform media facts")
	}
	var nulls int64
	if err := db.Model(&Asset{}).Where("asset_id = ? AND verified_at IS NULL", row.ID).Count(&nulls).Error; err != nil || nulls != 1 {
		t.Fatal("unverified media must use SQL NULL", err)
	}
	plain, err := s.evidence.ReadForExecution(ctx, 1, op.PayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "fixture", ReasonCode: "asset_dispatch", OperationID: op.ID})
	if err != nil {
		t.Fatal(err)
	}
	var request struct{ URL, GroupId, ProjectName, AssetType string }
	if json.Unmarshal(plain, &request) != nil || request.URL != source || request.GroupId == "" || request.ProjectName != testScope().Project || request.AssetType != "Image" {
		t.Fatal("encrypted execution body lost exact provider request")
	}
	for _, table := range []string{"video_assets", "video_asset_operations", "video_asset_events", "invocation_payloads", "invocation_payload_staging"} {
		var rows []map[string]any
		if err := db.Table(table).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(rows)
		if err != nil || strings.Contains(string(raw), "source-token-fixture") || strings.Contains(string(raw), source) {
			t.Fatal("source URL escaped encrypted execution storage", table)
		}
	}
	// Only the official management client sees the submitted URL; there is no
	// media downloader or scanner configured in this test.
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != managementHost {
			t.Fatal("gateway tried to download source media")
		}
		if r.URL.Query().Get("Action") == "CreateAsset" {
			raw, _ := io.ReadAll(r.Body)
			var got struct{ URL string }
			if json.Unmarshal(raw, &got) != nil || got.URL != source {
				t.Fatal("provider URL changed")
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
		}
		return provider.roundTrip(r)
	})})
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(16 * time.Second)
	if err := s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAsset(ctx, 1, a.ID)
	if err != nil || got.Status != "ready" || got.Reference != "asset://"+a.ID {
		t.Fatal("official Active image is not referenceable", got.Status, err)
	}
	page, err := s.ConsoleAssets(ctx, 1, ListFilter{GroupID: g.ID, Status: "ready", Limit: 20})
	if err != nil || page.Total != 1 || page.Data[0].ProviderStatus != "active" || page.Data[0].MediaVerificationStatus != "unverified" || page.Data[0].GenerationStatus != "ready" {
		t.Fatal("provider status conflated with local verification", err)
	}
	bindings, err := s.ResolveBindingsTx(db, 1, testScope(), "provider-image-task", []ReferenceInput{{AssetID: a.ID, Role: "reference_image", ContentIndex: 0}})
	if err != nil || len(bindings) != 1 || bindings[0].ProviderAssetID == "" || bindings[0].MetadataVersion != 0 {
		t.Fatal("official image binding failed", err)
	}
	replayedAsset, replayed, err := s.CreateAsset(ctx, c, in)
	if err != nil || !replayed || replayedAsset.ID != a.ID || provider.calls["CreateAsset"] != 1 {
		t.Fatal("idempotency resubmitted direct URL", err)
	}
}

func TestDirectProviderUnknownCreateIsNotResubmitted(t *testing.T) {
	s, db, now, provider := setup(t)
	g := readyGroup(t, s, "unknown-provider-url")
	s.options.VerifyMedia = nil
	c := createContext("unknown-provider-image")
	in := AssetCreate{GroupID: g.ID, MediaType: "image", URL: "https://media.example/image.png"}
	a, _, err := s.CreateAsset(context.Background(), c, in)
	if err != nil {
		t.Fatal(err)
	}
	provider.unknown = true
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.unknown = false
	*now = now.Add(time.Minute)
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, replayed, err := s.CreateAsset(context.Background(), c, in)
	if err != nil || !replayed || got.ID != a.ID || got.Reference != "" || provider.calls["CreateAsset"] != 1 {
		t.Fatal("unknown create was blindly retried", err)
	}
	var op Operation
	if err := db.Where("resource_id = ? AND action = ?", a.ID, "CreateAsset").Take(&op).Error; err != nil || op.Status != "unknown" {
		t.Fatal("unknown receipt lost", err)
	}
}

func TestDirectAVRegistrationDoesNotFabricateBillingDuration(t *testing.T) {
	for _, kind := range []string{"video", "audio"} {
		t.Run(kind, func(t *testing.T) {
			s, db, now, provider := setup(t)
			g := readyGroup(t, s, "av-"+kind)
			s.options.VerifyMedia = nil
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("Action") != "GetAsset" {
					return provider.roundTrip(r)
				}
				var req struct{ Id string }
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Fatal("bad request")
				}
				return syncReply(r, map[string]any{"Id": req.Id, "GroupId": provider.groupByAsset[req.Id], "ProjectName": testScope().Project, "AssetType": strings.ToUpper(kind[:1]) + kind[1:], "Status": "Active"}), nil
			})})
			a, _, err := s.CreateAsset(context.Background(), createContext("direct-"+kind), AssetCreate{GroupID: g.ID, MediaType: kind, URL: "https://media.example/" + kind})
			if err != nil {
				t.Fatal("AV registration should be available", err)
			}
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(16 * time.Second)
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			page, err := s.ConsoleAssets(context.Background(), 1, ListFilter{GroupID: g.ID, ProviderStatus: "active", Limit: 20})
			if err != nil || page.Total != 1 {
				t.Fatal("official AV status lost", err)
			}
			got := page.Data[0]
			if got.Reference != "" || got.DurationMS != 0 || got.MediaVerificationStatus != "unverified" || got.GenerationStatus != "blocked" || got.GenerationBlockReason != "media_facts_unavailable" {
				t.Fatal("unknown AV duration presented as generation ready")
			}
			if _, err := s.ResolveBindingsTx(db, 1, testScope(), "av-task", []ReferenceInput{{AssetID: a.ID, Role: "reference_" + kind, ContentIndex: 0}}); !errors.Is(err, ErrUnsupported) {
				t.Fatal("unknown duration did not fail explicitly before billing", err)
			}
			var refs int64
			if err := db.Model(&TaskReference{}).Count(&refs).Error; err != nil || refs != 0 {
				t.Fatal("unsupported media reserved a task reference", err)
			}
			ready, err := s.ConsoleAssets(context.Background(), 1, ListFilter{GroupID: g.ID, Status: "ready", Limit: 20})
			if err != nil || ready.Total != 0 {
				t.Fatal("unknown duration leaked into ready filter", err)
			}
		})
	}
}

func TestOptionalVerifiedAVFactsRetainReferenceEligibility(t *testing.T) {
	for _, kind := range []string{"video", "audio"} {
		t.Run(kind, func(t *testing.T) {
			s, db, now, _ := setup(t)
			g := readyGroup(t, s, "verified-av-"+kind)
			a := readyAsset(t, s, now, g.ID, "verified-av-"+kind)
			mime := "video/mp4"
			if kind == "audio" {
				mime = "audio/wav"
			}
			if err := db.Model(&Asset{}).Where("asset_id = ?", a.ID).Updates(map[string]any{"media_type": kind, "mime": mime, "duration_ms": 4000, "width": 1280, "height": 720, "fps_num": 24, "fps_den": 1}).Error; err != nil {
				t.Fatal(err)
			}
			for _, duration := range []int64{4000, 0, 31000} {
				if err := db.Model(&Asset{}).Where("asset_id = ?", a.ID).Update("duration_ms", duration).Error; err != nil {
					t.Fatal(err)
				}
				page, err := s.ConsoleAssets(context.Background(), 1, ListFilter{GroupID: g.ID, Limit: 20})
				if err != nil || len(page.Data) != 1 {
					t.Fatal(err)
				}
				filter, err := s.ConsoleAssets(context.Background(), 1, ListFilter{GroupID: g.ID, Status: "ready", Limit: 20})
				if err != nil {
					t.Fatal(err)
				}
				wantReady := duration == 4000
				if (page.Data[0].GenerationStatus == "ready") != wantReady || (filter.Total == 1) != wantReady {
					t.Fatal("AV list filter and measured facts disagree", duration)
				}
				bindings, err := s.ResolveBindingsTx(db, 1, testScope(), "verified-av-task", []ReferenceInput{{AssetID: a.ID, Role: "reference_" + kind, ContentIndex: 0}})
				if wantReady {
					if err != nil || len(bindings) != 1 || bindings[0].DurationMS != 4000 {
						t.Fatal("trusted duration lost", err)
					}
					if err := s.ReleaseBindingsTx(db, 1, "verified-av-task"); err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, ErrUnsupported) {
					t.Fatal("invalid AV facts accepted", err)
				}
			}
		})
	}
}

func TestOptionalVerifiedURLCannotRedirectRegistrationToPrivateHost(t *testing.T) {
	s, _, _, provider := setup(t)
	g := readyGroup(t, s, "unsafe-verified-fetch")
	s.options.VerifyMedia = func(context.Context, uint64, string, string) (MediaEvidence, error) {
		return MediaEvidence{ImmutableObjectRef: "trusted", FetchURL: "https://127.0.0.1/private", ObjectSHA256: strings.Repeat("a", 64), MIME: "image/png", ByteSize: 4096, Width: 640, Height: 480, Version: 1, VerifiedAt: s.now()}, nil
	}
	_, _, err := s.CreateAsset(context.Background(), createContext("unsafe-verified-fetch"), AssetCreate{GroupID: g.ID, MediaType: "image", URL: "https://media.example/safe"})
	if !errors.Is(err, ErrInvalid) || provider.calls["CreateAsset"] != 0 {
		t.Fatal("optional verified path bypassed source URL boundary", err)
	}
}
