package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/xpcattr"
)

func fakeWhois(a xpcattr.Attribution, err error) whoisCapture {
	return func(context.Context, xpcattr.Options) (xpcattr.Attribution, error) { return a, err }
}

var whoisFixture = xpcattr.Attribution{
	PID: 17874, Name: "OSLogService", Source: xpcattr.SourceSample,
	Clients: []xpcattr.Client{{
		PID: 17649, Name: "Runner", Weight: 800, Share: 0.8, CommandLine: "/x/Runner",
		Chain:    []xpcattr.ProcRef{{PID: 17491, Name: "xcodebuild"}},
		Launcher: &xpcattr.ProcRef{PID: 17491, Name: "xcodebuild"},
	}},
}

func TestRunWhoisHuman(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWhoisWithIO([]string{"17874"}, &out, &errBuf, fakeWhois(whoisFixture, nil)); code != 0 {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"OSLogService (17874) busy, driven by Runner (17649), launched by xcodebuild (17491)", "80.0%", "<- xcodebuild (17491)"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRunWhoisJSONAndErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWhoisWithIO([]string{"--json", "1"}, &out, &errBuf, fakeWhois(whoisFixture, nil)); code != 0 || !strings.Contains(out.String(), `"source": "sample"`) {
		t.Errorf("json exit/out: %d %s", code, out.String())
	}
	if code := runWhoisWithIO(nil, &out, &errBuf, fakeWhois(whoisFixture, nil)); code != 2 {
		t.Errorf("missing pid exit = %d", code)
	}
	if code := runWhoisWithIO([]string{"abc"}, &out, &errBuf, fakeWhois(whoisFixture, nil)); code != 2 {
		t.Errorf("bad pid exit = %d", code)
	}
	errBuf.Reset()
	if code := runWhoisWithIO([]string{"5"}, &out, &errBuf, fakeWhois(xpcattr.Attribution{}, errors.New("boom"))); code != 1 || !strings.Contains(errBuf.String(), "boom") {
		t.Errorf("capture error exit = %d %q", code, errBuf.String())
	}
}

func TestRunWhoisSetsDeadline(t *testing.T) {
	var out, errBuf bytes.Buffer
	var got time.Duration
	capture := func(ctx context.Context, o xpcattr.Options) (xpcattr.Attribution, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("no deadline")
		}
		got = time.Until(dl)
		return xpcattr.Attribution{PID: o.PID}, nil
	}
	runWhoisWithIO([]string{"--duration", "5s", "9"}, &out, &errBuf, capture)
	if got < 15*time.Second || got > 20*time.Second {
		t.Errorf("deadline in %v, want about 20s", got)
	}
}

func TestRunWhoisNoClients(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWhoisWithIO([]string{"5"}, &out, &errBuf, fakeWhois(xpcattr.Attribution{PID: 5}, nil)); code != 0 || !strings.Contains(out.String(), "No XPC/Mach clients") {
		t.Errorf("exit %d out %q", code, out.String())
	}
}
