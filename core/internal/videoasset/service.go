package videoasset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrStorage
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
func safeName(v string, max int, required bool) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= max && (!required || strings.TrimSpace(v) != "") && strings.IndexFunc(v, unicode.IsControl) < 0
}
func validHTTPS(raw string, max int) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= max && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func nowMillis(s *Service) time.Time { return s.now().UTC().Truncate(time.Millisecond) }
func groupScope(g Group) Scope {
	return Scope{g.ScopeID, g.ScopeVersion, g.AccountID, g.Region, g.Project}
}
func portraitScope(p Portrait) Scope {
	return Scope{p.ScopeID, p.ScopeVersion, p.AccountID, p.Region, p.Project}
}
func opScope(o Operation) Scope {
	return Scope{o.ScopeID, o.ScopeVersion, o.AccountID, o.Region, o.Project}
}
func sameScope(a, b Scope) bool { return a == b }
func bodyJSON(v any) []byte     { raw, _ := json.Marshal(v); return raw }
func (s *Service) resolve(ctx context.Context, org uint64, requested Scope) (RuntimeScope, error) {
	r, err := s.options.ResolveScope(ctx, org, requested)
	if err != nil {
		return RuntimeScope{}, ErrUnsupported
	}
	if !validScope(r.Scope) || requested.ID != "" && !sameScope(requested, r.Scope) {
		return RuntimeScope{}, ErrUnsupported
	}
	return r, nil
}
func lockTenant(tx *gorm.DB, org uint64) error {
	if org == 0 {
		return ErrInvalid
	}
	row := TenantLock{OrgID: org}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "org_id"}}, DoNothing: true}).Create(&row).Error; err != nil {
		return ErrStorage
	}
	if err := tx.Model(&TenantLock{}).Where("org_id = ?", org).Update("version", gorm.Expr("version + 1")).Error; err != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) event(tx *gorm.DB, org uint64, resourceType, resourceID, operation, kind, evidence, actor string) error {
	id, err := newID("ave_")
	if err != nil {
		return err
	}
	row := Event{ID: id, OrgID: org, ResourceType: resourceType, ResourceID: resourceID, OperationID: operation, Kind: kind, EvidenceRef: evidence, ActorID: actor, CreatedAt: nowMillis(s)}
	if err := tx.Create(&row).Error; err != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) digest(version, domain string, org uint64, path, key string) string {
	return hex.EncodeToString(mac(s.options.HMACKeys[version], string(bodyJSON([]any{"video-assets-v1", domain, org, path, key}))))
}
func (s *Service) replay(tx *gorm.DB, c CreateContext, path string, raw []byte) (Operation, bool, error) {
	hashes := make([]string, 0, len(s.options.HMACKeys))
	for version := range s.options.HMACKeys {
		hashes = append(hashes, s.digest(version, "idempotency", c.OrgID, path, c.IdempotencyKey))
	}
	var old Operation
	err := tx.Where("org_id = ? AND canonical_path = ? AND idempotency_hash IN ?", c.OrgID, path, hashes).Take(&old).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, ErrStorage
	}
	if len(s.options.HMACKeys[old.HMACKeyVersion]) == 0 || s.digest(old.HMACKeyVersion, "body", c.OrgID, path, string(raw)) != old.BodyHash {
		return Operation{}, false, ErrConflict
	}
	return old, true, nil
}
func validCreate(c CreateContext) bool {
	return c.OrgID > 0 && ((c.CallerKind == "session_user" && c.UserID > 0 && c.KeyID == 0) || ((c.CallerKind == "" || c.CallerKind == "api_key") && c.KeyID > 0)) && validID(c.RequestID) && len(c.IdempotencyKey) > 0 && len(c.IdempotencyKey) <= 128 && strings.IndexFunc(c.IdempotencyKey, unicode.IsControl) < 0
}

