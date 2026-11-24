package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemonprofile"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

// profileClient serves opts from a real daemon socket, so the parity tests
// compare the bytes a client actually receives.
func profileClient(t *testing.T, opts daemonprofile.Options) *daemonclient.Client {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "sppf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	svc := daemonprofile.NewService(opts)
	daemonprofile.RegisterMethods(server, svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; svc.Close(5 * time.Second) })
	for i := 0; i < 200; i++ {
		if c, err := daemonclient.Dial(ctx, paths.Socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
	return nil
}

// profileRun starts a job, polls it to the end and returns its result
// indented as the CLI's --json encoder writes it, or the result error.
func profileRun(t *testing.T, c *daemonclient.Client, params daemonprofile.StartParams) (string, error) {
	t.Helper()
	ctx := context.Background()
	var rec daemonprofile.Job
	if err := c.Call(ctx, daemonprofile.MethodStart, params, &rec); err != nil {
		t.Fatalf("start %+v: %v", params, err)
	}
	for i := 0; rec.Status == daemonprofile.StatusWorking; i++ {
		if i > 500 {
			t.Fatalf("job %s did not finish", rec.ID)
		}
		time.Sleep(10 * time.Millisecond)
		if err := c.Call(ctx, daemonprofile.MethodStatus, daemonprofile.IDParams{ID: rec.ID}, &rec); err != nil {
			t.Fatal(err)
		}
	}
	var raw json.RawMessage
	if err := c.Call(ctx, daemonprofile.MethodResult, daemonprofile.IDParams{ID: rec.ID}, &raw); err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	return b.String() + "\n", nil
}

func sameProfileJSON(t *testing.T, what, cli, rpc string) {
	t.Helper()
	if cli != rpc {
		t.Fatalf("%s: daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", what, cli, rpc)
	}
}

func TestDaemonSampleMatchesCLIJSON(t *testing.T) {
	for _, fixture := range []string{"testdata/sample_report.txt", "testdata/sample_report_images.txt"} {
		deps := sampleDepsFrom(t, fixture)
		c := profileClient(t, daemonprofile.Options{OS: hostos.Darwin, SampleCapture: deps.capture})
		for _, tc := range []struct {
			args   []string
			params daemonprofile.StartParams
		}{
			{nil, daemonprofile.StartParams{Kind: "sample", PID: 412}},
			{[]string{"--duration", "3", "--interval", "5"}, daemonprofile.StartParams{Kind: "sample", PID: 412, DurationMS: 3000, IntervalMS: 5}},
		} {
			var cli, errb bytes.Buffer
			if rc := runSampleWithIO(append(append([]string{"--json", "--no-cache"}, tc.args...), "412"), &cli, &errb, deps); rc != 0 {
				t.Fatalf("exit %d: %s", rc, errb.String())
			}
			rpc, err := profileRun(t, c, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			sameProfileJSON(t, fixture+" "+strings.Join(tc.args, " "), cli.String(), rpc)
		}
	}
	// A failed capture is the CLI's stderr text.
	failing := sampleDeps{capture: func(context.Context, process.SampleOptions) (process.SampleResult, error) {
		return process.SampleResult{}, errors.New("sample: no such process <&>")
	}}
	c := profileClient(t, daemonprofile.Options{OS: hostos.Darwin, SampleCapture: failing.capture})
	var cli, errb bytes.Buffer
	if rc := runSampleWithIO([]string{"--json", "--no-cache", "9"}, &cli, &errb, failing); rc != 1 {
		t.Fatalf("failing exit %d", rc)
	}
	_, err := profileRun(t, c, daemonprofile.StartParams{Kind: "sample", PID: 9})
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

func TestDaemonSpindumpMatchesCLIJSON(t *testing.T) {
	input := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(input, []byte(cliReport), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(cliReport), nil
	}
	deps := spindumpDeps{run: runner, readFile: os.ReadFile}
	c := profileClient(t, daemonprofile.Options{OS: hostos.Darwin, SpindumpRun: runner})
	for _, tc := range []struct {
		args   []string
		params daemonprofile.StartParams
	}{
		{[]string{"--input", input}, daemonprofile.StartParams{Kind: "spindump", Input: input}},
		{[]string{"4012"}, daemonprofile.StartParams{Kind: "spindump", PID: 4012}},
		{[]string{"--duration", "5", "4012"}, daemonprofile.StartParams{Kind: "spindump", PID: 4012, DurationMS: 5000}},
	} {
		var cli, errb bytes.Buffer
		if rc := runSpindumpWithIO(append([]string{"--json"}, tc.args...), &cli, &errb, deps); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		cliArgs := strings.Join(gotArgs, " ")
		rpc, err := profileRun(t, c, tc.params)
		if err != nil {
			t.Fatal(err)
		}
		sameProfileJSON(t, "spindump "+strings.Join(tc.args, " "), cli.String(), rpc)
		if got := strings.Join(gotArgs, " "); got != cliArgs {
			t.Fatalf("daemon ran %q, CLI ran %q", got, cliArgs)
		}
	}
	// Without root the daemon fails as the CLI does without --sudo.
	rootRunner := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("spindump must be run as root\n"), errors.New("exit status 1")
	}
	rc := profileClient(t, daemonprofile.Options{OS: hostos.Darwin, SpindumpRun: rootRunner})
	var cli, errb bytes.Buffer
	if code := runSpindumpWithIO([]string{"--json", "4012"}, &cli, &errb, spindumpDeps{run: rootRunner}); code != 1 {
		t.Fatalf("root exit %d", code)
	}
	_, err := profileRun(t, rc, daemonprofile.StartParams{Kind: "spindump", PID: 4012})
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

func TestDaemonFlamegraphMatchesCLIJSON(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(input, []byte(fgSample), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := func(context.Context, int, int) (string, error) { return fgSample, nil }
	deps := flamegraphDeps{capture: capture, readFile: os.ReadFile, writeFile: os.WriteFile}
	c := profileClient(t, daemonprofile.Options{OS: hostos.Darwin, FlamegraphCapture: capture})
	for i, tc := range []struct {
		args   []string
		params daemonprofile.StartParams
	}{
		{[]string{"--input", input}, daemonprofile.StartParams{Kind: "flamegraph", Input: input}},
		{[]string{"4012"}, daemonprofile.StartParams{Kind: "flamegraph", PID: 4012}},
		{[]string{"--duration", "4", "4012"}, daemonprofile.StartParams{Kind: "flamegraph", PID: 4012, DurationMS: 4000}},
	} {
		// Both write to the same absolute path, so the JSON's svg matches.
		out := filepath.Join(dir, "out", strings.Repeat("g", i+1)+".svg")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		var cli, errb bytes.Buffer
		if rc := runFlamegraphWithIO(append([]string{"--json", "--out", out}, tc.args...), &cli, &errb, deps); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		cliSVG, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(out); err != nil {
			t.Fatal(err)
		}
		tc.params.Out = out
		rpc, err := profileRun(t, c, tc.params)
		if err != nil {
			t.Fatal(err)
		}
		sameProfileJSON(t, "flamegraph "+strings.Join(tc.args, " "), cli.String(), rpc)
		rpcSVG, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(cliSVG, rpcSVG) {
			t.Fatalf("flamegraph %v: daemon SVG differs from CLI SVG", tc.args)
		}
	}
	// An empty sample fails with the CLI's text.
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("nothing"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cli, errb bytes.Buffer
	if rc := runFlamegraphWithIO([]string{"--json", "--input", empty, "--out", filepath.Join(dir, "e.svg")}, &cli, &errb, deps); rc != 1 {
		t.Fatalf("empty exit %d", rc)
	}
	_, err := profileRun(t, c, daemonprofile.StartParams{Kind: "flamegraph", Input: empty, Out: filepath.Join(dir, "e.svg")})
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

// Off macOS the CLI answers sample, spindump and flamegraph with the
// structured unsupported object; profile.start sends the same object as
// error data, without starting a job.
func TestDaemonProfileMatchesCLIUnsupported(t *testing.T) {
	if hostos.Current() == hostos.Darwin {
		t.Skip("the unsupported answer is only given off macOS")
	}
	c := profileClient(t, daemonprofile.Options{})
	for kind, params := range map[string]daemonprofile.StartParams{
		"sample":     {Kind: "sample", PID: 1},
		"spindump":   {Kind: "spindump", PID: 1},
		"flamegraph": {Kind: "flamegraph", PID: 1, Out: "/tmp/g.svg"},
	} {
		var cli, errb bytes.Buffer
		code, gated := gateDarwinOnly(kind, []string{"--json"}, &cli, &errb)
		if !gated || code != 1 {
			t.Fatalf("%s not gated", kind)
		}
		err := c.Call(context.Background(), daemonprofile.MethodStart, params, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
			t.Fatalf("%s: err = %v", kind, err)
		}
		// The CLI prints this object without HTML escaping and the JSON-RPC
		// encoder escapes the hint's <pid>, so compare the decoded values.
		var cliRep, rpcRep hostos.CommandReport
		if err := json.Unmarshal(cli.Bytes(), &cliRep); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(rpcErr.Data, &rpcRep); err != nil {
			t.Fatal(err)
		}
		if cliRep != rpcRep || cliRep.Command != kind {
			t.Fatalf("%s: daemon data %+v, CLI %+v", kind, rpcRep, cliRep)
		}
	}
}
