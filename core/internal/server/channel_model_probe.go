package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/forward"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/server/v1compat"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type probedUpstreamModelDTO struct {
	ModelID                string   `json:"model_id"`
	Name                   string   `json:"name"`
	VendorName             string   `json:"vendor_name,omitempty"`
	ProviderID             uint64   `json:"provider_id"`
	ProviderName           string   `json:"provider_name"`
	TypeID                 uint64   `json:"type_id"`
	TypeName               string   `json:"type_name"`
	DefaultFeatures        []string `json:"default_features"`
	SuggestedBillingRuleID uint64   `json:"suggested_billing_rule_id"`
	SuggestedForwardRuleID uint64   `json:"suggested_forward_rule_id"`
	InputUSDPer1M          float64  `json:"input_usd_per_1m,omitempty"`
	OutputUSDPer1M         float64  `json:"output_usd_per_1m,omitempty"`
	FixedUSD               float64  `json:"fixed_usd,omitempty"`
	Exists                 bool     `json:"exists"`
	ExistingMid            string   `json:"existing_mid,omitempty"`
}

func (s *Server) matchDefaultRulesForModel(ctx context.Context, modelID, typeName string, billingRules []pricing.BillingRule, forwardRules []forward.ForwardRule) (uint64, uint64) {
	lowerModel := strings.ToLower(strings.TrimSpace(modelID))

	findBillingByPID := func(pid string) uint64 {
		for _, r := range billingRules {
			if r.IsActive == 1 && r.PID == pid {
				return r.ID
			}
		}
		return 0
	}
	findBillingByType := func(bType string) uint64 {
		for _, r := range billingRules {
			if r.IsActive == 1 && r.BillingType == bType {
				return r.ID
			}
		}
		return 0
	}
	findForwardByEID := func(eid string) uint64 {
		for _, r := range forwardRules {
			if r.IsActive == 1 && r.EID == eid {
				return r.ID
			}
		}
		return 0
	}
	findForwardByCategory := func(cat string) uint64 {
		for _, r := range forwardRules {
			if r.IsActive == 1 && (r.Category == cat || strings.Contains(strings.ToLower(r.Name), strings.ToLower(cat))) {
				return r.ID
			}
		}
		return 0
	}

	var bID, fID uint64
	switch typeName {
	case "视频":
		switch {
		case strings.Contains(lowerModel, "seedance"):
			bID = cmp.Or(findBillingByPID("preset-seedance-video"), findBillingByType(pricing.BillingTypeDuration))
			fID = cmp.Or(findForwardByEID("seedance-video"), findForwardByCategory("视频"))
		case strings.Contains(lowerModel, "kling"):
			bID = cmp.Or(findBillingByPID("preset-kling-video"), findBillingByType(pricing.BillingTypeDuration))
			fID = cmp.Or(findForwardByEID("kling-video"), findForwardByCategory("视频"))
		case strings.Contains(lowerModel, "vidu"):
			bID = findBillingByType(pricing.BillingTypeDuration)
			fID = cmp.Or(findForwardByEID("vidu-video"), findForwardByCategory("视频"))
		case strings.Contains(lowerModel, "wan"):
			bID = findBillingByType(pricing.BillingTypeDuration)
			fID = cmp.Or(findForwardByEID("wan-video"), findForwardByCategory("视频"))
		default:
			bID = findBillingByType(pricing.BillingTypeDuration)
			fID = findForwardByCategory("视频")
		}
	case "图片":
		bID = cmp.Or(findBillingByPID("preset-per-image"), findBillingByType(pricing.BillingTypeRequests))
		if strings.Contains(lowerModel, "seedream") {
			fID = cmp.Or(findForwardByEID("volc-image"), findForwardByCategory("图片"))
		} else {
			fID = cmp.Or(findForwardByEID("openai-images"), findForwardByCategory("图片"))
		}
	case "音频":
		bID = cmp.Or(findBillingByPID("preset-tts-chars"), findBillingByType(pricing.BillingTypeRequests))
		fID = cmp.Or(findForwardByEID("openai-audio-speech"), findForwardByCategory("音频"))
	case "向量":
		bID = findBillingByType(pricing.BillingTypeTokens)
		fID = cmp.Or(findForwardByEID("openai-embeddings"), findForwardByCategory("向量"))
	case "排序":
		bID = findBillingByType(pricing.BillingTypeRequests)
		fID = cmp.Or(findForwardByEID("rerank-v1"), findForwardByCategory("排序"))
	case "封装":
		bID = findBillingByType(pricing.BillingTypeRequests)
		fID = cmp.Or(findForwardByCategory("封装"), findForwardByCategory("视频"))
	default: // 聊天
		if strings.Contains(lowerModel, "claude") {
			bID = cmp.Or(findBillingByPID("preset-claude-cache"), findBillingByType(pricing.BillingTypeTokens))
			fID = cmp.Or(findForwardByEID("anthropic-messages"), findForwardByCategory("聊天"))
		} else if strings.Contains(lowerModel, "gemini") {
			bID = findBillingByType(pricing.BillingTypeTokens)
			fID = cmp.Or(findForwardByEID("gemini-generate"), findForwardByCategory("聊天"))
		} else {
			bID = cmp.Or(findBillingByPID("preset-openai-tokens"), findBillingByType(pricing.BillingTypeTokens))
			fID = cmp.Or(findForwardByEID("openai-chat"), findForwardByCategory("聊天"))
		}
	}
	if bID == 0 && len(billingRules) > 0 {
		bID = billingRules[0].ID
	}
	if fID == 0 && len(forwardRules) > 0 {
		fID = forwardRules[0].ID
	}
	return bID, fID
}

