package video

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
)

func (s *Service) cursor(org uint64, scope, value string) string {
	raw := value + "." + digest(s.Secret, "cursor:v1", fmt.Sprint(org), scope, value)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
func (s *Service) uncursor(org uint64, scope, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 512 {
		return "", ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(cursor)
	if e != nil {
		return "", ErrInvalid
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(digest(s.Secret, "cursor:v1", fmt.Sprint(org), scope, parts[0]))) {
		return "", ErrInvalid
	}
	return parts[0], nil
}
func pageSize(c *gin.Context) (int, error) {
	v := c.DefaultQuery("limit", "20")
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 100 {
		return 0, ErrInvalid
	}
	return n, nil
}

func (s *Service) HandleList(c *gin.Context) {
	org := requestKey(c).OrgID
	limit, e := pageSize(c)
	if e != nil {
		respondError(c, e)
		return
	}
	scope := "tasks/" + c.Query("model") + "/" + c.Query("status")
	cursor, e := s.uncursor(org, scope, c.Query("cursor"))
	if e != nil {
		respondError(c, e)
		return
	}
	q := s.DB.Table("tasks AS t").Select("t.*").Joins("JOIN video_task_states v ON v.task_id=t.task_id AND v.org_id=t.org_id").Where("t.org_id = ? AND t.provider = ? AND v.deleted_at IS NULL", org, Provider)
	if model := c.Query("model"); model != "" {
		q = q.Where("t.model = ?", model)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("t.status = ?", status)
	}
	if cursor != "" {
		id, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			respondError(c, ErrInvalid)
			return
		}
		q = q.Where("t.id > ?", id)
	}
	var rows []task.Task
	if e = q.Order("t.id").Limit(limit + 1).Scan(&rows).Error; e != nil {
		respondError(c, e)
		return
	}
	var next *string
	if len(rows) > limit {
		v := s.cursor(org, scope, fmt.Sprint(rows[limit-1].ID))
		next = &v
		rows = rows[:limit]
	}
	data := make([]TaskView, 0, len(rows))
	for _, t := range rows {
		var st State
		if e = s.DB.Where("org_id = ? AND task_id = ?", org, t.TaskID).First(&st).Error; e != nil {
			respondError(c, e)
			return
		}
		view, err := s.deliveryView(c.Request.Context(), t, st, fmt.Sprint(requestKey(c).ID), false)
		if err != nil {
			respondError(c, err)
			return
		}
		data = append(data, view)
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"data": data, "next_cursor": next})
}

func (s *Service) HandleEvents(c *gin.Context) {
	org := requestKey(c).OrgID
	id := c.Param("id")
	if e := s.owned(org, id); e != nil {
		respondError(c, e)
		return
	}
	limit, e := pageSize(c)
	if e != nil {
		respondError(c, e)
		return
	}
	scope := "events/" + id
	cursor, e := s.uncursor(org, scope, c.Query("cursor"))
	if e != nil {
		respondError(c, e)
		return
	}
	q := s.DB.Where("org_id = ? AND task_id = ?", org, id)
	if cursor != "" {
		seq, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			respondError(c, ErrInvalid)
			return
		}
		q = q.Where("sequence > ?", seq)
	}
	rows := []Event{}
	if e = q.Order("sequence").Limit(limit + 1).Find(&rows).Error; e != nil {
		respondError(c, e)
		return
	}
	var next *string
	if len(rows) > limit {
		v := s.cursor(org, scope, fmt.Sprint(rows[limit-1].Sequence))
		next = &v
		rows = rows[:limit]
	}
	c.JSON(200, gin.H{"data": rows, "next_cursor": next})
}

