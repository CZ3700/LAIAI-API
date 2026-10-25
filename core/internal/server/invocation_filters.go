package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

type invocationQuerySpec struct {
	Filter          invocation.QueryFilter
	Page, PageSize  int
	Timezone        string
	DisplayCurrency string
}

// Field and Reason are server-owned constants, never rejected query values.
type invocationFilterError struct{ Field, Reason string }

var invocationAbsoluteTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func (e *invocationFilterError) Error() string { return "invalid invocation query" }

func invalidInvocationFilter(field, reason string) error {
	return &invocationFilterError{Field: field, Reason: reason}
}

func parseInvocationQuery(values url.Values, admin bool, now time.Time) (invocationQuerySpec, error) {
	var out invocationQuerySpec
	allowed := map[string]bool{"filter_version": true, "source": true, "time_basis": true, "timezone": true,
		"since": true, "until": true, "model": true, "request_id": true, "api_key_id": true,
		"modality": true, "result": true, "status": true, "is_stream": true,
		"page": true, "page_size": true, "display_currency": true, "org_id": admin, "channel_id": admin}
	for name, entries := range values {
		if !allowed[name] {
			return out, invalidInvocationFilter("query", "unsupported_parameter")
		}
		if len(entries) != 1 {
			return out, invalidInvocationFilter(name, "duplicate_parameter")
		}
	}
	if values.Get("filter_version") != "1" {
		return out, invalidInvocationFilter("filter_version", "unsupported_version")
	}
	for name, expected := range map[string]string{"source": "invocation_log", "time_basis": "recorded_at"} {
		if values.Has(name) && values.Get(name) != expected {
			return out, invalidInvocationFilter(name, "unsupported_value")
		}
	}
	out.Timezone = "UTC"
	if values.Has("timezone") {
		out.Timezone = values.Get("timezone")
	}
	if out.Timezone == "" || out.Timezone == "Local" || len(out.Timezone) > 64 {
		return out, invalidInvocationFilter("timezone", "invalid_timezone")
	}
	location, err := time.LoadLocation(out.Timezone)
	if err != nil {
		return out, invalidInvocationFilter("timezone", "invalid_timezone")
	}
	out.Filter.Timezone = out.Timezone
	if values.Has("since") != values.Has("until") {
		return out, invalidInvocationFilter("since", "paired_range_required")
	}
	if values.Has("since") {
		since, until := values.Get("since"), values.Get("until")
		dateOnly := len(since) == len("2006-01-02")
		if dateOnly != (len(until) == len("2006-01-02")) {
			return out, invalidInvocationFilter("until", "mixed_time_formats")
		}
		parse := func(raw string) (time.Time, error) {
			if dateOnly {
				return time.ParseInLocation("2006-01-02", raw, location)
			}
			if len(raw) > 40 || !invocationAbsoluteTime.MatchString(raw) {
				return time.Time{}, invalidInvocationFilter("since", "invalid_time")
			}
			return time.Parse(time.RFC3339Nano, raw)
		}
		if out.Filter.Since, err = parse(since); err != nil {
			return out, invalidInvocationFilter("since", "invalid_time")
		}
		if out.Filter.Until, err = parse(until); err != nil {
			return out, invalidInvocationFilter("until", "invalid_time")
		}
	} else {
		local := now.In(location)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
		out.Filter.Since, out.Filter.Until = start.AddDate(0, 0, -6), start.AddDate(0, 0, 1)
	}
	if out.Filter.Since.Year() < 1000 || out.Filter.Until.Year() > 9999 ||
		!out.Filter.Since.Before(out.Filter.Until) || out.Filter.Until.After(out.Filter.Since.In(location).AddDate(0, 0, 90)) {
		return out, invalidInvocationFilter("until", "invalid_range")
	}
	out.Filter.Since, out.Filter.Until = out.Filter.Since.UTC(), out.Filter.Until.UTC()
	if out.Filter.Since.Year() < 1000 || out.Filter.Until.Year() > 9999 {
		return out, invalidInvocationFilter("until", "invalid_range")
	}
	for name, maxLength := range map[string]int{"model": 128, "request_id": 32} {
		if !values.Has(name) {
			continue
		}
		value := values.Get(name)
		if value == "" || utf8.RuneCountInString(value) > maxLength || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return out, invalidInvocationFilter(name, "invalid_text")
		}
		if name == "model" {
			out.Filter.Model = value
		} else {
			out.Filter.RequestID = value
		}
	}
	for name, dst := range map[string]*uint64{"api_key_id": &out.Filter.APIKeyID, "org_id": &out.Filter.OrgID, "channel_id": &out.Filter.ChannelID} {
		if values.Has(name) {
			id, e := invocationPositiveInteger(values.Get(name))
			if e != nil {
				return out, invalidInvocationFilter(name, "positive_integer_required")
			}
			*dst = id
		}
	}
	if values.Has("modality") {
		v := values.Get("modality")
		switch v {
		case "", invocation.ModalityChat, invocation.ModalityEmbeddings, invocation.ModalityRerank,
			invocation.ModalityImage, invocation.ModalityAudioSpeech, invocation.ModalityAudioSTT,
			invocation.ModalityRealtime, invocation.ModalityVideo, invocation.ModalityMusic:
			out.Filter.Modality = &v
		default:
			return out, invalidInvocationFilter("modality", "unsupported_value")
		}
	}
	if values.Has("result") && values.Has("status") {
		return out, invalidInvocationFilter("status", "conflicting_parameters")
	}
	if values.Has("result") {
		switch values.Get("result") {
		case "all":
		case "success", "failed":
			out.Filter.Result = values.Get("result")
		default:
			return out, invalidInvocationFilter("result", "unsupported_value")
		}
	}
	if values.Has("status") {
		switch values.Get("status") {
		case invocation.StatusSuccess, invocation.StatusUpstreamError, invocation.StatusNoChannel, invocation.StatusInsufficientBalance:
			out.Filter.Status = values.Get("status")
		default:
			return out, invalidInvocationFilter("status", "unsupported_value")
		}
	}
	if values.Has("is_stream") {
		v := values.Get("is_stream")
		if v != "true" && v != "false" {
			return out, invalidInvocationFilter("is_stream", "boolean_required")
		}
		b := v == "true"
		out.Filter.IsStream = &b
	}
	out.Page, out.PageSize = 1, 20
	for name, dst := range map[string]*int{"page": &out.Page, "page_size": &out.PageSize} {
		if values.Has(name) {
			n, e := invocationPositiveInteger(values.Get(name))
			if e != nil || n > 100001 { // Bound before converting to native int or multiplying.
				return out, invalidInvocationFilter(name, "invalid_page")
			}
			*dst = int(n)
		}
	}
	if out.PageSize != 20 && out.PageSize != 50 && out.PageSize != 100 {
		return out, invalidInvocationFilter("page_size", "unsupported_value")
	}
	if out.Page-1 > 100000/out.PageSize {
		return out, invalidInvocationFilter("page", "offset_limit_exceeded")
	}
	out.DisplayCurrency = "USD"
	if values.Has("display_currency") {
		out.DisplayCurrency = values.Get("display_currency")
	}
	if out.DisplayCurrency != "USD" && out.DisplayCurrency != "CNY" {
		return out, invalidInvocationFilter("display_currency", "unsupported_value")
	}
	return out, nil
}

