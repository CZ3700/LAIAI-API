package videoasset

import "gorm.io/gorm"

// Official Active/Processing/Failed are normalized to lowercase. Local admission
// or deletion states are not evidence of an official asset state. Keep the
// existing public status/stage separate from this console-only projection.
func consoleProviderStatus(row Asset, dto AssetDTO) string {
	if row.ProviderID == nil || *row.ProviderID == "" ||
		operationHasStatus(dto.CreateOperation, "unknown") || operationHasStatus(dto.DeleteOperation, "unknown") {
		return "unknown"
	}
	switch row.Status {
	case StatusActive, StatusProcessing, StatusFailed:
		return row.Status
	default:
		return "unknown"
	}
}

func operationHasStatus(op *OperationDTO, states ...string) bool {
	if op != nil {
		for _, state := range states {
			if op.Status == state {
				return true
			}
		}
	}
	return false
}

// This describes the asset's reference eligibility, not complete video-task
// admission. Model compatibility, current scope, authorization and budgets must
// still pass their existing checks when the caller creates a generation task.
func consoleGenerationStatus(row Asset, dto AssetDTO, providerStatus, mediaStatus string) (string, string) {
	switch {
	case operationHasStatus(dto.CreateOperation, "unknown") || operationHasStatus(dto.DeleteOperation, "unknown") || dto.DeletionBlockReason == "submission_uncertain":
		return "blocked", "submission_uncertain"
	case row.Status == StatusDeleting || operationHasStatus(dto.CreateOperation, "pending", "in_flight") || operationHasStatus(dto.DeleteOperation, "pending", "in_flight"):
		return "blocked", "operation_pending"
	case providerStatus != StatusActive:
		return "blocked", "provider_not_ready"
	case mediaStatus != "verified":
		return "blocked", "media_unverified"
	case dto.Reference == "":
		return "blocked", "resource_not_ready"
	default:
		return "ready", ""
	}
}

func validProviderStatusFilter(status string) bool {
	switch status {
	case "", StatusActive, StatusProcessing, StatusFailed, "unknown":
		return true
	default:
		return false
	}
}

// Match the same immutable identity used when exposing operation receipts. Even
// if an older row still has an Active snapshot, an unknown create/delete must
// not be classified as a confirmed official asset by either listing or filter.
const consoleUnknownSubmission = `EXISTS (SELECT 1 FROM video_asset_operations o
 JOIN video_asset_groups g ON g.org_id = video_assets.org_id AND g.group_id = video_assets.group_id
 WHERE o.org_id = video_assets.org_id AND o.resource_type = 'asset' AND o.resource_id = video_assets.asset_id
 AND o.action IN ('CreateAsset','DeleteAsset') AND o.status = 'unknown'
 AND o.scope_id = video_assets.scope_id AND o.scope_version = video_assets.scope_version
 AND g.scope_id = video_assets.scope_id AND g.scope_version = video_assets.scope_version
 AND o.account_id = g.account_id AND o.region = g.region AND o.project = g.project
 AND (o.action = 'DeleteAsset' OR o.operation_id = video_assets.create_operation_id))`

func applyProviderStatusFilter(q *gorm.DB, status string) *gorm.DB {
	if status == "" {
		return q
	}
	if status == "unknown" {
		return q.Where("provider_id IS NULL OR provider_id = '' OR status NOT IN ? OR "+consoleUnknownSubmission,
			[]string{StatusActive, StatusProcessing, StatusFailed})
	}
	return q.Where("provider_id IS NOT NULL AND provider_id <> '' AND status = ? AND NOT ("+consoleUnknownSubmission+")", status)
}
