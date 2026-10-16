package video

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AssetIntegrationOptions contains process configuration only. Permissions,
// account routing and verified media are explicit administrator-owned rows.
type AssetIntegrationOptions struct {
	PlatformCallbackURL string
	MaxLicenseAge       time.Duration
	Client              *videoasset.ManagementClient
}

type AssetScopeConfig struct {
	ProviderScopeID                 uint64 `gorm:"primaryKey"`
	Version                         uint64 `gorm:"primaryKey"`
	AccountRef, Region, ProjectName string
	CapabilitiesJSON                string `gorm:"column:capabilities_json"`
	CredentialCipher                string `json:"-"`
	Enabled                         bool
	CreatedBy                       uint64
	CreatedAt, UpdatedAt            time.Time
}

func (AssetScopeConfig) TableName() string { return "video_asset_scope_configs" }

type AssetOrgRoute struct {
	OrgID           uint64    `gorm:"primaryKey" json:"org_id"`
	ProviderScopeID uint64    `gorm:"primaryKey" json:"provider_scope_id"`
	ScopeVersion    uint64    `json:"scope_version"`
	Enabled         bool      `json:"enabled"`
	DefaultRoute    bool      `json:"default_route"`
	UpdatedAt       time.Time `json:"-"`
}

func (AssetOrgRoute) TableName() string { return "video_asset_org_routes" }

// Registration inputs are accepted only from an authenticated superadmin.
// They are never returned, logged, or used as public API DTOs.
type AssetScopeRegistration struct {
	ProviderScopeID uint64                  `json:"provider_scope_id"`
	Version         uint64                  `json:"version"`
	Enabled         bool                    `json:"enabled"`
	Capabilities    videoasset.Capabilities `json:"capabilities"`
	AccessKey       string                  `json:"access_key"`
	SecretKey       string                  `json:"secret_key"`
	SessionToken    string                  `json:"session_token"`
}

func (AssetScopeRegistration) String() string   { return "[redacted asset scope registration]" }
func (AssetScopeRegistration) GoString() string { return "[redacted asset scope registration]" }

type AssetMediaRegistration struct {
	ID                 string    `json:"id"`
	OrgID              uint64    `json:"org_id"`
	ClientURL          string    `json:"client_url"`
	FetchURL           string    `json:"fetch_url"`
	MediaType          string    `json:"media_type"`
	ImmutableObjectRef string    `json:"immutable_object_ref"`
	ObjectSHA256       string    `json:"object_sha256"`
	MIME               string    `json:"mime"`
	ByteSize           int64     `json:"byte_size"`
	DurationMS         int64     `json:"duration_ms"`
	Width              int       `json:"width"`
	Height             int       `json:"height"`
	FPSNum             int       `json:"fps_num"`
	FPSDen             int       `json:"fps_den"`
	Version            uint64    `json:"version"`
	VerifiedAt         time.Time `json:"verified_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	EvidenceRef        string    `json:"evidence_ref"`
}

func (AssetMediaRegistration) String() string   { return "[redacted asset media registration]" }
func (AssetMediaRegistration) GoString() string { return "[redacted asset media registration]" }

type AssetMediaRegistry struct {
	ID                                                string `gorm:"primaryKey;column:id"`
	OrgID                                             uint64
	URLHMAC                                           string `gorm:"column:url_hmac"`
	MediaType, ImmutableObjectRef, ObjectSHA256, MIME string
	ByteSize, DurationMS                              int64
	Width, Height, FPSNum, FPSDen                     int
	Version                                           uint64
	VerifiedAt, ExpiresAt                             time.Time
	EvidenceRef                                       string
	ValueCipher                                       string `json:"-"`
	RevokedAt                                         *time.Time
	CreatedBy                                         uint64
	CreatedAt                                         time.Time
}

func (AssetMediaRegistry) TableName() string { return "video_asset_media_registry" }

type AssetCallbackRegistration struct {
	ID          string    `json:"id"`
	OrgID       uint64    `json:"org_id"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expires_at"`
	EvidenceRef string    `json:"evidence_ref"`
}
type AssetCallbackRegistry struct {
	ID          string `gorm:"primaryKey;column:id"`
	OrgID       uint64
	URLHMAC     string `gorm:"column:url_hmac"`
	ValueCipher string `json:"-"`
	EvidenceRef string
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	CreatedBy   uint64
	CreatedAt   time.Time
}

func (AssetCallbackRegistry) TableName() string { return "video_asset_callback_registry" }

// TaskParameters is a closed billing/authorization snapshot. No prompt, URL,
// provider response body, credential or human-person information belongs here.
type TaskParameters struct {
	TaskID                                        string `gorm:"primaryKey;column:task_id"`
	OrgID                                         uint64
	SchemaVersion                                 uint64
	ProviderScopeID, ProviderScopeVersion         uint64
	AccountRef, Region, ProjectName               string
	PublicModel, UpstreamModel, CapabilityVersion string
	ParametersJSON                                string `gorm:"column:parameters_json"`
	HasInputVideo                                 bool
	InputDurationMS                               int64
	CreatedAt                                     time.Time
}

