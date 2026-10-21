package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

// Setup DTOs deliberately enumerate every selected and serialized field. In
// particular, the database evaluates credential presence without selecting its
// ciphertext, and this diagnostic never invokes a credential resolver.
type AssetSetupProvider struct {
	ID          uint64 `json:"id"`
	AccountRef  string `json:"account_ref"`
	Region      string `json:"region"`
	ProjectName string `json:"project_name"`
	Enabled     bool   `json:"enabled"`
}

type AssetSetupConfig struct {
	ProviderScopeID         uint64                  `json:"provider_scope_id"`
	Version                 uint64                  `json:"version"`
	Enabled                 bool                    `json:"enabled"`
	CredentialPresent       bool                    `json:"credential_present"`
	Capabilities            videoasset.Capabilities `json:"capabilities" gorm:"-"`
	CapabilitiesValid       bool                    `json:"capabilities_valid" gorm:"-"`
	IdentityMatchesProvider bool                    `json:"identity_matches_provider" gorm:"-"`
}

type AssetSetupPolicy struct {
	Present               bool `json:"present" gorm:"-"`
	AsyncExecutionAllowed bool `json:"async_execution_allowed"`
	CaptureHistory        bool `json:"capture_history"`
	RetentionDays         int  `json:"retention_days"`
}

type AssetSetupView struct {
	OrgID              uint64                            `json:"org_id"`
	OrgActive          bool                              `json:"org_active"`
	ConfigurationReady bool                              `json:"configuration_ready"`
	OfficialAccess     string                            `json:"official_access"`
	ProviderScopes     []AssetSetupProvider              `json:"provider_scopes"`
	ScopeConfigs       []AssetSetupConfig                `json:"scope_configs"`
	OrgRoutes          []AssetOrgRoute                   `json:"org_routes"`
	Policy             AssetSetupPolicy                  `json:"policy"`
	Deployment         videoasset.ConsoleDeploymentState `json:"deployment"`
	Issues             []string                          `json:"issues"`
}

