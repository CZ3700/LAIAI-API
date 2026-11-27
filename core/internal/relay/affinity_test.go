package relay

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestComputeAffinityKey(t *testing.T) {
	t.Parallel()

	longText := strings.Repeat("a", 800) + "TAIL_DIFFERENCE"
	longText2 := strings.Repeat("a", 800) + "OTHER_TAIL"

	tests := []struct {
		name      string
		sessionID string
		keyID     uint64
		model     string
		req       map[string]any
		reqOther  map[string]any
		wantSame  bool
		wantEmpty bool
		wantPref  string
	}{
		{
			name:      "explicit X-Session-Id takes precedence",
			sessionID: "sess-xyz",
			keyID:     11,
			model:     "gpt-4o",
			req:       map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}},
			reqOther:  map[string]any{"messages": []any{map[string]any{"role": "user", "content": "world"}}},
			wantSame:  true,
			wantPref:  "s:11:gpt-4o:",
		},
		{
			name:     "prompt_cache_key used when no X-Session-Id",
			keyID:    12,
			model:    "gpt-5",
			req:      map[string]any{"prompt_cache_key": "cache-abc", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
			reqOther: map[string]any{"prompt_cache_key": "cache-abc", "messages": []any{map[string]any{"role": "user", "content": "diff"}}},
			wantSame: true,
			wantPref: "s:12:gpt-5:",
		},
		{
			name:     "512B canonical prompt prefix ignores differences after 512 bytes",
			keyID:    13,
			model:    "claude-sonnet-4",
			req:      map[string]any{"messages": []any{map[string]any{"role": "system", "content": longText}}},
			reqOther: map[string]any{"messages": []any{map[string]any{"role": "system", "content": longText2}}},
			wantSame: true,
			wantPref: "p:13:claude-sonnet-4:",
		},
		{
			name:      "empty messages without session yields empty affinity key",
			keyID:     14,
			model:     "gpt-4o",
			req:       map[string]any{"messages": []any{}},
			wantEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k1 := computeAffinityKey(tc.sessionID, tc.keyID, tc.model, tc.req)
			if tc.wantEmpty {
				if k1 != "" {
					t.Fatalf("got %q, want empty affinity key", k1)
				}
				return
			}
			if !strings.HasPrefix(k1, tc.wantPref) {
				t.Fatalf("key = %q, want prefix %q", k1, tc.wantPref)
			}
			if tc.reqOther != nil {
				k2 := computeAffinityKey(tc.sessionID, tc.keyID, tc.model, tc.reqOther)
				if tc.wantSame && k1 != k2 {
					t.Fatalf("keys differ: %q vs %q", k1, k2)
				}
				if !tc.wantSame && k1 == k2 {
					t.Fatalf("keys unexpectedly collided: %q", k1)
				}
			}
		})
	}
}

func TestAffinityPickBYOKAndEviction(t *testing.T) {
	t.Parallel()

	now := time.Now
	r := &Relay{
		Now:           now,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		health:        map[uint64]float64{},
		breakerStates: map[uint64]*breakerState{},
	}
	r.aff.init(now)

	ch1 := Channel{ID: 1, Name: "c1", Provider: "openai", Models: "gpt-4o", Weight: 100, Priority: 10, Status: 1}
	ch2 := Channel{ID: 2, Name: "c2", Provider: "openai", Models: "gpt-4o", Weight: 1, Priority: 10, Status: 1}
	ch3 := Channel{ID: 3, Name: "c3-byok-org99", Provider: "openai", Models: "gpt-4o", Weight: 100, Priority: 10, Status: 1, OwnerOrgID: 99}
	ch1.compile()
	ch2.compile()
	ch3.compile()
	snap := []Channel{ch1, ch2, ch3}

	ctx := context.Background()
	affKey := computeAffinityKey("sess-sticky", 1, "gpt-4o", nil)
	r.aff.bind(ctx, affKey, ch2.ID)

	stickyID, ok := r.aff.get(ctx, affKey)
	if !ok || stickyID != ch2.ID {
		t.Fatalf("stickyID=%d ok=%v, want ch2=%d", stickyID, ok, ch2.ID)
	}

	// Sticky binding selects ch2 deterministically over higher-weight ch1 in same tier.
	for i := range 20 {
		picked, err := r.pickSnapshotPolicyWithAffinity(snap, "gpt-4o", 0, nil, "", 1, nil, stickyID)
		if err != nil {
			t.Fatalf("run %d pick error: %v", i, err)
		}
		if picked.ID != ch2.ID {
			t.Fatalf("run %d picked channel %d, want sticky channel %d", i, picked.ID, ch2.ID)
		}
	}

	// BYOK org isolation must never be bypassed by a sticky channel ID belonging to another org.
	pickedOtherOrg, err := r.pickSnapshotPolicyWithAffinity(snap, "gpt-4o", 0, nil, "", 1, nil, ch3.ID)
	if err != nil {
		t.Fatalf("pick other org: %v", err)
	}
	if pickedOtherOrg.ID == ch3.ID {
		t.Fatalf("org 1 request leaked onto org 99 BYOK channel %d via sticky ID", ch3.ID)
	}

	// Evicting ch2 clears sticky mapping immediately.
	r.aff.evictChannel(ctx, ch2.ID)
	if got, found := r.aff.get(ctx, affKey); found || got != 0 {
		t.Fatalf("after evictChannel, got=%d found=%v, want cleared", got, found)
	}

	// Evict via breaker opening or disableChannel helper also clears local entries.
	r.aff.bind(ctx, affKey, ch2.ID)
	for range breakerThreshold {
		r.breakerNote(ch2.ID, false)
	}
	if got, found := r.aff.get(ctx, affKey); found || got != 0 {
		t.Fatalf("after breaker tripped, got=%d found=%v, want evicted", got, found)
	}
}
