package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This fixture binds exact migration bytes only. All metadata below is synthetic
// and tests comparator/control flow; it is not evidence of MySQL acceptance.
//
//go:embed testdata/0135_video_query_settlement_shape.sql
var shapeTestSQL []byte

func shapeString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func shapeInt(i int64) sql.NullInt64      { return sql.NullInt64{Int64: i, Valid: true} }

func syntheticShapeProfile() *settlementShapeProfile {
	p := &settlementShapeProfile{Migration: settlementShapeMigration, SQLSHA256: settlementShapeSQLSHA256}
	for _, name := range []string{"runtime_version", "adapter_version", "protocol_contract_version", "observation_schema_version", "applied_at", "recovery_due_at", "received_at", "attempt_id"} {
		p.Columns = append(p.Columns, settlementShapeColumn{ColumnName: name, DataType: "synthetic", ColumnType: "synthetic", IsNullable: "NO"})
	}
	p.Columns = append(p.Columns,
		settlementShapeColumn{ColumnName: "generation_applied_at", DataType: "datetime", ColumnType: "datetime(3)", IsNullable: "YES", DatetimePrecision: shapeInt(3)},
		settlementShapeColumn{ColumnName: "settlement_kind", DataType: "varchar", ColumnType: "varchar(24)", IsNullable: "NO", ColumnDefault: shapeString(""), CharacterSetName: shapeString("utf8mb4"), CollationName: shapeString("utf8mb4_0900_bin")},
		settlementShapeColumn{ColumnName: settlementShapeColumnName, DataType: "tinyint", ColumnType: "tinyint unsigned", IsNullable: "NO", Extra: "VIRTUAL GENERATED", GenerationExpression: "SYNTHETIC_EXACT_BYTES_NOT_MYSQL_EVIDENCE"})
	p.Indexes = append(p.Indexes, settlementShapeIndex{IndexName: "PRIMARY", NonUnique: 0, SeqInIndex: 1, ColumnName: shapeString("attempt_id"), Collation: shapeString("A"), IndexType: "BTREE", IsVisible: "YES"})
	for i, name := range []string{"runtime_version", "adapter_version", "protocol_contract_version", "observation_schema_version", settlementShapeColumnName, "applied_at", "recovery_due_at", "received_at", "attempt_id"} {
		p.Indexes = append(p.Indexes, settlementShapeIndex{IndexName: settlementShapeIndexName, NonUnique: 1, SeqInIndex: i + 1, ColumnName: shapeString(name), Collation: shapeString("A"), IndexType: "BTREE", IsVisible: "YES"})
	}
	return p
}

func shapeMetadata(p *settlementShapeProfile, expanded bool) settlementShapeMetadata {
	var m settlementShapeMetadata
	for _, c := range p.Columns {
		if expanded || c.ColumnName != settlementShapeColumnName {
			m.Columns = append(m.Columns, c)
		}
	}
	for _, i := range p.Indexes {
		if expanded || i.IndexName != settlementShapeIndexName {
			m.Indexes = append(m.Indexes, i)
		}
	}
	return m
}