func (TaskParameters) TableName() string { return "video_task_parameters" }

type assetSeal struct {
	Domain  string
	Org     uint64
	Record  string
	Version uint64
	Data    json.RawMessage
}

var assetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func assetID(v string) bool { return assetIDPattern.MatchString(v) }
func assetHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 4096 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(raw, "\r\n\t")
}
func assetAdmin(actor *identity.User) error {
	if actor == nil || actor.ID == 0 || actor.Status != identity.StatusActive || actor.Role != identity.PlatRoleSuperadmin {
		return ErrForbidden
	}
	return nil
}
func (s *Service) assetSeal(domain string, org uint64, id string, version uint64, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", ErrInvalid
	}
	raw, err := json.Marshal(assetSeal{domain, org, id, version, data})
	if err != nil {
		return "", ErrInvalid
	}
	value, err := s.Keyring.Encrypt(string(raw))
	if err != nil {
		return "", ErrUnavailable
	}
	return value, nil
}
func (s *Service) assetOpen(domain string, org uint64, id string, version uint64, cipher string, v any) error {
	raw, err := s.Keyring.Decrypt(cipher)
	if err != nil {
		return ErrUnavailable
	}
	var sealed assetSeal
	if json.Unmarshal([]byte(raw), &sealed) != nil || sealed.Domain != domain || sealed.Org != org || sealed.Record != id || sealed.Version != version || json.Unmarshal(sealed.Data, v) != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) assetURLHash(org uint64, domain, raw string) string {
	return digest(s.Secret, "asset-url:"+domain+":v1", strconv.FormatUint(org, 10), raw)
}
func (s *Service) assetAuditTx(tx *gorm.DB, org uint64, resource, id, kind, evidence, actor string) error {
	idEvent, err := randomID("ave_")
	if err != nil {
		return ErrUnavailable
	}
	return tx.Create(&videoasset.Event{ID: idEvent, OrgID: org, ResourceType: resource, ResourceID: id, Kind: kind, EvidenceRef: evidence, ActorID: actor, CreatedAt: s.Now().UTC()}).Error
}
func (s *Service) lockAssetTenantTx(tx *gorm.DB, org uint64) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&videoasset.TenantLock{OrgID: org}).Error; err != nil {
		return err
	}
	return tx.Model(&videoasset.TenantLock{}).Where("org_id = ?", org).Update("version", gorm.Expr("version + 1")).Error
}

// ConfigureAssets installs non-nil fail-closed resolvers even before any
// provider capability or tenant route has been explicitly enabled.
func (s *Service) ConfigureAssets(options AssetIntegrationOptions) error {
	if s == nil || s.DB == nil || s.Keyring == nil || s.Evidence == nil || len(s.Secret) < 32 {
		return ErrUnavailable
	}
	if options.PlatformCallbackURL != "" {
		u, err := url.Parse(options.PlatformCallbackURL)
		if err != nil || !assetHTTPS(options.PlatformCallbackURL) || u.Path != videoasset.PortraitCallbackPath || u.RawQuery != "" {
			return ErrInvalid
		}
	}
	mac := hmac.New(sha256.New, s.Secret)
	_, _ = mac.Write([]byte("video-asset-integration-hmac:v1"))
	a, err := videoasset.NewService(s.DB, s.Keyring, s.Evidence, videoasset.Options{
		Now: func() time.Time { return s.Now().UTC() }, ResolveScope: s.resolveAssetRuntime,
		VerifyMedia: s.verifyRegisteredAssetMedia, ResolveCallback: s.resolveAssetCallbackURL,
		ResolveCallbackByID: s.resolveAssetCallbackID, PlatformCallbackURL: options.PlatformCallbackURL,
		HMACKeys: map[string][]byte{"v1": mac.Sum(nil)}, ActiveHMACKey: "v1", MaxLicenseAge: options.MaxLicenseAge, Client: options.Client,
	})
	if err != nil {
		return err
	}
	s.Assets = a
	return nil
}

