package routinggroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func orgPreviewGrant(t *testing.T, writes *Service, group uint64) Grant {
	t.Helper()
	g, changed, err := writes.SetGrant(context.Background(), GrantInput{OrgID: 1, GroupID: group, Status: Enabled, Audit: audit})
	if err != nil || !changed {
		t.Fatal("grant fixture", err)
	}
	return g
}

func TestAdminOrgGrantSeekUsesGroupIDAndIndependentDefault(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	var groups []Group
	var grants []Grant
	for i := uint64(1); i <= 102; i++ {
		groups = append(groups, Group{ID: i * 3, Slug: fmt.Sprintf("synthetic-%d", i), Name: "synthetic", Status: Enabled, PolicyVersion: i})
		// Grant IDs deliberately run in the opposite direction from group IDs.
		g := Grant{ID: 103 - i, OrgID: 1, GroupID: i * 3, Status: Enabled, GrantVersion: 1}
		if i == 102 {
			one := uint8(1)
			g.DefaultSlot = &one
		}
		if i == 51 {
			g.Status = Revoked
		}
		grants = append(grants, g)
	}
	if err := writes.DB.CreateInBatches(groups, 50).Error; err != nil {
		t.Fatal(err)
	}
	if err := writes.DB.CreateInBatches(grants, 50).Error; err != nil {
		t.Fatal(err)
	}
	if err := writes.DB.Create(&Grant{OrgID: 2, GroupID: 3, Status: Enabled, GrantVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	before := adminReadFingerprint(t, writes.DB)
	var queries []string
	var groupBatches [][]uint64
	const callback = "test:org-grant-seek"
	if err := writes.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		queries = append(queries, tx.Statement.SQL.String())
		if tx.Statement.Table == "routing_groups" && tx.Error == nil {
			rows, ok := tx.Statement.Dest.(*[]Group)
			if !ok {
				t.Fatal("group batch must be one bounded result")
			}
			ids := []uint64{}
			for _, row := range *rows {
				ids = append(ids, row.ID)
			}
			groupBatches = append(groupBatches, ids)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writes.DB.Callback().Query().Remove(callback) })
	reads.DB = writes.DB.Where("org_id = ?", 2).Limit(1).Order("group_id DESC")
	first, err := reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{}, Page{Limit: 100})
	if err != nil || len(first.Items) != 100 || !first.HasMore || first.NextCursor != 300 {
		t.Fatalf("page: %+v %v", first, err)
	}
	if first.Default == nil || first.Default.GroupID != 306 || !first.Default.Valid || *first.Default.GroupPolicyVersion != 102 {
		t.Fatal("default was lost outside page")
	}
	if len(queries) != 4 || len(groupBatches) != 2 || len(groupBatches[0]) != 100 || !reflect.DeepEqual(groupBatches[1], []uint64{306}) {
		t.Fatalf("page exceeded fixed SQL/batch budget or fetched probe: queries=%d batches=%v", len(queries), groupBatches)
	}
	for _, sql := range queries {
		if strings.Contains(strings.ToUpper(sql), "COUNT(") || strings.Contains(strings.ToUpper(sql), "OFFSET") {
			t.Fatal("seek used count/offset")
		}
	}
	for i, row := range first.Items {
		if row.Grant.GroupID != uint64(i+1)*3 || row.Grant.OrgID != 1 || row.Group.ID != row.Grant.GroupID || row.ConfigurationValid != (row.Grant.Status == Enabled) {
			t.Fatal("wrong group seek, ownership or configuration flag")
		}
	}
	_ = writes.DB.Callback().Query().Remove(callback)
	last, err := reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{}, Page{Cursor: first.NextCursor, Limit: 100})
	if err != nil || len(last.Items) != 2 || last.HasMore || last.NextCursor != 0 || last.Items[0].Grant.GroupID != 303 || last.Items[1].Grant.GroupID != 306 {
		t.Fatal("last page", err)
	}
	revoked, err := reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{Status: Revoked}, Page{})
	if err != nil || len(revoked.Items) != 1 || revoked.Items[0].Grant.GroupID != 153 || revoked.Default == nil || revoked.Default.GroupID != 306 {
		t.Fatal("status filter broadened or hid default", err)
	}
	if got := adminReadFingerprint(t, writes.DB); got != before {
		t.Fatal("read mutated configuration")
	}
}

