package arkpricetemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixtureTemplates(t *testing.T) []Template {
	t.Helper()
	raw, err := os.ReadFile("data/seedance-20261019-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var items []Template
	if err = json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func templateRecord(t *testing.T, item Template) Record {
	t.Helper()
	item.BodySHA256 = ""
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	return Record{Version: item.Version, Model: item.Model, BodyJSON: string(raw), BodyHash: hex.EncodeToString(hash[:])}
}

func TestMigrationContainsExactApprovedSnapshot(t *testing.T) {
	seed, err := os.ReadFile("../../../migrations/0121_seedance_pricing_template_20261019.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(seed), "INSERT INTO video_pricing_templates") != 1 || strings.Contains(string(seed), "UPDATE ") || strings.Contains(string(seed), "video_pricing_rules") {
		t.Fatal("template migration must only install independent snapshots")
	}
	expected := map[string]struct {
		count, minimum int
		hash           string
	}{
		"seedance-2.0":      {8, 288, "e59ac88a663847f194743e8581ed3583ba3a628faafd282790ddf398d4da888f"},
		"seedance-2.5":      {6, 486, "77bf4562a2c05b57f6cb0f4ca2aab099d6816e3fb42e37f16f5155c0d11ca3bb"},
		"seedance-2.0-fast": {4, 144, "383cf5a1c291c6f816419d44d50775f9a3d1ede655c2aba86bda8672a5ea0bae"},
		"seedance-2.0-mini": {4, 144, "8b260fcebad256d6e41eea60e67a526c8898fed7b3179712282763898f93bfd5"},
	}
	total := 0
	for _, item := range fixtureTemplates(t) {
		row := templateRecord(t, item)
		got, err := Decode(row)
		if err != nil {
			t.Fatal(item.Model, err)
		}
		want, ok := expected[item.Model]
		if !ok || len(got.Cells) != want.count || got.MinimumCellCount != want.minimum || row.BodyHash != want.hash {
			t.Fatal("approved snapshot changed", item.Model)
		}
		if !strings.Contains(string(seed), "'"+row.BodyJSON+"'") || !strings.Contains(string(seed), "'"+row.BodyHash+"'") {
			t.Fatal("SQL and readable snapshot differ", item.Model)
		}
		if strings.Contains(row.BodyJSON, ";") || strings.Contains(row.BodyJSON, "--") {
			t.Fatal("unsafe migration literal")
		}
		if got.Reference() != (Reference{InitialVersion, item.Model, want.hash}) {
			t.Fatal("wrong template reference")
		}
		total += len(got.Cells)
		delete(expected, item.Model)
	}
	if len(expected) != 0 || total != 22 {
		t.Fatal("incomplete model/price coverage")
	}
}

func TestTemplateRejectsCorruptOrIncompatibleEvidence(t *testing.T) {
	base := fixtureTemplates(t)[2]
	tests := []struct {
		name   string
		change func(*Template)
	}{
		{"upstream identity", func(v *Template) { v.UpstreamModel = "doubao-seedance-2-0-260128" }},
		{"unknown model", func(v *Template) { v.Model = "seedance-1.0" }},
		{"schema", func(v *Template) { v.SchemaVersion = 2 }},
		{"FX absent", func(v *Template) { v.FXUSDPerCNY = "" }},
		{"profile hash", func(v *Template) { v.ProfileSHA256 = strings.Repeat("a", 64) }},
		{"wrong minimum", func(v *Template) { v.MinimumRuleVersion = "official-min-seedance-2.0-20261019-v1" }},
		{"incomplete cells", func(v *Template) { v.Cells = v.Cells[:3] }},
		{"duplicate cells", func(v *Template) { v.Cells[1] = v.Cells[0] }},
		{"unsupported resolution", func(v *Template) { v.Cells[0].Resolution = "1080p" }},
		{"cost drift", func(v *Template) { v.Cells[0].SupplierCNYPer1M = "1" }},
		{"zero retail", func(v *Template) { v.Cells[0].RetailUSDPer1M = "0" }},
		{"exponent retail", func(v *Template) { v.Cells[0].RetailUSDPer1M = "1e1" }},
		{"risk too low", func(v *Template) { v.Cells[0].RiskMultiplier = "0.5" }},
		{"risk too high", func(v *Template) { v.Cells[0].RiskMultiplier = "101" }},
		{"reference minimum removed", func(v *Template) { v.Cells[1].MinimumRuleVersion = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var item Template
			_ = json.Unmarshal(raw, &item)
			test.change(&item)
			if _, err := Decode(templateRecord(t, item)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted bad template: %v", err)
			}
		})
	}
	row := templateRecord(t, base)
	row.BodyHash = strings.Repeat("0", 64)
	if _, err := Decode(row); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted corrupt raw hash")
	}
	row = templateRecord(t, base)
	row.BodyJSON = strings.Replace(row.BodyJSON, `"currency":"USD"`, `"currency":"USD","currency":"USD"`, 1)
	sum := sha256.Sum256([]byte(row.BodyJSON))
	row.BodyHash = hex.EncodeToString(sum[:])
	if _, err := Decode(row); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted duplicate JSON key")
	}
}

func TestTemplateFixedRetailIsNotRecomputedFromFX(t *testing.T) {
	item := fixtureTemplates(t)[2]
	item.FXUSDPerCNY = "0.2"
	got, err := Decode(templateRecord(t, item))
	if err != nil || got.Cells[0].RetailUSDPer1M != "5.504314192206" {
		t.Fatal("retail must remain the versioned snapshot", err)
	}
}

func TestTemplateRequiresInstalledDatabaseData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = Load(db, "seedance-2.0-fast", InitialVersion); err == nil {
		t.Fatal("missing migration fell back to code")
	}
	if err = db.Exec(`CREATE TABLE video_pricing_templates (id INTEGER PRIMARY KEY, version TEXT NOT NULL, model TEXT NOT NULL, body_hash TEXT NOT NULL, body_json TEXT NOT NULL, created_at DATETIME, UNIQUE(version,model))`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = Load(db, "seedance-2.0-fast", InitialVersion); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("missing data must not be synthesized", err)
	}
	for _, item := range fixtureTemplates(t) {
		row := templateRecord(t, item)
		if err = db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	items, err := List(db, "")
	if err != nil || len(items) != 4 {
		t.Fatal("list", len(items), err)
	}
	items, err = List(db, "seedance-2.0-fast")
	if err != nil || len(items) != 1 {
		t.Fatal("model isolation", len(items), err)
	}
	if _, err = Load(db, "seedance-2.0-fast", "unknown"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if _, err = List(db, "seedance-1.0"); !errors.Is(err, ErrInvalid) {
		t.Fatal("1.x must be unsupported")
	}
	if err = db.Model(&Record{}).Where("model = ?", "seedance-2.0-fast").Update("body_hash", strings.Repeat("0", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = List(db, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("corrupt installed snapshot silently omitted")
	}
}
