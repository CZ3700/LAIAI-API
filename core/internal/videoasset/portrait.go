package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type portraitSecret struct {
	OrgID             uint64
	SessionID         string
	Scope             Scope
	ExpiresAt         time.Time
	BytedToken, H5URL string
}

func (s *Service) CreatePortrait(ctx context.Context, c CreateContext, input PortraitCreate) (PortraitDTO, bool, error) {
	ctx = WithCaller(ctx, c)
	// 平台侧配置缺失与客户请求格式错误必须分流：混在同一个 invalid_request
	// 里会把"网关没配好"伪装成"客户请求不合法"（2026-10-28 两个生产 400 的根因）。
	if s.options.ResolveCallback == nil || s.options.ResolveCallbackByID == nil || !validPlatformCallback(s.options.PlatformCallbackURL) {
		return PortraitDTO{}, false, ErrUnconfigured
	}
	if !validCreate(c) || !safeName(input.Name, 64, false) || !safeName(input.Description, 300, false) || !validHTTPS(input.CallbackURL, 2048) {
		return PortraitDTO{}, false, ErrInvalid
	}
	if old, found, err := s.replay(s.db.WithContext(ctx), c, "/v1/video/portrait-authorizations", bodyJSON(input)); err != nil {
		return PortraitDTO{}, false, err
	} else if found {
		dto, err := s.GetPortraitForCaller(ctx, c, old.ResourceID, true)
		return dto, true, err
	}
	r, err := s.resolve(ctx, c.OrgID, Scope{})
	if err != nil {
		return PortraitDTO{}, false, err
	}
	if !r.Capabilities.Portrait {
		return PortraitDTO{}, false, ErrUnsupported
	}
	target, err := s.options.ResolveCallback(ctx, c.OrgID, input.CallbackURL)
	if err != nil || !validID(target.ID) || target.URL != input.CallbackURL {
		return PortraitDTO{}, false, ErrCallbackNotRegistered
	}
	id, err := newID("pas_")
	if err != nil {
		return PortraitDTO{}, false, err
	}
	// The secret callback state is bound at dispatch and never copied into the
	// execution capsule. Client return URLs are not sent to the provider.
	provider := bodyJSON(map[string]any{"ProjectName": r.Scope.Project})
	op, replayed, err := s.accept(ctx, c, r, "/v1/video/portrait-authorizations", "CreateVisualValidateSession", "portrait", id, bodyJSON(input), provider, func(tx *gorm.DB, op Operation) error {
		now := nowMillis(s)
		p := Portrait{ID: id, OrgID: c.OrgID, OwnerUserID: c.UserID, CreatedByKeyID: c.KeyID, ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version, AccountID: r.Scope.AccountID, Region: r.Scope.Region, Project: r.Scope.Project,
			SessionStatus: "creating", LicenseStatus: "unknown", CreateOperationID: op.ID, CallbackTargetID: target.ID, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&p).Error; err != nil {
			return ErrStorage
		}
		if err := tx.Create(&PortraitLabel{SessionID: id, OrgID: c.OrgID, Name: input.Name, Description: input.Description}).Error; err != nil {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return PortraitDTO{}, false, err
	}
	dto, err := s.GetPortraitForCaller(ctx, c, op.ResourceID, true)
	return dto, replayed, err
}

// readSecret runs in a short transaction. Its audit event must commit before
// any secret is returned. The purpose is restricted to worker checks or the
// original key's H5 handoff; generic history readers cannot access this store.
func (s *Service) readSecret(ctx context.Context, p Portrait, actor string, ownerKey uint64, worker bool) (portraitSecret, error) {
	var secret portraitSecret
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, p.OrgID); err != nil {
			return err
		}
		var current Portrait
		if err := tx.Where("org_id = ? AND session_id = ?", p.OrgID, p.ID).Take(&current).Error; err != nil {
			return ErrNotFound
		}
		allowed := worker || current.CreatedByKeyID == ownerKey
		if caller, present := callerFrom(ctx); present && !worker {
			allowed = validCaller(caller, p.OrgID) && ((caller.UserID > 0 && caller.UserID == current.OwnerUserID) || (caller.UserID == 0 && current.OwnerUserID == 0 && caller.KeyID > 0 && caller.KeyID == current.CreatedByKeyID))
		}
		if current.SessionStatus != "pending" || current.SessionExpiresAt == nil || !current.SessionExpiresAt.After(s.now()) || !allowed {
			return ErrSecret
		}
		var row Secret
		if err := tx.Where("org_id = ? AND session_id = ? AND secret_id = ? AND destroyed_at IS NULL", p.OrgID, p.ID, current.SecretRef).Take(&row).Error; err != nil {
			return ErrSecret
		}
		if !row.ExpiresAt.After(s.now()) {
			return ErrSecret
		}
		raw, err := s.keys.Decrypt(row.ValueCipher)
		if err != nil {
			return ErrSecret
		}
		if json.Unmarshal([]byte(raw), &secret) != nil || secret.OrgID != p.OrgID || secret.SessionID != p.ID || !sameScope(secret.Scope, portraitScope(current)) || !secret.ExpiresAt.Equal(row.ExpiresAt) || !secret.ExpiresAt.After(s.now()) {
			secret = portraitSecret{}
			return ErrSecret
		}
		kind := "h5_url_read"
		if worker {
			kind = "portrait_result_check"
		}
		return s.event(tx, p.OrgID, "portrait", p.ID, current.CreateOperationID, kind, row.ID, actor)
	})
	if err != nil {
		return portraitSecret{}, err
	}
	if !secret.ExpiresAt.After(s.now()) {
		return portraitSecret{}, ErrSecret
	}
	return secret, nil
}