// AssetSetup reads local configuration only. configuration_ready means its
// local prerequisites are recorded; credential validity, remote connectivity
// and actual official permissions remain not_checked even for a complete setup.
// A Service with DB but no Assets can inspect the disabled gateway safely.
func (s *Service) AssetSetup(ctx context.Context, actor *identity.User, org uint64) (AssetSetupView, error) {
	out := AssetSetupView{OrgID: org, OfficialAccess: "not_checked", ProviderScopes: []AssetSetupProvider{}, ScopeConfigs: []AssetSetupConfig{}, OrgRoutes: []AssetOrgRoute{}, Issues: []string{}}
	if err := assetAdmin(actor); err != nil {
		return out, err
	}
	if org == 0 {
		return out, ErrInvalid
	}
	if s == nil || s.DB == nil {
		return out, ErrUnavailable
	}
	ready := true
	issue := func(code string, blocking bool) {
		out.Issues = append(out.Issues, code)
		if blocking {
			ready = false
		}
	}
	if s.Assets == nil {
		issue("gateway_unavailable", true)
	}
	var returnURL string
	out.Deployment, returnURL = s.Assets.ConsoleSetupState()
	if !out.Deployment.ConsoleConfigured {
		issue("console_not_configured", true)
	}
	if !out.Deployment.MediaConfigured {
		issue("media_not_configured", false)
	}
	if !out.Deployment.PlatformCallbackConfigured {
		issue("platform_callback_not_configured", false)
	}
	if !out.Deployment.PortraitReturnConfigured {
		if u, err := url.Parse(returnURL); err == nil && u.Path == videoasset.PortraitCallbackPath {
			issue("portrait_return_is_callback", false)
		} else {
			issue("portrait_return_not_configured", false)
		}
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var organization struct{ Status int8 }
		if err := tx.Table("organizations").Select("status").Where("id = ?", org).Take(&organization).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrUnavailable
		}
		out.OrgActive = organization.Status == identity.StatusActive
		if !out.OrgActive {
			issue("organization_inactive", true)
		}
		if err := tx.Table("video_provider_scopes").Select("id, account_ref, region, project_name, enabled").Order("id").Find(&out.ProviderScopes).Error; err != nil {
			return ErrUnavailable
		}
		if len(out.ProviderScopes) == 0 {
			issue("provider_scopes_missing", true)
		}
		providers := make(map[uint64]AssetSetupProvider, len(out.ProviderScopes))
		for _, provider := range out.ProviderScopes {
			providers[provider.ID] = provider
		}
		var rows []struct {
			ProviderScopeID, Version        uint64
			Enabled, CredentialPresent      bool
			AccountRef, Region, ProjectName string
			CapabilitiesJSON                string `gorm:"column:capabilities_json"`
		}
		if err := tx.Table("video_asset_scope_configs").Select("provider_scope_id, version, enabled, account_ref, region, project_name, capabilities_json, CASE WHEN credential_cipher IS NOT NULL AND credential_cipher <> '' THEN 1 ELSE 0 END AS credential_present").Order("provider_scope_id, version DESC").Find(&rows).Error; err != nil {
			return ErrUnavailable
		}
		for _, row := range rows {
			config := AssetSetupConfig{ProviderScopeID: row.ProviderScopeID, Version: row.Version, Enabled: row.Enabled, CredentialPresent: row.CredentialPresent}
			config.CapabilitiesValid = strings.HasPrefix(strings.TrimSpace(row.CapabilitiesJSON), "{") && json.Unmarshal([]byte(row.CapabilitiesJSON), &config.Capabilities) == nil
			if !config.CapabilitiesValid {
				config.Capabilities = videoasset.Capabilities{}
			}
			provider, exists := providers[row.ProviderScopeID]
			config.IdentityMatchesProvider = exists && row.AccountRef == provider.AccountRef && row.Region == provider.Region && row.ProjectName == provider.ProjectName
			out.ScopeConfigs = append(out.ScopeConfigs, config)
		}
		if len(out.ScopeConfigs) == 0 {
			issue("management_scope_configs_missing", true)
		}
		if err := tx.Table("video_asset_org_routes").Select("org_id, provider_scope_id, scope_version, enabled, default_route").Where("org_id = ?", org).Order("provider_scope_id").Find(&out.OrgRoutes).Error; err != nil {
			return ErrUnavailable
		}
		var defaults []AssetOrgRoute
		for _, route := range out.OrgRoutes {
			if route.Enabled && route.DefaultRoute {
				defaults = append(defaults, route)
			}
		}
		if len(defaults) == 0 {
			issue("org_default_route_missing", true)
		} else if len(defaults) != 1 {
			issue("org_default_route_ambiguous", true)
		} else {
			route := defaults[0]
			provider, found := providers[route.ProviderScopeID]
			if !found {
				issue("routed_provider_missing", true)
			} else if !provider.Enabled {
				issue("routed_provider_disabled", true)
			} else if !validAssetProvider(ProviderScope{ID: provider.ID, AccountRef: provider.AccountRef, Region: provider.Region, ProjectName: provider.ProjectName, Enabled: provider.Enabled}) {
				issue("routed_provider_invalid", true)
			}
			var config *AssetSetupConfig
			for i := range out.ScopeConfigs {
				if out.ScopeConfigs[i].ProviderScopeID == route.ProviderScopeID && out.ScopeConfigs[i].Version == route.ScopeVersion {
					config = &out.ScopeConfigs[i]
					break
				}
			}
			if config == nil {
				issue("routed_scope_config_missing", true)
			} else {
				if !config.Enabled {
					issue("routed_scope_config_disabled", true)
				}
				if !config.IdentityMatchesProvider {
					issue("scope_identity_mismatch", true)
				}
				if !config.CredentialPresent {
					issue("management_credentials_missing", true)
				}
				if !config.CapabilitiesValid {
					issue("scope_capabilities_invalid", true)
				}
			}
		}
		if err := tx.Table("video_org_policies").Select("async_execution_allowed, capture_history, retention_days").Where("org_id = ?", org).Take(&out.Policy).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUnavailable
			}
			issue("execution_policy_missing", true)
		} else {
			out.Policy.Present = true
			if !out.Policy.AsyncExecutionAllowed {
				issue("async_execution_disabled", true)
			}
		}
		if out.Deployment.PortraitReturnConfigured {
			var count int64
			if len(s.Secret) >= 32 {
				if err := tx.Table("video_asset_callback_registry").Where("org_id = ? AND url_hmac = ? AND revoked_at IS NULL AND expires_at > ? AND value_cipher <> ''", org, s.assetURLHash(org, "callback", returnURL), now).Count(&count).Error; err != nil {
					return ErrUnavailable
				}
			}
			if count == 0 {
				issue("portrait_return_not_registered", false)
			}
			out.Deployment.PortraitTargetReady = ready && out.Deployment.PlatformCallbackConfigured && out.Deployment.CallbackResolversConfigured && count > 0
		}
		return nil
	})
	if err != nil {
		return AssetSetupView{}, err
	}
	out.ConfigurationReady = ready
	return out, nil
}
