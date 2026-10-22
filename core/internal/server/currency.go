package server

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/shopspring/decimal"
)

const defaultCNYPerUSD = "7.3"

var currencyRatePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,8})?$`)

func parseCNYPerUSD(raw string) (decimal.Decimal, error) {
	if !currencyRatePattern.MatchString(raw) || len(raw) > 16 {
		return decimal.Zero, errors.New("cny_per_usd must be a decimal with at most 8 fractional digits")
	}
	v, err := decimal.NewFromString(raw)
	if err != nil || v.LessThan(decimal.RequireFromString("0.01")) || v.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, errors.New("cny_per_usd must be between 0.01 and 100")
	}
	return v, nil
}

// The same fixed rate serves presentation, payment methods and new EPay orders.
func (s *Server) effectiveCNYPerUSD() (decimal.Decimal, string) {
	raw := strings.TrimSpace(s.getSetting("cny_per_usd"))
	if raw != "" {
		if v, err := parseCNYPerUSD(raw); err == nil {
			return v, v.String()
		}
		s.Log().Warn("invalid cny_per_usd configuration; using safe default")
	}
	return decimal.RequireFromString(defaultCNYPerUSD), defaultCNYPerUSD
}

func normalizeCurrency(v string) string {
	if v == "CNY" {
		return v
	}
	return "USD"
}

func containsCurrencyID(list string, id uint64) bool {
	for _, v := range strings.Split(list, ",") {
		if strings.TrimSpace(v) == strconv.FormatUint(id, 10) {
			return true
		}
	}
	return false
}

// User-based buckets stay stable across sessions and organizations.
func currencyBucket(userID uint64) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte("display-currency-v1:" + strconv.FormatUint(userID, 10)))
	return h.Sum32() % 100
}

func (s *Server) currencyDisplayAvailable(userID, orgID uint64) bool {
	if !s.getBoolSetting("currency_display_enabled") {
		return false
	}
	if containsCurrencyID(s.getSetting("currency_display_user_ids"), userID) || containsCurrencyID(s.getSetting("currency_display_org_ids"), orgID) {
		return true
	}
	percent, err := strconv.Atoi(s.getSetting("currency_display_percent"))
	return err == nil && percent >= 0 && percent <= 100 && int(currencyBucket(userID)) < percent
}

func validateCurrencySetting(name, value string) (string, error) {
	switch name {
	case "cny_per_usd":
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil
		}
		v, err := parseCNYPerUSD(value)
		if err != nil {
			return "", err
		}
		return v.String(), nil
	case "currency_display_enabled":
		if value != "true" && value != "false" {
			return "", errors.New("currency_display_enabled must be true or false")
		}
	case "currency_display_percent":
		if value != "0" && value != "5" && value != "25" && value != "100" {
			return "", errors.New("currency_display_percent must be 0, 5, 25 or 100")
		}
	case "currency_display_user_ids", "currency_display_org_ids":
		if len(value) > 4096 {
			return "", errors.New("currency rollout allowlist too long")
		}
		if value == "" {
			return value, nil
		}
		for _, id := range strings.Split(value, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
			if err != nil || n == 0 {
				return "", errors.New("currency rollout allowlist must contain positive comma-separated IDs")
			}
		}
	}
	return value, nil
}

func (s *Server) updatePreferences(c *gin.Context) {
	var req struct {
		DisplayCurrency string `json:"display_currency"`
	}
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		fail(c, 400, "invalid preferences")
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(c, 400, "invalid preferences")
		return
	}
	if req.DisplayCurrency != "USD" && req.DisplayCurrency != "CNY" {
		fail(c, 400, "display_currency must be USD or CNY")
		return
	}
	claims := mustClaims(c)
	result := s.db.Model(&identity.User{}).Where("id = ? AND status = ?", claims.UserID, identity.StatusActive).Update("display_currency", req.DisplayCurrency)
	if result.Error != nil {
		fail(c, 500, "save preferences failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"display_currency": req.DisplayCurrency, "currency_display_available": s.currencyDisplayAvailable(claims.UserID, claims.OrgID)})
}
