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
	"github.com/kaeawc/spectra/internal/daemonprocinfo"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/services"
)

// procInfoClient serves opts from a real daemon socket, so the parity tests
// compare the bytes a client actually receives.
func procInfoClient(t *testing.T, opts daemonprocinfo.Options) *daemonclient.Client {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "sppi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	daemonprocinfo.RegisterMethods(server, daemonprocinfo.NewService(opts))
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

// procInfoRPC calls method and returns the result indented as the CLI's
// --json encoder writes it.
func procInfoRPC(t *testing.T, c *daemonclient.Client, method string, params any) string {
	t.Helper()
	var raw json.RawMessage
	if err := c.Call(context.Background(), method, params, &raw); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return indentRaw(t, raw)
}

func indentRaw(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	return b.String() + "\n"
}

func procInfoSame(t *testing.T, what, cli, rpc string) {
	t.Helper()
	if cli != rpc {
		t.Fatalf("%s: daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", what, cli, rpc)
	}
}

func TestDaemonLineageMatchesCLIJSON(t *testing.T) {
	c := procInfoClient(t, daemonprocinfo.Options{Processes: lineageFakeProcs()})
	for _, pid := range []string{"1", "10", "20"} {
		var cli, errb bytes.Buffer
		if rc := runLineageWithIO([]string{"--json", pid}, &cli, &errb, lineageFakeProcs()); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		var n int
		_ = json.Unmarshal([]byte(pid), &n)
		procInfoSame(t, "lineage "+pid, cli.String(), procInfoRPC(t, c, daemonprocinfo.MethodLineage, daemonprocinfo.LineageParams{PID: n}))
	}
	var cli, errb bytes.Buffer
	if rc := runLineageWithIO([]string{"--json", "999"}, &cli, &errb, lineageFakeProcs()); rc != 1 {
		t.Fatalf("missing pid exit %d", rc)
	}
	err := c.Call(context.Background(), daemonprocinfo.MethodLineage, daemonprocinfo.LineageParams{PID: 999}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

func ownerFixture() (owner.FindFunc, func(int) owner.UnderFunc, *int) {
	found := []owner.Holder{{
		PID: 20, User: "jason", Command: "app <&>", Hold: owner.HoldListen, Detail: "tcp *:8080",
		Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Chain:   []owner.Link{{PID: 10, Command: "sshd"}, {PID: 1, Command: "launchd"}},
	}}
	find := func(_ context.Context, tg owner.Target) (owner.Result, error) {
		if tg.Kind == owner.KindPID {
			return owner.Result{Target: tg}, nil
		}
		return owner.Result{Target: tg, Holders: found, Incomplete: true, Hint: "not running as root"}, nil
	}
	excluded := new(int)
	under := func(exclude int) owner.UnderFunc {
		*excluded = exclude
		return func(_ context.Context, dirs []string) (owner.UnderResult, error) {
			return owner.UnderResult{Holders: []owner.Holder{{PID: 30, Command: "vim", Hold: owner.HoldCwd, Cwd: dirs[0] + "/src"}}}, nil
		}
	}
	return find, under, excluded
}

func TestDaemonOwnerMatchesCLIJSON(t *testing.T) {
	find, under, excluded := ownerFixture()
	c := procInfoClient(t, daemonprocinfo.Options{OwnerFind: find, OwnerUnder: under})
	for _, tc := range []struct {
		args   []string
		params daemonprocinfo.OwnerParams
	}{
		{[]string{":8080"}, daemonprocinfo.OwnerParams{Target: ":8080"}},
		{[]string{"udp:53"}, daemonprocinfo.OwnerParams{Target: "udp:53"}},
		{[]string{"/var/log/system.log"}, daemonprocinfo.OwnerParams{Target: "/var/log/system.log"}},
		{[]string{"4242"}, daemonprocinfo.OwnerParams{Target: "4242", TimeoutMS: 5000}},
		{[]string{"--under", "/work/a", "/work/b/"}, daemonprocinfo.OwnerParams{Under: true, Dirs: []string{"/work/a", "/work/b/"}, ExcludePID: 77}},
	} {
		var cli, errb bytes.Buffer
		runOwnerWithIO(append([]string{"--json"}, tc.args...), &cli, &errb, "/work", find, under(0))
		procInfoSame(t, "owner "+strings.Join(tc.args, " "), cli.String(), procInfoRPC(t, c, daemonprocinfo.MethodOwner, tc.params))
	}
	if *excluded != 77 {
		t.Fatalf("under exclude_pid = %d, want 77", *excluded)
	}
}

const servicesPlist = `<?xml version="1.0"?><plist version="1.0"><dict>
<key>Label</key><string>com.example.agent</string>
<key>Program</key><string>/usr/local/bin/agent</string>
<key>RunAtLoad</key><true/>
<key>StartInterval</key><integer>3600</integer>
</dict></plist>`

func servicesFixture(t *testing.T) services.Options {
	dir := t.TempDir()
	for _, name := range []string{"com.example.agent", "com.example.idle"} {
		body := strings.Replace(servicesPlist, "com.example.agent", name, 1)
		if err := os.WriteFile(filepath.Join(dir, name+".plist"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return services.Options{
		PlistDirs: []string{dir},
		Now:       func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
		Runner: services.RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "/bin/launchctl" && len(args) == 1 && args[0] == "list" {
				return []byte("PID\tStatus\tLabel\n123\t0\tcom.example.agent\n-\t0\tcom.example.idle\n-\t0\tcom.example.extra\n"), nil
			}
			return nil, os.ErrNotExist
		}),
	}
}

func TestDaemonServicesMatchesCLIJSON(t *testing.T) {
	base := servicesFixture(t)
	c := procInfoClient(t, daemonprocinfo.Options{OS: hostos.Darwin, Services: base})
	for _, tc := range []struct {
		args   []string
		params daemonprocinfo.ServicesParams
	}{
		{nil, daemonprocinfo.ServicesParams{}},
		{[]string{"--domain", "all"}, daemonprocinfo.ServicesParams{Domain: "all"}},
		{[]string{"--running"}, daemonprocinfo.ServicesParams{Running: true}},
		{[]string{"--label", "IDLE", "--on-demand"}, daemonprocinfo.ServicesParams{Label: "IDLE", OnDemand: true}},
	} {
		var cli, errb bytes.Buffer
		if rc := runServicesWithIO(append([]string{"--json"}, tc.args...), &cli, &errb, base); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		if tc.args == nil && !strings.Contains(cli.String(), "com.example.idle") {
			t.Fatalf("fixture produced no jobs: %s", cli.String())
		}
		procInfoSame(t, "services "+strings.Join(tc.args, " "), cli.String(), procInfoRPC(t, c, daemonprocinfo.MethodServices, tc.params))
	}
}

func logsFixture() logquery.Query {
	ndjson := `{"timestamp":"2026-01-01 11:45:00.000000-0800","processID":42,"processImagePath":"/usr/libexec/backupd","messageType":"Error","eventMessage":"backup <failed> & retried","subsystem":"com.apple.backup"}
{"timestamp":"2026-01-01 11:46:00.000000-0800","processID":42,"processImagePath":"/usr/libexec/backupd","messageType":"Default","eventMessage":"ok","subsystem":"com.apple.backup"}
`
	return logquery.Query{
		OS:  hostos.Darwin,
		Now: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
		Runner: logquery.RunnerFunc(func(context.Context, string, ...string) ([]byte, []byte, error) {
			return []byte(ndjson), nil, nil
		}),
	}
}

func TestDaemonLogsMatchesCLIJSON(t *testing.T) {
	base := logsFixture()
	c := procInfoClient(t, daemonprocinfo.Options{OS: hostos.Darwin, Logs: base})
	for _, tc := range []struct {
		args   []string
		params daemonprocinfo.LogsParams
	}{
		{[]string{"--process", "backupd", "--last", "10m"}, daemonprocinfo.LogsParams{Process: "backupd", LastMS: (10 * time.Minute).Milliseconds()}},
		{nil, daemonprocinfo.LogsParams{}},
		{[]string{"--grep", "fail", "--level", "Error", "--top", "1"}, daemonprocinfo.LogsParams{Grep: "fail", Level: "Error", Top: 1}},
	} {
		var cli, errb bytes.Buffer
		if rc := runLogsWithIO(append([]string{"--json"}, tc.args...), &cli, &errb, base); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		if !strings.Contains(cli.String(), "backup \\u003cfailed\\u003e") {
			t.Fatalf("fixture produced no entries: %s", cli.String())
		}
		procInfoSame(t, "logs "+strings.Join(tc.args, " "), cli.String(), procInfoRPC(t, c, daemonprocinfo.MethodLogs, tc.params))
	}
	// A window the CLI rejects as a usage error is invalid params here.
	var cli, errb bytes.Buffer
	if rc := runLogsWithIO([]string{"--json", "--last", "48h"}, &cli, &errb, base); rc != 2 {
		t.Fatalf("long window exit %d", rc)
	}
	err := c.Call(context.Background(), daemonprocinfo.MethodLogs, daemonprocinfo.LogsParams{LastMS: (48 * time.Hour).Milliseconds()}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}

// Off macOS the CLI answers services and logs with the structured
// unsupported object; the daemon sends the same object as error data.
func TestDaemonDarwinOnlyMatchesCLIUnsupported(t *testing.T) {
	if hostos.Current() == hostos.Darwin {
		t.Skip("the unsupported answer is only given off macOS")
	}
	c := procInfoClient(t, daemonprocinfo.Options{})
	for cmd, method := range map[string]string{"services": daemonprocinfo.MethodServices, "logs": daemonprocinfo.MethodLogs} {
		var cli, errb bytes.Buffer
		code, gated := gateDarwinOnly(cmd, []string{"--json"}, &cli, &errb)
		if !gated || code != 1 {
			t.Fatalf("%s not gated", cmd)
		}
		err := c.Call(context.Background(), method, nil, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
			t.Fatalf("%s: err = %v", cmd, err)
		}
		procInfoSame(t, cmd+" unsupported", cli.String(), indentRaw(t, rpcErr.Data))
	}
}
