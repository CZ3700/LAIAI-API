package server

import (
	"context"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
)

// Public catalog and SDK model discovery share one transactional projection.
// Read the primary: version, binding and rule rows must describe one snapshot.
func (s *Server) publicPricing(ctx context.Context) ([]gin.H, error) {
	out := []gin.H{}
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prices []relay.ModelPrice
		var channels []relay.Channel
		var metas []ModelMeta
		var configs []video.ModelConfig
		if e := tx.Order("model").Find(&prices).Error; e != nil {
			return e
		}
		if e := tx.Select("id,provider,base_url,models,model_mapping,status,owner_org_id").Where("status = 1 AND owner_org_id = 0").Order("id").Find(&channels).Error; e != nil {
			return e
		}
		if e := tx.Find(&metas).Error; e != nil {
			return e
		}
		if s.video != nil {
			if e := tx.Order("public_model").Find(&configs).Error; e != nil {
				return e
			}
		}
		metaMap := map[string]ModelMeta{}
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
			for _, ch := range channels {
				if !ch.ServesModel(p.Model) {
					continue
				}
				model := catalogModel{Model: p.Model, UpstreamModel: ch.UpstreamModel(p.Model), PricingKind: "unknown", BillingModes: []string{}}
				if meta, ok := metaMap[p.Model]; ok {
					model.Meta = &meta
				}
				classifyCatalogModel(&model, ch, p, true, configs, false)
				// Official models have canonical identities and a separate published tariff.
				if model.PricingKind == "video" {
					continue
				}
				d := genericDescriptor(model, p, true, ch)
				if d.Status != "published" {
					continue
				}
				row := gin.H{"model": p.Model, "provider": ch.Provider, "input_usd_per_1m": p.InputUSDPer1M, "output_usd_per_1m": p.OutputUSDPer1M, "image_usd": p.ImageUSD, "pricing": d}
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
		videos, e := loadVideoPricingProjection(tx, configs, channels, s.now().UTC())
		if e != nil {
			return e
		}
		for name, v := range videos {
			if len(v.Pricing.Combinations) == 0 {
				continue
			}
			row := gin.H{"model": name, "provider": v.Provider, "category": "video", "pricing": v.Pricing}
			attachMeta(row, name)
			row["category"] = "video"
			out = append(out, row)
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["model"].(string) < out[j]["model"].(string) })
		return nil
	})
	return out, e
}

func (s *Server) listPricedModels(c *gin.Context) {
	reject := func(status int, message string) {
		c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "type": "laiai_relay_error"}})
	}
	auth := c.GetHeader("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		reject(401, "missing api key")
		return
	}
	key, e := identity.VerifyKey(s.db.WithContext(c.Request.Context()), strings.TrimPrefix(auth, "Bearer "), s.now())
	if e != nil {
		reject(401, "invalid api key")
		return
	}
	rows, e := s.publicPricing(c.Request.Context())
	if e != nil {
		reject(500, "model list failed")
		return
	}
	data := []gin.H{}
	for _, r := range rows {
		name := r["model"].(string)
		if key.AllowsModel(name) {
			data = append(data, gin.H{"id": name, "object": "model", "owned_by": r["provider"]})
		}
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}
