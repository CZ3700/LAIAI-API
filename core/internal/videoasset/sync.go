package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

type SyncGate struct {
	AccountKey     string `gorm:"primaryKey"`
	Version        uint64
	NextDispatchAt *time.Time
}

func (SyncGate) TableName() string { return "video_asset_sync_gates" }
func lockSyncGate(tx *gorm.DB, account, region string) (SyncGate, error) {
	gate := SyncGate{AccountKey: hash(bodyJSON([]string{account, region}))}
	if tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_key"}}, DoNothing: true}).Create(&gate).Error != nil {
		return gate, ErrStorage
	}
	if tx.Model(&SyncGate{}).Where("account_key = ?", gate.AccountKey).Update("version", gorm.Expr("version + 1")).Error != nil {
		return gate, ErrStorage
	}
	if tx.Where("account_key = ?", gate.AccountKey).Take(&gate).Error != nil {
		return gate, ErrStorage
	}
	return gate, nil
}

type SyncJob struct {
	ID                                              string     `gorm:"primaryKey" json:"id"`
	OrgID                                           uint64     `json:"-"`
	ScopeID                                         string     `json:"scope_id"`
	ScopeVersion                                    uint64     `json:"-"`
	AccountID                                       string     `json:"account_id"`
	Region                                          string     `json:"region"`
	Project                                         string     `json:"project"`
	Mode                                            string     `json:"mode"`
	Kind                                            string     `json:"kind"`
	Phase                                           string     `json:"phase"`
	Status                                          string     `json:"status"`
	GroupID, AfterGroupID, CheckpointID, SeenHashes string     `json:"-"`
	IdempotencyHash                                 string     `json:"-"`
	Pages                                           int64      `json:"pages"`
	Items                                           int64      `json:"items"`
	Fence                                           uint64     `json:"-"`
	LeaseUntil                                      *time.Time `json:"-"`
	ErrorCode                                       string     `json:"error_code,omitempty"`
	CreatedAt, UpdatedAt                            time.Time
}

func (SyncJob) TableName() string { return "video_asset_sync_jobs" }