func (s *Service) RegisterAssetScope(ctx context.Context, actor *identity.User, input AssetScopeRegistration) error {
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if input.ProviderScopeID == 0 || input.Version == 0 || len(input.AccessKey) < 8 || len(input.SecretKey) < 8 || len(input.AccessKey) > 1024 || len(input.SecretKey) > 4096 || len(input.SessionToken) > 8192 || strings.ContainsAny(input.AccessKey+input.SecretKey+input.SessionToken, "\r\n\x00") {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var provider ProviderScope
		if err := locked(tx).Where("id = ? AND enabled = ?", input.ProviderScopeID, true).First(&provider).Error; err != nil {
			return ErrCapability
		}
		if !validAssetProvider(provider) {
			return ErrCapability
		}
		caps, _ := json.Marshal(input.Capabilities)
		row := AssetScopeConfig{ProviderScopeID: provider.ID, Version: input.Version, AccountRef: provider.AccountRef, Region: provider.Region, ProjectName: provider.ProjectName, CapabilitiesJSON: string(caps), Enabled: input.Enabled, CreatedBy: actor.ID, CreatedAt: s.Now().UTC(), UpdatedAt: s.Now().UTC()}
		credentials := struct{ Account, Region, Project, AccessKey, SecretKey, SessionToken string }{provider.AccountRef, provider.Region, provider.ProjectName, input.AccessKey, input.SecretKey, input.SessionToken}
		var err error
		row.CredentialCipher, err = s.assetSeal("management", 0, strconv.FormatUint(provider.ID, 10), input.Version, credentials)
		if err != nil {
			return err
		}
		var count int64
		if tx.Model(&AssetScopeConfig{}).Where("provider_scope_id = ? AND version = ?", row.ProviderScopeID, row.Version).Count(&count).Error != nil {
			return ErrUnavailable
		}
		if count != 0 {
			return ErrConflict
		}
		if err = tx.Create(&row).Error; err != nil {
			return ErrUnavailable
		}
		return s.assetAuditTx(tx, 0, "asset_scope", fmt.Sprintf("%d_v%d", row.ProviderScopeID, row.Version), "scope_registered", "", strconv.FormatUint(actor.ID, 10))
	})
}
func validAssetProvider(p ProviderScope) bool {
	return p.ID > 0 && p.Enabled && p.Region == "cn-beijing" && assetID(p.AccountRef) && assetID(p.ProjectName)
}
func (s *Service) SetAssetScopeEnabled(ctx context.Context, actor *identity.User, scopeID, version uint64, enabled bool) error {
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if scopeID == 0 || version == 0 {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row AssetScopeConfig
		if locked(tx).Where("provider_scope_id = ? AND version = ?", scopeID, version).First(&row).Error != nil {
			return ErrNotFound
		}
		if err := tx.Model(&row).Updates(map[string]any{"enabled": enabled, "updated_at": s.Now().UTC()}).Error; err != nil {
			return ErrUnavailable
		}
		return s.assetAuditTx(tx, 0, "asset_scope", fmt.Sprintf("%d_v%d", scopeID, version), "scope_enabled_changed", "", strconv.FormatUint(actor.ID, 10))
	})
}
func (s *Service) SetOrgAssetRoute(ctx context.Context, actor *identity.User, input AssetOrgRoute) error {
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if input.OrgID == 0 || input.ProviderScopeID == 0 || input.ScopeVersion == 0 {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockAssetTenantTx(tx, input.OrgID); err != nil {
			return err
		}
		var org identity.Organization
		if tx.First(&org, input.OrgID).Error != nil {
			return ErrNotFound
		}
		var config AssetScopeConfig
		if tx.Where("provider_scope_id = ? AND version = ?", input.ProviderScopeID, input.ScopeVersion).First(&config).Error != nil {
			return ErrCapability
		}
		if input.Enabled && !config.Enabled {
			return ErrCapability
		}
		if input.DefaultRoute {
			if err := tx.Model(&AssetOrgRoute{}).Where("org_id = ?", input.OrgID).Update("default_route", false).Error; err != nil {
				return ErrUnavailable
			}
		}
		input.UpdatedAt = s.Now().UTC()
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "org_id"}, {Name: "provider_scope_id"}}, DoUpdates: clause.AssignmentColumns([]string{"scope_version", "enabled", "default_route", "updated_at"})}).Create(&input).Error; err != nil {
			return ErrUnavailable
		}
		return s.assetAuditTx(tx, input.OrgID, "asset_route", strconv.FormatUint(input.ProviderScopeID, 10), "route_configured", "", strconv.FormatUint(actor.ID, 10))
	})
}

