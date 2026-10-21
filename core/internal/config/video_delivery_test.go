package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSeedanceStartsWithoutArtifactStorage(t *testing.T) {
	t.Setenv("LAIAI_DSN", "test@tcp(localhost:3306)/test?parseTime=true")
	t.Setenv("LAIAI_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("LAIAI_DATA_KEYS", "test:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("LAIAI_DATA_KEY_ACTIVE", "test")
	t.Setenv("LAIAI_VIDEO_HMAC_SECRET", strings.Repeat("h", 32))
	for _, key := range []string{"LAIAI_VIDEO_ARTIFACT_ROOT", "LAIAI_VIDEO_ARTIFACT_TEMP", "LAIAI_VIDEO_ARTIFACT_HOSTS", "LAIAI_VIDEO_MEDIA_ROOT", "LAIAI_VIDEO_MEDIA_PUBLIC_BASE_URL", "LAIAI_VIDEO_MEDIA_SIGNING_KEY", "LAIAI_VIDEO_MEDIA_FFPROBE_PATH", "LAIAI_VIDEO_MEDIA_SCAN_COMMAND"} {
		t.Setenv(key, "")
	}
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Keyring == nil || len(config.VideoHMACSecret) != 32 {
		t.Fatal("video gateway not configured")
	}
}