type probeTargetSpec struct {
	BaseURL      string
	APIKey       string
	ProviderType string
	System       string
}

func (s *Server) resolveProbeTargets(ctx context.Context, req struct {
	ChannelID     uint64   `json:"channel_id"`
	PresetID      *uint64  `json:"preset_id"`
	SubChannelIDs []uint64 `json:"sub_channel_ids"`
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key"`
	ProviderType  string   `json:"provider_type"`
}) []probeTargetSpec {
	savedTargets := make([]probeTargetSpec, 0, 4)
	if req.PresetID != nil && *req.PresetID > 0 {
		var row channelConfigRow
		if err := s.db.WithContext(ctx).First(&row, *req.PresetID).Error; err == nil && strings.TrimSpace(row.BaseURL) != "" {
			savedTargets = append(savedTargets, probeTargetSpec{
				BaseURL:      row.BaseURL,
				APIKey:       s.decryptConfigSecret(row.APIKeyEnc),
				ProviderType: cmp.Or(row.ProviderType, "openai"),
				System:       row.UpstreamSystem,
			})
		}
	}
	if len(req.SubChannelIDs) > 0 {
		var rows []channelConfigRow
		if err := s.db.WithContext(ctx).Where("id IN ?", req.SubChannelIDs).Order("priority DESC, id ASC").Find(&rows).Error; err == nil {
			for _, row := range rows {
				if strings.TrimSpace(row.BaseURL) != "" {
					savedTargets = append(savedTargets, probeTargetSpec{
						BaseURL:      row.BaseURL,
						APIKey:       s.decryptConfigSecret(row.APIKeyEnc),
						ProviderType: cmp.Or(row.ProviderType, "openai"),
						System:       row.UpstreamSystem,
					})
				}
			}
		}
	}
	if len(savedTargets) == 0 && req.ChannelID > 0 {
		var ch relay.Channel
		if err := s.db.WithContext(ctx).First(&ch, req.ChannelID).Error; err == nil {
			meta := s.loadV1ChannelMetaMap()[ch.ID]
			primary, subs := s.resolveV1SubChannels(meta.PresetID, meta.Config)
			for _, sub := range subs {
				if strings.TrimSpace(sub.BaseURL) != "" {
					savedTargets = append(savedTargets, probeTargetSpec{
						BaseURL:      sub.BaseURL,
						APIKey:       s.decryptConfigSecret(sub.APIKeyEnc),
						ProviderType: cmp.Or(sub.ProviderType, ch.Provider),
						System:       sub.UpstreamSystem,
					})
				}
			}
			if len(savedTargets) == 0 && primary != nil && strings.TrimSpace(primary.BaseURL) != "" {
				savedTargets = append(savedTargets, probeTargetSpec{
					BaseURL:      primary.BaseURL,
					APIKey:       s.decryptConfigSecret(primary.APIKeyEnc),
					ProviderType: cmp.Or(primary.ProviderType, ch.Provider),
					System:       primary.UpstreamSystem,
				})
			}
			if len(savedTargets) == 0 && strings.TrimSpace(ch.BaseURL) != "" {
				savedTargets = append(savedTargets, probeTargetSpec{
					BaseURL:      ch.BaseURL,
					APIKey:       s.decryptConfigSecret(ch.CredentialEnc),
					ProviderType: ch.Provider,
				})
			}
		}
	}

	customBase := strings.TrimSpace(req.BaseURL)
	customKey := strings.TrimSpace(req.APIKey)
	if isMaskedSecret(customKey) || customKey == "placeholder-key" {
		customKey = ""
	}
	if customKey != "" {
		base := customBase
		if base == "" && len(savedTargets) > 0 {
			base = savedTargets[0].BaseURL
		}
		if base != "" {
			return []probeTargetSpec{{
				BaseURL:      base,
				APIKey:       customKey,
				ProviderType: cmp.Or(strings.TrimSpace(req.ProviderType), "openai"),
			}}
		}
	}
	if len(savedTargets) > 0 {
		if customBase != "" && (len(savedTargets) == 1 || NormalizeNewAPIOrigin(customBase) != NormalizeNewAPIOrigin(savedTargets[0].BaseURL)) {
			savedTargets[0].BaseURL = customBase
		}
		return savedTargets
	}
	if customBase != "" {
		return []probeTargetSpec{{
			BaseURL:      customBase,
			APIKey:       "",
			ProviderType: cmp.Or(strings.TrimSpace(req.ProviderType), "openai"),
		}}
	}
	return nil
}

