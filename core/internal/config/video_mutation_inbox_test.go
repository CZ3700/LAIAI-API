package config

import (
	"os"
	"strings"
	"testing"
)

func TestVideoMutationInboxIndependentStrictDeploymentGate(t *testing.T) {
	for _, key := range []string{
		"LAIAI_DSN_REPLICAS", "LAIAI_DB_MAX_OPEN_CONNS", "LAIAI_DB_MAX_IDLE_CONNS",
		"LAIAI_ADMIN_TOKEN", "LAIAI_DATA_KEYS", "LAIAI_DATA_KEY_ACTIVE", "LAIAI_VIDEO_HMAC_SECRET",
		"LAIAI_INVOCATION_RETENTION_DAYS", "LAIAI_TRUSTED_PROXIES", "LAIAI_VIDEO_MEDIA_ROOT",
		"LAIAI_VIDEO_MEDIA_PUBLIC_BASE_URL", "LAIAI_VIDEO_MEDIA_SIGNING_KEY", "LAIAI_VIDEO_MEDIA_FFPROBE_PATH", "LAIAI_VIDEO_MEDIA_SCAN_COMMAND",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("LAIAI_DSN", "test@tcp(localhost:3306)/test?parseTime=true")
	t.Setenv("LAIAI_AUTH_SECRET", strings.Repeat("a", 32))
	for _, create := range []string{"false", "true"} {
		t.Run("create_"+create, func(t *testing.T) {
			t.Setenv("LAIAI_VIDEO_RESPONSE_INBOX", create)
			for _, value := range []string{"", "false", "true", "0", "1", "TRUE", "False", " true", "false ", " ", "true\n", "private-invalid-value"} {
				t.Run("mutation_"+value, func(t *testing.T) {
					t.Setenv("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX", value)
					cfg, err := Load()
					if value != "" && value != "false" && value != "true" {
						if cfg != nil || err == nil || err.Error() != "config: LAIAI_VIDEO_MUTATION_RESPONSE_INBOX must be exactly true or false when set" {
							t.Fatal("invalid mutation producer flag must fail without reflecting its value")
						}
						return
					}
					if err != nil || cfg.VideoResponseInboxEnabled != (create == "true") || cfg.VideoMutationResponseInboxEnabled != (value == "true") {
						t.Fatal("producer gates are not independent", err)
					}
				})
			}
			t.Run("mutation_unset", func(t *testing.T) {
				t.Setenv("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX", "")
				if err := os.Unsetenv("LAIAI_VIDEO_MUTATION_RESPONSE_INBOX"); err != nil {
					t.Fatal(err)
				}
				cfg, err := Load()
				if err != nil || cfg.VideoMutationResponseInboxEnabled || cfg.VideoResponseInboxEnabled != (create == "true") {
					t.Fatal("unset mutation producer flag must stay off", err)
				}
			})
		})
	}
}
