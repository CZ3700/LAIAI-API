package server

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Public catalog and SDK model discovery share one projection over models + active channels.
func (s *Server) publicPricing(ctx context.Context) ([]gin.H, error) {
	var prices []relay.ModelPrice
	var channels []relay.Channel
	var metas []ModelMeta
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Order("model").Find(&prices).Error; e != nil {
			return e
		}
		if e := tx.Select("id,provider,base_url,models,model_mapping,status,owner_org_id").Where("status = 1 AND owner_org_id = 0").Order("id").Find(&channels).Error; e != nil {
			return e
		}
		return tx.Find(&metas).Error
	}); err != nil {
		return nil, err
	}
	return projectGenericPublicPricing(ctx, prices, channels, metas)
}

func projectGenericPublicPricing(ctx context.Context, prices []relay.ModelPrice, channels []relay.Channel, metas []ModelMeta) ([]gin.H, error) {
	out := []gin.H{}
	metaMap := make(map[string]ModelMeta, len(metas))
	for _, m := range metas {
		metaMap[m.Model] = m
	}
	attachMeta := func(row gin.H, name string) {
		if m, ok := metaMap[name]; ok {
			row["vendor"] = m.Vendor
			row["description"] = m.Description
			row["category"] = m.Category
			row["tags"] = m.Tags
			row["context_length"] = m.ContextLength
			row["icon"] = m.Icon
		}
	}
	for _, p := range prices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, ch := range channels {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !ch.ServesModel(p.Model) {
				continue
			}
			model := catalogModel{Model: p.Model, UpstreamModel: ch.UpstreamModel(p.Model), PricingKind: "unknown", BillingModes: []string{}}
			if meta, ok := metaMap[p.Model]; ok {
				model.Meta = &meta
			}
			classifyCatalogModel(&model, ch, p, true)
			d := genericDescriptor(model, p, true, ch)
			if d.Status != "published" {
				continue
			}
			row := gin.H{
				"model":             p.Model,
				"provider":          ch.Provider,
				"input_usd_per_1m":  p.InputUSDPer1M,
				"output_usd_per_1m": p.OutputUSDPer1M,
				"image_usd":         p.ImageUSD,
				"pricing":           d,
			}
			if p.PerCallUSD.IsPositive() {
				row["per_call_usd"] = p.PerCallUSD
			}
			if p.CachedInputUSDPer1M.IsPositive() {
				row["cached_input_usd_per_1m"] = p.CachedInputUSDPer1M
			}
			attachMeta(row, p.Model)
			out = append(out, row)
			break
		}
	}
	return out, nil
}

func extractGatewayAPIKey(c *gin.Context) string {
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if token, ok := strings.CutPrefix(auth, "Bearer "); ok && strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k
	}
	return strings.TrimSpace(c.Query("key"))
}

func (s *Server) authenticateGatewayKey(c *gin.Context) (*identity.APIKey, bool) {
	reject := func(status int, message string) {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "laiai_relay_error"}})
	}
	rawKey := extractGatewayAPIKey(c)
	if rawKey == "" {
		reject(http.StatusUnauthorized, "missing api key")
		return nil, false
	}
	key, err := identity.VerifyKey(s.db.WithContext(c.Request.Context()), rawKey, s.now())
	if err != nil {
		reject(http.StatusUnauthorized, "invalid api key")
		return nil, false
	}
	return key, true
}

