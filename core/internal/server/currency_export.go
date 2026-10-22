package server

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// Export snapshots are presentation metadata, never ledger conversion rates.
// A nil snapshot preserves the original USD CSV contract for existing clients.
type exportMoneySnapshot struct {
	Currency   string    `json:"display_currency"`
	CNYPerUSD  string    `json:"cny_per_usd"`
	SnapshotAt time.Time `json:"rate_snapshot_at"`
}

func (s *Server) newExportMoney(currency string) (*exportMoneySnapshot, error) {
	if currency == "" {
		return nil, nil
	}
	if currency != "USD" && currency != "CNY" {
		return nil, errors.New("display_currency must be USD or CNY")
	}
	_, rate := s.effectiveCNYPerUSD()
	return &exportMoneySnapshot{Currency: currency, CNYPerUSD: rate, SnapshotAt: s.now().UTC()}, nil
}

func (m *exportMoneySnapshot) validate() error {
	if m == nil {
		return nil
	}
	if (m.Currency != "USD" && m.Currency != "CNY") || m.SnapshotAt.IsZero() {
		return errors.New("invalid export money snapshot")
	}
	_, err := parseCNYPerUSD(m.CNYPerUSD)
	return err
}

func (m *exportMoneySnapshot) amount(usd decimal.Decimal) string {
	if m == nil || m.Currency == "USD" {
		return usd.String()
	}
	// Snapshots are validated at creation and again on worker resume.
	rate, _ := decimal.NewFromString(m.CNYPerUSD)
	return usd.Mul(rate).String()
}

func (m *exportMoneySnapshot) costHeader(header []string) []string {
	if m == nil {
		return header
	}
	return append(header, "display_currency", "cost_display", "cny_per_usd", "rate_snapshot_at")
}

func (m *exportMoneySnapshot) costRow(row []string, usd decimal.Decimal) []string {
	if m == nil {
		return row
	}
	return append(row, m.Currency, m.amount(usd), m.CNYPerUSD, m.SnapshotAt.Format(time.RFC3339Nano))
}

func (m *exportMoneySnapshot) headers(c *gin.Context, filename string) {
	if m != nil {
		filename += "-" + m.Currency
		c.Header("X-Display-Currency", m.Currency)
		c.Header("X-CNY-Per-USD", m.CNYPerUSD)
		c.Header("X-Rate-Snapshot-At", m.SnapshotAt.Format(time.RFC3339Nano))
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`.csv"`)
}
