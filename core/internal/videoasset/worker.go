package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"strings"
	"time"
)

// ProcessOnce may run concurrently on multiple servers. Claims are fenced;
// expired mutating claims become unknown and are never automatically resent.
// Query intents may be recreated later because they do not create resources.
func (s *Service) ProcessOnce(ctx context.Context) error {
	if err := s.expireSecrets(ctx); err != nil {
		return err
	}
	var stale []Operation
	if err := s.db.WithContext(ctx).Where("status = ? AND lease_expires_at <= ?", "in_flight", s.now()).Order("created_at").Limit(100).Find(&stale).Error; err != nil {
		return ErrStorage
	}
	for _, op := range stale {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockTenant(tx, op.OrgID); err != nil {
				return err
			}
			res := tx.Model(&Operation{}).Where("operation_id = ? AND status = ? AND fence = ? AND lease_expires_at <= ?", op.ID, "in_flight", op.Fence, s.now()).Updates(map[string]any{"status": "unknown", "safe_error_code": "submission_uncertain", "updated_at": nowMillis(s)})
			if res.Error != nil {
				return ErrStorage
			}
			if res.RowsAffected == 0 {
				return nil
			}
			if err := s.markUnknown(tx, op); err != nil {
				return err
			}
			return s.event(tx, op.OrgID, op.ResourceType, op.ResourceID, op.ID, "lease_expired", "", "worker")
		}); err != nil {
			return err
		}
	}
	if err := s.enqueueQueries(ctx); err != nil {
		return err
	}
	var pending []Operation
	if err := s.db.WithContext(ctx).Where("status = ? AND next_attempt_at <= ?", "pending", s.now()).Order("created_at, operation_id").Limit(16).Find(&pending).Error; err != nil {
		return ErrStorage
	}
	for _, candidate := range pending {
		if err := s.process(ctx, candidate); err != nil {
			return err
		}
	}
	if s.consoleEnabled() {
		if err := s.syncPortraitNames(ctx); err != nil {
			return err
		}
		if err := s.processSyncJobs(ctx); err != nil {
			return err
		}
		if err := s.cleanupMedia(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) process(ctx context.Context, op Operation) error {
	worker, err := newID("aw_")
	if err != nil {
		return err
	}
	claimed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, op.OrgID); err != nil {
			return err
		}
		lease := nowMillis(s).Add(2 * time.Minute)
		result := tx.Model(&Operation{}).Where("operation_id = ? AND org_id = ? AND status = ? AND fence = ?", op.ID, op.OrgID, "pending", op.Fence).
			Updates(map[string]any{"status": "in_flight", "lease_owner": worker, "lease_expires_at": lease, "fence": op.Fence + 1, "updated_at": nowMillis(s)})
		if result.Error != nil {
			return ErrStorage
		}
		claimed = result.RowsAffected == 1
		if !claimed {
			return nil
		}
		op.Fence++
		op.LeaseOwner = worker
		op.LeaseExpiresAt = &lease
		op.UpdatedAt = nowMillis(s)
		op.Status = "in_flight"
		return s.event(tx, op.OrgID, op.ResourceType, op.ResourceID, op.ID, "sending", op.PayloadID, worker)
	})
	if err != nil || !claimed {
		return err
	}
	runtime, err := s.resolve(ctx, op.OrgID, opScope(op))
	if err != nil {
		return s.finish(ctx, op, RuntimeScope{}, Result{Outcome: "not_sent", SafeErrorCode: "unsupported_capability"}, nil)
	}
	if err := s.revalidateDispatch(ctx, op, runtime); err != nil {
		return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "dispatch_policy_rejected"}, nil)
	}
	body, err := s.evidence.ReadForExecution(ctx, op.OrgID, op.PayloadID, invocation.VideoAccess{ActorRole: invocation.VideoRoleWorker, ActorID: worker, ReasonCode: "asset_dispatch", OperationID: op.ID})
	if err != nil {
		return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "execution_payload_unavailable"}, nil)
	}
	defer clear(body)
	if !operationAllowed(runtime.Capabilities, op.Action) {
		return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "unsupported_capability"}, body)
	}
	if op.Action == "CreateVisualValidateSession" {
		callback, err := s.callbackURL(ctx, op)
		if err != nil {
			return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "callback_unavailable"}, body)
		}
		clear(body)
		body = bodyJSON(map[string]any{"CallbackURL": callback, "ProjectName": runtime.Scope.Project})
		defer clear(body)
	}
	if op.Action == "GetVisualValidateResult" {
		var p Portrait
		if err := s.db.WithContext(ctx).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Take(&p).Error; err != nil {
			return ErrStorage
		}
		secret, err := s.readSecret(ctx, p, worker, 0, true)
		if err != nil {
			return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "portrait_session_expired"}, body)
		}
		clear(body)
		body = bodyJSON(map[string]any{"BytedToken": secret.BytedToken, "ProjectName": runtime.Scope.Project})
		defer clear(body)
	}
	// Ensure no expired/stolen claim crosses the paid-submit boundary.
	var count int64
	if err := s.db.WithContext(ctx).Model(&Operation{}).Where("operation_id = ? AND status = ? AND fence = ? AND lease_owner = ? AND lease_expires_at > ?", op.ID, "in_flight", op.Fence, worker, s.now()).Count(&count).Error; err != nil {
		return ErrStorage
	}
	if count != 1 {
		return nil
	}
	if op.Action == "DeleteAssetGroup" {
		check := s.options.Client.Call(ctx, runtime.Scope, runtime.Credentials, "ListAssets", bodyJSON(map[string]any{"Filter": map[string]any{"GroupType": "AIGC", "GroupIds": []string{extractProviderID(body)}}, "MaxResults": 1, "ProjectName": runtime.Scope.Project}), s.now())
		var page struct {
			Items     []json.RawMessage
			NextToken string
		}
		valid := check.Outcome == "accepted" && json.Unmarshal(check.Data, &page) == nil && page.Items != nil && len(page.Items) == 0 && page.NextToken == ""
		clear(check.Body)
		clear(check.Data)
		if !valid {
			return s.finish(ctx, op, runtime, Result{Outcome: "not_sent", SafeErrorCode: "group_not_empty_or_unverified"}, body)
		}
	}
	result := s.options.Client.Call(ctx, runtime.Scope, runtime.Credentials, op.Action, body, s.now())
	defer clear(result.Body)
	defer clear(result.Data)
	return s.finish(ctx, op, runtime, result, body)
}
func operationAllowed(c Capabilities, action string) bool {
	switch action {
	case "UpdateAssetGroup", "UpdateAsset":
		return c.Update
	case "DeleteAsset":
		return c.DeleteAssets
	case "ListAssetGroups", "ListAssets":
		return c.Sync
	case "CreateAssetGroup":
		return c.VirtualGroups
	case "DeleteAssetGroup":
		return c.DeleteVirtualEmptyGroups && c.ExclusiveProjectWrites
	case "CreateAsset", "GetAsset":
		return c.Images || c.Videos || c.Audio
	case "CreateVisualValidateSession", "GetVisualValidateResult":
		return c.Portrait
	case "GetAssetGroup":
		return c.VirtualGroups || c.Portrait
	}
	return false
}

