package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/laiai/laiai-next/core/internal/dberr"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type AssetProjectRegistration struct {
	SourceProviderScopeID uint64 `json:"source_provider_scope_id"`
	ProjectName           string `json:"project_name"`
}

type AssetProjectRegistrationResult struct {
	ProviderScope AssetSetupProvider `json:"provider_scope"`
}

func assetProjectScopeKey(account, region, project string) string {
	raw, _ := json.Marshal([]string{"asset-project:v1", account, region, project})
	sum := sha256.Sum256(raw)
	return "asset-project:" + hex.EncodeToString(sum[:])
}

// RegisterAssetProject records a local identity, not a cloud project or a paid
// video admission. An enabled identity permits later explicit asset management
// configuration; it supplies no model, price, quota, credentials or org route.
func (s *Service) RegisterAssetProject(ctx context.Context, actor *identity.User, input AssetProjectRegistration) (AssetProjectRegistrationResult, error) {
	var out AssetProjectRegistrationResult
	if err := assetAdmin(actor); err != nil {
		return out, err
	}
	if input.SourceProviderScopeID == 0 || !assetID(input.ProjectName) {
		return out, ErrInvalid
	}
	if s == nil || s.DB == nil {
		return out, ErrUnavailable
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source ProviderScope
		if err := locked(tx).First(&source, input.SourceProviderScopeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrUnavailable
		}
		fx, err := decimal.NewFromString(source.FXUSDPerCNY)
		if !validAssetProvider(source) || err != nil || !fx.IsPositive() || source.FXVersion == "" || len(source.FXVersion) > 64 || source.FXObservedAt.IsZero() || source.FXObservedAt.After(now) {
			return ErrCapability
		}
		findExisting := func() (ProviderScope, bool, error) {
			var rows []ProviderScope
			// The current locking read also sees a concurrent winner after a
			// duplicate-key insert. Legacy ambiguous identities fail closed.
			if err := locked(tx).Where("account_ref = ? AND region = ? AND project_name = ?", source.AccountRef, source.Region, input.ProjectName).Limit(2).Find(&rows).Error; err != nil {
				return ProviderScope{}, false, ErrUnavailable
			}
			if len(rows) > 1 {
				return ProviderScope{}, false, ErrConflict
			}
			if len(rows) == 1 {
				return rows[0], true, nil
			}
			return ProviderScope{}, false, nil
		}
		existing, found, err := findExisting()
		if err != nil {
			return err
		}
		if found {
			out.ProviderScope = assetProjectProjection(existing)
			return nil
		}
		row := ProviderScope{
			ScopeKey:   assetProjectScopeKey(source.AccountRef, source.Region, input.ProjectName),
			AccountRef: source.AccountRef, Region: source.Region, ProjectName: input.ProjectName, Enabled: true,
			// Preserve the source observation; this operation does not verify or
			// refresh an exchange rate for any future pricing publication.
			FXUSDPerCNY: source.FXUSDPerCNY, FXVersion: source.FXVersion, FXObservedAt: source.FXObservedAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			if !dberr.IsDuplicate(err) {
				return ErrUnavailable
			}
			winner, exists, lookupErr := findExisting()
			if lookupErr != nil {
				return lookupErr
			}
			if !exists {
				return ErrConflict
			}
			out.ProviderScope = assetProjectProjection(winner)
			return nil
		}
		id, err := randomID("ave_")
		if err != nil {
			return ErrUnavailable
		}
		if err := tx.Create(&videoasset.Event{ID: id, OrgID: 0, ResourceType: "asset_project", ResourceID: strconv.FormatUint(row.ID, 10), Kind: "asset_project_registered", ActorID: strconv.FormatUint(actor.ID, 10), CreatedAt: now}).Error; err != nil {
			return ErrUnavailable
		}
		out.ProviderScope = assetProjectProjection(row)
		return nil
	})
	if err != nil {
		return AssetProjectRegistrationResult{}, err
	}
	return out, nil
}

func assetProjectProjection(row ProviderScope) AssetSetupProvider {
	return AssetSetupProvider{ID: row.ID, AccountRef: row.AccountRef, Region: row.Region, ProjectName: row.ProjectName, Enabled: row.Enabled}
}
