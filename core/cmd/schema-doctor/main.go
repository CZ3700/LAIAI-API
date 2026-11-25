// schema-doctor: migration safety lint and live PostgreSQL schema drift check.
//
//	go run ./cmd/schema-doctor -lint [-strict] [-dir ../migrations]
//	LAIAI_DSN="postgres://user:pass@host:5432/laiai?sslmode=disable" go run ./cmd/schema-doctor [-dir ../migrations]
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/laiai/laiai-next/core/internal/dbopen"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dir := flag.String("dir", "../migrations", "migrations directory")
	lintOnly := flag.Bool("lint", false, "run offline migration lint only")
	strict := flag.Bool("strict", false, "fail on warnings as well as danger")
	flag.Parse()

	fsys := os.DirFS(*dir)

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

	dsn := os.Getenv("LAIAI_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "LAIAI_DSN required for drift check (or pass -lint for offline only)")
		os.Exit(2)
	}
	db, err := dbopen.Open(dsn, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(2)
	}

	problems := 0

	applied, pending, err := migrate.Status(db, fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: %v\n", err)
		os.Exit(2)
	}
	if len(pending) > 0 {
		fmt.Printf("DRIFT: %d pending migration(s) not applied: %v\n", len(pending), pending)
		problems++
	}

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
