package routinggroup

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	sqliteDriver "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrInvalid     = errors.New("routinggroup: invalid input")
	ErrConflict    = errors.New("routinggroup: version conflict")
	ErrNotFound    = errors.New("routinggroup: not found")
	ErrOwnership   = errors.New("routinggroup: ownership mismatch")
	ErrUnavailable = errors.New("routinggroup: dependency unavailable")
	ErrInactive    = errors.New("routinggroup: inactive resource")
	ErrExists      = errors.New("routinggroup: group already exists")
)

type Organization struct {
	ID     uint64
	Active bool
}
type Channel struct {
	ID, OwnerOrgID uint64
	Status         int8
}

// Implementations perform current reads with UPDATE locks on the supplied tx,
// in the supplied ascending order. They MUST NOT open another transaction.
type OrganizationLocker interface {
	LockOrganizations(context.Context, *gorm.DB, []uint64) ([]Organization, error)
}
type ChannelLocker interface {
	LockChannels(context.Context, *gorm.DB, []uint64) ([]Channel, error)
}

// Only trusted host code can call this config service. HTTP commands require
// named-superadmin authentication; an Audit alone is not authentication.
type Service struct {
	DB            *gorm.DB
	Organizations OrganizationLocker
	Channels      ChannelLocker
}

func (s *Service) transact(ctx context.Context, a Audit, fn func(*gorm.DB) error) error {
	if a.ActorUserID == 0 || !a.valid() {
		return ErrInvalid
	}
	if s == nil || s.DB == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.DB.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx).Transaction(fn)
}

func (s *Service) lockOrg(tx *gorm.DB, id uint64) (Organization, error) {
	if id == 0 || s.Organizations == nil {
		return Organization{}, ErrUnavailable
	}
	rows, err := s.Organizations.LockOrganizations(tx.Statement.Context, tx, []uint64{id})
	if err != nil {
		return Organization{}, err
	}
	if len(rows) != 1 || rows[0].ID != id {
		return Organization{}, ErrNotFound
	}
	return rows[0], nil
}

func lockedGroup(tx *gorm.DB, owner, id uint64) (Group, error) {
	var g Group
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_org_id = ?", id, owner).Take(&g).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return g, ErrNotFound
	}
	return g, err
}