// GET /v1/balance: 返回当前 API Key 令牌子配额的剩余与已用额度。
func (s *Server) getTokenBalance(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	key, ok := s.authenticateGatewayKey(c)
	if !ok {
		return
	}
	usedF, _ := key.UsedAmount.Float64()
	if key.QuotaLimit.IsPositive() {
		rem := key.QuotaLimit.Sub(key.UsedAmount)
		if rem.IsNegative() {
			rem = decimal.Zero
		}
		remF, _ := rem.Float64()
		c.JSON(http.StatusOK, gin.H{
			"success":         true,
			"remain_balance":  remF,
			"used_balance":    usedF,
			"unlimited_quota": false,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"remain_balance":  -1.0,
		"used_balance":    usedF,
		"unlimited_quota": true,
	})
}

// GET /v1/user/balance: 返回当前 API Key 所属组织账户的全局可用总额度（资金读必须走主库 s.db）。
func (s *Server) getUserBalance(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	ctx := c.Request.Context()
	key, ok := s.authenticateGatewayKey(c)
	if !ok {
		return
	}
	posted, held, err := ledger.OrgBalances(s.db.WithContext(ctx), key.OrgID, ledger.AccountUserCredit)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "balance unavailable", "type": "laiai_relay_error"}})
		return
	}
	avail := posted.Sub(held)
	if avail.IsNegative() {
		avail = decimal.Zero
	}
	availF, _ := avail.Float64()
	var totalSpent float64
	_ = s.db.WithContext(ctx).
		Table("invocations").
		Where("org_id = ?", key.OrgID).
		Select("COALESCE(SUM(cost_usd), 0)").
		Scan(&totalSpent).Error
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"remain_balance":  availF,
		"used_balance":    totalSpent,
		"unlimited_quota": false,
	})
}

func (s *Server) listPricedModels(c *gin.Context) {
	reject := func(status int, message string) {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "laiai_relay_error"}})
	}
	c.Header("Cache-Control", "private, no-store")
	key, ok := s.authenticateGatewayKey(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var channels []relay.Channel
	if err := s.readOnly().WithContext(ctx).
		Select("id,provider,models,model_mapping,status,owner_org_id").
		Where("status = 1 AND (owner_org_id = 0 OR owner_org_id = ?)", key.OrgID).
		Order("id").Find(&channels).Error; err != nil {
		reject(503, "model catalog unavailable")
		return
	}
	var prices []relay.ModelPrice
	if err := s.readOnly().WithContext(ctx).Find(&prices).Error; err != nil {
		reject(503, "model catalog unavailable")
		return
	}
	priced := make(map[string]bool, len(prices))
	for _, p := range prices {
		priced[p.Model] = true
	}
	displayNames := make(map[string]string)
	var bindings []pricing.ModelRuleBinding
	if err := s.readOnly().WithContext(ctx).Where("is_active = 1").Find(&bindings).Error; err == nil {
		for _, b := range bindings {
			priced[b.Model] = true
			if strings.TrimSpace(b.DisplayName) != "" {
				displayNames[b.Model] = strings.TrimSpace(b.DisplayName)
			}
		}
	}

	type modelEntry struct {
		ID      string
		OwnedBy string
	}
	entries := make([]modelEntry, 0, len(priced))
	seen := map[string]bool{}
	for _, ch := range channels {
		for m := range strings.SplitSeq(ch.Models, ",") {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] || !priced[m] || !key.AllowsModel(m) {
				continue
			}
			seen[m] = true
			entries = append(entries, modelEntry{ID: m, OwnedBy: ch.Provider})
		}
	}
	slices.SortFunc(entries, func(a, b modelEntry) int {
		return cmp.Compare(a.ID, b.ID)
	})

	isGeminiFormat := strings.HasPrefix(c.Request.URL.Path, "/v1beta/") ||
		(c.GetHeader("Authorization") == "" && (c.GetHeader("x-goog-api-key") != "" || c.Query("key") != ""))
	if isGeminiFormat {
		models := make([]gin.H, 0, len(entries))
		for _, item := range entries {
			models = append(models, gin.H{
				"name":                       "models/" + item.ID,
				"baseModelId":                item.ID,
				"displayName":                cmp.Or(displayNames[item.ID], item.ID),
				"description":                item.OwnedBy + " model",
				"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
			})
		}
		c.JSON(http.StatusOK, gin.H{"models": models})
		return
	}

	data := make([]gin.H, 0, len(entries))
	for _, item := range entries {
		data = append(data, gin.H{"id": item.ID, "object": "model", "created": 1719441600, "owned_by": item.OwnedBy})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
