package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/xpcattr"
)

func TestAppendXPCCauses(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	orig := xpcAttribute
	defer func() { xpcAttribute = orig }()

	busy := []process.Info{{PID: 17874, Command: "OSLogService", CPUPct: 95}}
	xpcAttribute = func(context.Context, int, []process.Info) (xpcattr.Attribution, error) {
		return xpcattr.Attribution{PID: 17874, Clients: []xpcattr.Client{{
			PID: 17649, Name: "Runner", Launcher: &xpcattr.ProcRef{PID: 17491, Name: "xcodebuild"},
		}}}, nil
	}
	got := appendXPCCauses(nil, busy)
	want := "OSLogService (17874) busy, driven by Runner (17649), launched by xcodebuild (17491)"
	if len(got) != 1 || got[0].Detail != want {
		t.Fatalf("got %+v", got)
	}

	if got := appendXPCCauses(nil, []process.Info{{PID: 1, CPUPct: 10}}); len(got) != 0 {
		t.Errorf("idle process attributed: %+v", got)
	}

	xpcAttribute = func(context.Context, int, []process.Info) (xpcattr.Attribution, error) {
		return xpcattr.Attribution{}, errors.New("sample failed")
	}
	if got := appendXPCCauses(nil, busy); len(got) != 0 {
		t.Errorf("error should add nothing: %+v", got)
	}
}

func TestAppendXPCCausesSkippedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	if got := appendXPCCauses(nil, []process.Info{{PID: 1, CPUPct: 99}}); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
