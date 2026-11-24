package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemoninventory"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/snapshotquery"
	"github.com/kaeawc/spectra/internal/timeline"
	"github.com/kaeawc/spectra/internal/toolchain"
	"github.com/kaeawc/spectra/internal/updates"
)

// inventoryClient serves opts from a real daemon socket, so the parity tests
// compare the bytes a client actually receives.
func inventoryClient(t *testing.T, opts daemoninventory.Options) *daemonclient.Client {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spinv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	daemoninventory.RegisterMethods(server, daemoninventory.NewService(opts))
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

// inventoryRPC calls method and returns the result indented as the CLI's
// --json encoder writes it.
func inventoryRPC(t *testing.T, c *daemonclient.Client, method string, params any) string {
	t.Helper()
	var raw json.RawMessage
	if err := c.Call(context.Background(), method, params, &raw); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	return b.String() + "\n"
}

func assertSameJSON(t *testing.T, what, cli, rpc string) {
	t.Helper()
	if cli != rpc {
		t.Fatalf("%s: daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", what, cli, rpc)
	}
}

func toolchainFixture(t *testing.T) toolchain.CollectOptions {
	home := t.TempDir()
	for _, d := range []string{
		filepath.Join(home, ".nvm", "versions", "node", "v22.1.0"),
		filepath.Join(home, ".rustup", "toolchains", "stable-aarch64-apple-darwin"),
		filepath.Join(home, ".sdkman", "candidates", "java", "21.0.6-tem"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	release := "JAVA_VERSION=\"21.0.6\"\nIMPLEMENTOR=\"Eclipse Adoptium\"\nJAVA_RUNTIME_VERSION=\"21.0.6+7-LTS\"\n"
	if err := os.WriteFile(filepath.Join(home, ".sdkman", "candidates", "java", "21.0.6-tem", "release"), []byte(release), 0o644); err != nil {
		t.Fatal(err)
	}
	return toolchain.CollectOptions{
		Home:          home,
		BrewCellars:   []string{filepath.Join(home, "cellar")},
		SystemJVMRoot: filepath.Join(home, "sys-jvm"),
		UserJVMRoot:   filepath.Join(home, "user-jvm"),
		EnvLookup:     func(string) (string, bool) { return "", false },
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			switch {
			case name == "brew" && len(args) > 0 && args[0] == "info":
				return []byte(`{"formulae":[{"name":"jq","full_name":"jq","installed":[{"version":"1.7.1"}]}]}`), nil
			case name == "brew" && len(args) > 0 && args[0] == "list":
				return []byte("firefox 130.0\n"), nil
			case name == "brew" && len(args) > 0 && args[0] == "tap":
				return []byte("homebrew/core\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

func TestDaemonToolchainMatchesCLIJSON(t *testing.T) {
	opts := toolchainFixture(t)
	var cli, errb bytes.Buffer
	if rc := runToolchainWithIO([]string{"--json"}, &cli, &errb, opts); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	c := inventoryClient(t, daemoninventory.Options{Toolchain: func(context.Context) toolchain.CollectOptions { return opts }})
	assertSameJSON(t, "toolchain", cli.String(), inventoryRPC(t, c, daemoninventory.MethodToolchain, nil))
}

func timelineFixture() []timeline.Source {
	return []timeline.Source{
		procSource(),
		timeline.LogSource{Run: func(context.Context, logquery.Query) (logquery.Result, error) {
			return logquery.Result{Entries: []logquery.LogEntry{
				{Timestamp: time.Date(2026, 1, 1, 11, 45, 0, 0, time.UTC), Process: "kernel", EventMessage: "oops <&>", MessageType: "Error"},
			}}, nil
		}},
		timeline.InstallSource{Fetch: func(context.Context) ([]updates.InstallEntry, error) {
			return []updates.InstallEntry{{Name: "App", Version: "2", InstallDate: time.Date(2026, 1, 1, 11, 50, 0, 0, time.UTC)}}, nil
		}},
	}
}

func TestDaemonTimelineMatchesCLIJSON(t *testing.T) {
	c := inventoryClient(t, daemoninventory.Options{
		TimelineSources: timelineFixture,
		Clock:           clock.NewFake(fixedNow()),
	})
	for _, tc := range []struct {
		flags  []string
		params any
	}{
		{nil, nil},
		{[]string{"--since", "20m"}, daemoninventory.TimelineParams{SinceMS: (20 * time.Minute).Milliseconds()}},
		{[]string{"--since", "6h"}, daemoninventory.TimelineParams{SinceMS: (6 * time.Hour).Milliseconds()}},
	} {
		var cli, errb bytes.Buffer
		if rc := runTimelineWithIO(append(tc.flags, "--json"), &cli, &errb, fixedNow, timelineFixture()...); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		assertSameJSON(t, "timeline", cli.String(), inventoryRPC(t, c, daemoninventory.MethodTimeline, tc.params))
	}
}

func snapshotFixture() *fakeSnapshotRegistry {
	reg := newFakeSnapshotRegistry()
	old := testSnapshot("snap-old", snapshot.KindBaseline, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	old.Processes = nil
	newer := testSnapshot("snap-new", snapshot.KindLive, time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	newer.Host.Hostname = "renamed.local"
	newer.Host.OSName = "Linux"
	reg.putSnapshot(old, "pre-incident")
	reg.putSnapshot(newer, "")
	return reg
}

func snapshotReadFixture(reg *fakeSnapshotRegistry) (snapshotReadDeps, daemoninventory.Options) {
	live := testSnapshot("snap-live", snapshot.KindLive, time.Date(2026, 5, 3, 13, 0, 0, 0, time.UTC))
	live.Host.Hostname = "live.local"
	liveFn := func(context.Context) *snapshot.Snapshot { s := live; return &s }
	now := time.Date(2026, 5, 3, 12, 30, 0, 0, time.UTC)
	cli := snapshotReadDeps{
		open: func() (snapshotquery.Reader, func(), error) { return reg, func() {}, nil },
		live: liveFn,
		now:  func() time.Time { return now },
	}
	return cli, daemoninventory.Options{Snapshots: reg, Live: liveFn, Clock: clock.NewFake(now)}
}

func TestDaemonSnapshotListMatchesCLIJSON(t *testing.T) {
	reg := snapshotFixture()
	cliDeps, opts := snapshotReadFixture(reg)
	c := inventoryClient(t, opts)
	for _, kind := range []string{"", "baseline", "live"} {
		var cli, errb bytes.Buffer
		if rc := runSnapshotListWithIO([]string{"--json", "--kind", kind}, &cli, &errb, cliDeps); rc != 0 {
			t.Fatalf("exit %d: %s", rc, errb.String())
		}
		assertSameJSON(t, "snapshot list "+kind, cli.String(), inventoryRPC(t, c, daemoninventory.MethodSnapshotList, daemoninventory.SnapshotListParams{Kind: kind}))
	}
}

func TestDaemonSnapshotListEmptyIsArray(t *testing.T) {
	cliDeps, opts := snapshotReadFixture(newFakeSnapshotRegistry())
	var cli, errb bytes.Buffer
	if rc := runSnapshotListWithIO([]string{"--json"}, &cli, &errb, cliDeps); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	if cli.String() != "[]\n" {
		t.Fatalf("CLI empty list = %q, want []", cli.String())
	}
	c := inventoryClient(t, opts)
	assertSameJSON(t, "empty snapshot list", cli.String(), inventoryRPC(t, c, daemoninventory.MethodSnapshotList, nil))
}

func TestDaemonSnapshotDiffMatchesCLIJSON(t *testing.T) {
	reg := snapshotFixture()
	cliDeps, opts := snapshotReadFixture(reg)
	c := inventoryClient(t, opts)
	for _, tc := range []struct {
		args   []string
		params daemoninventory.SnapshotDiffParams
	}{
		{[]string{"snap-old", "snap-new"}, daemoninventory.SnapshotDiffParams{A: "snap-old", B: "snap-new"}},
		{[]string{"snap-new", "live"}, daemoninventory.SnapshotDiffParams{A: "snap-new", B: "live"}},
		{[]string{"baseline"}, daemoninventory.SnapshotDiffParams{Baseline: true}},
		{[]string{"baseline", "pre-incident", "snap-new"}, daemoninventory.SnapshotDiffParams{Baseline: true, A: "pre-incident", B: "snap-new"}},
		{[]string{"--since", "12h"}, daemoninventory.SnapshotDiffParams{SinceMS: (12 * time.Hour).Milliseconds()}},
	} {
		var cli, errb bytes.Buffer
		if rc := runSnapshotDiffWithIO(append([]string{"--json"}, tc.args...), &cli, &errb, cliDeps); rc != 0 {
			t.Fatalf("%v: exit %d: %s", tc.args, rc, errb.String())
		}
		assertSameJSON(t, "snapshot diff", cli.String(), inventoryRPC(t, c, daemoninventory.MethodSnapshotDiff, tc.params))
	}
}

// A diff the CLI rejects with an error is an error over RPC too, with the
// same message.
func TestDaemonSnapshotDiffErrorMatchesCLI(t *testing.T) {
	reg := snapshotFixture()
	cliDeps, opts := snapshotReadFixture(reg)
	var cli, errb bytes.Buffer
	if rc := runSnapshotDiffWithIO([]string{"--json", "snap-old", "nope"}, &cli, &errb, cliDeps); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	c := inventoryClient(t, opts)
	err := c.Call(context.Background(), daemoninventory.MethodSnapshotDiff, daemoninventory.SnapshotDiffParams{A: "snap-old", B: "nope"}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
		t.Fatalf("rpc error %v, CLI stderr %q", err, errb.String())
	}
}