func assetScopeID(id uint64) string { return "scope_" + strconv.FormatUint(id, 10) }
func (s *Service) assetScopeTx(tx *gorm.DB, org uint64, providerID uint64, requested videoasset.Scope, credentials bool) (videoasset.RuntimeScope, error) {
	var out videoasset.RuntimeScope
	if org == 0 {
		return out, ErrForbidden
	}
	var route AssetOrgRoute
	q := tx.Where("org_id = ? AND enabled = ?", org, true)
	if providerID != 0 {
		q = q.Where("provider_scope_id = ?", providerID)
	} else if requested.ID != "" {
		if !strings.HasPrefix(requested.ID, "scope_") {
			return out, ErrCapability
		}
		id, err := strconv.ParseUint(strings.TrimPrefix(requested.ID, "scope_"), 10, 64)
		if err != nil || assetScopeID(id) != requested.ID {
			return out, ErrCapability
		}
		q = q.Where("provider_scope_id = ?", id)
	} else {
		q = q.Where("default_route = ?", true)
	}
	if q.First(&route).Error != nil {
		return out, ErrCapability
	}
	var config AssetScopeConfig
	if tx.Where("provider_scope_id = ? AND version = ? AND enabled = ?", route.ProviderScopeID, route.ScopeVersion, true).First(&config).Error != nil {
		return out, ErrCapability
	}
	var provider ProviderScope
	if tx.First(&provider, route.ProviderScopeID).Error != nil || !validAssetProvider(provider) || provider.AccountRef != config.AccountRef || provider.Region != config.Region || provider.ProjectName != config.ProjectName {
		return out, ErrCapability
	}
	out.Scope = videoasset.Scope{ID: assetScopeID(provider.ID), Version: config.Version, AccountID: provider.AccountRef, Region: provider.Region, Project: provider.ProjectName}
	if requested.ID != "" && requested != out.Scope {
		return out, ErrCapability
	}
	if json.Unmarshal([]byte(config.CapabilitiesJSON), &out.Capabilities) != nil {
		return out, ErrCapability
	}
	var policy Policy
	if tx.Where("org_id = ?", org).First(&policy).Error != nil {
		return out, ErrForbidden
	}
	out.ExecutionRetentionConsent = policy.AsyncExecutionAllowed
	out.CaptureHistory = policy.CaptureHistory
	if credentials {
		var c struct{ Account, Region, Project, AccessKey, SecretKey, SessionToken string }
		if err := s.assetOpen("management", 0, strconv.FormatUint(provider.ID, 10), config.Version, config.CredentialCipher, &c); err != nil {
			return out, err
		}
		if c.Account != provider.AccountRef || c.Region != provider.Region || c.Project != provider.ProjectName || c.AccessKey == "" || c.SecretKey == "" {
			return out, ErrCapability
		}
		out.Credentials = videoasset.Credentials{AccessKey: c.AccessKey, SecretKey: c.SecretKey, SessionToken: c.SessionToken}
	}
	return out, nil
}
func (s *Service) resolveAssetRuntime(ctx context.Context, org uint64, requested videoasset.Scope) (videoasset.RuntimeScope, error) {
	var out videoasset.RuntimeScope
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = s.assetScopeTx(tx, org, 0, requested, true)
		return err
	})
	if err != nil {
		return videoasset.RuntimeScope{}, err
	}
	return out, nil
}

