package videoasset

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

type AssetSource struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

// OfficialSource reads a scoped short-lived URL, never the media bytes. Each
// explicit preview is authorized before the official request and again before
// delivery; neither the source URL nor the raw reply is stored in ordinary rows.
func (s *Service) OfficialSource(ctx context.Context, org uint64, id string) (AssetSource, error) {
	caller, present := callerFrom(ctx)
	if !present || !validCaller(caller, org) || !validID(caller.RequestID) {
		return AssetSource{}, ErrNotFound
	}
	var a Asset
	if !validID(id) || owned(ctx, s.db.WithContext(ctx), org, "asset", "asset_id").Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", org, id).Take(&a).Error != nil {
		return AssetSource{}, ErrNotFound
	}
	if a.ProviderID == nil || a.Status != StatusActive {
		return AssetSource{}, ErrConflict
	}
	var g Group
	if owned(ctx, s.db.WithContext(ctx), org, "asset_group", "group_id").Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", org, a.GroupID).Take(&g).Error != nil {
		return AssetSource{}, ErrNotFound
	}
	if g.ProviderID == nil || g.Status != StatusActive || g.ReviewState != ReviewClear || a.ScopeID != g.ScopeID || a.ScopeVersion != g.ScopeVersion {
		return AssetSource{}, ErrConflict
	}
	r, err := s.resolve(ctx, org, groupScope(g))
	if err != nil {
		return AssetSource{}, err
	}
	attemptID, err := newID("asp_")
	if err != nil {
		return AssetSource{}, ErrStorage
	}
	if err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.event(tx, org, "asset", id, "", "official_source_requested", "", callerLabel(caller))
	}); err != nil {
		return AssetSource{}, err
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, "GetAsset", bodyJSON(map[string]string{"Id": *a.ProviderID, "ProjectName": g.Project}), s.now())
	defer clear(result.Body)
	defer clear(result.Data)
	prepared, err := s.prepareHistoryEvidence(r, invocation.VideoPayloadInput{OrgID: org, RequestID: caller.RequestID, AttemptID: attemptID, Direction: invocation.VideoUpstreamResponse, SchemaVersion: 1, Body: result.Body, SecretValues: []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken}})
	if err != nil {
		return AssetSource{}, ErrStorage
	}
	var official upstreamAsset
	valid := result.Outcome == "accepted" && json.Unmarshal(result.Data, &official) == nil && official.ID == *a.ProviderID && official.GroupID == *g.ProviderID && official.ProjectName == g.Project && strings.ToLower(official.AssetType) == a.MediaType && official.Status == "Active" && publicAssetSourceURL(official.URL)
	for _, secret := range []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken} {
		if secret != "" && strings.Contains(official.URL, secret) {
			valid = false
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, org); err != nil {
			return err
		}
		if err := resourceVisible(ctx, tx, org, "asset", id); err != nil {
			return err
		}
		var current Asset
		if tx.Where("org_id = ? AND asset_id = ? AND row_version = ? AND status = ? AND deleted_at IS NULL", org, id, a.RowVersion, StatusActive).Take(&current).Error != nil {
			return ErrConflict
		}
		var group Group
		if tx.Where("org_id = ? AND group_id = ? AND row_version = ? AND status = ? AND review_state = ? AND deleted_at IS NULL", org, g.ID, g.RowVersion, StatusActive, ReviewClear).Take(&group).Error != nil {
			return ErrConflict
		}
		ref, err := s.evidence.StageTx(tx, prepared)
		if err != nil {
			return ErrStorage
		}
		kind := "official_source_read"
		if !valid {
			kind = "official_source_unavailable"
		}
		return s.event(tx, org, "asset", id, "", kind, ref, callerLabel(caller))
	})
	if err != nil {
		return AssetSource{}, err
	}
	if !valid {
		return AssetSource{}, ErrConflict
	}
	return AssetSource{URL: official.URL, Source: "upstream"}, nil
}
