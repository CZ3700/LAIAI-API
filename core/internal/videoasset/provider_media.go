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

// Reference eligibility follows the official state: an active asset with a
// provider id is usable for every media type. The measured-facts requirement
// for audio/video made URL-mode assets (which the platform never probes, by
// design — Volcano downloads and owns the media) permanently ineligible;
// dropped in favor of upstream verification. Measured facts remain optional
// console metadata surfaced as the media-verification status. Zero duration is
// never fabricated: billing estimates keep the caller-declared duration and the
// model's own cap.
func referenceMediaReady(a Asset) bool {
	return a.Status == StatusActive && a.ProviderID != nil && *a.ProviderID != ""
}

// The list filter matches referenceMediaReady.
const referenceMediaReadySQL = `video_assets.provider_id IS NOT NULL AND video_assets.provider_id <> ''`
