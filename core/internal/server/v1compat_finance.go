package server

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
)

func (s *Server) v1FinanceRechargeTypes(c *gin.Context) {
	c.JSON(http.StatusOK, []string{"online_recharge", "admin_add", "admin_deduct", "admin_override", "gift"})
}

func (s *Server) v1FinanceRecharges(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	pageSize = min(100, max(1, pageSize))

	type ledgerJoinRow struct {
		ID         uint64          `gorm:"column:id"`
		OrgID      uint64          `gorm:"column:org_id"`
		Kind       string          `gorm:"column:kind"`
		Delta      decimal.Decimal `gorm:"column:delta"`
		RefType    string          `gorm:"column:ref_type"`
		RefID      string          `gorm:"column:ref_id"`
		Reason     string          `gorm:"column:reason"`
		CreatedAt  time.Time       `gorm:"column:created_at"`
		OwnerID    uint64          `gorm:"column:owner_id"`
		OwnerEmail string          `gorm:"column:owner_email"`
		OwnerName  string          `gorm:"column:owner_name"`
	}

	baseSQL := `FROM ledger_journal j
		JOIN ledger_accounts a ON a.id = j.account_id AND a.type = 'user_credit'
		LEFT JOIN organizations o ON o.id = a.org_id
		LEFT JOIN org_members om ON om.org_id = a.org_id AND om.role = 'owner'
		LEFT JOIN users u ON u.id = om.user_id
		WHERE j.kind IN ('credit', 'grant', 'debit')
		AND COALESCE(j.ref_type, '') NOT IN ('video', 'task', 'rebalance')`

	var args []any
	if kw := strings.TrimSpace(c.Query("user_id")); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		baseSQL += ` AND (u.email LIKE ? ESCAPE '\' OR u.display_name LIKE ? ESCAPE '\' OR CAST(u.id AS TEXT) = ? OR j.ref_id LIKE ? ESCAPE '\')`
		args = append(args, pat, pat, kw, pat)
	}
	if rt := strings.TrimSpace(c.Query("recharge_type")); rt != "" {
		switch rt {
		case "online_recharge":
			baseSQL += " AND j.kind = 'credit' AND j.ref_type IN ('topup', 'payment')"
		case "admin_add":
			baseSQL += " AND j.kind = 'grant' AND j.ref_type IN ('admin_grant', 'admin_override')"
		case "admin_deduct", "admin_override":
			baseSQL += " AND j.kind = 'debit' AND j.ref_type IN ('admin_debit', 'admin_override')"
		case "gift":
			baseSQL += " AND j.kind = 'grant' AND j.ref_type IN ('topup_bonus', 'welcome_grant', 'invite_grant')"
		}
	}
	if startUnix, endUnix, ok := parseFinanceUnixRange(c); ok {
		if startUnix > 0 {
			baseSQL += " AND j.created_at >= ?"
			args = append(args, time.Unix(startUnix, 0).UTC())
		}
		if endUnix > 0 {
			baseSQL += " AND j.created_at <= ?"
			args = append(args, time.Unix(endUnix, 0).UTC())
		}
	}

	var total int64
	if err := rdb.Raw("SELECT COUNT(*) "+baseSQL, args...).Scan(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query finance recharges failed")
		return
	}
	var totalAmt decimal.Decimal
	_ = rdb.Raw("SELECT COALESCE(SUM(j.delta), 0) "+baseSQL, args...).Scan(&totalAmt).Error

	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	var rows []ledgerJoinRow
	if err := rdb.Raw(`SELECT j.id, a.org_id, j.kind, j.delta, j.ref_type, j.ref_id, j.reason, j.created_at,
		COALESCE(u.id, 0) AS owner_id, COALESCE(u.email, '') AS owner_email, COALESCE(u.display_name, '') AS owner_name `+
		baseSQL+` ORDER BY j.id DESC LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query finance recharges failed")
		return
	}

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		rt := mapLedgerKindToRechargeType(r.Kind, r.RefType, r.Delta)
		amt, _ := r.Delta.Float64()
		out = append(out, gin.H{
			"id":            r.ID,
			"user_id":       strconv.FormatUint(cmp.Or(r.OwnerID, r.OrgID), 10),
			"username":      cmp.Or(r.OwnerName, r.OwnerEmail, "org#"+strconv.FormatUint(r.OrgID, 10)),
			"uid":           strconv.FormatUint(cmp.Or(r.OwnerID, r.OrgID), 10),
			"amount":        amt,
			"recharge_type": rt,
			"order_no":      r.RefID,
			"remark":        r.Reason,
			"operator":      cmp.Or(r.RefType, "system"),
			"fee_amount":    0.0,
			"created_at":    r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	totF, _ := totalAmt.Float64()
	c.JSON(http.StatusOK, gin.H{
		"data":                    out,
		"total":                   total,
		"total_amount":            totF,
		"total_fee_amount":        0,
		"voucher_writeoff_amount": 0,
	})
}

func mapLedgerKindToRechargeType(kind, refType string, delta decimal.Decimal) string {
	switch kind {
	case ledger.KindCredit:
		if refType == "topup" || refType == "payment" {
			return "online_recharge"
		}
		if delta.IsNegative() {
			return "admin_deduct"
		}
		return "admin_override"
	case ledger.KindGrant:
		if refType == "admin_grant" || refType == "admin_override" {
			return "admin_add"
		}
		return "gift"
	case ledger.KindDebit:
		return "admin_deduct"
	default:
		if delta.IsNegative() {
			return "admin_deduct"
		}
		return "admin_override"
	}
}

func (s *Server) v1FinanceOrders(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	page, _ := strconv.Atoi(cmp.Or(c.Query("page"), "1"))
	page = max(1, page)
	pageSize, _ := strconv.Atoi(cmp.Or(c.Query("per_page"), c.Query("page_size"), "20"))
	pageSize = min(100, max(1, pageSize))

	baseSQL := `FROM topup_orders t
		LEFT JOIN org_members om ON om.org_id = t.org_id AND om.role = 'owner'
		LEFT JOIN users u ON u.id = om.user_id
		WHERE 1=1`
	var args []any
	if kw := strings.TrimSpace(c.Query("search")); kw != "" {
		pat := "%" + escapeSQLLike(kw) + "%"
		baseSQL += ` AND (t.order_no LIKE ? ESCAPE '\' OR t.trade_no LIKE ? ESCAPE '\' OR u.email LIKE ? ESCAPE '\' OR CAST(t.org_id AS TEXT) = ?)`
		args = append(args, pat, pat, pat, kw)
	}
	if st := strings.TrimSpace(c.Query("status")); st != "" {
		switch st {
		case "pending":
			baseSQL += " AND t.status = ?"
			args = append(args, topupPending)
		case "paid":
			baseSQL += " AND t.status = ?"
			args = append(args, topupPaid)
		case "closed":
			baseSQL += " AND t.status NOT IN (?, ?)"
			args = append(args, topupPending, topupPaid)
		}
	}
	if pm := strings.TrimSpace(c.Query("payment_method")); pm != "" {
		baseSQL += " AND t.provider = ?"
		args = append(args, pm)
	}
	if startUnix, endUnix, ok := parseFinanceUnixRange(c); ok {
		if startUnix > 0 {
			baseSQL += " AND t.created_at >= ?"
			args = append(args, startUnix)
		}
		if endUnix > 0 {
			baseSQL += " AND t.created_at <= ?"
			args = append(args, endUnix)
		}
	}

	var total int64
	if err := rdb.Raw("SELECT COUNT(*) "+baseSQL, args...).Scan(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query orders failed")
		return
	}
	var totalAmt decimal.Decimal
	_ = rdb.Raw("SELECT COALESCE(SUM(t.amount_usd), 0) "+baseSQL, args...).Scan(&totalAmt).Error

	type orderJoinRow struct {
		ID          uint64          `gorm:"column:id"`
		OrderNo     string          `gorm:"column:order_no"`
		OrgID       uint64          `gorm:"column:org_id"`
		Provider    string          `gorm:"column:provider"`
		AmountUSD   decimal.Decimal `gorm:"column:amount_usd"`
		PayAmount   decimal.Decimal `gorm:"column:pay_amount"`
		PayCurrency string          `gorm:"column:pay_currency"`
		Status      int8            `gorm:"column:status"`
		TradeNo     string          `gorm:"column:trade_no"`
		CreatedAt   int64           `gorm:"column:created_at"`
		PaidAt      int64           `gorm:"column:paid_at"`
		OwnerID     uint64          `gorm:"column:owner_id"`
		OwnerEmail  string          `gorm:"column:owner_email"`
		OwnerName   string          `gorm:"column:owner_name"`
	}
	pageArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	var rows []orderJoinRow
	if err := rdb.Raw(`SELECT t.id, t.order_no, t.org_id, t.provider, t.amount_usd, t.pay_amount, t.pay_currency,
		t.status, t.trade_no, t.created_at, t.paid_at,
		COALESCE(u.id, 0) AS owner_id, COALESCE(u.email, '') AS owner_email, COALESCE(u.display_name, '') AS owner_name `+
		baseSQL+` ORDER BY t.id DESC LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "query orders failed")
		return
	}

	methodsSet := map[string]struct{}{}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		methodsSet[r.Provider] = struct{}{}
		amtF, _ := r.AmountUSD.Float64()
		payF, _ := r.PayAmount.Float64()
		statusStr := "closed"
		if r.Status == topupPending {
			statusStr = "pending"
		} else if r.Status == topupPaid {
			statusStr = "paid"
		}
		var paidAt any
		if r.PaidAt > 0 {
			paidAt = time.Unix(r.PaidAt, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, gin.H{
			"id":             r.ID,
			"out_trade_no":   r.OrderNo,
			"user_id":        strconv.FormatUint(cmp.Or(r.OwnerID, r.OrgID), 10),
			"username":       cmp.Or(r.OwnerName, r.OwnerEmail, "org#"+strconv.FormatUint(r.OrgID, 10)),
			"uid":            strconv.FormatUint(cmp.Or(r.OwnerID, r.OrgID), 10),
			"payment_method": r.Provider,
			"amount":         amtF,
			"fee_amount":     0.0,
			"pay_amount":     payF,
			"pay_currency":   r.PayCurrency,
			"status":         statusStr,
			"trade_no":       r.TradeNo,
			"created_at":     time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339),
			"paid_at":        paidAt,
		})
	}
	methods := make([]string, 0, len(methodsSet))
	for m := range methodsSet {
		if m != "" {
			methods = append(methods, m)
		}
	}
	slices.Sort(methods)
	totF, _ := totalAmt.Float64()
	c.JSON(http.StatusOK, gin.H{
		"data":             out,
		"total":            total,
		"total_amount":     totF,
		"total_fee_amount": 0,
		"payment_methods":  methods,
	})
}

func (s *Server) v1FinanceOrderAction(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "请在支付渠道对账后通过系统调账确认"})
}

func (s *Server) v1FinanceDailyStats(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := s.readOnly().WithContext(ctx)

	endDay := s.now().UTC()
	startDay := endDay.AddDate(0, 0, -29)
	if raw := strings.TrimSpace(c.Query("start_date")); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			startDay = parsed.UTC()
		}
	}
	if raw := strings.TrimSpace(c.Query("end_date")); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			endDay = parsed.UTC()
		}
	}
	if endDay.Before(startDay) {
		endDay = startDay
	}
	days := int(endDay.Sub(startDay).Hours()/24) + 1
	if days > 180 {
		startDay = endDay.AddDate(0, 0, -179)
		days = 180
	}

	sinceDate := startDay.Format("2006-01-02")
	var invRows []invocation.Invocation
	_ = rdb.Model(&invocation.Invocation{}).
		Select("model, api_key_id, org_id, prompt_tokens, completion_tokens, cost_usd, created_at").
		Where("created_at >= ?", sinceDate).Find(&invRows).Error

	var topups []TopupOrder
	_ = rdb.Model(&TopupOrder{}).Where("status = ? AND paid_at >= ?", topupPaid, startDay.Unix()).Find(&topups).Error

	var totalUsers int64
	_ = rdb.Model(&identity.User{}).Count(&totalUsers).Error
	var retainedTotal decimal.Decimal
	_ = rdb.Model(&ledger.Account{}).Where("type = ?", ledger.AccountUserCredit).
		Select("COALESCE(SUM(posted_balance), 0)").Scan(&retainedTotal).Error

	type dayAgg struct {
		requests  int64
		tokens    int64
		cost      decimal.Decimal
		recharge  decimal.Decimal
		tokensSet map[uint64]struct{}
		orgsSet   map[uint64]struct{}
	}
	byDay := make(map[string]*dayAgg, days)
	ensureDay := func(d string) *dayAgg {
		if g, ok := byDay[d]; ok {
			return g
		}
		g := &dayAgg{tokensSet: map[uint64]struct{}{}, orgsSet: map[uint64]struct{}{}}
		byDay[d] = g
		return g
	}
	modelCount := map[string]int64{}
	modelCost := map[string]decimal.Decimal{}
	for _, inv := range invRows {
		d := inv.CreatedAt.UTC().Format("2006-01-02")
		g := ensureDay(d)
		g.requests++
		g.tokens += inv.PromptTokens + inv.CompletionTokens
		g.cost = g.cost.Add(inv.CostUSD)
		if inv.APIKeyID > 0 {
			g.tokensSet[inv.APIKeyID] = struct{}{}
		}
		g.orgsSet[inv.OrgID] = struct{}{}
		modelCount[inv.Model]++
		modelCost[inv.Model] = modelCost[inv.Model].Add(inv.CostUSD)
	}
	for _, tp := range topups {
		d := time.Unix(tp.PaidAt, 0).UTC().Format("2006-01-02")
		g := ensureDay(d)
		g.recharge = g.recharge.Add(tp.AmountUSD)
	}

	retainedF, _ := retainedTotal.Float64()
	dailyStats := make([]gin.H, 0, days)
	for i := range days {
		d := startDay.AddDate(0, 0, i).Format("2006-01-02")
		g := byDay[d]
		var reqs, toks, actTok, actUsr int64
		var costF, rechF float64
		if g != nil {
			reqs = g.requests
			toks = g.tokens
			actTok = int64(len(g.tokensSet))
			actUsr = int64(len(g.orgsSet))
			costF, _ = g.cost.Float64()
			rechF, _ = g.recharge.Float64()
		}
		dailyStats = append(dailyStats, gin.H{
			"date":                  d,
			"total_requests":        reqs,
			"total_tokens":          toks,
			"total_cost":            costF,
			"active_tokens":         actTok,
			"active_users":          actUsr,
			"new_users":             0,
			"total_users":           totalUsers,
			"system_recharge_total": rechF,
			"system_cost_total":     costF,
			"system_retained_total": retainedF,
			"online_recharge":       rechF,
			"daily_system_balance":  rechF - costF,
			"daily_gift_recharge":   0,
			"daily_system_recharge": rechF,
		})
	}

	type ms struct {
		Model     string  `json:"model"`
		Count     int64   `json:"count"`
		TotalCost float64 `json:"total_cost"`
		LastThree []any   `json:"last_three_days"`
	}
	mStats := make([]ms, 0, len(modelCount))
	for m, cnt := range modelCount {
		cf, _ := modelCost[m].Float64()
		mStats = append(mStats, ms{Model: m, Count: cnt, TotalCost: cf, LastThree: []any{}})
	}
	slices.SortFunc(mStats, func(a, b ms) int {
		return cmp.Compare(b.TotalCost, a.TotalCost)
	})
	if len(mStats) > 20 {
		mStats = mStats[:20]
	}

	c.JSON(http.StatusOK, gin.H{
		"daily_stats": dailyStats,
		"model_stats": mStats,
	})
}

func parseFinanceUnixRange(c *gin.Context) (int64, int64, bool) {
	var startUnix, endUnix int64
	if raw := strings.TrimSpace(c.Query("start_time")); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			startUnix = t.Unix()
		}
	}
	if raw := strings.TrimSpace(c.Query("end_time")); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			endUnix = t.Unix()
		}
	}
	if startUnix == 0 && endUnix == 0 {
		return 0, 0, false
	}
	return startUnix, endUnix, true
}

func mustUnmarshalJSONSlice[T any](raw string) []T {
	out := make([]T, 0)
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}