func validRegisteredMedia(in AssetMediaRegistration, now time.Time) bool {
	hash, err := hex.DecodeString(in.ObjectSHA256)
	if !assetID(in.ID) || in.OrgID == 0 || !assetID(in.ImmutableObjectRef) || !assetID(in.EvidenceRef) || !assetHTTPS(in.ClientURL) || !assetHTTPS(in.FetchURL) || err != nil || len(hash) != 32 || in.ObjectSHA256 != strings.ToLower(in.ObjectSHA256) || in.Version == 0 || in.ByteSize <= 0 || in.VerifiedAt.After(now) || now.Sub(in.VerifiedAt) > time.Hour || !in.ExpiresAt.After(now) || in.ExpiresAt.After(in.VerifiedAt.Add(time.Hour)) {
		return false
	}
	switch in.MediaType {
	case "image":
		return in.ByteSize < 30_000_000 && in.Width > 300 && in.Width < 6000 && in.Height > 300 && in.Height < 6000 && int64(in.Width)*10 > int64(in.Height)*4 && int64(in.Width)*2 < int64(in.Height)*5 && strings.Contains("|image/jpeg|image/png|image/webp|image/tiff|image/gif|image/heic|image/heif|", "|"+in.MIME+"|")
	case "video":
		return in.ByteSize <= 200_000_000 && in.DurationMS >= 2000 && in.DurationMS <= 30000 && in.Width >= 300 && in.Width <= 6000 && in.Height >= 300 && in.Height <= 6000 && int64(in.Width)*10 >= int64(in.Height)*4 && int64(in.Width)*2 <= int64(in.Height)*5 && int64(in.Width)*int64(in.Height) >= 407696 && int64(in.Width)*int64(in.Height) <= 8295044 && in.FPSDen > 0 && in.FPSDen <= 1_000_000 && int64(in.FPSNum) >= 24*int64(in.FPSDen) && int64(in.FPSNum) <= 60*int64(in.FPSDen) && (in.MIME == "video/mp4" || in.MIME == "video/quicktime")
	case "audio":
		return in.ByteSize <= 15_000_000 && in.DurationMS >= 2000 && in.DurationMS <= 30000 && (in.MIME == "audio/mpeg" || in.MIME == "audio/wav" || in.MIME == "audio/x-wav")
	}
	return false
}
func (s *Service) RegisterAssetMedia(ctx context.Context, actor *identity.User, input AssetMediaRegistration) error {
	input.VerifiedAt = input.VerifiedAt.UTC().Truncate(time.Millisecond)
	input.ExpiresAt = input.ExpiresAt.UTC().Truncate(time.Millisecond)
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if !validRegisteredMedia(input, s.Now().UTC()) {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockAssetTenantTx(tx, input.OrgID); err != nil {
			return err
		}
		var org identity.Organization
		if tx.First(&org, input.OrgID).Error != nil {
			return ErrNotFound
		}
		row := AssetMediaRegistry{ID: input.ID, OrgID: input.OrgID, URLHMAC: s.assetURLHash(input.OrgID, "media", input.ClientURL), MediaType: input.MediaType, ImmutableObjectRef: input.ImmutableObjectRef, ObjectSHA256: input.ObjectSHA256, MIME: input.MIME, ByteSize: input.ByteSize, DurationMS: input.DurationMS, Width: input.Width, Height: input.Height, FPSNum: input.FPSNum, FPSDen: input.FPSDen, Version: input.Version, VerifiedAt: input.VerifiedAt, ExpiresAt: input.ExpiresAt, EvidenceRef: input.EvidenceRef, CreatedBy: actor.ID, CreatedAt: s.Now().UTC()}
		var err error
		row.ValueCipher, err = s.assetSeal("media", input.OrgID, input.ID, input.Version, input)
		if err != nil {
			return err
		}
		var count int64
		if tx.Model(&AssetMediaRegistry{}).Where("id = ?", input.ID).Count(&count).Error != nil {
			return ErrUnavailable
		}
		if count != 0 {
			return ErrConflict
		}
		if err = tx.Create(&row).Error; err != nil {
			return ErrUnavailable
		}
		return s.assetAuditTx(tx, input.OrgID, "registered_media", input.ID, "media_registered", input.EvidenceRef, strconv.FormatUint(actor.ID, 10))
	})
}
func (s *Service) registeredMediaTx(tx *gorm.DB, org uint64, raw, kind string) (videoasset.MediaEvidence, error) {
	var out videoasset.MediaEvidence
	if org == 0 || !assetHTTPS(raw) {
		return out, ErrInvalid
	}
	var row AssetMediaRegistry
	err := locked(tx).Where("org_id = ? AND url_hmac = ? AND media_type = ? AND revoked_at IS NULL AND expires_at > ?", org, s.assetURLHash(org, "media", raw), kind, s.Now().UTC()).Order("verified_at DESC, id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, ErrUnavailable
	}
	var input AssetMediaRegistration
	if err := s.assetOpen("media", org, row.ID, row.Version, row.ValueCipher, &input); err != nil {
		return out, err
	}
	if input.ID != row.ID || input.OrgID != org || input.ClientURL != raw || input.MediaType != kind || !validRegisteredMedia(input, s.Now().UTC()) || input.ObjectSHA256 != row.ObjectSHA256 || input.ImmutableObjectRef != row.ImmutableObjectRef || input.ByteSize != row.ByteSize || input.DurationMS != row.DurationMS || input.Width != row.Width || input.Height != row.Height || input.FPSNum != row.FPSNum || input.FPSDen != row.FPSDen || input.MIME != row.MIME || input.EvidenceRef != row.EvidenceRef || !input.ExpiresAt.Equal(row.ExpiresAt) || !input.VerifiedAt.Equal(row.VerifiedAt) {
		return out, ErrUnavailable
	}
	if err := s.assetAuditTx(tx, org, "registered_media", row.ID, "media_resolved", row.EvidenceRef, "gateway"); err != nil {
		return out, ErrUnavailable
	}
	out = videoasset.MediaEvidence{ImmutableObjectRef: input.ImmutableObjectRef, ObjectSHA256: input.ObjectSHA256, MIME: input.MIME, FetchURL: input.FetchURL, ByteSize: input.ByteSize, DurationMS: input.DurationMS, Width: input.Width, Height: input.Height, FPSNum: input.FPSNum, FPSDen: input.FPSDen, Version: input.Version, VerifiedAt: input.VerifiedAt}
	return out, nil
}
func (s *Service) verifyRegisteredAssetMedia(ctx context.Context, org uint64, raw, kind string) (videoasset.MediaEvidence, error) {
	var out videoasset.MediaEvidence
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var err error; out, err = s.registeredMediaTx(tx, org, raw, kind); return err })
	if err != nil {
		return videoasset.MediaEvidence{}, err
	}
	return out, nil
}
func (s *Service) RegisterAssetCallback(ctx context.Context, actor *identity.User, input AssetCallbackRegistration) error {
	input.ExpiresAt = input.ExpiresAt.UTC().Truncate(time.Millisecond)
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if !assetID(input.ID) || input.OrgID == 0 || !assetHTTPS(input.URL) || !assetID(input.EvidenceRef) || !input.ExpiresAt.After(s.Now().UTC()) || input.ExpiresAt.After(s.Now().UTC().Add(365*24*time.Hour)) {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockAssetTenantTx(tx, input.OrgID); err != nil {
			return err
		}
		var org identity.Organization
		if tx.First(&org, input.OrgID).Error != nil {
			return ErrNotFound
		}
		row := AssetCallbackRegistry{ID: input.ID, OrgID: input.OrgID, URLHMAC: s.assetURLHash(input.OrgID, "callback", input.URL), ExpiresAt: input.ExpiresAt, EvidenceRef: input.EvidenceRef, CreatedBy: actor.ID, CreatedAt: s.Now().UTC()}
		var err error
		row.ValueCipher, err = s.assetSeal("callback", input.OrgID, input.ID, 1, input)
		if err != nil {
			return err
		}
		var count int64
		if tx.Model(&AssetCallbackRegistry{}).Where("id = ?", input.ID).Count(&count).Error != nil {
			return ErrUnavailable
		}
		if count != 0 {
			return ErrConflict
		}
		if err = tx.Create(&row).Error; err != nil {
			return ErrUnavailable
		}
		return s.assetAuditTx(tx, input.OrgID, "registered_callback", input.ID, "callback_registered", input.EvidenceRef, strconv.FormatUint(actor.ID, 10))
	})
}
func (s *Service) assetCallback(ctx context.Context, org uint64, lookup string, byID bool) (videoasset.CallbackTarget, error) {
	var out videoasset.CallbackTarget
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := locked(tx).Where("org_id = ? AND revoked_at IS NULL AND expires_at > ?", org, s.Now().UTC())
		if byID {
			if !assetID(lookup) {
				return ErrInvalid
			}
			q = q.Where("id = ?", lookup)
		} else {
			if !assetHTTPS(lookup) {
				return ErrInvalid
			}
			q = q.Where("url_hmac = ?", s.assetURLHash(org, "callback", lookup))
		}
		var row AssetCallbackRegistry
		if q.Order("created_at DESC, id DESC").First(&row).Error != nil {
			return ErrForbidden
		}
		var input AssetCallbackRegistration
		if err := s.assetOpen("callback", org, row.ID, 1, row.ValueCipher, &input); err != nil {
			return err
		}
		if input.ID != row.ID || input.OrgID != org || !input.ExpiresAt.Equal(row.ExpiresAt) || input.EvidenceRef != row.EvidenceRef || !assetHTTPS(input.URL) || s.assetURLHash(org, "callback", input.URL) != row.URLHMAC || !byID && input.URL != lookup {
			return ErrUnavailable
		}
		if err := s.assetAuditTx(tx, org, "registered_callback", row.ID, "callback_resolved", row.EvidenceRef, "gateway"); err != nil {
			return ErrUnavailable
		}
		out = videoasset.CallbackTarget{ID: row.ID, URL: input.URL}
		return nil
	})
	if err != nil {
		return videoasset.CallbackTarget{}, err
	}
	return out, nil
}
func (s *Service) resolveAssetCallbackURL(ctx context.Context, org uint64, raw string) (videoasset.CallbackTarget, error) {
	return s.assetCallback(ctx, org, raw, false)
}
func (s *Service) resolveAssetCallbackID(ctx context.Context, org uint64, id string) (videoasset.CallbackTarget, error) {
	return s.assetCallback(ctx, org, id, true)
}
func (s *Service) revokeAssetRegistration(ctx context.Context, actor *identity.User, org uint64, id string, callback bool) error {
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if org == 0 || !assetID(id) {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.lockAssetTenantTx(tx, org); err != nil {
			return err
		}
		var model any = &AssetMediaRegistry{}
		kind := "media_revoked"
		if callback {
			model = &AssetCallbackRegistry{}
			kind = "callback_revoked"
		}
		res := tx.Model(model).Where("org_id = ? AND id = ? AND revoked_at IS NULL", org, id).Updates(map[string]any{"revoked_at": s.Now().UTC(), "value_cipher": ""})
		if res.Error != nil {
			return ErrUnavailable
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return s.assetAuditTx(tx, org, "registry", id, kind, "", strconv.FormatUint(actor.ID, 10))
	})
}
func (s *Service) RevokeAssetMedia(ctx context.Context, actor *identity.User, org uint64, id string) error {
	return s.revokeAssetRegistration(ctx, actor, org, id, false)
}
func (s *Service) RevokeAssetCallback(ctx context.Context, actor *identity.User, org uint64, id string) error {
	return s.revokeAssetRegistration(ctx, actor, org, id, true)
}