func (s *Service) accept(ctx context.Context, c CreateContext, r RuntimeScope, path, action, resourceType, resourceID string, public, provider []byte, create func(*gorm.DB, Operation) error, initialReceipt ...string) (Operation, bool, error) {
	if !validCreate(c) || !r.ExecutionRetentionConsent {
		return Operation{}, false, ErrInvalid
	}
	id, err := newID("op_")
	if err != nil {
		return Operation{}, false, err
	}
	now := nowMillis(s)
	prepared, err := s.evidence.Prepare(invocation.VideoPayloadInput{OrgID: c.OrgID, RequestID: c.RequestID, AttemptID: id, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: provider, ExpiresAt: now.Add(24 * time.Hour), CaptureEnabled: true, SecretValues: []string{r.Credentials.AccessKey, r.Credentials.SecretKey, r.Credentials.SessionToken}})
	if err != nil {
		return Operation{}, false, ErrInvalid
	}
	op := Operation{ID: id, OrgID: c.OrgID, Action: action, ResourceType: resourceType, ResourceID: resourceID, RequestID: c.RequestID, CanonicalPath: path,
		HMACKeyVersion: s.options.ActiveHMACKey, IdempotencyHash: s.digest(s.options.ActiveHMACKey, "idempotency", c.OrgID, path, c.IdempotencyKey), BodyHash: s.digest(s.options.ActiveHMACKey, "body", c.OrgID, path, string(public)),
		ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version, AccountID: r.Scope.AccountID, Region: r.Scope.Region, Project: r.Scope.Project, Status: "pending", PayloadID: prepared.PayloadID(), CreatedAt: now, UpdatedAt: now, NextAttemptAt: now}
	if len(initialReceipt) > 0 {
		op.Receipt = initialReceipt[0]
	}
	replayed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, c.OrgID); err != nil {
			return err
		}
		old, found, err := s.replay(tx, c, path, public)
		if err != nil {
			return err
		}
		if found {
			op = old
			replayed = true
			return nil
		}
		if err := create(tx, op); err != nil {
			return err
		}
		if _, err := s.evidence.StageTx(tx, prepared); err != nil {
			return ErrStorage
		}
		if err := tx.Create(&op).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, c.OrgID, resourceType, resourceID, id, "intent", prepared.PayloadID(), "gateway")
	})
	return op, replayed, err
}

