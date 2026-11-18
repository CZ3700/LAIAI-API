package videoasset

// 火山 Action RPC 形态的真人认证透传端点服务层（2026-11-18）：
//   POST /v1/video/?Action=CreateVisualValidateSession&Version=2024-01-01
//   POST /v1/video/?Action=GetVisualValidateResult&Version=2024-01-01
// 下游（Panify 等）按火山管理 RPC 的 URL/参数/响应形状透传，鉴权仍用平台
// Bearer Key。创建为同步调用（火山原生同步语义）：受理即返回完整
// {BytedToken, H5Link, CallbackURL}，本地落 pending 会话 + 加密 secret +
// byted_token 指纹；查询按 BytedToken 指纹反查会话并同步核实，GroupId 就绪时
// 复用认证即授权联动（建 real 组 + 自动登记许可）。两条路径的组/许可终态与
// 异步管线完全一致。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	rpcActionCreateSession = "CreateVisualValidateSession"
	rpcActionGetResult     = "GetVisualValidateResult"
	rpcVersion             = "2024-01-01"
	bytedTokenHashDomain   = "byted-token"
	rpcSessionTTL          = 30 * time.Minute
)

type rpcPortraitCreateInput struct {
	CallbackURL string
	ProjectName string
}

// parseRPCInput 接受火山 PascalCase 与平台 snake_case 两种键形。
func parseRPCInput(raw []byte) (rpcPortraitCreateInput, bool) {
	var pascal struct {
		CallbackURL *string `json:"CallbackURL"`
		ProjectName *string `json:"ProjectName"`
	}
	var snake struct {
		CallbackURL *string `json:"callback_url"`
		ProjectName *string `json:"project_name"`
	}
	if json.Unmarshal(raw, &pascal) != nil || json.Unmarshal(raw, &snake) != nil {
		return rpcPortraitCreateInput{}, false
	}
	pick := func(p, s *string) string {
		if p != nil {
			return *p
		}
		if s != nil {
			return *s
		}
		return ""
	}
	in := rpcPortraitCreateInput{CallbackURL: pick(pascal.CallbackURL, snake.CallbackURL), ProjectName: pick(pascal.ProjectName, snake.ProjectName)}
	if in.CallbackURL == "" {
		return rpcPortraitCreateInput{}, false
	}
	return in, true
}

// bytedTokenHashes 返回全部密钥版本下的指纹（轮换期按序匹配）。
func (s *Service) bytedTokenHashes(org uint64, token string) []string {
	hashes := make([]string, 0, len(s.options.HMACKeys))
	for version := range s.options.HMACKeys {
		hashes = append(hashes, s.digest(version, bytedTokenHashDomain, 0, "", token))
	}
	return hashes
}

// applyPortraitSecretTx 落 pending 会话：加密 secret（bytedToken/H5Link）、
// 写 byted_token 指纹、推进 creating → pending。创建的两条路径（异步 worker
// 与同步 RPC）共用。
func (s *Service) applyPortraitSecretTx(tx *gorm.DB, p *Portrait, bytedToken, h5Link string, expires time.Time) error {
	secretID, err := newID("psec_")
	if err != nil {
		return err
	}
	secret := portraitSecret{OrgID: p.OrgID, SessionID: p.ID, Scope: portraitScope(*p), ExpiresAt: expires, BytedToken: bytedToken, H5URL: h5Link}
	sealed, err := s.keys.Encrypt(string(bodyJSON(secret)))
	if err != nil {
		return ErrSecret
	}
	row := Secret{ID: secretID, OrgID: p.OrgID, SessionID: p.ID, ValueCipher: sealed, ExpiresAt: expires, CreatedAt: nowMillis(s)}
	if err := tx.Create(&row).Error; err != nil {
		return ErrStorage
	}
	if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", p.OrgID, p.ID).Updates(map[string]any{
		"session_status": "pending", "secret_ref": secretID, "session_expires_at": expires,
		"byted_token_hash": s.digest(s.options.ActiveHMACKey, bytedTokenHashDomain, 0, "", bytedToken),
		"row_version":      gorm.Expr("row_version + 1"), "updated_at": nowMillis(s),
	}).Error; err != nil {
		return ErrStorage
	}
	return nil
}