func TestAdminOrgReadsKeepInvalidConfigurationAndInactiveOrg(t *testing.T) {
	writes, reads, deps := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "current")
	orgPreviewGrant(t, writes, g.ID)
	if _, err := writes.SetDefault(context.Background(), DefaultInput{OrgID: 1, GroupID: &g.ID, ExpectedTargetVersion: 1, Audit: audit}); err != nil {
		t.Fatal(err)
	}
	check := func(want []string, policy *uint64) {
		t.Helper()
		out, err := reads.GetOrgDefault(context.Background(), 1)
		if err != nil || out.Default == nil || out.Default.Valid != (len(want) == 0) || !reflect.DeepEqual(out.Default.InvalidReasons, want) || !reflect.DeepEqual(out.Default.GroupPolicyVersion, policy) {
			t.Fatalf("default reasons: %+v, %v", out, err)
		}
	}
	check([]string{}, &g.PolicyVersion)
	deps.orgs[1] = false
	if err := writes.DB.Model(&Group{}).Where("id = ?", g.ID).Updates(map[string]any{"owner_org_id": 2, "status": Disabled}).Error; err != nil {
		t.Fatal(err)
	}
	check([]string{"org_inactive", "group_inactive", "ownership_mismatch"}, &g.PolicyVersion)
	page, err := reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{}, Page{})
	if err != nil || page.Organization.Active || len(page.Items) != 1 || !page.Items[0].GroupExists || page.Items[0].OwnershipValid || page.Items[0].ConfigurationValid {
		t.Fatal("invalid relationship hidden or considered valid", err)
	}
	if err := writes.DB.Delete(&Group{}, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	check([]string{"org_inactive", "group_missing"}, nil)
	page, err = reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{}, Page{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Group != nil || page.Items[0].GroupExists || page.Items[0].OwnershipValid || page.Items[0].ConfigurationValid {
		t.Fatal("dangling relationship was removed/repaired", err)
	}
	// A legacy invalid enabled/default combination cannot be written through
	// the current CHECK; exercise only the pure diagnostic projection for it.
	bad := Grant{GroupID: g.ID, Status: Revoked, GrantVersion: 3}
	view := adminDefaultView(Organization{ID: 1, Active: true}, &bad, map[uint64]Group{})
	if !reflect.DeepEqual(view.InvalidReasons, []string{"group_missing", "grant_inactive"}) {
		t.Fatal("missing diagnostic reason")
	}
	for _, org := range []uint64{0, 999} {
		want := ErrInvalid
		if org != 0 {
			want = ErrNotFound
		}
		if _, err := reads.GetOrgDefault(context.Background(), org); !errors.Is(err, want) {
			t.Fatal("default invalid scope", err)
		}
		if _, err := reads.ListOrgGrants(context.Background(), org, OrgGrantFilter{}, Page{}); !errors.Is(err, want) {
			t.Fatal("list invalid scope", err)
		}
	}
	for _, tc := range []struct {
		filter OrgGrantFilter
		page   Page
	}{{OrgGrantFilter{Status: "enabled "}, Page{}}, {OrgGrantFilter{Status: Disabled}, Page{}}, {OrgGrantFilter{}, Page{Limit: 101}}, {OrgGrantFilter{}, Page{Limit: -1}}} {
		if _, err := reads.ListOrgGrants(context.Background(), 1, tc.filter, tc.page); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid list accepted", err)
		}
	}
}

