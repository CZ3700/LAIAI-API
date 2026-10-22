package videoasset

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOfficialAssetSourceAuthorizesBeforeReadAndNeverStoresMedia(t *testing.T) {
	s, db, now, provider := setup(t)
	group := readyGroup(t, s, "source-group")
	asset := readyAsset(t, s, now, group.ID, "source-asset")
	owner := createContext("source-read")
	ctx := WithCaller(context.Background(), owner)
	before := provider.calls["GetAsset"]
	other := owner
	other.UserID++
	if _, err := s.OfficialSource(WithCaller(context.Background(), other), 1, asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user read source: %v", err)
	}
	if provider.calls["GetAsset"] != before {
		t.Fatal("unauthorized source called provider")
	}
	got, err := s.OfficialSource(ctx, 1, asset.ID)
	if err != nil || got.Source != "upstream" || !strings.HasPrefix(got.URL, "https://private.example/") {
		t.Fatalf("official source unavailable: %+v %v", got, err)
	}
	if provider.calls["GetAsset"] != before+1 {
		t.Fatal("source fetched more than one official read")
	}
	var events []Event
	if err := db.Where("resource_id = ? AND kind LIKE 'official_source_%'", asset.ID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EvidenceRef == "" {
		t.Fatalf("missing source request/result audit: %+v", events)
	}
	for _, e := range events {
		if e.ActorID != callerLabel(owner) {
			t.Fatal("source audit lost caller")
		}
		if strings.Contains(e.EvidenceRef+e.ActorID+e.Kind, "private-media-signature") {
			t.Fatal("source leaked into metadata")
		}
	}
	var uploads int64
	db.Model(&Upload{}).Count(&uploads)
	if uploads != 0 || s.console.Media != nil {
		t.Fatal("provider preview created platform storage")
	}
	provider.project = "different-project"
	if _, err := s.OfficialSource(ctx, 1, asset.ID); err == nil {
		t.Fatal("cross-project source delivered")
	}
	provider.project = testScope().Project
	if err := db.Model(&Asset{}).Where("asset_id = ?", asset.ID).Update("status", StatusDeleting).Error; err != nil {
		t.Fatal(err)
	}
	before = provider.calls["GetAsset"]
	if _, err := s.OfficialSource(ctx, 1, asset.ID); !errors.Is(err, ErrConflict) || provider.calls["GetAsset"] != before {
		t.Fatal("deleting asset source delivered")
	}
}

func TestOfficialAssetSourceWithoutHistoryHasNoResponsePlaintextCapture(t *testing.T) {
	s, db, now, _ := setup(t)
	g := readyGroup(t, s, "source-nohistory")
	a := readyAsset(t, s, now, g.ID, "source-nohistory-asset")
	resolve := s.options.ResolveScope
	s.options.ResolveScope = func(ctx context.Context, org uint64, want Scope) (RuntimeScope, error) {
		r, e := resolve(ctx, org, want)
		r.CaptureHistory = false
		return r, e
	}
	_, err := s.OfficialSource(WithCaller(context.Background(), createContext("source-nohistory-read")), 1, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var captured int64
	if err := db.Table("invocation_payloads").Where("attempt_id LIKE 'asp_%' AND plaintext_bytes > 0").Count(&captured).Error; err != nil || captured != 0 {
		t.Fatalf("history disabled but raw URL captured: %d %v", captured, err)
	}
}
