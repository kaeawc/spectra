// Package captiveportal replicates Apple's captive-portal probe: it fetches the
// well-known hotspot-detect page over plain HTTP without following redirects
// and classifies the response as a clear link, a captive portal, or a link
// behind a transparent proxy.
package captiveportal

import (
	"context"
	"strings"
)

// ProbeURL is Apple's captive-detection endpoint; a clear link returns the
// canonical success page below.
const ProbeURL = "http://captive.apple.com/hotspot-detect.html"

// successPage is the exact body Apple serves for a clear link. A captive portal
// can embed the <TITLE>Success</TITLE> fragment in its own login page, so the
// whole normalized body must match — a substring check is not enough.
const successPage = "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>"

// proxyServerMarkers are substrings of a Server header that point at a proxy or
// interception product (matched case-insensitively).
var proxyServerMarkers = []string{
	"squid", "proxy", "zscaler", "bluecoat", "blue coat",
	"forcepoint", "netskope", "mcafee", "fortigate", "fortiproxy",
}

// Response is the subset of an HTTP response the classifier needs. It is
// produced by the injected Fetcher so classification can be tested offline.
type Response struct {
	StatusCode int
	Location   string // Location header (for redirects)
	Via        string // Via header (proxy chain)
	Server     string // Server header
	Body       string // response body (bounded by the fetcher)
	ElapsedMS  int64
}

// Fetcher performs the plain-HTTP probe GET without following redirects.
type Fetcher func(ctx context.Context, url string) (Response, error)

// Result is the captive-portal verdict for one probe.
type Result struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code,omitempty"`
	Portal     bool   `json:"portal"`
	Proxied    bool   `json:"proxied,omitempty"`
	Reason     string `json:"reason"`
	Location   string `json:"location,omitempty"`
	Via        string `json:"via,omitempty"`
	Server     string `json:"server,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Probe runs the captive-portal check using fetch and returns the verdict.
func Probe(ctx context.Context, fetch Fetcher) Result {
	res := Result{URL: ProbeURL}
	resp, err := fetch(ctx, ProbeURL)
	if err != nil {
		res.Error = err.Error()
		res.Reason = "probe request failed — the link may be down or fully blocked"
		return res
	}
	res.StatusCode = resp.StatusCode
	res.Location = resp.Location
	res.Via = resp.Via
	res.Server = resp.Server
	res.ElapsedMS = resp.ElapsedMS
	res.Portal, res.Proxied, res.Reason = classify(resp)
	return res
}

// classify decides whether a probe response indicates a captive portal and/or a
// transparent proxy.
func classify(resp Response) (portal, proxied bool, reason string) {
	proxied = resp.Via != "" || serverLooksLikeProxy(resp.Server)

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return true, proxied, "redirected to a portal login page: " + orNone(resp.Location)
	case resp.StatusCode == 511:
		return true, proxied, "511 Network Authentication Required — captive portal"
	case resp.StatusCode == 200 && isSuccessPage(resp.Body):
		if proxied {
			return false, true, "clear, but the response passed through a proxy (" + proxyDetail(resp) + ")"
		}
		return false, false, "clear — Apple success page returned, no captive portal"
	case resp.StatusCode == 200:
		return true, proxied, "200 OK but the body is not Apple's success page — a portal is likely serving its own page"
	default:
		return true, proxied, "unexpected status; treat the link as captive until it returns the success page"
	}
}

// isSuccessPage reports whether the body is exactly Apple's success page once
// surrounding whitespace is trimmed.
func isSuccessPage(body string) bool {
	return strings.TrimSpace(body) == successPage
}

func serverLooksLikeProxy(server string) bool {
	s := strings.ToLower(server)
	for _, m := range proxyServerMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func proxyDetail(resp Response) string {
	if resp.Via != "" {
		return "Via: " + resp.Via
	}
	return "Server: " + resp.Server
}

func orNone(s string) string {
	if s == "" {
		return "(no Location header)"
	}
	return s
}
