package videoasset

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This projection is also checked again under the tenant lock before mutation.
// An old rejected deletion remains visible without preventing a new explicit try.
func (s *Service) groupDeletionState(tx *gorm.DB, group Group) (string, string, *Operation, error) {
	var latest *Operation
	var row Operation
	q := tx.Where("org_id = ? AND resource_type = ? AND resource_id = ? AND action = ? AND scope_id = ? AND scope_version = ? AND account_id = ? AND region = ? AND project = ?",
		group.OrgID, "asset_group", group.ID, "DeleteAssetGroup", group.ScopeID, group.ScopeVersion, group.AccountID, group.Region, group.Project)
	if group.DeleteOperationID != "" {
		q = q.Where("operation_id = ?", group.DeleteOperationID)
	}
	if err := q.Order("created_at DESC, operation_id DESC").Take(&row).Error; err == nil {
		latest = &row
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", nil, ErrStorage
	}
	block := func(reason string) (string, string, *Operation, error) { return "blocked", reason, latest, nil }
	if group.Kind != KindVirtual {
		return block("real_group_not_supported")
	}
	if group.ReviewState != ReviewClear {
		return block("submission_uncertain")
	}
	var pending []Operation
	if err := tx.Where("org_id = ? AND resource_type = ? AND resource_id = ? AND status IN ?", group.OrgID, "asset_group", group.ID,
		[]string{"pending", "in_flight", "unknown"}).Find(&pending).Error; err != nil {
		return "", "", nil, ErrStorage
	}
	for _, op := range pending {
		if op.Status == "unknown" {
			return block("submission_uncertain")
		}
	}
	if len(pending) > 0 {
		return block("operation_pending")
	}
	var count int64
	// Failed creations cannot legitimately own even historical assets. Official
	// groups may retain soft-deleted assets, whose audit history must survive.
	assets := tx.Model(&Asset{}).Where("org_id = ? AND group_id = ?", group.OrgID, group.ID)
	if group.ProviderID != nil {
		assets = assets.Where("deleted_at IS NULL")
	}
	if err := assets.Count(&count).Error; err != nil {
		return "", "", nil, ErrStorage
	}
	if count > 0 {
		return block("group_not_empty")
	}
	if err := tx.Model(&TaskReference{}).Where("org_id = ? AND group_id = ? AND released_at IS NULL", group.OrgID, group.ID).Count(&count).Error; err != nil {
		return "", "", nil, ErrStorage
	}
	if count > 0 {
		return block("resource_in_use")
	}
	if group.ProviderID == nil {
		if group.Status != StatusFailed || group.CreateOperationID == "" {
			return block("creation_not_rejected")
		}
		var created Operation
		err := tx.Where("org_id = ? AND operation_id = ?", group.OrgID, group.CreateOperationID).Take(&created).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", nil, ErrStorage
		}
		if err != nil || created.Action != "CreateAssetGroup" || created.ResourceType != "asset_group" ||
			created.ResourceID != group.ID || created.Status != "rejected" || !sameScope(opScope(created), groupScope(group)) {
			return block("creation_not_rejected")
		}
		return "local_failed", "", latest, nil
	}
	if !identifier.MatchString(*group.ProviderID) || group.Status != StatusActive {
		return block("group_not_ready")
	}
	return "official", "", latest, nil
}

func deletionBlockError(reason string) error {
	switch reason {
	case "group_not_empty":
		return ErrNotEmpty
	case "resource_in_use":
		return ErrInUse
	case "real_group_not_supported", "scope_unavailable", "delete_capability_disabled", "exclusive_project_required":
		return ErrUnsupported
	default:
		return ErrConflict
	}
}

