package arkpricetemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestFlash0155MigrationBodyDecodes（2026-11-16 部署名 pin 轮次）：0155 迁移行
// body_json 必须通过 Decode 全链校验，且 upstream_model 已 pin 到 261115。
func TestFlash0155MigrationBodyDecodes(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/0155_seedance_2_5_flash_upstream_pin.sql")
	if err != nil {
		t.Skip("migration file not present in this checkout")
	}
	sqlText := string(raw)
	start := strings.Index(sqlText, `{"schema_version"`)
	if start < 0 {
		t.Fatal("body not found in migration")
	}
	rest := sqlText[start:]
	end := strings.Index(rest, `','2026-`)
	body := rest[:end]
	sum := sha256.Sum256([]byte(body))
	tpl, err := Decode(Record{Version: "seedance-20261116-v1", Model: "seedance-2.5-flash", BodyHash: hex.EncodeToString(sum[:]), BodyJSON: body})
	if err != nil {
		t.Fatalf("0155 template must decode: %v", err)
	}
	if tpl.UpstreamModel != "doubao-seedance-2-5-flash-261115" {
		t.Fatalf("upstream not pinned: %s", tpl.UpstreamModel)
	}
	if len(tpl.Cells) != 4 || tpl.MinimumCellCount != 324 {
		t.Fatalf("cells=%d minimum=%d", len(tpl.Cells), tpl.MinimumCellCount)
	}
}
