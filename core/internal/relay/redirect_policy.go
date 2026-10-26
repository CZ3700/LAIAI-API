package relay

import (
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// NewUpstreamClient preserves the caller's existing first-hop network policy.
// Redirects may not move credentials or request bodies to another origin.
func NewUpstreamClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: upstreamCheckRedirect}
}

type requestOrigin struct {
	scheme string
	host   string
	port   uint16
}

func redirectOrigin(u *url.URL) (requestOrigin, bool) {
	if u == nil || u.Opaque != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(u.Host, "/\\?#@") || strings.ContainsFunc(u.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return requestOrigin{}, false
	}
	var host, port string
	if strings.HasPrefix(u.Host, "[") {
		end := strings.IndexByte(u.Host, ']')
		if end < 0 {
			return requestOrigin{}, false
		}
		addr, err := netip.ParseAddr(u.Host[1:end])
		if err != nil || !addr.Is6() {
			return requestOrigin{}, false
		}
		// Addr.String normalizes the address, but preserves the case-sensitive
		// interface zone. Never lowercase an IPv6 zone as if it were DNS.
		host = addr.String()
		rest := u.Host[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
				return requestOrigin{}, false
			}
			port = rest[1:]
		}
	} else {
		if strings.Count(u.Host, ":") > 1 {
			return requestOrigin{}, false
		}
		host = u.Host
		if at := strings.LastIndexByte(host, ':'); at >= 0 {
			port, host = host[at+1:], host[:at]
			if port == "" {
				return requestOrigin{}, false
			}
		}
		if host == "" || strings.ContainsAny(host, "[]%/\\?#@") || strings.ContainsFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return requestOrigin{}, false
		}
		// Do not fold trailing dots, subdomains or other DNS aliases.
		host = strings.ToLower(host)
	}
	n := uint64(80)
	if u.Scheme == "https" {
		n = 443
	}
	if port != "" {
		for _, c := range port {
			if c < '0' || c > '9' {
				return requestOrigin{}, false
			}
		}
		var err error
		n, err = strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return requestOrigin{}, false
		}
	}
	return requestOrigin{scheme: u.Scheme, host: host, port: uint16(n)}, true
}

func upstreamCheckRedirect(req *http.Request, via []*http.Request) error {
	return checkRequestRedirect(req, via, false)
}

func mediaCheckRedirect(req *http.Request, via []*http.Request) error {
	return checkRequestRedirect(req, via, true)
}

func checkRequestRedirect(req *http.Request, via []*http.Request, media bool) error {
	// Preserve the old guarded client's boundary: via includes the first request,
	// so at most five requests may be sent. Keep the last 3xx for the caller.
	if req == nil || len(via) == 0 || len(via) >= 5 {
		return http.ErrUseLastResponse
	}
	var initial, previous requestOrigin
	crossed := false
	for i := 0; i <= len(via); i++ {
		current := req
		if i < len(via) {
			current = via[i]
		}
		if current == nil {
			return http.ErrUseLastResponse
		}
		origin, ok := redirectOrigin(current.URL)
		if !ok {
			return http.ErrUseLastResponse
		}
		if media && ((current.Method != http.MethodGet && current.Method != http.MethodHead) || (current.Body != nil && current.Body != http.NoBody) || current.ContentLength != 0) {
			return http.ErrUseLastResponse
		}
		if i == 0 {
			initial = origin
		} else {
			if previous.scheme == "https" && origin.scheme == "http" {
				return http.ErrUseLastResponse
			}
			if origin != initial {
				crossed = true
			}
		}
		previous = origin
	}
	if crossed {
		if !media {
			return http.ErrUseLastResponse
		}
		// Go copies headers from the initial request on EVERY redirect. Clearing
		// only the first cross-origin hop leaks again on A->B->B or A->B->A.
		// No inherited header value is safe: arbitrary override names (including
		// Accept/User-Agent) can contain a credential. Jar must remain nil too.
		req.Header = make(http.Header)
		req.Host = ""
	}
	return nil
}