// readPendingSecretTx 幂等重放：解密 pending 会话的 secret 取回完整回跳三元组。
func (s *Service) readPendingSecretTx(tx *gorm.DB, org uint64, sessionID string) (bytedToken, h5Link string, err error) {
	var p Portrait
	if err = tx.Where("org_id = ? AND session_id = ?", org, sessionID).Take(&p).Error; err != nil {
		return "", "", ErrNotFound
	}
	if p.SessionStatus != "pending" || p.SecretRef == "" {
		return "", "", ErrConflict
	}
	var row Secret
	if err = tx.Where("org_id = ? AND session_id = ? AND secret_id = ? AND destroyed_at IS NULL", org, sessionID, p.SecretRef).Take(&row).Error; err != nil {
		return "", "", ErrSecret
	}
	raw, err := s.keys.Decrypt(row.ValueCipher)
	if err != nil {
		return "", "", ErrSecret
	}
	var secret portraitSecret
	if json.Unmarshal([]byte(raw), &secret) != nil || secret.SessionID != sessionID {
		return "", "", ErrSecret
	}
	return secret.BytedToken, secret.H5URL, nil
}

// SyncCreateVisualValidateSession 同步创建：事务受理 → 同步调官方 → 事务落
// pending。官方拒绝/超时时会话置 failed 并返回安全错误（火山原生行为即失败
// 后换新会话重试；本地 failed 会话不占用幂等键）。
func (s *Service) SyncCreateVisualValidateSession(ctx context.Context, c CreateContext, callbackURL string) (PortraitDTO, string, error) {
	ctx = WithCaller(ctx, c)
	if !validCreate(c) || !validHTTPS(callbackURL, 2048) {
		return PortraitDTO{}, "", ErrInvalid
	}
	if s.options.ResolveCallbackByID == nil || !validPlatformCallback(s.options.PlatformCallbackURL) {
		return PortraitDTO{}, "", ErrUnconfigured
	}
	r, err := s.resolve(ctx, c.OrgID, Scope{})
	if err != nil {
		return PortraitDTO{}, "", err
	}
	if !r.Capabilities.Portrait {
		return PortraitDTO{}, "", ErrUnsupported
	}
	// 幂等受理：同 org + 原 Key + 同幂等键的存活会话（pending/succeeded）直接返回。
	var existing Portrait
	if err := s.db.WithContext(ctx).Where("org_id = ? AND created_by_key_id = ? AND idempotency_key = ? AND session_status IN ?",
		c.OrgID, c.KeyID, c.IdempotencyKey, []string{"pending", "succeeded"}).Order("created_at DESC").Take(&existing).Error; err == nil {
		dto, err := s.GetPortraitForCaller(ctx, c, existing.ID, true)
		return dto, "", err
	}
	id, err := newID("pas_")
	if err != nil {
		return PortraitDTO{}, "", err
	}
	// 事务受理：creating 会话 + 标签 + 归属。幂等键随行落库（failed 会话不占键）。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, c.OrgID); err != nil {
			return err
		}
		p := Portrait{ID: id, OrgID: c.OrgID, OwnerUserID: c.UserID, CreatedByKeyID: c.KeyID, ScopeID: r.Scope.ID, ScopeVersion: r.Scope.Version,
			AccountID: r.Scope.AccountID, Region: r.Scope.Region, Project: r.Scope.Project, SessionStatus: "creating", LicenseStatus: "unknown",
			CallbackTargetID: "user_submitted", IdempotencyKey: c.IdempotencyKey, RowVersion: 1, CreatedAt: nowMillis(s), UpdatedAt: nowMillis(s)}
		if err := tx.Create(&p).Error; err != nil {
			return ErrStorage
		}
		if err := tx.Create(&PortraitLabel{SessionID: id, OrgID: c.OrgID}).Error; err != nil {
			return ErrStorage
		}
		return tx.Create(&Ownership{OrgID: c.OrgID, ResourceType: "portrait", ResourceID: id, OwnerUserID: c.UserID}).Error
	})
	if err != nil {
		return PortraitDTO{}, "", err
	}
	// 同步调官方：CallbackURL 直传（火山原生形态，H5 完成后直跳客户页面拼
	// bytedToken/resultCode 参数）；ReturnURL 双发兼容旧字段名。
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, rpcActionCreateSession,
		bodyJSON(map[string]any{"ProjectName": r.Scope.Project, "CallbackURL": callbackURL, "ReturnURL": callbackURL}), s.now())
	var data struct{ BytedToken, H5Link, CallbackURL string }
	providerRejected := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", c.OrgID, id).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if result.Outcome != "accepted" {
			// 失败标记必须在本次事务提交（不能借 ErrConflict 回滚掉）：
			// video_portrait_authorizations 无 safe_error_code 列（异步路径的
			// 失败详情在 operations 表，同步路径无 op），失败码只进事件留痕，
			// 客户在本次响应已收到完整安全错误信封。
			providerRejected = true
			if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND session_status = ?", c.OrgID, id, "creating").
				Updates(map[string]any{"session_status": "failed", "row_version": gorm.Expr("row_version + 1"), "updated_at": nowMillis(s)}).Error; err != nil {
				return ErrStorage
			}
			return s.event(tx, c.OrgID, "portrait", id, "", "rpc_session_failed", "", result.SafeErrorCode)
		}
		if json.Unmarshal(result.Data, &data) != nil || data.BytedToken == "" || len(data.BytedToken) > 4096 || !validHTTPS(data.H5Link, 4096) {
			return ErrInvalid
		}
		return s.applyPortraitSecretTx(tx, &p, data.BytedToken, data.H5Link, s.now().Add(rpcSessionTTL))
	})
	if err != nil {
		return PortraitDTO{}, "", err
	}
	if providerRejected {
		return PortraitDTO{}, "", providerRejectedError(result)
	}
	// 同步端点直接构造响应视图：GetPortraitForCaller/GetPortrait 的 H5 读取
	// 走 readSecret 的 owner 闸（pending+原 Key 已满足），但这里 H5Link 就在
	// 本次官方响应里，无需再解密 secret；pending DTO 的其余字段就地可得。
	expires := s.now().Add(rpcSessionTTL)
	dto := PortraitDTO{ID: id, Status: "pending", ExpiresAt: &expires, AuthorizationURL: data.H5Link}
	return dto, data.BytedToken, nil
}

