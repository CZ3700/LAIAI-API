package video

import (
	"errors"
	"fmt"
	"testing"

	"github.com/laiai/laiai-next/core/internal/videoasset"
)

func TestAssetRuntimeRetentionPolicyIsExplicit(t *testing.T) {
	for _, days := range []int{-1, 0, 1, 30, 31} {
		t.Run(fmt.Sprintf("days_%d", days), func(t *testing.T) {
			s, _ := assetSetupFixture(t)
			seedAssetSetup(t, s)
			if err := s.DB.Exec("UPDATE video_org_policies SET retention_days=? WHERE org_id=1", days).Error; err != nil {
				t.Fatal(err)
			}
			got, err := s.assetScopeTx(s.DB, 1, 0, videoasset.Scope{}, false)
			if days < 1 || days > 30 {
				if !errors.Is(err, ErrForbidden) || !errors.Is(err, videoasset.ErrExecutionPolicy) {
					t.Fatal("invalid saved policy must not acquire a default retention grant", err)
				}
				return
			}
			if err != nil || got.RetentionDays != days || got.CaptureHistory || !got.ExecutionRetentionConsent {
				t.Fatalf("runtime changed saved policy: %+v, err=%v", got, err)
			}
		})
	}
}