func TestAdminOrgGrantPreviewMatchesCommandWithoutWrites(t *testing.T) {
	for _, name := range []string{"create", "new_revoked", "new_wrong_version", "noop", "stale", "expected_zero", "revoke_default", "reenable", "org_inactive_enable", "org_inactive_revoke", "group_inactive_enable", "group_inactive_revoke", "foreign_private", "missing_group", "missing_org"} {
		t.Run(name, func(t *testing.T) {
			writes, reads, deps := adminReadFixture(t)
			g := enabledGroup(t, writes, 0, "target")
			in := GrantPreviewInput{OrgID: 1, GroupID: g.ID, Status: Enabled}
			wantErr := error(nil)
			wantChanged := true
			switch name {
			case "create":
			case "new_revoked":
				in.Status = Revoked
				wantErr = ErrNotFound
			case "new_wrong_version":
				in.ExpectedVersion = 3
				wantErr = ErrConflict
			case "foreign_private":
				other := enabledGroup(t, writes, 2, "foreign")
				in.GroupID = other.ID
				wantErr = ErrNotFound
			case "missing_group":
				in.GroupID = 999
				wantErr = ErrNotFound
			case "missing_org":
				in.OrgID = 999
				wantErr = ErrNotFound
			default:
				orgPreviewGrant(t, writes, g.ID)
				in.ExpectedVersion = 1
				switch name {
				case "noop":
					wantChanged = false
				case "stale":
					in.ExpectedVersion = 2
					wantErr = ErrConflict
				case "expected_zero":
					in.ExpectedVersion = 0
					wantErr = ErrConflict
				case "revoke_default":
					if _, err := writes.SetDefault(context.Background(), DefaultInput{OrgID: 1, GroupID: &g.ID, ExpectedTargetVersion: 1, Audit: audit}); err != nil {
						t.Fatal(err)
					}
					in.ExpectedVersion = 2
					in.Status = Revoked
				case "reenable":
					if _, _, err := writes.SetGrant(context.Background(), GrantInput{OrgID: 1, GroupID: g.ID, ExpectedVersion: 1, Status: Revoked, Audit: audit}); err != nil {
						t.Fatal(err)
					}
					in.ExpectedVersion = 2
				case "org_inactive_enable":
					deps.orgs[1] = false
					in.ExpectedVersion = 99
					wantErr = ErrInactive
				case "org_inactive_revoke":
					deps.orgs[1] = false
					in.Status = Revoked
				case "group_inactive_enable", "group_inactive_revoke":
					if err := writes.DB.Model(&Group{}).Where("id = ?", g.ID).Update("status", Disabled).Error; err != nil {
						t.Fatal(err)
					}
					if name == "group_inactive_enable" {
						in.ExpectedVersion = 99
						wantErr = ErrInactive
					} else {
						in.Status = Revoked
					}
				}
			}
			before := adminReadFingerprint(t, writes.DB)
			out, err := reads.PreviewGrant(context.Background(), in)
			if !errors.Is(err, wantErr) {
				t.Fatalf("preview error: got %v want %v", err, wantErr)
			}
			if after := adminReadFingerprint(t, writes.DB); after != before {
				t.Fatal("preview wrote rows/events")
			}
			actual, changed, writeErr := writes.SetGrant(context.Background(), GrantInput{OrgID: in.OrgID, GroupID: in.GroupID, ExpectedVersion: in.ExpectedVersion, Status: in.Status, Audit: audit})
			if !errors.Is(writeErr, wantErr) {
				t.Fatalf("writer/preview semantics differ: %v / %v", writeErr, err)
			}
			if wantErr != nil {
				if !reflect.DeepEqual(out, OrgGrantPreview{}) || adminReadFingerprint(t, writes.DB) != before {
					t.Fatal("failed preview leaked partial data or refused writer mutated")
				}
				return
			}
			if out.Changed != wantChanged || changed != wantChanged || !reflect.DeepEqual(out.Change, in) || len(out.ObservedGrants) != 1 {
				t.Fatal("wrong normalized preview/changed")
			}
			if !changed {
				if len(out.AffectedGrants) != 0 || adminReadFingerprint(t, writes.DB) != before {
					t.Fatal("noop changed state")
				}
				return
			}
			if len(out.AffectedGrants) != 1 {
				t.Fatal("grant has one version transition")
			}
			e := out.AffectedGrants[0]
			if e.ToVersion != actual.GrantVersion || e.FromVersion != in.ExpectedVersion || e.Status != actual.Status || e.IsDefault || actual.DefaultSlot != nil {
				t.Fatal("grant effect differs from actual command")
			}
			if name == "create" && (e.GrantID != nil || out.ObservedGrants[0].Grant != nil || out.ObservedGrants[0].Exists || !reflect.DeepEqual(e.Actions, []string{"create"})) {
				t.Fatal("created preview invented grant ID")
			}
			if name == "revoke_default" && (!reflect.DeepEqual(e.Actions, []string{"revoke", "default_clear"}) || out.ObservedCurrentDefault == nil || out.ObservedCurrentDefault.GrantVersion != 2) {
				t.Fatal("revoke default omitted one-transition clear")
			}
		})
	}
}

