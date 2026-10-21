package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"strings"
	"time"
)

// ConsoleOptions contains trusted deployment dependencies, never client supplied
// credentials or URLs. Missing dependencies leave the respective feature closed.
type ConsoleOptions struct {
	enabled     bool
	CallbackURL string // Verified console return page, never the provider callback receiver.
	Media       *MediaStore
}

func (s *Service) ConfigureConsole(o ConsoleOptions) error {
	if o.CallbackURL != "" && !validHTTPS(o.CallbackURL, 2048) {
		return ErrInvalid
	}
	if o.CallbackURL != "" {
		u, _ := url.Parse(o.CallbackURL)
		if u.Path == PortraitCallbackPath {
			return ErrInvalid
		}
	}
	o.enabled = true
	s.console = o
	return nil
}

type Details struct {
	ResourceID   string     `gorm:"primaryKey" json:"-"`
	OrgID        uint64     `json:"-"`
	Description  string     `json:"description"`
	Source       string     `json:"source"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	UpdatedAt    time.Time  `json:"-"`
}

func (Details) TableName() string { return "video_asset_details" }

type PortraitLabel struct {
	SessionID         string `gorm:"primaryKey"`
	OrgID             uint64
	Name, Description string
	NameSynced        bool
}

func (PortraitLabel) TableName() string { return "video_portrait_labels" }
func saveDetails(tx *gorm.DB, org uint64, id, description, source string, now time.Time) error {
	return tx.Create(&Details{ResourceID: id, OrgID: org, Description: description, Source: source, UpdatedAt: now}).Error
}
func (s *Service) GetPortraitForCaller(ctx context.Context, c CreateContext, id string, withURL bool) (PortraitDTO, error) {
	if c.CallerKind != "session_user" {
		return s.GetPortrait(ctx, c.OrgID, id, c.KeyID, withURL)
	}
	if c.UserID == 0 || c.KeyID != 0 {
		return PortraitDTO{}, ErrAuthorization
	}
	var p Portrait
	if err := s.db.WithContext(ctx).Where("org_id = ? AND session_id = ?", c.OrgID, id).Take(&p).Error; err != nil {
		return PortraitDTO{}, ErrNotFound
	}
	if withURL && (p.OwnerUserID != c.UserID || p.CreatedByKeyID != 0) {
		return PortraitDTO{}, ErrSecret
	}
	return s.GetPortrait(ctx, c.OrgID, id, 0, withURL)
}

type CapabilityView struct {
	Ready            bool     `json:"ready"`
	ScopeID          string   `json:"scope_id,omitempty"`
	Project          string   `json:"project,omitempty"`
	Virtual          bool     `json:"virtual"`
	Portrait         bool     `json:"portrait"`
	MediaTypes       []string `json:"media_types"`
	Sync             bool     `json:"sync"`
	Update           bool     `json:"update"`
	Delete           bool     `json:"delete"`
	DeleteEmptyGroup bool     `json:"delete_empty_group"`
	Upload           bool     `json:"upload"`
	Reason           string   `json:"reason,omitempty"`
}

func (s *Service) ConsoleCapabilities(ctx context.Context, org uint64) CapabilityView {
	out := CapabilityView{MediaTypes: []string{}}
	r, err := s.resolveConfiguredScope(ctx, org, Scope{})
	if err != nil {
		out.Reason = "management_scope_not_configured"
		if errors.Is(err, ErrExecutionPolicy) {
			out.Reason = "execution_policy_required"
		}
		return out
	}
	if !r.ExecutionRetentionConsent {
		out.Reason = "execution_policy_required"
		return out
	}
	out.Ready = true
	out.ScopeID = r.Scope.ID
	out.Project = r.Scope.Project
	out.Virtual = r.Capabilities.VirtualGroups
	out.Portrait = r.Capabilities.Portrait && s.consolePortraitReady(ctx, org)
	out.Sync = r.Capabilities.Sync
	out.Update = r.Capabilities.Update
	out.Delete = r.Capabilities.DeleteAssets
	out.DeleteEmptyGroup = r.Capabilities.DeleteVirtualEmptyGroups && r.Capabilities.ExclusiveProjectWrites
	out.Upload = s.console.Media != nil
	for _, t := range []string{"image", "video", "audio"} {
		if mediaAllowed(r.Capabilities, t) {
			out.MediaTypes = append(out.MediaTypes, t)
		}
	}
	return out
}
func (s *Service) CreateConsolePortrait(ctx context.Context, c CreateContext, in PortraitCreate) (PortraitDTO, bool, error) {
	if !s.consolePortraitReady(ctx, c.OrgID) {
		return PortraitDTO{}, false, ErrUnsupported
	}
	in.CallbackURL = s.console.CallbackURL
	return s.CreatePortrait(ctx, c, in)
}

func (s *Service) consolePortraitReady(ctx context.Context, org uint64) bool {
	if s.console.CallbackURL == "" || !validPlatformCallback(s.options.PlatformCallbackURL) || s.options.ResolveCallback == nil || s.options.ResolveCallbackByID == nil {
		return false
	}
	target, err := s.options.ResolveCallback(ctx, org, s.console.CallbackURL)
	if err != nil || !validID(target.ID) || target.URL != s.console.CallbackURL {
		return false
	}
	current, err := s.options.ResolveCallbackByID(ctx, org, target.ID)
	return err == nil && current.ID == target.ID && current.URL == target.URL
}

type ListFilter struct {
	Kind, GroupID, Name, MediaType, Status, Cursor string
	Limit                                          int
}
type ConsoleGroup struct {
	GroupDTO
	Description       string     `json:"description"`
	Version           uint64     `json:"version"`
	Source            string     `json:"source"`
	LastSyncedAt      *time.Time `json:"last_synced_at,omitempty"`
	PortraitSessionID string     `json:"portrait_session_id,omitempty"`
}
type ConsoleAsset struct {
	AssetDTO
	Version    uint64 `json:"version"`
	Stage      string `json:"stage"`
	Bytes      int64  `json:"bytes"`
	DurationMS int64  `json:"duration_ms"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	ErrorCode  string `json:"error_code,omitempty"`
	PreviewURL string `json:"preview_url,omitempty"`
	Source     string `json:"source"`
}
type GroupPage struct {
	Data       []ConsoleGroup `json:"data"`
	Total      int64          `json:"total"`
	NextCursor string         `json:"next_cursor,omitempty"`
}
type AssetPage struct {
	Data       []ConsoleAsset `json:"data"`
	Total      int64          `json:"total"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func filterIdentity(f ListFilter) string {
	f.Cursor = ""
	f.Limit = 0
	return "console:" + hash(bodyJSON(f))
}
func validFilter(f ListFilter) bool {
	return f.Limit >= 1 && f.Limit <= 100 && safeName(f.Name, 100, false) && (f.Kind == "" || f.Kind == KindVirtual || f.Kind == "real_person") && (f.MediaType == "" || f.MediaType == "image" || f.MediaType == "video" || f.MediaType == "audio")
}
func applyName(q *gorm.DB, name string) *gorm.DB {
	if name != "" {
		q = q.Where("name LIKE ? ESCAPE '!'", "%"+strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(name)+"%")
	}
	return q
}
func filterStatus(status string) (string, bool) {
	switch status {
	case "":
		return "", true
	case "ready":
		return StatusActive, true
	case "needs_review", "verifying":
		return "needs_review", true
	case "uploading":
		return "uploading", true
	case "registering", "processing":
		return StatusProcessing, true
	case "creating":
		return StatusPending, true
	case "failed":
		return StatusFailed, true
	case "deleting":
		return StatusDeleting, true
	}
	return "", false
}
func (s *Service) ConsoleGroups(ctx context.Context, org uint64, f ListFilter) (GroupPage, error) {
	out := GroupPage{Data: []ConsoleGroup{}}
	if org == 0 || !validFilter(f) {
		return out, ErrInvalid
	}
	status, ok := filterStatus(f.Status)
	if !ok {
		return out, ErrInvalid
	}
	after, err := s.parseCursor(org, filterIdentity(f), f.Cursor)
	if err != nil {
		return out, err
	}
	q := applyName(s.db.WithContext(ctx).Model(&Group{}).Where("org_id = ? AND deleted_at IS NULL", org), f.Name)
	if f.Kind != "" {
		kind := f.Kind
		if kind == "real_person" {
			kind = KindReal
		}
		q = q.Where("kind = ?", kind)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err = q.Count(&out.Total).Error; err != nil {
		return out, ErrStorage
	}
	if after != "" {
		q = q.Where("group_id > ?", after)
	}
	var rows []Group
	if q.Order("group_id").Limit(f.Limit+1).Find(&rows).Error != nil {
		return out, ErrStorage
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		out.NextCursor = s.cursor(org, filterIdentity(f), rows[len(rows)-1].ID)
	}
	for _, row := range rows {
		dto, e := s.GetGroup(ctx, org, row.ID)
		if e != nil {
			return out, e
		}
		d := Details{}
		e = s.db.WithContext(ctx).Where("org_id = ? AND resource_id = ?", org, row.ID).Take(&d).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return out, ErrStorage
		}
		out.Data = append(out.Data, ConsoleGroup{dto, d.Description, row.RowVersion, d.Source, d.LastSyncedAt, row.PortraitID})
	}
	return out, nil
}
func (s *Service) ConsoleAssets(ctx context.Context, org uint64, f ListFilter) (AssetPage, error) {
	out := AssetPage{Data: []ConsoleAsset{}}
	if org == 0 || !validFilter(f) {
		return out, ErrInvalid
	}
	status, ok := filterStatus(f.Status)
	if !ok {
		return out, ErrInvalid
	}
	after, err := s.parseCursor(org, filterIdentity(f), f.Cursor)
	if err != nil {
		return out, err
	}
	q := applyName(s.db.WithContext(ctx).Model(&Asset{}).Where("org_id = ? AND deleted_at IS NULL", org), f.Name)
	if f.GroupID != "" {
		q = q.Where("group_id = ?", f.GroupID)
	}
	if f.MediaType != "" {
		q = q.Where("media_type = ?", f.MediaType)
	}
	ready := `video_assets.review_state = ? AND video_assets.metadata_version > 0 AND video_assets.immutable_object_ref <> '' AND video_assets.provider_id IS NOT NULL AND EXISTS (SELECT 1 FROM video_asset_groups g WHERE g.org_id = video_assets.org_id AND g.group_id = video_assets.group_id AND g.deleted_at IS NULL AND g.status = ? AND g.review_state = ? AND g.scope_id = video_assets.scope_id AND g.scope_version = video_assets.scope_version AND (g.kind = ? OR EXISTS (SELECT 1 FROM video_portrait_authorizations p WHERE p.org_id = g.org_id AND p.session_id = g.portrait_id AND p.session_status IN ('succeeded','imported_verified') AND p.license_status = 'verified' AND p.license_scope = 'video_generation' AND p.license_evidence_ref <> '' AND p.revoked_at IS NULL AND p.valid_from <= ? AND p.valid_until > ? AND p.last_verified_at <= ? AND p.last_verified_at >= ? AND p.reverify_due_at > ?)))`
	now := s.now()
	args := []any{ReviewClear, StatusActive, ReviewClear, KindVirtual, now, now, now, now.Add(-s.options.MaxLicenseAge), now}
	if status == "needs_review" {
		q = q.Where("status = ?", StatusActive).Where("NOT ("+ready+")", args...)
	} else if status != "" {
		q = q.Where("status = ?", status)
		if status == StatusActive {
			q = q.Where(ready, args...)
		}
	}
	if err = q.Count(&out.Total).Error; err != nil {
		return out, ErrStorage
	}
	if after != "" {
		q = q.Where("asset_id > ?", after)
	}
	var rows []Asset
	if q.Order("asset_id").Limit(f.Limit+1).Find(&rows).Error != nil {
		return out, ErrStorage
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		out.NextCursor = s.cursor(org, filterIdentity(f), rows[len(rows)-1].ID)
	}
	for _, row := range rows {
		dto, e := s.GetAsset(ctx, org, row.ID)
		if e != nil {
			return out, e
		}
		stage := dto.Status
		if row.Status == StatusPending || row.Status == StatusProcessing {
			stage = "registering"
		}
		if row.ReviewState == ReviewRequired {
			stage = "needs_review"
		}
		preview := ""
		if row.ImmutableObjectRef != "" && s.console.Media != nil {
			preview = "/api/video/assets/" + row.ID + "/preview"
		}
		source := "platform"
		var detail Details
		if err := s.db.WithContext(ctx).Where("org_id = ? AND resource_id = ?", org, row.ID).Take(&detail).Error; err == nil {
			source = detail.Source
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrStorage
		}
		out.Data = append(out.Data, ConsoleAsset{dto, row.RowVersion, stage, row.ByteSize, row.DurationMS, row.Width, row.Height, row.SafeErrorCode, preview, source})
	}
	return out, nil
}

type Rename struct {
	Name            string  `json:"name"`
	Description     *string `json:"description,omitempty"`
	ExpectedVersion uint64  `json:"expected_version"`
}

func (s *Service) RenameResource(ctx context.Context, c CreateContext, id string, isGroup bool, in Rename) (OperationDTO, error) {
	if !validCreate(c) || !validID(id) || !safeName(in.Name, 64, true) || in.ExpectedVersion == 0 || in.Description != nil && !safeName(*in.Description, 300, false) || !isGroup && in.Description != nil {
		return OperationDTO{}, ErrInvalid
	}
	var group Group
	var asset Asset
	action, kind, path := "UpdateAsset", "asset", "/v1/video/assets/"+id
	if isGroup {
		action, kind, path = "UpdateAssetGroup", "asset_group", "/v1/video/asset-groups/"+id
		if s.db.WithContext(ctx).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", c.OrgID, id).Take(&group).Error != nil {
			return OperationDTO{}, ErrNotFound
		}
	} else {
		if s.db.WithContext(ctx).Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", c.OrgID, id).Take(&asset).Error != nil {
			return OperationDTO{}, ErrNotFound
		}
		if s.db.WithContext(ctx).Where("org_id = ? AND group_id = ?", c.OrgID, asset.GroupID).Take(&group).Error != nil {
			return OperationDTO{}, ErrNotFound
		}
	}
	r, err := s.resolve(ctx, c.OrgID, groupScope(group))
	if err != nil || !r.Capabilities.Update {
		return OperationDTO{}, ErrUnsupported
	}
	provider := group.ProviderID
	if !isGroup {
		provider = asset.ProviderID
	}
	if provider == nil {
		return OperationDTO{}, ErrConflict
	}
	body := map[string]any{"Id": *provider, "Name": in.Name, "ProjectName": r.Scope.Project}
	if in.Description != nil {
		body["Description"] = *in.Description
	}
	op, _, err := s.accept(ctx, c, r, path, action, kind, id, bodyJSON(in), bodyJSON(body), func(tx *gorm.DB, _ Operation) error {
		var n int64
		if tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND status IN ?", c.OrgID, id, []string{"pending", "in_flight", "unknown"}).Count(&n).Error != nil {
			return ErrStorage
		}
		if n > 0 {
			return ErrConflict
		}
		q := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND row_version = ? AND status <> ? AND deleted_at IS NULL", c.OrgID, id, in.ExpectedVersion, StatusDeleting)
		if isGroup {
			q = tx.Model(&Group{}).Where("org_id = ? AND group_id = ? AND row_version = ? AND status = ? AND deleted_at IS NULL", c.OrgID, id, in.ExpectedVersion, StatusActive)
		}
		res := q.Update("row_version", gorm.Expr("row_version + 1"))
		if res.Error != nil {
			return ErrStorage
		}
		if res.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	return operationDTO(op), err
}
func (s *Service) DeleteAsset(ctx context.Context, c CreateContext, id string) (OperationDTO, bool, error) {
	if !validCreate(c) || !validID(id) {
		return OperationDTO{}, false, ErrInvalid
	}
	var a Asset
	if s.db.WithContext(ctx).Where("org_id = ? AND asset_id = ?", c.OrgID, id).Take(&a).Error != nil {
		return OperationDTO{}, false, ErrNotFound
	}
	if a.DeletedAt != nil {
		return OperationDTO{}, true, nil
	}
	var g Group
	if s.db.WithContext(ctx).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", c.OrgID, a.GroupID).Take(&g).Error != nil {
		return OperationDTO{}, false, ErrNotFound
	}
	r, err := s.resolve(ctx, c.OrgID, groupScope(g))
	if err != nil || !r.Capabilities.DeleteAssets {
		return OperationDTO{}, false, ErrUnsupported
	}
	if a.ProviderID == nil || (a.Status != StatusActive && a.Status != StatusFailed) {
		return OperationDTO{}, false, ErrConflict
	}
	op, _, err := s.accept(ctx, c, r, "/v1/video/assets/"+id, "DeleteAsset", "asset", id, bodyJSON(map[string]string{"id": id}), bodyJSON(map[string]string{"Id": *a.ProviderID, "ProjectName": r.Scope.Project}), func(tx *gorm.DB, _ Operation) error {
		var mutating int64
		if tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND status IN ?", c.OrgID, id, []string{"pending", "in_flight", "unknown"}).Count(&mutating).Error != nil {
			return ErrStorage
		}
		if mutating > 0 {
			return ErrConflict
		}
		var count int64
		if tx.Model(&TaskReference{}).Where("org_id = ? AND asset_id = ? AND released_at IS NULL", c.OrgID, id).Count(&count).Error != nil {
			return ErrStorage
		}
		if count > 0 {
			return ErrInUse
		}
		res := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND status = ? AND row_version = ? AND deleted_at IS NULL", c.OrgID, id, a.Status, a.RowVersion).Updates(map[string]any{"status": StatusDeleting, "row_version": gorm.Expr("row_version + 1"), "updated_at": nowMillis(s)})
		if res.Error != nil {
			return ErrStorage
		}
		if res.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	}, string(bodyJSON(map[string]string{"prior_status": a.Status, "prior_review_state": a.ReviewState})))
	return operationDTO(op), false, err
}
func (s *Service) applyConsoleAccepted(tx *gorm.DB, op Operation, result Result, request []byte) error {
	switch op.Action {
	case "UpdateAsset", "UpdateAssetGroup":
		var response struct{ ID string }
		var in struct {
			ID, Name    string
			Description *string
		}
		if json.Unmarshal(result.Data, &response) != nil || json.Unmarshal(request, &in) != nil || response.ID != in.ID {
			return ErrInvalid
		}
		updates := map[string]any{"name": in.Name, "updated_at": nowMillis(s), "row_version": gorm.Expr("row_version + 1"), "review_state": ReviewClear}
		q := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND provider_id = ? AND deleted_at IS NULL", op.OrgID, op.ResourceID, in.ID)
		if op.Action == "UpdateAssetGroup" {
			q = tx.Model(&Group{}).Where("org_id = ? AND group_id = ? AND provider_id = ? AND deleted_at IS NULL", op.OrgID, op.ResourceID, in.ID)
		}
		if q.Updates(updates).Error != nil {
			return ErrStorage
		}
		if in.Description != nil {
			if tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource_id"}}, DoUpdates: clause.Assignments(map[string]any{"description": *in.Description, "updated_at": nowMillis(s)})}).Create(&Details{ResourceID: op.ResourceID, OrgID: op.OrgID, Description: *in.Description, Source: "platform", UpdatedAt: nowMillis(s)}).Error != nil {
				return ErrStorage
			}
		}
		return nil
	case "DeleteAsset":
		var response map[string]json.RawMessage
		if json.Unmarshal(result.Data, &response) != nil || response == nil || len(response) != 0 {
			return ErrInvalid
		}
		now := nowMillis(s)
		if tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND status = ?", op.OrgID, op.ResourceID, StatusDeleting).Updates(map[string]any{"status": StatusDeleted, "deleted_at": now, "updated_at": now, "review_state": ReviewClear, "row_version": gorm.Expr("row_version + 1")}).Error != nil {
			return ErrStorage
		}
		return nil
	}
	return ErrUnsupported
}
func (s *Service) syncPortraitNames(ctx context.Context) error {
	var labels []PortraitLabel
	if s.db.WithContext(ctx).Where("name_synced = ?", false).Limit(16).Find(&labels).Error != nil {
		return ErrStorage
	}
	for _, label := range labels {
		var p Portrait
		if s.db.WithContext(ctx).Where("org_id = ? AND session_id = ? AND session_status = ?", label.OrgID, label.SessionID, "succeeded").Take(&p).Error != nil || p.GroupID == nil {
			continue
		}
		if label.Name == "" {
			if s.db.Model(&PortraitLabel{}).Where("org_id = ? AND session_id = ?", label.OrgID, label.SessionID).Update("name_synced", true).Error != nil {
				return ErrStorage
			}
			continue
		}
		var g Group
		if s.db.Where("org_id = ? AND group_id = ?", p.OrgID, *p.GroupID).Take(&g).Error != nil {
			return ErrStorage
		}
		if g.ProviderID == nil {
			continue
		}
		r, err := s.resolve(ctx, p.OrgID, groupScope(g))
		if err != nil || !r.Capabilities.Update {
			continue
		}
		var confirmed int64
		s.db.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND action = ? AND status = ?", p.OrgID, g.ID, "UpdateAssetGroup", "confirmed").Count(&confirmed)
		if confirmed > 0 {
			if s.db.Model(&PortraitLabel{}).Where("org_id = ? AND session_id = ?", p.OrgID, p.ID).Update("name_synced", true).Error != nil {
				return ErrStorage
			}
			continue
		}
		if err := s.enqueueQuery(ctx, p.OrgID, groupScope(g), "asset_group", g.ID, "UpdateAssetGroup", bodyJSON(map[string]string{"Id": *g.ProviderID, "Name": label.Name, "Description": label.Description, "ProjectName": g.Project})); err != nil {
			return err
		}
	}
	return nil
}
func callerLabel(c CreateContext) string {
	if c.CallerKind == "session_user" {
		return fmt.Sprintf("user:%d", c.UserID)
	}
	return fmt.Sprintf("key:%d", c.KeyID)
}