// SyncGetVisualValidateResult 同步核实：按 BytedToken 指纹反查会话并调官方。
// GroupId 就绪时复用认证即授权联动（建 real 组 + 自动登记许可）；官方返回空
// GroupId 表示仍在等待（合法结果）。
func (s *Service) SyncGetVisualValidateResult(ctx context.Context, c CreateContext, bytedToken, projectName string) (groupId, sessionID string, err error) {
	ctx = WithCaller(ctx, c)
	if !validCreate(c) || bytedToken == "" || len(bytedToken) > 4096 {
		return "", "", ErrInvalid
	}
	hashes := s.bytedTokenHashes(c.OrgID, bytedToken)
	var p Portrait
	q := s.db.WithContext(ctx).Model(&Portrait{}).Where("byted_token_hash IN ?", hashes).Order("created_at DESC")
	if err := q.Take(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 非平台会话的 token（管理 AK 直建等）交由官方裁决：纯透传不建组。
			return s.passThroughValidateResult(ctx, c, bytedToken, projectName)
		}
		return "", "", ErrStorage
	}
	if p.OrgID != c.OrgID {
		// 跨组织 token 不泄露存在性：按仍在等待处理。
		return "", "", nil
	}
	switch p.SessionStatus {
	case "succeeded":
		if p.GroupID == nil {
			return "", "", ErrConflict
		}
		return *p.GroupID, p.ID, nil
	case "failed", "expired":
		return "", p.ID, nil
	}
	if p.SessionStatus != "pending" || p.SessionExpiresAt == nil || !p.SessionExpiresAt.After(s.now()) {
		return "", p.ID, nil
	}
	r, err := s.resolve(ctx, c.OrgID, portraitScope(p))
	if err != nil {
		return "", "", err
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, rpcActionGetResult,
		bodyJSON(map[string]any{"BytedToken": bytedToken, "ProjectName": r.Scope.Project}), s.now())
	if result.Outcome != "accepted" {
		return "", "", providerRejectedError(result)
	}
	var data struct{ GroupID string }
	if json.Unmarshal(result.Data, &data) != nil {
		return "", "", ErrInvalid
	}
	if data.GroupID == "" {
		return "", p.ID, nil
	}
	if !identifier.MatchString(data.GroupID) {
		return "", "", ErrInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", c.OrgID, p.ID).Take(&current).Error; err != nil {
			return err
		}
		return s.applyVisualValidateGroupTx(tx, c.OrgID, &current, data.GroupID, portraitScope(current), p.CreateOperationID, "rpc:"+c.RequestID, rpcEvidenceRef(c.RequestID), nowMillis(s), s.options.AutoLicense)
	})
	if err != nil {
		return "", "", err
	}
	return data.GroupID, p.ID, nil
}

// passThroughValidateResult 无本地会话时的纯透传（不建组、不落库）。
func (s *Service) passThroughValidateResult(ctx context.Context, c CreateContext, bytedToken, projectName string) (groupId, sessionID string, err error) {
	r, err := s.resolve(ctx, c.OrgID, Scope{})
	if err != nil {
		return "", "", err
	}
	if projectName != "" {
		r.Scope.Project = projectName
	}
	result := s.options.Client.Call(ctx, r.Scope, r.Credentials, rpcActionGetResult,
		bodyJSON(map[string]any{"BytedToken": bytedToken, "ProjectName": r.Scope.Project}), s.now())
	if result.Outcome != "accepted" {
		return "", "", providerRejectedError(result)
	}
	var data struct{ GroupID string }
	if json.Unmarshal(result.Data, &data) != nil {
		return "", "", ErrInvalid
	}
	return data.GroupID, "", nil
}

