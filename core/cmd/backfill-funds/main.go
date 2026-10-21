// backfill-funds projects historical journal/hold facts into the console's
// activity tables. It never mutates balances, journals, holds, or prices.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/laiai/laiai-next/core/internal/ledger"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	before := flag.String("before", "", "required fixed UTC cutoff in RFC3339 format")
	limit := flag.Int("limit", 100, "maximum source rows per batch (1..500)")
	batches := flag.Int("batches", 1, "maximum batches in this invocation (1..1000)")
	flag.Parse()
	cutoff, err := time.Parse(time.RFC3339, *before)
	if err != nil || cutoff.After(time.Now()) || *limit < 1 || *limit > 500 || *batches < 1 || *batches > 1000 {
		fmt.Fprintln(os.Stderr, "a past RFC3339 -before cutoff, -limit 1..500 and -batches 1..1000 are required")
		os.Exit(2)
	}
	dsn := os.Getenv("LAIAI_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "LAIAI_DSN is required")
		os.Exit(2)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		fmt.Fprintln(os.Stderr, "database connection unavailable")
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "database handle unavailable")
		os.Exit(1)
	}
	defer sqlDB.Close()
	db, err = ledger.EnableActivityTracking(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "activity schema unavailable; apply formal migrations first")
		os.Exit(1)
	}
	total := 0
	for i := 0; i < *batches; i++ {
		n, err := ledger.BackfillActivities(db, cutoff.UTC(), *limit)
		total += n
		if err != nil {
			fmt.Fprintf(os.Stderr, "backfill stopped after %d source rows; investigate database invariants before resuming\n", total)
			os.Exit(1)
		}
		if n == 0 {
			fmt.Printf("complete: %d source rows projected before %s\n", total, cutoff.UTC().Format(time.RFC3339))
			return
		}
	}
	fmt.Printf("batch limit reached: %d source rows projected; rerun with the same cutoff to continue\n", total)
}
