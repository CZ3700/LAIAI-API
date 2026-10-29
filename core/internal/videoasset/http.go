package videoasset

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

// RegisterRoutes expects a group mounted at /v1/video. Its parent middleware
// authenticates and persists RequestStart/Finish, setting video_key and
// video_request_id. Operation GET remains on the root router with fallback to
// GetOperation so task and asset paths cannot register duplicate Gin routes.
func (s *Service) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/asset-groups", s.createGroupHTTP)
	group.GET("/asset-groups", s.listGroupsHTTP)
	group.GET("/asset-groups/:group_id", s.getGroupHTTP)
	group.DELETE("/asset-groups/:group_id", s.deleteGroupHTTP)
	group.POST("/assets", s.createAssetHTTP)
	group.GET("/assets", s.listAssetsHTTP)
	group.GET("/assets/:asset_id", s.getAssetHTTP)
	group.DELETE("/assets/:asset_id", s.deleteAssetHTTP)
	group.PATCH("/assets/:asset_id", s.renameAssetHTTP)
	group.PATCH("/asset-groups/:group_id", s.renameGroupHTTP)
	group.POST("/portrait-authorizations", s.createPortraitHTTP)
	group.GET("/portrait-authorizations", s.listPortraitsHTTP)
	group.GET("/portrait-authorizations/:session_id", s.getPortraitHTTP)
}
func (s *Service) auth(c *gin.Context, write bool) (CreateContext, bool) {
	raw, exists := c.Get("video_key")
	key, ok := raw.(*identity.APIKey)
	if !exists || !ok || key == nil || key.OrgID == 0 || key.ID == 0 {
		c.Set("video_error", "unauthenticated")
		c.AbortWithStatusJSON(401, gin.H{"error": SafeError{Code: "unauthenticated", Message: "Authentication is required."}, "request_id": c.GetString("video_request_id")})
		return CreateContext{}, false
	}
	allowed := false
	for _, scope := range strings.FieldsFunc(key.Scopes, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if scope == "*" || scope == "video:*" || scope == "video:write" || !write && scope == "video:read" {
			allowed = true
		}
	}
	if !allowed {
		c.Set("video_error", "forbidden")
		c.AbortWithStatusJSON(403, gin.H{"error": SafeError{Code: "forbidden", Message: "The key lacks this video permission."}, "request_id": c.GetString("video_request_id")})
		return CreateContext{}, false
	}
	context := CreateContext{OrgID: key.OrgID, KeyID: key.ID, RequestID: c.GetString("video_request_id"), IdempotencyKey: c.GetHeader("Idempotency-Key")}
	if key.UserID != nil {
		context.UserID = *key.UserID
	}
	if !validID(context.RequestID) {
		s.httpError(c, ErrStorage)
		return CreateContext{}, false
	}
	c.Request = c.Request.WithContext(WithCaller(c.Request.Context(), context))
	return context, true
}
func decodeBody(c *gin.Context, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	// Public creation DTOs are flat objects. Reject duplicate keys before typed
	// decoding so two parsers cannot disagree about the value being authorized.
	scanner := json.NewDecoder(bytes.NewReader(raw))
	token, err := scanner.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalid
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(dst)
	if typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return ErrInvalid
	}
	for i := 0; i < typ.Elem().NumField(); i++ {
		name := strings.Split(typ.Elem().Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	for scanner.More() {
		token, err := scanner.Token()
		if err != nil {
			return ErrInvalid
		}
		name, ok := token.(string)
		if !ok || seen[name] || !allowed[name] {
			return ErrInvalid
		}
		seen[name] = true
		var value json.RawMessage
		if scanner.Decode(&value) != nil {
			return ErrInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (s *Service) httpError(c *gin.Context, err error) {
	status := 500
	code := "internal_error"
	message := "The operation could not be completed."
	switch {
	case errors.Is(err, ErrInvalid):
		status = 400
		code = "invalid_request"
		message = "The request is invalid."
	case errors.Is(err, ErrNotFound):
		status = 404
		code = "not_found"
		message = "The resource was not found."
	case errors.Is(err, ErrUnsupported):
		status = 422
		code = "unsupported_capability"
		message = "This capability is not enabled."
	case errors.Is(err, ErrUnconfigured):
		status = 503
		code = "provider_unavailable"
		message = "Portrait authorization is not configured on this gateway yet."
	case errors.Is(err, ErrCallbackNotRegistered):
		status = 400
		code = "invalid_request"
		message = "callback_url must exactly match an HTTPS return address registered for this organization."
	case errors.Is(err, ErrConflict):
		status = 409
		code = "invalid_state"
		message = "The resource or idempotency state conflicts with this request."
	case errors.Is(err, ErrAuthorization):
		status = 403
		code = "authorization_required"
		message = "A current verified portrait license is required."
	case errors.Is(err, ErrSecret):
		status = 403
		code = "forbidden"
		message = "The authorization link is not available to this key."
	case errors.Is(err, ErrNotEmpty):
		status = 409
		code = "group_not_empty"
		message = "The group contains assets."
	case errors.Is(err, ErrInUse):
		status = 409
		code = "resource_in_use"
		message = "The resource is referenced by an active task."
	}
	// video_error 供父级 video 中间件落 api_request_logs.safe_error_code；
	// 此前资产网关从不设置该键，后台视频请求日志里错误码恒为空（2026-10-28 生产复现）。
	c.Set("video_error", code)
	retryable := status == 429 || status == 503
	c.AbortWithStatusJSON(status, gin.H{"error": SafeError{Code: code, Message: message, Retryable: retryable}, "request_id": c.GetString("video_request_id")})
}
func replayHeader(c *gin.Context, replayed bool) {
	c.Header("Idempotency-Replayed", strconv.FormatBool(replayed))
}

// rpcEnvelope 按 Volcengine OpenAPI 官方响应信封组装 ResponseMetadata + Result；
// RequestId 即平台请求 ID，与 X-Request-Id 头一致。
func rpcEnvelope(c *gin.Context, action string, result gin.H) gin.H {
	return gin.H{
		"ResponseMetadata": gin.H{
			"RequestId": c.GetString("video_request_id"),
			"Action":    action,
			"Version":   "2024-01-01",
			"Service":   "ark",
			"Region":    "cn-beijing",
		},
		"Result": result,
	}
}
func requireIdempotency(c *gin.Context) bool {
	// 契约（OpenAPI 评审稿）要求所有创建请求携带 Idempotency-Key；此前缺头只落
	// 笼统的 invalid_request "The request is invalid."，客户无从排查（2026-10-28 反馈）。
	// 与服务层 validCreate 同规则：非空、≤128、无控制字符。
	key := c.GetHeader("Idempotency-Key")
	if len(key) == 0 || len(key) > 128 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		c.Set("video_error", "invalid_request")
		c.AbortWithStatusJSON(400, gin.H{"error": SafeError{Code: "invalid_request", Message: "The Idempotency-Key header is required for this request.", Retryable: false}, "request_id": c.GetString("video_request_id")})
		return false
	}
	return true
}
func pageParams(c *gin.Context) (int, string, error) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, "", ErrInvalid
		}
		limit = value
	}
	return limit, c.Query("cursor"), nil
}
func pageResponse(c *gin.Context, data any, next string) {
	var cursor *string
	if next != "" {
		cursor = &next
	}
	c.JSON(200, gin.H{"data": data, "next_cursor": cursor})
}
func (s *Service) createGroupHTTP(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	if !requireIdempotency(c) {
		return
	}
	var input GroupCreate
	if err := decodeBody(c, &input); err != nil {
		s.httpError(c, err)
		return
	}
	dto, replayed, err := s.CreateGroup(c.Request.Context(), auth, input)
	if err != nil {
		s.httpError(c, err)
		return
	}
	replayHeader(c, replayed)
	c.Header("Location", "/v1/video/asset-groups/"+dto.ID)
	// 火山官方接口格式原路返回：ResponseMetadata + Result
	c.JSON(202, rpcEnvelope(c, "CreateAssetGroup", gin.H{
		"Id":     dto.ID,
		"Name":   dto.Name,
		"Status": dto.Status,
	}))
}
func (s *Service) getGroupHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	dto, err := s.GetGroup(c.Request.Context(), auth.OrgID, c.Param("group_id"))
	if err != nil {
		s.httpError(c, err)
		return
	}
	// 火山官方接口格式原路返回
	c.JSON(200, rpcEnvelope(c, "GetAssetGroup", gin.H{
		"Id":         dto.ID,
		"Name":       dto.Name,
		"Status":     dto.Status,
		"AssetCount": dto.AssetCount,
		// Version 是平台扩展字段（官方无此字段）：PATCH expected_version 乐观锁的唯一读取来源。
		"Version":   dto.Version,
		"CreatedAt": dto.CreatedAt.Unix(),
		"UpdatedAt": dto.UpdatedAt.Unix(),
	}))
}
func (s *Service) listGroupsHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	limit, cursor, err := pageParams(c)
	if err != nil {
		s.httpError(c, err)
		return
	}
	data, next, err := s.ListGroups(c.Request.Context(), auth.OrgID, limit, cursor)
	if err != nil {
		s.httpError(c, err)
		return
	}
	items := make([]gin.H, 0, len(data))
	for _, g := range data {
		items = append(items, gin.H{
			"Id":         g.ID,
			"Name":       g.Name,
			"Status":     g.Status,
			"AssetCount": g.AssetCount,
		})
	}
	c.JSON(200, rpcEnvelope(c, "ListAssetGroups", gin.H{
		"Items":      items,
		"NextCursor": next,
	}))
}
func (s *Service) deleteGroupHTTP(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	cascade := false
	if raw := c.Query("cascade"); raw != "" {
		if raw != "true" && raw != "false" {
			s.httpError(c, ErrInvalid)
			return
		}
		cascade = raw == "true"
	}
	dto, done, err := s.DeleteGroup(c.Request.Context(), auth, c.Param("group_id"), cascade)
	if err != nil {
		s.httpError(c, err)
		return
	}
	if done {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(202, rpcEnvelope(c, "DeleteAssetGroup", gin.H{
		"Id":     dto.ID,
		"Status": "deleting",
	}))
}
func (s *Service) createAssetHTTP(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	if !requireIdempotency(c) {
		return
	}
	var input AssetCreate
	if err := decodeBody(c, &input); err != nil {
		s.httpError(c, err)
		return
	}
	dto, replayed, err := s.CreateAsset(c.Request.Context(), auth, input)
	if err != nil {
		s.httpError(c, err)
		return
	}
	replayHeader(c, replayed)
	c.Header("Location", "/v1/video/assets/"+dto.ID)
	// 火山官方接口格式原路返回
	c.JSON(202, rpcEnvelope(c, "CreateAsset", gin.H{
		"Id":        dto.ID,
		"GroupId":   dto.GroupID,
		"AssetType": dto.MediaType,
		"Status":    dto.Status,
	}))
}
func (s *Service) getAssetHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	dto, err := s.GetAsset(c.Request.Context(), auth.OrgID, c.Param("asset_id"))
	if err != nil {
		s.httpError(c, err)
		return
	}
	c.JSON(200, rpcEnvelope(c, "GetAsset", gin.H{
		"Id":        dto.ID,
		"GroupId":   dto.GroupID,
		"AssetType": dto.MediaType,
		"Status":    dto.Status,
		"URL":       dto.Reference,
		// Version 是平台扩展字段（官方无此字段）：PATCH expected_version 乐观锁的唯一读取来源。
		"Version":   dto.Version,
		"CreatedAt": dto.CreatedAt.Unix(),
		"UpdatedAt": dto.UpdatedAt.Unix(),
	}))
}
func (s *Service) listAssetsHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	limit, cursor, err := pageParams(c)
	if err != nil {
		s.httpError(c, err)
		return
	}
	data, next, err := s.ListAssets(c.Request.Context(), auth.OrgID, c.Query("group_id"), limit, cursor)
	if err != nil {
		s.httpError(c, err)
		return
	}
	items := make([]gin.H, 0, len(data))
	for _, a := range data {
		items = append(items, gin.H{
			"Id":        a.ID,
			"GroupId":   a.GroupID,
			"AssetType": a.MediaType,
			"Status":    a.Status,
		})
	}
	c.JSON(200, rpcEnvelope(c, "ListAssets", gin.H{
		"Items":      items,
		"NextCursor": next,
	}))
}
func (s *Service) deleteAssetHTTP(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	dto, done, err := s.DeleteAsset(c.Request.Context(), auth, c.Param("asset_id"))
	if err != nil {
		s.httpError(c, err)
		return
	}
	if done {
		c.Status(204)
		return
	}
	c.JSON(202, rpcEnvelope(c, "DeleteAsset", gin.H{
		"Id":     dto.ID,
		"Status": "deleting",
	}))
}
func (s *Service) createPortraitHTTP(c *gin.Context) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	if !requireIdempotency(c) {
		return
	}
	var input PortraitCreate
	if err := decodeBody(c, &input); err != nil {
		s.httpError(c, err)
		return
	}
	dto, replayed, err := s.CreatePortrait(c.Request.Context(), auth, input)
	if err != nil {
		s.httpError(c, err)
		return
	}
	replayHeader(c, replayed)
	c.Header("Location", "/v1/video/portrait-authorizations/"+dto.ID)
	// 火山官方接口格式原路返回
	c.JSON(202, rpcEnvelope(c, "CreateVisualValidateSession", gin.H{
		"SessionId": dto.ID,
		"Status":    dto.Status,
	}))
}
func (s *Service) getPortraitHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	dto, err := s.GetPortrait(c.Request.Context(), auth.OrgID, c.Param("session_id"), auth.KeyID, true)
	if err != nil {
		s.httpError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	// 火山官方接口格式原路返回
	result := gin.H{
		"SessionId": dto.ID,
		"Status":    dto.Status,
		"CreatedAt": dto.CreatedAt.Unix(),
	}
	if dto.GroupID != nil {
		result["GroupId"] = *dto.GroupID
	}
	if dto.Error != nil {
		result["Error"] = dto.Error
		result["CreateRequestId"] = dto.RequestID
		result["UpstreamRequestId"] = dto.UpstreamRequestID
		result["ProviderHttpStatus"] = dto.ProviderHTTPStatus
	}
	if dto.AuthorizationURL != "" {
		result["H5URL"] = dto.AuthorizationURL
	}
	c.JSON(200, rpcEnvelope(c, "GetVisualValidateResult", result))
}
func (s *Service) listPortraitsHTTP(c *gin.Context) {
	auth, ok := s.auth(c, false)
	if !ok {
		return
	}
	limit, cursor, err := pageParams(c)
	if err != nil {
		s.httpError(c, err)
		return
	}
	data, next, err := s.ListPortraits(c.Request.Context(), auth.OrgID, limit, cursor)
	if err != nil {
		s.httpError(c, err)
		return
	}
	pageResponse(c, data, next)
}

func (s *Service) renameAssetHTTP(c *gin.Context) { s.renameHTTP(c, false) }
func (s *Service) renameGroupHTTP(c *gin.Context) { s.renameHTTP(c, true) }
func (s *Service) renameHTTP(c *gin.Context, group bool) {
	auth, ok := s.auth(c, true)
	if !ok {
		return
	}
	var in Rename
	if decodeBody(c, &in) != nil {
		s.httpError(c, ErrInvalid)
		return
	}
	id := c.Param("asset_id")
	if group {
		id = c.Param("group_id")
	}
	dto, err := s.RenameResource(c.Request.Context(), auth, id, group, in)
	if err != nil {
		s.httpError(c, err)
		return
	}
	c.JSON(202, dto)
}
