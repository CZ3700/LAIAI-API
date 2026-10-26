package video

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
)

// Include provider quota buckets by reservation identity: provider buckets use
// org_id=0, so an organization-only bucket predicate would miss their changes.
func catalogCreateFacts(t *testing.T, f *fixture, taskID string) string {
	t.Helper()
	var reservations []videobilling.QuotaReservation
	must(t, f.s.DB.Where("org_id = ?", f.key.OrgID).Order("id").Find(&reservations).Error)
	ids := []uint64{}
	for _, r := range reservations {
		ids = append(ids, r.BucketID)
	}
	var buckets []videobilling.QuotaBucket
	if len(ids) > 0 {
		must(t, f.s.DB.Where("id IN ?", ids).Order("id").Find(&buckets).Error)
	}
	var key struct{ UsedAmount decimal.Decimal }
	must(t, f.s.DB.Model(&identity.APIKey{}).Select("used_amount").Where("id = ? AND org_id = ?", f.key.ID, f.key.OrgID).Take(&key).Error)
	counts := []int64{}
	for _, model := range []any{&task.Task{}, &State{}, &Operation{}, &videobilling.Snapshot{}, &CredentialSnapshot{}, &Event{}, &Outbox{}, &IdempotencyRecord{}} {
		var count int64
		must(t, f.s.DB.Model(model).Where("org_id = ?", f.key.OrgID).Count(&count).Error)
		counts = append(counts, count)
	}
	raw, err := json.Marshal([]any{responseInboxFunds(t, f, taskID), reservations, buckets, key.UsedAmount, counts})
	must(t, err)
	return string(raw)
}

func TestMySQLVideoCatalogCreateReplaySurvivesPublicationClosure(t *testing.T) {
	f := mysqlFixture(t)
	in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "test prompt private"}}}
	first := f.create(t, "catalog-replay-original")
	var cfg ModelConfig
	must(t, f.s.DB.Where("public_model = ?", ark.Model20).First(&cfg).Error)
	before := catalogCreateFacts(t, f, first.ID)
	for _, change := range []string{"model_disabled", "capability_invalid", "price_disabled"} {
		t.Run(change, func(t *testing.T) {
			must(t, f.s.DB.Save(&cfg).Error)
			must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ? AND model = ?", cfg.ProviderScopeID, cfg.PublicModel).Update("enabled", true).Error)
			switch change {
			case "model_disabled":
				must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", cfg.PublicModel).Update("enabled", false).Error)
			case "capability_invalid":
				must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", cfg.PublicModel).Update("capability_json", "{}").Error)
			case "price_disabled":
				must(t, f.s.DB.Model(&videobilling.PricingRule{}).Where("provider_scope_id = ? AND model = ?", cfg.ProviderScopeID, cfg.PublicModel).Update("enabled", false).Error)
			}
			replayed, replay, err := f.s.Create(context.Background(), f.key, f.requestID("replay"), "catalog-replay-original", in)
			must(t, err)
			if !replay || replayed.ID != first.ID || replayed.Status != first.Status {
				t.Fatal("historical same-body receipt was re-admitted against current publication")
			}
			changed := in
			changed.Content = []ark.ContentItem{{Type: "text", Text: "different body"}}
			_, replay, err = f.s.Create(context.Background(), f.key, f.requestID("conflict"), "catalog-replay-original", changed)
			if !errors.Is(err, ErrConflict) || replay {
				t.Fatal("different-body conflict was hidden by unavailable publication", err)
			}
			_, replay, err = f.s.Create(context.Background(), f.key, f.requestID("new"), "catalog-new-"+change, in)
			if err == nil || replay {
				t.Fatal("new request bypassed closed publication")
			}
			if catalogCreateFacts(t, f, first.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("replay or failed admission changed tasks, money, quota or operations")
			}
		})
	}
}

func TestMySQLVideoCatalogCreateUsesOnlyFixedChannel(t *testing.T) {
	for _, fault := range []string{"disabled", "byok", "wrong_endpoint", "wrong_provider"} {
		t.Run(fault, func(t *testing.T) {
			f := mysqlFixture(t)
			first := f.create(t, "catalog-fixed-original")
			var cfg ModelConfig
			var original relay.Channel
			must(t, f.s.DB.Where("public_model = ?", ark.Model20).First(&cfg).Error)
			must(t, f.s.DB.Where("id = ?", cfg.ChannelID).First(&original).Error)
			other := original
			other.ID = 0
			other.Name = "catalog fixed replacement"
			switch fault {
			case "disabled":
				other.Status = 2
			case "byok":
				other.OwnerOrgID = f.key.OrgID
			case "wrong_endpoint":
				other.BaseURL = "https://example.invalid/api/v3"
			case "wrong_provider":
				other.Provider = "openai"
			}
			must(t, f.s.DB.Create(&other).Error)
			must(t, f.s.DB.Model(&ModelConfig{}).Where("public_model = ?", cfg.PublicModel).Update("channel_id", other.ID).Error)
			before := catalogCreateFacts(t, f, first.ID)
			in := ark.CreateRequest{Model: ark.Model20, Content: []ark.ContentItem{{Type: "text", Text: "new fixed-channel request"}}}
			if _, err := f.s.EstimateForKey(context.Background(), f.key, in); !errors.Is(err, ErrCapability) {
				t.Fatal("estimate borrowed another available official channel", err)
			}
			if _, replay, err := f.s.Create(context.Background(), f.key, f.requestID("fixed"), "catalog-fixed-new", in); !errors.Is(err, ErrCapability) || replay {
				t.Fatal("create borrowed another available official channel", err)
			}
			if catalogCreateFacts(t, f, first.ID) != before || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("invalid fixed-channel admission changed durable effects")
			}
			// A valid explicit rebind is a new acceptance decision, while the old
			// task remains bound to its original channel and original snapshot.
			must(t, f.s.DB.Model(&relay.Channel{}).Where("id = ?", other.ID).Updates(map[string]any{"status": 1, "owner_org_id": 0, "provider": "volcengine", "base_url": original.BaseURL}).Error)
			next, replay, err := f.s.Create(context.Background(), f.key, f.requestID("fixed-valid"), "catalog-fixed-valid", in)
			must(t, err)
			var rows []task.Task
			must(t, f.s.DB.Where("org_id = ? AND task_id IN ?", f.key.OrgID, []string{first.ID, next.ID}).Find(&rows).Error)
			if replay || len(rows) != 2 || f.posts.Load() != 0 || f.gets.Load() != 0 {
				t.Fatal("valid rebind did not create exactly one local acceptance")
			}
			for _, row := range rows {
				want := original.ID
				if row.TaskID == next.ID {
					want = other.ID
				}
				if row.ChannelID != want {
					t.Fatal("explicit channel selection rewrote an accepted task")
				}
			}
		})
	}
}