func TestAdminOrgDefaultPreviewMatchesFullPointerCAS(t *testing.T) {
	for _, name := range []string{"empty_noop", "first", "switch", "clear", "same_noop", "expected_null_conflict", "wrong_current_version", "wrong_target_version", "same_wrong_target_version", "same_wrong_current_version", "target_ungranted", "target_revoked", "org_inactive_target", "org_inactive_clear", "group_inactive_target", "group_inactive_clear", "foreign_target", "foreign_current", "missing_current_group"} {
		t.Run(name, func(t *testing.T) {
			writes, reads, deps := adminReadFixture(t)
			a := enabledGroup(t, writes, 0, "a")
			b := enabledGroup(t, writes, 1, "b")
			orgPreviewGrant(t, writes, a.ID)
			orgPreviewGrant(t, writes, b.ID)
			in := DefaultPreviewInput{OrgID: 1}
			wantErr := error(nil)
			wantChanged := true
			if name != "empty_noop" && name != "first" {
				if _, err := writes.SetDefault(context.Background(), DefaultInput{OrgID: 1, GroupID: &a.ID, ExpectedTargetVersion: 1, Audit: audit}); err != nil {
					t.Fatal(err)
				}
				in.ExpectedGroupID = &a.ID
				in.ExpectedCurrentVersion = 2
				in.GroupID = &b.ID
				in.ExpectedTargetVersion = 1
			}
			switch name {
			case "empty_noop":
				wantChanged = false
			case "first":
				in.GroupID = &a.ID
				in.ExpectedTargetVersion = 1
			case "switch":
			case "clear":
				in.GroupID = nil
				in.ExpectedTargetVersion = 0
			case "same_noop":
				in.GroupID = &a.ID
				in.ExpectedTargetVersion = 2
				wantChanged = false
			case "expected_null_conflict":
				in.ExpectedGroupID = nil
				wantErr = ErrConflict
			case "wrong_current_version":
				in.ExpectedCurrentVersion = 1
				wantErr = ErrConflict
			case "wrong_target_version":
				in.ExpectedTargetVersion = 2
				wantErr = ErrConflict
			case "same_wrong_target_version":
				in.GroupID = &a.ID
				in.ExpectedTargetVersion = 1
				wantErr = ErrConflict
			case "same_wrong_current_version":
				in.GroupID = &a.ID
				in.ExpectedTargetVersion = 2
				in.ExpectedCurrentVersion = 1
				wantErr = ErrConflict
			case "target_ungranted":
				if err := writes.DB.Where("org_id = ? AND group_id = ?", 1, b.ID).Delete(&Grant{}).Error; err != nil {
					t.Fatal(err)
				}
				wantErr = ErrNotFound
			case "target_revoked":
				if _, _, err := writes.SetGrant(context.Background(), GrantInput{OrgID: 1, GroupID: b.ID, ExpectedVersion: 1, Status: Revoked, Audit: audit}); err != nil {
					t.Fatal(err)
				}
				wantErr = ErrInactive
			case "org_inactive_target":
				deps.orgs[1] = false
				in.ExpectedCurrentVersion = 99
				wantErr = ErrInactive
			case "org_inactive_clear":
				deps.orgs[1] = false
				in.GroupID = nil
				in.ExpectedTargetVersion = 0
			case "group_inactive_target":
				if err := writes.DB.Model(&Group{}).Where("id = ?", b.ID).Update("status", Disabled).Error; err != nil {
					t.Fatal(err)
				}
				in.ExpectedCurrentVersion = 99
				wantErr = ErrInactive
			case "group_inactive_clear":
				if err := writes.DB.Model(&Group{}).Where("id = ?", a.ID).Update("status", Disabled).Error; err != nil {
					t.Fatal(err)
				}
				in.GroupID = nil
				in.ExpectedTargetVersion = 0
			case "foreign_target":
				c := enabledGroup(t, writes, 2, "foreign")
				in.GroupID = &c.ID
				wantErr = ErrNotFound
			case "foreign_current":
				if err := writes.DB.Model(&Group{}).Where("id = ?", a.ID).Update("owner_org_id", 2).Error; err != nil {
					t.Fatal(err)
				}
				wantErr = ErrNotFound
			case "missing_current_group":
				if err := writes.DB.Delete(&Group{}, a.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantErr = ErrNotFound
			}
			before := adminReadFingerprint(t, writes.DB)
			out, err := reads.PreviewDefault(context.Background(), in)
			if !errors.Is(err, wantErr) {
				t.Fatalf("preview error got %v want %v", err, wantErr)
			}
			if adminReadFingerprint(t, writes.DB) != before {
				t.Fatal("default preview wrote rows/events")
			}
			changed, writeErr := writes.SetDefault(context.Background(), DefaultInput{OrgID: in.OrgID, GroupID: in.GroupID, ExpectedGroupID: in.ExpectedGroupID, ExpectedCurrentVersion: in.ExpectedCurrentVersion, ExpectedTargetVersion: in.ExpectedTargetVersion, Audit: audit})
			if !errors.Is(writeErr, wantErr) {
				t.Fatalf("preview/writer mismatch %v / %v", err, writeErr)
			}
			if wantErr != nil {
				if !reflect.DeepEqual(out, OrgDefaultPreview{}) || adminReadFingerprint(t, writes.DB) != before {
					t.Fatal("failed preview leaked result or failed writer mutated")
				}
				return
			}
			if out.Changed != wantChanged || changed != wantChanged || !reflect.DeepEqual(out.Change, in) {
				t.Fatal("changed/normalized CAS mismatch")
			}
			if !changed {
				if len(out.AffectedGrants) != 0 || adminReadFingerprint(t, writes.DB) != before {
					t.Fatal("noop mutated")
				}
				return
			}
			if len(out.AffectedGrants) == 0 || len(out.AffectedGrants) > 2 {
				t.Fatal("unbounded effects")
			}
			for _, effect := range out.AffectedGrants {
				var actual Grant
				if err := writes.DB.Where("org_id = ? AND group_id = ?", 1, effect.GroupID).Take(&actual).Error; err != nil {
					t.Fatal(err)
				}
				isDefault := actual.DefaultSlot != nil && *actual.DefaultSlot == 1
				if effect.GrantID == nil || *effect.GrantID != actual.ID || effect.ToVersion != actual.GrantVersion || effect.ToVersion != effect.FromVersion+1 || effect.Status != actual.Status || effect.IsDefault != isDefault {
					t.Fatal("proposed effect differs from committed command")
				}
			}
			if name == "switch" && (out.AffectedGrants[0].GroupID != a.ID || out.AffectedGrants[1].GroupID != b.ID || !reflect.DeepEqual(out.AffectedGrants[0].Actions, []string{"default_clear"}) || !reflect.DeepEqual(out.AffectedGrants[1].Actions, []string{"default_set"})) {
				t.Fatal("switch must clear then set")
			}
			if in.GroupID != nil && out.Change.GroupID == in.GroupID || in.ExpectedGroupID != nil && out.Change.ExpectedGroupID == in.ExpectedGroupID {
				t.Fatal("caller mutable pointers retained")
			}
		})
	}
}

func TestAdminOrgReadFailureReturnsNoPartialSnapshotOrDriverLog(t *testing.T) {
	writes, reads, _ := adminReadFixture(t)
	g := enabledGroup(t, writes, 0, "group")
	orgPreviewGrant(t, writes, g.ID)
	if _, err := writes.SetDefault(context.Background(), DefaultInput{OrgID: 1, GroupID: &g.ID, ExpectedTargetVersion: 1, Audit: audit}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	warn := logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})
	reads.DB = writes.DB.Session(&gorm.Session{Logger: warn})
	secretCause := errors.New("SELECT credential synthetic-secret driver detail")
	const callback = "test:org-admin-read-failure"
	if err := writes.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "org_group_grants" && strings.Contains(tx.Statement.SQL.String(), "default_slot = 1") {
			_ = tx.AddError(secretCause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writes.DB.Callback().Query().Remove(callback) })
	check := func(err error) {
		t.Helper()
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, secretCause) || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("unsafe error or lost cause", err)
		}
	}
	list, err := reads.ListOrgGrants(context.Background(), 1, OrgGrantFilter{}, Page{})
	check(err)
	if !reflect.DeepEqual(list, OrgGrantPage{}) {
		t.Fatal("partial page escaped")
	}
	def, err := reads.GetOrgDefault(context.Background(), 1)
	check(err)
	if !reflect.DeepEqual(def, OrgDefaultView{}) {
		t.Fatal("partial default escaped")
	}
	grant, err := reads.PreviewGrant(context.Background(), GrantPreviewInput{OrgID: 1, GroupID: g.ID, ExpectedVersion: 2, Status: Revoked})
	check(err)
	if !reflect.DeepEqual(grant, OrgGrantPreview{}) {
		t.Fatal("partial grant preview escaped")
	}
	preview, err := reads.PreviewDefault(context.Background(), DefaultPreviewInput{OrgID: 1, ExpectedGroupID: &g.ID, ExpectedCurrentVersion: 2})
	check(err)
	if !reflect.DeepEqual(preview, OrgDefaultPreview{}) {
		t.Fatal("partial default preview escaped")
	}
	if logs.Len() != 0 {
		t.Fatal("domain leaked driver/SQL into inherited logger")
	}
	// Prove the inherited Warn logger works and was not globally suppressed.
	_ = reads.DB.Exec("SELECT synthetic_missing_column FROM routing_groups").Error
	if logs.Len() == 0 {
		t.Fatal("logger positive control not active")
	}
	_ = writes.DB.Callback().Query().Remove(callback)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reads.GetOrgDefault(ctx, 1)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatal("cancel lost safe cause", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = reads.PreviewGrant(ctx, GrantPreviewInput{OrgID: 1, GroupID: g.ID, ExpectedVersion: 2, Status: Revoked})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatal("deadline lost safe cause", err)
	}
}