type taskResourcePlan struct {
	Scope              videoasset.Scope
	References         []videoasset.ReferenceInput
	Bindings           []videoasset.Binding
	InputDurationMS    int64
	HasUnverifiedVideo bool
}

func (s *Service) taskBindingsTx(tx *gorm.DB, org uint64, in ark.CreateRequest, provider ProviderScope, cap ark.Capability) (ark.CreateRequest, ark.Bindings, taskResourcePlan, error) {
	bindings := ark.Bindings{Assets: map[string]ark.AssetBinding{}, Drafts: map[string]ark.DraftBinding{}}
	resources := taskResourcePlan{}
	if len(in.Content) == 0 || cap.MaxContentItems <= 0 || len(in.Content) > cap.MaxContentItems {
		return in, bindings, resources, ErrInvalid
	}
	// Clone the request before replacing verified URLs; the original is retained
	// only by the separately governed Invocation Store and idempotency digest.
	raw, _ := json.Marshal(in)
	var effective ark.CreateRequest
	if json.Unmarshal(raw, &effective) != nil {
		return in, bindings, resources, ErrInvalid
	}
	for _, item := range effective.Content {
		if item.Type == "draft_task" && len(effective.Content) != 1 {
			return in, bindings, resources, ErrInvalid
		}
	}
	for _, item := range effective.Content {
		if item.ImageURL != nil || item.VideoURL != nil || item.AudioURL != nil {
			var tenant videoasset.TenantLock
			if err := locked(tx).Where("org_id = ?", org).Take(&tenant).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return in, bindings, resources, ErrUnavailable
			}
			break
		}
	}
	for index, item := range effective.Content {
		var ref *ark.MediaReference
		kind := ""
		switch item.Type {
		case "image_url":
			ref = item.ImageURL
			kind = "image"
		case "video_url":
			ref = item.VideoURL
			kind = "video"
		case "audio_url":
			ref = item.AudioURL
			kind = "audio"
		}
		if ref != nil {
			if strings.HasPrefix(ref.URL, "asset://") {
				role := item.Role
				if role == "" {
					role = "reference_" + kind
				}
				resources.References = append(resources.References, videoasset.ReferenceInput{AssetID: strings.TrimPrefix(ref.URL, "asset://"), Role: role, ContentIndex: index})
			} else {
				media, err := s.registeredMediaTx(tx, org, ref.URL, kind)
				if errors.Is(err, ErrNotFound) {
					// Raw HTTPS is transparently passed to Ark. We make no
					// immutability or measured-duration claim for this URL.
					if kind == "video" {
						resources.HasUnverifiedVideo = true
					}
				} else if err != nil {
					return in, bindings, resources, err
				} else {
					ref.URL = media.FetchURL
					if kind == "video" {
						resources.InputDurationMS += media.DurationMS
					}
				}
			}
		}
		if item.Type == "draft_task" && item.DraftTask != nil {
			parent, st, err := loadTask(tx, org, item.DraftTask.ID, true)
			if err != nil {
				return in, bindings, resources, ErrNotFound
			}
			var metadata TaskParameters
			if tx.Where("org_id = ? AND task_id = ?", org, parent.TaskID).First(&metadata).Error != nil {
				return in, bindings, resources, ErrState
			}
			var params ark.EffectiveParameters
			if json.Unmarshal([]byte(metadata.ParametersJSON), &params) != nil || metadata.SchemaVersion != 1 || !params.Draft || parent.Status != string(ark.StatusSucceeded) || st.DeletedAt != nil || st.DeletionOperationID != "" || parent.UpstreamTaskID == "" || parent.Model != in.Model || metadata.PublicModel != in.Model || st.UpstreamModel != cap.UpstreamModel || metadata.UpstreamModel != cap.UpstreamModel || metadata.CapabilityVersion != st.CapabilityVersion || metadata.CapabilityVersion != cap.Version || st.ProviderScopeID != provider.ID || metadata.ProviderScopeID != provider.ID || metadata.AccountRef != provider.AccountRef || metadata.Region != provider.Region || metadata.ProjectName != provider.ProjectName {
				return in, bindings, resources, ErrState
			}
			bindings.Drafts[parent.TaskID] = ark.DraftBinding{UpstreamTaskID: parent.UpstreamTaskID, PublicModel: parent.Model, CapabilityVersion: metadata.CapabilityVersion, Parameters: params, HasInputVideo: metadata.HasInputVideo}
			resources.InputDurationMS = metadata.InputDurationMS
			var inherited []videoasset.TaskReference
			if tx.Where("org_id = ? AND task_id = ?", org, parent.TaskID).Order("content_index").Find(&inherited).Error != nil {
				return in, bindings, resources, ErrUnavailable
			}
			for _, r := range inherited {
				resources.References = append(resources.References, videoasset.ReferenceInput{AssetID: r.AssetID, Role: r.Role, ContentIndex: r.ContentIndex})
			}
			if len(inherited) > 0 {
				resources.Scope = videoasset.Scope{ID: assetScopeID(provider.ID), Version: metadata.ProviderScopeVersion, AccountID: provider.AccountRef, Region: provider.Region, Project: provider.ProjectName}
			}
		}
	}
	if len(resources.References) > 0 {
		if s.Assets == nil {
			return in, bindings, resources, ErrCapability
		}
		runtime, err := s.assetScopeTx(tx, org, provider.ID, resources.Scope, false)
		if err != nil {
			return in, bindings, resources, err
		}
		resources.Scope = runtime.Scope
		resolved, err := s.Assets.ValidateBindingsTx(tx, org, runtime.Scope, resources.References)
		if err != nil {
			return in, bindings, resources, err
		}
		resources.Bindings = resolved
		for _, b := range resolved {
			if b.AuthorizationVersion > 0 && !runtime.Capabilities.Portrait {
				return in, bindings, resources, ErrCapability
			}
			if b.MediaType == "image" && !runtime.Capabilities.Images || b.MediaType == "video" && !runtime.Capabilities.Videos || b.MediaType == "audio" && !runtime.Capabilities.Audio {
				return in, bindings, resources, ErrCapability
			}
			bindings.Assets[b.AssetID] = ark.AssetBinding{MediaType: b.MediaType, UpstreamURI: "asset://" + b.ProviderAssetID}
			if b.MediaType == "video" && len(bindings.Drafts) == 0 {
				resources.InputDurationMS += b.DurationMS
			}
		}
	}
	return effective, bindings, resources, nil
}
func (s *Service) persistTaskResourcesTx(tx *gorm.DB, org uint64, taskID string, p createPlan) error {
	if len(p.resources.References) > 0 {
		if s.Assets == nil {
			return ErrCapability
		}
		actual, err := s.Assets.ResolveBindingsTx(tx, org, p.resources.Scope, taskID, p.resources.References)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(p.resources.Bindings)
		got, _ := json.Marshal(actual)
		if string(expected) != string(got) {
			return ErrConflict
		}
	}
	params, err := json.Marshal(p.normalized.Parameters)
	if err != nil {
		return ErrInvalid
	}
	row := TaskParameters{TaskID: taskID, OrgID: org, SchemaVersion: 1, ProviderScopeID: p.scope.ID, ProviderScopeVersion: p.resources.Scope.Version, AccountRef: p.scope.AccountRef, Region: p.scope.Region, ProjectName: p.scope.ProjectName, PublicModel: p.normalized.PublicModel, UpstreamModel: p.normalized.UpstreamModel, CapabilityVersion: p.normalized.CapabilityVersion, ParametersJSON: string(params), HasInputVideo: p.normalized.HasInputVideo, InputDurationMS: p.resources.InputDurationMS, CreatedAt: s.Now().UTC()}
	return tx.Create(&row).Error
}