type Discovery struct {
	ID                   string `gorm:"primaryKey" json:"id"`
	ScopeID              string `json:"scope_id"`
	ScopeVersion         uint64 `json:"scope_version"`
	AccountID            string `json:"account_id"`
	Region               string `json:"region"`
	Project              string `json:"project"`
	ProviderID           string `json:"provider_id"`
	Kind                 string `json:"kind"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Status               string `json:"status"`
	AssignedOrg          uint64 `json:"assigned_org,omitempty"`
	GroupID              string `json:"group_id,omitempty"`
	Version              uint64 `json:"version"`
	CreatedAt, UpdatedAt time.Time
}

func (Discovery) TableName() string { return "video_asset_discoveries" }

type upstreamGroup struct{ ID, Name, Description, GroupType, ProjectName string }
type upstreamAsset struct {
	ID, Name, GroupID, AssetType, Status, ProjectName, URL string
	Error                                                  struct{ Code string }
}

func (s *Service) consoleEnabled() bool { return s.console.enabled }
func extractProviderID(raw []byte) string {
	var v struct{ ID string }
	_ = json.Unmarshal(raw, &v)
	return v.ID
}
func (s *Service) CreateSyncJob(ctx context.Context, c CreateContext, kind, mode string) (SyncJob, error) {
	if !validCreate(c) || (kind != KindVirtual && kind != "real_person") || (mode != "owned" && mode != "discover") {
		return SyncJob{}, ErrInvalid
	}
	r, e := s.resolve(ctx, c.OrgID, Scope{})
	if e != nil || !r.Capabilities.Sync {
		return SyncJob{}, ErrUnsupported
	}
	id, e := newID("asj_")
	if e != nil {
		return SyncJob{}, e
	}
	key := s.digest(s.options.ActiveHMACKey, "sync", c.OrgID, kind+":"+mode, c.IdempotencyKey)
	out := SyncJob{}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, c.OrgID); e != nil {
			return e
		}
		e := tx.Where("org_id = ? AND idempotency_hash = ?", c.OrgID, key).Take(&out).Error
		if e == nil {
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrStorage
		}
		if _, err := lockSyncGate(tx, r.Scope.AccountID, r.Scope.Region); err != nil {
			return err
		}
		var n int64
		if tx.Model(&SyncJob{}).Where("scope_id = ? AND status IN ?", r.Scope.ID, []string{"pending", "running"}).Count(&n).Error != nil {
			return ErrStorage
		}
		if n > 0 {
			return ErrConflict
		}
		now := nowMillis(s)
		out = SyncJob{ID: id, OrgID: c.OrgID, ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version, AccountID: r.Scope.AccountID, Region: r.Scope.Region, Project: r.Scope.Project, Mode: mode, Kind: kind, Phase: "groups", Status: "pending", IdempotencyHash: key, CreatedAt: now, UpdatedAt: now}
		if tx.Create(&out).Error != nil {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, "sync", id, "", "sync_requested", "", callerLabel(c))
	})
	return out, e
}
func (s *Service) GetSyncJob(ctx context.Context, org uint64, id string, admin bool) (SyncJob, error) {
	var out SyncJob
	q := s.db.WithContext(ctx).Where("org_id = ? AND id = ?", org, id)
	if !admin {
		q = q.Where("mode = ?", "owned")
	}
	if q.Take(&out).Error != nil {
		return out, ErrNotFound
	}
	return out, nil
}
func (s *Service) ListDiscoveries(ctx context.Context, scope string) ([]Discovery, error) {
	var out []Discovery
	if !validID(scope) {
		return nil, ErrInvalid
	}
	if s.db.WithContext(ctx).Where("scope_id = ? AND status = ?", scope, "unassigned").Order("id").Limit(100).Find(&out).Error != nil {
		return nil, ErrStorage
	}
	return out, nil
}
func (s *Service) AssignDiscovery(ctx context.Context, c CreateContext, id string, version uint64, evidence string) (GroupDTO, error) {
	if !validCreate(c) || !validID(evidence) {
		return GroupDTO{}, ErrInvalid
	}
	var d Discovery
	if s.db.WithContext(ctx).Where("id = ?", id).Take(&d).Error != nil {
		return GroupDTO{}, ErrNotFound
	}
	r, e := s.resolve(ctx, c.OrgID, Scope{d.ScopeID, d.ScopeVersion, d.AccountID, d.Region, d.Project})
	if e != nil {
		return GroupDTO{}, ErrUnsupported
	}
	_ = r
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, c.OrgID); e != nil {
			return e
		}
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&d).Error != nil {
			return ErrNotFound
		}
		if d.Status == "assigned" && d.AssignedOrg == c.OrgID {
			return nil
		}
		if d.Status != "unassigned" || d.Version != version {
			return ErrConflict
		}
		gid, e := newID("ag_")
		if e != nil {
			return e
		}
		now := nowMillis(s)
		g := Group{ID: gid, OrgID: c.OrgID, Kind: d.Kind, Name: d.Name, ScopeID: d.ScopeID, ScopeVersion: d.ScopeVersion, AccountID: d.AccountID, Region: d.Region, Project: d.Project, ProviderID: &d.ProviderID, Status: StatusActive, ReviewState: ReviewClear, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if d.Kind == KindReal {
			pid, e := newID("pas_")
			if e != nil {
				return e
			}
			g.PortraitID = pid
			p := Portrait{ID: pid, OrgID: c.OrgID, OwnerUserID: c.UserID, ScopeID: d.ScopeID, ScopeVersion: d.ScopeVersion, AccountID: d.AccountID, Region: d.Region, Project: d.Project, SessionStatus: "imported_pending", LicenseStatus: "unknown", GroupID: &gid, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
			if tx.Create(&p).Error != nil {
				return ErrStorage
			}
		}
		if tx.Create(&g).Error != nil {
			return ErrConflict
		}
		if saveDetails(tx, c.OrgID, gid, d.Description, "imported", now) != nil {
			return ErrStorage
		}
		if tx.Model(&Discovery{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{"status": "assigned", "assigned_org": c.OrgID, "group_id": gid, "version": version + 1, "updated_at": now}).Error != nil {
			return ErrStorage
		}
		d.GroupID = gid
		return s.event(tx, c.OrgID, "asset_group", gid, "", "official_group_assigned", evidence, callerLabel(c))
	})
	if e != nil {
		return GroupDTO{}, e
	}
	return s.GetGroup(ctx, c.OrgID, d.GroupID)
}
func (s *Service) processSyncJobs(ctx context.Context) error {
	// Read-only jobs may safely retry their checkpoint after a lost worker. The
	// fenced apply prevents that worker from overwriting a newer committed page.
	var jobs []SyncJob
	if s.db.WithContext(ctx).Where("status = ? OR (status = ? AND lease_until <= ?)", "pending", "running", s.now()).Order("created_at").Limit(2).Find(&jobs).Error != nil {
		return ErrStorage
	}
	for _, job := range jobs {
		if e := s.syncPage(ctx, job); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) syncPage(ctx context.Context, job SyncJob) error {
	now := nowMillis(s)
	lease := now.Add(2 * time.Minute)
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		gate, err := lockSyncGate(tx, job.AccountID, job.Region)
		if err != nil {
			return err
		}
		if gate.NextDispatchAt != nil && gate.NextDispatchAt.After(now) {
			return nil
		}
		claim := tx.Model(&SyncJob{}).Where("id = ? AND fence = ? AND (status = ? OR (status = ? AND lease_until <= ?))", job.ID, job.Fence, "pending", "running", now).Updates(map[string]any{"status": "running", "fence": job.Fence + 1, "lease_until": lease, "updated_at": now})
		if claim.Error != nil {
			return ErrStorage
		}
		if claim.RowsAffected != 1 {
			return nil
		}
		if tx.Model(&SyncGate{}).Where("account_key = ?", gate.AccountKey).Update("next_dispatch_at", now.Add(time.Second)).Error != nil {
			return ErrStorage
		}
		claimed = true
		return nil
	})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	job.Fence++
	fail := func(code string) error {
		return s.db.WithContext(ctx).Model(&SyncJob{}).Where("id = ? AND fence = ?", job.ID, job.Fence).Updates(map[string]any{"status": "failed", "error_code": code, "lease_until": nil, "updated_at": nowMillis(s)}).Error
	}
	r, e := s.resolve(ctx, job.OrgID, Scope{job.ScopeID, job.ScopeVersion, job.AccountID, job.Region, job.Project})
	if e != nil || !r.Capabilities.Sync {
		return fail("management_scope_unavailable")
	}
	groupType := "AIGC"
	kind := KindVirtual
	if job.Kind == "real_person" {
		groupType = "LivenessFace"
		kind = KindReal
	}
	filter := map[string]any{"GroupType": groupType}
	action := "ListAssetGroups"
	var group Group
	if job.Phase == "assets" {
		q := s.db.WithContext(ctx).Where("org_id = ? AND scope_id = ? AND kind = ? AND deleted_at IS NULL AND provider_id IS NOT NULL", job.OrgID, job.ScopeID, kind)
		if job.GroupID != "" {
			q = q.Where("group_id = ?", job.GroupID)
		} else {
			q = q.Where("group_id > ?", job.AfterGroupID)
		}
		e = q.Order("group_id").Take(&group).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return s.db.Model(&SyncJob{}).Where("id = ? AND fence = ?", job.ID, job.Fence).Updates(map[string]any{"status": "succeeded", "lease_until": nil, "updated_at": nowMillis(s)}).Error
		}
		if e != nil {
			return fail("resource_query_failed")
		}
		if group.Status == StatusDeleting {
			return fail("resource_mutation_in_progress")
		}
		action = "ListAssets"
		filter["GroupIds"] = []string{*group.ProviderID}
		job.GroupID = group.ID
	}
	body := map[string]any{"Filter": filter, "MaxResults": 100, "ProjectName": job.Project, "SortBy": "CreateTime", "SortOrder": "Asc"}
	if job.CheckpointID != "" {
		raw, e := s.evidence.ReadForExecution(ctx, job.OrgID, job.CheckpointID, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: "asset-sync", ReasonCode: "asset_sync_checkpoint", OperationID: job.ID})
		if e != nil {
			return fail("checkpoint_expired")
		}
		var checkpoint struct{ NextToken string }
		e = json.Unmarshal(raw, &checkpoint)
		clear(raw)
		if e != nil {
			return fail("checkpoint_invalid")
		}
		body["NextToken"] = checkpoint.NextToken
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, action, bodyJSON(body), s.now())
	defer clear(result.Body)
	defer clear(result.Data)
	if result.Outcome != "accepted" {
		return fail("provider_sync_failed")
	}
	var page struct {
		Items     []json.RawMessage
		NextToken string
	}
	if json.Unmarshal(result.Data, &page) != nil || page.Items == nil || len(page.Items) > 100 || len(page.NextToken) > 16384 {
		return fail("provider_response_invalid")
	}
	if job.Pages >= 10000 {
		return fail("sync_page_limit")
	}
	nextHash := hash([]byte(page.NextToken))
	if page.NextToken != "" && strings.Contains("|"+job.SeenHashes+"|", "|"+nextHash+"|") {
		return fail("pagination_cycle")
	}
	checkpointID := ""
	var checkpoint *invocation.PreparedVideoPayload
	if page.NextToken != "" {
		checkpoint, e = s.evidence.Prepare(invocation.VideoPayloadInput{OrgID: job.OrgID, RequestID: job.ID, AttemptID: job.ID + "_" + strings.TrimPrefix(nextHash[:12], ""), Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: bodyJSON(map[string]string{"NextToken": page.NextToken}), ExpiresAt: now.Add(24 * time.Hour), CaptureEnabled: true})
		if e != nil {
			return fail("checkpoint_unavailable")
		}
		checkpointID = checkpoint.PayloadID()
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockTenant(tx, job.OrgID); e != nil {
			return e
		}
		var current SyncJob
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND fence = ? AND status = ?", job.ID, job.Fence, "running").Take(&current).Error != nil {
			return nil
		}
		if checkpoint != nil {
			if _, e = s.evidence.StageTx(tx, checkpoint); e != nil {
				return ErrStorage
			}
		}
		count := int64(0)
		for _, raw := range page.Items {
			if job.Phase == "groups" {
				var item upstreamGroup
				if json.Unmarshal(raw, &item) != nil || item.ProjectName != job.Project || item.GroupType != groupType || !identifier.MatchString(item.ID) || !safeName(item.Name, 64, false) || !safeName(item.Description, 300, false) {
					return ErrInvalid
				}
				n, e := s.syncGroupTx(tx, job, item, kind)
				if e != nil {
					return e
				}
				count += n
			} else {
				var item upstreamAsset
				if json.Unmarshal(raw, &item) != nil || item.ProjectName != job.Project || item.GroupID != *group.ProviderID || !identifier.MatchString(item.ID) {
					return ErrInvalid
				}
				if e := s.syncAssetTx(tx, job, group, item); e != nil {
					return e
				}
				count++
			}
		}
		updates := map[string]any{"status": "pending", "pages": job.Pages + 1, "items": job.Items + count, "group_id": job.GroupID, "checkpoint_id": checkpointID, "lease_until": nil, "updated_at": nowMillis(s)}
		if page.NextToken != "" {
			updates["seen_hashes"] = strings.Trim(job.SeenHashes+"|"+nextHash, "|")
		} else {
			updates["seen_hashes"] = ""
			if job.Phase == "groups" {
				if job.Mode == "discover" {
					updates["status"] = "succeeded"
				} else {
					updates["phase"] = "assets"
				}
			} else {
				updates["after_group_id"] = job.GroupID
				updates["group_id"] = ""
			}
		}
		if tx.Model(&SyncJob{}).Where("id = ? AND fence = ?", job.ID, job.Fence).Updates(updates).Error != nil {
			return ErrStorage
		}
		return s.event(tx, job.OrgID, "sync", job.ID, "", "sync_page_committed", "", "worker")
	})
	if errors.Is(e, ErrInvalid) {
		return fail("provider_response_invalid")
	}
	return e
}

func (s *Service) syncGroupTx(tx *gorm.DB, job SyncJob, item upstreamGroup, kind string) (int64, error) {
	var g Group
	e := tx.Where("scope_id = ? AND provider_id = ?", job.ScopeID, item.ID).Take(&g).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		if job.Mode != "discover" {
			return 0, nil
		}
		id, e := newID("ad_")
		if e != nil {
			return 0, e
		}
		now := nowMillis(s)
		d := Discovery{ID: id, ScopeID: job.ScopeID, ScopeVersion: job.ScopeVersion, AccountID: job.AccountID, Region: job.Region, Project: job.Project, ProviderID: item.ID, Kind: kind, Name: item.Name, Description: item.Description, Status: "unassigned", Version: 1, CreatedAt: now, UpdatedAt: now}
		return 1, tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope_id"}, {Name: "provider_id"}}, DoNothing: true}).Create(&d).Error
	}
	if e != nil {
		return 0, ErrStorage
	}
	if g.OrgID != job.OrgID || g.DeletedAt != nil || g.Status != StatusActive {
		return 0, nil
	}
	var active int64
	if tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND action IN ? AND status IN ?", g.OrgID, g.ID, []string{"UpdateAssetGroup", "DeleteAssetGroup"}, []string{"pending", "in_flight", "unknown"}).Count(&active).Error != nil {
		return 0, ErrStorage
	}
	if active > 0 {
		return 0, nil
	}
	now := nowMillis(s)
	if tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", g.OrgID, g.ID).Updates(map[string]any{"name": item.Name, "updated_at": now, "row_version": gorm.Expr("row_version + 1")}).Error != nil {
		return 0, ErrStorage
	}
	d := Details{OrgID: g.OrgID, ResourceID: g.ID, Description: item.Description, Source: "platform", LastSyncedAt: &now, UpdatedAt: now}
	return 1, tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "resource_id"}}, DoUpdates: clause.Assignments(map[string]any{"description": item.Description, "last_synced_at": now, "updated_at": now})}).Create(&d).Error
}
func (s *Service) syncAssetTx(tx *gorm.DB, job SyncJob, g Group, item upstreamAsset) error {
	kind := strings.ToLower(item.AssetType)
	if kind != "image" && kind != "video" && kind != "audio" {
		return ErrInvalid
	}
	status := StatusProcessing
	switch item.Status {
	case "Active":
		status = StatusActive
	case "Processing":
	case "Failed":
		status = StatusFailed
	default:
		return ErrInvalid
	}
	var a Asset
	e := tx.Where("scope_id = ? AND provider_id = ?", job.ScopeID, item.ID).Take(&a).Error
	now := nowMillis(s)
	if errors.Is(e, gorm.ErrRecordNotFound) {
		id, e := newID("asset_")
		if e != nil {
			return e
		}
		a = Asset{ID: id, OrgID: g.OrgID, GroupID: g.ID, MediaType: kind, Name: item.Name, ScopeID: g.ScopeID, ScopeVersion: g.ScopeVersion, ProviderID: &item.ID, Status: status, ReviewState: ReviewRequired, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if tx.Create(&a).Error != nil {
			return ErrStorage
		}
		return saveDetails(tx, g.OrgID, id, "", "imported", now)
	}
	if e != nil {
		return ErrStorage
	}
	if a.OrgID != g.OrgID || a.GroupID != g.ID {
		return ErrConflict
	}
	if a.DeletedAt != nil || a.Status == StatusDeleting {
		return nil
	}
	var active int64
	if tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND action IN ? AND status IN ?", g.OrgID, a.ID, []string{"UpdateAsset", "DeleteAsset"}, []string{"pending", "in_flight", "unknown"}).Count(&active).Error != nil {
		return ErrStorage
	}
	if active > 0 {
		return nil
	}
	code := ""
	if status == StatusFailed {
		code = "asset_processing_failed"
	}
	return tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", g.OrgID, a.ID).Updates(map[string]any{"name": item.Name, "status": status, "safe_error_code": code, "updated_at": now, "row_version": gorm.Expr("row_version + 1")}).Error
}
