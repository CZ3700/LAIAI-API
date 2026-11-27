package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
)

var allowedArkAssetActions = map[string]bool{
	"CreateAsset":      true,
	"GetAsset":         true,
	"ListAssets":       true,
	"UpdateAsset":      true,
	"DeleteAsset":      true,
	"CreateAssetGroup": true,
	"GetAssetGroup":    true,
	"ListAssetGroups":  true,
	"UpdateAssetGroup": true,
	"DeleteAssetGroup": true,
}

func (s *Server) arkClientForNamespace(ns string) (*ark.Client, string, error) {
	cfgMap := s.loadPluginExtConfigMap(ns)
	ak := strings.TrimSpace(cfgMap["volc_access_key"])
	skRaw := strings.TrimSpace(cfgMap["volc_secret_key"])
	if ak == "" || skRaw == "" {
		return nil, "", errMissingAssetCred(ns)
	}
	sk := skRaw
	if s.keyring != nil && strings.HasPrefix(skRaw, "enc:v1:") {
		dec, err := s.keyring.Decrypt(skRaw)
		if err != nil {
			return nil, "", err
		}
		sk = dec
	}
	region := cmp.Or(strings.TrimSpace(cfgMap["volc_region"]), ark.DefaultVolcRegion)
	project := cmp.Or(strings.TrimSpace(cfgMap["volc_project_name"]), "default")
	baseOverride := cmp.Or(s.arkOpenAIOverride, strings.TrimSpace(cfgMap["volc_endpoint"]))
	cl := ark.NewClient(ark.ClientConfig{
		AccessKey:       ak,
		SecretKey:       sk,
		Region:          region,
		ProjectName:     project,
		GroupID:         strings.TrimSpace(cfgMap["volc_group_id"]),
		BaseURLOverride: baseOverride,
		ThrottleScope:   ns,
	}, s.arkHTTPClient)
	return cl, project, nil
}

type errMissingAssetCred string

func (e errMissingAssetCred) Error() string {
	return "系统未配置素材管理凭证(" + string(e) + ")"
}

func (s *Server) ensurePositiveOrgBalance(c *gin.Context, orgID uint64) bool {
	avail, err := ledger.OrgAvailable(s.db.WithContext(c.Request.Context()), orgID, ledger.AccountUserCredit)
	if err != nil {
		fail(c, http.StatusInternalServerError, "balance check failed")
		return false
	}
	if !avail.IsPositive() {
		fail(c, http.StatusPaymentRequired, "insufficient available balance")
		return false
	}
	return true
}

func (s *Server) isAdminUserID(c *gin.Context, userID uint64) bool {
	if userID == 0 {
		return false
	}
	var u identity.User
	if err := s.db.WithContext(c.Request.Context()).Select("role").First(&u, userID).Error; err != nil {
		return false
	}
	return identity.RoleRank(u.Role) >= identity.RoleRank(identity.PlatRoleAdmin)
}

func (s *Server) loadPluginExtConfigMap(pluginNS string) map[string]string {
	prefix := "plugin_cfg:" + pluginNS + ":"
	var rows []Setting
	_ = s.db.Where("name LIKE ?", prefix+"%").Find(&rows).Error
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.TrimPrefix(r.Name, prefix)] = r.Value
	}
	return out
}

func (s *Server) savePluginExtSetting(pluginNS, key, val string) error {
	name := "plugin_cfg:" + pluginNS + ":" + key
	return s.db.Save(&Setting{Name: name, Value: val}).Error
}

func apiKeyUserID(key *identity.APIKey) uint64 {
	if key == nil || key.UserID == nil {
		return 0
	}
	return *key.UserID
}