func (s *Service) deleteFailedGroup(ctx context.Context, c CreateContext, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, c.OrgID); err != nil {
			return err
		}
		var group Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id = ?", c.OrgID, id).Take(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrStorage
		}
		if group.DeletedAt != nil {
			return nil
		}
		mode, reason, _, err := s.groupDeletionState(tx, group)
		if err != nil {
			return err
		}
		if mode != "local_failed" {
			return deletionBlockError(reason)
		}
		now := nowMillis(s)
		res := tx.Model(&Group{}).Where("org_id = ? AND group_id = ? AND row_version = ? AND deleted_at IS NULL", c.OrgID, id, group.RowVersion).
			Updates(map[string]any{"status": StatusDeleted, "deleted_at": now, "row_version": gorm.Expr("row_version + 1"), "updated_at": now})
		if res.Error != nil || res.RowsAffected != 1 {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, "asset_group", id, group.CreateOperationID, "local_failed_group_deleted", "", callerLabel(c))
	})
}

func safeOperationError(code string) *SafeError {
	messages := map[string]string{
		"provider_timeout":               "The provider request timed out; the operation result needs verification.",
		"provider_rate_limited":          "The provider rate limit was reached.",
		"provider_authentication_failed": "The provider rejected the management credentials.",
		"provider_permission_denied":     "The management account cannot access this project or action.",
		"provider_rejected":              "The provider rejected this operation.",
		"provider_group_not_found":       "The provider reported that the group was not found.",
		"group_not_empty":                "The official group contains assets and cannot be deleted.",
		"group_empty_check_failed":       "The official empty-group check could not be verified.",
		"group_absence_unverified":       "The provider reported a missing group, but the follow-up check could not confirm it.",
		"group_identity_mismatch":        "The deletion request does not match the group's fixed provider identity.",
		"provider_response_invalid":      "The provider response could not be verified.",
		"submission_uncertain":           "The operation outcome is uncertain. Do not submit it again.",
		"unsupported_capability":         "The configured scope no longer permits this operation.",
		"execution_payload_unavailable":  "The retained execution request is unavailable.",
		"group_not_empty_or_unverified":  "The official empty-group check could not be verified.",
	}
	message, ok := messages[code]
	if !ok {
		return &SafeError{Code: "provider_unavailable", Message: "Provider operation requires verification.", Retryable: false}
	}
	return &SafeError{Code: code, Message: message, Retryable: false}
}

type deletionEvidence struct {
	stage  string
	result Result
}

// Every stage gets its own Invocation Store correlation and event. Capture=false
// retains only capture_disabled metadata, respecting the organization's policy.
func (s *Service) prepareDeletionEvidence(op Operation, r RuntimeScope, evidence []deletionEvidence) ([]*invocation.PreparedVideoPayload, error) {
	prepared := make([]*invocation.PreparedVideoPayload, len(evidence))
	for i, entry := range evidence {
		if len(entry.result.Body) == 0 {
			continue
		}
		p, err := s.prepareHistoryEvidence(r, invocation.VideoPayloadInput{
			OrgID: op.OrgID, RequestID: op.RequestID, AttemptID: op.ID + "_" + entry.stage,
			Direction: invocation.VideoUpstreamResponse, SchemaVersion: 1,
			Body:                  entry.result.Body,
			SecretValues:          []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken},
			CaptureRepresentation: "response_json_redacted",
		})
		if err != nil {
			return nil, ErrStorage
		}
		prepared[i] = p
	}
	return prepared, nil
}

// The execution payload, current row, and immutable operation scope must agree
// before either the empty check or any destructive provider call is dispatched.
func (s *Service) deletionIdentity(ctx context.Context, op Operation, request []byte) bool {
	var body struct{ ID, ProjectName string }
	if json.Unmarshal(request, &body) != nil || !identifier.MatchString(body.ID) || body.ProjectName != op.Project {
		return false
	}
	var group Group
	if s.db.WithContext(ctx).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", op.OrgID, op.ResourceID).Take(&group).Error != nil {
		return false
	}
	return group.Kind == KindVirtual && group.Status == StatusDeleting && group.ReviewState == ReviewClear &&
		group.DeleteOperationID == op.ID && group.ProviderID != nil && *group.ProviderID == body.ID &&
		sameScope(groupScope(group), opScope(op))
}