func (s *Service) HandleBilling(c *gin.Context) {
	org := requestKey(c).OrgID
	id := c.Param("id")
	if e := s.owned(org, id); e != nil {
		respondError(c, e)
		return
	}
	var st State
	var snap videobilling.Snapshot
	var entries []videobilling.BillingEntry
	if e := s.DB.Where("org_id = ? AND task_id = ?", org, id).First(&st).Error; e != nil {
		respondError(c, e)
		return
	}
	if e := s.DB.Where("org_id = ? AND task_id = ?", org, id).First(&snap).Error; e != nil {
		respondError(c, e)
		return
	}
	if e := s.DB.Where("org_id = ? AND task_id = ?", org, id).Order("id").Find(&entries).Error; e != nil {
		respondError(c, e)
		return
	}
	var settled *string
	released := decimal.Zero
	var tokens *int64
	rows := []gin.H{{"entry_id": "be_reserve_" + fmt.Sprint(snap.ID), "type": "reserve", "amount": snap.HoldUSD.StringFixed(8), "currency": "USD", "created_at": snap.CreatedAt}}
	for _, entry := range entries {
		if entry.State == videobilling.Released {
			v := entry.CapturedUSD.StringFixed(8)
			settled = &v
		}
		if entry.State == videobilling.Settled {
			v := entry.CapturedUSD.StringFixed(8)
			settled = &v
			tokens = entry.CompletionTokens
			rows = append(rows, gin.H{"entry_id": "be_capture_" + fmt.Sprint(entry.ID), "type": "capture", "amount": v, "currency": "USD", "created_at": entry.CreatedAt})
		}
		if entry.ReleasedUSD.IsPositive() {
			released = released.Add(entry.ReleasedUSD)
			rows = append(rows, gin.H{"entry_id": "be_release_" + fmt.Sprint(entry.ID), "type": "release", "amount": entry.ReleasedUSD.StringFixed(8), "currency": "USD", "created_at": entry.CreatedAt})
		}
	}
	var adjustments []videobilling.AdjustmentApplication
	if e := s.DB.Where("org_id = ? AND task_id = ?", org, id).Order("id").Find(&adjustments).Error; e != nil {
		respondError(c, e)
		return
	}
	for _, a := range adjustments {
		net := a.NetUSD.StringFixed(8)
		settled = &net
		if a.CompletionTokens != nil {
			tokens = a.CompletionTokens
		}
		if a.CustomerDeltaUSD.IsZero() {
			continue
		}
		typ := "adjustment_debit"
		if a.CustomerDeltaUSD.IsNegative() {
			typ = "adjustment_credit"
		}
		rows = append(rows, gin.H{"entry_id": "be_adjust_" + fmt.Sprint(a.ID), "type": typ, "amount": a.CustomerDeltaUSD.Abs().StringFixed(8), "currency": "USD", "created_at": a.CreatedAt})
	}
	mode := "without_input_video"
	var meter videobilling.MeteringInput
	_ = json.Unmarshal([]byte(snap.MeteringJSON), &meter)
	if meter.InputVideo {
		mode = "with_input_video"
	}
	var usage *UsageView
	if st.ObservedCompletionTokens != nil || st.TotalTokens != nil {
		usage = &UsageView{CompletionTokens: st.ObservedCompletionTokens, TotalTokens: st.TotalTokens}
	}
	c.JSON(200, gin.H{"task_id": id, "state": st.BillingState, "currency": "USD", "reserved_amount": snap.HoldUSD.StringFixed(8), "settled_amount": settled, "released_amount": released.StringFixed(8), "actual_usage": usage, "billable_tokens": tokens, "pricing_version": snap.RuleVersion, "billing_rule_version": snap.FormulaVersion, "metering": publicMetering(meter, snap.MinimumRuleVersion), "pricing_snapshot": gin.H{"price_rule_id": snap.RuleID, "mode": mode, "generation_mode": meter.Mode, "resolution": meter.Resolution, "input_video": meter.InputVideo, "service_tier": meter.ServiceTier, "draft": meter.Draft, "minimum_rule_version": snap.MinimumRuleVersion, "unit": "million_video_tokens", "unit_price_usd_per_1m": snap.RetailUSDPer1M.Mul(snap.EffectiveRatio).String(), "currency": "USD", "rounding_mode": "half_up", "rounding_scale": 8}, "entries": rows})
}

func (s *Service) HandleModels(c *gin.Context) {
	key := requestKey(c)
	if key == nil {
		apiError(c, 403, "forbidden")
		return
	}
	observation, err := s.Catalog(c.Request.Context(), CatalogSubjectRef{OrgID: key.OrgID, KeyID: key.ID})
	if err != nil {
		if errors.Is(err, ErrCatalogDenied) {
			apiError(c, 403, "forbidden")
		} else {
			apiError(c, 503, "provider_unavailable")
		}
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"data": observation.Models, "catalog": gin.H{
		"contract": "official-video-catalog-v1", "checked_at": observation.CheckedAt,
		"assurance": "catalog_observation", "grouped_key_issuance_enabled": false,
		"not_checked": []string{"request_content", "asset_authorization", "balance", "quota", "rate_limit", "concurrency", "provider_health"},
	}})
}
