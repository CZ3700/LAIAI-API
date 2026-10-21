package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoasset"
)

func TestAssetConsoleExecutionPolicyDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, change, reason string
		missingPolicy        bool
	}{
		{"policy_missing", "DELETE FROM video_org_policies WHERE org_id=1", "execution_policy_required", true},
		{"policy_disabled", "UPDATE video_org_policies SET async_execution_allowed=0 WHERE org_id=1", "execution_policy_required", false},
		{"policy_enabled", "", "", false},
		{"policy_storage_failure", "DROP TABLE video_org_policies", "management_scope_not_configured", false},
		{"route_missing", "DELETE FROM video_asset_org_routes WHERE org_id=1", "management_scope_not_configured", false},
		{"management_version_disabled", "UPDATE video_asset_scope_configs SET enabled=0 WHERE provider_scope_id=10", "management_scope_not_configured", false},
		{"credentials_unreadable", "UPDATE video_asset_scope_configs SET credential_cipher='invalid-cipher' WHERE provider_scope_id=10", "management_scope_not_configured", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := assetSetupFixture(t)
			seedAssetSetup(t, s)
			keys, err := crypto.NewKeyring("test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "test")
			if err != nil {
				t.Fatal(err)
			}
			s.Keyring = keys
			credentials := struct{ Account, Region, Project, AccessKey, SecretKey, SessionToken string }{
				Account: "1234567890", Region: "cn-beijing", Project: "default", AccessKey: "fixture-access-key", SecretKey: "fixture-secret-key",
			}
			cipher, err := s.assetSeal("management", 0, "10", 7, credentials)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.DB.Exec("UPDATE video_asset_scope_configs SET credential_cipher=? WHERE provider_scope_id=10", cipher).Error; err != nil {
				t.Fatal(err)
			}
			s.Assets, err = videoasset.NewService(s.DB, keys, &invocation.VideoStore{}, videoasset.Options{
				Now: s.Now, ActiveHMACKey: "test", HMACKeys: map[string][]byte{"test": []byte(strings.Repeat("h", 32))}, ResolveScope: s.resolveAssetRuntime,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != "" {
				if err = s.DB.Exec(tc.change).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Capability discovery cannot create consent, bind a route, or change
			// stored configuration while diagnosing an incomplete setup.
			if err = s.DB.Exec("PRAGMA query_only=ON").Error; err != nil {
				t.Fatal(err)
			}
			got := s.Assets.ConsoleCapabilities(context.Background(), 1)
			if tc.reason != "" {
				want := videoasset.CapabilityView{MediaTypes: []string{}, Reason: tc.reason}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("unavailable setup exposed capabilities: %+v", got)
				}
			} else if !got.Ready || got.Reason != "" || !got.Virtual || !reflect.DeepEqual(got.MediaTypes, []string{"image"}) {
				t.Fatalf("configured scope unavailable: %+v", got)
			}
			_, err = s.resolveAssetRuntime(context.Background(), 1, videoasset.Scope{})
			if tc.missingPolicy && (!errors.Is(err, videoasset.ErrExecutionPolicy) || !errors.Is(err, ErrForbidden)) {
				t.Fatalf("missing policy must preserve admission denial and safe diagnostic: %v", err)
			}
			if !tc.missingPolicy && errors.Is(err, videoasset.ErrExecutionPolicy) {
				t.Fatal("unrelated configuration error classified as missing consent")
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"fixture-access-key", "fixture-secret-key", "credential_cipher", "invalid-cipher", "1234567890"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("diagnostic disclosed management configuration")
				}
			}
		})
	}
}