// handleArkAssetProxy handles POST /api?Action=...&Version=2024-01-01[&ns=asset_manager|asset_manager_intl].
func (s *Server) handleArkAssetProxy(c *gin.Context) {
	key, ok := s.taskService.AuthKeyFlexible(c)
	if !ok {
		return
	}
	userID := apiKeyUserID(key)
	action := cmp.Or(strings.TrimSpace(c.Query("Action")), strings.TrimSpace(c.Query("action")))
	version := cmp.Or(strings.TrimSpace(c.Query("Version")), strings.TrimSpace(c.Query("version")), ark.DefaultArkAPIVersion)
	if !allowedArkAssetActions[action] {
		fail(c, http.StatusBadRequest, "Unsupported Ark Asset Action: "+action)
		return
	}
	pluginNS := ark.PluginAssetManager
	if strings.TrimSpace(c.Query("ns")) == ark.PluginAssetManagerIntl {
		pluginNS = ark.PluginAssetManagerIntl
	}

	isAdmin := s.isAdminUserID(c, userID)
	if !isAdmin && !s.ensurePositiveOrgBalance(c, key.OrgID) {
		return
	}

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	if body == nil {
		body = map[string]any{}
	}

	client, projectName, err := s.arkClientForNamespace(pluginNS)
	if err != nil {
		s.Log().Warn("load Ark asset credentials", "namespace", pluginNS, "err", err)
		fail(c, http.StatusBadRequest, "asset credentials are unavailable")
		return
	}

	// Force configured ProjectName & clamp 64-char Name limits before forwarding.
	body["ProjectName"] = cmp.Or(projectName, "default")
	if rawName, ok := body["Name"].(string); ok {
		switch action {
		case "CreateAsset", "UpdateAsset":
			body["Name"] = ark.ClampCreateAssetName(rawName)
		case "CreateAssetGroup", "UpdateAssetGroup":
			body["Name"] = ark.ClampArkName(rawName)
		}
	}

	bodyID, _ := body["Id"].(string)
	bodyGroupID, _ := body["GroupId"].(string)
	bodyAssetType, _ := body["AssetType"].(string)
	bodyURL, _ := body["URL"].(string)
	bodyName, _ := body["Name"].(string)
	bodyDesc, _ := body["Description"].(string)

	var ownedSet map[string]struct{}
	if !isAdmin {
		switch action {
		case "GetAsset", "UpdateAsset", "DeleteAsset":
			if oerr := ark.EnsureOwnedOrClaimAssetOrGroup(s.db, "asset", bodyID, key.OrgID, userID, pluginNS); oerr != nil {
				fail(c, http.StatusForbidden, oerr.Error())
				return
			}
		case "GetAssetGroup", "UpdateAssetGroup", "DeleteAssetGroup":
			if oerr := ark.EnsureOwnedOrClaimAssetOrGroup(s.db, "group", bodyID, key.OrgID, userID, pluginNS); oerr != nil {
				fail(c, http.StatusForbidden, oerr.Error())
				return
			}
		case "CreateAsset":
			if strings.TrimSpace(bodyGroupID) == "" {
				fail(c, http.StatusBadRequest, "缺少必需参数 GroupId")
				return
			}
			if oerr := ark.EnsureOwnedOrClaimAssetOrGroup(s.db, "group", bodyGroupID, key.OrgID, userID, pluginNS); oerr != nil {
				fail(c, http.StatusForbidden, oerr.Error())
				return
			}
		case "ListAssets":
			ownedSet = s.loadOwnedIDs("plugin_assets", "asset_id", key.OrgID, pluginNS)
			if len(ownedSet) == 0 {
				c.JSON(http.StatusOK, emptyArkListEnvelope(action, version))
				return
			}
		case "ListAssetGroups":
			ownedSet = s.loadOwnedIDs("plugin_asset_groups", "group_id", key.OrgID, pluginNS)
			if len(ownedSet) == 0 {
				c.JSON(http.StatusOK, emptyArkListEnvelope(action, version))
				return
			}
		}
	}

	rawResp, status, cerr := client.CallAPI(c.Request.Context(), http.MethodPost, "ark", "", action, version, body, nil)
	if cerr != nil {
		code := status
		if code < 400 || code >= 600 {
			code = http.StatusBadGateway
		}
		s.Log().Warn("Ark asset request failed", "action", action, "status", status)
		fail(c, code, "upstream asset request failed")
		return
	}

	var parsed map[string]any
	if uerr := json.Unmarshal(rawResp, &parsed); uerr == nil {
		if ownedSet != nil {
			filterArkListEnvelope(parsed, ownedSet)
		}
		if action == "CreateAsset" {
			if aid := ark.ExtractResultField(parsed, "Id"); aid != "" {
				_ = s.db.Create(&ark.PluginAsset{
					OrgID:           key.OrgID,
					UserID:          userID,
					AssetType:       cmp.Or(strings.ToLower(bodyAssetType), "image"),
					Source:          ark.SourceAPIProxy,
					Status:          "approved",
					FileName:        ark.DeriveCreateAssetName(bodyURL, cmp.Or(bodyAssetType, "Image")),
					FileURL:         bodyURL,
					AssetID:         aid,
					GroupID:         bodyGroupID,
					Category:        "API素材",
					PluginNS:        pluginNS,
					StorageProvider: "tos",
				}).Error
			}
		} else if action == "CreateAssetGroup" {
			if gid := ark.ExtractResultField(parsed, "Id"); gid != "" {
				_ = s.db.Create(&ark.PluginAssetGroup{
					OrgID:       key.OrgID,
					UserID:      userID,
					GroupID:     gid,
					Name:        cmp.Or(bodyName, "未命名"),
					Description: bodyDesc,
					PluginNS:    pluginNS,
				}).Error
			}
		} else if action == "DeleteAsset" && strings.TrimSpace(bodyID) != "" {
			_ = s.db.Where("asset_id = ? AND org_id = ? AND plugin_ns = ? AND source = ?",
				strings.TrimSpace(bodyID), key.OrgID, pluginNS, ark.SourceAPIProxy).
				Delete(&ark.PluginAsset{}).Error
		} else if action == "DeleteAssetGroup" && strings.TrimSpace(bodyID) != "" {
			_ = s.db.Where("group_id = ? AND org_id = ? AND plugin_ns = ?",
				strings.TrimSpace(bodyID), key.OrgID, pluginNS).
				Delete(&ark.PluginAssetGroup{}).Error
		}
		c.JSON(http.StatusOK, parsed)
		return
	}

	c.Data(http.StatusOK, "application/json", rawResp)
}

