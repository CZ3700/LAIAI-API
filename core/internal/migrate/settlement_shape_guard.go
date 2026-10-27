package migrate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"gorm.io/gorm"
)

const settlementShapeMigration = "0135_video_query_settlement_shape"
const settlementShapeColumnName = "settlement_shape_ready"
const settlementShapeIndexName = "idx_video_query_settlement_due"
const settlementShapeSQLSHA256 = "ac66e37551559df567455bf3dc0db4ea0ab5facaba225ec41467689826b74e43"

// This is an exact metadata fixture, not a runtime override. Include all baseline
// columns and indexes plus the two 0135 additions, omitting volatile cardinality.
// Future unrelated additions are tolerated; none of these expected objects may
// disappear or change. Expression bytes are compared without normalization.
type settlementShapeProfile struct {
	Migration string
	SQLSHA256 string
	Columns   []settlementShapeColumn
	Indexes   []settlementShapeIndex
}

type settlementShapeColumn struct {
	ColumnName           string
	DataType             string
	ColumnType           string
	IsNullable           string
	ColumnDefault        sql.NullString
	Extra                string
	GenerationExpression string
	DatetimePrecision    sql.NullInt64
	CharacterSetName     sql.NullString
	CollationName        sql.NullString
}

type settlementShapeIndex struct {
	IndexName  string
	NonUnique  int
	SeqInIndex int
	ColumnName sql.NullString
	Collation  sql.NullString
	SubPart    sql.NullInt64
	IndexType  string
	IsVisible  string
	Expression sql.NullString
}

type settlementShapeMetadata struct {
	Columns []settlementShapeColumn
	Indexes []settlementShapeIndex
}

type settlementShapeGuard struct{ profile *settlementShapeProfile }

