package config

import (
	"os"
	"strings"
	"testing"
)

func TestVideoResponseInboxDeploymentGate(t *testing.T) {
	// No database or video provider is contacted by Load. Clear optional
	// deployment settings so this contract does not depend on the test host.
	for _, key := range []string{
		"LAIAI_DSN_REPLICAS", "LAIAI_DB_MAX_OPEN_CONNS", "LAIAI_DB_MAX_IDLE_CONNS",
		"LAIAI_ADMIN_TOKEN", "LAIAI_DATA_KEYS", "LAIAI_DATA_KEY_ACTIVE",
		"LAIAI_VIDEO_HMAC_SECRET", "LAIAI_INVOCATION_RETENTION_DAYS", "LAIAI_TRUSTED_PROXIES",
		"LAIAI_VIDEO_MEDIA_ROOT", "LAIAI_VIDEO_MEDIA_PUBLIC_BASE_URL", "LAIAI_VIDEO_MEDIA_SIGNING_KEY",
		"LAIAI_VIDEO_MEDIA_FFPROBE_PATH", "LAIAI_VIDEO_MEDIA_SCAN_COMMAND",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("LAIAI_DSN", "test@tcp(localhost:3306)/test?parseTime=true")
	t.Setenv("LAIAI_AUTH_SECRET", strings.Repeat("a", 32))
	for _, tc := range []struct {
		name  string
		value string
		unset bool
		want  bool
		bad   bool
	}{
		{name: "unset_defaults_off", unset: true},
		{name: "empty_defaults_off"},
		{name: "explicit_false", value: "false"},
		{name: "explicit_true", value: "true", want: true},
		{name: "numeric_zero", value: "0", bad: true},
		{name: "numeric_one", value: "1", bad: true},
		{name: "uppercase", value: "TRUE", bad: true},
		{name: "mixed_case", value: "False", bad: true},
		{name: "leading_space", value: " true", bad: true},
		{name: "trailing_space", value: "false ", bad: true},
		{name: "only_space", value: " ", bad: true},
		{name: "newline", value: "true\n", bad: true},
		{name: "arbitrary_value", value: "not-a-boolean-private-value", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LAIAI_VIDEO_RESPONSE_INBOX", tc.value)
			if tc.unset {
				if err := os.Unsetenv("LAIAI_VIDEO_RESPONSE_INBOX"); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if tc.bad {
				if err == nil || cfg != nil || err.Error() != "config: LAIAI_VIDEO_RESPONSE_INBOX must be exactly true or false when set" {
					t.Fatalf("invalid deployment gate must reject startup without echoing the supplied value: config=%v error=%v", cfg != nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.VideoResponseInboxEnabled != tc.want {
				t.Fatalf("response inbox registration enabled=%v, want %v", cfg.VideoResponseInboxEnabled, tc.want)
			}
		})
	}
}