func detectedModelsSource(detectedURL string) string {
	u := strings.ToLower(strings.TrimSpace(detectedURL))
	switch {
	case strings.HasSuffix(u, "/api/v1"):
		return "/api/v1/models"
	case strings.HasSuffix(u, "/api/v3"):
		return "/api/v3/models"
	case strings.HasSuffix(u, "/v1beta"):
		return "/v1beta/models"
	case strings.HasSuffix(u, "/openai/v1"):
		return "/openai/v1/models"
	case strings.HasSuffix(u, "/v1"):
		return "/v1/models"
	default:
		return "/models"
	}
}

func (s *Server) v1ProbeChannelModels(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		ChannelID     uint64   `json:"channel_id"`
		PresetID      *uint64  `json:"preset_id"`
		SubChannelIDs []uint64 `json:"sub_channel_ids"`
		BaseURL       string   `json:"base_url"`
		APIKey        string   `json:"api_key"`
		ProviderType  string   `json:"provider_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid probe request")
		return
	}
	targets := s.resolveProbeTargets(ctx, req)
	if len(targets) == 0 {
		fail(c, http.StatusBadRequest, "请先选择上游渠道或填写端点基础地址 (Base URL)")
		return
	}

	discoveredOrder := make([]string, 0, 64)
	vendorHints := map[string]string{}
	seen := map[string]bool{}
	detectedURL := ""
	var sources []string
	var lastErr string

	for _, target := range targets {
		if relay.IsMetadataURL(target.BaseURL) {
			lastErr = "禁止探测内网元数据地址"
			continue
		}
		probeKey := strings.TrimSpace(target.APIKey)
		if probeKey == "placeholder-key" {
			probeKey = ""
		}
		pr := s.relay.ProbeChannel(ctx, probeKey, target.BaseURL, mapTkeProviderToRelay(target.ProviderType), s.upstreamHTTP == nil)
		if pr != nil && pr.OK && len(pr.Models) > 0 {
			if detectedURL == "" {
				detectedURL = pr.DetectedBaseURL + "/models"
			}
			sources = append(sources, detectedModelsSource(pr.DetectedBaseURL))
			for _, m := range pr.Models {
				m = strings.TrimSpace(m)
				if m == "" {
					continue
				}
				if !seen[m] {
					seen[m] = true
					discoveredOrder = append(discoveredOrder, m)
				}
				if owner := strings.TrimSpace(pr.ModelOwners[m]); owner != "" && vendorHints[m] == "" {
					vendorHints[m] = owner
				}
			}
		} else if pr != nil && pr.Message != "" {
			lastErr = pr.Message
		}
	}

	if len(discoveredOrder) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success":        false,
			"message":        cmp.Or(lastErr, "未能通过 /v1/models 探测到可用模型，请检查 Base URL 与该渠道 API Key 权限"),
			"data":           []probedUpstreamModelDTO{},
			"total":          0,
			"new_count":      0,
			"existing_count": 0,
		})
		return
	}

	_ = v1compat.EnsurePresetRules(ctx, s.db)
	var billingRules []pricing.BillingRule
	_ = s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&billingRules).Error
	var forwardRules []forward.ForwardRule
	_ = s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&forwardRules).Error

	existingMap := map[string]string{}
	extMap := s.loadV1ModelExtMap()
	var bindings []pricing.ModelRuleBinding
	_ = s.readOnly().WithContext(ctx).Select("model").Find(&bindings).Error
	for _, b := range bindings {
		mid := cmp.Or(extMap[b.Model].Mid, "m-"+strconv.FormatUint(modelNumericID(b.Model), 10))
		existingMap[b.Model] = mid
	}
	var prices []relay.ModelPrice
	_ = s.readOnly().WithContext(ctx).Select("model").Find(&prices).Error
	for _, p := range prices {
		if _, ok := existingMap[p.Model]; !ok {
			mid := cmp.Or(extMap[p.Model].Mid, "m-"+strconv.FormatUint(modelNumericID(p.Model), 10))
			existingMap[p.Model] = mid
		}
	}

	items := make([]probedUpstreamModelDTO, 0, len(discoveredOrder))
	var newCount, existingCount int
	for _, m := range discoveredOrder {
		vName := vendorHints[m]
		pID, pName, tID, tName, defFeatJSON := v1compat.InferModelProviderAndType(vName, "", m)
		bID, fID := s.matchDefaultRulesForModel(ctx, m, tName, billingRules, forwardRules)
		existingMid, exists := existingMap[m]
		if exists {
			existingCount++
		} else {
			newCount++
		}
		items = append(items, probedUpstreamModelDTO{
			ModelID:                m,
			Name:                   m,
			VendorName:             cmp.Or(vName, pName),
			ProviderID:             pID,
			ProviderName:           pName,
			TypeID:                 tID,
			TypeName:               tName,
			DefaultFeatures:        v1compat.ParseFeatureAttributes(defFeatJSON),
			SuggestedBillingRuleID: bID,
			SuggestedForwardRuleID: fID,
			Exists:                 exists,
			ExistingMid:            existingMid,
		})
	}

	slices.Sort(sources)
	sources = slices.Compact(sources)
	c.JSON(http.StatusOK, gin.H{
		"success":           true,
		"detected_base_url": detectedURL,
		"source":            strings.Join(sources, " + "),
		"total":             len(items),
		"new_count":         newCount,
		"existing_count":    existingCount,
		"data":              items,
	})
}

func (s *Server) v1ImportProbedModels(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		ChannelID *uint64  `json:"channel_id"`
		ModelIDs  []string `json:"model_ids"`
		Models    []struct {
			ModelID        string   `json:"model_id"`
			Name           string   `json:"name"`
			VendorName     string   `json:"vendor_name"`
			ProviderID     uint64   `json:"provider_id"`
			APIProviderID  uint64   `json:"api_provider_id"`
			TypeID         uint64   `json:"type_id"`
			Features       []string `json:"features"`
			BillingRuleID  uint64   `json:"billing_rule_id"`
			ForwardRuleID  uint64   `json:"forward_rule_id"`
			InputUSDPer1M  *float64 `json:"input_usd_per_1m"`
			OutputUSDPer1M *float64 `json:"output_usd_per_1m"`
			FixedUSD       *float64 `json:"fixed_usd"`
		} `json:"models"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid import request")
		return
	}
	if len(req.Models) == 0 && len(req.ModelIDs) == 0 {
		fail(c, http.StatusBadRequest, "请选择至少一个模型")
		return
	}

	_ = v1compat.EnsurePresetRules(ctx, s.db)
	var billingRules []pricing.BillingRule
	_ = s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&billingRules).Error
	var forwardRules []forward.ForwardRule
	_ = s.readOnly().WithContext(ctx).Order("sort_order DESC, id DESC").Find(&forwardRules).Error

	typesList := s.listModelTypesData()
	typeByID := make(map[uint64]v1compat.ClassificationItem, len(typesList))
	for _, tp := range typesList {
		typeByID[tp.ID] = tp
	}
	providersList := s.listModelProvidersData()
	provByID := make(map[uint64]v1compat.ClassificationItem, len(providersList))
	for _, pv := range providersList {
		provByID[pv.ID] = pv
	}

	type candidate struct {
		ModelID        string
		Name           string
		VendorName     string
		ProviderID     uint64
		APIProviderID  uint64
		TypeID         uint64
		Features       []string
		BillingRuleID  uint64
		ForwardRuleID  uint64
		InputUSDPer1M  *float64
		OutputUSDPer1M *float64
		FixedUSD       *float64
	}
	var cands []candidate
	seen := map[string]bool{}
	for _, m := range req.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cands = append(cands, candidate{
			ModelID: id, Name: cmp.Or(strings.TrimSpace(m.Name), id), VendorName: m.VendorName,
			ProviderID: m.ProviderID, APIProviderID: m.APIProviderID, TypeID: m.TypeID,
			Features: m.Features, BillingRuleID: m.BillingRuleID, ForwardRuleID: m.ForwardRuleID,
			InputUSDPer1M: m.InputUSDPer1M, OutputUSDPer1M: m.OutputUSDPer1M, FixedUSD: m.FixedUSD,
		})
	}
	for _, mid := range req.ModelIDs {
		id := strings.TrimSpace(mid)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cands = append(cands, candidate{ModelID: id, Name: id})
	}

	extMap := s.loadV1ModelExtMap()
	results := make([]gin.H, 0, len(cands))
	importedCount := 0
	var importedMids []string
	var importedNames []string

	for _, cand := range cands {
		var existingB pricing.ModelRuleBinding
		hasExisting := s.db.WithContext(ctx).Where("model = ?", cand.ModelID).First(&existingB).Error == nil
		infProvID, infProvName, infTypeID, infTypeName, infDefFeat := v1compat.InferModelProviderAndType(cand.VendorName, "", cand.ModelID)
		pID := cand.ProviderID
		if pID == 0 {
			pID = infProvID
		}
		pName := cmp.Or(provByID[pID].Name, infProvName)
		tID := cand.TypeID
		if tID == 0 {
			tID = infTypeID
		}
		tName := cmp.Or(typeByID[tID].Name, infTypeName, "聊天")
		numID := modelNumericID(cand.ModelID)
		mid := cmp.Or(extMap[cand.ModelID].Mid, "m-"+strconv.FormatUint(numID, 10))

		if !hasExisting {
			bID, fID := s.matchDefaultRulesForModel(ctx, cand.ModelID, tName, billingRules, forwardRules)
			if cand.BillingRuleID > 0 {
				bID = cand.BillingRuleID
			}
			if cand.ForwardRuleID > 0 {
				fID = cand.ForwardRuleID
			}
			fwdJSON := "[]"
			if fID > 0 {
				fwdJSON = fmt.Sprintf("[%d]", fID)
			}
			feats := cand.Features
			if len(feats) == 0 {
				defRaw := cmp.Or(typeByID[tID].DefaultFeatures, infDefFeat)
				feats = v1compat.ParseFeatureAttributes(defRaw)
			}
			featBytes, _ := json.Marshal(feats)

			p := relay.ModelPrice{Model: cand.ModelID}
			if cand.InputUSDPer1M != nil && *cand.InputUSDPer1M > 0 {
				p.InputUSDPer1M = decimal.NewFromFloat(*cand.InputUSDPer1M)
			}
			if cand.OutputUSDPer1M != nil && *cand.OutputUSDPer1M > 0 {
				p.OutputUSDPer1M = decimal.NewFromFloat(*cand.OutputUSDPer1M)
			}
			if cand.FixedUSD != nil && *cand.FixedUSD > 0 {
				p.PerCallUSD = decimal.NewFromFloat(*cand.FixedUSD)
			}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&p).Error; err != nil {
				fail(c, http.StatusInternalServerError, "save model price failed")
				return
			}
			meta := ModelMeta{
				Model:     cand.ModelID,
				Vendor:    cmp.Or(cand.VendorName, pName),
				Category:  tName,
				Icon:      provByID[pID].Logo,
				UpdatedAt: s.now().UnixMilli(),
			}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&meta).Error; err != nil {
				fail(c, http.StatusInternalServerError, "save model meta failed")
				return
			}
			binding := pricing.ModelRuleBinding{
				Model:               cand.ModelID,
				DisplayName:         cand.Name,
				BillingRuleID:       bID,
				ForwardRuleIDs:      fwdJSON,
				PreDeduction:        decimal.NewFromFloat(0.01),
				SiteDiscount:        decimal.NewFromInt(1),
				SiteDiscountEnabled: 1,
				GlobalDiscount:      decimal.NewFromInt(1),
				IsListed:            1,
				IsActive:            1,
			}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&binding).Error; err != nil {
				fail(c, http.StatusInternalServerError, "save model binding failed")
				return
			}

			apiProvID := cand.APIProviderID
			if apiProvID == 0 {
				apiProvID = pID
			}
			extMap[cand.ModelID] = v1ModelExt{
				ProviderID:        pID,
				APIProviderID:     apiProvID,
				TypeID:            tID,
				OriginalID:        cand.ModelID,
				Mid:               mid,
				FeatureAttributes: string(featBytes),
				GroupRatios:       "{}",
			}
			importedCount++
		} else {
			ext := extMap[cand.ModelID]
			if ext.Mid == "" {
				ext.Mid = mid
				extMap[cand.ModelID] = ext
			}
		}

		importedMids = append(importedMids, mid)
		importedNames = append(importedNames, cand.ModelID)
		results = append(results, gin.H{
			"id":          numID,
			"mid":         mid,
			"model_id":    cand.ModelID,
			"name":        cmp.Or(existingB.DisplayName, cand.Name),
			"provider_id": pID,
			"type_id":     tID,
			"type_name":   tName,
			"is_new":      !hasExisting,
		})
	}

	if b, err := json.Marshal(extMap); err == nil {
		_ = s.upsertRawSetting(v1KeyModelExt, string(b))
	}

	if req.ChannelID != nil && *req.ChannelID > 0 {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var ch relay.Channel
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ch, *req.ChannelID).Error; err != nil {
				return err
			}
			mergedNames := make([]string, 0, 16)
			for item := range strings.SplitSeq(ch.Models, ",") {
				if name := strings.TrimSpace(item); name != "" {
					mergedNames = append(mergedNames, name)
				}
			}
			for _, name := range importedNames {
				if !slices.Contains(mergedNames, name) {
					mergedNames = append(mergedNames, name)
				}
			}
			if err := tx.Model(&ch).Update("models", joinTrim(mergedNames)).Error; err != nil {
				return err
			}
			return updateV1ChannelMeta(tx, ch.ID, func(meta *v1ChannelMeta) {
				for _, mid := range importedMids {
					if !slices.Contains(meta.ModelMids, mid) {
						meta.ModelMids = append(meta.ModelMids, mid)
					}
				}
			})
		})
		if err != nil {
			s.Log().Error("link imported models to channel", "channel", *req.ChannelID, "err", err)
			fail(c, http.StatusInternalServerError, "link imported channel models failed")
			return
		}
		s.invalidateSettingsCache()
	}

	s.writeAudit(c, "v1_models_import_probed", strconv.Itoa(importedCount), "imported probed upstream models")
	s.invalidateCache(relay.KindPrice, "")
	s.invalidateCache(relay.KindForwardRule, "")
	s.invalidateCache(relay.KindBillingRule, "")
	s.invalidateCache(relay.KindChannel, "")

	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"imported_count": importedCount,
		"total_count":    len(results),
		"models":         results,
	})
}

