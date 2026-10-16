package server

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ledger"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func (s *Server) registerFundActivities(org *gin.RouterGroup) {
	org.GET("/fund-activities", s.listFundActivities)
	org.GET("/fund-activities/:id", s.getFundActivity)
	org.GET("/fund-summary", s.getFundSummary)
	org.GET("/fund-exports", s.exportFundActivities)
}
func fundFilter(c *gin.Context, limit int) (ledger.ActivityFilter, error) {
	f := ledger.ActivityFilter{Limit: limit, Type: c.Query("type"), Direction: c.Query("direction"), Status: c.Query("status"), RefID: c.Query("ref_id")}
	if len(f.Type) > 64 || len(f.RefID) > 160 {
		return f, ledger.ErrInvalidInput
	}
	if f.Direction != "" && !containsPrice([]string{"credit", "debit", "freeze", "unfreeze"}, f.Direction) {
		return f, ledger.ErrInvalidInput
	}
	if f.Status != "" && !containsPrice([]string{"success", "failed", "processing", "reversed"}, f.Status) {
		return f, ledger.ErrInvalidInput
	}
	if raw := c.Query("before"); raw != "" {
		n, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || n == 0 {
			return f, ledger.ErrInvalidInput
		}
		f.Before = n
	}
	for _, entry := range []struct {
		key string
		dst **time.Time
	}{{"from", &f.From}, {"until", &f.Until}} {
		if raw := c.Query(entry.key); raw != "" {
			v, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return f, e
			}
			v = v.UTC()
			*entry.dst = &v
		}
	}
	if f.From != nil && f.Until != nil && !f.From.Before(*f.Until) {
		return f, ledger.ErrInvalidInput
	}
	return f, nil
}
func (s *Server) listFundActivities(c *gin.Context) {
	f, e := fundFilter(c, 51)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	if c.DefaultQuery("view", "movements") == "operations" {
		rows, e := ledger.ListOperations(s.db, org, f)
		if e != nil {
			fail(c, 503, "operation query unavailable")
			return
		}
		var next uint64
		if len(rows) > 50 {
			next = rows[49].ID
			rows = rows[:50]
		}
		c.JSON(200, gin.H{"items": rows, "next_before": next, "view": "operations", "amount_semantics": "requested amount is not a debit"})
		return
	}
	if c.DefaultQuery("view", "movements") != "movements" {
		fail(c, 422, "invalid activity view")
		return
	}
	rows, e := ledger.ListMovements(s.db, org, f)
	if e != nil {
		fail(c, 503, "movement query unavailable")
		return
	}
	var next uint64
	if len(rows) > 50 {
		next = rows[49].ID
		rows = rows[:50]
	}
	c.JSON(200, gin.H{"items": rows, "next_before": next, "view": "movements", "amount_semantics": "committed deltas; freezing is not consumption"})
}
func (s *Server) getFundActivity(c *gin.Context) {
	id, e := strconv.ParseUint(c.Param("id"), 10, 64)
	if e != nil || id == 0 {
		fail(c, 404, "activity not found")
		return
	}
	op, rows, events, e := ledger.OperationDetail(s.db, mustClaims(c).OrgID, id)
	if e != nil {
		fail(c, 404, "activity not found")
		return
	}
	c.JSON(200, gin.H{"operation": op, "movements": rows, "events": events})
}
func (s *Server) getFundSummary(c *gin.Context) {
	f, e := fundFilter(c, 1)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	var posted, held decimal.Decimal
	sums := []struct {
		Direction      string          `json:"direction"`
		Amount         decimal.Decimal `json:"amount"`
		PostedDelta    decimal.Decimal `json:"posted_delta"`
		HeldDelta      decimal.Decimal `json:"held_delta"`
		AvailableDelta decimal.Decimal `json:"available_delta"`
	}{}
	e = s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		posted, held, e = ledger.OrgBalances(tx, org, ledger.AccountUserCredit)
		if e != nil {
			return e
		}
		q := tx.Model(&ledger.Movement{}).Where("org_id = ? AND account_type = ? AND type <> ?", org, ledger.AccountUserCredit, "internal_transfer")
		if f.From != nil {
			q = q.Where("occurred_at >= ?", *f.From)
		}
		if f.Until != nil {
			q = q.Where("occurred_at < ?", *f.Until)
		}
		if f.Type != "" {
			q = q.Where("type = ?", f.Type)
		}
		if f.Direction != "" {
			q = q.Where("direction = ?", f.Direction)
		}
		if f.RefID != "" {
			q = q.Where("ref_id = ?", f.RefID)
		}
		if f.Status != "" {
			q = q.Where("status = ?", f.Status)
		}
		return q.Select("direction, SUM(amount) AS amount, SUM(posted_delta) AS posted_delta, SUM(held_delta) AS held_delta, SUM(available_delta) AS available_delta").Group("direction").Scan(&sums).Error
	})
	if e != nil {
		fail(c, 503, "fund summary unavailable")
		return
	}
	c.JSON(200, gin.H{"currency": "USD", "posted_balance": posted.String(), "held_amount": held.String(), "available_balance": posted.Sub(held).String(), "movements": sums, "balance_scope": "current organization, all user-credit shards", "history_scope": "tracked committed movements; earlier history may be incomplete", "balance_after_semantics": "per-operation organization balance is not reconstructed"})
}
func fundCSVSafe(s string) string {
	if len(s) > 0 && strings.ContainsAny(s[:1], "=+-@\t\r\n") {
		return "'" + s
	}
	return s
}
func (s *Server) exportFundActivities(c *gin.Context) {
	f, e := fundFilter(c, 1000)
	if e != nil {
		fail(c, 422, "invalid activity filters")
		return
	}
	org := mustClaims(c).OrgID
	rows, e := ledger.ListMovements(s.db, org, f)
	if e != nil {
		fail(c, 503, "export unavailable")
		return
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"id", "operation_id", "time", "type", "direction", "status", "currency", "amount", "posted_delta", "held_delta", "available_delta", "ref_type", "ref_id", "quality"})
	for _, r := range rows {
		_ = w.Write([]string{fmt.Sprint(r.ID), fmt.Sprint(r.OperationID), r.CreatedAt.Format(time.RFC3339), fundCSVSafe(r.Type), r.Direction, r.Status, r.Currency, r.Amount.String(), r.PostedDelta.String(), r.HeldDelta.String(), r.AvailableDelta.String(), fundCSVSafe(r.RefType), fundCSVSafe(r.RefID), r.Quality})
	}
	w.Flush()
	if w.Error() != nil {
		fail(c, 503, "export unavailable")
		return
	}
	// JSON transport permits authenticated downloads through the existing API
	// helper; the caller saves csv verbatim and can continue with next_before.
	var next uint64
	if len(rows) == 1000 {
		next = rows[len(rows)-1].ID
	}
	c.JSON(200, gin.H{"filename": "fund-movements.csv", "csv": b.String(), "rows": len(rows), "next_before": next, "limit": 1000})
}
