package migrate

import (
	"os"
	"testing"
	"testing/fstest"
)

// 回归护栏：仓库现存 migrations/ 不得含任何 danger 级（破坏性 DDL）。
// 同时打印 warn（多 DDL 文件）供知情，不 fail。
func TestRealMigrationsHaveNoDangerDDL(t *testing.T) {
	findings, err := Lint(os.DirFS("../../../migrations"))
	if err != nil {
		t.Fatalf("lint real migrations: %v", err)
	}
	for _, f := range findings {
		if f.Severity == "danger" {
			t.Errorf("danger finding in repo migrations: %s", f)
		} else {
			t.Logf("warn: %s", f)
		}
	}
	if HasDanger(findings) {
		t.Fatal("repo migrations contain destructive DDL — needs manual runbook + ack")
	}
}

// 破坏性 DDL → danger；多条 DDL → warn；单条/DML → 干净。
func TestLintClassifies(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_create.sql": {Data: []byte("CREATE TABLE t (id INTEGER PRIMARY KEY);")},
		"0002_multi.sql": {Data: []byte(
			"ALTER TABLE t ADD COLUMN a TEXT NOT NULL DEFAULT '';\n" +
				"ALTER TABLE t ADD COLUMN b TEXT NOT NULL DEFAULT '';")},
		"0003_drop.sql":     {Data: []byte("DROP TABLE t;")},
		"0004_modify.sql":   {Data: []byte("ALTER TABLE t MODIFY COLUMN a BIGINT;")},
		"0005_seed.sql":     {Data: []byte("INSERT INTO t (id) VALUES (1);")},
	}
	findings, err := Lint(fsys)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	var danger, warn int
	byFile := map[string][]LintFinding{}
	for _, f := range findings {
		byFile[f.File] = append(byFile[f.File], f)
		switch f.Severity {
		case "danger":
			danger++
		case "warn":
			warn++
		}
	}
	if len(byFile["0003_drop.sql"]) == 0 || byFile["0003_drop.sql"][0].Severity != "danger" {
		t.Errorf("DROP TABLE should be danger, got %v", byFile["0003_drop.sql"])
	}
	if len(byFile["0004_modify.sql"]) == 0 || byFile["0004_modify.sql"][0].Severity != "danger" {
		t.Errorf("MODIFY COLUMN should be danger, got %v", byFile["0004_modify.sql"])
	}
	if len(byFile["0002_multi.sql"]) == 0 || byFile["0002_multi.sql"][0].Severity != "warn" {
		t.Errorf("multi-ADD-COLUMN should be warn, got %v", byFile["0002_multi.sql"])
	}
	if len(byFile["0001_create.sql"]) != 0 {
		t.Errorf("single CREATE TABLE should be clean, got %v", byFile["0001_create.sql"])
	}
	if len(byFile["0005_seed.sql"]) != 0 {
		t.Errorf("single INSERT should be clean, got %v", byFile["0005_seed.sql"])
	}
	if !HasDanger(findings) {
		t.Fatal("HasDanger should be true")
	}
	if danger != 2 {
		t.Fatalf("danger count = %d, want 2 (drop + modify)", danger)
	}
}
