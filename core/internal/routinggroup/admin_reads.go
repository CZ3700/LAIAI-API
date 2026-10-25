package routinggroup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// AdminReadService is only for the host's authenticated management surface.
// It supplies structural configuration, never runtime routing authorization.
// Host readers must use this exact read-only transaction and context, with a
// separate <=200ms SELECT budget, and may only return the finite safe DTOs.
type AdminReadService struct {
	DB            *gorm.DB
	Organizations OrganizationReader
	Channels      ChannelReader
}

type OrganizationReader interface {
	ReadOrganizations(context.Context, *gorm.DB, []uint64) ([]Organization, error)
}

type ChannelReader interface {
	ReadChannels(context.Context, *gorm.DB, []uint64) ([]AdminChannel, error)
}

type AdminChannel struct {
	ID, OwnerOrgID uint64
	Name, Provider string
	Status         int8
}

type Page struct {
	Cursor uint64
	Limit  int
}
type GroupFilter struct {
	OwnerOrgID *uint64
	Status     string
}
type GroupPage struct {
	Items      []Group
	NextCursor uint64
	HasMore    bool
}
type AdminBinding struct {
	Binding                                       Binding
	Channel                                       *AdminChannel
	ChannelExists, OwnershipValid, ChannelEnabled bool
}
type BindingPage struct {
	Group      Group
	Items      []AdminBinding
	NextCursor uint64
	HasMore    bool
}

type PreviewChange struct {
	Kind                      string
	Name, Description, Status *string
	ChannelID                 uint64
}
type PreviewInput struct {
	ExpectedVersion uint64
	Change          PreviewChange
}
type ObservedCount struct {
	ObservedCount     uint64
	Exact, LowerBound bool
	Limit             int
}
type StructuralCounts struct{ Bindings, Grants, EnabledGrants, DefaultPointers ObservedCount }
type GroupPreview struct {
	Group, ProposedGroup  Group
	ObservedGroupVersion  uint64
	Change                PreviewChange
	Changed               bool
	Channel               *AdminChannel
	WasBound, WillBeBound *bool
	Counts                StructuralCounts
}

const (
	adminReadWindow     = time.Second
	adminSelectWindow   = 200 * time.Millisecond
	adminRelationLimit  = 1000
	adminGroupColumns   = "id, slug, name, description, owner_org_id, status, policy_version, created_at, updated_at"
	adminBindingColumns = "group_id, channel_id, created_at, updated_at"
)

// Error text is safe even when the underlying driver includes parameters or
// SQL. Is/Unwrap preserve cancellation/deadline classification internally.
type adminReadError struct{ cause error }

func (e *adminReadError) Error() string        { return ErrUnavailable.Error() }
func (e *adminReadError) Unwrap() error        { return e.cause }
func (e *adminReadError) Is(target error) bool { return target == ErrUnavailable }

func adminReadFailure(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrInvalid, ErrConflict, ErrNotFound, ErrOwnership, ErrInactive} {
		if errors.Is(err, known) {
			return known
		}
	}
	return &adminReadError{cause: err}
}

func (s *AdminReadService) read(ctx context.Context, fn func(*gorm.DB) error) error {
	if s == nil || s.DB == nil || s.DB.Statement == nil || ctx == nil {
		return ErrUnavailable
	}
	if _, ok := s.DB.Statement.ConnPool.(gorm.TxCommitter); ok {
		return ErrUnavailable
	}
	if s.DB.Dialector == nil || (s.DB.Dialector.Name() != "mysql" && s.DB.Dialector.Name() != "sqlite") {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, adminReadWindow)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return adminReadFailure(err)
	}
	db := s.DB.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := fn(tx); err != nil {
			return err
		}
		return ctx.Err() // Never return a partial or expired successful snapshot.
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return adminReadFailure(err)
}

