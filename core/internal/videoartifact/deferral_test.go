package videoartifact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSourceDeferralDoesNotExhaustDownloadBudget(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_deferred", Video)
	blobs := &memoryBlobs{}
	w := testWorker(t, repo, blobs)
	w.MaxAttempts = 3
	w.Resolver = resolverFunc(func(context.Context, uint64, string, Kind) (string, error) { return "", ErrSourceDeferred })
	for i := 0; i < 10; i++ {
		_, err := w.Transfer(context.Background(), 1, "task_deferred", Video, false)
		if !errors.Is(err, ErrSourceDeferred) {
			t.Fatal(err)
		}
		r, err := repo.Lookup(context.Background(), 1, "task_deferred", Video, testNow)
		if err != nil || r.Attempts != 0 || r.Status != Pending || r.LeaseUntil != nil || r.LeaseToken != "" {
			t.Fatalf("deferral consumed attempt: %+v %v", r, err)
		}
	}
	if blobs.puts != 0 {
		t.Fatal("deferred source reached blob storage")
	}
	w.Resolver = resolverFunc(func(context.Context, uint64, string, Kind) (string, error) {
		return "https://cdn.example.com/output", nil
	})
	r, err := w.Transfer(context.Background(), 1, "task_deferred", Video, false)
	if err != nil || r.Status != Ready || r.Attempts != 1 {
		t.Fatalf("later admitted transfer failed: %+v %v", r, err)
	}
}

func TestLateSourceDeferralCannotRefundNewLeaseOrReadyOutput(t *testing.T) {
	repo := testRepo(t)
	register(t, repo, 1, "task_deferral_fence", Video)
	old, err := repo.Claim(context.Background(), 1, "task_deferral_fence", Video, testNow, time.Minute, false, 3)
	if err != nil {
		t.Fatal(err)
	}
	now := testNow.Add(2 * time.Minute)
	current, err := repo.Claim(context.Background(), 1, "task_deferral_fence", Video, now, time.Minute, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	code(t, repo.Defer(context.Background(), old, now), "artifact_lease_lost")
	if err = repo.Complete(context.Background(), current, Metadata{MediaType: "video/mp4", Size: 20, SHA256: strings.Repeat("a", 64)}, now); err != nil {
		t.Fatal(err)
	}
	code(t, repo.Defer(context.Background(), old, now), "artifact_lease_lost")
	r, err := repo.Lookup(context.Background(), 1, "task_deferral_fence", Video, now)
	if err != nil || r.Status != Ready || r.Attempts != 2 {
		t.Fatalf("stale deferral corrupted newer result: %+v %v", r, err)
	}
}
