package videoasset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
)

func groupErrorReply(req *http.Request, status int, code string, change func(map[string]any)) *http.Response {
	meta := map[string]any{"RequestId": "official-error-" + req.URL.Query().Get("Action"), "Action": req.URL.Query().Get("Action"),
		"Version": managementVersion, "Service": "ark", "Region": "cn-beijing", "Error": map[string]string{"Code": code, "Message": "raw-private-provider-message"}}
	if change != nil {
		change(meta)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(bodyJSON(map[string]any{"ResponseMetadata": meta}))), Request: req}
}

func rejectedGroup(t *testing.T, s *Service) GroupDTO {
	t.Helper()
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return groupErrorReply(req, 403, "AccessDenied", nil), nil
	})})
	group, _, err := s.CreateGroup(context.Background(), createContext("rejected-create"), GroupCreate{Kind: KindVirtual, Name: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	group, err = s.GetGroup(context.Background(), 1, group.ID)
	if err != nil || group.Status != "failed" || group.DeletionMode != "local_failed" {
		t.Fatal(group, err)
	}
	return group
}

func TestFailedGroupLocalCleanupIsAtomicIsolatedAndIdempotent(t *testing.T) {
	s, db, _, _ := setup(t)
	group := rejectedGroup(t, s)
	ctx := context.Background()
	// Cleanup must still work after removing the management configuration,
	// without even resolving credentials or making a provider request.
	s.options.ResolveScope = func(context.Context, uint64, Scope) (RuntimeScope, error) {
		t.Fatal("local cleanup resolved provider credentials")
		return RuntimeScope{}, ErrUnsupported
	}
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("local cleanup contacted provider")
		return nil, nil
	})})
	other := createContext("other")
	other.OrgID = 2
	if _, _, err := s.DeleteGroup(ctx, other, group.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// If the audit write fails, no tombstone may escape its transaction.
	if err := db.Exec(`CREATE TRIGGER reject_local_delete_audit BEFORE INSERT ON video_asset_events
		WHEN NEW.kind = 'local_failed_group_deleted' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, done, err := s.DeleteGroup(ctx, createContext("cleanup"), group.ID, false); !errors.Is(err, ErrStorage) || done {
		t.Fatal(done, err)
	}
	var row Group
	if err := db.Where("group_id = ?", group.ID).Take(&row).Error; err != nil || row.DeletedAt != nil || row.Status != StatusFailed {
		t.Fatal(row, err)
	}
	if err := db.Exec("DROP TRIGGER reject_local_delete_audit").Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cleanup", "cleanup", "cleanup-new-key"} {
		if _, done, err := s.DeleteGroup(ctx, createContext(key), group.ID, false); err != nil || !done {
			t.Fatal(done, err)
		}
	}
	var events, operations int64
	db.Model(&Event{}).Where("org_id = ? AND resource_id = ? AND kind = ?", 1, group.ID, "local_failed_group_deleted").Count(&events)
	db.Model(&Operation{}).Where("org_id = ? AND resource_id = ?", 1, group.ID).Count(&operations)
	if events != 1 || operations != 1 {
		t.Fatal("duplicate audit or deletion provider intent", events, operations)
	}
	if _, err := s.GetGroup(ctx, 1, group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cleanup visible", err)
	}
}

func TestFailedGroupCleanupRequiresDefiniteMatchingCreation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*gorm.DB, GroupDTO)
	}{
		{"unknown", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("status", "unknown")
		}},
		{"needs-review", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Group{}).Where("group_id = ?", g.ID).Update("review_state", ReviewRequired)
		}},
		{"scope", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("project", "other")
		}},
		{"resource", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("resource_id", "other")
		}},
		{"org", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("org_id", 2)
		}},
		{"action", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("action", "UpdateAssetGroup")
		}},
		{"provider-empty-string-is-not-nil", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Group{}).Where("group_id = ?", g.ID).Update("provider_id", "")
		}},
		{"pending", func(db *gorm.DB, g GroupDTO) {
			db.Model(&Operation{}).Where("resource_id = ?", g.ID).Update("status", "pending")
		}},
		{"historical-asset", func(db *gorm.DB, g GroupDTO) {
			now := g.CreatedAt
			db.Create(&Asset{ID: "historic", OrgID: 1, GroupID: g.ID, DeletedAt: &now})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, _ := setup(t)
			g := rejectedGroup(t, s)
			tc.edit(db, g)
			if _, done, err := s.DeleteGroup(context.Background(), createContext("cleanup"), g.ID, false); err == nil || done {
				t.Fatal("unsafe cleanup", done, err)
			}
			var row Group
			db.Where("group_id = ?", g.ID).Take(&row)
			if row.DeletedAt != nil {
				t.Fatal("group was deleted")
			}
		})
	}
}

func TestGroupNotFoundRequiresExactEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		change func(map[string]any)
		want   bool
	}{
		{"exact", 404, "NotFound.group_id", nil, true},
		{"generic404", 404, "NotFound", nil, false},
		{"permission", 403, "NotFound.group_id", nil, false},
		{"wrong-case", 404, "notfound.group_id", nil, false},
		{"wrong-action", 404, "NotFound.group_id", func(m map[string]any) { m["Action"] = "DeleteAsset" }, false},
		{"wrong-version", 404, "NotFound.group_id", func(m map[string]any) { m["Version"] = "other" }, false},
		{"wrong-service", 404, "NotFound.group_id", func(m map[string]any) { m["Service"] = "other" }, false},
		{"wrong-region", 404, "NotFound.group_id", func(m map[string]any) { m["Region"] = "other" }, false},
		{"missing-id", 404, "NotFound.group_id", func(m map[string]any) { delete(m, "RequestId") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, action := range []string{"DeleteAssetGroup", "GetAssetGroup"} {
				client := NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
					return groupErrorReply(req, tc.status, tc.code, tc.change), nil
				})})
				s, _, _, _ := setup(t)
				got := client.Call(context.Background(), testScope(), Credentials{AccessKey: "fixture", SecretKey: "fixture"}, action,
					[]byte(`{"Id":"upstream1","ProjectName":"project1"}`), s.now())
				if got.GroupNotFound != tc.want {
					t.Fatal(got)
				}
				if tc.change != nil && got.Outcome != "unknown" {
					t.Fatal("unverified error identity became definite rejection", got)
				}
			}
		})
	}
}

func TestDeleteMissingGroupRequiresSameScopedFollowupAndRetainsEvidence(t *testing.T) {
	for _, scenario := range []string{"exact", "generic404", "403", "present", "wrong-envelope", "timeout", "history-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, _, provider := setup(t)
			g := readyGroup(t, s, "group")
			var group Group
			db.Where("group_id = ?", g.ID).Take(&group)
			if scenario == "history-disabled" {
				resolve := s.options.ResolveScope
				s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
					r, err := resolve(ctx, org, scope)
					r.CaptureHistory = false
					return r, err
				}
			}
			calls := map[string]int{}
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				action := req.URL.Query().Get("Action")
				calls[action]++
				if action == "ListAssets" {
					return provider.roundTrip(req)
				}
				var wire map[string]string
				raw, _ := io.ReadAll(req.Body)
				if json.Unmarshal(raw, &wire) != nil || wire["Id"] != *group.ProviderID || wire["ProjectName"] != group.Project ||
					req.Host != managementHost || !strings.Contains(req.Header.Get("Authorization"), "/cn-beijing/ark/request") {
					t.Fatal("changed provider identity or signing scope")
				}
				if action == "DeleteAssetGroup" {
					return groupErrorReply(req, 404, "NotFound.group_id", nil), nil
				}
				switch scenario {
				case "generic404":
					return groupErrorReply(req, 404, "NotFound", nil), nil
				case "403":
					return groupErrorReply(req, 403, "AccessDenied", nil), nil
				case "present":
					return syncReply(req, map[string]any{"Id": *group.ProviderID, "ProjectName": group.Project, "GroupType": "AIGC"}), nil
				case "wrong-envelope":
					return groupErrorReply(req, 404, "NotFound.group_id", func(m map[string]any) { m["Action"] = "ListAssets" }), nil
				case "timeout":
					return nil, errors.New("fixture network failure")
				default:
					return groupErrorReply(req, 404, "NotFound.group_id", nil), nil
				}
			})})
			op, _, err := s.DeleteGroup(context.Background(), createContext("delete"), g.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			var stored Operation
			db.Where("operation_id = ?", op.ID).Take(&stored)
			succeeded := scenario == "exact" || scenario == "history-disabled"
			if succeeded && (stored.Status != "confirmed" || stored.Receipt != `{"absence_confirmed":true}`) ||
				!succeeded && (stored.Status != "unknown" || stored.SafeErrorCode != "group_absence_unverified") ||
				stored.HTTPStatus != 404 {
				t.Fatal(stored.Status, stored.SafeErrorCode, stored.HTTPStatus, stored.Receipt)
			}
			db.Where("group_id = ?", g.ID).Take(&group)
			if (group.DeletedAt != nil) != succeeded {
				t.Fatal("wrong tombstone state")
			}
			if !succeeded {
				dto, e := s.GetGroup(context.Background(), 1, g.ID)
				if e != nil || dto.DeletionMode != "blocked" || dto.DeletionBlockReason != "submission_uncertain" ||
					dto.DeleteOperation == nil || dto.DeleteOperation.Status != "unknown" || dto.DeleteOperation.Error.Code != "group_absence_unverified" {
					t.Fatal(dto, e)
				}
			}
			// Reopening, repeating DELETE, and workers must never replay unknown.
			_, _, _ = s.DeleteGroup(context.Background(), createContext("delete-again"), g.ID, false)
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls["DeleteAssetGroup"] != 1 || calls["GetAssetGroup"] != 1 || calls["ListAssets"] != 1 {
				t.Fatal(calls)
			}
			var events []Event
			db.Where("org_id = ? AND operation_id = ? AND kind IN ?", 1, op.ID, []string{"delete_precheck", "delete_verify"}).Order("kind").Find(&events)
			if len(events) != 2 {
				t.Fatal("missing stage audit", events)
			}
			for _, event := range events {
				if scenario == "timeout" && event.Kind == "delete_verify" {
					continue
				}
				var payload invocation.VideoPayload
				if err := db.Where("payload_id = ?", event.EvidenceRef).Take(&payload).Error; err != nil {
					t.Fatal(err)
				}
				if scenario == "history-disabled" {
					if payload.CaptureStatus != invocation.VideoCaptureDisabled || payload.WrappedKey != "" {
						t.Fatal("history policy bypass")
					}
				} else {
					raw, err := s.evidence.ReadForHistory(context.Background(), 1, event.EvidenceRef,
						invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user1", ReasonCode: "request_debugging", OperationID: "read1"})
					if err != nil || !bytes.Contains(raw, []byte("ResponseMetadata")) {
						t.Fatal("missing encrypted evidence", err)
					}
					clear(raw)
				}
			}
			if scenario != "history-disabled" {
				raw, err := s.evidence.ReadForHistory(context.Background(), 1, stored.ResponsePayloadID,
					invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: "user1", ReasonCode: "request_debugging", OperationID: "read2"})
				if err != nil || !bytes.Contains(raw, []byte("NotFound.group_id")) {
					t.Fatal("original DELETE evidence lost", err)
				}
				clear(raw)
			}
		})
	}
}

func TestGroupDeletePreflightFailuresAreVisibleAndRetained(t *testing.T) {
	for _, scenario := range []string{"nonempty", "permission", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, _, provider := setup(t)
			g := readyGroup(t, s, "group")
			calls := 0
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Query().Get("Action") != "ListAssets" {
					t.Fatal("DELETE must not be sent after failed precheck")
				}
				switch scenario {
				case "nonempty":
					return syncReply(req, map[string]any{"Items": []any{map[string]string{"Id": "asset1"}}}), nil
				case "permission":
					return groupErrorReply(req, 403, "AccessDenied", nil), nil
				default:
					return syncReply(req, map[string]any{}), nil
				}
			})})
			c := createContext("delete")
			op, _, err := s.DeleteGroup(context.Background(), c, g.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			dto, err := s.GetGroup(context.Background(), 1, g.ID)
			if err != nil || dto.DeletionMode != "official" || dto.DeleteOperation == nil ||
				dto.DeleteOperation.ID != op.ID || dto.DeleteOperation.Status != "rejected" || dto.DeleteOperation.Error == nil {
				t.Fatal(dto, err)
			}
			wantCode := "group_empty_check_failed"
			if scenario == "nonempty" {
				wantCode = "group_not_empty"
			}
			if dto.DeleteOperation.Error.Code != wantCode || strings.Contains(string(bodyJSON(dto)), "raw-private-provider-message") {
				t.Fatal(dto)
			}
			var event Event
			if err := db.Where("operation_id = ? AND kind = ?", op.ID, "delete_precheck").Take(&event).Error; err != nil || event.EvidenceRef == "" {
				t.Fatal(event, err)
			}
			// Legacy rows that cleared the pointer still restore the last receipt.
			db.Model(&Group{}).Where("group_id = ?", g.ID).Update("delete_operation_id", "")
			dto, err = s.GetGroup(context.Background(), 1, g.ID)
			if err != nil || dto.DeleteOperation == nil || dto.DeleteOperation.ID != op.ID {
				t.Fatal("legacy rejection disappeared", dto, err)
			}
			replay, _, err := s.DeleteGroup(context.Background(), c, g.ID, false)
			if err != nil || replay.ID != op.ID || replay.Status != "rejected" {
				t.Fatal(replay, err)
			}
			if calls != 1 {
				t.Fatal("same idempotency key resent", calls)
			}
			// A distinct, deliberate retry can succeed after the precondition changes.
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(provider.roundTrip)})
			next, _, err := s.DeleteGroup(context.Background(), createContext("new-delete"), g.ID, false)
			if err != nil || next.ID == op.ID {
				t.Fatal(next, err)
			}
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetGroup(context.Background(), 1, g.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestGroupDeletionRechecksClaimIdentityAndCapabilityAfterPreflight(t *testing.T) {
	for _, scenario := range []string{"lease", "fence", "identity", "capability"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, now, provider := setup(t)
			group := readyGroup(t, s, "group")
			op, _, err := s.DeleteGroup(context.Background(), createContext("delete"), group.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("Action") != "ListAssets" {
					t.Fatal("stale worker sent destructive request")
				}
				switch scenario {
				case "lease":
					*now = now.Add(3 * time.Minute)
				case "fence":
					db.Model(&Operation{}).Where("operation_id = ?", op.ID).Update("fence", gorm.Expr("fence + 1"))
				case "identity":
					db.Model(&Group{}).Where("group_id = ?", group.ID).Update("provider_id", "changed-id")
				case "capability":
					resolve := s.options.ResolveScope
					s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
						r, err := resolve(ctx, org, scope)
						r.Capabilities.DeleteVirtualEmptyGroups = false
						return r, err
					}
				}
				return provider.roundTrip(req)
			})})
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			var row Group
			db.Where("group_id = ?", group.ID).Take(&row)
			if row.DeletedAt != nil {
				t.Fatal("stale worker removed group")
			}
			got, err := s.GetOperation(context.Background(), 1, op.ID)
			if err != nil || got.Status == "confirmed" {
				t.Fatal(got, err)
			}
			if scenario == "fence" && got.Status != "in_flight" {
				t.Fatal("stale worker modified new fence", got)
			}
			if scenario == "capability" && got.Status != "rejected" {
				t.Fatal("known not-sent capability failure", got)
			}
		})
	}
}

func TestDeleteAbsenceConfirmationRollsBackWithAuditAndNeverResends(t *testing.T) {
	s, db, now, provider := setup(t)
	group := readyGroup(t, s, "group")
	deletes := 0
	s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("Action") == "ListAssets" {
			return provider.roundTrip(req)
		}
		if req.URL.Query().Get("Action") == "DeleteAssetGroup" {
			deletes++
		}
		return groupErrorReply(req, 404, "NotFound.group_id", nil), nil
	})})
	op, _, err := s.DeleteGroup(context.Background(), createContext("delete"), group.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_absence_audit BEFORE INSERT ON video_asset_events
		WHEN NEW.kind = 'delete_absence_confirmed' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessOnce(context.Background()); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	var row Group
	db.Where("group_id = ?", group.ID).Take(&row)
	got, err := s.GetOperation(context.Background(), 1, op.ID)
	if err != nil || row.DeletedAt != nil || row.Status != StatusDeleting || got.Status != "in_flight" {
		t.Fatal(row.Status, row.DeletedAt, got, err)
	}
	var evidence int64
	db.Model(&invocation.VideoPayload{}).Where("request_id = ? AND direction = ?", got.RequestID, invocation.VideoUpstreamResponse).Count(&evidence)
	if evidence != 0 {
		t.Fatal("partial evidence escaped rollback", evidence)
	}
	if err := db.Exec("DROP TRIGGER reject_absence_audit").Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(3 * time.Minute)
	if err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetOperation(context.Background(), 1, op.ID)
	if err != nil || got.Status != "unknown" || deletes != 1 {
		t.Fatal(got, err, deletes)
	}
}

func TestOfficialDeletionRetainsLiveReferenceAndCapabilityBarriers(t *testing.T) {
	s, db, now, _ := setup(t)
	group := readyGroup(t, s, "group")
	asset := readyAsset(t, s, now, group.ID, "asset")
	if _, _, err := s.DeleteGroup(context.Background(), createContext("nonempty"), group.ID, false); !errors.Is(err, ErrNotEmpty) {
		t.Fatal(err)
	}
	if err := db.Create(&TaskReference{OrgID: 1, TaskID: "task1", AssetID: asset.ID, GroupID: group.ID, ContentIndex: 0, Role: "reference_image", CreatedAt: *now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&Asset{}).Where("asset_id = ?", asset.ID).Update("deleted_at", *now).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteGroup(context.Background(), createContext("referenced"), group.ID, false); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if err := db.Model(&TaskReference{}).Where("task_id = ?", "task1").Update("released_at", *now).Error; err != nil {
		t.Fatal(err)
	}
	resolve := s.options.ResolveScope
	s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
		r, err := resolve(ctx, org, scope)
		r.Capabilities.ExclusiveProjectWrites = false
		return r, err
	}
	dto, err := s.GetGroup(context.Background(), 1, group.ID)
	if err != nil || dto.DeletionMode != "blocked" || dto.DeletionBlockReason != "exclusive_project_required" {
		t.Fatal(dto, err)
	}
	if _, _, err := s.DeleteGroup(context.Background(), createContext("exclusive"), group.ID, false); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

// realGroupDeletionFixture 手工落一个 active 的 LivenessFace 组：真人组无
// 显式创建 API（火山侧由认证产生），fixture 直接构造与 worker 建组同构的行。
func realGroupDeletionFixture(t *testing.T, db *gorm.DB, license string, withPortrait bool, withAsset, withRef bool) Group {
	t.Helper()
	scope := testScope()
	now := time.Now()
	providerID := "group-real-delete-fixture"
	group := Group{ID: "ag_real_delete_" + license, OrgID: 1, Kind: KindReal, Name: "real", ScopeID: scope.ID,
		ScopeVersion: scope.Version, AccountID: scope.AccountID, Region: scope.Region, Project: scope.Project,
		ProviderID: &providerID, Status: StatusActive, ReviewState: ReviewClear, RowVersion: 1}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Ownership{OrgID: 1, ResourceType: "asset_group", ResourceID: group.ID, OwnerUserID: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if withPortrait {
		group.PortraitID = "pas_real_" + license
		portrait := Portrait{ID: group.PortraitID, OrgID: 1, OwnerUserID: 2, CreatedByKeyID: 3,
			ScopeID: scope.ID, ScopeVersion: scope.Version, AccountID: scope.AccountID, Region: scope.Region, Project: scope.Project,
			SessionStatus: "succeeded", LicenseStatus: license, CreateOperationID: "op_portrait_fixture",
			CallbackTargetID: "callback1", SecretRef: "secret_fixture", LicenseScope: "video_generation",
			LicenseVersion: 1, LicenseEvidenceRef: "evidence_fixture", LicenseSource: "official", RowVersion: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&portrait).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&Group{}).Where("org_id = ? AND group_id = ?", group.OrgID, group.ID).Update("portrait_id", group.PortraitID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if withAsset {
		asset := Asset{ID: "asset_real_" + license, OrgID: 1, GroupID: group.ID, MediaType: "image", Name: "face",
			ScopeID: scope.ID, ScopeVersion: scope.Version, Status: StatusActive, ReviewState: ReviewClear, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&asset).Error; err != nil {
			t.Fatal(err)
		}
	}
	if withRef {
		ref := TaskReference{OrgID: 1, TaskID: "task_ref_" + license, AssetID: "asset_real_" + license, GroupID: group.ID, ContentIndex: 0, Role: "reference_image", AssetMetadataVersion: 1, AuthorizationVersion: 1, CreatedAt: now}
		if err := db.Create(&ref).Error; err != nil {
			t.Fatal(err)
		}
	}
	return group
}

// TestRealGroupDeletionFollowsOfficialLicenseGateAndCascades（2026-11-18 对齐
// 火山 DeleteAssetGroup）：真人组删除由许可状态闸门控（verified/unknown 拒、
// expired/revoked 放行），火山级联删素材故跳过本地 group_not_empty 与空组
// 预检，worker 确认后组/素材/许可三终态联动。
func TestRealGroupDeletionFollowsOfficialLicenseGateAndCascades(t *testing.T) {
	cases := []struct {
		name     string
		license  string
		portrait bool
		withRef  bool
		wantErr  error
	}{
		{"verified-blocks", "verified", true, false, ErrAuthorizationActive},
		{"unknown-blocks", "unknown", true, false, ErrAuthorizationActive},
		{"no-portrait-blocks", "verified", false, false, ErrAuthorizationActive},
		{"reference-blocks", "expired", true, true, ErrInUse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, _ := setup(t)
			calls := map[string]int{}
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				calls[req.URL.Query().Get("Action")]++
				return groupErrorReply(req, 200, "", nil), nil
			})})
			group := realGroupDeletionFixture(t, db, tc.license, tc.portrait, true, tc.withRef)
			if _, _, err := s.DeleteGroup(context.Background(), createContext("real-delete"), group.ID, false); !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v got %v", tc.wantErr, err)
			}
			if calls["DeleteAssetGroup"] != 0 {
				t.Fatal("blocked deletion reached the provider", calls)
			}
			var stored Group
			db.Where("group_id = ?", group.ID).Take(&stored)
			if stored.Status != StatusActive || stored.DeletedAt != nil {
				t.Fatal("blocked group must stay untouched", stored.Status)
			}
		})
	}

	t.Run("expired-cascades-accepts-and-confirms", func(t *testing.T) {
		s, db, _, _ := setup(t)
		calls := map[string]int{}
		s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			action := req.URL.Query().Get("Action")
			calls[action]++
			if action == "DeleteAssetGroup" {
				return syncReply(req, map[string]any{}), nil
			}
			if action == "ListAssets" {
				t.Fatal("real group deletion must skip the virtual empty-group precheck")
			}
			return groupErrorReply(req, 404, "NotFound.group_id", nil), nil
		})})
		group := realGroupDeletionFixture(t, db, "expired", true, true, false)
		op, done, err := s.DeleteGroup(context.Background(), createContext("real-delete-ok"), group.ID, false)
		if err != nil || done {
			t.Fatal(op, done, err)
		}
		if err := s.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls["DeleteAssetGroup"] != 1 || calls["ListAssets"] != 0 {
			t.Fatal(calls)
		}
		var stored Group
		db.Where("group_id = ?", group.ID).Take(&stored)
		if stored.Status != StatusDeleted || stored.DeletedAt == nil {
			t.Fatal("confirmed group must be tombstoned", stored.Status)
		}
		var asset Asset
		if err := db.Where("asset_id = ?", "asset_real_expired").Take(&asset).Error; err != nil {
			t.Fatal(err)
		}
		if asset.Status != StatusDeleted || asset.DeletedAt == nil {
			t.Fatal("official cascade must soft-delete group assets", asset.Status)
		}
		var portrait Portrait
		if err := db.Where("session_id = ?", stored.PortraitID).Take(&portrait).Error; err != nil {
			t.Fatal(err)
		}
		if portrait.LicenseStatus != "revoked" || portrait.RevokedAt == nil {
			t.Fatal("bound license must be force-revoked", portrait.LicenseStatus)
		}
	})
}

func TestVerifiedRealGroupDeletionWithProcessingFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		additional     string
		secondUpstream string
		withReference  bool
		wantErr        error
	}{
		{name: "all failed across pages", additional: StatusFailed, secondUpstream: "Failed"},
		{name: "local active blocks", additional: StatusActive, wantErr: ErrAuthorizationActive},
		{name: "local processing blocks", additional: StatusProcessing, wantErr: ErrAuthorizationActive},
		{name: "in-flight reference blocks", withReference: true, wantErr: ErrInUse},
		{name: "upstream active on later page blocks", additional: StatusFailed, secondUpstream: "Active"},
		{name: "untracked upstream failure blocks", secondUpstream: "Failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, _ := setup(t)
			group := realGroupDeletionFixture(t, db, "verified", true, true, tc.withReference)
			firstID, secondID := "asset-upstream-failed-1", "asset-upstream-failed-2"
			if err := db.Model(&Asset{}).Where("asset_id = ?", "asset_real_verified").Updates(map[string]any{
				"provider_id": firstID, "status": StatusFailed, "safe_error_code": "asset_processing_failed",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if tc.additional != "" {
				if err := db.Create(&Asset{ID: "asset_second", OrgID: group.OrgID, GroupID: group.ID,
					ProviderID: &secondID, ScopeID: group.ScopeID, ScopeVersion: group.ScopeVersion, MediaType: "image",
					ReviewState: ReviewClear, Status: tc.additional, SafeErrorCode: "asset_processing_failed", RowVersion: 1,
				}).Error; err != nil {
					t.Fatal(err)
				}
			}
			listCalls, deleteCalls := 0, 0
			s.options.Client = NewManagementClient(&http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("Action") == "DeleteAssetGroup" {
					deleteCalls++
					return syncReply(req, map[string]any{}), nil
				}
				var input struct {
					Filter struct {
						GroupType string
						GroupIds  []string
					}
					NextToken string
				}
				if req.URL.Query().Get("Action") != "ListAssets" || json.NewDecoder(req.Body).Decode(&input) != nil ||
					input.Filter.GroupType != "LivenessFace" || len(input.Filter.GroupIds) != 1 || input.Filter.GroupIds[0] != *group.ProviderID {
					t.Errorf("unexpected provider request for %s", req.URL.Query().Get("Action"))
					return groupErrorReply(req, 400, "InvalidParameter", nil), nil
				}
				listCalls++
				if listCalls == 1 && input.NextToken == "" {
					return syncReply(req, map[string]any{
						"Items":     []safeReceipt{{ID: firstID, GroupID: *group.ProviderID, ProjectName: group.Project, Status: "Failed"}},
						"NextToken": "next-page",
					}), nil
				}
				if listCalls != 2 || input.NextToken != "next-page" {
					t.Errorf("unexpected cursor: %q", input.NextToken)
				}
				return syncReply(req, map[string]any{
					"Items": []safeReceipt{{ID: secondID, GroupID: *group.ProviderID, ProjectName: group.Project, Status: tc.secondUpstream}},
				}), nil
			})})
			op, done, err := s.DeleteGroup(context.Background(), createContext("delete-failed-real-group"), group.ID, false)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || listCalls != 0 || deleteCalls != 0 {
					t.Fatalf("expected %v before provider calls, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || done {
				t.Fatal(op, done, err)
			}
			if err := s.ProcessOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			var stored Group
			var recorded Operation
			var license Portrait
			if err := db.Where("group_id = ?", group.ID).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("operation_id = ?", op.ID).Take(&recorded).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("session_id = ?", group.PortraitID).Take(&license).Error; err != nil {
				t.Fatal(err)
			}
			if listCalls != 2 {
				t.Fatalf("expected both provider pages, got %d calls", listCalls)
			}
			if tc.additional == StatusFailed && tc.secondUpstream == "Failed" {
				if deleteCalls != 1 || recorded.Status != "confirmed" || stored.DeletedAt == nil || license.LicenseStatus != "revoked" {
					t.Fatalf("expected confirmed cascade: calls=%d operation=%s group=%s license=%s", deleteCalls, recorded.Status, stored.Status, license.LicenseStatus)
				}
				var live int64
				if err := db.Model(&Asset{}).Where("group_id = ? AND deleted_at IS NULL", group.ID).Count(&live).Error; err != nil || live != 0 {
					t.Fatalf("expected all failed assets to be deleted, live=%d err=%v", live, err)
				}
			} else if deleteCalls != 0 || recorded.Status != "rejected" || recorded.SafeErrorCode != "group_failed_assets_unverified" ||
				stored.Status != StatusActive || stored.DeletedAt != nil || license.LicenseStatus != "verified" {
				t.Fatalf("expected rejected precheck: calls=%d operation=%s code=%s group=%s license=%s", deleteCalls, recorded.Status, recorded.SafeErrorCode, stored.Status, license.LicenseStatus)
			}
		})
	}
}