// Recheck capability and portrait policy when claiming paid work, not only at
// acceptance. The transaction ends before the network call; the sending intent
// is already durable, so this is a bounded dispatch authorization point.
func (s *Service) revalidateDispatch(ctx context.Context, op Operation, r RuntimeScope) error {
	if !r.ExecutionRetentionConsent || !operationAllowed(r.Capabilities, op.Action) {
		return ErrUnsupported
	}
	if op.Action != "CreateAsset" {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, op.OrgID); err != nil {
			return err
		}
		var asset Asset
		if err := tx.Where("org_id = ? AND asset_id = ? AND create_operation_id = ?", op.OrgID, op.ResourceID, op.ID).Take(&asset).Error; err != nil {
			return ErrNotFound
		}
		var group Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id = ?", op.OrgID, asset.GroupID).Take(&group).Error; err != nil {
			return ErrNotFound
		}
		if asset.Status != StatusPending || asset.DeletedAt != nil || asset.ReviewState != ReviewClear || group.Status != StatusActive || group.DeletedAt != nil || group.ReviewState != ReviewClear || !sameScope(groupScope(group), opScope(op)) || !mediaAllowed(r.Capabilities, asset.MediaType) {
			return ErrConflict
		}
		if _, err := s.authorizeGroup(tx, group, "video_generation"); err != nil {
			return err
		}
		return s.event(tx, op.OrgID, "asset", asset.ID, op.ID, "dispatch_authorized", "", op.LeaseOwner)
	})
}