func (s *Server) loadOwnedIDs(table, col string, orgID uint64, pluginNS string) map[string]struct{} {
	var vals []string
	_ = s.db.Table(table).Where("org_id = ? AND plugin_ns = ? AND "+col+" <> ''", orgID, pluginNS).Pluck(col, &vals).Error
	out := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		out[v] = struct{}{}
	}
	return out
}

func emptyArkListEnvelope(action, version string) map[string]any {
	return map[string]any{
		"ResponseMetadata": map[string]any{
			"Action":  action,
			"Version": version,
			"Service": "ark",
		},
		"Result": map[string]any{
			"Items":      []any{},
			"TotalCount": 0,
		},
	}
}

func filterArkListEnvelope(parsed map[string]any, owned map[string]struct{}) {
	res, _ := parsed["Result"].(map[string]any)
	items, _ := res["Items"].([]any)
	if items == nil {
		items, _ = res["List"].([]any)
	}
	filtered := make([]any, 0, len(items))
	for _, entry := range items {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["Id"].(string)
		if id == "" {
			id, _ = m["assetId"].(string)
		}
		if id == "" {
			id, _ = m["groupId"].(string)
		}
		if _, ok := owned[id]; ok {
			filtered = append(filtered, entry)
		}
	}
	parsed["Result"] = map[string]any{"Items": filtered, "TotalCount": len(filtered)}
}

