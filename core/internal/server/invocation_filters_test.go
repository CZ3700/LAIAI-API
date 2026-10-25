package server

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func invocationFilterValues(extra url.Values) url.Values {
	out := url.Values{"filter_version": {"1"}}
	for key, values := range extra {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func TestInvocationFiltersStrictParameters(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		extra url.Values
		admin bool
	}{
		{"version_empty", url.Values{"filter_version": {""}}, false},
		{"version_future", url.Values{"filter_version": {"2"}}, false},
		{"version_duplicate", url.Values{"filter_version": {"1", "1"}}, false},
		{"duplicate_model", url.Values{"model": {"a", "a"}}, false},
		{"unknown_parameter", url.Values{"prompt": {"private body"}}, false},
		{"include_totals_is_not_a_second_opt_in", url.Values{"include_totals": {"true"}}, false},
		{"org_rejected", url.Values{"org_id": {"1"}}, false},
		{"channel_rejected", url.Values{"channel_id": {"1"}}, false},
		{"negative_id", url.Values{"api_key_id": {"-1"}}, false},
		{"signed_id", url.Values{"api_key_id": {"+1"}}, false},
		{"zero_id", url.Values{"org_id": {"0"}}, true},
		{"overflow_id", url.Values{"channel_id": {"18446744073709551616"}}, true},
		{"empty_model", url.Values{"model": {""}}, false},
		{"model_length", url.Values{"model": {strings.Repeat("字", 129)}}, false},
		{"request_id_length", url.Values{"request_id": {strings.Repeat("a", 33)}}, false},
		{"control", url.Values{"request_id": {"id\nsecret"}}, false},
		{"invalid_utf8", url.Values{"model": {string([]byte{0xff})}}, false},
		{"result_status_conflict", url.Values{"result": {"all"}, "status": {"success"}}, false},
		{"unknown_result", url.Values{"result": {"pending"}}, false},
		{"legacy_status_alias", url.Values{"status": {"failed"}}, false},
		{"unknown_modality", url.Values{"modality": {"text"}}, false},
		{"bool_number", url.Values{"is_stream": {"1"}}, false},
		{"bool_empty", url.Values{"is_stream": {""}}, false},
		{"implicit_local", url.Values{"timezone": {"Local"}}, false},
		{"timezone_empty", url.Values{"timezone": {""}}, false},
		{"unknown_timezone", url.Values{"timezone": {"Bogus/Zone"}}, false},
		{"source", url.Values{"source": {"video_tasks"}}, false},
		{"time_basis", url.Values{"time_basis": {"completed_at"}}, false},
		{"page_zero", url.Values{"page": {"0"}}, false},
		{"page_overflow", url.Values{"page": {"18446744073709551615"}}, false},
		{"page_size", url.Values{"page_size": {"21"}}, false},
		{"offset", url.Values{"page": {"5002"}}, false},
		{"currency", url.Values{"display_currency": {"cny"}}, false},
		{"currency_empty", url.Values{"display_currency": {""}}, false},
		{"unpaired_date", url.Values{"since": {"2026-10-01"}}, false},
		{"mixed_time", url.Values{"since": {"2026-10-01"}, "until": {"2026-10-02T00:00:00Z"}}, false},
		{"implicit_offset", url.Values{"since": {"2026-10-01T00:00:00"}, "until": {"2026-10-02T00:00:00"}}, false},
		{"invalid_offset", url.Values{"since": {"2026-10-01T00:00:00+24:00"}, "until": {"2026-10-02T00:00:00+24:00"}}, false},
		{"comma_fraction", url.Values{"since": {"2026-10-01T00:00:00,1Z"}, "until": {"2026-10-02T00:00:00Z"}}, false},
		{"invalid_date", url.Values{"since": {"2026-02-30"}, "until": {"2026-03-05"}}, false},
		{"empty_range", url.Values{"since": {"2026-10-01"}, "until": {"2026-10-01"}}, false},
		{"range_91_days", url.Values{"since": {"2026-01-01"}, "until": {"2026-04-02"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseInvocationQuery(invocationFilterValues(test.extra), test.admin, now)
			var field *invocationFilterError
			if !errors.As(err, &field) || field.Field == "" || field.Reason == "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected safe field validation failure, got %v", err)
			}
		})
	}
}

