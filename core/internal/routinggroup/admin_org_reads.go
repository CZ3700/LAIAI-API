package routinggroup

import (
	"context"
	"errors"
	"sort"

	"gorm.io/gorm"
)

type OrgGrantFilter struct{ Status string }
type AdminOrgGrant struct {
	Grant                                           Grant
	Group                                           *Group
	GroupExists, OwnershipValid, ConfigurationValid bool
}
type DefaultView struct {
	GroupID, GrantVersion uint64
	GroupPolicyVersion    *uint64
	Valid                 bool
	InvalidReasons        []string
}
type OrgGrantPage struct {
	Organization Organization
	Items        []AdminOrgGrant
	Default      *DefaultView
	NextCursor   uint64
	HasMore      bool
}
type OrgDefaultView struct {
	Organization Organization
	Default      *DefaultView
}
type DefaultPointer struct{ GroupID, GrantVersion uint64 }
type GrantPreviewInput struct {
	OrgID, GroupID, ExpectedVersion uint64
	Status                          string
}
type DefaultPreviewInput struct {
	OrgID                                         uint64
	ExpectedGroupID, GroupID                      *uint64
	ExpectedCurrentVersion, ExpectedTargetVersion uint64
}
type ObservedOrgGrant struct {
	GroupID uint64
	Exists  bool
	Grant   *Grant
	Group   Group
}
type AffectedOrgGrant struct {
	GroupID                uint64
	GrantID                *uint64
	Actions                []string
	FromVersion, ToVersion uint64
	Status                 string
	IsDefault              bool
}
type OrgGrantPreview struct {
	Organization           Organization
	Change                 GrantPreviewInput
	Changed                bool
	ObservedCurrentDefault *DefaultPointer
	ObservedGrants         []ObservedOrgGrant
	AffectedGrants         []AffectedOrgGrant
}
type OrgDefaultPreview struct {
	Organization           Organization
	Change                 DefaultPreviewInput
	Changed                bool
	ObservedCurrentDefault *DefaultPointer
	ObservedGrants         []ObservedOrgGrant
	AffectedGrants         []AffectedOrgGrant
}

const adminGrantColumns = "id, org_id, group_id, status, grant_version, default_slot, created_at, updated_at"

