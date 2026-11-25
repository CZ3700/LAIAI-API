package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func (s *Server) v1ModelProviders(c *gin.Context) {
	c.JSON(http.StatusOK, v1compat.StandardProviders())
}

func (s *Server) v1ModelTypes(c *gin.Context) {
	c.JSON(http.StatusOK, v1compat.StandardModelTypes())
}

func (s *Server) v1ListChannelCategories(c *gin.Context) {
	rows := []forward.ChannelCategory{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list channel categories failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": rows, "data": rows})
}

func (s *Server) v1ListBillingRules(c *gin.Context) {
	rows := []pricing.BillingRule{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list billing rules failed")
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) v1ListForwardRules(c *gin.Context) {
	rows := []forward.ForwardRule{}
	if err := s.readOnly().WithContext(c.Request.Context()).Order("sort_order ASC, id DESC").Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list forward rules failed")
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (s *Server) v1AdminListModels(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	var bindings []pricing.ModelRuleBinding
	if err := rdb.Order("sort_order DESC, model ASC").Find(&bindings).Error; err != nil && !v1compat.IsMissingTable(err) {
		fail(c, http.StatusInternalServerError, "list model bindings failed")
		return
	}
	var prices []relay.ModelPrice
	_ = rdb.Order("model ASC").Find(&prices).Error
	var metas []ModelMeta
	_ = rdb.Find(&metas).Error
	var billingRules []pricing.BillingRule
	_ = rdb.Find(&billingRules).Error
	var forwardRules []forward.ForwardRule
	_ = rdb.Find(&forwardRules).Error

	priceMap := make(map[string]relay.ModelPrice, len(prices))
	for _, p := range prices {
		priceMap[p.Model] = p
	}
	metaMap := make(map[string]ModelMeta, len(metas))
	for _, m := range metas {
		metaMap[m.Model] = m
	}
	brMap := make(map[uint64]pricing.BillingRule, len(billingRules))
	for _, br := range billingRules {
		brMap[br.ID] = br
	}
	frMap := make(map[uint64]forward.ForwardRule, len(forwardRules))
	for _, fr := range forwardRules {
		frMap[fr.ID] = fr
	}
	bindingMap := make(map[string]pricing.ModelRuleBinding, len(bindings))
	for _, b := range bindings {
		bindingMap[b.Model] = b
	}

	seen := make(map[string]struct{}, len(bindings)+len(prices))
	names := make([]string, 0, len(bindings)+len(prices))
	for _, b := range bindings {
		seen[b.Model] = struct{}{}
		names = append(names, b.Model)
	}
	for _, p := range prices {
		if _, ok := seen[p.Model]; !ok {
			seen[p.Model] = struct{}{}
			names = append(names, p.Model)
		}
	}

	out := make([]gin.H, 0, len(names))
	for _, modelName := range names {
		b, hasB := bindingMap[modelName]
		p := priceMap[modelName]
		meta := metaMap[modelName]

		fwdIDs := []uint64{}
		fwdNames := []string{}
		if hasB {
			fwdIDs = v1compat.ParseRuleIDList(b.ForwardRuleIDs)
			for _, fid := range fwdIDs {
				if fr, ok := frMap[fid]; ok {
					fwdNames = append(fwdNames, fmt.Sprintf("%s (%s)", fr.Name, fr.EID))
				}
			}
		}
		var brName, brPID string
		if hasB && b.BillingRuleID > 0 {
			if br, ok := brMap[b.BillingRuleID]; ok {
				brName = br.Name
				brPID = br.PID
			}
		}

		displayName := modelName
		isListed := true
		isActive := true
		sortOrder := 0
		preDed := decimal.Zero
		siteDisc := decimal.NewFromInt(1)
		siteDiscEn := false
		if hasB {
			displayName = cmp.Or(strings.TrimSpace(b.DisplayName), modelName)
			isListed = b.IsListed == 1
			isActive = b.IsActive == 1
			sortOrder = b.SortOrder
			preDed = b.PreDeduction
			if b.SiteDiscount.IsPositive() {
				siteDisc = b.SiteDiscount
			}
			siteDiscEn = b.SiteDiscountEnabled == 1
		}

		out = append(out, gin.H{
			"id":                    modelName,
			"model":                 modelName,
			"model_id":              modelName,
			"name":                  displayName,
			"model_alias":           b.ModelAlias,
			"vendor":                meta.Vendor,
			"category":              meta.Category,
			"description":           meta.Description,
			"context_length":        meta.ContextLength,
			"icon":                  meta.Icon,
			"billing_rule_id":       b.BillingRuleID,
			"billing_rule_name":     brName,
			"billing_rule_pid":      brPID,
			"forward_rule_ids":      fwdIDs,
			"forward_rule_names":    fwdNames,
			"pre_deduction":         preDed,
			"site_discount":         siteDisc,
			"site_discount_enabled": siteDiscEn,
			"input_usd_per_1m":      p.InputUSDPer1M,
			"output_usd_per_1m":     p.OutputUSDPer1M,
			"image_usd":             p.ImageUSD,
			"per_call_usd":          p.PerCallUSD,
			"is_listed":             isListed,
			"is_active":             isActive,
			"sort_order":            sortOrder,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (s *Server) v1AdminUpsertModel(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		Model               string           `json:"model"`
		ModelID             string           `json:"model_id"`
		Name                string           `json:"name"`
		ModelAlias          string           `json:"model_alias"`
		Vendor              string           `json:"vendor"`
		Category            string           `json:"category"`
		Description         string           `json:"description"`
		ContextLength       int              `json:"context_length"`
		Icon                string           `json:"icon"`
		BillingRuleID       uint64           `json:"billing_rule_id"`
		ForwardRuleIDs      []uint64         `json:"forward_rule_ids"`
		PreDeduction        *decimal.Decimal `json:"pre_deduction"`
		SiteDiscount        *decimal.Decimal `json:"site_discount"`
		SiteDiscountEnabled *bool            `json:"site_discount_enabled"`
		InputUSDPer1M       *decimal.Decimal `json:"input_usd_per_1m"`
		OutputUSDPer1M      *decimal.Decimal `json:"output_usd_per_1m"`
		ImageUSD            *decimal.Decimal `json:"image_usd"`
		PerCallUSD          *decimal.Decimal `json:"per_call_usd"`
		IsListed            *bool            `json:"is_listed"`
		IsActive            *bool            `json:"is_active"`
		SortOrder           int              `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	model := cmp.Or(strings.TrimSpace(c.Param("id")), strings.TrimSpace(req.Model), strings.TrimSpace(req.ModelID))
	if model == "" {
		fail(c, http.StatusBadRequest, "model is required")
		return
	}

	db := s.db.WithContext(ctx)
	if req.BillingRuleID > 0 {
		var cnt int64
		if err := db.Table("billing_rules").Where("id = ?", req.BillingRuleID).Count(&cnt).Error; err != nil || cnt == 0 {
			fail(c, http.StatusBadRequest, "referenced billing_rule_id not found")
			return
		}
	}
	uniqueFwds := deduplicateUint64s(req.ForwardRuleIDs)
	if len(uniqueFwds) > 0 {
		var cnt int64
		if err := db.Table("forward_rules").Where("id IN ?", uniqueFwds).Count(&cnt).Error; err != nil || cnt != int64(len(uniqueFwds)) {
			fail(c, http.StatusBadRequest, "referenced forward_rule_ids contain unknown id")
			return
		}
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		dVal := func(p *decimal.Decimal) decimal.Decimal {
			if p == nil || p.IsNegative() {
				return decimal.Zero
			}
			return *p
		}
		// Keep model_prices synchronized as fallback baseline price.
		if req.InputUSDPer1M != nil || req.OutputUSDPer1M != nil || req.ImageUSD != nil || req.PerCallUSD != nil || req.BillingRuleID > 0 {
			var existingPrice relay.ModelPrice
			_ = tx.Where("model = ?", model).First(&existingPrice).Error
			p := relay.ModelPrice{
				Model:          model,
				InputUSDPer1M:  existingPrice.InputUSDPer1M,
				OutputUSDPer1M: existingPrice.OutputUSDPer1M,
				ImageUSD:       existingPrice.ImageUSD,
				PerCallUSD:     existingPrice.PerCallUSD,
			}
			if req.InputUSDPer1M != nil {
				p.InputUSDPer1M = dVal(req.InputUSDPer1M)
			}
			if req.OutputUSDPer1M != nil {
				p.OutputUSDPer1M = dVal(req.OutputUSDPer1M)
			}
			if req.ImageUSD != nil {
				p.ImageUSD = dVal(req.ImageUSD)
			}
			if req.PerCallUSD != nil {
				p.PerCallUSD = dVal(req.PerCallUSD)
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&p).Error; err != nil {
				return err
			}
		}
		meta := ModelMeta{
			Model:         model,
			Vendor:        strings.TrimSpace(req.Vendor),
			Category:      strings.TrimSpace(req.Category),
			Description:   strings.TrimSpace(req.Description),
			ContextLength: max(0, req.ContextLength),
			Icon:          strings.TrimSpace(req.Icon),
			UpdatedAt:     s.now().UnixMilli(),
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&meta).Error; err != nil {
			return err
		}

		fwdBytes, _ := json.Marshal(uniqueFwds)
		preDed := dVal(req.PreDeduction)
		siteDisc := decimal.NewFromInt(1)
		if req.SiteDiscount != nil && req.SiteDiscount.IsPositive() {
			siteDisc = *req.SiteDiscount
		}
		isListed := int8(1)
		if req.IsListed != nil {
			isListed = b2i8(*req.IsListed)
		}
		isActive := int8(1)
		if req.IsActive != nil {
			isActive = b2i8(*req.IsActive)
		}
		siteEn := int8(0)
		if req.SiteDiscountEnabled != nil {
			siteEn = b2i8(*req.SiteDiscountEnabled)
		}
		binding := pricing.ModelRuleBinding{
			Model:               model,
			DisplayName:         cmp.Or(strings.TrimSpace(req.Name), model),
			ModelAlias:          strings.TrimSpace(req.ModelAlias),
			BillingRuleID:       req.BillingRuleID,
			ForwardRuleIDs:      string(fwdBytes),
			PreDeduction:        preDed,
			SiteDiscount:        siteDisc,
			SiteDiscountEnabled: siteEn,
			GlobalDiscount:      decimal.NewFromInt(1),
			IsListed:            isListed,
			IsActive:            isActive,
			SortOrder:           req.SortOrder,
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "model"}}, UpdateAll: true}).Create(&binding).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "save model failed")
		return
	}
	s.writeAudit(c, "v1_model_upsert", model, "saved model metadata & rule binding")
	s.invalidateCache(relay.KindPrice, model)
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"model": model})
}

func (s *Server) v1AdminDeleteModel(c *gin.Context) {
	model := strings.TrimSpace(c.Param("id"))
	if model == "" {
		fail(c, http.StatusBadRequest, "model required")
		return
	}
	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model = ?", model).Delete(&pricing.ModelRuleBinding{}).Error; err != nil && !v1compat.IsMissingTable(err) {
			return err
		}
		_ = tx.Where("model = ?", model).Delete(&ModelMeta{}).Error
		return tx.Where("model = ?", model).Delete(&relay.ModelPrice{}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete model failed")
		return
	}
	s.writeAudit(c, "v1_model_delete", model, "deleted model binding and price")
	s.invalidateCache(relay.KindPrice, model)
	s.invalidateCache(relay.KindForwardRule, model)
	s.invalidateCache(relay.KindBillingRule, model)
	c.JSON(http.StatusOK, gin.H{"deleted": model})
}

func (s *Server) v1AdminListAnnouncements(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": s.loadAnnouncements(c.Request.Context())})
}

func (s *Server) v1AdminCreateAnnouncement(c *gin.Context) {
	var req struct {
		Title     string `json:"title" binding:"required"`
		Content   string `json:"content" binding:"required"`
		IsPinned  int8   `json:"is_pinned"`
		IsActive  *int8  `json:"is_active"`
		IsPopup   int8   `json:"is_popup"`
		SortOrder int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "title and content are required")
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	var maxID uint64
	for _, it := range items {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	active := int8(1)
	if req.IsActive != nil && *req.IsActive == 0 {
		active = 0
	}
	now := s.now().UTC().Format(time.RFC3339)
	entry := v1Announcement{
		ID:        maxID + 1,
		Title:     strings.TrimSpace(req.Title),
		Content:   req.Content,
		IsPinned:  req.IsPinned,
		IsActive:  active,
		IsPopup:   req.IsPopup,
		SortOrder: req.SortOrder,
		CreatedAt: now,
		UpdatedAt: now,
	}
	items = append([]v1Announcement{entry}, items...)
	if err := s.saveAnnouncements(ctx, items); err != nil {
		fail(c, http.StatusInternalServerError, "save announcement failed")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"announcement": entry})
}

func (s *Server) v1AdminUpdateAnnouncement(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Title     *string `json:"title"`
		Content   *string `json:"content"`
		IsPinned  *int8   `json:"is_pinned"`
		IsActive  *int8   `json:"is_active"`
		IsPopup   *int8   `json:"is_popup"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	found := false
	for i := range items {
		if items[i].ID == id {
			found = true
			if req.Title != nil {
				items[i].Title = strings.TrimSpace(*req.Title)
			}
			if req.Content != nil {
				items[i].Content = *req.Content
			}
			if req.IsPinned != nil {
				items[i].IsPinned = *req.IsPinned
			}
			if req.IsActive != nil {
				items[i].IsActive = *req.IsActive
			}
			if req.IsPopup != nil {
				items[i].IsPopup = *req.IsPopup
			}
			if req.SortOrder != nil {
				items[i].SortOrder = *req.SortOrder
			}
			items[i].UpdatedAt = s.now().UTC().Format(time.RFC3339)
			break
		}
	}
	if !found {
		fail(c, http.StatusNotFound, "announcement not found")
		return
	}
	if err := s.saveAnnouncements(ctx, items); err != nil {
		fail(c, http.StatusInternalServerError, "save announcement failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": id})
}

func (s *Server) v1AdminDeleteAnnouncement(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	items := s.loadAnnouncements(ctx)
	next := make([]v1Announcement, 0, len(items))
	removed := false
	for _, it := range items {
		if it.ID == id {
			removed = true
			continue
		}
		next = append(next, it)
	}
	if !removed {
		fail(c, http.StatusNotFound, "announcement not found")
		return
	}
	if err := s.saveAnnouncements(ctx, next); err != nil {
		fail(c, http.StatusInternalServerError, "delete announcement failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// ── Admin Docs API ──────────────────────────

func (s *Server) v1AdminListDocs(c *gin.Context) {
	tree, err := v1compat.ListDocTree(c.Request.Context(), s.db, "", false)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list docs failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"tree": tree})
}

func (s *Server) v1AdminGetDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	doc, err := v1compat.GetDocDetail(c.Request.Context(), s.db, id, c.Query("lang"), c.Query("lang") == "", false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "document not found")
			return
		}
		fail(c, http.StatusInternalServerError, "get doc failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"doc": doc})
}

func (s *Server) v1AdminCreateDoc(c *gin.Context) {
	var req v1compat.DocMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	doc, err := v1compat.CreateDoc(c.Request.Context(), s.db, req)
	if errors.Is(err, v1compat.ErrDocParentNotDir) || errors.Is(err, v1compat.ErrDocCycle) {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "create doc failed")
		return
	}
	s.writeAudit(c, "doc_create", strconv.FormatUint(doc.ID, 10), "created doc "+doc.Title)
	c.JSON(http.StatusCreated, gin.H{"doc": doc})
}

func (s *Server) v1AdminUpdateDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req v1compat.DocMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	doc, err := v1compat.UpdateDoc(c.Request.Context(), s.db, id, req)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "document not found")
		return
	}
	if errors.Is(err, v1compat.ErrDocParentNotDir) || errors.Is(err, v1compat.ErrDocCycle) {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "update doc failed")
		return
	}
	s.writeAudit(c, "doc_update", strconv.FormatUint(id, 10), "updated doc "+doc.Title)
	c.JSON(http.StatusOK, gin.H{"doc": doc})
}

func (s *Server) v1AdminDeleteDoc(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	err := v1compat.DeleteDoc(c.Request.Context(), s.db, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete doc failed")
		return
	}
	s.writeAudit(c, "doc_delete", strconv.FormatUint(id, 10), "deleted doc")
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

func (s *Server) v1AdminSetDocExpanded(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		DefaultExpanded int8 `json:"default_expanded"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	err := v1compat.SetDocDefaultExpanded(c.Request.Context(), s.db, id, req.DefaultExpanded != 0)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, http.StatusBadRequest, "directory not found")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "update default expanded failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "default_expanded": req.DefaultExpanded != 0})
}

func (s *Server) v1AdminImportDefaultDocs(c *gin.Context) {
	if err := v1compat.EnsurePresetDocs(c.Request.Context(), s.db, true); err != nil {
		fail(c, http.StatusInternalServerError, "import default docs failed")
		return
	}
	s.writeAudit(c, "doc_import_default", "all", "restored preset docs")
	c.JSON(http.StatusOK, gin.H{"message": "已重置系统预设文档"})
}

func (s *Server) v1AdminClearAllDocs(c *gin.Context) {
	if err := v1compat.ClearAllDocs(c.Request.Context(), s.db); err != nil {
		fail(c, http.StatusInternalServerError, "clear docs failed")
		return
	}
	s.writeAudit(c, "doc_clear_all", "all", "cleared all docs")
	c.JSON(http.StatusOK, gin.H{"message": "cleared"})
}
