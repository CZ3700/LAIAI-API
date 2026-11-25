package server

import (
	"context"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/pricing"
	"github.com/laiai/laiai-next/core/internal/relay"
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

func (s *Server) listPricedModels(c *gin.Context) {
	reject := func(status int, message string) {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "laiai_relay_error"}})
	}
	c.Header("Cache-Control", "private, no-store")
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		reject(401, "missing api key")
		return
	}
	key, err := identity.VerifyKey(s.db.WithContext(c.Request.Context()), strings.TrimPrefix(auth, "Bearer "), s.now())
	if err != nil {
		reject(401, "invalid api key")
		return
	}
	ctx := c.Request.Context()
	var channels []relay.Channel
	if err := s.readOnly().WithContext(ctx).
		Select("id,provider,models,status,owner_org_id").
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
	var bindings []pricing.ModelRuleBinding
	if err := s.readOnly().WithContext(ctx).Where("is_active = 1").Find(&bindings).Error; err == nil {
		for _, b := range bindings {
			priced[b.Model] = true
		}
	}
	data := make([]gin.H, 0, len(priced))
	seen := map[string]bool{}
	for _, ch := range channels {
		for m := range strings.SplitSeq(ch.Models, ",") {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] || !priced[m] || !key.AllowsModel(m) {
				continue
			}
			seen[m] = true
			data = append(data, gin.H{"id": m, "object": "model", "owned_by": ch.Provider})
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i]["id"].(string) < data[j]["id"].(string) })
	c.JSON(200, gin.H{"object": "list", "data": data})
}
