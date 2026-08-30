package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/captiveportal"
)

func captiveFetcher(resp captiveportal.Response) captiveportal.Fetcher {
	return func(context.Context, string) (captiveportal.Response, error) { return resp, nil }
}

func TestRunNetworkCaptiveClearExit0(t *testing.T) {
	fetch := captiveFetcher(captiveportal.Response{StatusCode: 200, Body: "<TITLE>Success</TITLE>"})
	var out, errBuf bytes.Buffer
	if code := runNetworkCaptiveWithIO(nil, &out, &errBuf, fetch); code != 0 {
		t.Fatalf("exit = %d, want 0 for a clear link", code)
	}
	if !strings.Contains(out.String(), "CLEAR") {
		t.Errorf("expected CLEAR verdict, got:\n%s", out.String())
	}
}

func TestRunNetworkCaptivePortalExit1(t *testing.T) {
	fetch := captiveFetcher(captiveportal.Response{StatusCode: 302, Location: "https://login"})
	var out, errBuf bytes.Buffer
	if code := runNetworkCaptiveWithIO(nil, &out, &errBuf, fetch); code != 1 {
		t.Fatalf("exit = %d, want 1 for a captive portal", code)
	}
	if !strings.Contains(out.String(), "CAPTIVE PORTAL") {
		t.Errorf("expected CAPTIVE PORTAL verdict, got:\n%s", out.String())
	}
}

func TestRunNetworkCaptiveRejectsArgs(t *testing.T) {
	fetch := captiveFetcher(captiveportal.Response{StatusCode: 200, Body: "<TITLE>Success</TITLE>"})
	var out, errBuf bytes.Buffer
	if code := runNetworkCaptiveWithIO([]string{"extra"}, &out, &errBuf, fetch); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunNetworkCaptiveJSON(t *testing.T) {
	fetch := captiveFetcher(captiveportal.Response{StatusCode: 200, Body: "<TITLE>Success</TITLE>"})
	var out, errBuf bytes.Buffer
	if code := runNetworkCaptiveWithIO([]string{"--json"}, &out, &errBuf, fetch); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"portal"`) {
		t.Errorf("expected JSON output, got:\n%s", out.String())
	}
}
