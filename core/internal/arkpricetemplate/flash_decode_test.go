package arkpricetemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestFlashTemplateDecodeRoundTrip（2026-11-13 上架轮次）：迁移行 body_json
// 必须通过 Decode 全链校验（canonical marshal 字节一致 + profile 证据哈希
// 对齐 + cells 与官方价目录逐值对齐）。
func TestFlashTemplateDecodeRoundTrip(t *testing.T) {
	const body = `{"schema_version":1,"version":"seedance-20261105-v1","model":"seedance-2.5-flash","upstream_model":"doubao-seedance-2-5-flash","label":"Seedance 2026-11-05","currency":"USD","fx_usd_per_cny":"0.148765248438","fx_version":"safe-20261016-6.7220","fx_observed_at":"2026-10-16T05:53:17.446Z","source_pricing_version":"flash-20261113-v1-price","profile_version":"ark-official-20261105-v1","profile_sha256":"8c5609fb84c54c64798c228f5f13c3d74111ef9faf13bbe0fac5d74eb4892564","minimum_rule_version":"official-min-seedance-2.5-flash-20261105-v1","minimum_cell_count":324,"formula_version":"ark-video-token-v1","cells":[{"mode":"generate","resolution":"480p","input_video":false,"service_tier":"default","draft":false,"enabled":true,"retail_usd_per_1m":"6.843201428148","supplier_cny_per_1m":"46","risk_multiplier":"4","minimum_rule_version":""},{"mode":"generate","resolution":"480p","input_video":true,"service_tier":"default","draft":false,"enabled":true,"retail_usd_per_1m":"4.165426956264","supplier_cny_per_1m":"28","risk_multiplier":"4","minimum_rule_version":"official-min-seedance-2.5-flash-20261105-v1"},{"mode":"generate","resolution":"720p","input_video":false,"service_tier":"default","draft":false,"enabled":true,"retail_usd_per_1m":"6.843201428148","supplier_cny_per_1m":"46","risk_multiplier":"4","minimum_rule_version":""},{"mode":"generate","resolution":"720p","input_video":true,"service_tier":"default","draft":false,"enabled":true,"retail_usd_per_1m":"4.165426956264","supplier_cny_per_1m":"28","risk_multiplier":"4","minimum_rule_version":"official-min-seedance-2.5-flash-20261105-v1"}]}`
	sum := sha256.Sum256([]byte(body))
	tpl, err := Decode(Record{Version: "seedance-20261105-v1", Model: "seedance-2.5-flash", BodyHash: hex.EncodeToString(sum[:]), BodyJSON: body})
	if err != nil {
		t.Fatalf("flash template must decode: %v", err)
	}
	if len(tpl.Cells) != 4 || tpl.MinimumCellCount != 324 {
		t.Fatalf("cells=%d minimum=%d", len(tpl.Cells), tpl.MinimumCellCount)
	}
	// 零售价与官方 CNY 价的平价关系（46/28 × FX 快照）。
	for _, c := range tpl.Cells {
		want := "6.843201428148"
		if c.InputVideo {
			want = "4.165426956264"
		}
		if c.RetailUSDPer1M != want {
			t.Fatalf("retail mismatch: %s/%t = %s want %s", c.Resolution, c.InputVideo, c.RetailUSDPer1M, want)
		}
	}
	// JSON 字节序漂移检测：canonical marshal 必须逐字节一致（BodySHA256 由
	// Decode 在校验后填充且 json:"-" 不参与——清空后再比较存储字节）。
	tpl.BodySHA256 = ""
	canonical, _ := json.Marshal(tpl)
	if string(canonical) != body {
		t.Fatal("stored body drifted from canonical marshal")
	}
}