func TestInvocationFiltersCalendarDefaultsAndBounds(t *testing.T) {
	now := time.Date(2026, 10, 26, 18, 25, 0, 0, time.UTC)
	q, err := parseInvocationQuery(invocationFilterValues(url.Values{"timezone": {"Asia/Shanghai"}}), false, now)
	if err != nil || q.Filter.Since.Format(time.RFC3339) != "2026-10-20T16:00:00Z" || q.Filter.Until.Format(time.RFC3339) != "2026-10-27T16:00:00Z" || q.DisplayCurrency != "USD" || q.Page != 1 || q.PageSize != 20 {
		t.Fatal("incorrect seven calendar days", q, err)
	}
	for _, test := range []struct {
		since, until string
		hours        int
	}{
		{"2026-03-08", "2026-03-09", 23},
		{"2026-11-01", "2026-11-02", 25},
	} {
		q, err := parseInvocationQuery(invocationFilterValues(url.Values{"timezone": {"America/New_York"}, "since": {test.since}, "until": {test.until}}), false, now)
		if err != nil || q.Filter.Until.Sub(q.Filter.Since) != time.Duration(test.hours)*time.Hour {
			t.Fatal("DST date range is not a calendar day", q, err)
		}
	}
	q, err = parseInvocationQuery(invocationFilterValues(url.Values{"timezone": {"America/New_York"}, "since": {"2026-09-01"}, "until": {"2026-11-30"}, "page": {"1001"}, "page_size": {"100"}}), false, now)
	if err != nil || q.Filter.Until.Sub(q.Filter.Since) != 90*24*time.Hour+time.Hour || q.Page != 1001 {
		t.Fatal("ninety calendar days or inclusive offset ceiling rejected", q, err)
	}
	q, err = parseInvocationQuery(invocationFilterValues(url.Values{"since": {"2026-10-01T08:00:00+08:00"}, "until": {"2026-10-02T08:00:00+08:00"}}), false, now)
	if err != nil || q.Filter.Since.Format(time.RFC3339) != "2026-10-01T00:00:00Z" || q.Filter.Until.Format(time.RFC3339) != "2026-10-02T00:00:00Z" {
		t.Fatal("absolute boundary gained an extra day", q, err)
	}
}

func TestInvocationFilterHashScopesAndPresentation(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a, err := parseInvocationQuery(invocationFilterValues(nil), false, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseInvocationQuery(invocationFilterValues(url.Values{"result": {"all"}, "page": {"2"}, "page_size": {"50"}, "display_currency": {"CNY"}}), false, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.filterHash(false, 1) != b.filterHash(false, 1) || a.filterHash(false, 1) == a.filterHash(false, 2) || a.filterHash(false, 1) == a.filterHash(true, 1) {
		t.Fatal("hash ignored authority or included presentation")
	}
	c, err := parseInvocationQuery(invocationFilterValues(url.Values{"modality": {""}, "is_stream": {"false"}, "model": {strings.Repeat("字", 128)}, "api_key_id": {"18446744073709551615"}}), false, now)
	if err != nil || c.Filter.Modality == nil || *c.Filter.Modality != "" || c.Filter.IsStream == nil || *c.Filter.IsStream || c.Filter.APIKeyID != ^uint64(0) {
		t.Fatal("explicit history/false/max ID lost", c, err)
	}
	if c.filterHash(false, 1) == a.filterHash(false, 1) {
		t.Fatal("explicit empty/false filters collapsed into absence")
	}
}