func readAdminDefaultTx(tx *gorm.DB, org uint64) (*Grant, error) {
	q, cancel, err := adminSelect(tx, adminGrantColumns)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var grant Grant
	err = q.Where("org_id = ? AND default_slot = 1", org).Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// At most 100 page groups and one off-page default. Never query the probe row.
func readAdminGroupsTx(tx *gorm.DB, ids []uint64, owner *uint64) (map[uint64]Group, error) {
	if len(ids) > 101 {
		return nil, ErrInvalid
	}
	ids = append([]uint64(nil), ids...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if id == 0 {
			return nil, ErrInvalid
		}
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	groups := make(map[uint64]Group, len(unique))
	for start := 0; start < len(unique); start += 100 {
		end := min(start+100, len(unique))
		q, cancel, err := adminSelect(tx, adminGroupColumns)
		if err != nil {
			return nil, err
		}
		q = q.Where("id IN ?", unique[start:end])
		if owner != nil {
			q = q.Where("owner_org_id = 0 OR owner_org_id = ?", *owner)
		}
		var rows []Group
		err = q.Order("id ASC").Limit(100).Find(&rows).Error
		cancel()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			groups[row.ID] = row
		}
	}
	return groups, nil
}

func readAdminOrgGrantsTx(tx *gorm.DB, org uint64, ids []uint64) (map[uint64]Grant, error) {
	if len(ids) == 0 {
		return map[uint64]Grant{}, nil
	}
	if len(ids) > 2 {
		return nil, ErrInvalid
	}
	q, cancel, err := adminSelect(tx, adminGrantColumns)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var rows []Grant
	err = q.Where("org_id = ? AND group_id IN ?", org, ids).Order("id ASC").Limit(2).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	grants := make(map[uint64]Grant, len(rows))
	for _, row := range rows {
		grants[row.GroupID] = row
	}
	return grants, nil
}

func adminDefaultPointer(grant *Grant) *DefaultPointer {
	if grant == nil {
		return nil
	}
	return &DefaultPointer{GroupID: grant.GroupID, GrantVersion: grant.GrantVersion}
}

func adminDefaultView(org Organization, grant *Grant, groups map[uint64]Group) *DefaultView {
	if grant == nil {
		return nil
	}
	out := &DefaultView{GroupID: grant.GroupID, GrantVersion: grant.GrantVersion, InvalidReasons: []string{}}
	if !org.Active {
		out.InvalidReasons = append(out.InvalidReasons, "org_inactive")
	}
	g, exists := groups[grant.GroupID]
	if !exists {
		out.InvalidReasons = append(out.InvalidReasons, "group_missing")
	} else {
		out.GroupPolicyVersion = &g.PolicyVersion
		if g.Status != Enabled {
			out.InvalidReasons = append(out.InvalidReasons, "group_inactive")
		}
		if g.OwnerOrgID != 0 && g.OwnerOrgID != org.ID {
			out.InvalidReasons = append(out.InvalidReasons, "ownership_mismatch")
		}
	}
	if grant.Status != Enabled {
		out.InvalidReasons = append(out.InvalidReasons, "grant_inactive")
	}
	out.Valid = len(out.InvalidReasons) == 0
	return out
}

func (s *AdminReadService) ListOrgGrants(ctx context.Context, org uint64, filter OrgGrantFilter, page Page) (OrgGrantPage, error) {
	if org == 0 || filter.Status != "" && filter.Status != Enabled && filter.Status != Revoked {
		return OrgGrantPage{}, ErrInvalid
	}
	page, err := normalizeAdminPage(page)
	if err != nil {
		return OrgGrantPage{}, err
	}
	out := OrgGrantPage{Items: []AdminOrgGrant{}}
	err = s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Organization, err = s.organizationTx(tx, org)
		if err != nil {
			return err
		}
		q, cancel, err := adminSelect(tx, adminGrantColumns)
		if err != nil {
			return err
		}
		q = q.Where("org_id = ? AND group_id > ?", org, page.Cursor)
		if filter.Status != "" {
			q = q.Where("status = ?", filter.Status)
		}
		var rows []Grant
		err = q.Order("group_id ASC").Limit(page.Limit + 1).Find(&rows).Error
		cancel()
		if err != nil {
			return err
		}
		if len(rows) > page.Limit {
			rows = rows[:page.Limit]
			out.HasMore, out.NextCursor = true, rows[len(rows)-1].GroupID
		}
		current, err := readAdminDefaultTx(tx, org)
		if err != nil {
			return err
		}
		ids := make([]uint64, 0, len(rows)+1)
		for _, row := range rows {
			ids = append(ids, row.GroupID)
		}
		if current != nil {
			ids = append(ids, current.GroupID)
		}
		groups, err := readAdminGroupsTx(tx, ids, nil)
		if err != nil {
			return err
		}
		out.Default = adminDefaultView(out.Organization, current, groups)
		for _, row := range rows {
			item := AdminOrgGrant{Grant: row}
			if group, ok := groups[row.GroupID]; ok {
				item.Group, item.GroupExists = &group, true
				item.OwnershipValid = group.OwnerOrgID == 0 || group.OwnerOrgID == org
				item.ConfigurationValid = out.Organization.Active && item.OwnershipValid && group.Status == Enabled && row.Status == Enabled
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	if err != nil {
		return OrgGrantPage{}, err
	}
	return out, nil
}

func (s *AdminReadService) GetOrgDefault(ctx context.Context, org uint64) (OrgDefaultView, error) {
	if org == 0 {
		return OrgDefaultView{}, ErrInvalid
	}
	var out OrgDefaultView
	err := s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Organization, err = s.organizationTx(tx, org)
		if err != nil {
			return err
		}
		current, err := readAdminDefaultTx(tx, org)
		if err != nil || current == nil {
			return err
		}
		groups, err := readAdminGroupsTx(tx, []uint64{current.GroupID}, nil)
		if err != nil {
			return err
		}
		out.Default = adminDefaultView(out.Organization, current, groups)
		return nil
	})
	if err != nil {
		return OrgDefaultView{}, err
	}
	return out, nil
}

func (s *AdminReadService) PreviewGrant(ctx context.Context, in GrantPreviewInput) (OrgGrantPreview, error) {
	if in.OrgID == 0 || in.GroupID == 0 || in.Status != Enabled && in.Status != Revoked {
		return OrgGrantPreview{}, ErrInvalid
	}
	out := OrgGrantPreview{Change: in, ObservedGrants: []ObservedOrgGrant{}, AffectedGrants: []AffectedOrgGrant{}}
	err := s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Organization, err = s.organizationTx(tx, in.OrgID)
		if err != nil {
			return err
		}
		groups, err := readAdminGroupsTx(tx, []uint64{in.GroupID}, &in.OrgID)
		if err != nil {
			return err
		}
		group, ok := groups[in.GroupID]
		if !ok {
			return ErrNotFound
		}
		if in.Status == Enabled && (!out.Organization.Active || group.Status != Enabled) {
			return ErrInactive
		}
		grants, err := readAdminOrgGrantsTx(tx, in.OrgID, []uint64{in.GroupID})
		if err != nil {
			return err
		}
		grant, exists := grants[in.GroupID]
		if !exists {
			if in.ExpectedVersion != 0 {
				return ErrConflict
			}
			if in.Status != Enabled {
				return ErrNotFound
			}
		} else if err = checkVersion(grant.GrantVersion, in.ExpectedVersion); err != nil {
			return err
		}
		current, err := readAdminDefaultTx(tx, in.OrgID)
		if err != nil {
			return err
		}
		out.ObservedCurrentDefault = adminDefaultPointer(current)
		observed := ObservedOrgGrant{GroupID: in.GroupID, Exists: exists, Group: group}
		if exists {
			observed.Grant = &grant
		}
		out.ObservedGrants = append(out.ObservedGrants, observed)
		if exists && grant.Status == in.Status {
			return nil
		}
		out.Changed = true
		effect := AffectedOrgGrant{GroupID: in.GroupID, ToVersion: 1, Actions: []string{"create"}, Status: in.Status}
		if exists {
			effect.GrantID, effect.FromVersion, effect.ToVersion = &grant.ID, grant.GrantVersion, grant.GrantVersion+1
			effect.Actions = []string{"enable"}
			if in.Status == Revoked {
				effect.Actions = []string{"revoke"}
			}
			if grant.DefaultSlot != nil && *grant.DefaultSlot == 1 {
				effect.Actions = append(effect.Actions, "default_clear")
			}
		}
		out.AffectedGrants = append(out.AffectedGrants, effect)
		return nil
	})
	if err != nil {
		return OrgGrantPreview{}, err
	}
	return out, nil
}

