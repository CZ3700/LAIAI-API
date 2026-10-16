package video

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
)

func TestPublicMeteringOmitsPrivateEvidenceAndPreservesUnknownHistory(t *testing.T) {
	m := videobilling.MeteringInput{InputVideo: true, InputDurationMS: 15000, FXUSDPerCNY: decimal.NewFromInt(9), FXVersion: "private-fx", InputContextHash: "private-hash", ParentDraftTaskID: "private-parent", MinimumEvidenceHash: "private-proof"}
	out := publicMetering(m, "minimum-v1")
	if out["input_duration_source"] != "unknown" || out["input_video_count"].(*int) != nil {
		t.Fatal("historical unknown source/count must not be invented")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{"private-", "fx_", "supplier", "context_hash", "evidence_hash", "parent_draft"} {
		if strings.Contains(string(raw), prohibited) {
			t.Fatalf("private metering field leaked: %s", prohibited)
		}
	}
	if out["usage_semantics"] != "reservation_upper_bound" || out["input_token_semantics"] != "official_zero_not_separately_metered" {
		t.Fatal("upper bound must not masquerade as actual usage")
	}
}

func TestMySQLPublicBillingDistinguishesReleasedZeroFromPendingUsage(t *testing.T) {
	f := mysqlFixture(t)
	v := f.create(t, "public-billing-zero")
	r := gin.New()
	r.GET("/v1/video/generations/:id/billing", f.s.Middleware(), f.s.HandleBilling)
	read := func() map[string]json.RawMessage {
		req := httptest.NewRequest("GET", "/v1/video/generations/"+v.ID+"/billing", nil)
		req.Header.Set("Authorization", "Bearer "+f.plain)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("billing HTTP %d", w.Code)
		}
		var out map[string]json.RawMessage
		must(t, json.Unmarshal(w.Body.Bytes(), &out))
		for _, field := range []string{"supplier", "fx_", "evidence_hash", "request_hmac"} {
			if strings.Contains(w.Body.String(), field) {
				t.Fatalf("private field %s leaked", field)
			}
		}
		return out
	}
	if string(read()["settled_amount"]) != "null" {
		t.Fatal("pending financial result must be unknown")
	}
	_, _, err := f.s.Cancel(context.Background(), f.key.OrgID, v.ID, f.requestID("cancel-billing"), "public-billing-cancel")
	must(t, err)
	out := read()
	if string(out["settled_amount"]) != `"0.00000000"` {
		t.Fatal("confirmed full release must show zero charge")
	}
	var meter map[string]any
	must(t, json.Unmarshal(out["metering"], &meter))
	if meter["resolution"] != "720p" || meter["input_duration_source"] != "none" {
		t.Fatal("creation-time dimensions missing")
	}
}
