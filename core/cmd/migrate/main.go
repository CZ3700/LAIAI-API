// migrate command applies versioned SQL migrations under migrations/ to PostgreSQL (or SQLite).
//
//	LAIAI_DSN="postgres://user:pass@host:5432/laiai?sslmode=disable" go run ./cmd/migrate [-dir ../migrations] [-status]
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/laiai/laiai-next/core/internal/dbopen"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dir := flag.String("dir", "../migrations", "migrations directory")
	statusOnly := flag.Bool("status", false, "show applied/pending and exit")
	flag.Parse()

	dsn := os.Getenv("LAIAI_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "LAIAI_DSN required (refusing to guess a database)")
		os.Exit(1)
	}
	db, err := dbopen.Open(dsn, &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	fsys := os.DirFS(*dir)

	if *statusOnly {
		applied, pending, err := migrate.Status(db, fsys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "status: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("applied (%d): %v\npending (%d): %v\n", len(applied), applied, len(pending), pending)
		return
	}

	ran, err := migrate.Up(db, fsys)
	for _, v := range ran {
		fmt.Println("applied:", v)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate failed: %v\n", err)
		os.Exit(2)
	}
	if len(ran) == 0 {
		fmt.Println("up to date")
	}
}
