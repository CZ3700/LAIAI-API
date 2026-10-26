package invocation

import (
	"context"
	"errors"
	"testing"
)

func TestVideoExecutionPurgeBoundsAndAuthority(t *testing.T) {
	s, db, _ := videoSetup(t)
	p := videoStage(t, s, db, videoInput(s))
	for _, batch := range []int{-1, 0, 1001, int(^uint(0) >> 1)} {
		if n, err := s.PurgeExpired(context.Background(), batch); n != 0 || !errors.Is(err, ErrVideoInvalid) {
			t.Fatal("invalid retention batch accepted", batch, n, err)
		}
	}
	if n, err := s.PurgeExpired(context.Background(), 1); n != 0 || !errors.Is(err, ErrVideoExecutionAuthorityUnavailable) {
		t.Fatal("SQLite must not supply execution authority", n, err)
	}
	meta := purgeMetadata(t, db, p)
	if meta.CaptureStatus != VideoCaptureStaged || meta.WrappedKey != p.metadata.WrappedKey {
		t.Fatal("unsupported authority changed payload")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := s.PurgeExpired(ctx, 1); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("retention cancellation cause lost", n, err)
	}
}
