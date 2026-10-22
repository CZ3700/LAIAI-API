package videoasset

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func publicStatus(status string) string {
	switch status {
	case StatusPending:
		return "creating"
	case StatusActive:
		return "ready"
	case StatusProcessing:
		return "processing"
	case StatusFailed:
		return "failed"
	case StatusDeleting:
		return "deleting"
	}
	return "failed"
}
func (s *Service) GetGroup(ctx context.Context, org uint64, id string) (GroupDTO, error) {
	if org == 0 || !validID(id) {
		return GroupDTO{}, ErrInvalid
	}
	var row Group
	if err := owned(ctx, s.db.WithContext(ctx), org, "asset_group", "group_id").Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", org, id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GroupDTO{}, ErrNotFound
		}
		return GroupDTO{}, ErrStorage
	}
	dto := GroupDTO{ID: row.ID, Version: row.RowVersion, Kind: row.Kind, Name: row.Name, Status: publicStatus(row.Status), AuthorizationStatus: "not_required", CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.Status == StatusPending && row.ReviewState == ReviewRequired {
		dto.Status = "needs_review"
	}
	if row.CreateOperationID != "" {
		created, err := resourceOperation(s.db.WithContext(ctx), org, "asset_group", row.ID, "CreateAssetGroup", row.CreateOperationID, groupScope(row))
		if err != nil {
			return GroupDTO{}, err
		}
		if created != nil {
			value := operationDTO(*created)
			dto.CreateOperation = &value
			if created.Status == "unknown" {
				dto.Status = "needs_review"
			}
		}
	}
	if err := owned(ctx, s.db.WithContext(ctx).Model(&Asset{}), org, "asset", "asset_id").Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", org, id).Count(&dto.AssetCount).Error; err != nil {
		return GroupDTO{}, ErrStorage
	}
	if row.Kind == KindReal {
		dto.Kind = "real_person"
		dto.AuthorizationStatus = "needs_review"
		var p Portrait
		if err := s.db.WithContext(ctx).Where("org_id = ? AND session_id = ?", org, row.PortraitID).Take(&p).Error; err != nil {
			return GroupDTO{}, ErrStorage
		}
		dto.AuthorizationStatus = s.licenseStatus(p, "video_generation")
		dto.AuthorizationLastVerifiedAt = p.LastVerifiedAt
		dto.AuthorizationValidUntil = p.ValidUntil
	}
	mode, reason, latest, err := s.groupDeletionState(s.db.WithContext(ctx), row)
	if err != nil {
		return GroupDTO{}, err
	}
	dto.DeletionMode, dto.DeletionBlockReason = mode, reason
	if latest != nil {
		value := operationDTO(*latest)
		dto.DeleteOperation = &value
	}
	if mode == "official" {
		r, err := s.resolve(ctx, org, groupScope(row))
		switch {
		case err != nil:
			dto.DeletionMode, dto.DeletionBlockReason = "blocked", "scope_unavailable"
		case !r.Capabilities.DeleteVirtualEmptyGroups:
			dto.DeletionMode, dto.DeletionBlockReason = "blocked", "delete_capability_disabled"
		case !r.Capabilities.ExclusiveProjectWrites:
			dto.DeletionMode, dto.DeletionBlockReason = "blocked", "exclusive_project_required"
		}
	}
	return dto, nil
}
func (s *Service) GetAsset(ctx context.Context, org uint64, id string) (AssetDTO, error) {
	if org == 0 || !validID(id) {
		return AssetDTO{}, ErrInvalid
	}
	var row Asset
	if err := owned(ctx, s.db.WithContext(ctx), org, "asset", "asset_id").Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", org, id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AssetDTO{}, ErrNotFound
		}
		return AssetDTO{}, ErrStorage
	}
	dto := AssetDTO{ID: row.ID, Version: row.RowVersion, GroupID: row.GroupID, MediaType: row.MediaType, Name: row.Name, Status: publicStatus(row.Status), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	var group Group
	if err := s.db.WithContext(ctx).Where("org_id = ? AND group_id = ?", org, row.GroupID).Take(&group).Error; err != nil {
		return AssetDTO{}, ErrStorage
	}
	if row.CreateOperationID != "" && row.ScopeID == group.ScopeID && row.ScopeVersion == group.ScopeVersion {
		created, err := resourceOperation(s.db.WithContext(ctx), org, "asset", row.ID, "CreateAsset", row.CreateOperationID, groupScope(group))
		if err != nil {
			return AssetDTO{}, err
		}
		if created != nil {
			value := operationDTO(*created)
			dto.CreateOperation = &value
			if created.Status == "unknown" {
				dto.Status = "needs_review"
			}
		}
	}
	mode, reason, latest, err := s.assetDeletionState(s.db.WithContext(ctx), row, group)
	if err != nil {
		return AssetDTO{}, err
	}
	dto.DeletionMode, dto.DeletionBlockReason = mode, reason
	if latest != nil {
		value := operationDTO(*latest)
		dto.DeleteOperation = &value
	}
	if mode == "official" {
		r, err := s.resolve(ctx, org, groupScope(group))
		switch {
		case err != nil || !r.ExecutionRetentionConsent:
			dto.DeletionMode, dto.DeletionBlockReason = "blocked", "scope_unavailable"
		case !r.Capabilities.DeleteAssets:
			dto.DeletionMode, dto.DeletionBlockReason = "blocked", "delete_capability_disabled"
		}
	}
	if row.Status == StatusActive {
		usable := row.ReviewState == ReviewClear && referenceMediaReady(row)
		usable = usable && group.DeletedAt == nil && group.Status == StatusActive && group.ReviewState == ReviewClear && row.ScopeID == group.ScopeID && row.ScopeVersion == group.ScopeVersion
		if group.Kind == KindReal {
			var p Portrait
			if s.db.WithContext(ctx).Where("org_id = ? AND session_id = ?", org, group.PortraitID).Take(&p).Error != nil {
				usable = false
			} else {
				usable = usable && s.licenseStatus(p, "video_generation") == "authorized"
			}
		}
		if usable {
			dto.Reference = "asset://" + row.ID
		} else {
			dto.Status = "needs_review"
		}
	}
	return dto, nil
}
func (s *Service) GetOperation(ctx context.Context, org uint64, id string) (OperationDTO, error) {
	if org == 0 || !validID(id) {
		return OperationDTO{}, ErrInvalid
	}
	var row Operation
	if err := owned(ctx, s.db.WithContext(ctx), org, "operation", "operation_id").Where("org_id = ? AND operation_id = ? ", org, id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OperationDTO{}, ErrNotFound
		}
		return OperationDTO{}, ErrStorage
	}
	return operationDTO(row), nil
}
func operationDTO(row Operation) OperationDTO {
	dto := OperationDTO{ID: row.ID, RequestID: row.RequestID, Kind: row.Action, ResourceID: row.ResourceID, Status: row.Status, PollURL: "/v1/video/operations/" + row.ID, CreatedAt: row.CreatedAt}
	if row.Status == "confirmed" || row.Status == "rejected" {
		dto.CompletedAt = &row.UpdatedAt
	}
	if row.SafeErrorCode != "" {
		dto.Error = safeOperationError(row.SafeErrorCode)
	}
	return dto
}