// safeClip 防御性截断（同 task.clipReason 语义）。
func safeClip(sv string, maxBytes int) string {
	if len(sv) <= maxBytes {
		return sv
	}
	return strings.ToValidUTF8(sv[:maxBytes], "")
}

func extractH5Link(data json.RawMessage) string {
	var out struct{ H5Link string }
	if json.Unmarshal(data, &out) != nil {
		return ""
	}
	return out.H5Link
}

func providerRejectedError(result Result) error {
	code := result.SafeErrorCode
	if code == "" {
		code = "provider_unavailable"
	}
	return errProviderRejected{SafeError{Code: code, Message: "The provider request did not succeed; do not reuse the result without verification.", Retryable: result.Outcome == "unknown"}}
}

func rpcEvidenceRef(requestID string) string {
	if requestID == "" {
		return ""
	}
	return "rpc:" + requestID
}

// applyVisualValidateGroupTx 是「认证即授权」的共享落地：succeeded 幂等校验、
// 建 real 组（官方 GroupId 映射为本组织 ag_ ID）、继承归属、会话推进与 secret
// 销毁、事件与自动许可登记。异步 worker（GetVisualValidateResult op）与同步
// RPC 端点共用，保证两条路径的组/许可终态完全一致。
func (s *Service) applyVisualValidateGroupTx(tx *gorm.DB, org uint64, p *Portrait, groupID string, scope Scope, createOpRef, actor, evidenceRef string, now time.Time, autoLicense bool) error {
	if p.SessionStatus == "succeeded" {
		if p.GroupID == nil {
			return ErrConflict
		}
		var existing Group
		if err := tx.Where("org_id = ? AND group_id = ?", org, *p.GroupID).Take(&existing).Error; err != nil {
			return ErrStorage
		}
		if existing.ProviderID == nil || *existing.ProviderID != groupID {
			return ErrConflict
		}
		return nil
	}
	if p.SessionStatus != "pending" || p.SessionExpiresAt == nil || !p.SessionExpiresAt.After(now) || !sameScope(portraitScope(*p), scope) {
		return ErrConflict
	}
	id, err := newID("ag_")
	if err != nil {
		return err
	}
	label := PortraitLabel{}
	_ = tx.Where("org_id = ? AND session_id = ?", org, p.ID).Take(&label).Error
	group := Group{ID: id, OrgID: org, Kind: KindReal, Name: label.Name, ScopeID: p.ScopeID, ScopeVersion: p.ScopeVersion, AccountID: p.AccountID, Region: p.Region, Project: p.Project, ProviderID: &groupID, Status: StatusActive, ReviewState: ReviewClear, PortraitID: p.ID, CreateOperationID: createOpRef, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&group).Error; err != nil {
		return ErrStorage
	}
	if err := inheritOwnerTx(tx, org, "portrait", p.ID, "asset_group", id); err != nil {
		return err
	}
	if err := saveDetails(tx, org, id, label.Description, "platform", now); err != nil {
		return err
	}
	if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND session_status = ?", org, p.ID, "pending").Updates(map[string]any{"session_status": "succeeded", "group_id": id, "license_status": "unknown", "row_version": gorm.Expr("row_version + 1"), "updated_at": now}).Error; err != nil {
		return ErrStorage
	}
	if err := tx.Model(&Secret{}).Where("org_id = ? AND secret_id = ?", org, p.SecretRef).Updates(map[string]any{"value_cipher": "", "destroyed_at": now}).Error; err != nil {
		return ErrStorage
	}
	if err := s.event(tx, org, "portrait", p.ID, p.CreateOperationID, "official_identity_verified", evidenceRef, actor); err != nil {
		return err
	}
	if autoLicense {
		if evidenceRef == "" {
			evidenceRef = actor
		}
		_ = s.verifyLicenseTx(tx, org, p.ID, LicenseEvidence{ActorID: "gateway", EvidenceRef: evidenceRef, Source: "verified_provider_license", Purpose: "video_generation", ValidFrom: now, ValidUntil: now.Add(s.options.AutoLicenseValidity), VerifiedAt: now, ReverifyDueAt: now.Add(s.options.MaxLicenseAge)})
	}
	return nil
}

