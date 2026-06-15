// schema-doctor：迁移安全 lint + 线上 schema 漂移体检（P1-7）。
//
//	# 离线（无需 DB，CI gate 用）：破坏性 DDL 即 fail；-strict 连 warn 一起 fail
//	go run ./cmd/schema-doctor -lint [-strict] [-dir ../migrations]
//
//	# 在线漂移体检（需 LAIAI_DSN）：有 pending / 有 unknown 已应用版本 / 期望表缺失 → fail
//	LAIAI_DSN="user:pass@tcp(host:3306)/laiai?parseTime=true" go run ./cmd/schema-doctor [-dir ../migrations]
//
// 纪律：只读体检，绝不改 schema（漂移修复需人工裁定）。
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/laiai/laiai-next/core/internal/migrate"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dir := flag.String("dir", "../migrations", "migrations directory")
	lintOnly := flag.Bool("lint", false, "只做离线迁移 lint（无需 DB）")
	strict := flag.Bool("strict", false, "lint 时 warn 也算失败（默认仅 danger 失败）")
	flag.Parse()

	fsys := os.DirFS(*dir)

	// —— 离线 lint（CI gate）——
	findings, err := migrate.Lint(fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint: %v\n", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f.String())
	}
	lintFail := migrate.HasDanger(findings) || (*strict && len(findings) > 0)
	if *lintOnly {
		if lintFail {
			fmt.Fprintln(os.Stderr, "FAIL: migration lint")
			os.Exit(1)
		}
		fmt.Println("OK: migration lint clean")
		return
	}
	if migrate.HasDanger(findings) {
		fmt.Fprintln(os.Stderr, "FAIL: destructive DDL present (run with -lint for detail)")
		os.Exit(1)
	}

	// —— 在线漂移体检 ——
	dsn := os.Getenv("LAIAI_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "LAIAI_DSN required for drift check (or pass -lint for offline only)")
		os.Exit(2)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(2)
	}

	problems := 0

	// 1. 无 pending：DB 必须在 head
	applied, pending, err := migrate.Status(db, fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		os.Exit(2)
	}
	if len(pending) > 0 {
		fmt.Printf("DRIFT: %d pending migration(s) not applied: %v\n", len(pending), pending)
		problems++
	}

	// 2. 无 unknown 已应用版本：DB 不应有仓库已不存在的迁移（历史分叉/回滚信号）
	known := map[string]bool{}
	if files, gerr := fs.Glob(fsys, "*.sql"); gerr == nil {
		for _, f := range files {
			known[strings.TrimSuffix(f, ".sql")] = true
		}
	}
	for _, v := range applied {
		if !known[v] {
			fmt.Printf("DRIFT: applied version %q not present in repo migrations (history divergence?)\n", v)
			problems++
		}
	}

	// 3. 期望表存在：迁移声明 CREATE TABLE 的表必须都在（揪迁移外手工删表）
	tables, err := migrate.CreatedTables(fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "createdtables: %v\n", err)
		os.Exit(2)
	}
	for _, tbl := range tables {
		if !db.Migrator().HasTable(tbl) {
			fmt.Printf("DRIFT: expected table %q missing from live schema\n", tbl)
			problems++
		}
	}

	if problems > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d schema drift problem(s)\n", problems)
		os.Exit(1)
	}
	fmt.Printf("OK: schema at head (%d applied, %d tables verified), no drift\n", len(applied), len(tables))
}