func cloneAdminID(id *uint64) *uint64 {
	if id == nil {
		return nil
	}
	v := *id
	return &v
}

func (s *AdminReadService) PreviewDefault(ctx context.Context, in DefaultPreviewInput) (OrgDefaultPreview, error) {
	if in.OrgID == 0 || in.ExpectedGroupID != nil && *in.ExpectedGroupID == 0 || in.GroupID != nil && *in.GroupID == 0 {
		return OrgDefaultPreview{}, ErrInvalid
	}
	in.ExpectedGroupID, in.GroupID = cloneAdminID(in.ExpectedGroupID), cloneAdminID(in.GroupID)
	out := OrgDefaultPreview{Change: in, ObservedGrants: []ObservedOrgGrant{}, AffectedGrants: []AffectedOrgGrant{}}
	err := s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Organization, err = s.organizationTx(tx, in.OrgID)
		if err != nil {
			return err
		}
		current, err := readAdminDefaultTx(tx, in.OrgID)
		if err != nil {
			return err
		}
		out.ObservedCurrentDefault = adminDefaultPointer(current)
		var currentID *uint64
		if current != nil {
			currentID = &current.GroupID
		}
		if !sameID(currentID, in.ExpectedGroupID) {
			return ErrConflict
		}
		ids := []uint64{}
		if currentID != nil {
			ids = append(ids, *currentID)
		}
		if in.GroupID != nil && !sameID(currentID, in.GroupID) {
			ids = append(ids, *in.GroupID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		groups, err := readAdminGroupsTx(tx, ids, &in.OrgID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, ok := groups[id]; !ok {
				return ErrNotFound
			}
		}
		if in.GroupID != nil && (!out.Organization.Active || groups[*in.GroupID].Status != Enabled) {
			return ErrInactive
		}
		if len(ids) == 0 {
			return nil
		}
		grants, err := readAdminOrgGrantsTx(tx, in.OrgID, ids)
		if err != nil {
			return err
		}
		if currentID != nil {
			grant, ok := grants[*currentID]
			if !ok || grant.DefaultSlot == nil || *grant.DefaultSlot != 1 || grant.Status != Enabled {
				return ErrConflict
			}
			if err = checkVersion(grant.GrantVersion, in.ExpectedCurrentVersion); err != nil {
				return err
			}
		}
		if in.GroupID != nil {
			grant, ok := grants[*in.GroupID]
			if !ok {
				return ErrNotFound
			}
			if grant.Status != Enabled {
				return ErrInactive
			}
			if err = checkVersion(grant.GrantVersion, in.ExpectedTargetVersion); err != nil {
				return err
			}
		}
		for _, id := range ids {
			grant := grants[id]
			out.ObservedGrants = append(out.ObservedGrants, ObservedOrgGrant{GroupID: id, Exists: true, Grant: &grant, Group: groups[id]})
		}
		if sameID(currentID, in.GroupID) {
			return nil
		}
		out.Changed = true
		for _, id := range []*uint64{currentID, in.GroupID} {
			if id == nil {
				continue
			}
			grant := grants[*id]
			effect := AffectedOrgGrant{GroupID: *id, GrantID: &grant.ID, FromVersion: grant.GrantVersion, ToVersion: grant.GrantVersion + 1, Status: grant.Status, Actions: []string{"default_clear"}}
			if in.GroupID != nil && *in.GroupID == *id {
				effect.Actions, effect.IsDefault = []string{"default_set"}, true
			}
			out.AffectedGrants = append(out.AffectedGrants, effect)
		}
		return nil
	})
	if err != nil {
		return OrgDefaultPreview{}, err
	}
	return out, nil
}
