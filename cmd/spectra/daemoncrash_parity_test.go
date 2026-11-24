package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/crashquery"
	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemoncrash"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// crashClient serves opts from a real daemon socket, so the parity tests
// compare the bytes a client actually receives.
func crashClient(t *testing.T, opts daemoncrash.Options) *daemonclient.Client {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spcr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	daemoncrash.RegisterMethods(server, daemoncrash.NewService(opts))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
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

// crashRPC calls method and returns the result indented as the CLI's --json
// encoder writes it.
func crashRPC(t *testing.T, c *daemonclient.Client, method string, params any) string {
	t.Helper()
	var raw json.RawMessage
	if err := c.Call(context.Background(), method, params, &raw); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return indentCrashJSON(t, raw)
}

func indentCrashJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	return b.String() + "\n"
}

func sameCrashJSON(t *testing.T, what, cli, rpc string) {
	t.Helper()
	if cli != rpc {
		t.Fatalf("%s: daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", what, cli, rpc)
	}
}

// crashFixtureDir holds a mix of reports: two signatures (one repeated),
// a memory kill, a jetsam kill, a legacy report, frames.ips and a non-.ips
// file, some under Retired/.
func crashFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Retired"), 0o755); err != nil {
		t.Fatal(err)
	}
	frames, err := os.ReadFile("../../internal/crashreport/testdata/frames.ips")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"MyApp-1.ips":         ipsReport("MyApp", "2026-08-01 10:00:00.00 -0500", "EXC_BAD_ACCESS"),
		"MyApp-2.ips":         ipsReport("MyApp", "2026-08-05 10:00:00.00 -0500", "EXC_BAD_ACCESS"),
		"Retired/Other-1.ips": ipsReport("Other <&>", "2026-08-03 10:00:00.00 -0500", "EXC_CRASH"),
		"Hungry-1.ips":        memResourceIPS("Hungry", "2026-08-04 10:00:00.00 -0500"),
		"Jetted-1.ips":        `{"app_name":"Jetted","timestamp":"2026-08-02 10:00:00.00 -0500","bug_type":"298","name":"Jetted"}` + "\n{}",
		"old.ips":             "Process: Old [1]\n",
		"frames.ips":          string(frames),
		"notes.txt":           "ignored",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDaemonCrashSweepsMatchCLIJSON(t *testing.T) {
	dir := crashFixtureDir(t)
	c := crashClient(t, daemoncrash.Options{})
	two, zero := 2, 0
	for _, tc := range []struct {
		sub    string
		method string
		args   []string
		params daemoncrash.SweepParams
	}{
		{"list", daemoncrash.MethodList, nil, daemoncrash.SweepParams{Dir: dir}},
		{"list", daemoncrash.MethodList, []string{"--limit", "2"}, daemoncrash.SweepParams{Dir: dir, Limit: &two}},
		{"list", daemoncrash.MethodList, []string{"--limit", "0"}, daemoncrash.SweepParams{Dir: dir, Limit: &zero}},
		{"signatures", daemoncrash.MethodSignatures, nil, daemoncrash.SweepParams{Dir: dir}},
		{"signatures", daemoncrash.MethodSignatures, []string{"--limit", "2"}, daemoncrash.SweepParams{Dir: dir, Limit: &two}},
		{"oom", daemoncrash.MethodOOM, nil, daemoncrash.SweepParams{Dir: dir}},
		{"oom", daemoncrash.MethodOOM, []string{"--limit", "0"}, daemoncrash.SweepParams{Dir: dir, Limit: &zero}},
		{"oom", daemoncrash.MethodOOM, nil, daemoncrash.SweepParams{Dir: filepath.Join(dir, "Retired")}},
	} {
		args := append([]string{tc.sub, "--json", "--dir", tc.params.Dir}, tc.args...)
		var cli, errb bytes.Buffer
		if rc := runCrashWithIO(args, &cli, &errb, nil); rc != 0 {
			t.Fatalf("%v: exit %d: %s", args, rc, errb.String())
		}
		if tc.args == nil && tc.params.Dir == dir && len(cli.String()) < 40 {
			t.Fatalf("%v: fixture produced no rows: %s", args, cli.String())
		}
		sameCrashJSON(t, strings.Join(args, " "), cli.String(), crashRPC(t, c, tc.method, tc.params))
	}
}

