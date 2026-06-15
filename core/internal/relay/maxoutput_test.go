package relay

import "testing"

// P0-2：hold 估算同时识别 max_tokens 与 max_completion_tokens，两者都给取较大者。
func TestRequestedMaxOutput(t *testing.T) {
	cases := []struct {
		name string
		req  map[string]any
		want int64
	}{
		{"max_tokens only", map[string]any{"max_tokens": float64(100)}, 100},
		{"max_completion_tokens only", map[string]any{"max_completion_tokens": float64(5000)}, 5000},
		{"both → larger", map[string]any{"max_tokens": float64(100), "max_completion_tokens": float64(5000)}, 5000},
		{"both → larger (reversed)", map[string]any{"max_tokens": float64(9000), "max_completion_tokens": float64(5000)}, 9000},
		{"neither", map[string]any{}, 0},
		{"zero ignored", map[string]any{"max_tokens": float64(0)}, 0},
	}
	for _, c := range cases {
		if got := requestedMaxOutput(c.req); got != c.want {
			t.Fatalf("%s: requestedMaxOutput = %d, want %d", c.name, got, c.want)
		}
	}
}