func TestSettlementShapeExactMetadataNegativeControls(t *testing.T) {
	p := syntheticShapeProfile()
	g, err := newSettlementShapeGuard(shapeTestSQL, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.matches(shapeMetadata(p, true), true); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*settlementShapeMetadata){
		"missing_column":     func(m *settlementShapeMetadata) { m.Columns = m.Columns[:len(m.Columns)-1] },
		"plain_column":       func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].Extra = "" },
		"signed_type":        func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].ColumnType = "tinyint" },
		"nullable":           func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].IsNullable = "YES" },
		"stored_not_virtual": func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].Extra = "STORED GENERATED" },
		"expression_bytes":   func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].GenerationExpression += " " },
		"expression_case": func(m *settlementShapeMetadata) {
			m.Columns[len(m.Columns)-1].GenerationExpression = strings.ToLower(m.Columns[len(m.Columns)-1].GenerationExpression)
		},
		"generated_default": func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-1].ColumnDefault = shapeString("0") },
		"missing_index":     func(m *settlementShapeMetadata) { m.Indexes = m.Indexes[:1] },
		"short_index":       func(m *settlementShapeMetadata) { m.Indexes = m.Indexes[:len(m.Indexes)-1] },
		"reordered_index": func(m *settlementShapeMetadata) {
			m.Indexes[1].ColumnName, m.Indexes[2].ColumnName = m.Indexes[2].ColumnName, m.Indexes[1].ColumnName
		},
		"index_prefix":         func(m *settlementShapeMetadata) { m.Indexes[1].SubPart = shapeInt(2) },
		"invisible":            func(m *settlementShapeMetadata) { m.Indexes[1].IsVisible = "NO" },
		"descending":           func(m *settlementShapeMetadata) { m.Indexes[1].Collation = shapeString("D") },
		"unique":               func(m *settlementShapeMetadata) { m.Indexes[1].NonUnique = 0 },
		"wrong_index_type":     func(m *settlementShapeMetadata) { m.Indexes[1].IndexType = "HASH" },
		"functional_index":     func(m *settlementShapeMetadata) { m.Indexes[1].Expression = shapeString("1") },
		"duplicate_index_part": func(m *settlementShapeMetadata) { m.Indexes = append(m.Indexes, m.Indexes[1]) },
		"duplicate_column":     func(m *settlementShapeMetadata) { m.Columns = append(m.Columns, m.Columns[0]) },
		"missing_old_column":   func(m *settlementShapeMetadata) { m.Columns = m.Columns[1:] },
		"old_index_changed":    func(m *settlementShapeMetadata) { m.Indexes[0].IsVisible = "NO" },
		"old_index_missing":    func(m *settlementShapeMetadata) { m.Indexes = m.Indexes[1:] },
		"source_precision":     func(m *settlementShapeMetadata) { m.Columns[len(m.Columns)-3].DatetimePrecision = shapeInt(6) },
		"source_collation": func(m *settlementShapeMetadata) {
			m.Columns[len(m.Columns)-2].CollationName = shapeString("utf8mb4_0900_ai_ci")
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := shapeMetadata(p, true)
			change(&m)
			if err := g.matches(m, true); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
	t.Run("future_unrelated_objects", func(t *testing.T) {
		m := shapeMetadata(p, true)
		m.Columns = append(m.Columns, settlementShapeColumn{ColumnName: "future_unrelated"})
		m.Indexes = append(m.Indexes, settlementShapeIndex{IndexName: "future_unrelated", SeqInIndex: 1})
		if err := g.matches(m, true); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSettlementShapeProfileIsBoundToApprovedEvidence(t *testing.T) {
	if _, err := newSettlementShapeGuard(shapeTestSQL, nil); err != nil {
		t.Fatal("approved production profile rejected", err)
	}
	p := syntheticShapeProfile()
	if _, err := newSettlementShapeGuard(append(append([]byte{}, shapeTestSQL...), '\n'), p); err == nil {
		t.Fatal("changed SQL accepted")
	}
	for _, mutate := range []func(*settlementShapeProfile){
		func(p *settlementShapeProfile) { p.Migration = "0134_video_query_generation_settlement" },
		func(p *settlementShapeProfile) { p.SQLSHA256 = strings.Repeat("0", 64) },
		func(p *settlementShapeProfile) { p.Columns[len(p.Columns)-1].GenerationExpression = "" },
		func(p *settlementShapeProfile) { p.Columns = append(p.Columns, p.Columns[0]) },
		func(p *settlementShapeProfile) { p.Indexes = p.Indexes[:len(p.Indexes)-1] },
		func(p *settlementShapeProfile) { p.Indexes[1].SubPart = shapeInt(1) },
		func(p *settlementShapeProfile) { p.Columns[len(p.Columns)-3].DatetimePrecision = shapeInt(6) },
	} {
		q := syntheticShapeProfile()
		mutate(q)
		if _, err := newSettlementShapeGuard(shapeTestSQL, q); err == nil {
			t.Fatal("invalid profile accepted")
		}
	}
}

// A scripted database/sql driver proves the real runner's ordering. It creates
// no database, executes no SQL engine, and makes no network connection.
type shapeScript struct {
	current, expanded                                                        settlementShapeMetadata
	recorded                                                                 bool
	ddlErr                                                                   error
	driftAfterDDL                                                            bool
	metadataReads, failMetadataAt, ddlCalls, recordCalls, commits, rollbacks int
	legacy                                                                   bool
	recordedVersion                                                          string
}
type shapeConnector struct{ s *shapeScript }

func (c shapeConnector) Connect(context.Context) (driver.Conn, error) { return &shapeConn{s: c.s}, nil }
func (c shapeConnector) Driver() driver.Driver                        { return shapeDriver{} }

type shapeDriver struct{}

func (shapeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("direct driver open disabled")
}

type shapeConn struct{ s *shapeScript }

func (*shapeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not expected")
}
func (*shapeConn) Close() error                                                   { return nil }
func (c *shapeConn) Begin() (driver.Tx, error)                                    { return &shapeTx{s: c.s}, nil }
func (c *shapeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return c.Begin() }
func (c *shapeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	s := c.s
	switch {
	case strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS schema_migrations"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, "ALTER TABLE video_query_receipts"), s.legacy && strings.HasPrefix(q, "CREATE TABLE legacy"):
		s.ddlCalls++
		if s.ddlErr == nil {
			s.current = s.expanded
		}
		if s.driftAfterDDL {
			s.current.Columns = append([]settlementShapeColumn{}, s.current.Columns...)
			s.current.Columns[len(s.current.Columns)-1].Extra = "STORED GENERATED"
		}
		return driver.RowsAffected(0), s.ddlErr
	case strings.HasPrefix(q, "INSERT INTO schema_migrations"):
		s.recordCalls++
		return driver.RowsAffected(1), nil
	default:
		return nil, fmt.Errorf("unexpected scripted exec: %s", q)
	}
}
func nullableString(s sql.NullString) driver.Value {
	if s.Valid {
		return s.String
	}
	return nil
}
func nullableInt(s sql.NullInt64) driver.Value {
	if s.Valid {
		return s.Int64
	}
	return nil
}
func (c *shapeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	if strings.Contains(q, "FROM `schema_migrations`") {
		var rows [][]driver.Value
		if s.recorded {
			version := s.recordedVersion
			if version == "" {
				version = settlementShapeMigration
			}
			rows = append(rows, []driver.Value{version, "fixture"})
		}
		return &shapeRows{columns: []string{"version", "applied_at"}, rows: rows}, nil
	}
	s.metadataReads++
	if s.failMetadataAt == s.metadataReads {
		return nil, errors.New("synthetic metadata unavailable")
	}
	if strings.Contains(q, "FROM information_schema.columns") {
		r := &shapeRows{columns: []string{"column_name", "data_type", "column_type", "is_nullable", "column_default", "extra", "generation_expression", "datetime_precision", "character_set_name", "collation_name"}}
		for _, v := range s.current.Columns {
			r.rows = append(r.rows, []driver.Value{v.ColumnName, v.DataType, v.ColumnType, v.IsNullable, nullableString(v.ColumnDefault), v.Extra, v.GenerationExpression, nullableInt(v.DatetimePrecision), nullableString(v.CharacterSetName), nullableString(v.CollationName)})
		}
		return r, nil
	}
	if strings.Contains(q, "FROM information_schema.statistics") {
		r := &shapeRows{columns: []string{"index_name", "non_unique", "seq_in_index", "column_name", "collation", "sub_part", "index_type", "is_visible", "expression"}}
		for _, v := range s.current.Indexes {
			r.rows = append(r.rows, []driver.Value{v.IndexName, int64(v.NonUnique), int64(v.SeqInIndex), nullableString(v.ColumnName), nullableString(v.Collation), nullableInt(v.SubPart), v.IndexType, v.IsVisible, nullableString(v.Expression)})
		}
		return r, nil
	}
	return nil, fmt.Errorf("unexpected scripted query: %s", q)
}

type shapeTx struct{ s *shapeScript }

func (t *shapeTx) Commit() error   { t.s.commits++; return nil }
func (t *shapeTx) Rollback() error { t.s.rollbacks++; return nil }

type shapeRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *shapeRows) Columns() []string { return r.columns }
func (*shapeRows) Close() error        { return nil }
func (r *shapeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}
func shapeScriptDB(t *testing.T, s *shapeScript) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(shapeConnector{s: s})
	t.Cleanup(func() { pool.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSettlementShapeRunnerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		recorded, preExpanded bool
		duplicate             string
		drift                 string
		failAt                int
		wantErr               bool
		ddl, records          int
	}{
		{name: "fresh", ddl: 1, records: 1},
		{name: "duplicate_column_complete", preExpanded: true, duplicate: "Duplicate column name", ddl: 1, records: 1},
		{name: "duplicate_key_complete", preExpanded: true, duplicate: "Duplicate key name", ddl: 1, records: 1},
		{name: "recorded_exact", recorded: true, preExpanded: true},
		{name: "recorded_missing_column", recorded: true, wantErr: true},
		{name: "recorded_wrong_expression", recorded: true, preExpanded: true, drift: "expression", wantErr: true},
		{name: "duplicate_column_missing_index", preExpanded: true, duplicate: "Duplicate column name", drift: "index", wantErr: true},
		{name: "duplicate_key_wrong_index", preExpanded: true, duplicate: "Duplicate key name", drift: "index_order", wantErr: true},
		{name: "same_name_plain_column", preExpanded: true, drift: "plain", wantErr: true},
		{name: "old_source_drift", drift: "source", wantErr: true},
		{name: "fresh_wrong_post_ddl", drift: "after", ddl: 1, wantErr: true},
		{name: "duplicate_changed_during_ddl", preExpanded: true, duplicate: "Duplicate column name", drift: "after", ddl: 1, wantErr: true},
		{name: "ddl_other_error", duplicate: "syntax error", ddl: 1, wantErr: true},
		{name: "before_column_read_error", failAt: 1, wantErr: true},
		{name: "before_index_read_error", failAt: 2, wantErr: true},
		{name: "after_column_read_error", failAt: 3, ddl: 1, wantErr: true},
		{name: "after_index_read_error", failAt: 4, ddl: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := syntheticShapeProfile()
			s := &shapeScript{current: shapeMetadata(p, tc.preExpanded), expanded: shapeMetadata(p, true), recorded: tc.recorded, failMetadataAt: tc.failAt}
			if tc.duplicate != "" {
				s.ddlErr = errors.New(tc.duplicate)
			}
			switch tc.drift {
			case "expression":
				s.current.Columns[len(s.current.Columns)-1].GenerationExpression = "wrong"
			case "plain":
				s.current.Columns[len(s.current.Columns)-1].Extra = ""
			case "index":
				s.current.Indexes = s.current.Indexes[:1]
			case "index_order":
				s.current.Indexes[1].ColumnName = shapeString("wrong")
			case "source":
				s.current.Columns[len(s.current.Columns)-2].ColumnType = "datetime(6)"
			case "after":
				s.driftAfterDDL = true
			}
			ran, err := upWithSettlementShapeProfile(shapeScriptDB(t, s), fstest.MapFS{settlementShapeMigration + ".sql": &fstest.MapFile{Data: shapeTestSQL}}, p)
			if (err != nil) != tc.wantErr || s.ddlCalls != tc.ddl || s.recordCalls != tc.records {
				t.Fatalf("err=%v ddl=%d records=%d ran=%v", err, s.ddlCalls, s.recordCalls, ran)
			}
			if !tc.wantErr && tc.records == 1 && !reflect.DeepEqual(ran, []string{settlementShapeMigration}) {
				t.Fatal("wrong recorded version", ran)
			}
			if tc.wantErr && len(ran) != 0 {
				t.Fatal("failed guard reported applied migration")
			}
		})
	}
}

func TestSettlementShapeApprovedProfileDefaultRunner(t *testing.T) {
	p := approvedSettlementShapeProfile()
	s := &shapeScript{current: shapeMetadata(p, false), expanded: shapeMetadata(p, true)}
	ran, err := up(shapeScriptDB(t, s), fstest.MapFS{settlementShapeMigration + ".sql": &fstest.MapFile{Data: shapeTestSQL}})
	if err != nil || !reflect.DeepEqual(ran, []string{settlementShapeMigration}) || s.ddlCalls != 1 || s.recordCalls != 1 {
		t.Fatal("approved default profile failed", ran, err)
	}
}
func TestSettlementShapeDoesNotChangeOtherMigrations(t *testing.T) {
	for _, tc := range []struct {
		name              string
		ddlErr            error
		recorded, wantErr bool
		ddl, records      int
	}{
		{name: "fresh", ddl: 1, records: 1},
		{name: "duplicate", ddlErr: errors.New("Duplicate column name legacy"), ddl: 1, records: 1},
		{name: "other_error", ddlErr: errors.New("legacy SQL failure"), wantErr: true, ddl: 1},
		{name: "recorded", recorded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shapeScript{legacy: true, ddlErr: tc.ddlErr, recorded: tc.recorded, recordedVersion: "0134_legacy"}
			ran, err := up(shapeScriptDB(t, s), fstest.MapFS{"0134_legacy.sql": &fstest.MapFile{Data: []byte("CREATE TABLE legacy (id INT);")}})
			if (err != nil) != tc.wantErr || s.ddlCalls != tc.ddl || s.recordCalls != tc.records || s.metadataReads != 0 {
				t.Fatal("unrelated migration behavior changed", ran, err)
			}
			if tc.records == 1 && !reflect.DeepEqual(ran, []string{"0134_legacy"}) {
				t.Fatal("wrong legacy version", ran)
			}
		})
	}
}
func TestSettlementShapeFailureRetainsPriorAppliedList(t *testing.T) {
	s := &shapeScript{legacy: true}
	ran, err := up(shapeScriptDB(t, s), fstest.MapFS{
		"0134_legacy.sql":                 &fstest.MapFile{Data: []byte("CREATE TABLE legacy (id INT);")},
		settlementShapeMigration + ".sql": &fstest.MapFile{Data: append(append([]byte{}, shapeTestSQL...), '\n')},
	})
	if err == nil || !reflect.DeepEqual(ran, []string{"0134_legacy"}) || s.ddlCalls != 1 || s.recordCalls != 1 || s.metadataReads != 0 {
		t.Fatal("0135 failure lost prior result or reached its DDL", ran, err)
	}
}