func TestDaemonCrashInspectMatchesCLIJSON(t *testing.T) {
	frames, err := filepath.Abs("../../internal/crashreport/testdata/frames.ips")
	if err != nil {
		t.Fatal(err)
	}
	notExist := func(p string) (crashreport.BinaryFile, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	for _, k := range []hostos.Kind{hostos.Darwin, hostos.Linux} {
		// No binary or dSYM exists and mdfind finds none, so on macOS every
		// frame resolves to "none" without running atos; off macOS source
		// lines are unsupported.
		runner := proc.NewFake().On(proc.MatchPrefix("mdfind"), proc.Response{})
		src := crashreport.SourceLineOptions{OS: k, Runner: runner, FS: emptySourceFS{}, Home: "/nonexistent"}
		c := crashClient(t, daemoncrash.Options{OS: k, Open: notExist, SourceLines: src})
		for _, tc := range []struct {
			args   []string
			params daemoncrash.InspectParams
		}{
			{nil, daemoncrash.InspectParams{Path: frames}},
			{[]string{"--all"}, daemoncrash.InspectParams{Path: frames, All: true}},
			{[]string{"--source-lines"}, daemoncrash.InspectParams{Path: frames, SourceLines: true}},
		} {
			args := append(append([]string{"--json"}, tc.args...), frames)
			var cli, errb bytes.Buffer
			if rc := runCrashInspect(args, &cli, &errb, os.ReadFile, notExist, src); rc != 0 {
				t.Fatalf("%v: exit %d: %s", args, rc, errb.String())
			}
			if !strings.Contains(cli.String(), `"frame_details"`) {
				t.Fatalf("fixture produced no frame details: %s", cli.String())
			}
			sameCrashJSON(t, k.String()+" inspect "+strings.Join(tc.args, " "), cli.String(), crashRPC(t, c, daemoncrash.MethodInspect, tc.params))
		}
	}
}

// emptySourceFS is a filesystem with nothing on it.
type emptySourceFS struct{}

func (emptySourceFS) Open(p string) (crashreport.BinaryFile, error) {
	return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}
func (emptySourceFS) Stat(p string) (fs.FileInfo, error) {
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}
func (emptySourceFS) ReadDir(p string) ([]fs.DirEntry, error) {
	return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
}

// A report the CLI cannot load fails with the CLI's stderr text.
func TestDaemonCrashInspectErrorsMatchCLI(t *testing.T) {
	legacy := writeCrashTemp(t, "old.crash", "Process: Foo [1]\n")
	missing := filepath.Join(t.TempDir(), "missing.ips")
	garbage := writeCrashTemp(t, "bad.ips", "{not json")
	c := crashClient(t, daemoncrash.Options{})
	for _, p := range []string{legacy, missing, garbage} {
		var cli, errb bytes.Buffer
		if rc := runCrashInspect([]string{"--json", p}, &cli, &errb, os.ReadFile, crashreport.OpenOS, crashreport.SourceLineOptions{}); rc != 1 {
			t.Fatalf("%s: exit %d", p, rc)
		}
		err := c.Call(context.Background(), daemoncrash.MethodInspect, daemoncrash.InspectParams{Path: p}, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError || rpcErr.Message+"\n" != errb.String() {
			t.Fatalf("%s: rpc error %v, CLI stderr %q", p, err, errb.String())
		}
	}
}

func TestDaemonSymbolicateMatchesCLIJSON(t *testing.T) {
	runner := fixedSymRunner("main (in App) (main.c:10)\n\nhelper (in App) + 4 <&>\n")
	c := crashClient(t, daemoncrash.Options{OS: hostos.Darwin, Symbolicate: runner})
	for _, tc := range []struct {
		args   []string
		params daemoncrash.SymbolicateParams
	}{
		{[]string{"-o", "/App.app/Contents/MacOS/App", "-l", "0x1000", "0x1010", "0x1020", "0x1030"},
			daemoncrash.SymbolicateParams{Binary: "/App.app/Contents/MacOS/App", LoadAddress: "0x1000", Addresses: []string{"0x1010", "0x1020", "0x1030"}}},
		{[]string{"-o", "/App.dSYM", "-l", "0x1000", "--arch", "arm64", "0x1010"},
			daemoncrash.SymbolicateParams{Binary: "/App.dSYM", LoadAddress: "0x1000", Arch: "arm64", Addresses: []string{"0x1010"}}},
	} {
		var cli, errb bytes.Buffer
		if rc := runSymbolicateWithIO(append([]string{"--json"}, tc.args...), nil, &cli, &errb, runner); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		sameCrashJSON(t, "symbolicate "+strings.Join(tc.args, " "), cli.String(), crashRPC(t, c, daemoncrash.MethodSymbolicate, tc.params))
	}
	// An atos failure is the CLI's error text.
	failing := func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("atos exited 1: no such file")
	}
	fc := crashClient(t, daemoncrash.Options{OS: hostos.Darwin, Symbolicate: failing})
	var cli, errb bytes.Buffer
	if rc := runSymbolicateWithIO([]string{"--json", "-o", "/App", "-l", "0x1000", "0x1010"}, nil, &cli, &errb, failing); rc != 1 {
		t.Fatalf("failing exit %d", rc)
	}
	err := fc.Call(context.Background(), daemoncrash.MethodSymbolicate, daemoncrash.SymbolicateParams{Binary: "/App", LoadAddress: "0x1000", Addresses: []string{"0x1010"}}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

// Off macOS the CLI answers symbolicate with the structured unsupported
// object; the daemon sends the same object as error data.
func TestDaemonSymbolicateMatchesCLIUnsupported(t *testing.T) {
	if hostos.Current() == hostos.Darwin {
		t.Skip("the unsupported answer is only given off macOS")
	}
	c := crashClient(t, daemoncrash.Options{})
	var cli, errb bytes.Buffer
	code, gated := gateDarwinOnly("symbolicate", []string{"--json"}, &cli, &errb)
	if !gated || code != 1 {
		t.Fatal("symbolicate not gated")
	}
	err := c.Call(context.Background(), daemoncrash.MethodSymbolicate, daemoncrash.SymbolicateParams{Binary: "/App", LoadAddress: "0x1000", Addresses: []string{"0x1010"}}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
		t.Fatalf("err = %v", err)
	}
	sameCrashJSON(t, "symbolicate unsupported", cli.String(), indentCrashJSON(t, rpcErr.Data))
}

// Off macOS with no dir the CLI fails pointing at --dir; the daemon fails
// pointing at dir, with the unsupported object as data.
func TestDaemonCrashSweepWithoutDirOffDarwin(t *testing.T) {
	c := crashClient(t, daemoncrash.Options{OS: hostos.Linux})
	for _, method := range []string{daemoncrash.MethodList, daemoncrash.MethodSignatures, daemoncrash.MethodOOM} {
		err := c.Call(context.Background(), method, nil, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError || !strings.Contains(rpcErr.Message, "pass dir") {
			t.Fatalf("%s: err = %v", method, err)
		}
		var r hostos.CommandReport
		if err := json.Unmarshal(rpcErr.Data, &r); err != nil {
			t.Fatal(err)
		}
		if r.Error != "unsupported_platform" || r.Feature != crashquery.ReportsFeature || r.OS != "linux" || !strings.HasPrefix(r.Command, "crash ") {
			t.Fatalf("%s: report = %+v", method, r)
		}
	}
}
