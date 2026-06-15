package migrate

import (
	"testing"
	"testing/fstest"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

var fixture = fstest.MapFS{
	"0001_a.sql": {Data: []byte(`
-- 建表
CREATE TABLE t1 (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE INDEX idx_t1_name ON t1 (name); -- 行内注释
`)},
	"0002_b.sql": {Data: []byte(`
INSERT INTO t1 (id, name) VALUES (1, 'seed');
`)},
}

func TestUpAppliesInOrderAndIsIdempotent(t *testing.T) {
	db := testDB(t)
	ran, err := Up(db, fixture)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(ran) != 2 || ran[0] != "0001_a" || ran[1] != "0002_b" {
		t.Fatalf("ran = %v", ran)
	}
	var n int64
	db.Table("t1").Count(&n)
	if n != 1 {
		t.Fatalf("t1 rows = %d", n)
	}
	// 重跑必须 no-op
	ran, err = Up(db, fixture)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if len(ran) != 0 {
		t.Fatalf("rerun applied %v, want none", ran)
	}
	db.Table("t1").Count(&n)
	if n != 1 {
		t.Fatalf("t1 rows after rerun = %d (duplicate apply!)", n)
	}
}

// 半应用恢复（对抗评审 high）：多 DDL 文件前几条已落地但版本未记录时，re-run 必须容忍
// 「已存在」跳过、补完剩余语句、记录版本——而非报错触发 entrypoint crash-loop。
func TestHalfAppliedMigrationRerunIsIdempotent(t *testing.T) {
	db := testDB(t)
	// 模拟前次半应用：第 1 个对象已存在，但 schema_migrations 未记录该版本
	if err := db.Exec("CREATE TABLE half1 (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	fs := fstest.MapFS{
		"0001_x.sql": {Data: []byte("CREATE TABLE half1 (id INTEGER PRIMARY KEY);\nCREATE TABLE half2 (id INTEGER PRIMARY KEY);")},
	}
	ran, err := Up(db, fs)
	if err != nil {
		t.Fatalf("half-applied re-run must tolerate already-exists, got: %v", err)
	}
	if len(ran) != 1 || ran[0] != "0001_x" {
		t.Fatalf("ran = %v, want 0001_x recorded", ran)
	}
	if db.Exec("SELECT 1 FROM half2 LIMIT 1").Error != nil {
		t.Fatal("half2 not created — remaining statement was skipped instead of applied")
	}
	if ran2, _ := Up(db, fs); len(ran2) != 0 {
		t.Fatalf("rerun applied %v, want none (version should now be recorded)", ran2)
	}
}

func TestFailedMigrationRollsBackAndStops(t *testing.T) {
	db := testDB(t)
	bad := fstest.MapFS{
		"0001_ok.sql":  {Data: []byte("CREATE TABLE ok1 (id INTEGER PRIMARY KEY);")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE ok2 (id INTEGER PRIMARY KEY);\nTHIS IS NOT SQL;")},
		"0003_后续.sql":  {Data: []byte("CREATE TABLE ok3 (id INTEGER PRIMARY KEY);")},
	}
	ran, err := Up(db, bad)
	if err == nil {
		t.Fatal("want error from bad migration")
	}
	if len(ran) != 1 || ran[0] != "0001_ok" {
		t.Fatalf("ran = %v, want only 0001_ok", ran)
	}
	// 0002 整体回滚：ok2 不应存在
	if db.Exec("SELECT 1 FROM ok2 LIMIT 1").Error == nil {
		t.Fatal("ok2 exists — failed migration not rolled back")
	}
	// 0003 不应被执行
	if db.Exec("SELECT 1 FROM ok3 LIMIT 1").Error == nil {
		t.Fatal("ok3 exists — runner did not stop at failure")
	}
	applied, pending, err := Status(db, bad)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || len(pending) != 2 {
		t.Fatalf("status applied=%v pending=%v", applied, pending)
	}
}
