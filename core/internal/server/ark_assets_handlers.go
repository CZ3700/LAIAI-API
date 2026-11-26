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
		fail(c, http.StatusBadRequest, err.Error())
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
		fail(c, code, cerr.Error())
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
	res, ok := parsed["Result"].(map[string]any)
	if !ok {
		return
	}
	items, ok := res["Items"].([]any)
	if !ok {
		return
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
		if _, ok := owned[id]; ok {
			filtered = append(filtered, entry)
		}
	}
	res["Items"] = filtered
	res["TotalCount"] = len(filtered)
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
	if action == "" {
		fail(c, http.StatusBadRequest, "缺少必需参数 Action")
		return
	}
	if !s.isAdminUserID(c, userID) && !s.ensurePositiveOrgBalance(c, key.OrgID) {
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
		if rawGid, _ := body["GroupId"].(string); strings.TrimSpace(rawGid) == "" {
			if strings.TrimSpace(binding.GroupID) == "" {
				rawG, _, gerr := ark.CallUpstreamActionRaw(c.Request.Context(), s.arkHTTPClient, proxyBase, bearerKey,
					"CreateAssetGroup", version, map[string]any{
						"Name":        "laiai_upstream_auto_group",
						"Description": "由火山视频转素材ID自动创建的素材组",
						"GroupType":   "AIGC",
					}, 10*time.Millisecond)
				if gerr == nil {
					var gm map[string]any
					if json.Unmarshal(rawG, &gm) == nil {
						if newGID := ark.ExtractResultField(gm, "Id"); newGID != "" {
							binding.GroupID = newGID
							_ = s.db.Model(&ark.UpstreamAssetBinding{}).Where("id = ?", binding.ID).Update("group_id", newGID).Error
						}
					}
				}
			}
			if strings.TrimSpace(binding.GroupID) != "" {
				body["GroupId"] = binding.GroupID
			}
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
	if cerr != nil {
		fail(c, http.StatusBadGateway, cerr.Error())
		return
	}

	pluginNS := ark.BindingNS(binding.ID)
	var parsed map[string]any
	if statusCode >= 200 && statusCode < 300 && json.Unmarshal(rawResp, &parsed) == nil {
		if action == "CreateAsset" {
			if aid := ark.ExtractResultField(parsed, "Id"); aid != "" {
				urlVal, _ := body["URL"].(string)
				at, _ := body["AssetType"].(string)
				_ = s.db.Create(&ark.PluginAsset{
					OrgID:           key.OrgID,
					UserID:          userID,
					AssetType:       cmp.Or(strings.ToLower(at), "image"),
					Source:          ark.SourceUpstreamRelay,
					Status:          "approved",
					FileName:        ark.DeriveCreateAssetName(urlVal, cmp.Or(at, "Image")),
					FileURL:         urlVal,
					AssetID:         aid,
					GroupID:         binding.GroupID,
					Category:        "API转换素材",
					PluginNS:        pluginNS,
					StorageProvider: "tos",
				}).Error
			}
		} else if action == "DeleteAsset" {
			if delID, _ := body["Id"].(string); strings.TrimSpace(delID) != "" {
				_ = s.db.Where("asset_id = ? AND plugin_ns = ? AND source = ?",
					strings.TrimSpace(delID), pluginNS, ark.SourceUpstreamRelay).Delete(&ark.PluginAsset{}).Error
			}
		}
	}

	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	c.Data(statusCode, "application/json", rawResp)
}
