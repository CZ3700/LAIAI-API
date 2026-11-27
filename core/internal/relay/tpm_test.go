package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeTPMCounter struct {
	mu  sync.Mutex
	m   map[string]int64
	err error
}

func (f *fakeTPMCounter) reserve(_ context.Context, key string, limit, tokens int64, _ time.Duration) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m[key]+tokens > limit {
		return false, nil
	}
	f.m[key] += tokens
	return true, nil
}

func (f *fakeTPMCounter) reconcile(_ context.Context, key string, delta int64, _ time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = max(int64(0), f.m[key]+delta)
	return nil
}

func tpmTestRelay(tc tpmCounter, modelTPM int) *Relay {
	return &Relay{
		Now:         func() time.Time { return time.Unix(1_700_000_040, 0) },
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		tpm:         tc,
		tpmWindows:  map[string]*tpmWindow{},
		rateEnabled: modelTPM > 0,
		rateTPM:     modelTPM,
		ratePolAt:   time.Unix(1_700_000_040, 0),
	}
}

func TestReserveAndReconcileTPM(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("redis counter reserve exceed reconcile and rollback", func(t *testing.T) {
		t.Parallel()
		fc := &fakeTPMCounter{m: map[string]int64{}}
		r := tpmTestRelay(fc, 150)

		epoch, ok := r.reserveTPM(ctx, 1, 100, 10, "gpt-5", 60)
		if !ok || epoch == 0 {
			t.Fatalf("first reserve failed: ok=%v epoch=%d", ok, epoch)
		}
		if _, ok := r.reserveTPM(ctx, 1, 100, 10, "gpt-5", 50); ok {
			t.Fatal("second reserve (60+50 > 100 key limit) should fail")
		}
		// Reconcile down because actual tokens were 20 instead of estimated 60 (delta = -40).
		r.reconcileTPM(ctx, epoch, 1, 100, 10, "gpt-5", -40)
		if _, ok := r.reserveTPM(ctx, 1, 100, 10, "gpt-5", 80); !ok {
			t.Fatal("reserve after -40 reconcile should succeed (20+80 <= 100)")
		}
		// Pre-stream refund (-80) frees quota back down to 20.
		r.reconcileTPM(ctx, epoch, 1, 100, 10, "gpt-5", -80)
		if _, ok := r.reserveTPM(ctx, 1, 100, 10, "gpt-5", 80); !ok {
			t.Fatal("reserve after pre-stream refund should succeed")
		}
	})

	t.Run("model TPM fail rolls back key TPM reservation", func(t *testing.T) {
		t.Parallel()
		fc := &fakeTPMCounter{m: map[string]int64{}}
		r := tpmTestRelay(fc, 150)

		// Key 1 consumes 120 of gpt-5 model budget (120/150 model).
		epoch, ok := r.reserveTPM(ctx, 1, 500, 10, "gpt-5", 120)
		if !ok {
			t.Fatal("initial reserve failed")
		}
		// Key 2 wants 40 tokens (fits key 2's 100 limit, but fails model 120+40>150).
		if _, ok := r.reserveTPM(ctx, 2, 100, 10, "gpt-5", 40); ok {
			t.Fatal("should block on model TPM limit")
		}
		// Key 2's key bucket must be rolled back to 0 so another model can use all 100 tokens.
		if _, ok := r.reserveTPM(ctx, 2, 100, 10, "other-model", 100); !ok {
			t.Fatalf("key 2 leaked failed reservation across models after epoch %d", epoch)
		}
	})

	t.Run("redis errors fail open", func(t *testing.T) {
		t.Parallel()
		fc := &fakeTPMCounter{m: map[string]int64{}, err: errors.New("redis down")}
		r := tpmTestRelay(fc, 10)
		if _, ok := r.reserveTPM(ctx, 1, 10, 1, "gpt-5", 9999); !ok {
			t.Fatal("TPM limiter must fail open on redis error")
		}
	})

	t.Run("in-memory fallback and ext_settings DB policy", func(t *testing.T) {
		t.Parallel()
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE TABLE ext_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO ext_settings (name, value) VALUES
			('model_rate_limit_enabled', 'true'),
			('model_rate_limit_rpm', '60'),
			('model_rate_limit_tpm', '120')`).Error; err != nil {
			t.Fatal(err)
		}
		r := &Relay{
			DB:         db,
			Now:        func() time.Time { return time.Unix(1_700_000_040, 0) },
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			tpmWindows: map[string]*tpmWindow{},
		}
		epoch, ok := r.reserveTPM(ctx, 5, 0, 2, "claude-sonnet-4", 100)
		if !ok {
			t.Fatal("first in-memory TPM reserve should pass")
		}
		if _, ok := r.reserveTPM(ctx, 5, 0, 2, "claude-sonnet-4", 30); ok {
			t.Fatal("100+30 > 120 should block in memory")
		}
		r.reconcileTPM(ctx, epoch, 5, 0, 2, "claude-sonnet-4", -20)
		if _, ok := r.reserveTPM(ctx, 5, 0, 2, "claude-sonnet-4", 40); !ok {
			t.Fatal("80+40 <= 120 after reconcile should pass")
		}
	})
}