// afterAssetsTerminalTx is intentionally a no-op when asset integration was
// not installed. It never deletes the historical reference rows.
func (s *Service) afterAssetsTerminalTx(tx *gorm.DB, org uint64, taskID string) error {
	if s.Assets == nil {
		return nil
	}
	return s.Assets.ReleaseBindingsTx(tx, org, taskID)
}

// ValidateTaskAssetsTx revalidates a persisted task immediately before its
// first provider POST. Callers lock the task first, then call this before any
// ledger/quota locks. A pre-send rejection is known not-sent; never use this
// check to reinterpret an already-sent or ambiguous provider operation.
func (s *Service) ValidateTaskAssetsTx(tx *gorm.DB, org uint64, taskID string) error {
	if tx == nil || org == 0 || !assetID(taskID) {
		return ErrInvalid
	}
	var refs []videoasset.TaskReference
	if err := tx.Where("org_id = ? AND task_id = ?", org, taskID).Order("content_index").Find(&refs).Error; err != nil {
		return ErrUnavailable
	}
	if len(refs) == 0 {
		return nil
	}
	if s.Assets == nil {
		return ErrCapability
	}
	var metadata TaskParameters
	if tx.Where("org_id = ? AND task_id = ?", org, taskID).First(&metadata).Error != nil {
		return ErrState
	}
	want := videoasset.Scope{ID: assetScopeID(metadata.ProviderScopeID), Version: metadata.ProviderScopeVersion, AccountID: metadata.AccountRef, Region: metadata.Region, Project: metadata.ProjectName}
	runtime, err := s.assetScopeTx(tx, org, metadata.ProviderScopeID, want, false)
	if err != nil {
		return err
	}
	inputs := make([]videoasset.ReferenceInput, 0, len(refs))
	versions := make(map[int]uint64, len(refs))
	for _, ref := range refs {
		if ref.ReleasedAt != nil {
			return ErrState
		}
		inputs = append(inputs, videoasset.ReferenceInput{AssetID: ref.AssetID, Role: ref.Role, ContentIndex: ref.ContentIndex})
		versions[ref.ContentIndex] = ref.AssetMetadataVersion
	}
	resolved, err := s.Assets.ValidateBindingsTx(tx, org, want, inputs)
	if err != nil {
		return err
	}
	for _, b := range resolved {
		if b.MetadataVersion != versions[b.ContentIndex] || b.AuthorizationVersion > 0 && !runtime.Capabilities.Portrait || b.MediaType == "image" && !runtime.Capabilities.Images || b.MediaType == "video" && !runtime.Capabilities.Videos || b.MediaType == "audio" && !runtime.Capabilities.Audio {
			return ErrCapability
		}
	}
	return nil
}
