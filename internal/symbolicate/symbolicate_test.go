package symbolicate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func runnerReturning(out string, err error) Runner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestSymbolicateParsesFrames(t *testing.T) {
	// One ObjC frame with file:line, one C symbol with an offset, one unresolved
	// address echoed back by atos.
	atos := strings.Join([]string{
		"-[AppDelegate applicationDidFinishLaunching:] (in MyApp) (AppDelegate.m:42)",
		"main (in MyApp) + 128",
		"0x000000010a4b31f4",
	}, "\n")
	req := Request{Binary: "/MyApp", LoadAddress: "0x10a400000", Addresses: []string{"0x10a4b31a0", "0x10a4b3200", "0x000000010a4b31f4"}}
	res, err := Symbolicate(context.Background(), req, runnerReturning(atos, nil))
	if err != nil {
		t.Fatalf("Symbolicate: %v", err)
	}
	if len(res.Frames) != 3 {
		t.Fatalf("frames = %d", len(res.Frames))
	}

	f0 := res.Frames[0]
	if !f0.Resolved || f0.Symbol != "-[AppDelegate applicationDidFinishLaunching:]" || f0.Location != "AppDelegate.m:42" {
		t.Errorf("frame0 = %+v", f0)
	}

	f1 := res.Frames[1]
	if !f1.Resolved || f1.Symbol != "main + 128" || f1.Location != "" {
		t.Errorf("frame1 = %+v", f1)
	}

	f2 := res.Frames[2]
	if f2.Resolved {
		t.Errorf("frame2 should be unresolved (atos echoed the address): %+v", f2)
	}
}

func TestSymbolicatePassesArch(t *testing.T) {
	var gotArgs []string
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("sym\n"), nil
	}
	_, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "0x1", Arch: "arm64", Addresses: []string{"0x2"}}, runner)
	if err != nil {
		t.Fatalf("Symbolicate: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-arch arm64") || !strings.Contains(joined, "-o /b") || !strings.Contains(joined, "-l 0x1") {
		t.Errorf("atos args missing expected flags: %v", gotArgs)
	}
}

func TestSymbolicateValidation(t *testing.T) {
	cases := []Request{
		{LoadAddress: "0x1", Addresses: []string{"0x2"}}, // no binary
		{Binary: "/b", Addresses: []string{"0x2"}},       // no load address
		{Binary: "/b", LoadAddress: "0x1"},               // no addresses
	}
	for i, req := range cases {
		if _, err := Symbolicate(context.Background(), req, runnerReturning("", nil)); err == nil {
			t.Errorf("case %d: expected a validation error", i)
		}
	}
}

func TestSymbolicateRejectsBadLoadAddress(t *testing.T) {
	ran := false
	runner := func(context.Context, string, ...string) ([]byte, error) { ran = true; return nil, nil }
	_, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "not-an-address", Addresses: []string{"0x2"}}, runner)
	if err == nil {
		t.Error("expected a validation error for a non-hex load address")
	}
	if ran {
		t.Error("atos must not run when the load address is invalid")
	}
	// A bare hex value (no 0x) is still valid.
	if _, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "10a400000", Addresses: []string{"0x2"}}, runnerReturning("s\n", nil)); err != nil {
		t.Errorf("bare hex load address should be accepted: %v", err)
	}
}

func TestSymbolicatePreservesBlankLines(t *testing.T) {
	// atos returned a blank line for the middle address; the third address must
	// still pair with symbol-C, not shift up.
	res, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "0x1", Addresses: []string{"0xa", "0xb", "0xc"}}, runnerReturning("symbol-A\n\nsymbol-C\n", nil))
	if err != nil {
		t.Fatalf("Symbolicate: %v", err)
	}
	if res.Frames[0].Symbol != "symbol-A" {
		t.Errorf("frame0 = %+v", res.Frames[0])
	}
	if res.Frames[1].Resolved {
		t.Errorf("frame1 (blank line) should be unresolved: %+v", res.Frames[1])
	}
	if res.Frames[2].Symbol != "symbol-C" {
		t.Errorf("frame2 = %+v", res.Frames[2])
	}
}

func TestSymbolicateRunnerError(t *testing.T) {
	_, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "0x1", Addresses: []string{"0x2"}}, runnerReturning("", errors.New("atos: no such file")))
	if err == nil {
		t.Error("expected an error when atos fails")
	}
}

func TestSymbolicateFewerLinesThanAddresses(t *testing.T) {
	// atos returned only one line for two addresses; the second frame stays
	// unresolved rather than panicking.
	res, err := Symbolicate(context.Background(), Request{Binary: "/b", LoadAddress: "0x1", Addresses: []string{"0xa", "0xb"}}, runnerReturning("foo (in B) (F.c:1)\n", nil))
	if err != nil {
		t.Fatalf("Symbolicate: %v", err)
	}
	if !res.Frames[0].Resolved || res.Frames[1].Resolved {
		t.Errorf("frames = %+v", res.Frames)
	}
}