// handleUpstreamAssetProxy handles POST /v1/plugin/:rid/assets?Action=...&Version=...
// Routing via 5-char rid in upstream_asset_bindings.
func (s *Server) handleUpstreamAssetProxy(c *gin.Context) {
	key, ok := s.taskService.AuthKeyFlexible(c)
	if !ok {
		return
	}
	userID := apiKeyUserID(key)
	rid := strings.TrimSpace(c.Param("rid"))
	action := cmp.Or(strings.TrimSpace(c.Query("Action")), strings.TrimSpace(c.Query("action")))
	version := cmp.Or(strings.TrimSpace(c.Query("Version")), strings.TrimSpace(c.Query("version")), ark.DefaultArkAPIVersion)
	if !allowedArkAssetActions[action] {
		fail(c, http.StatusBadRequest, "unsupported Ark asset action")
		return
	}
	isAdmin := s.isAdminUserID(c, userID)
	if !isAdmin && !s.ensurePositiveOrgBalance(c, key.OrgID) {
		return
	}

	var binding ark.UpstreamAssetBinding
	if err := s.db.WithContext(c.Request.Context()).Where("rid = ?", rid).First(&binding).Error; err != nil {
		fail(c, http.StatusNotFound, "找不到指定的素材上游渠道 [rid: "+rid+"]")
		return
	}
	if binding.IsActive != 1 {
		fail(c, http.StatusBadRequest, "上游素材渠道["+binding.Name+"]已被停用")
		return
	}

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "invalid JSON request body")
		return
	}
	if body == nil {
		body = map[string]any{}
	}

	ctx := c.Request.Context()
	pluginNS := ark.BindingNS(binding.ID)
	requestDB := s.db.WithContext(ctx)
	bodyID, _ := body["Id"].(string)
	bodyGroupID, _ := body["GroupId"].(string)
	var ownedSet map[string]struct{}
	if !isAdmin {
		var table, column, id string
		switch action {
		case "GetAsset", "UpdateAsset", "DeleteAsset":
			table, column, id = "plugin_assets", "asset_id", bodyID
		case "GetAssetGroup", "UpdateAssetGroup", "DeleteAssetGroup":
			if strings.TrimSpace(bodyID) == strings.TrimSpace(binding.GroupID) {
				fail(c, http.StatusForbidden, "asset group is managed by the system")
				return
			}
			table, column, id = "plugin_asset_groups", "group_id", bodyID
		case "CreateAsset":
			if strings.TrimSpace(bodyGroupID) != "" {
				table, column, id = "plugin_asset_groups", "group_id", bodyGroupID
			}
		case "ListAssets":
			ownedSet = s.loadOwnedIDs("plugin_assets", "asset_id", key.OrgID, pluginNS)
		case "ListAssetGroups":
			ownedSet = s.loadOwnedIDs("plugin_asset_groups", "group_id", key.OrgID, pluginNS)
		}
		if table != "" {
			id = strings.TrimSpace(id)
			if id == "" {
				fail(c, http.StatusBadRequest, "asset or group ID is required")
				return
			}
			var count int64
			if err := requestDB.Table(table).Where("org_id = ? AND plugin_ns = ? AND "+column+" = ?", key.OrgID, pluginNS, id).Count(&count).Error; err != nil {
				s.Log().Error("check upstream asset ownership", "binding", binding.ID, "err", err)
				fail(c, http.StatusInternalServerError, "asset ownership check failed")
				return
			}
			if count == 0 {
				fail(c, http.StatusForbidden, "asset or group is outside this organization")
				return
			}
		}
		if ownedSet != nil && len(ownedSet) == 0 {
			c.JSON(http.StatusOK, emptyArkListEnvelope(action, version))
			return
		}
	}

	var cfgRow channelConfigRow
	if err := s.db.WithContext(c.Request.Context()).First(&cfgRow, binding.ChannelConfigID).Error; err != nil {
		fail(c, http.StatusBadRequest, "绑定关联的上游渠道配置不存在")
		return
	}
	bearerKey := cfgRow.APIKeyEnc
	if s.keyring != nil && strings.HasPrefix(bearerKey, "enc:v1:") {
		dec, derr := s.keyring.Decrypt(bearerKey)
		if derr != nil {
			fail(c, http.StatusInternalServerError, "解密上游渠道密钥失败")
			return
		}
		bearerKey = dec
	}
	proxyBase := strings.TrimRight(strings.TrimSpace(cfgRow.BaseURL), "/")
	if rel := strings.TrimSpace(binding.AssetBasePath); rel != "" {
		if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
			proxyBase = strings.TrimRight(rel, "/")
		} else if u, uerr := forward.JoinUpstreamURL(proxyBase, rel); uerr == nil {
			proxyBase = strings.TrimRight(u.String(), "/")
		}
	}

	if action == "CreateAsset" {
		if strings.TrimSpace(bodyGroupID) == "" {
			if strings.TrimSpace(binding.GroupID) == "" {
				rawGroup, status, err := ark.CallUpstreamActionRaw(ctx, s.arkHTTPClient, proxyBase, bearerKey,
					"CreateAssetGroup", version, map[string]any{
						"Name":      "laiai_upstream_auto_group",
						"GroupType": "AIGC",
					}, 10*time.Millisecond)
				var group map[string]any
				if err != nil || status < 200 || status >= 300 || json.Unmarshal(rawGroup, &group) != nil {
					s.Log().Warn("create default upstream asset group", "binding", binding.ID, "status", status)
					fail(c, http.StatusBadGateway, "upstream asset group creation failed")
					return
				}
				binding.GroupID = ark.ExtractResultField(group, "Id")
				if binding.GroupID == "" {
					fail(c, http.StatusBadGateway, "upstream asset group response missing ID")
					return
				}
				if err := requestDB.Model(&ark.UpstreamAssetBinding{}).Where("id = ?", binding.ID).Update("group_id", binding.GroupID).Error; err != nil {
					s.Log().Error("save default upstream asset group", "binding", binding.ID, "err", err)
					fail(c, http.StatusInternalServerError, "could not save upstream asset group")
					return
				}
			}
			bodyGroupID = binding.GroupID
			body["GroupId"] = bodyGroupID
		}
		if rawName, ok := body["Name"].(string); ok {
			body["Name"] = ark.ClampCreateAssetName(rawName)
		} else if u, _ := body["URL"].(string); u != "" {
			at, _ := body["AssetType"].(string)
			body["Name"] = ark.DeriveCreateAssetName(u, cmp.Or(at, "Image"))
		}
	}

	rawResp, statusCode, cerr := ark.CallUpstreamActionRaw(
		c.Request.Context(), s.arkHTTPClient, proxyBase, bearerKey, action, version, body, 10*time.Millisecond,
	)
	if cerr != nil || statusCode < 200 || statusCode >= 300 {
		s.Log().Warn("upstream asset request failed", "action", action, "binding", binding.ID, "status", statusCode)
		if statusCode < 400 || statusCode >= 600 {
			statusCode = http.StatusBadGateway
		}
		fail(c, statusCode, "upstream asset request failed")
		return
	}

	var parsed map[string]any
	if err := json.Unmarshal(rawResp, &parsed); err != nil || parsed == nil {
		fail(c, http.StatusBadGateway, "invalid upstream asset response")
		return
	}
	if meta, ok := parsed["ResponseMetadata"].(map[string]any); ok && meta["Error"] != nil {
		fail(c, http.StatusBadGateway, "upstream asset request failed")
		return
	}

	if ownedSet != nil {
		filterArkListEnvelope(parsed, ownedSet)
		c.JSON(statusCode, parsed)
		return
	}

	var saveErr error
	switch action {
	case "CreateAsset":
		if aid := ark.ExtractResultField(parsed, "Id"); aid != "" {
			urlVal, _ := body["URL"].(string)
			at, _ := body["AssetType"].(string)
			saveErr = requestDB.Create(&ark.PluginAsset{
				OrgID: key.OrgID, UserID: userID, AssetID: aid, GroupID: bodyGroupID,
				AssetType: cmp.Or(strings.ToLower(at), "image"), Source: ark.SourceUpstreamRelay,
				Status: "approved", FileName: ark.DeriveCreateAssetName(urlVal, cmp.Or(at, "Image")),
				FileURL: urlVal, Category: "API", PluginNS: pluginNS, StorageProvider: "tos",
			}).Error
		}
	case "CreateAssetGroup":
		if gid := ark.ExtractResultField(parsed, "Id"); gid != "" {
			name, _ := body["Name"].(string)
			description, _ := body["Description"].(string)
			saveErr = requestDB.Create(&ark.PluginAssetGroup{
				OrgID: key.OrgID, UserID: userID, GroupID: gid, Name: name, Description: description, PluginNS: pluginNS,
			}).Error
		}
	case "DeleteAsset", "DeleteAssetGroup":
		scope := requestDB.Where("plugin_ns = ?", pluginNS)
		if !isAdmin {
			scope = scope.Where("org_id = ?", key.OrgID)
		}
		if action == "DeleteAsset" {
			saveErr = scope.Where("asset_id = ?", strings.TrimSpace(bodyID)).Delete(&ark.PluginAsset{}).Error
		} else {
			saveErr = scope.Where("group_id = ?", strings.TrimSpace(bodyID)).Delete(&ark.PluginAssetGroup{}).Error
		}
	}
	if saveErr != nil {
		s.Log().Error("persist upstream asset action", "action", action, "binding", binding.ID, "err", saveErr)
		fail(c, http.StatusInternalServerError, "could not save upstream asset changes")
		return
	}
	c.Data(statusCode, "application/json", rawResp)
}
