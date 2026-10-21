package invocation

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strings"
)

// VideoDelivery is the entire plaintext schema of a delivery capsule. Keeping
// it separate prevents disabled history capture from retaining arbitrary data.
type VideoDelivery struct {
	VideoURL     string `json:"video_url,omitempty"`
	LastFrameURL string `json:"last_frame_url,omitempty"`
}

// ValidVideoDeliveryURL checks a provider-returned link without fetching it or
// rewriting its signature. It never accepts transport credentials in a URL.
func ValidVideoDeliveryURL(raw string, secrets []string) bool {
	if raw == "" || len(raw) > 16384 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || net.ParseIP(host) != nil {
		return false
	}
	// Browsers apply WHATWG IPv4 parsing, accepting legacy decimal/octal/hex
	// forms that Go's net.ParseIP rejects. A numeric final DNS label triggers
	// that algorithm; public DNS TLDs are not numeric, so reject it outright.
	last := host[strings.LastIndex(host, ".")+1:]
	digits, hex := true, strings.HasPrefix(last, "0x") && len(last) > 2
	for _, c := range last {
		if c < '0' || c > '9' {
			digits = false
		}
	}
	if hex {
		for _, c := range last[2:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				hex = false
			}
		}
	}
	if digits || hex {
		return false
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return false
	}
	for _, secret := range secrets {
		if secret != "" && (strings.Contains(raw, secret) || strings.Contains(decoded, secret)) {
			return false
		}
	}
	return true
}

func videoDeliveryJSON(raw []byte, secrets []string) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var links VideoDelivery
	if err := d.Decode(&links); err != nil {
		return nil, ErrVideoInvalid
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, ErrVideoInvalid
	}
	if links.VideoURL == "" && links.LastFrameURL == "" {
		return nil, ErrVideoInvalid
	}
	for _, link := range []string{links.VideoURL, links.LastFrameURL} {
		if link != "" && !ValidVideoDeliveryURL(link, secrets) {
			return nil, ErrVideoInvalid
		}
	}
	return json.Marshal(links)
}