type cursorData struct {
	Org                       uint64
	Kind, After, Version, MAC string
}

func (s *Service) cursor(org uint64, kind, after string) string {
	c := cursorData{Org: org, Kind: kind, After: after, Version: s.options.ActiveHMACKey}
	c.MAC = s.digest(c.Version, "cursor", org, kind, after)
	return base64.RawURLEncoding.EncodeToString(bodyJSON(c))
}
func (s *Service) parseCursor(org uint64, kind, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 1024 {
		return "", ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", ErrInvalid
	}
	var c cursorData
	if json.Unmarshal(decoded, &c) != nil || c.Org != org || c.Kind != kind || !validID(c.After) || len(s.options.HMACKeys[c.Version]) == 0 {
		return "", ErrInvalid
	}
	actual, err := hex.DecodeString(c.MAC)
	expected, _ := hex.DecodeString(s.digest(c.Version, "cursor", org, kind, c.After))
	if err != nil || !hmac.Equal(actual, expected) {
		return "", ErrInvalid
	}
	return c.After, nil
}
func (s *Service) ListGroups(ctx context.Context, org uint64, limit int, cursor string) ([]GroupDTO, string, error) {
	if org == 0 || limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	kind := actorCursor(ctx, "groups")
	after, err := s.parseCursor(org, kind, cursor)
	if err != nil {
		return nil, "", err
	}
	var rows []Group
	q := owned(ctx, s.db.WithContext(ctx), org, "asset_group", "group_id").Where("org_id = ? AND deleted_at IS NULL", org)
	if after != "" {
		q = q.Where("group_id > ?", after)
	}
	if err := q.Order("group_id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", ErrStorage
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = s.cursor(org, kind, rows[len(rows)-1].ID)
	}
	result := make([]GroupDTO, 0, len(rows))
	for _, row := range rows {
		dto, err := s.GetGroup(ctx, org, row.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		result = append(result, dto)
	}
	return result, next, nil
}
func (s *Service) ListAssets(ctx context.Context, org uint64, group string, limit int, cursor string) ([]AssetDTO, string, error) {
	if org == 0 || limit < 1 || limit > 100 || group != "" && !validID(group) {
		return nil, "", ErrInvalid
	}
	kind := actorCursor(ctx, "assets:"+group)
	after, err := s.parseCursor(org, kind, cursor)
	if err != nil {
		return nil, "", err
	}
	var rows []Asset
	q := owned(ctx, s.db.WithContext(ctx), org, "asset", "asset_id").Where("org_id = ? AND deleted_at IS NULL", org)
	if group != "" {
		if err := resourceVisible(ctx, s.db, org, "asset_group", group); err != nil {
			return nil, "", err
		}
		q = q.Where("group_id = ?", group)
	}
	if after != "" {
		q = q.Where("asset_id > ?", after)
	}
	if err := q.Order("asset_id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", ErrStorage
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = s.cursor(org, kind, rows[len(rows)-1].ID)
	}
	result := make([]AssetDTO, 0, len(rows))
	for _, row := range rows {
		dto, err := s.GetAsset(ctx, org, row.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		result = append(result, dto)
	}
	return result, next, nil
}

func (s *Service) DeleteGroup(ctx context.Context, c CreateContext, id string, cascade bool) (OperationDTO, bool, error) {
	ctx = WithCaller(ctx, c)
	if cascade {
		return OperationDTO{}, false, ErrUnsupported
	}
	if c.IdempotencyKey == "" {
		c.IdempotencyKey = "delete:" + id + ":" + c.RequestID
	}
	if !validID(id) || !validCreate(c) {
		return OperationDTO{}, false, ErrInvalid
	}
	var group Group
	if err := owned(ctx, s.db.WithContext(ctx), c.OrgID, "asset_group", "group_id").Where("org_id = ? AND group_id = ?", c.OrgID, id).Take(&group).Error; err != nil {
		return OperationDTO{}, false, ErrNotFound
	}
	if group.DeletedAt != nil {
		return OperationDTO{}, true, nil
	}
	mode, reason, latest, err := s.groupDeletionState(s.db.WithContext(ctx), group)
	if err != nil {
		return OperationDTO{}, false, err
	}
	if latest != nil && (latest.Status == "pending" || latest.Status == "in_flight" || latest.Status == "unknown") {
		return operationDTO(*latest), false, nil
	}
	if mode == "local_failed" {
		err := s.deleteFailedGroup(ctx, c, id)
		return OperationDTO{}, err == nil, err
	}
	if mode != "official" {
		return OperationDTO{}, false, deletionBlockError(reason)
	}
	r, err := s.resolve(ctx, c.OrgID, groupScope(group))
	if err != nil {
		return OperationDTO{}, false, err
	}
	if !r.Capabilities.DeleteVirtualEmptyGroups || !r.Capabilities.ExclusiveProjectWrites || group.Kind != KindVirtual {
		return OperationDTO{}, false, ErrUnsupported
	}
	if group.ProviderID == nil {
		return OperationDTO{}, false, ErrConflict
	}
	op, _, err := s.accept(ctx, c, r, "/v1/video/asset-groups/"+id, "DeleteAssetGroup", "asset_group", id, bodyJSON(map[string]any{"id": id, "cascade": false}), bodyJSON(map[string]any{"Id": *group.ProviderID, "ProjectName": r.Scope.Project}), func(tx *gorm.DB, op Operation) error {
		var current Group
		if err := owned(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), c.OrgID, "asset_group", "group_id").Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", c.OrgID, id).Take(&current).Error; err != nil {
			return ErrNotFound
		}
		if !sameScope(groupScope(current), r.Scope) || current.ProviderID == nil || *current.ProviderID != *group.ProviderID {
			return ErrConflict
		}
		mode, reason, _, err := s.groupDeletionState(tx, current)
		if err != nil {
			return err
		}
		if mode != "official" {
			return deletionBlockError(reason)
		}
		if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", c.OrgID, id).Updates(map[string]any{"status": StatusDeleting, "delete_operation_id": op.ID, "row_version": gorm.Expr("row_version + 1"), "updated_at": nowMillis(s)}).Error; err != nil {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return OperationDTO{}, false, err
	}
	return operationDTO(op), false, nil
}

func (s *Service) licenseStatus(p Portrait, purpose string) string {
	now := s.now()
	if p.LicenseStatus == "revoked" || p.RevokedAt != nil {
		return "revoked"
	}
	if p.LicenseStatus == "expired" || p.ValidUntil != nil && !p.ValidUntil.After(now) {
		return "expired"
	}
	if (p.SessionStatus != "succeeded" && p.SessionStatus != "imported_verified") || p.LicenseStatus != "verified" || p.ValidFrom == nil || p.ValidUntil == nil || p.ValidFrom.After(now) || !p.ValidUntil.After(*p.ValidFrom) || p.LicenseScope != purpose || p.LicenseEvidenceRef == "" || p.LastVerifiedAt == nil || p.LastVerifiedAt.After(now) || now.Sub(*p.LastVerifiedAt) > s.options.MaxLicenseAge || p.ReverifyDueAt == nil || !p.ReverifyDueAt.After(now) {
		return "needs_review"
	}
	return "authorized"
}
func (s *Service) authorizeGroup(tx *gorm.DB, g Group, purpose string) (uint64, error) {
	if g.Kind == KindVirtual {
		return 0, nil
	}
	if g.Kind != KindReal || g.PortraitID == "" {
		return 0, ErrAuthorization
	}
	var p Portrait
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", g.OrgID, g.PortraitID).Take(&p).Error; err != nil {
		return 0, ErrAuthorization
	}
	if !sameScope(portraitScope(p), groupScope(g)) || p.GroupID == nil || *p.GroupID != g.ID || s.licenseStatus(p, purpose) != "authorized" {
		return 0, ErrAuthorization
	}
	return p.LicenseVersion, nil
}

