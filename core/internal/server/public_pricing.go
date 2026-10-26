package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/video"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
		var e error
		out, e = projectGenericPublicPricing(ctx, prices, channels, metas, configs)
		if e != nil {
			return e
		}
		metaMap := make(map[string]ModelMeta, len(metas))
		for _, m := range metas {
			metaMap[m.Model] = m
		}
		attachMeta := func(row gin.H, name string) {
			if m, ok := metaMap[name]; ok {
				row["vendor"], row["description"], row["category"], row["tags"] = m.Vendor, m.Description, m.Category, m.Tags
				row["context_length"], row["icon"] = m.ContextLength, m.Icon
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

func projectGenericPublicPricing(ctx context.Context, prices []relay.ModelPrice, channels []relay.Channel, metas []ModelMeta, configs []video.ModelConfig) ([]gin.H, error) {
	out := []gin.H{}
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
	return out, nil
}

// Discovery shares the video subject and official projection's single RR
// snapshot. Generic rows retain their existing three reads and classification.
func readGenericDiscoveryTx(ctx context.Context, tx *gorm.DB) ([]gin.H, error) {
	var prices []relay.ModelPrice
	var channels []relay.Channel
	var metas []ModelMeta
	if err := discoverySelectTx(ctx, tx, "*", func(q *gorm.DB) error { return q.Order("model").Find(&prices).Error }); err != nil {
		return nil, err
	}
	if err := discoverySelectTx(ctx, tx, "id,provider,base_url,models,model_mapping,status,owner_org_id", func(q *gorm.DB) error {
		return q.Where("status = 1 AND owner_org_id = 0").Order("id").Find(&channels).Error
	}); err != nil {
		return nil, err
	}
	if err := discoverySelectTx(ctx, tx, "*", func(q *gorm.DB) error { return q.Find(&metas).Error }); err != nil {
		return nil, err
	}
	// seedanceIdentity already recognizes the supported canonical/deployment
	// identities and unsupported aliases without reading private configs again.
	return projectGenericPublicPricing(ctx, prices, channels, metas, nil)
}

func discoverySelectTx(ctx context.Context, tx *gorm.DB, columns string, read func(*gorm.DB) error) error {
	if ctx == nil || tx == nil || tx.Statement == nil || tx.Config == nil || tx.Dialector == nil || tx.DryRun || read == nil {
		return video.ErrCatalogInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return video.ErrCatalogInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		millis := time.Until(deadline).Milliseconds()
		if millis < 1 {
			return context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(millis, int64(200)), columns)
	}
	err := read(tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns))
	if child.Err() != nil {
		return child.Err()
	}
	if err != nil {
		return video.ErrCatalogUnavailable
	}
	return nil
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
	if s.video == nil {
		reject(503, "model catalog unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	tx := s.db.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard}).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		reject(503, "model catalog unavailable")
		return
	}
	defer tx.Rollback()
	observation, err := s.video.ReadCatalogTx(ctx, tx, video.CatalogSubjectRef{OrgID: key.OrgID, KeyID: key.ID})
	if errors.Is(err, video.ErrCatalogDenied) {
		reject(403, "invalid api key")
		return
	}
	if err != nil {
		reject(503, "model catalog unavailable")
		return
	}
	rows, err := readGenericDiscoveryTx(ctx, tx)
	if err != nil {
		reject(503, "model catalog unavailable")
		return
	}
	data := []gin.H{}
	seen := map[string]bool{}
	for _, row := range rows {
		name := row["model"].(string)
		if observation.AllowsModel(name) && !seen[name] {
			data = append(data, gin.H{"id": name, "object": "model", "owned_by": row["provider"]})
			seen[name] = true
		}
	}
	for _, model := range observation.Models {
		if model.HasEligibleAction() && !seen[model.ID] {
			data = append(data, gin.H{"id": model.ID, "object": "model", "owned_by": model.Provider})
			seen[model.ID] = true
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i]["id"].(string) < data[j]["id"].(string) })
	if ctx.Err() != nil {
		reject(503, "model catalog unavailable")
		return
	}
	if err := tx.Commit().Error; err != nil {
		reject(503, "model catalog unavailable")
		return
	}
	if ctx.Err() != nil {
		reject(503, "model catalog unavailable")
		return
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}
