package netstate

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

// nettopDeltaCapture is real output of `nettop -P -x -d -L 2 -s 1 -J
// bytes_in,bytes_out -t external` on macOS 26 while curl downloaded a file,
// trimmed to a few processes. The first block is the sum over each
// process's currently open flows; the second is what moved during the 1s
// delay. Names are truncated to 15 characters and may contain spaces.
const nettopDeltaCapture = `,bytes_in,bytes_out,
apsd.376,5694,23492,
mDNSResponder.519,52868346,11865217,
Runner.Listener.18550,15965,58139,
Google Chrome H.69559,15011,15724,
ssh.8112,10190,4406,
curl.8183,2776036,519,
,bytes_in,bytes_out,
apsd.376,0,0,
mDNSResponder.519,3023,576,
Runner.Listener.18550,246,2585,
Google Chrome H.69559,0,0,
ssh.8112,124,96,
curl.8183,233783,0,
`

func TestCollectProcessBytesUsesDeltaBlock(t *testing.T) {
	args := ProcessBytesArgs(3)
	f := proc.NewFake().OnExact("nettop", args, proc.Response{Result: proc.Result{Stdout: []byte(nettopDeltaCapture)}})
	rows, err := CollectProcessBytes(context.Background(), f, 2500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	if r := rows[5]; r.PID != 8183 || r.Command != "curl" || r.BytesIn != 233783 || r.BytesOut != 0 {
		t.Errorf("curl row = %+v, want the second (delta) block", r)
	}
	if r := rows[3]; r.PID != 69559 || r.Command != "Google Chrome H" || r.BytesIn != 0 {
		t.Errorf("row with space = %+v", r)
	}
	if !hasArg(args, "-d") || !hasArg(args, "3") || !hasArg(args, "external") {
		t.Errorf("args = %v, want delta mode with a 3s delay", args)
	}
}

func TestCollectProcessBytesRoundsUpToWholeSeconds(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "1", 300 * time.Millisecond: "1", time.Second: "1", 1001 * time.Millisecond: "2"} {
		f := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Result: proc.Result{Stdout: []byte(nettopDeltaCapture)}})
		if _, err := CollectProcessBytes(context.Background(), f, d); err != nil {
			t.Fatal(err)
		}
		args := f.Calls()[0].Args
		if got := args[6]; args[5] != "-s" || got != want {
			t.Errorf("interval %v: -s %s, want %s", d, got, want)
		}
	}
}

func TestCollectProcessBytesEmptyIntervalIsNotTheTotals(t *testing.T) {
	out := ",bytes_in,bytes_out,\ncurl.1,500,5,\n,bytes_in,bytes_out,\n"
	f := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Result: proc.Result{Stdout: []byte(out)}})
	rows, err := CollectProcessBytes(context.Background(), f, time.Second)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v err = %v, want an empty interval", rows, err)
	}
}

func TestCollectProcessBytesErrors(t *testing.T) {
	short := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Result: proc.Result{Stdout: []byte(",bytes_in,bytes_out,\ncurl.1,5,5,\n")}})
	if _, err := CollectProcessBytes(context.Background(), short, time.Second); err == nil || !strings.Contains(err.Error(), "2 sample blocks") {
		t.Errorf("one block: err = %v", err)
	}
	failed := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Result: proc.Result{ExitCode: 64, Stderr: []byte("usage")}})
	if _, err := CollectProcessBytes(context.Background(), failed, time.Second); err == nil || !strings.Contains(err.Error(), "nettop") {
		t.Errorf("non-zero exit: err = %v", err)
	}
	missing := proc.NewFake().On(proc.MatchPrefix("nettop"), proc.Response{Err: &exec.Error{Name: "nettop", Err: exec.ErrNotFound}})
	if _, err := CollectProcessBytes(context.Background(), missing, time.Second); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("missing: err = %v, want exec.ErrNotFound", err)
	}
}