// RPCDispatch 火山管理 RPC 分发：POST /v1/video/?Action=...&Version=2024-01-01
// （/v1/video/management 为等价别名）。鉴权与限流沿用平台 video 中间件。
func (s *Service) RPCDispatch(c *gin.Context) {
	if v := c.Query("Version"); v != rpcVersion {
		rpcFail(c, 400, "InvalidParameter", "Unsupported Version; expected "+rpcVersion+".")
		return
	}
	switch c.Query("Action") {
	case rpcActionCreateSession:
		s.rpcCreateVisualValidateSession(c)
	case rpcActionGetResult:
		s.rpcGetVisualValidateResult(c)
	default:
		rpcFail(c, 400, "InvalidAction", "Supported actions: CreateVisualValidateSession, GetVisualValidateResult.")
	}
}

func rpcFail(c *gin.Context, status int, code, message string) {
	c.Set("video_error", code)
	c.JSON(status, gin.H{"ResponseMetadata": gin.H{
		"RequestId": c.GetString("video_request_id"),
		"Action":    c.Query("Action"),
		"Version":   rpcVersion,
		"Service":   "ark",
		"Region":    "cn-beijing",
		"Error":     gin.H{"Code": code, "Message": message},
	}})
}

// rpcHTTPError 复用 REST 错误映射，但输出火山 RPC 错误信封。
func (s *Service) rpcHTTPError(c *gin.Context, err error) {
	var rejected errProviderRejected
	if errors.As(err, &rejected) {
		rpcFail(c, 409, rejected.Code, rejected.Message)
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		rpcFail(c, 404, "NotFound", "The resource was not found in this organization.")
	case errors.Is(err, ErrInvalid):
		rpcFail(c, 422, "InvalidParameter", "The request parameters are invalid.")
	case errors.Is(err, ErrConflict):
		rpcFail(c, 409, "invalid_state", "The resource or idempotency state conflicts with this request.")
	case errors.Is(err, ErrAuthorization):
		rpcFail(c, 403, "authorization_required", "A current verified portrait license is required.")
	case errors.Is(err, ErrUnsupported):
		rpcFail(c, 422, "unsupported_capability", "This capability is not enabled.")
	case errors.Is(err, ErrUnconfigured):
		rpcFail(c, 503, "provider_unavailable", "Portrait authorization is not configured on this gateway yet.")
	default:
		rpcFail(c, 503, "provider_unavailable", "The request could not be completed; retry with the same parameters.")
	}
}

func (s *Service) rpcCreateVisualValidateSession(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 8192))
	if err != nil {
		rpcFail(c, 400, "InvalidParameter", "The request body could not be read.")
		return
	}
	in, ok := parseRPCInput(raw)
	if !ok {
		rpcFail(c, 422, "InvalidParameter", "CallbackURL (HTTPS) is required.")
		return
	}
	dto, bytedToken, err := s.SyncCreateVisualValidateSession(c.Request.Context(), auth, in.CallbackURL)
	if err != nil {
		s.rpcHTTPError(c, err)
		return
	}
	c.JSON(200, rpcEnvelope(c, rpcActionCreateSession, gin.H{
		"BytedToken":  bytedToken,
		"H5Link":      dto.AuthorizationURL,
		"CallbackURL": in.CallbackURL,
		"SessionId":   dto.ID,
		"Status":      dto.Status,
	}))
}

func (s *Service) rpcGetVisualValidateResult(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 8192))
	if err != nil {
		rpcFail(c, 400, "InvalidParameter", "The request body could not be read.")
		return
	}
	var in struct {
		BytedToken  *string `json:"BytedToken"`
		ProjectName *string `json:"ProjectName"`
	}
	if json.Unmarshal(raw, &in) != nil || in.BytedToken == nil || *in.BytedToken == "" {
		rpcFail(c, 422, "InvalidParameter", "BytedToken is required.")
		return
	}
	projectName := ""
	if in.ProjectName != nil {
		projectName = *in.ProjectName
	}
	groupID, sessionID, err := s.SyncGetVisualValidateResult(c.Request.Context(), auth, *in.BytedToken, projectName)
	if err != nil {
		s.rpcHTTPError(c, err)
		return
	}
	result := gin.H{"GroupId": groupID}
	if sessionID != "" {
		result["SessionId"] = sessionID
	}
	c.JSON(200, rpcEnvelope(c, rpcActionGetResult, result))
}

// errProviderRejected 把 SafeError 包装成 error（SafeError 本身不实现 error）。
type errProviderRejected struct{ SafeError }

func (e errProviderRejected) Error() string { return e.Code + ": " + e.Message }