func checkVersion(have, want uint64) error {
	if want == 0 || have != want || have == math.MaxUint64 {
		return ErrConflict
	}
	return nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

func validText(name, description string) bool {
	return utf8.ValidString(name) && utf8.ValidString(description) && strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 128 && utf8.RuneCountInString(description) <= 512
}
func validStatus(s string) bool { return s == Draft || s == Enabled || s == Disabled }

type CreateInput struct {
	OwnerOrgID              uint64
	Slug, Name, Description string
	Audit                   Audit
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Group, error) {
	if !slugPattern.MatchString(in.Slug) || !validText(in.Name, in.Description) {
		return Group{}, ErrInvalid
	}
	var g Group
	err := s.transact(ctx, in.Audit, func(tx *gorm.DB) error {
		if in.OwnerOrgID != 0 {
			o, e := s.lockOrg(tx, in.OwnerOrgID)
			if e != nil {
				return e
			}
			if !o.Active {
				return ErrInactive
			}
		}
		g = Group{OwnerOrgID: in.OwnerOrgID, Slug: in.Slug, Name: in.Name, Description: in.Description, Status: Draft, PolicyVersion: 1}
		if e := tx.Create(&g).Error; e != nil {
			if groupInsertDuplicate(tx, e) {
				// Confirm this exact owner/slug using a current read. Never map a
				// primary-key collision or an event failure to a duplicate group.
				var existing Group
				check := tx.Session(&gorm.Session{NewDB: true, Context: tx.Statement.Context, Logger: logger.Discard}).
					Select("id, owner_org_id, slug").Clauses(clause.Locking{Strength: "SHARE"}).
					Where("owner_org_id = ? AND slug = ?", in.OwnerOrgID, in.Slug).Take(&existing)
				if check.Error == nil && existing.ID != 0 && existing.OwnerOrgID == in.OwnerOrgID && existing.Slug == in.Slug {
					return ErrExists
				}
			}
			return e
		}
		return event(tx, "group", g.ID, g.PolicyVersion, "group_created", g.OwnerOrgID, g.ID, 0, in.Audit)
	})
	if err != nil {
		return Group{}, err
	}
	return g, nil
}

// This classifier is used only for the routing_groups INSERT, never for an
// entire transaction. Driver text can include user values and is not parsed.
func groupInsertDuplicate(tx *gorm.DB, err error) bool {
	switch tx.Dialector.Name() {
	case "mysql":
		var duplicate *mysql.MySQLError
		return errors.As(err, &duplicate) && duplicate.Number == 1062
	case "sqlite":
		var duplicate *sqliteDriver.Error
		return errors.As(err, &duplicate) && duplicate.Code() == 2067 // SQLITE_CONSTRAINT_UNIQUE
	default:
		return false
	}
}

type UpdateInput struct {
	OwnerOrgID, GroupID, ExpectedVersion uint64
	Name, Description, Status            string
	Audit                                Audit
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Group, bool, error) {
	if in.GroupID == 0 || !validText(in.Name, in.Description) || !validStatus(in.Status) {
		return Group{}, false, ErrInvalid
	}
	var g Group
	changed := false
	err := s.transact(ctx, in.Audit, func(tx *gorm.DB) error {
		active := true
		if in.OwnerOrgID != 0 {
			o, e := s.lockOrg(tx, in.OwnerOrgID)
			if e != nil {
				return e
			}
			active = o.Active
		}
		var e error
		g, e = lockedGroup(tx, in.OwnerOrgID, in.GroupID)
		if e != nil {
			return e
		}
		if e = checkVersion(g.PolicyVersion, in.ExpectedVersion); e != nil {
			return e
		}
		if in.Status == Enabled && !active {
			return ErrInactive
		}
		if g.Name == in.Name && g.Description == in.Description && g.Status == in.Status {
			return nil
		}
		g.Name, g.Description, g.Status = in.Name, in.Description, in.Status
		g.PolicyVersion++
		changed = true
		if e = tx.Model(&Group{}).Where("id = ? AND owner_org_id = ?", g.ID, g.OwnerOrgID).Updates(map[string]any{"name": g.Name, "description": g.Description, "status": g.Status, "policy_version": g.PolicyVersion}).Error; e != nil {
			return e
		}
		return event(tx, "group", g.ID, g.PolicyVersion, "group_updated", g.OwnerOrgID, g.ID, 0, in.Audit)
	})
	if err != nil {
		return Group{}, false, err
	}
	return g, changed, nil
}

type BindingInput struct {
	OwnerOrgID, GroupID, ChannelID, ExpectedVersion uint64
	Bound                                           bool
	Audit                                           Audit
}

func (s *Service) SetBinding(ctx context.Context, in BindingInput) (Group, bool, error) {
	if in.GroupID == 0 || in.ChannelID == 0 {
		return Group{}, false, ErrInvalid
	}
	var g Group
	changed := false
	err := s.transact(ctx, in.Audit, func(tx *gorm.DB) error {
		if in.OwnerOrgID != 0 {
			o, e := s.lockOrg(tx, in.OwnerOrgID)
			if e != nil {
				return e
			}
			if in.Bound && !o.Active {
				return ErrInactive
			}
		}
		var e error
		g, e = lockedGroup(tx, in.OwnerOrgID, in.GroupID)
		if e != nil {
			return e
		}
		if e = checkVersion(g.PolicyVersion, in.ExpectedVersion); e != nil {
			return e
		}
		if s.Channels == nil {
			return ErrUnavailable
		}
		channels, e := s.Channels.LockChannels(tx.Statement.Context, tx, []uint64{in.ChannelID})
		if e != nil {
			return e
		}
		if len(channels) > 1 || len(channels) == 1 && channels[0].ID != in.ChannelID {
			return ErrUnavailable
		}
		// Removing a dangling binding is allowed; adding it requires a current
		// channel with the exact immutable ownership domain, never a cache.
		if in.Bound && len(channels) == 0 {
			return ErrNotFound
		}
		if len(channels) == 1 && channels[0].OwnerOrgID != g.OwnerOrgID {
			return ErrOwnership
		}
		var binding Binding
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ? AND channel_id = ?", g.ID, in.ChannelID).Take(&binding).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		exists := e == nil
		if exists == in.Bound {
			return nil
		}
		if in.Bound {
			e = tx.Create(&Binding{GroupID: g.ID, ChannelID: in.ChannelID}).Error
		} else {
			e = tx.Where("group_id = ? AND channel_id = ?", g.ID, in.ChannelID).Delete(&Binding{}).Error
		}
		if e != nil {
			return e
		}
		g.PolicyVersion++
		changed = true
		if e = tx.Model(&Group{}).Where("id = ? AND owner_org_id = ?", g.ID, g.OwnerOrgID).Update("policy_version", g.PolicyVersion).Error; e != nil {
			return e
		}
		kind := "channel_unbound"
		if in.Bound {
			kind = "channel_bound"
		}
		return event(tx, "group", g.ID, g.PolicyVersion, kind, g.OwnerOrgID, g.ID, in.ChannelID, in.Audit)
	})
	if err != nil {
		return Group{}, false, err
	}
	return g, changed, nil
}

type GrantInput struct {
	OrgID, GroupID, ExpectedVersion uint64
	Status                          string
	Audit                           Audit
}

func lockGrantGroup(tx *gorm.DB, org, id uint64) (Group, error) {
	var g Group
	e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND (owner_org_id = 0 OR owner_org_id = ?)", id, org).Take(&g).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return g, ErrNotFound
	}
	return g, e
}