type safeReceipt struct{ ID, GroupID, ProjectName, AssetType, Status string }

func (s *Service) finish(ctx context.Context, op Operation, r RuntimeScope, result Result, request []byte) error {
	var prepared *invocation.PreparedVideoPayload
	if r.CaptureHistory && len(result.Body) > 0 {
		var err error
		secrets := []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken}
		if op.Action == "CreateVisualValidateSession" {
			var callback struct{ CallbackURL string }
			if json.Unmarshal(request, &callback) == nil && callback.CallbackURL != "" {
				secrets = append(secrets, callback.CallbackURL)
				if parsed, err := url.Parse(callback.CallbackURL); err == nil {
					secrets = append(secrets, parsed.Query().Get("state"))
				}
			}
		}
		prepared, err = s.evidence.Prepare(invocation.VideoPayloadInput{OrgID: op.OrgID, RequestID: op.RequestID, AttemptID: op.ID, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: result.Body, ExpiresAt: nowMillis(s).Add(30 * 24 * time.Hour), CaptureEnabled: true, SecretValues: secrets})
		// HTTP facts remain authoritative even if optional body capture cannot be
		// prepared. The safe event records capture absence, never raw parser errors.
		if err != nil {
			prepared = nil
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, op.OrgID); err != nil {
			return err
		}
		var current Operation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ? AND org_id = ?", op.ID, op.OrgID).Take(&current).Error; err != nil {
			return ErrStorage
		}
		if current.Fence != op.Fence || current.LeaseOwner != op.LeaseOwner || (current.Status != "in_flight" && current.Status != "unknown") {
			return nil
		}
		status := "unknown"
		code := result.SafeErrorCode
		if prepared != nil {
			if _, err := s.evidence.StageTx(tx, prepared); err != nil {
				return ErrStorage
			}
			op.ResponsePayloadID = prepared.PayloadID()
		}
		if r.CaptureHistory && len(result.Body) > 0 && prepared == nil {
			if err := s.event(tx, op.OrgID, op.ResourceType, op.ResourceID, op.ID, "response_capture_unavailable", "", op.LeaseOwner); err != nil {
				return err
			}
		}
		if result.Outcome == "accepted" {
			// A savepoint prevents a partially applied resource from escaping when a
			// malformed official response is conservatively classified as unknown.
			applyErr := tx.Transaction(func(inner *gorm.DB) error { return s.applyAccepted(inner, op, result, request) })
			if applyErr == nil {
				status = "confirmed"
				code = ""
			} else if errors.Is(applyErr, ErrStorage) {
				return applyErr
			} else {
				code = "provider_response_invalid"
			}
		} else if result.Outcome == "rejected" || result.Outcome == "not_sent" {
			status = "rejected"
			if err := s.markRejected(tx, op); err != nil {
				return err
			}
		}
		if status == "unknown" {
			if err := s.markUnknown(tx, op); err != nil {
				return err
			}
		}
		receipt := ""
		if op.Action != "CreateVisualValidateSession" && len(result.Data) > 0 {
			var value safeReceipt
			if json.Unmarshal(result.Data, &value) == nil {
				if value.ID != "" && !identifier.MatchString(value.ID) {
					value.ID = ""
				}
				if value.GroupID != "" && !identifier.MatchString(value.GroupID) {
					value.GroupID = ""
				}
				if value.ProjectName != "" && !validID(value.ProjectName) {
					value.ProjectName = ""
				}
				if value.AssetType != "Image" && value.AssetType != "Video" && value.AssetType != "Audio" {
					value.AssetType = ""
				}
				if value.Status != "Processing" && value.Status != "Active" && value.Status != "Failed" {
					value.Status = ""
				}
				receipt = string(bodyJSON(value))
			}
		}
		if status == "unknown" && op.Action == "DeleteAsset" {
			receipt = op.Receipt
		}
		updates := map[string]any{"status": status, "http_status": result.HTTPStatus, "upstream_request_id": result.RequestID, "safe_error_code": code, "response_payload_id": op.ResponsePayloadID, "receipt": receipt, "updated_at": nowMillis(s), "lease_expires_at": nil}
		if err := tx.Model(&Operation{}).Where("operation_id = ? AND fence = ?", op.ID, op.Fence).Updates(updates).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, op.OrgID, op.ResourceType, op.ResourceID, op.ID, "response_"+status, op.ResponsePayloadID, op.LeaseOwner)
	})
}

