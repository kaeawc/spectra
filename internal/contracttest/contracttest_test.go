package contracttest

import (
	"context"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

func TestRecorderRoundTripsThroughRunner(t *testing.T) {
	inner := proc.NewFake().
		OnExact("sysctl", []string{"-n", "hw.ncpu"}, proc.Response{Result: proc.Result{Stdout: []byte("8\n")}}).
		OnExact("bin", nil, proc.Response{Result: proc.Result{Stdout: []byte{0xff, 0x00}, ExitCode: 3}})
	rec := &Recorder{Inner: inner}
	ctx := context.Background()
	if _, err := rec.Run(ctx, proc.Cmd{Name: "sysctl", Args: []string{"-n", "hw.ncpu"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Run(ctx, proc.Cmd{Name: "bin"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls[1].StdoutB64; got != "/wA=" {
		t.Fatalf("binary stdout not base64: %q", got)
	}
	c := Case{Dir: t.TempDir()}
	prev := *Update
	*Update = true
	rec.Save(t, c)
	*Update = prev

	replay := c.Runner(t)
	res, err := replay.Run(ctx, proc.Cmd{Name: "sysctl", Args: []string{"-n", "hw.ncpu"}})
	if err != nil || string(res.Stdout) != "8\n" {
		t.Fatalf("replay sysctl = %q, %v", res.Stdout, err)
	}
	res, err = replay.Run(ctx, proc.Cmd{Name: "bin"})
	if err != nil || res.ExitCode != 3 || string(res.Stdout) != "\xff\x00" {
		t.Fatalf("replay bin = %+v, %v", res, err)
	}
}