func (s *Service) SetGrant(ctx context.Context, in GrantInput) (Grant, bool, error) {
	if in.OrgID == 0 || in.GroupID == 0 || (in.Status != Enabled && in.Status != Revoked) {
		return Grant{}, false, ErrInvalid
	}
	var grant Grant
	changed := false
	err := s.transact(ctx, in.Audit, func(tx *gorm.DB) error {
		org, e := s.lockOrg(tx, in.OrgID)
		if e != nil {
			return e
		}
		group, e := lockGrantGroup(tx, in.OrgID, in.GroupID)
		if e != nil {
			return e
		}
		if in.Status == Enabled && (!org.Active || group.Status != Enabled) {
			return ErrInactive
		}
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id = ?", in.OrgID, in.GroupID).Take(&grant).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			if in.ExpectedVersion != 0 {
				return ErrConflict
			}
			if in.Status != Enabled {
				return ErrNotFound
			}
			grant = Grant{OrgID: in.OrgID, GroupID: in.GroupID, Status: Enabled, GrantVersion: 1}
			if e = tx.Create(&grant).Error; e != nil {
				return e
			}
		} else {
			if e != nil {
				return e
			}
			if e = checkVersion(grant.GrantVersion, in.ExpectedVersion); e != nil {
				return e
			}
			if grant.Status == in.Status {
				return nil
			}
			grant.Status = in.Status
			grant.GrantVersion++
			grant.DefaultSlot = nil
			if e = tx.Model(&Grant{}).Where("id = ? AND org_id = ?", grant.ID, in.OrgID).Updates(map[string]any{"status": grant.Status, "grant_version": grant.GrantVersion, "default_slot": nil}).Error; e != nil {
				return e
			}
		}
		changed = true
		return event(tx, "grant", grant.ID, grant.GrantVersion, "grant_"+in.Status, in.OrgID, in.GroupID, 0, in.Audit)
	})
	if err != nil {
		return Grant{}, false, err
	}
	return grant, changed, nil
}

// nil expected/current means explicitly observed no default; it never means
// "ignore concurrent changes". Versions are mandatory for every non-nil side.
type DefaultInput struct {
	OrgID                                         uint64
	ExpectedGroupID, GroupID                      *uint64
	ExpectedCurrentVersion, ExpectedTargetVersion uint64
	Audit                                         Audit
}

func sameID(a, b *uint64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (s *Service) SetDefault(ctx context.Context, in DefaultInput) (bool, error) {
	if in.OrgID == 0 || in.ExpectedGroupID != nil && *in.ExpectedGroupID == 0 || in.GroupID != nil && *in.GroupID == 0 {
		return false, ErrInvalid
	}
	changed := false
	err := s.transact(ctx, in.Audit, func(tx *gorm.DB) error {
		org, e := s.lockOrg(tx, in.OrgID)
		if e != nil {
			return e
		}
		// Org lock serializes all default writers. This pre-read is only to
		// discover group IDs before locking groups, not a grant row lock.
		var current Grant
		e = tx.Where("org_id = ? AND default_slot = 1", in.OrgID).Take(&current).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		var currentID *uint64
		if e == nil {
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
		groups := map[uint64]Group{}
		for _, id := range ids {
			g, e := lockGrantGroup(tx, in.OrgID, id)
			if e != nil {
				return e
			}
			groups[id] = g
		}
		if in.GroupID != nil && (!org.Active || groups[*in.GroupID].Status != Enabled) {
			return ErrInactive
		}
		if len(ids) == 0 {
			return nil
		}
		var grants []Grant
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id IN ?", in.OrgID, ids).Order("id ASC").Find(&grants).Error; e != nil {
			return e
		}
		byGroup := map[uint64]*Grant{}
		for i := range grants {
			byGroup[grants[i].GroupID] = &grants[i]
		}
		if currentID != nil {
			g := byGroup[*currentID]
			if g == nil || g.DefaultSlot == nil || *g.DefaultSlot != 1 || g.Status != Enabled {
				return ErrConflict
			}
			if e = checkVersion(g.GrantVersion, in.ExpectedCurrentVersion); e != nil {
				return e
			}
		}
		if in.GroupID != nil {
			g := byGroup[*in.GroupID]
			if g == nil {
				return ErrNotFound
			}
			if g.Status != Enabled {
				return ErrInactive
			}
			if e = checkVersion(g.GrantVersion, in.ExpectedTargetVersion); e != nil {
				return e
			}
		}
		if sameID(currentID, in.GroupID) {
			return nil
		}
		// Clear before set, preserving the unique constraint at each statement.
		for _, id := range []*uint64{currentID, in.GroupID} {
			if id == nil {
				continue
			}
			g := byGroup[*id]
			g.GrantVersion++
			var slot *uint8
			kind := "default_cleared"
			if in.GroupID != nil && *id == *in.GroupID {
				one := uint8(1)
				slot = &one
				kind = "default_set"
			}
			if e = tx.Model(&Grant{}).Where("id = ? AND org_id = ?", g.ID, in.OrgID).Updates(map[string]any{"default_slot": slot, "grant_version": g.GrantVersion}).Error; e != nil {
				return e
			}
			if e = event(tx, "grant", g.ID, g.GrantVersion, kind, in.OrgID, g.GroupID, 0, in.Audit); e != nil {
				return e
			}
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}
