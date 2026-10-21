package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/video"
	"github.com/laiai/laiai-next/core/internal/videoasset"
)

// Use the same constructors as cmd/core with optional input media and H5 off.
// Never fake the retired output artifact service to satisfy readiness checks.
func configureDirectVideoRuntime(t *testing.T, s *Server) {
	t.Helper()
	s.keyring = testKeyring(t)
	svc, err := video.NewService(s.db, s.keyring, []byte(strings.Repeat("h", 32)), nil)
	arkBillMust(t, err)
	svc.Client = &http.Client{Transport: configNoNetwork{t}}
	arkBillMust(t, svc.ConfigureAssets(video.AssetIntegrationOptions{}))
	arkBillMust(t, svc.Assets.ConfigureConsole(videoasset.ConsoleOptions{}))
	if svc.Evidence == nil || svc.ArtifactService != nil || svc.ArtifactWorker != nil {
		t.Fatal("direct delivery runtime unexpectedly depends on generated-media storage")
	}
	s.video = svc
}

func TestVideoConfigurationRequiresDirectDeliveryRuntime(t *testing.T) {
	f, db, tokens, in := initialOfficialPricingFixture(t, ark.Model20)
	runtime := f.srv.video
	before := verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs")
	for _, unavailable := range []*video.Service{nil, {}} {
		f.srv.video = unavailable
		templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 503)
		if before != verificationSnapshot(t, db, "video_model_configs", "video_pricing_rules", "video_provider_scopes", "video_quota_policies", "audit_logs") {
			t.Fatal("unavailable runtime changed published configuration")
		}
	}
	f.srv.video = runtime
	// This is the real production assembly: no output storage, download worker,
	// input-media directory, scanner, or H5 target is necessary for publication.
	templateCall(t, f, "/api/admin/video/configurations", tokens[1], "", in, 201)
	var cfg video.ModelConfig
	arkBillMust(t, db.Where("public_model = ?", ark.Model20).Take(&cfg).Error)
	if cfg.Version != in.Configuration.Version || cfg.ProviderScopeID != in.Scope.ID || cfg.PricingVersion != in.Configuration.PricingVersion {
		t.Fatal("direct delivery configuration did not publish")
	}
}