func invocationPositiveInteger(raw string) (uint64, error) {
	if raw == "" || len(raw) > 20 {
		return 0, invalidInvocationFilter("query", "positive_integer_required")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, invalidInvocationFilter("query", "positive_integer_required")
		}
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		return 0, invalidInvocationFilter("query", "positive_integer_required")
	}
	return v, nil
}

type invocationNormalizedFilters struct {
	Source    string    `json:"source"`
	TimeBasis string    `json:"time_basis"`
	Timezone  string    `json:"timezone"`
	Since     time.Time `json:"since"`
	Until     time.Time `json:"until"`
	Model     string    `json:"model,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	APIKeyID  uint64    `json:"api_key_id,omitempty"`
	Modality  *string   `json:"modality,omitempty"`
	Result    string    `json:"result,omitempty"`
	Status    string    `json:"status,omitempty"`
	IsStream  *bool     `json:"is_stream,omitempty"`
	OrgID     uint64    `json:"org_id,omitempty"`
	ChannelID uint64    `json:"channel_id,omitempty"`
}

func (q invocationQuerySpec) normalized() invocationNormalizedFilters {
	f := q.Filter
	return invocationNormalizedFilters{Source: "invocation_log", TimeBasis: "recorded_at", Timezone: q.Timezone,
		Since: f.Since, Until: f.Until, Model: f.Model, RequestID: f.RequestID, APIKeyID: f.APIKeyID,
		Modality: f.Modality, Result: f.Result, Status: f.Status, IsStream: f.IsStream, OrgID: f.OrgID, ChannelID: f.ChannelID}
}

// A record-set hash includes server-authorized scope, never pagination/FX.
// It is not a response cache key, authorization token, or historical snapshot.
func (q invocationQuerySpec) filterHash(admin bool, orgID uint64) string {
	scope := "organization"
	if admin {
		scope, orgID = "platform", 0
	}
	body, _ := json.Marshal(struct {
		Version int                         `json:"version"`
		Scope   string                      `json:"scope"`
		OrgID   uint64                      `json:"authorized_org_id"`
		Filters invocationNormalizedFilters `json:"filters"`
	}{1, scope, orgID, q.normalized()})
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:])
}
