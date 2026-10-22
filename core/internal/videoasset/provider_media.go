package videoasset

import (
	"context"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// publicAssetSourceURL performs structural validation only. The gateway does not
// resolve or fetch the URL; Ark owns the download, inspection and registration.
// It must not be described as evidence that the source was locally verified.
func publicAssetSourceURL(raw string) bool {
	if !validHTTPS(raw, 4096) || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		return publicIP(ip)
	}
	if host == "localhost" || !strings.Contains(host, ".") || strings.ContainsAny(host, ":%\\") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	// Reject shortened/decimal/hex numeric address spellings rather than relying
	// on a downstream URL parser's interpretation of an apparent hostname.
	labels := strings.Split(host, ".")
	tld := labels[len(labels)-1]
	if strings.HasPrefix(tld, "0x") || strings.IndexFunc(tld, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
	}
	return true
}

// Existing internally verified immutable media may retain its measured facts.
// An unregistered public URL is sent to Ark unchanged with no invented facts.
// Neither branch performs network downloads or scans in this package.
func (s *Service) registrationMedia(ctx context.Context, org uint64, raw, kind string) (MediaEvidence, error) {
	if s.console.Media != nil && strings.HasPrefix(raw, s.console.Media.base+"/") {
		m, err := s.verifyMedia(ctx, org, raw, kind)
		if err != nil || !validMedia(m, kind, s.now()) {
			return MediaEvidence{}, ErrInvalid
		}
		return m, nil
	}
	if s.options.VerifyMedia != nil {
		if m, err := s.options.VerifyMedia(ctx, org, raw, kind); err == nil && validMedia(m, kind, s.now()) {
			return m, nil
		}
	}
	return MediaEvidence{FetchURL: raw}, nil
}

// A confirmed official image can be referenced without a platform copy. Video
// and audio references still need trusted duration facts; zero is never used as
// a substitute for an unknown input length in admission or billing.
func referenceMediaReady(a Asset) bool {
	if a.Status != StatusActive || a.ProviderID == nil || *a.ProviderID == "" {
		return false
	}
	return a.MediaType == "image" || (a.MediaType == "video" || a.MediaType == "audio") && hasMediaFacts(a)
}

// The list filter preserves the same media-type distinction as referenceMediaReady.
// Measured facts are written only by the optional verified-media path. Image
// eligibility comes from the official state, not from a fictitious local copy.
const referenceMediaReadySQL = `video_assets.provider_id IS NOT NULL AND video_assets.provider_id <> '' AND
(video_assets.media_type = 'image' OR
 (video_assets.metadata_version > 0 AND video_assets.immutable_object_ref <> '' AND
 LENGTH(video_assets.object_sha256) = 64 AND video_assets.verified_at IS NOT NULL AND video_assets.byte_size > 0 AND
 video_assets.duration_ms BETWEEN 2000 AND 30000 AND
 ((video_assets.media_type = 'audio' AND video_assets.byte_size <= 15000000 AND video_assets.mime IN ('audio/mpeg','audio/wav','audio/x-wav')) OR
  (video_assets.media_type = 'video' AND video_assets.byte_size <= 200000000 AND video_assets.mime IN ('video/mp4','video/quicktime') AND
   video_assets.width BETWEEN 300 AND 6000 AND video_assets.height BETWEEN 300 AND 6000 AND
   video_assets.width * 10 >= video_assets.height * 4 AND video_assets.width * 2 <= video_assets.height * 5 AND
   video_assets.width * video_assets.height BETWEEN 407696 AND 8295044 AND
   video_assets.fps_den BETWEEN 1 AND 1000000 AND video_assets.fps_num BETWEEN 24 * video_assets.fps_den AND 60 * video_assets.fps_den))))`
