package video

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/videobilling"
)

func TestRequestMeteringProjectsPublicFactsAndPreservesUnknownProvenance(t *testing.T) {
	snap := videobilling.Snapshot{MeteringJSON: `{"InputVideo":true,"InputDurationMS":15000,"OutputDurationMS":4000,"Width":640,"Height":640,"FPSNumerator":24,"FPSDenominator":1,"FXUSDPerCNY":"never-public-fx","InputContextHash":"internal-context","ParentDraftTaskID":"internal-parent"}`, CapabilityVersion: "cap-v1"}
	got := requestMetering(snap, `{"Resolution":"480p","Ratio":"1:1","Draft":false,"AccountRef":"never-public-account"}`)
	if got == nil || got.InputDurationSource != "unknown" || got.InputVideoCount != nil || got.Mode != "draft_conversion" || got.InputDurationMS != 15000 || got.Width != 640 {
		t.Fatalf("bad estimate projection: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"never-public", "internal-context", "internal-parent", "FXUSDPerCNY", "AccountRef"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("internal fact exposed", secret)
		}
	}
	snap.MeteringJSON = `{"InputVideo":true,"InputVideoCount":2,"InputDurationSource":"verified_media","InputDurationMS":12000,"MinimumTokens":123}`
	got = requestMetering(snap, `{}`)
	if got.InputDurationSource != "verified_media" || got.InputVideoCount == nil || *got.InputVideoCount != 2 || got.MinimumTokens == nil || *got.MinimumTokens != 123 {
		t.Fatal(got)
	}
	snap.MeteringJSON = `{"InputVideo":false}`
	got = requestMetering(snap, `{}`)
	if got.InputDurationSource != "none" || got.InputVideoCount == nil || *got.InputVideoCount != 0 {
		t.Fatal(got)
	}
	snap.MeteringJSON = `invalid`
	if requestMetering(snap, `{}`) != nil {
		t.Fatal("malformed estimate must not fabricate zero facts")
	}
}
