package videoartifact

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

type DownloadConfig struct {
	// Exact DNS hostnames approved by the operator. Empty means disabled;
	// wildcards, literal IPs, localhost and private-address overrides are absent.
	AllowedHosts []string
	MaxBytes     int64
	Timeout      time.Duration
	// Must be a private, access-controlled local directory, never a web root.
	TempDir string
}

type Downloader struct {
	config DownloadConfig
	hosts  map[string]bool
	client *http.Client
	lookup func(context.Context, string) ([]net.IPAddr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
}

func NewDownloader(config DownloadConfig) (*Downloader, error) {
	if len(config.AllowedHosts) == 0 || config.MaxBytes <= 0 || config.MaxBytes > 10<<30 || config.Timeout <= 0 || config.TempDir == "" {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	info, err := os.Stat(config.TempDir)
	if err != nil || !info.IsDir() {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	d := &Downloader{config: config, hosts: make(map[string]bool), lookup: net.DefaultResolver.LookupIPAddr}
	for _, host := range config.AllowedHosts {
		if !validHost(host) {
			return nil, failure("invalid_artifact_config", 503, false)
		}
		d.hosts[host] = true
	}
	d.dial = (&net.Dialer{Timeout: config.Timeout}).DialContext
	transport := &http.Transport{Proxy: nil, DialContext: d.dialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableCompression: true, DisableKeepAlives: true, TLSHandshakeTimeout: config.Timeout, ResponseHeaderTimeout: config.Timeout, MaxResponseHeaderBytes: 32 << 10}
	d.client = &http.Client{Transport: transport, Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return d, nil
}

func validHost(host string) bool {
	if len(host) > 253 || host != strings.ToLower(host) || net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (d *Downloader) validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || !d.hosts[u.Hostname()] || (u.Port() != "" && u.Port() != "443") {
		return nil, failure("artifact_url_blocked", 502, false)
	}
	return u, nil
}

var blockedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	for _, prefix := range blockedRanges {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

func (d *Downloader) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || !d.hosts[host] {
		return nil, failure("artifact_url_blocked", 502, false)
	}
	ips, err := d.lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, failure("artifact_download_failed", 502, true)
	}
	// Reject the complete DNS answer when any address is unsafe. Dial the exact
	// already-checked IP; there is no second DNS lookup or rebinding gap.
	for _, candidate := range ips {
		if candidate.Zone != "" || !publicIP(candidate.IP) {
			return nil, failure("artifact_url_blocked", 502, false)
		}
	}
	for _, candidate := range ips {
		conn, dialErr := d.dial(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, failure("artifact_download_failed", 502, true)
}

func allowedMIME(kind Kind, value string) bool {
	return kind == Video && (value == "video/mp4" || value == "video/quicktime") || kind == LastFrame && value == "image/jpeg"
}

type temporaryFile struct {
	*os.File
	path string
}

func (f *temporaryFile) Close() error {
	closeErr := f.File.Close()
	removeErr := os.Remove(f.path)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

// Fetch makes one GET, follows no redirects, forwards no caller/provider
// credentials, bounds bytes/time and spools to a private temporary file.
func (d *Downloader) Fetch(ctx context.Context, raw string, kind Kind) (*Download, error) {
	if d == nil || d.client == nil || (kind != Video && kind != LastFrame) {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	u, err := d.validateURL(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, failure("artifact_url_blocked", 502, false)
	}
	req.Header.Set("Accept-Encoding", "identity")
	res, err := d.client.Do(req)
	if err != nil {
		var safe *Error
		if errors.As(err, &safe) && safe.Code == "artifact_url_blocked" {
			return nil, failure("artifact_url_blocked", 502, false)
		}
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, failure("artifact_download_timeout", 504, true)
		}
		return nil, failure("artifact_download_failed", 502, true)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, failure("artifact_download_failed", 502, true)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || !allowedMIME(kind, mediaType) || (res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity") {
		return nil, failure("artifact_invalid_media", 502, false)
	}
	if res.ContentLength > d.config.MaxBytes {
		return nil, failure("artifact_too_large", 502, false)
	}
	file, err := os.CreateTemp(d.config.TempDir, "ark-artifact-*")
	if err != nil {
		return nil, failure("artifact_transfer_failed", 503, true)
	}
	temp := &temporaryFile{File: file, path: file.Name()}
	success := false
	defer func() {
		if !success {
			_ = temp.Close()
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(res.Body, d.config.MaxBytes+1))
	if err != nil || ctx.Err() != nil {
		if ctx.Err() != nil {
			return nil, failure("artifact_download_timeout", 504, true)
		}
		return nil, failure("artifact_download_failed", 502, true)
	}
	if size > d.config.MaxBytes {
		return nil, failure("artifact_too_large", 502, false)
	}
	if size == 0 || (res.ContentLength >= 0 && res.ContentLength != size) {
		return nil, failure("artifact_invalid_media", 502, false)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, failure("artifact_transfer_failed", 503, true)
	}
	metadata := Metadata{MediaType: mediaType, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	if kind == LastFrame {
		config, decodeErr := jpeg.DecodeConfig(file)
		if decodeErr != nil || config.Width <= 0 || config.Height <= 0 {
			return nil, failure("artifact_invalid_media", 502, false)
		}
		metadata.Width = &config.Width
		metadata.Height = &config.Height
	} else {
		var prefix [12]byte
		if _, readErr := io.ReadFull(file, prefix[:]); readErr != nil || string(prefix[4:8]) != "ftyp" {
			return nil, failure("artifact_invalid_media", 502, false)
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, failure("artifact_transfer_failed", 503, true)
	}
	success = true
	return &Download{Body: temp, Metadata: metadata}, nil
}