func (s *Service) markUnknown(tx *gorm.DB, op Operation) error {
	updates := map[string]any{"review_state": ReviewRequired, "updated_at": nowMillis(s)}
	switch op.Action {
	case "CreateAssetGroup", "DeleteAssetGroup", "UpdateAssetGroup":
		if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", op.OrgID, op.ResourceID).Updates(updates).Error; err != nil {
			return ErrStorage
		}
	case "CreateAsset", "DeleteAsset", "UpdateAsset":
		if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", op.OrgID, op.ResourceID).Updates(updates).Error; err != nil {
			return ErrStorage
		}
	case "CreateVisualValidateSession":
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND session_status = ?", op.OrgID, op.ResourceID, "creating").Updates(map[string]any{"session_status": "submission_unknown", "updated_at": nowMillis(s)}).Error; err != nil {
			return ErrStorage
		}
	case "GetAsset":
		if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", op.OrgID, op.ResourceID).Update("updated_at", nowMillis(s)).Error; err != nil {
			return ErrStorage
		}
	case "GetVisualValidateResult":
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Update("updated_at", nowMillis(s)).Error; err != nil {
			return ErrStorage
		}
	}
	return nil
}
func (s *Service) markRejected(tx *gorm.DB, op Operation) error {
	updates := map[string]any{"status": StatusFailed, "updated_at": nowMillis(s)}
	switch op.Action {
	case "CreateAssetGroup":
		if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", op.OrgID, op.ResourceID).Updates(updates).Error; err != nil {
			return ErrStorage
		}
	case "CreateAsset":
		if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", op.OrgID, op.ResourceID).Updates(updates).Error; err != nil {
			return ErrStorage
		}
	case "CreateVisualValidateSession":
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Updates(map[string]any{"session_status": "failed", "updated_at": nowMillis(s)}).Error; err != nil {
			return ErrStorage
		}
	case "DeleteAssetGroup":
		if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ?", op.OrgID, op.ResourceID).Updates(map[string]any{"status": StatusActive, "delete_operation_id": "", "review_state": ReviewClear, "updated_at": nowMillis(s)}).Error; err != nil {
			return ErrStorage
		}
	case "DeleteAsset":
		var prior struct {
			Status string `json:"prior_status"`
			Review string `json:"prior_review_state"`
		}
		_ = json.Unmarshal([]byte(op.Receipt), &prior)
		if prior.Status != StatusActive && prior.Status != StatusFailed {
			prior.Status = StatusFailed
		}
		if prior.Review != ReviewClear {
			prior.Review = ReviewRequired
		}
		if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", op.OrgID, op.ResourceID).Updates(map[string]any{"status": prior.Status, "review_state": prior.Review, "updated_at": nowMillis(s)}).Error; err != nil {
			return ErrStorage
		}
	case "GetAsset", "GetVisualValidateResult":
		return s.markUnknown(tx, op)
	}
	return nil
}

