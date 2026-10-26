package video

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

// At most three reads; any explicit route (even to an unrelated scope or
// disabled) prevents shared-default inheritance. No credential column is read.
func (s *Service) catalogPortraitsTx(ctx context.Context, tx *gorm.DB, org uint64, scopes map[uint64]ProviderScope, policy Policy, policyExists bool) (map[uint64]bool, error) {
	out := map[uint64]bool{}
	if s.Assets == nil || len(scopes) == 0 {
		return out, nil
	}
	set := map[uint64]bool{}
	for id := range scopes {
		set[id] = true
	}
	ids := catalogIDs(set)
	parts, args := []string{}, []any{}
	for _, id := range ids {
		parts = append(parts, "SELECT ? AS provider_scope_id")
		args = append(args, id)
	}
	var routes []struct {
		ProviderScopeID uint64
		ScopeVersion    *uint64
		Enabled         bool
		HasExplicit     bool
	}
	columns := "seeds.provider_scope_id, r.scope_version, r.enabled, EXISTS(SELECT 1 FROM video_asset_org_routes all_routes WHERE all_routes.org_id = "
	// Use a bound scalar subquery in the FROM clause, never interpolate tenant IDs.
	columns += "tenant.org_id) AS has_explicit"
	derived := "(" + strings.Join(parts, " UNION ALL ") + ") AS seeds CROSS JOIN (SELECT ? AS org_id) AS tenant"
	args = append(args, org)
	err := catalogRead(ctx, tx, columns, func(q *gorm.DB) error {
		return q.Table(derived, args...).Joins("LEFT JOIN video_asset_org_routes r ON r.org_id = tenant.org_id AND r.provider_scope_id = seeds.provider_scope_id").Find(&routes).Error
	})
	if err != nil {
		return nil, err
	}
	if len(routes) != len(ids) {
		return nil, ErrCatalogUnavailable
	}
	selected := map[uint64]uint64{}
	explicit := routes[0].HasExplicit
	for _, route := range routes {
		if route.HasExplicit != explicit {
			return nil, ErrCatalogUnavailable
		}
		if explicit && route.Enabled && route.ScopeVersion != nil && *route.ScopeVersion > 0 {
			selected[route.ProviderScopeID] = *route.ScopeVersion
		}
	}
	sharedAsync := false
	if !explicit {
		var shared AssetSharedDefault
		err = catalogRead(ctx, tx, "id, provider_scope_id, scope_version, enabled, allow_async", func(q *gorm.DB) error { return q.Where("id = 1").Take(&shared).Error })
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err == nil && shared.Enabled && shared.AllowAsync && shared.ScopeVersion > 0 {
			if _, found := scopes[shared.ProviderScopeID]; found {
				selected[shared.ProviderScopeID] = shared.ScopeVersion
				sharedAsync = true
			}
		}
	}
	if len(selected) == 0 {
		return out, nil
	}
	var rows []struct {
		ProviderScopeID, Version        uint64
		AccountRef, Region, ProjectName string
		Enabled                         bool
		CapabilitiesJSON                *string `gorm:"column:capabilities_json"`
		CapabilitiesBytes               *int64
	}
	length := catalogLength(tx, "capabilities_json")
	columns = "provider_scope_id, version, account_ref, region, project_name, enabled, " + length + " AS capabilities_bytes, CASE WHEN " + length + " <= 65536 THEN capabilities_json ELSE NULL END AS capabilities_json"
	err = catalogRead(ctx, tx, columns, func(q *gorm.DB) error {
		conditions, values := []string{}, []any{}
		for _, id := range ids {
			if version, ok := selected[id]; ok {
				conditions = append(conditions, "(provider_scope_id = ? AND version = ?)")
				values = append(values, id, version)
			}
		}
		return q.Table("video_asset_scope_configs").Where(strings.Join(conditions, " OR "), values...).Limit(5).Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	if len(rows) > 4 {
		return nil, ErrCatalogLimit
	}
	allowAsync := sharedAsync
	if policyExists {
		allowAsync = policy.AsyncExecutionAllowed && policy.RetentionDays >= 1 && policy.RetentionDays <= 30
	}
	for _, row := range rows {
		if row.CapabilitiesBytes != nil && *row.CapabilitiesBytes > 65536 {
			return nil, ErrCatalogLimit
		}
		provider, exists := scopes[row.ProviderScopeID]
		if !exists || row.Version != selected[row.ProviderScopeID] || !row.Enabled || !validAssetProvider(provider) || row.AccountRef != provider.AccountRef || row.Region != provider.Region || row.ProjectName != provider.ProjectName || row.CapabilitiesJSON == nil || !utf8.ValidString(*row.CapabilitiesJSON) {
			continue
		}
		var caps videoasset.Capabilities
		if json.Unmarshal([]byte(*row.CapabilitiesJSON), &caps) == nil && allowAsync && caps.Portrait {
			out[row.ProviderScopeID] = true
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}
