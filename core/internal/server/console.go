package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
)

// getBalance：GET /api/balance —— 当前 org 余额/冻结（Console 仪表盘）。
// 跨所有分片 SUM 归并（OrgBalances）：未分片(n=1)时与单行读逐字一致；分片后聚合各片，
// 无账户行（首笔授信前）SUM 自然返回 0。只读不 EnsureAccount。
func (s *Server) getBalance(c *gin.Context) {
	claims := mustClaims(c)
	posted, held, err := ledger.OrgBalances(s.db.WithContext(c.Request.Context()), claims.OrgID, ledger.AccountUserCredit)
	if err != nil {
		fail(c, http.StatusInternalServerError, "balance query failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"balance": posted, "held": held})
}

// getLedger：GET /api/ledger?page= —— 当前 org 资金流水（统一账本，钱包页用）。
// 充值/赠送/消费/释放全部进 ledger_journal，是用户侧唯一权威流水。delta 带符号，
// 入账为正、扣减为负；balance_after 是每笔后的余额快照。
// JournalEntry 结构未映射 created_at，这里用原始 SQL 取齐展示字段。
func (s *Server) getLedger(c *gin.Context) {
	claims := mustClaims(c)
	rdb := s.db.WithContext(c.Request.Context())
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	const pageSize = 20

	// 跨所有分片账户的流水（account_id IN 各分片 id）：未分片(n=1)时即单账户，逐字一致。
	ids, err := ledger.ShardAccountIDs(rdb, claims.OrgID, ledger.AccountUserCredit)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ledger query failed")
		return
	}
	if len(ids) == 0 { // 首笔授信前无账户行
		c.JSON(http.StatusOK, gin.H{"total": 0, "page": page, "page_size": pageSize, "items": []any{}})
		return
	}
	var total int64
	rdb.Table("ledger_journal").Where("account_id IN ?", ids).Count(&total)
	var rows []struct {
		ID           uint64          `json:"id"`
		Kind         string          `json:"kind"`
		Delta        decimal.Decimal `json:"delta"`
		BalanceAfter decimal.Decimal `json:"balance_after"`
		RefType      string          `json:"ref_type"`
		RefID        string          `json:"ref_id"`
		Reason       string          `json:"reason"`
		CreatedAt    time.Time       `json:"created_at"` // RFC3339；前端 new Date() 直接可解析（修复 ZZ 崩溃）
	}
	rdb.Raw(`SELECT id, kind, delta, balance_after, ref_type, ref_id, reason, created_at
		FROM ledger_journal WHERE account_id IN ?
		ORDER BY id DESC LIMIT ? OFFSET ?`, ids, pageSize, (page-1)*pageSize).Scan(&rows)
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "page_size": pageSize, "items": rows})
}

// getPricing：GET /api/pricing —— 公开模型价目（无需登录；营销站/模型市场页同源数据）。
// 与 /v1/models 同一可见性规则：已定价且有活跃渠道才列出（孤儿定价隐藏）。
func (s *Server) getPricing(c *gin.Context) {
	rdb := s.db.WithContext(c.Request.Context())
	var prices []relay.ModelPrice
	if err := rdb.Order("model").Find(&prices).Error; err != nil {
		fail(c, http.StatusInternalServerError, "pricing query failed")
		return
	}
	var channels []relay.Channel
	if err := rdb.Where("status = 1").Find(&channels).Error; err != nil {
		fail(c, http.StatusInternalServerError, "pricing query failed")
		return
	}
	servedBy := func(model string) (string, bool) {
		for i := range channels {
			if channels[i].ServesModel(model) {
				return channels[i].Provider, true
			}
		}
		return "", false
	}
	metaMap := s.loadModelMetaMap()
	out := make([]gin.H, 0, len(prices))
	for _, p := range prices {
		provider, ok := servedBy(p.Model)
		if !ok {
			continue
		}
		row := gin.H{
			"model":             p.Model,
			"provider":          provider,
			"input_usd_per_1m":  p.InputUSDPer1M,
			"output_usd_per_1m": p.OutputUSDPer1M,
			"image_usd":         p.ImageUSD,
		}
		// 非零才带（批 D-2 模型详情完整价目）：按次固定价（MJ/转写类）与缓存输入折扣价
		if p.PerCallUSD.IsPositive() {
			row["per_call_usd"] = p.PerCallUSD
		}
		if p.CachedInputUSDPer1M.IsPositive() {
			row["cached_input_usd_per_1m"] = p.CachedInputUSDPer1M
		}
		if meta, ok := metaMap[p.Model]; ok { // 模型元数据（gap P2-D）：有则前端用真实厂商/分类，无则回退按名猜
			row["vendor"] = meta.Vendor
			row["category"] = meta.Category
			row["tags"] = meta.Tags
			row["context_length"] = meta.ContextLength
			row["icon"] = meta.Icon
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"models": out})
}