func (s *Service) GetPortrait(ctx context.Context, org uint64, id string, keyID uint64, withURL bool) (PortraitDTO, error) {
	if org == 0 || !validID(id) {
		return PortraitDTO{}, ErrInvalid
	}
	var p Portrait
	if err := owned(ctx, s.db.WithContext(ctx), org, "portrait", "session_id").Where("org_id = ? AND session_id = ?", org, id).Take(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PortraitDTO{}, ErrNotFound
		}
		return PortraitDTO{}, ErrStorage
	}
	dto := PortraitDTO{ID: p.ID, Status: p.SessionStatus, ExpiresAt: p.SessionExpiresAt, CreatedAt: p.CreatedAt}
	if p.SessionStatus == "failed" || p.SessionStatus == "submission_unknown" {
		var operation Operation
		err := s.db.WithContext(ctx).Where("org_id = ? AND operation_id = ? AND resource_type = ? AND resource_id = ? AND action = ?", org, p.CreateOperationID, "portrait", p.ID, "CreateVisualValidateSession").Take(&operation).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return PortraitDTO{}, ErrStorage
		}
		if err == nil {
			dto.Error = safeOperationError(operation.SafeErrorCode)
			if validID(operation.RequestID) {
				dto.RequestID = operation.RequestID
			}
			if identifier.MatchString(operation.UpstreamRequestID) {
				dto.UpstreamRequestID = operation.UpstreamRequestID
			}
			dto.ProviderHTTPStatus = operation.HTTPStatus
		}
	}
	if p.SessionStatus == "succeeded" {
		dto.GroupID = p.GroupID
	}
	if p.SessionStatus == "pending" && p.SessionExpiresAt != nil && !p.SessionExpiresAt.After(s.now()) {
		dto.Status = "expired"
	}
	if withURL && dto.Status == "pending" {
		secret, err := s.readSecret(ctx, p, "gateway", keyID, false)
		if err != nil {
			return PortraitDTO{}, err
		}
		dto.AuthorizationURL = secret.H5URL
	}
	return dto, nil
}
func (s *Service) ListPortraits(ctx context.Context, org uint64, limit int, cursor string) ([]PortraitDTO, string, error) {
	if org == 0 || limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	kind := actorCursor(ctx, "portraits")
	after, err := s.parseCursor(org, kind, cursor)
	if err != nil {
		return nil, "", err
	}
	q := owned(ctx, s.db.WithContext(ctx), org, "portrait", "session_id").Where("org_id = ?", org)
	if after != "" {
		q = q.Where("session_id > ?", after)
	}
	var rows []Portrait
	if err := q.Order("session_id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", ErrStorage
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = s.cursor(org, kind, rows[len(rows)-1].ID)
	}
	result := make([]PortraitDTO, 0, len(rows))
	for _, row := range rows {
		dto, err := s.GetPortrait(ctx, org, row.ID, 0, false)
		if err != nil {
			return nil, "", err
		}
		result = append(result, dto)
	}
	return result, next, nil
}

func (s *Service) expireSecrets(ctx context.Context) error {
	now := nowMillis(s)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Never rewrite succeeded authentication when the separate license expires.
		if err := tx.Model(&Portrait{}).Where("session_status = ? AND session_expires_at <= ?", "pending", now).Updates(map[string]any{"session_status": "expired", "updated_at": now}).Error; err != nil {
			return ErrStorage
		}
		if err := tx.Model(&Secret{}).Where("expires_at <= ? AND destroyed_at IS NULL", now).Updates(map[string]any{"value_cipher": "", "destroyed_at": now}).Error; err != nil {
			return ErrStorage
		}
		return nil
	})
}

func (s *Service) applyPortraitCreated(tx *gorm.DB, op Operation, result Result, request []byte) error {
	var data struct{ BytedToken, H5Link, CallbackURL string }
	var original struct{ CallbackURL string }
	if json.Unmarshal(result.Data, &data) != nil || json.Unmarshal(request, &original) != nil || data.BytedToken == "" || len(data.BytedToken) > 4096 || !validHTTPS(data.H5Link, 4096) || data.CallbackURL != "" && data.CallbackURL != original.CallbackURL {
		return ErrInvalid
	}
	var p Portrait
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Take(&p).Error; err != nil {
		return ErrNotFound
	}
	if p.SessionStatus != "creating" && p.SessionStatus != "submission_unknown" {
		return ErrConflict
	}
	// Claim timestamp precedes official issuance, avoiding an optimistic expiry.
	expires := op.UpdatedAt.Add(30 * time.Minute)
	if !expires.After(s.now()) {
		return ErrSecret
	}
	secretID, err := newID("psec_")
	if err != nil {
		return err
	}
	secret := portraitSecret{OrgID: op.OrgID, SessionID: p.ID, Scope: opScope(op), ExpiresAt: expires, BytedToken: data.BytedToken, H5URL: data.H5Link}
	sealed, err := s.keys.Encrypt(string(bodyJSON(secret)))
	if err != nil {
		return ErrSecret
	}
	row := Secret{ID: secretID, OrgID: op.OrgID, SessionID: p.ID, ValueCipher: sealed, ExpiresAt: expires, CreatedAt: nowMillis(s)}
	if err := tx.Create(&row).Error; err != nil {
		return ErrStorage
	}
	if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", op.OrgID, p.ID).Updates(map[string]any{"session_status": "pending", "secret_ref": secretID, "session_expires_at": expires, "row_version": gorm.Expr("row_version + 1"), "updated_at": nowMillis(s)}).Error; err != nil {
		return ErrStorage
	}
	return nil
}
