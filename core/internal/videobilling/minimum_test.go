package videobilling

import (
	"errors"
	"gorm.io/gorm"
	"strings"
	"testing"
)

const minimumTestSchema = `
CREATE TABLE video_minimum_rule_sets(id INTEGER PRIMARY KEY AUTOINCREMENT,model TEXT NOT NULL,version TEXT NOT NULL,source_url TEXT NOT NULL,evidence_hash TEXT NOT NULL,observed_at DATETIME NOT NULL,verified_by INTEGER NOT NULL,verified_at DATETIME NOT NULL,UNIQUE(model,version));
CREATE TABLE video_minimum_rule_cells(id INTEGER PRIMARY KEY AUTOINCREMENT,rule_set_id INTEGER NOT NULL,resolution TEXT NOT NULL,ratio TEXT NOT NULL,output_duration_ms INTEGER NOT NULL,fps INTEGER NOT NULL,minimum_tokens INTEGER NOT NULL,UNIQUE(rule_set_id,resolution,ratio,output_duration_ms,fps));`

func TestMinimumExactCoverageAndSnapshot(t *testing.T) {
	db, _ := setup(t, "100")
	if e := db.Exec(minimumTestSchema).Error; e != nil {
		t.Fatal(e)
	}
	set := MinimumRuleSet{Model: "seedance-2.0", Version: "minimum-v1", SourceURL: "https://docs.volcengine.com/docs/ark/model-pricing", EvidenceHash: strings.Repeat("a", 64), ObservedAt: testAt, VerifiedAt: testAt, VerifiedBy: 1}
	cells := []MinimumRuleCell{{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 500000}, {Resolution: "720p", Ratio: "16:9", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 600000}, {Resolution: "720p", Ratio: "1:1", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 700000}, {Resolution: "720p", Ratio: "1:1", OutputDurationMS: 5000, FPS: 24, MinimumTokens: 800000}}
	if e := db.Transaction(func(tx *gorm.DB) error { _, e := PublishMinimumTx(tx, set, cells); return e }); e != nil {
		t.Fatal(e)
	}
	r := ruleFixture()
	r.InputVideo = true
	r.MinimumRuleVersion = set.Version
	r.MinimumTokens = 1
	m := meterFixture()
	m.InputVideo = true
	m.InputDurationMS = 4000
	m.OutputDurationMS = 4000
	if e := db.Transaction(func(tx *gorm.DB) error {
		q, e := QuoteWithMinimumTx(tx, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9"})
		if e != nil {
			return e
		}
		if q.EstimatedTokens != 500000 || q.Metering.MinimumTokens != 500000 || q.Metering.MinimumEvidenceHash != set.EvidenceHash || q.Metering.MinimumRuleSetID == 0 {
			t.Fatalf("incorrect minimum snapshot %#v", q)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	ctx := MinimumContext{Resolution: "720p", Ratio: "adaptive", Ratios: []string{"16:9", "1:1", "adaptive"}, AutomaticDuration: true, MinDuration: 4, MaxDuration: 5}
	if e := db.Transaction(func(tx *gorm.DB) error {
		q, e := QuoteWithMinimumTx(tx, r, m, ctx)
		if e != nil {
			return e
		}
		if q.EstimatedTokens != 800000 {
			t.Fatal("adaptive did not select conservative maximum")
		}
		return ValidateMinimumCoverageTx(tx, r, ctx, 24)
	}); e != nil {
		t.Fatal(e)
	}
	ctx.MaxDuration = 6
	if e := db.Transaction(func(tx *gorm.DB) error { return ValidateMinimumCoverageTx(tx, r, ctx, 24) }); !errors.Is(e, ErrPricingUnavailable) {
		t.Fatal("missing cell accepted", e)
	}
	m.OutputFrames = 97
	m.OutputDurationMS = 0
	if e := db.Transaction(func(tx *gorm.DB) error {
		_, e := QuoteWithMinimumTx(tx, r, m, MinimumContext{Resolution: "720p", Ratio: "16:9"})
		return e
	}); !errors.Is(e, ErrPricingUnavailable) {
		t.Fatal("unverified frame minimum accepted")
	}
}
func TestMinimumEvidenceRejectsUntrustedAndDuplicate(t *testing.T) {
	db, _ := setup(t, "100")
	if e := db.Exec(minimumTestSchema).Error; e != nil {
		t.Fatal(e)
	}
	base := MinimumRuleSet{Model: "seedance-2.0", Version: "v1", SourceURL: "https://docs.volcengine.com/docs/ark", EvidenceHash: strings.Repeat("a", 64), ObservedAt: testAt, VerifiedAt: testAt, VerifiedBy: 1}
	for _, url := range []string{"https://docs.volcengine.com.evil.test/x", "https://user@docs.volcengine.com/x", "http://docs.volcengine.com/x", "https://docs.volcengine.com:444/x"} {
		x := base
		x.SourceURL = url
		if validMinimumSource(x) {
			t.Fatal("source accepted", url)
		}
	}
	c := MinimumRuleCell{Resolution: "720p", Ratio: "16:9", OutputDurationMS: 4000, FPS: 24, MinimumTokens: 100}
	if e := db.Transaction(func(tx *gorm.DB) error { _, e := PublishMinimumTx(tx, base, []MinimumRuleCell{c, c}); return e }); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate cells", e)
	}
	var n int64
	db.Model(&MinimumRuleSet{}).Count(&n)
	if n != 0 {
		t.Fatal("partial evidence survived rejection")
	}
}