func (s *Service) applyAccepted(tx *gorm.DB, op Operation, result Result, request []byte) error {
	now := nowMillis(s)
	switch op.Action {
	case "CreateAssetGroup", "CreateAsset":
		var data struct{ ID string }
		if json.Unmarshal(result.Data, &data) != nil || !identifier.MatchString(data.ID) {
			return ErrInvalid
		}
		if op.Action == "CreateAssetGroup" {
			if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ? AND create_operation_id = ?", op.OrgID, op.ResourceID, op.ID).Updates(map[string]any{"provider_id": data.ID, "status": StatusActive, "review_state": ReviewClear, "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
				return ErrStorage
			}
		} else {
			if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ? AND create_operation_id = ?", op.OrgID, op.ResourceID, op.ID).Updates(map[string]any{"provider_id": data.ID, "status": StatusProcessing, "review_state": ReviewClear, "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
				return ErrStorage
			}
		}
		return nil
	case "GetAsset":
		var data safeReceipt
		if json.Unmarshal(result.Data, &data) != nil || data.ProjectName != op.Project {
			return ErrInvalid
		}
		var asset Asset
		if err := tx.Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", op.OrgID, op.ResourceID).Take(&asset).Error; err != nil {
			return ErrNotFound
		}
		var group Group
		if err := tx.Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", op.OrgID, asset.GroupID).Take(&group).Error; err != nil {
			return ErrNotFound
		}
		if asset.ProviderID == nil || *asset.ProviderID != data.ID || group.ProviderID == nil || *group.ProviderID != data.GroupID || strings.ToLower(data.AssetType) != asset.MediaType || !sameScope(groupScope(group), opScope(op)) || group.Status != StatusActive {
			return ErrInvalid
		}
		status := ""
		switch data.Status {
		case "Processing":
			status = StatusProcessing
		case "Active":
			status = StatusActive
		case "Failed":
			status = StatusFailed
		default:
			return ErrInvalid
		}
		if asset.Status == StatusActive && status == StatusProcessing || asset.Status == StatusFailed && status != StatusFailed {
			return ErrConflict
		}
		code := ""
		if status == StatusFailed {
			code = "asset_processing_failed"
		}
		if err := tx.Model(&Asset{}).Where("org_id = ? AND asset_id = ?", op.OrgID, op.ResourceID).Updates(map[string]any{"status": status, "review_state": ReviewClear, "safe_error_code": code, "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
			return ErrStorage
		}
		return nil
	case "DeleteAssetGroup":
		var empty map[string]json.RawMessage
		if json.Unmarshal(result.Data, &empty) != nil || empty == nil {
			return ErrInvalid
		}
		if err := tx.Model(&Group{}).Where("org_id = ? AND group_id = ? AND status = ?", op.OrgID, op.ResourceID, StatusDeleting).Updates(map[string]any{"status": StatusDeleted, "review_state": ReviewClear, "deleted_at": now, "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
			return ErrStorage
		}
		return nil
	case "CreateVisualValidateSession":
		return s.applyPortraitCreated(tx, op, result, request)
	case "GetVisualValidateResult":
		var data struct{ GroupID string }
		if json.Unmarshal(result.Data, &data) != nil || !identifier.MatchString(data.GroupID) {
			return ErrInvalid
		}
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if p.SessionStatus == "succeeded" {
			if p.GroupID == nil {
				return ErrConflict
			}
			var existing Group
			if err := tx.Where("org_id = ? AND group_id = ?", op.OrgID, *p.GroupID).Take(&existing).Error; err != nil {
				return ErrStorage
			}
			if existing.ProviderID == nil || *existing.ProviderID != data.GroupID {
				return ErrConflict
			}
			return nil
		}
		if p.SessionStatus != "pending" || p.SessionExpiresAt == nil || !p.SessionExpiresAt.After(now) || !sameScope(portraitScope(p), opScope(op)) {
			return ErrConflict
		}
		id, err := newID("ag_")
		if err != nil {
			return err
		}
		label := PortraitLabel{}
		_ = tx.Where("org_id = ? AND session_id = ?", op.OrgID, p.ID).Take(&label).Error
		group := Group{ID: id, OrgID: op.OrgID, Kind: KindReal, Name: label.Name, ScopeID: p.ScopeID, ScopeVersion: p.ScopeVersion, AccountID: p.AccountID, Region: p.Region, Project: p.Project, ProviderID: &data.GroupID, Status: StatusActive, ReviewState: ReviewClear, PortraitID: p.ID, CreateOperationID: op.ID, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&group).Error; err != nil {
			return ErrStorage
		}
		if err := saveDetails(tx, op.OrgID, id, label.Description, "platform", now); err != nil {
			return err
		}
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND session_status = ?", op.OrgID, p.ID, "pending").Updates(map[string]any{"session_status": "succeeded", "group_id": id, "license_status": "unknown", "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
			return ErrStorage
		}
		if err := tx.Model(&Secret{}).Where("org_id = ? AND secret_id = ?", op.OrgID, p.SecretRef).Updates(map[string]any{"value_cipher": "", "destroyed_at": now}).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, op.OrgID, "portrait", p.ID, op.ID, "official_identity_verified", op.ResponsePayloadID, op.LeaseOwner)
	}
	return s.applyConsoleAccepted(tx, op, result, request)
}

