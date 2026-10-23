package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/xpcattr"
)

func fakeXPC(a xpcattr.Attribution, err error, calls *int) xpcAttributeFunc {
	return func(context.Context, int, []process.Info) (xpcattr.Attribution, error) {
		*calls++
		return a, err
	}
}

func TestAppendXPCCauses(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	busy := []process.Info{{PID: 17874, Command: "OSLogService", ExecutablePath: "/usr/libexec/OSLogService", CPUPct: 95}}
	var calls int
	a := xpcattr.Attribution{PID: 17874, Clients: []xpcattr.Client{{
		PID: 17649, Name: "Runner", Launcher: &xpcattr.ProcRef{PID: 17491, Name: "xcodebuild"},
	}}}
	got := appendXPCCauses(nil, busy, fakeXPC(a, nil, &calls))
	want := "OSLogService (17874) busy, driven by Runner (17649), launched by xcodebuild (17491)"
	if len(got) != 1 || got[0].Detail != want || calls != 1 {
		t.Fatalf("got %+v calls=%d", got, calls)
	}

	if got := appendXPCCauses(nil, busy, fakeXPC(xpcattr.Attribution{}, errors.New("sample failed"), &calls)); len(got) != 0 {
		t.Errorf("error should add nothing: %+v", got)
	}
}

func TestAppendXPCCausesGating(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	var calls int
	f := fakeXPC(xpcattr.Attribution{}, nil, &calls)
	cases := map[string][]process.Info{
		"idle service":     {{PID: 1, Command: "OSLogService", CPUPct: 10}},
		"busy non-service": {{PID: 2, Command: "Slack", ExecutablePath: "/Applications/Slack.app/Contents/MacOS/Slack", CPUPct: 99}},
	}
	for name, procs := range cases {
		appendXPCCauses(nil, procs, f)
		if calls != 0 {
			t.Errorf("%s: sampled", name)
		}
	}
	appendXPCCauses(nil, []process.Info{{PID: 3, Command: "OSLogService", CPUPct: 99}}, nil)
}

func TestAppendXPCCausesSkippedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	var calls int
	appendXPCCauses(nil, []process.Info{{PID: 1, Command: "OSLogService", CPUPct: 99}}, fakeXPC(xpcattr.Attribution{}, nil, &calls))
	if calls != 0 {
		t.Error("sampled off Darwin")
	}
}

func TestAppendXPCCausesBoundedByTimeout(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	slow := func(ctx context.Context, _ int, _ []process.Info) (xpcattr.Attribution, error) {
		<-ctx.Done()
		return xpcattr.Attribution{}, ctx.Err()
	}
	start := time.Now()
	got := appendXPCCauses(nil, []process.Info{{PID: 3, Command: "OSLogService", CPUPct: 99}}, slow)
	if len(got) != 0 || time.Since(start) > xpcTimeout+2*time.Second {
		t.Errorf("got %+v after %v", got, time.Since(start))
	}
}
