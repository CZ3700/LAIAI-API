package leader

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPGStoreAdvisoryLockMutualExclusion(t *testing.T) {
	dsn := os.Getenv("LAIAI_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("LAIAI_PG_TEST_DSN not set")
	}

	db1, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()

	db2, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	lockID := time.Now().UnixNano()
	s1 := NewPGStore(db1, lockID)
	s2 := NewPGStore(db2, lockID)
	defer func() { _ = s2.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	held1, err := s1.acquireOrRenew(ctx, "k", "t1", 30*time.Second)
	if err != nil || !held1 {
		t.Fatalf("s1 first acquire = (%v, %v), want (true, nil)", held1, err)
	}

	held2, err := s2.acquireOrRenew(ctx, "k", "t2", 30*time.Second)
	if err != nil || held2 {
		t.Fatalf("s2 contend acquire = (%v, %v), want (false, nil)", held2, err)
	}

	// Renewing on s1 should succeed cleanly without error.
	held1Again, err := s1.acquireOrRenew(ctx, "k", "t1", 30*time.Second)
	if err != nil || !held1Again {
		t.Fatalf("s1 renew = (%v, %v), want (true, nil)", held1Again, err)
	}

	// Releasing s1 lets s2 take over the lock.
	if err := s1.Close(); err != nil {
		t.Fatalf("s1 close: %v", err)
	}
	held2After, err := s2.acquireOrRenew(ctx, "k", "t2", 30*time.Second)
	if err != nil || !held2After {
		t.Fatalf("s2 takeover = (%v, %v), want (true, nil)", held2After, err)
	}
}