func resolveChannelUpstreamModelWithMeta(meta v1ChannelMeta, ch *relay.Channel, subID *uint64, targetModel, targetMid string) string {
	if len(meta.Config) > 0 {
		var cfg struct {
			HAModelMapping map[string]map[string]string `json:"ha_model_mapping"`
		}
		if json.Unmarshal(meta.Config, &cfg) == nil && subID != nil && *subID > 0 {
			if subMap := cfg.HAModelMapping[strconv.FormatUint(*subID, 10)]; subMap != nil {
				if mapped := cmp.Or(strings.TrimSpace(subMap[targetMid]), strings.TrimSpace(subMap[targetModel])); mapped != "" {
					return mapped
				}
			}
		}
	}
	return ch.UpstreamModel(targetModel)
}

func (s *Server) v1TestChannel(c *gin.Context) {
	if s.relay == nil {
		fail(c, http.StatusServiceUnavailable, "relay not configured")
		return
	}
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var ch relay.Channel
	if err := s.readOnly().WithContext(ctx).First(&ch, id).Error; err != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	var req struct {
		Model         string         `json:"model"`
		ForwardRuleID *uint64        `json:"forward_rule_id"`
		SubChannelID  *uint64        `json:"sub_channel_id"`
		Scenario      string         `json:"scenario"`
		CustomBody    map[string]any `json:"custom_body"`
	}
	_ = c.ShouldBindJSON(&req)

	rawInputModel := strings.TrimSpace(req.Model)
	targetModel := rawInputModel
	if targetModel != "" {
		if resolved, _ := s.resolveCanonicalChannelModels(ctx, []string{targetModel}); len(resolved) > 0 {
			targetModel = resolved[0]
		}
	}
	if targetModel == "" {
		for item := range strings.SplitSeq(ch.Models, ",") {
			if m := strings.TrimSpace(item); m != "" {
				targetModel = m
				break
			}
		}
	}
	if targetModel == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"err_msg": "channel has no models configured",
			"latency": 0,
		})
		return
	}

	meta := s.loadV1ChannelMetaMap()[ch.ID]
	chCopy := ch
	var activeSubID *uint64
	if req.SubChannelID != nil && *req.SubChannelID > 0 {
		activeSubID = req.SubChannelID
		if subCfg, found := s.findV1ChannelConfig(*req.SubChannelID); found {
			if subCfg.BaseURL != "" {
				chCopy.BaseURL = subCfg.BaseURL
			}
			if subCfg.APIKeyEnc != "" {
				chCopy.CredentialEnc = subCfg.APIKeyEnc
			}
			if subCfg.ProviderType != "" {
				chCopy.Provider = mapTkeProviderToRelay(subCfg.ProviderType)
			}
		}
	} else if primary, _ := s.resolveV1SubChannels(meta.PresetID, meta.Config); primary != nil {
		pID := primary.ID
		activeSubID = &pID
		if chCopy.BaseURL == "" || chCopy.BaseURL == "https://api.openai.com" {
			if primary.BaseURL != "" {
				chCopy.BaseURL = primary.BaseURL
			}
		}
		if primary.APIKeyEnc != "" {
			chCopy.CredentialEnc = primary.APIKeyEnc
		}
	}

	ext := s.loadV1ModelExtMap()[targetModel]
	targetMid := cmp.Or(ext.Mid, rawInputModel)
	upstreamModel := resolveChannelUpstreamModelWithMeta(meta, &chCopy, activeSubID, targetModel, targetMid)

	_, _, _, typeName, _ := v1compat.InferModelProviderAndType("", "", targetModel)
	if ext.TypeID > 0 {
		for _, tp := range s.listModelTypesData() {
			if tp.ID == ext.TypeID {
				typeName = tp.Name
				break
			}
		}
	}

	var matchedRule gin.H
	entryPath := ""
	if req.ForwardRuleID != nil && *req.ForwardRuleID > 0 {
		var fr forward.ForwardRule
		if err := s.readOnly().WithContext(ctx).First(&fr, *req.ForwardRuleID).Error; err == nil {
			matchedRule = gin.H{
				"id":        fr.ID,
				"eid":       fr.EID,
				"name":      fr.Name,
				"rule_type": fr.RuleType,
				"category":  fr.Category,
			}
			var cfg struct {
				PathAccept []string `json:"path_accept"`
			}
			if json.Unmarshal([]byte(fr.ConfigJSON), &cfg) == nil && len(cfg.PathAccept) > 0 && strings.TrimSpace(cfg.PathAccept[0]) != "" {
				entryPath = cfg.PathAccept[0]
			}
		}
	}

	scenario := cmp.Or(strings.TrimSpace(req.Scenario), "default")
	relPath := "/v1/chat/completions"
	payload := map[string]any{
		"model":      upstreamModel,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": 16,
	}
	authKind := "bearer"

	switch scenario {
	case "anthropic_messages":
		relPath = "/v1/messages"
		authKind = "anthropic"
		payload = map[string]any{
			"model":      upstreamModel,
			"max_tokens": 16,
			"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		}
	case "gemini_native":
		relPath = "/v1beta/models/" + upstreamModel + ":generateContent"
		authKind = "gemini"
		payload = map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "ping"}}}},
		}
	case "openai_responses":
		relPath = "/v1/responses"
		payload = map[string]any{
			"model": upstreamModel,
			"input": "ping",
		}
	case "stream":
		relPath = "/v1/chat/completions"
		payload["stream"] = true
	default:
		switch typeName {
		case "向量":
			relPath = "/v1/embeddings"
			payload = map[string]any{"model": upstreamModel, "input": "ping"}
		case "排序":
			relPath = "/v1/rerank"
			payload = map[string]any{"model": upstreamModel, "query": "ping", "documents": []string{"ping pong", "hello"}}
		case "图片":
			relPath = "/v1/images/generations"
			payload = map[string]any{"model": upstreamModel, "prompt": "a minimal geometric cube", "size": "1024x1024", "n": 1}
		case "音频":
			if scenario == "stt" {
				relPath = "/v1/audio/transcriptions"
			} else {
				relPath = "/v1/audio/speech"
				payload = map[string]any{"model": upstreamModel, "input": "ping", "voice": "alloy"}
			}
		case "视频":
			if strings.Contains(strings.ToLower(chCopy.BaseURL), "volces") || strings.Contains(strings.ToLower(upstreamModel), "seedance") {
				relPath = "/api/v3/contents/generations/tasks"
				payload = map[string]any{
					"model":      upstreamModel,
					"content":    []map[string]any{{"type": "text", "text": "calm ocean waves"}},
					"resolution": "480p",
					"duration":   5,
				}
			} else {
				relPath = "/v1/video/generations"
				payload = map[string]any{"model": upstreamModel, "prompt": "calm ocean waves", "resolution": "480p", "duration": 5}
			}
		}
	}
	if len(req.CustomBody) > 0 {
		payload = map[string]any{}
		for k, v := range req.CustomBody {
			payload[k] = v
		}
		if _, exists := payload["model"]; !exists {
			payload["model"] = upstreamModel
		}
	}
	if entryPath == "" {
		entryPath = relPath
	}

	credPlain := s.decryptConfigSecret(chCopy.CredentialEnc)
	if credPlain == "" || credPlain == "placeholder-key" {
		c.JSON(http.StatusOK, gin.H{
			"success":      false,
			"err_msg":      "当前渠道或子渠道未配置有效的上游 API Key",
			"latency":      0,
			"status":       0,
			"entry_path":   entryPath,
			"scenario":     scenario,
			"matched_rule": matchedRule,
			"request_data": payload,
		})
		return
	}

	if scenario == "default" && typeName == "聊天" && len(req.CustomBody) == 0 && s.upstreamHTTP == nil {
		res := s.relay.TestChannel(ctx, &chCopy, upstreamModel)
		targetURL := strings.TrimRight(chCopy.BaseURL, "/") + "/chat/completions"
		bodyJSON, _ := json.Marshal(payload)
		curlCmd := fmt.Sprintf(`curl -X POST %q -H "Content-Type: application/json" -H "Authorization: Bearer sk-****" -d '%s'`, targetURL, string(bodyJSON))
		c.JSON(http.StatusOK, gin.H{
			"success":      res.OK,
			"err_msg":      res.Message,
			"latency":      res.LatencyMs,
			"status":       res.Status,
			"entry_path":   entryPath,
			"scenario":     scenario,
			"matched_rule": matchedRule,
			"request_data": payload,
			"response_data": gin.H{
				"ok":         res.OK,
				"status":     res.Status,
				"latency_ms": res.LatencyMs,
				"message":    res.Message,
			},
			"curl_command": curlCmd,
		})
		return
	}

	baseTrim := strings.TrimRight(chCopy.BaseURL, "/")
	targetURL := baseTrim + relPath
	if strings.HasSuffix(baseTrim, "/v1") && strings.HasPrefix(relPath, "/v1/") {
		targetURL = baseTrim + strings.TrimPrefix(relPath, "/v1")
	} else if strings.HasSuffix(baseTrim, "/v1beta") && strings.HasPrefix(relPath, "/v1beta/") {
		targetURL = baseTrim + strings.TrimPrefix(relPath, "/v1beta")
	} else if strings.HasSuffix(baseTrim, "/api/v3") && strings.HasPrefix(relPath, "/api/v3/") {
		targetURL = baseTrim + strings.TrimPrefix(relPath, "/api/v3")
	}

	bodyBytes, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	upReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	switch authKind {
	case "anthropic":
		upReq.Header.Set("x-api-key", credPlain)
		upReq.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		upReq.Header.Set("x-goog-api-key", credPlain)
	default:
		upReq.Header.Set("Authorization", "Bearer "+credPlain)
	}
	t0 := time.Now()
	resp, doErr := s.upstreamHTTPClient().Do(upReq)
	latency := time.Since(t0).Milliseconds()
	if doErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":      false,
			"err_msg":      "upstream unreachable: " + doErr.Error(),
			"latency":      latency,
			"status":       0,
			"entry_path":   entryPath,
			"scenario":     scenario,
			"matched_rule": matchedRule,
			"request_data": payload,
		})
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	var parsedResp any
	if json.Unmarshal(respBytes, &parsedResp) != nil {
		parsedResp = string(respBytes)
	}
	okResp := resp.StatusCode >= 200 && resp.StatusCode < 300
	errMsg := ""
	if !okResp {
		errMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		if len(errMsg) > 300 {
			errMsg = errMsg[:300]
		}
	}
	curlCmd := fmt.Sprintf(`curl -X POST %q -H "Content-Type: application/json" -H "Authorization: Bearer sk-****" -d '%s'`, targetURL, string(bodyBytes))
	c.JSON(http.StatusOK, gin.H{
		"success":       okResp,
		"err_msg":       errMsg,
		"latency":       latency,
		"status":        resp.StatusCode,
		"entry_path":    entryPath,
		"scenario":      scenario,
		"matched_rule":  matchedRule,
		"request_data":  payload,
		"response_data": parsedResp,
		"curl_command":  curlCmd,
	})
}