// LicenseEvidence is supplied only by trusted reviewed backoffice workflows.
// There is deliberately no customer HTTP mutation endpoint for this object.
type LicenseEvidence struct {
	ActorID, EvidenceRef, Source, Purpose            string
	ValidFrom, ValidUntil, VerifiedAt, ReverifyDueAt time.Time
}

func (s *Service) VerifyLicenseTx(tx *gorm.DB, org uint64, sessionID string, e LicenseEvidence) error {
	if tx == nil {
		return ErrInvalid
	}
	return tx.Transaction(func(inner *gorm.DB) error { return s.verifyLicenseTx(inner, org, sessionID, e) })
}
func (s *Service) verifyLicenseTx(tx *gorm.DB, org uint64, sessionID string, e LicenseEvidence) error {
	now := s.now()
	if tx == nil || org == 0 || !validID(sessionID) || !validID(e.ActorID) || !validID(e.EvidenceRef) || e.Purpose != "video_generation" ||
		(e.Source != "reviewed_contract" && e.Source != "reviewed_console" && e.Source != "verified_provider_license") || e.ValidFrom.After(now) || !e.ValidUntil.After(now) || !e.ValidUntil.After(e.ValidFrom) || e.VerifiedAt.After(now) || now.Sub(e.VerifiedAt) > s.options.MaxLicenseAge || !e.ReverifyDueAt.After(now) || e.ReverifyDueAt.After(e.VerifiedAt.Add(s.options.MaxLicenseAge)) {
		return ErrInvalid
	}
	if err := lockTenant(tx, org); err != nil {
		return err
	}
	var p Portrait
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", org, sessionID).Take(&p).Error; err != nil {
		return ErrNotFound
	}
	if (p.SessionStatus != "succeeded" && p.SessionStatus != "imported_verified" && p.SessionStatus != "imported_pending") || p.GroupID == nil || p.RevokedAt != nil {
		return ErrAuthorization
	}
	state := p.SessionStatus
	if state == "imported_pending" {
		state = "imported_verified"
	}
	if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", org, sessionID).Updates(map[string]any{"session_status": state, "license_status": "verified", "valid_from": e.ValidFrom, "valid_until": e.ValidUntil, "license_scope": e.Purpose, "license_version": gorm.Expr("license_version + 1"), "license_evidence_ref": e.EvidenceRef, "license_source": e.Source, "last_verified_at": e.VerifiedAt, "reverify_due_at": e.ReverifyDueAt, "updated_at": nowMillis(s)}).Error; err != nil {
		return ErrStorage
	}
	return s.event(tx, org, "portrait", sessionID, "", "license_verified", e.EvidenceRef, e.ActorID)
}
func (s *Service) RevokeLicenseTx(tx *gorm.DB, org uint64, sessionID, evidenceRef, actorID string) error {
	if tx == nil {
		return ErrInvalid
	}
	return tx.Transaction(func(inner *gorm.DB) error { return s.revokeLicenseTx(inner, org, sessionID, evidenceRef, actorID) })
}
func (s *Service) revokeLicenseTx(tx *gorm.DB, org uint64, sessionID, evidenceRef, actorID string) error {
	if tx == nil || org == 0 || !validID(sessionID) || !validID(evidenceRef) || !validID(actorID) {
		return ErrInvalid
	}
	if err := lockTenant(tx, org); err != nil {
		return err
	}
	res := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND license_status <> ?", org, sessionID, "revoked").Updates(map[string]any{"license_status": "revoked", "revoked_at": nowMillis(s), "license_version": gorm.Expr("license_version + 1"), "updated_at": nowMillis(s)})
	if res.Error != nil {
		return ErrStorage
	}
	if res.RowsAffected == 0 {
		var p Portrait
		if err := tx.Where("org_id = ? AND session_id = ?", org, sessionID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		return nil
	}
	return s.event(tx, org, "portrait", sessionID, "", "license_revoked", evidenceRef, actorID)
}