func newSettlementShapeGuard(raw []byte, profile *settlementShapeProfile) (*settlementShapeGuard, error) {
	if profile == nil {
		profile = approvedSettlementShapeProfile()
	}
	if profile == nil {
		return nil, errors.New("migrate: 0135 exact metadata profile is not approved")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != settlementShapeSQLSHA256 || profile.SQLSHA256 != settlementShapeSQLSHA256 || profile.Migration != settlementShapeMigration {
		return nil, errors.New("migrate: 0135 SQL/profile binding mismatch")
	}
	if err := validateSettlementShapeProfile(profile); err != nil {
		return nil, err
	}
	return &settlementShapeGuard{profile: profile}, nil
}

func validateSettlementShapeProfile(p *settlementShapeProfile) error {
	invalid := errors.New("migrate: 0135 exact metadata profile is incomplete")
	columns := map[string]settlementShapeColumn{}
	for _, c := range p.Columns {
		if c.ColumnName == "" {
			return invalid
		}
		if _, exists := columns[c.ColumnName]; exists {
			return invalid
		}
		columns[c.ColumnName] = c
	}
	c, ok := columns[settlementShapeColumnName]
	if !ok || c.DataType != "tinyint" || c.ColumnType != "tinyint unsigned" || c.IsNullable != "NO" || c.ColumnDefault.Valid || c.Extra != "VIRTUAL GENERATED" || c.GenerationExpression == "" || c.DatetimePrecision.Valid || c.CharacterSetName.Valid || c.CollationName.Valid {
		return invalid
	}
	g, ok := columns["generation_applied_at"]
	if !ok || g.DataType != "datetime" || g.ColumnType != "datetime(3)" || g.IsNullable != "YES" || g.ColumnDefault.Valid || g.Extra != "" || g.GenerationExpression != "" || !g.DatetimePrecision.Valid || g.DatetimePrecision.Int64 != 3 || g.CharacterSetName.Valid || g.CollationName.Valid {
		return invalid
	}
	k, ok := columns["settlement_kind"]
	if !ok || k.DataType != "varchar" || k.ColumnType != "varchar(24)" || k.IsNullable != "NO" || !k.ColumnDefault.Valid || k.ColumnDefault.String != "" || k.Extra != "" || k.GenerationExpression != "" || k.DatetimePrecision.Valid || !k.CharacterSetName.Valid || k.CharacterSetName.String != "utf8mb4" || !k.CollationName.Valid || k.CollationName.String != "utf8mb4_0900_bin" {
		return invalid
	}
	want := []string{"runtime_version", "adapter_version", "protocol_contract_version", "observation_schema_version", settlementShapeColumnName, "applied_at", "recovery_due_at", "received_at", "attempt_id"}
	found := 0
	seen := map[string]bool{}
	for _, index := range p.Indexes {
		key := fmt.Sprintf("%s/%d", index.IndexName, index.SeqInIndex)
		if index.IndexName == "" || index.SeqInIndex < 1 || seen[key] {
			return invalid
		}
		seen[key] = true
		if index.IndexName != settlementShapeIndexName {
			continue
		}
		if found >= len(want) || index.SeqInIndex != found+1 || !index.ColumnName.Valid || index.ColumnName.String != want[found] || index.NonUnique != 1 || !index.Collation.Valid || index.Collation.String != "A" || index.SubPart.Valid || index.IndexType != "BTREE" || index.IsVisible != "YES" || index.Expression.Valid {
			return invalid
		}
		if _, exists := columns[want[found]]; !exists {
			return invalid
		}
		found++
	}
	if found != len(want) {
		return invalid
	}
	return nil
}

func readSettlementShapeMetadata(db *gorm.DB) (settlementShapeMetadata, error) {
	var out settlementShapeMetadata
	if err := db.Raw(`SELECT column_name,data_type,column_type,is_nullable,column_default,extra,generation_expression,datetime_precision,character_set_name,collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='video_query_receipts' ORDER BY ordinal_position`).Scan(&out.Columns).Error; err != nil {
		return out, errors.New("migrate: 0135 column metadata unavailable")
	}
	if err := db.Raw(`SELECT index_name,non_unique,seq_in_index,column_name,collation,sub_part,index_type,is_visible,expression FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='video_query_receipts' ORDER BY index_name,seq_in_index`).Scan(&out.Indexes).Error; err != nil {
		return out, errors.New("migrate: 0135 index metadata unavailable")
	}
	return out, nil
}

func (g *settlementShapeGuard) before(db *gorm.DB, recorded bool) error {
	actual, err := readSettlementShapeMetadata(db)
	if err != nil {
		return err
	}
	if recorded {
		return g.matches(actual, true)
	}
	// Either pristine 0134 or exact already-completed DDL can be replayed.
	// A same-name partial or wrong object is never accepted as a duplicate.
	for _, c := range actual.Columns {
		if c.ColumnName == settlementShapeColumnName {
			return g.matches(actual, true)
		}
	}
	for _, i := range actual.Indexes {
		if i.IndexName == settlementShapeIndexName {
			return g.matches(actual, true)
		}
	}
	return g.matches(actual, false)
}

func (g *settlementShapeGuard) after(db *gorm.DB) error {
	actual, err := readSettlementShapeMetadata(db)
	if err != nil {
		return err
	}
	return g.matches(actual, true)
}

func (g *settlementShapeGuard) matches(actual settlementShapeMetadata, expanded bool) error {
	columns := map[string]settlementShapeColumn{}
	indexes := map[string][]settlementShapeIndex{}
	for _, c := range actual.Columns {
		if _, duplicate := columns[c.ColumnName]; duplicate {
			return errors.New("migrate: 0135 duplicate column metadata")
		}
		columns[c.ColumnName] = c
	}
	for _, i := range actual.Indexes {
		indexes[i.IndexName] = append(indexes[i.IndexName], i)
	}
	for _, expected := range g.profile.Columns {
		if !expanded && expected.ColumnName == settlementShapeColumnName {
			continue
		}
		actual, ok := columns[expected.ColumnName]
		if !ok || !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("migrate: 0135 column shape mismatch: %s", expected.ColumnName)
		}
	}
	wantIndexes := map[string][]settlementShapeIndex{}
	for _, expected := range g.profile.Indexes {
		if !expanded && expected.IndexName == settlementShapeIndexName {
			continue
		}
		wantIndexes[expected.IndexName] = append(wantIndexes[expected.IndexName], expected)
	}
	for name, expected := range wantIndexes {
		if !reflect.DeepEqual(indexes[name], expected) {
			return fmt.Errorf("migrate: 0135 index shape mismatch: %s", name)
		}
	}
	return nil
}
