package video

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

func TestVideoCatalogPortraitInheritanceDoesNotOverrideExplicitRouting(t *testing.T) {
	for _, mode := range []string{"shared", "explicit_other_disabled", "explicit_selected", "policy_disabled", "wrong_identity", "read_failure"} {
		t.Run(mode, func(t *testing.T) {
			db := catalogLocalDB(t)
			shared := AssetSharedDefault{ID: 1, ProviderScopeID: 1, ScopeVersion: 2, Enabled: true, AllowAsync: true}
			if err := db.Create(&shared).Error; err != nil {
				t.Fatal(err)
			}
			config := AssetScopeConfig{ProviderScopeID: 1, Version: 2, AccountRef: "account", Region: "cn-beijing", ProjectName: "default", CapabilitiesJSON: `{"Portrait":true}`, CredentialCipher: "must-never-be-selected", Enabled: true}
			if mode == "wrong_identity" {
				config.ProjectName = "other"
			}
			if err := db.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			policy, exists := Policy{}, false
			if mode == "explicit_other_disabled" {
				if err := db.Create(&AssetOrgRoute{OrgID: 5, ProviderScopeID: 999, ScopeVersion: 7, Enabled: false}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "explicit_selected" {
				if err := db.Create(&AssetOrgRoute{OrgID: 5, ProviderScopeID: 1, ScopeVersion: 2, Enabled: true}).Error; err != nil {
					t.Fatal(err)
				}
				policy = Policy{AsyncExecutionAllowed: true, RetentionDays: 1}
				exists = true
			}
			if mode == "policy_disabled" {
				policy = Policy{AsyncExecutionAllowed: false, RetentionDays: 30}
				exists = true
			}
			if mode == "read_failure" {
				if err := db.Exec("DROP TABLE video_asset_scope_configs").Error; err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			if err := db.Callback().Query().After("gorm:query").Register("catalog_test:portrait_count", func(q *gorm.DB) {
				reads++
				if q.Statement.SQL.String() != "" && containsCatalogSecretProjection(q.Statement.SQL.String()) {
					t.Fatal("portrait reader selected credential data")
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Query().Remove("catalog_test:portrait_count")
			s := &Service{DB: db, Assets: &videoasset.Service{}}
			err := db.Transaction(func(tx *gorm.DB) error {
				out, err := s.catalogPortraitsTx(context.Background(), tx, 5, map[uint64]ProviderScope{1: {ID: 1, AccountRef: "account", Region: "cn-beijing", ProjectName: "default", Enabled: true}}, policy, exists)
				if mode == "read_failure" {
					if !errors.Is(err, ErrCatalogUnavailable) || out != nil {
						t.Fatal("SQL failure silently became portrait=false", err)
					}
					return nil
				}
				want := mode == "shared" || mode == "explicit_selected"
				if err != nil || out[1] != want || reads > 3 {
					t.Fatal("portrait observation violated override/policy/identity or query budget", err, reads)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func containsCatalogSecretProjection(sql string) bool {
	return strings.Contains(strings.ToLower(sql), "credential_cipher")
}