func (s *Service) CreateGroup(ctx context.Context, c CreateContext, input GroupCreate) (GroupDTO, bool, error) {
	if !validCreate(c) || input.Kind != KindVirtual || !safeName(input.Name, 64, true) || !safeName(input.Description, 300, false) {
		return GroupDTO{}, false, ErrInvalid
	}
	if old, found, err := s.replay(s.db.WithContext(ctx), c, "/v1/video/asset-groups", bodyJSON(input)); err != nil {
		return GroupDTO{}, false, err
	} else if found {
		dto, err := s.GetGroup(ctx, c.OrgID, old.ResourceID)
		return dto, true, err
	}
	r, err := s.resolve(ctx, c.OrgID, Scope{})
	if err != nil {
		return GroupDTO{}, false, err
	}
	if !r.Capabilities.VirtualGroups {
		return GroupDTO{}, false, ErrUnsupported
	}
	id, err := newID("ag_")
	if err != nil {
		return GroupDTO{}, false, err
	}
	provider := bodyJSON(map[string]any{"Name": input.Name, "Description": input.Description, "GroupType": "AIGC", "ProjectName": r.Scope.Project})
	op, replayed, err := s.accept(ctx, c, r, "/v1/video/asset-groups", "CreateAssetGroup", "asset_group", id, bodyJSON(input), provider, func(tx *gorm.DB, op Operation) error {
		now := nowMillis(s)
		g := Group{ID: id, OrgID: c.OrgID, Kind: KindVirtual, Name: input.Name, ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version, AccountID: r.Scope.AccountID, Region: r.Scope.Region, Project: r.Scope.Project,
			Status: StatusPending, ReviewState: ReviewClear, CreateOperationID: op.ID, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&g).Error; err != nil {
			return ErrStorage
		}
		if err := saveDetails(tx, c.OrgID, id, input.Description, "platform", now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return GroupDTO{}, false, err
	}
	dto, err := s.GetGroup(ctx, c.OrgID, op.ResourceID)
	return dto, replayed, err
}

func mediaAllowed(c Capabilities, kind string) bool {
	return kind == "image" && c.Images || kind == "video" && c.Videos || kind == "audio" && c.Audio
}
func validMedia(m MediaEvidence, kind string, now time.Time) bool {
	if !validID(m.ImmutableObjectRef) || !validHTTPS(m.FetchURL, 4096) || len(m.ObjectSHA256) != 64 || m.Version == 0 || m.ByteSize < 1 || m.VerifiedAt.IsZero() || m.VerifiedAt.After(now) || now.Sub(m.VerifiedAt) > time.Hour {
		return false
	}
	if _, err := hex.DecodeString(m.ObjectSHA256); err != nil {
		return false
	}
	switch kind {
	case "image":
		formatOK := false
		switch m.MIME {
		case "image/jpeg", "image/png", "image/webp", "image/tiff", "image/gif", "image/heic", "image/heif":
			formatOK = true
		}
		return m.ByteSize < 30_000_000 && m.Width > 300 && m.Width < 6000 && m.Height > 300 && m.Height < 6000 && m.Width*10 > m.Height*4 && m.Width*2 < m.Height*5 && formatOK
	case "video":
		return m.ByteSize <= 200_000_000 && m.DurationMS >= 2000 && m.DurationMS <= 30000 && m.Width >= 300 && m.Width <= 6000 && m.Height >= 300 && m.Height <= 6000 && m.Width*10 >= m.Height*4 && m.Width*2 <= m.Height*5 && m.Width*m.Height >= 407696 && m.Width*m.Height <= 8295044 && m.FPSDen > 0 && m.FPSDen <= 1_000_000 && m.FPSNum >= 24*m.FPSDen && m.FPSNum <= 60*m.FPSDen && (m.MIME == "video/mp4" || m.MIME == "video/quicktime")
	case "audio":
		return m.ByteSize <= 15_000_000 && m.DurationMS >= 2000 && m.DurationMS <= 30000 && (m.MIME == "audio/mpeg" || m.MIME == "audio/wav" || m.MIME == "audio/x-wav")
	}
	return false
}
func (s *Service) CreateAsset(ctx context.Context, c CreateContext, input AssetCreate) (AssetDTO, bool, error) {
	if !validCreate(c) || !validID(input.GroupID) || !validHTTPS(input.URL, 4096) || !safeName(input.Name, 64, false) {
		return AssetDTO{}, false, ErrInvalid
	}
	if old, found, err := s.replay(s.db.WithContext(ctx), c, "/v1/video/assets", bodyJSON(input)); err != nil {
		return AssetDTO{}, false, err
	} else if found {
		dto, err := s.GetAsset(ctx, c.OrgID, old.ResourceID)
		return dto, true, err
	}
	var group Group
	if err := s.db.WithContext(ctx).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", c.OrgID, input.GroupID).Take(&group).Error; err != nil {
		return AssetDTO{}, false, ErrNotFound
	}
	r, err := s.resolve(ctx, c.OrgID, groupScope(group))
	if err != nil {
		return AssetDTO{}, false, err
	}
	if !mediaAllowed(r.Capabilities, input.MediaType) || s.options.VerifyMedia == nil && s.console.Media == nil {
		return AssetDTO{}, false, ErrUnsupported
	}
	media, err := s.verifyMedia(ctx, c.OrgID, input.URL, input.MediaType)
	if err != nil || !validMedia(media, input.MediaType, s.now()) {
		return AssetDTO{}, false, ErrInvalid
	}
	if group.ProviderID == nil {
		return AssetDTO{}, false, ErrConflict
	}
	id, err := newID("asset_")
	if err != nil {
		return AssetDTO{}, false, err
	}
	provider := bodyJSON(map[string]any{"GroupId": *group.ProviderID, "URL": media.FetchURL, "Name": input.Name, "AssetType": strings.ToUpper(input.MediaType[:1]) + input.MediaType[1:], "ProjectName": r.Scope.Project})
	op, replayed, err := s.accept(ctx, c, r, "/v1/video/assets", "CreateAsset", "asset", id, bodyJSON(input), provider, func(tx *gorm.DB, op Operation) error {
		var current Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id = ?", c.OrgID, group.ID).Take(&current).Error; err != nil {
			return ErrNotFound
		}
		if current.Status != StatusActive || current.ReviewState != ReviewClear || current.DeletedAt != nil || !sameScope(groupScope(current), r.Scope) || current.ProviderID == nil || *current.ProviderID != *group.ProviderID {
			return ErrConflict
		}
		if _, err := s.authorizeGroup(tx, current, "video_generation"); err != nil {
			return err
		}
		now := nowMillis(s)
		asset := Asset{ID: id, OrgID: c.OrgID, GroupID: group.ID, MediaType: input.MediaType, Name: input.Name, ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version, Status: StatusPending, ReviewState: ReviewClear, SourcePayloadID: op.PayloadID,
			ImmutableObjectRef: media.ImmutableObjectRef, ObjectSHA256: media.ObjectSHA256, MIME: media.MIME, ByteSize: media.ByteSize, DurationMS: media.DurationMS, Width: media.Width, Height: media.Height, FPSNum: media.FPSNum, FPSDen: media.FPSDen, MetadataVersion: media.Version, VerifiedAt: media.VerifiedAt,
			CreateOperationID: op.ID, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&asset).Error; err != nil {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return AssetDTO{}, false, err
	}
	dto, err := s.GetAsset(ctx, c.OrgID, op.ResourceID)
	return dto, replayed, err
}

// Durable immutable facts do not expire with the temporary upstream fetch URL.
func hasMediaFacts(a Asset) bool {
	return validMedia(MediaEvidence{ImmutableObjectRef: a.ImmutableObjectRef, FetchURL: "https://verified.invalid/media", ObjectSHA256: a.ObjectSHA256, MIME: a.MIME, ByteSize: a.ByteSize, DurationMS: a.DurationMS, Width: a.Width, Height: a.Height, FPSNum: a.FPSNum, FPSDen: a.FPSDen, Version: a.MetadataVersion, VerifiedAt: a.VerifiedAt}, a.MediaType, a.VerifiedAt)
}

func (s *Service) ResolveBindingsTx(tx *gorm.DB, org uint64, scope Scope, taskID string, refs []ReferenceInput) ([]Binding, error) {
	if tx == nil {
		return nil, ErrInvalid
	}
	var bindings []Binding
	err := tx.Transaction(func(inner *gorm.DB) error {
		var err error
		bindings, err = s.resolveBindingsTx(inner, org, scope, taskID, refs, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// ValidateBindingsTx shares every authorization check with ResolveBindingsTx,
// but creates no references. The existing tenant row is locked before groups,
// so a caller can later persist references in the same transaction safely.
func (s *Service) ValidateBindingsTx(tx *gorm.DB, org uint64, scope Scope, refs []ReferenceInput) ([]Binding, error) {
	return s.resolveBindingsTx(tx, org, scope, "", refs, false)
}
func (s *Service) resolveBindingsTx(tx *gorm.DB, org uint64, scope Scope, taskID string, refs []ReferenceInput, persist bool) ([]Binding, error) {
	if tx == nil || org == 0 || !validScope(scope) || persist && !validID(taskID) || len(refs) > 50 {
		return nil, ErrInvalid
	}
	if persist {
		if err := lockTenant(tx, org); err != nil {
			return nil, err
		}
	} else {
		var tenant TenantLock
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", org).Take(&tenant).Error; err != nil {
			return nil, ErrNotFound
		}
	}
	// The per-org lock serializes group/asset deletion and binding across all
	// callers; stable group/asset order also makes lock intent explicit.
	inputs := append([]ReferenceInput(nil), refs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].AssetID < inputs[j].AssetID })
	result := make([]Binding, 0, len(inputs))
	seen := map[int]bool{}
	for _, ref := range inputs {
		if !validID(ref.AssetID) || ref.ContentIndex < 0 || seen[ref.ContentIndex] || !validID(ref.Role) {
			return nil, ErrInvalid
		}
		seen[ref.ContentIndex] = true
		var asset Asset
		if err := tx.Where("org_id = ? AND asset_id = ? AND deleted_at IS NULL", org, ref.AssetID).Take(&asset).Error; err != nil {
			return nil, ErrNotFound
		}
		var group Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND group_id = ? AND deleted_at IS NULL", org, asset.GroupID).Take(&group).Error; err != nil {
			return nil, ErrNotFound
		}
		if asset.Status != StatusActive || asset.ReviewState != ReviewClear || !hasMediaFacts(asset) || asset.ProviderID == nil || group.Status != StatusActive || group.ReviewState != ReviewClear || !sameScope(groupScope(group), scope) || asset.ScopeID != scope.ID || asset.ScopeVersion != scope.Version {
			return nil, ErrConflict
		}
		licenseVersion, err := s.authorizeGroup(tx, group, "video_generation")
		if err != nil {
			return nil, err
		}
		if persist {
			row := TaskReference{OrgID: org, TaskID: taskID, AssetID: asset.ID, GroupID: group.ID, ContentIndex: ref.ContentIndex, Role: ref.Role, AssetMetadataVersion: asset.MetadataVersion, AuthorizationVersion: licenseVersion, CreatedAt: nowMillis(s)}
			created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "org_id"}, {Name: "task_id"}, {Name: "content_index"}}, DoNothing: true}).Create(&row)
			if created.Error != nil {
				return nil, ErrStorage
			}
			if created.RowsAffected == 0 {
				var old TaskReference
				if err := tx.Where("org_id = ? AND task_id = ? AND content_index = ?", org, taskID, ref.ContentIndex).Take(&old).Error; err != nil {
					return nil, ErrStorage
				}
				if old.AssetID != row.AssetID || old.Role != row.Role || old.ReleasedAt != nil || old.AssetMetadataVersion != row.AssetMetadataVersion || old.AuthorizationVersion != row.AuthorizationVersion {
					return nil, ErrConflict
				}
			}
		}
		result = append(result, Binding{AssetID: asset.ID, ProviderAssetID: *asset.ProviderID, MediaType: asset.MediaType, Role: ref.Role, ContentIndex: ref.ContentIndex, DurationMS: asset.DurationMS, Width: asset.Width, Height: asset.Height, FPSNum: asset.FPSNum, FPSDen: asset.FPSDen, MetadataVersion: asset.MetadataVersion, AuthorizationVersion: licenseVersion})
	}
	return result, nil
}
func (s *Service) ReleaseBindingsTx(tx *gorm.DB, org uint64, taskID string) error {
	if tx == nil || org == 0 || !validID(taskID) {
		return ErrInvalid
	}
	if err := lockTenant(tx, org); err != nil {
		return err
	}
	if err := tx.Model(&TaskReference{}).Where("org_id = ? AND task_id = ? AND released_at IS NULL", org, taskID).Update("released_at", nowMillis(s)).Error; err != nil {
		return ErrStorage
	}
	return nil
}