//go:embed testdata/0135-profile-probe-result.json
var shapeCapturedProbe []byte

func TestSettlementShapeApprovedProfileMatchesCapturedMetadata(t *testing.T) {
	sum := sha256.Sum256(shapeCapturedProbe)
	if hex.EncodeToString(sum[:]) != "50548308bb0aaec4b8fd7b53d4b40749a703b786d95b68215df74fc8d1905cab" {
		t.Fatal("captured evidence changed")
	}
	var proof struct {
		Columns []map[string]json.RawMessage
		Indexes []map[string]json.RawMessage
		Passed  bool
		DDL     string `json:"ddl_sha256"`
	}
	if err := json.Unmarshal(shapeCapturedProbe, &proof); err != nil || !proof.Passed || proof.DDL != settlementShapeSQLSHA256 {
		t.Fatal("invalid captured evidence")
	}
	p := approvedSettlementShapeProfile()
	if p == nil || len(p.Columns) != 25 || len(p.Indexes) != 43 || len(proof.Columns) != len(p.Columns) || len(proof.Indexes) != len(p.Indexes) {
		t.Fatal("profile omitted captured metadata")
	}
	str := func(m map[string]json.RawMessage, k string) string {
		var v string
		if err := json.Unmarshal(m[k], &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	nstr := func(m map[string]json.RawMessage, k string) sql.NullString {
		var v *string
		if err := json.Unmarshal(m[k], &v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return sql.NullString{}
		}
		return shapeString(*v)
	}
	nint := func(m map[string]json.RawMessage, k string) sql.NullInt64 {
		var v *int64
		if err := json.Unmarshal(m[k], &v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return sql.NullInt64{}
		}
		return shapeInt(*v)
	}
	for i, c := range proof.Columns {
		want := settlementShapeColumn{ColumnName: str(c, "COLUMN_NAME"), DataType: str(c, "DATA_TYPE"), ColumnType: str(c, "COLUMN_TYPE"), IsNullable: str(c, "IS_NULLABLE"), ColumnDefault: nstr(c, "COLUMN_DEFAULT"), Extra: str(c, "EXTRA"), GenerationExpression: str(c, "GENERATION_EXPRESSION"), DatetimePrecision: nint(c, "DATETIME_PRECISION"), CharacterSetName: nstr(c, "CHARACTER_SET_NAME"), CollationName: nstr(c, "COLLATION_NAME")}
		if !reflect.DeepEqual(p.Columns[i], want) {
			t.Fatal("column differs from captured bytes", i)
		}
	}
	for i, c := range proof.Indexes {
		want := settlementShapeIndex{IndexName: str(c, "INDEX_NAME"), NonUnique: int(nint(c, "NON_UNIQUE").Int64), SeqInIndex: int(nint(c, "SEQ_IN_INDEX").Int64), ColumnName: nstr(c, "COLUMN_NAME"), Collation: nstr(c, "COLLATION"), SubPart: nint(c, "SUB_PART"), IndexType: str(c, "INDEX_TYPE"), IsVisible: str(c, "IS_VISIBLE"), Expression: nstr(c, "EXPRESSION")}
		if !reflect.DeepEqual(p.Indexes[i], want) {
			t.Fatal("index part differs from captured bytes", i)
		}
	}
}