func (s *Service) enqueueQueries(ctx context.Context) error {
	var assets []Asset
	if err := s.db.WithContext(ctx).Where("status = ? AND updated_at <= ? AND deleted_at IS NULL", StatusProcessing, s.now().Add(-15*time.Second)).Order("updated_at").Limit(20).Find(&assets).Error; err != nil {
		return ErrStorage
	}
	for _, asset := range assets {
		if asset.ProviderID == nil {
			continue
		}
		var g Group
		if err := s.db.WithContext(ctx).Where("org_id = ? AND group_id = ?", asset.OrgID, asset.GroupID).Take(&g).Error; err != nil {
			return ErrStorage
		}
		if err := s.enqueueQuery(ctx, asset.OrgID, groupScope(g), "asset", asset.ID, "GetAsset", bodyJSON(map[string]any{"Id": *asset.ProviderID, "ProjectName": g.Project})); err != nil {
			return err
		}
	}
	var portraits []Portrait
	if err := s.db.WithContext(ctx).Where("session_status = ? AND session_expires_at > ? AND updated_at <= ?", "pending", s.now(), s.now().Add(-15*time.Second)).Order("updated_at").Limit(20).Find(&portraits).Error; err != nil {
		return ErrStorage
	}
	for _, p := range portraits {
		if err := s.enqueueQuery(ctx, p.OrgID, portraitScope(p), "portrait", p.ID, "GetVisualValidateResult", bodyJSON(map[string]any{"ProjectName": p.Project})); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) enqueueQuery(ctx context.Context, org uint64, scope Scope, resourceType, resourceID, action string, body []byte) error {
	runtime, err := s.resolve(ctx, org, scope)
	if err != nil || !runtime.ExecutionRetentionConsent {
		return nil
	}
	id, err := newID("op_")
	if err != nil {
		return err
	}
	request, err := newID("ar_")
	if err != nil {
		return err
	}
	now := nowMillis(s)
	prepared, err := s.evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: request, AttemptID: id, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: body, ExpiresAt: now.Add(time.Hour), CaptureEnabled: true})
	if err != nil {
		return ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, org); err != nil {
			return err
		}
		states := []string{"pending", "in_flight"}
		if strings.HasPrefix(action, "Update") {
			states = append(states, "unknown", "rejected", "confirmed")
		}
		var count int64
		if err := tx.Model(&Operation{}).Where("org_id = ? AND resource_id = ? AND action = ? AND status IN ?", org, resourceID, action, states).Count(&count).Error; err != nil {
			return ErrStorage
		}
		if count > 0 {
			return nil
		}
		if _, err := s.evidence.StageTx(tx, prepared); err != nil {
			return ErrStorage
		}
		row := Operation{ID: id, OrgID: org, Action: action, ResourceType: resourceType, ResourceID: resourceID, RequestID: request, CanonicalPath: "internal:" + action + ":" + resourceID, HMACKeyVersion: s.options.ActiveHMACKey, IdempotencyHash: s.digest(s.options.ActiveHMACKey, "internal", org, action, id), BodyHash: s.digest(s.options.ActiveHMACKey, "body", org, action, string(body)), ScopeID: scope.ID, ScopeVersion: scope.Version, AccountID: scope.AccountID, Region: scope.Region, Project: scope.Project, Status: "pending", PayloadID: prepared.PayloadID(), CreatedAt: now, UpdatedAt: now, NextAttemptAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, org, resourceType, resourceID, id, "query_intent", prepared.PayloadID(), "worker")
	})
}