func (s *Service) processGroupDeletion(ctx context.Context, op Operation, r RuntimeScope, request []byte) error {
	if !s.deletionIdentity(ctx, op, request) {
		return s.finish(ctx, op, r, Result{Outcome: "unknown", SafeErrorCode: "group_identity_mismatch"}, request)
	}
	check := s.options.Client.Call(ctx, r.Scope, r.Credentials, "ListAssets",
		bodyJSON(map[string]any{"Filter": map[string]any{"GroupType": "AIGC", "GroupIds": []string{extractProviderID(request)}}, "MaxResults": 1, "ProjectName": r.Scope.Project}), s.now())
	defer clear(check.Body)
	defer clear(check.Data)
	evidence := []deletionEvidence{{stage: "precheck", result: check}}
	var page struct {
		Items     []json.RawMessage
		NextToken string
	}
	if check.Outcome != "accepted" || json.Unmarshal(check.Data, &page) != nil || page.Items == nil || len(page.Items) > 0 || page.NextToken != "" {
		code := "group_empty_check_failed"
		if check.Outcome == "accepted" && page.Items != nil && len(page.Items) > 0 {
			code = "group_not_empty"
		}
		// No DELETE was sent. Keep the check's HTTP/request identifiers and body,
		// but rejection describes our precondition, not a provider DELETE reply.
		check.Outcome, check.SafeErrorCode = "not_sent", code
		return s.finish(ctx, op, r, check, request, evidence...)
	}
	// The empty check is a network round trip. Recheck the claim and capability
	// immediately before the irreversible DELETE, not merely before ListAssets.
	var live int64
	if err := s.db.WithContext(ctx).Model(&Operation{}).Where("org_id = ? AND operation_id = ? AND status = ? AND fence = ? AND lease_owner = ? AND lease_expires_at > ?",
		op.OrgID, op.ID, "in_flight", op.Fence, op.LeaseOwner, s.now()).Count(&live).Error; err != nil {
		return ErrStorage
	}
	if live != 1 {
		return s.finish(ctx, op, r, Result{Outcome: "unknown", SafeErrorCode: "submission_uncertain"}, request, evidence...)
	}
	current, err := s.resolve(ctx, op.OrgID, opScope(op))
	if err != nil || !current.ExecutionRetentionConsent || !operationAllowed(current.Capabilities, op.Action) {
		return s.finish(ctx, op, r, Result{Outcome: "not_sent", SafeErrorCode: "unsupported_capability"}, request, evidence...)
	}
	r = current
	if !s.deletionIdentity(ctx, op, request) {
		return s.finish(ctx, op, r, Result{Outcome: "unknown", SafeErrorCode: "group_identity_mismatch"}, request, evidence...)
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, "DeleteAssetGroup", request, s.now())
	defer clear(result.Body)
	defer clear(result.Data)
	if result.GroupNotFound {
		// Query the exact ID in the same fixed account/region/project. A 2xx
		// group, ordinary 404, malformed envelope, denial, or timeout is not proof.
		verify := s.options.Client.Call(ctx, r.Scope, r.Credentials, "GetAssetGroup", request, s.now())
		defer clear(verify.Body)
		defer clear(verify.Data)
		evidence = append(evidence, deletionEvidence{stage: "verify", result: verify})
		if verify.GroupNotFound {
			result.Outcome, result.SafeErrorCode, result.AbsenceConfirmed = "accepted", "", true
			result.Data = json.RawMessage(`{}`)
		} else {
			result.Outcome, result.SafeErrorCode = "unknown", "group_absence_unverified"
		}
	}
	return s.finish(ctx, op, r, result, request, evidence...)
}
