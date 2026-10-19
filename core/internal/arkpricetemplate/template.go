// Package arkpricetemplate reads immutable, migration-installed retail snapshots.
// A template is not a channel, a published price rule, or permission to dispatch.
package arkpricetemplate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/arkpricecatalog"
	"github.com/laiai/laiai-next/core/internal/arkverification"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const InitialVersion = "seedance-20261019-v1"

var ErrInvalid = errors.New("invalid or incompatible video pricing template")
var exactDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var versionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type Record struct {
	ID        uint64 `gorm:"primaryKey"`
	Version   string
	Model     string
	BodyHash  string
	BodyJSON  string `gorm:"column:body_json"`
	CreatedAt time.Time
}

func (Record) TableName() string { return "video_pricing_templates" }

type Reference struct {
	Version string `json:"version"`
	Model   string `json:"model"`
	SHA256  string `json:"sha256"`
}

type Cell struct {
	Mode               string `json:"mode"`
	Resolution         string `json:"resolution"`
	InputVideo         bool   `json:"input_video"`
	ServiceTier        string `json:"service_tier"`
	Draft              bool   `json:"draft"`
	Enabled            bool   `json:"enabled"`
	RetailUSDPer1M     string `json:"retail_usd_per_1m"`
	SupplierCNYPer1M   string `json:"supplier_cny_per_1m"`
	RiskMultiplier     string `json:"risk_multiplier"`
	MinimumRuleVersion string `json:"minimum_rule_version"`
}

func (c Cell) Key() string {
	return fmt.Sprintf("%s/%s/%t/%s/%t", c.Mode, c.Resolution, c.InputVideo, c.ServiceTier, c.Draft)
}

type Template struct {
	SchemaVersion        int       `json:"schema_version"`
	Version              string    `json:"version"`
	Model                string    `json:"model"`
	UpstreamModel        string    `json:"upstream_model"`
	Label                string    `json:"label"`
	Currency             string    `json:"currency"`
	FXUSDPerCNY          string    `json:"fx_usd_per_cny"`
	FXVersion            string    `json:"fx_version"`
	FXObservedAt         time.Time `json:"fx_observed_at"`
	SourcePricingVersion string    `json:"source_pricing_version"`
	ProfileVersion       string    `json:"profile_version"`
	ProfileSHA256        string    `json:"profile_sha256"`
	MinimumRuleVersion   string    `json:"minimum_rule_version"`
	MinimumCellCount     int       `json:"minimum_cell_count"`
	FormulaVersion       string    `json:"formula_version"`
	Cells                []Cell    `json:"cells"`
	// Computed from the exact stored JSON, never trusted from the document itself.
	BodySHA256 string `json:"body_sha256,omitempty"`
}

func (t Template) Reference() Reference { return Reference{t.Version, t.Model, t.BodySHA256} }

func positive(value string) (decimal.Decimal, bool) {
	if !exactDecimal.MatchString(value) {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(value)
	return d, err == nil && d.IsPositive()
}

func upstream(model string) string {
	switch model {
	case ark.Model20:
		return "doubao-seedance-2-0-260128"
	case ark.Model25:
		return "doubao-seedance-2-5-260628"
	case ark.Model20Fast:
		return "doubao-seedance-2-0-fast-260128"
	case ark.Model20Mini:
		return "doubao-seedance-2-0-mini-260615"
	default:
		return ""
	}
}

// Decode verifies the raw document and its reviewed dependency identities.
// It never recalculates retail from the target account's exchange rate.
func Decode(row Record) (Template, error) {
	var t Template
	if len(row.BodyJSON) == 0 || len(row.BodyJSON) > 128<<10 {
		return t, ErrInvalid
	}
	sum := sha256.Sum256([]byte(row.BodyJSON))
	if hex.EncodeToString(sum[:]) != row.BodyHash {
		return t, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewBufferString(row.BodyJSON))
	d.DisallowUnknownFields()
	if d.Decode(&t) != nil || d.Decode(new(any)) != io.EOF || t.BodySHA256 != "" || t.SchemaVersion != 1 || t.Version != row.Version || !versionName.MatchString(t.Version) || t.Model != row.Model || t.UpstreamModel == "" || t.UpstreamModel != upstream(t.Model) || t.Label == "" || t.Currency != "USD" || t.FormulaVersion != "ark-video-token-v1" || t.SourcePricingVersion == "" || t.FXVersion == "" || t.FXObservedAt.IsZero() {
		return Template{}, ErrInvalid
	}
	// Migration documents use canonical typed JSON. This also rejects duplicate
	// keys and alternative representations before they can obscure provenance.
	canonical, err := json.Marshal(t)
	if err != nil || !bytes.Equal(canonical, []byte(row.BodyJSON)) {
		return Template{}, ErrInvalid
	}
	if _, ok := positive(t.FXUSDPerCNY); !ok {
		return Template{}, ErrInvalid
	}
	p, err := arkverification.Load(t.Model)
	if err != nil || p.Version != t.ProfileVersion || p.EvidenceSHA256 != t.ProfileSHA256 || p.MinimumVersion != t.MinimumRuleVersion || len(p.MinimumCells) != t.MinimumCellCount {
		return Template{}, ErrInvalid
	}
	ref, err := arkpricecatalog.ReferenceCandidates(t.Model)
	if err != nil || len(t.Cells) != len(ref.Prices) {
		return Template{}, ErrInvalid
	}
	wanted := make(map[string]string, len(ref.Prices))
	for _, price := range ref.Prices {
		wanted[price.Key] = price.SupplierCNYPer1M
	}
	for _, c := range t.Cells {
		cost, found := wanted[c.Key()]
		retail, goodRetail := positive(c.RetailUSDPer1M)
		supplier, goodCost := positive(c.SupplierCNYPer1M)
		risk, goodRisk := positive(c.RiskMultiplier)
		if !found || !goodRetail || !retail.IsPositive() || !goodCost || supplier.String() != cost || !goodRisk || risk.LessThan(decimal.NewFromInt(1)) || risk.GreaterThan(decimal.NewFromInt(100)) || !c.Enabled || c.InputVideo && c.MinimumRuleVersion != p.MinimumVersion || !c.InputVideo && c.MinimumRuleVersion != "" {
			return Template{}, ErrInvalid
		}
		delete(wanted, c.Key())
	}
	if len(wanted) != 0 {
		return Template{}, ErrInvalid
	}
	t.BodySHA256 = row.BodyHash
	return t, nil
}

// Load requires an installed database row. There is deliberately no code fallback
// when migration data is absent or corrupted.
func Load(tx *gorm.DB, model, version string) (Template, error) {
	if upstream(model) == "" || !versionName.MatchString(version) {
		return Template{}, ErrInvalid
	}
	var row Record
	if err := tx.Where("model = ? AND version = ?", model, version).Take(&row).Error; err != nil {
		return Template{}, err
	}
	return Decode(row)
}

func List(tx *gorm.DB, model string) ([]Template, error) {
	if model != "" && upstream(model) == "" {
		return nil, ErrInvalid
	}
	q := tx.Model(&Record{})
	if model != "" {
		q = q.Where("model = ?", model)
	}
	var rows []Record
	if err := q.Order("version DESC, model ASC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(rows))
	for _, row := range rows {
		t, err := Decode(row)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