// Columns are compile-time constants. NewDB prevents previous predicates,
// LIMIT or projections from leaking into a second query in the snapshot.
func adminSelect(tx *gorm.DB, columns string) (*gorm.DB, context.CancelFunc, error) {
	remaining := adminSelectWindow
	if deadline, ok := tx.Statement.Context.Deadline(); ok {
		if left := time.Until(deadline); left < remaining {
			remaining = left
		}
	}
	if remaining < time.Millisecond {
		return nil, func() {}, context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(tx.Statement.Context, remaining)
	selectSQL := columns
	if tx.Dialector.Name() == "mysql" {
		selectSQL = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", remaining.Milliseconds(), columns)
	}
	return tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Select(selectSQL), cancel, nil
}

func normalizeAdminPage(page Page) (Page, error) {
	if page.Limit == 0 {
		page.Limit = 20
	}
	if page.Limit < 1 || page.Limit > 100 {
		return Page{}, ErrInvalid
	}
	return page, nil
}

func (s *AdminReadService) ListGroups(ctx context.Context, filter GroupFilter, page Page) (GroupPage, error) {
	page, err := normalizeAdminPage(page)
	if err != nil || filter.Status != "" && !validStatus(filter.Status) {
		return GroupPage{}, ErrInvalid
	}
	if filter.OwnerOrgID != nil {
		owner := *filter.OwnerOrgID
		filter.OwnerOrgID = &owner
	}
	out := GroupPage{Items: []Group{}}
	err = s.read(ctx, func(tx *gorm.DB) error {
		q, cancel, err := adminSelect(tx, adminGroupColumns)
		if err != nil {
			return err
		}
		defer cancel()
		q = q.Model(&Group{}).Where("id > ?", page.Cursor)
		if filter.OwnerOrgID != nil {
			q = q.Where("owner_org_id = ?", *filter.OwnerOrgID)
		}
		if filter.Status != "" {
			q = q.Where("status = ?", filter.Status)
		}
		if err := q.Order("id ASC").Limit(page.Limit + 1).Find(&out.Items).Error; err != nil {
			return err
		}
		if len(out.Items) > page.Limit {
			out.HasMore = true
			out.Items = out.Items[:page.Limit]
			out.NextCursor = out.Items[len(out.Items)-1].ID
		}
		return nil
	})
	if err != nil {
		return GroupPage{}, err
	}
	return out, nil
}

func readAdminGroupTx(tx *gorm.DB, id uint64) (Group, error) {
	q, cancel, err := adminSelect(tx, adminGroupColumns)
	if err != nil {
		return Group{}, err
	}
	defer cancel()
	var group Group
	err = q.Model(&Group{}).Where("id = ?", id).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return group, err
}

func (s *AdminReadService) GetGroup(ctx context.Context, id uint64) (Group, error) {
	if id == 0 {
		return Group{}, ErrInvalid
	}
	var out Group
	err := s.read(ctx, func(tx *gorm.DB) error { var err error; out, err = readAdminGroupTx(tx, id); return err })
	if err != nil {
		return Group{}, err
	}
	return out, nil
}

func readAdminBindingsTx(tx *gorm.DB, id, cursor uint64, limit int) ([]Binding, error) {
	q, cancel, err := adminSelect(tx, adminBindingColumns)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows := []Binding{}
	err = q.Model(&Binding{}).Where("group_id = ? AND channel_id > ?", id, cursor).Order("channel_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (s *AdminReadService) channelsTx(tx *gorm.DB, ids []uint64) (map[uint64]AdminChannel, error) {
	out := make(map[uint64]AdminChannel, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if s.Channels == nil || len(ids) > 100 {
		return nil, ErrUnavailable
	}
	wanted := make(map[uint64]bool, len(ids))
	for i, id := range ids {
		if id == 0 || i > 0 && ids[i-1] >= id {
			return nil, ErrInvalid
		}
		wanted[id] = true
	}
	rows, err := s.Channels.ReadChannels(tx.Statement.Context, tx, ids)
	if err != nil {
		return nil, &adminReadError{cause: err}
	}
	if len(rows) > len(ids) {
		return nil, ErrUnavailable
	}
	for _, row := range rows {
		if !wanted[row.ID] {
			return nil, ErrUnavailable
		}
		if _, dup := out[row.ID]; dup {
			return nil, ErrUnavailable
		}
		out[row.ID] = row
	}
	return out, nil
}

func (s *AdminReadService) ListBindings(ctx context.Context, id uint64, page Page) (BindingPage, error) {
	page, err := normalizeAdminPage(page)
	if err != nil || id == 0 {
		return BindingPage{}, ErrInvalid
	}
	out := BindingPage{Items: []AdminBinding{}}
	err = s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Group, err = readAdminGroupTx(tx, id)
		if err != nil {
			return err
		}
		rows, err := readAdminBindingsTx(tx, id, page.Cursor, page.Limit+1)
		if err != nil {
			return err
		}
		if len(rows) > page.Limit {
			out.HasMore = true
			rows = rows[:page.Limit]
			out.NextCursor = rows[len(rows)-1].ChannelID
		}
		ids := make([]uint64, len(rows))
		for i, row := range rows {
			ids[i] = row.ChannelID
		}
		channels, err := s.channelsTx(tx, ids)
		if err != nil {
			return err
		}
		for _, row := range rows {
			item := AdminBinding{Binding: row}
			if channel, ok := channels[row.ChannelID]; ok {
				copy := channel
				item.Channel = &copy
				item.ChannelExists = true
				item.OwnershipValid = channel.OwnerOrgID == out.Group.OwnerOrgID
				item.ChannelEnabled = channel.Status == 1
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	if err != nil {
		return BindingPage{}, err
	}
	return out, nil
}

func copyPreviewChange(in PreviewChange) (PreviewChange, error) {
	if in.Name != nil {
		v := *in.Name
		in.Name = &v
	}
	if in.Description != nil {
		v := *in.Description
		in.Description = &v
	}
	if in.Status != nil {
		v := *in.Status
		in.Status = &v
	}
	switch in.Kind {
	case "update":
		if in.ChannelID != 0 || in.Name == nil && in.Description == nil && in.Status == nil {
			return PreviewChange{}, ErrInvalid
		}
	case "bind", "unbind":
		if in.ChannelID == 0 || in.Name != nil || in.Description != nil || in.Status != nil {
			return PreviewChange{}, ErrInvalid
		}
	default:
		return PreviewChange{}, ErrInvalid
	}
	return in, nil
}

func (s *AdminReadService) organizationTx(tx *gorm.DB, id uint64) (Organization, error) {
	if id == 0 {
		return Organization{Active: true}, nil
	}
	if s.Organizations == nil {
		return Organization{}, ErrUnavailable
	}
	rows, err := s.Organizations.ReadOrganizations(tx.Statement.Context, tx, []uint64{id})
	if err != nil {
		return Organization{}, &adminReadError{cause: err}
	}
	if len(rows) == 0 {
		return Organization{}, ErrNotFound
	}
	if len(rows) != 1 || rows[0].ID != id {
		return Organization{}, ErrUnavailable
	}
	return rows[0], nil
}

func readAdminBindingExistsTx(tx *gorm.DB, group, channel uint64) (bool, error) {
	q, cancel, err := adminSelect(tx, "channel_id")
	if err != nil {
		return false, err
	}
	defer cancel()
	var row Binding
	err = q.Model(&Binding{}).Where("group_id = ? AND channel_id = ?", group, channel).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func observedCount(value uint64, exact bool) ObservedCount {
	return ObservedCount{ObservedCount: value, Exact: exact, LowerBound: !exact, Limit: adminRelationLimit}
}

func readStructuralCountsTx(tx *gorm.DB, group uint64) (StructuralCounts, error) {
	bindings, err := readAdminBindingsTx(tx, group, 0, adminRelationLimit+1)
	if err != nil {
		return StructuralCounts{}, err
	}
	bindingExact := len(bindings) <= adminRelationLimit
	if !bindingExact {
		bindings = bindings[:adminRelationLimit]
	}
	q, cancel, err := adminSelect(tx, "org_id, status, default_slot")
	if err != nil {
		return StructuralCounts{}, err
	}
	defer cancel()
	var grants []Grant
	if err := q.Model(&Grant{}).Where("group_id = ?", group).Order("org_id ASC").Limit(adminRelationLimit + 1).Find(&grants).Error; err != nil {
		return StructuralCounts{}, err
	}
	grantExact := len(grants) <= adminRelationLimit
	if !grantExact {
		grants = grants[:adminRelationLimit]
	}
	var enabled, defaults uint64
	for _, grant := range grants {
		if grant.Status == Enabled {
			enabled++
		}
		if grant.DefaultSlot != nil && *grant.DefaultSlot == 1 {
			defaults++
		}
	}
	return StructuralCounts{Bindings: observedCount(uint64(len(bindings)), bindingExact), Grants: observedCount(uint64(len(grants)), grantExact), EnabledGrants: observedCount(enabled, grantExact), DefaultPointers: observedCount(defaults, grantExact)}, nil
}

func (s *AdminReadService) Preview(ctx context.Context, id uint64, in PreviewInput) (GroupPreview, error) {
	change, err := copyPreviewChange(in.Change)
	if err != nil || id == 0 || in.ExpectedVersion == 0 {
		return GroupPreview{}, ErrInvalid
	}
	out := GroupPreview{Change: change}
	err = s.read(ctx, func(tx *gorm.DB) error {
		var err error
		out.Group, err = readAdminGroupTx(tx, id)
		if err != nil {
			return err
		}
		if err := checkVersion(out.Group.PolicyVersion, in.ExpectedVersion); err != nil {
			return err
		}
		out.ObservedGroupVersion = out.Group.PolicyVersion
		out.ProposedGroup = out.Group
		org, err := s.organizationTx(tx, out.Group.OwnerOrgID)
		if err != nil {
			return err
		}
		if change.Kind == "update" {
			if change.Name != nil {
				out.ProposedGroup.Name = *change.Name
			}
			if change.Description != nil {
				out.ProposedGroup.Description = *change.Description
			}
			if change.Status != nil {
				out.ProposedGroup.Status = *change.Status
			}
			if !validText(out.ProposedGroup.Name, out.ProposedGroup.Description) || !validStatus(out.ProposedGroup.Status) {
				return ErrInvalid
			}
			if out.ProposedGroup.Status == Enabled && !org.Active {
				return ErrInactive
			}
			out.Changed = out.Group.Name != out.ProposedGroup.Name || out.Group.Description != out.ProposedGroup.Description || out.Group.Status != out.ProposedGroup.Status
			name, description, status := out.ProposedGroup.Name, out.ProposedGroup.Description, out.ProposedGroup.Status
			out.Change = PreviewChange{Kind: "update", Name: &name, Description: &description, Status: &status}
		} else {
			bound := change.Kind == "bind"
			if bound && !org.Active {
				return ErrInactive
			}
			channels, err := s.channelsTx(tx, []uint64{change.ChannelID})
			if err != nil {
				return err
			}
			channel, exists := channels[change.ChannelID]
			if bound && !exists {
				return ErrNotFound
			}
			if exists && channel.OwnerOrgID != out.Group.OwnerOrgID {
				return ErrOwnership
			}
			if exists {
				copy := channel
				out.Channel = &copy
			}
			before, err := readAdminBindingExistsTx(tx, id, change.ChannelID)
			if err != nil {
				return err
			}
			out.WasBound = &before
			out.WillBeBound = &bound
			out.Changed = before != bound
		}
		out.Counts, err = readStructuralCountsTx(tx, id)
		return err
	})
	if err != nil {
		return GroupPreview{}, err
	}
	return out, nil
}
