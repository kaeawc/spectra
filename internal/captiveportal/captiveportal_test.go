package captiveportal

import (
	"context"
	"errors"
	"testing"
)

const successBody = "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>\n"

func fetcher(resp Response, err error) Fetcher {
	return func(context.Context, string) (Response, error) { return resp, err }
}

func TestProbeClear(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 200, Body: successBody, ElapsedMS: 12}, nil))
	if r.Portal || r.Proxied {
		t.Errorf("expected a clear link, got portal=%v proxied=%v", r.Portal, r.Proxied)
	}
	if r.StatusCode != 200 {
		t.Errorf("status = %d", r.StatusCode)
	}
}

func TestProbeRedirectPortal(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 302, Location: "https://portal.hotel.example/login"}, nil))
	if !r.Portal {
		t.Fatal("a 3xx redirect must be flagged as a captive portal")
	}
	if r.Location != "https://portal.hotel.example/login" {
		t.Errorf("location = %q", r.Location)
	}
}

func TestProbe200WithLoginBody(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 200, Body: "<html><body>Please sign in to WiFi</body></html>"}, nil))
	if !r.Portal {
		t.Error("200 with a non-success body must be flagged as a captive portal")
	}
}

func TestProbeMarkerFragmentIsNotSuccess(t *testing.T) {
	// A portal that embeds the success title in its own login page must not
	// pass as CLEAR.
	body := "<html><head><TITLE>Success</TITLE></head><body>Please log in to continue</body></html>"
	r := Probe(context.Background(), fetcher(Response{StatusCode: 200, Body: body}, nil))
	if !r.Portal {
		t.Error("a body containing the marker but not the exact success page must be a portal")
	}
}

func TestProbeProxyServerHeader(t *testing.T) {
	// A clear success page whose Server header names a proxy product is PROXIED.
	r := Probe(context.Background(), fetcher(Response{StatusCode: 200, Body: successBody, Server: "ZScaler/1.0"}, nil))
	if r.Portal {
		t.Error("a success page is not a portal")
	}
	if !r.Proxied {
		t.Error("a proxy-product Server header must mark the link PROXIED even without a Via header")
	}
}

func TestProbe511(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 511}, nil))
	if !r.Portal {
		t.Error("511 must be flagged as a captive portal")
	}
}

func TestProbeProxiedButClear(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 200, Body: successBody, Via: "1.1 proxy.corp"}, nil))
	if r.Portal {
		t.Error("a success page behind a proxy is not a captive portal")
	}
	if !r.Proxied || r.Via != "1.1 proxy.corp" {
		t.Errorf("expected proxied with the Via header, got proxied=%v via=%q", r.Proxied, r.Via)
	}
}

func TestProbeFetchError(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{}, errors.New("dial tcp: no route to host")))
	if r.Error == "" {
		t.Error("a fetch failure must be recorded")
	}
	if r.Portal {
		t.Error("a hard fetch failure is an error, not a portal verdict")
	}
}

func TestProbeUnexpectedStatus(t *testing.T) {
	r := Probe(context.Background(), fetcher(Response{StatusCode: 403}, nil))
	if !r.Portal {
		t.Error("an unexpected status should be treated as captive until it returns success")
	}
}
