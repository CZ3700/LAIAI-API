package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVideoMediaConfigurationFailsClosed(t *testing.T) {
	keys := []string{"LAIAI_VIDEO_MEDIA_ROOT", "LAIAI_VIDEO_MEDIA_PUBLIC_BASE_URL", "LAIAI_VIDEO_MEDIA_SIGNING_KEY", "LAIAI_VIDEO_MEDIA_FFPROBE_PATH", "LAIAI_VIDEO_MEDIA_SCAN_COMMAND"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	c := &Config{VideoHMACSecret: []byte(strings.Repeat("h", 32))}
	if err := c.loadVideoMedia(); err != nil {
		t.Fatal("optional media configuration should remain disabled", err)
	}
	t.Setenv(keys[0], filepath.Join(t.TempDir(), "private-media"))
	if err := c.loadVideoMedia(); err == nil {
		t.Fatal("partial media configuration accepted")
	}
	t.Setenv(keys[1], "https://media.example.com/api/video/media-source")
	t.Setenv(keys[2], strings.Repeat("m", 32))
	t.Setenv(keys[3], "ffprobe")
	t.Setenv(keys[4], `["clamdscan","--no-summary"]`)
	if err := c.loadVideoMedia(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://media.example.com/api/video/media-source", "https://media.example.com/other", "https://user:secret@media.example.com/api/video/media-source", "https://media.example.com/api/video/media-source?secret=value"} {
		t.Setenv(keys[1], bad)
		if err := c.loadVideoMedia(); err == nil {
			t.Fatal("unsafe source URL accepted")
		}
	}
	t.Setenv(keys[1], "https://media.example.com/api/video/media-source")
	for _, bad := range []string{`[]`, `null`, `"scanner --unsafe-shell"`, `[""]`} {
		t.Setenv(keys[4], bad)
		if err := c.loadVideoMedia(); err == nil {
			t.Fatal("invalid scanner command accepted")
		}
	}
}
