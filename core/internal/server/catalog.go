package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

// adminModelCatalog：GET /api/admin/model-catalog —— 上游模型目录。
// 把所有渠道声明的模型(Channel.Models CSV)聚合成「模型 → 承载它的渠道列表」，
// 并标注是否已配置定价。定价管理时一眼看清：哪些上游模型已上货、在哪些渠道、是否漏配价。
func (s *Server) adminModelCatalog(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var channels []relay.Channel
	rdb.Order("id").Find(&channels)
	var prices []relay.ModelPrice
	rdb.Find(&prices)
	priceMap := make(map[string]relay.ModelPrice, len(prices))
	for _, p := range prices {
		priceMap[p.Model] = p
	}

	type chRef struct {
		ID       uint64 `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Status   int8   `json:"status"`
	}
	type modelRow struct {
		Model          string           `json:"model"`
		Channels       []chRef          `json:"channels"`
		Priced         bool             `json:"priced"`
		InputUSDPer1M  *decimal.Decimal `json:"input_usd_per_1m,omitempty"`
		OutputUSDPer1M *decimal.Decimal `json:"output_usd_per_1m,omitempty"`
	}
	catalog := map[string]*modelRow{}
	for _, ch := range channels {
		for _, m := range strings.Split(ch.Models, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			row := catalog[m]
			if row == nil {
				row = &modelRow{Model: m, Channels: []chRef{}}
				if p, ok := priceMap[m]; ok {
					row.Priced = true
					in, out := p.InputUSDPer1M, p.OutputUSDPer1M
					row.InputUSDPer1M, row.OutputUSDPer1M = &in, &out
				}
				catalog[m] = row
			}
			row.Channels = append(row.Channels, chRef{ch.ID, ch.Name, ch.Provider, ch.Status})
		}
	}
	out := make([]*modelRow, 0, len(catalog))
	for _, r := range catalog {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })

	// 统计：上货模型总数 / 已定价 / 漏配价（运营一眼看缺口）
	priced := 0
	for _, r := range out {
		if r.Priced {
			priced++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"models": out, "total": len(out), "priced": priced, "unpriced": len(out) - priced,
	})
}
