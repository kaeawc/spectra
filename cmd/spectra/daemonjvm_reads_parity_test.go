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

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemonjvm"
	"github.com/kaeawc/spectra/internal/jvm"
)

const jvmReadsPID = 4242

// jvmReadsClient serves the per-pid read methods with run as the JDK tool
// runner from a real daemon socket, so the parity tests compare the bytes a
// client actually receives.
func jvmReadsClient(t *testing.T, run jvm.CmdRunner, now time.Time) *daemonclient.Client {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spjr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	daemonjvm.RegisterReadMethods(server, daemonjvm.NewService(daemonjvm.Options{
		Runner: func(context.Context) jvm.CmdRunner { return run },
		Clock:  clock.NewFake(now),
	}))
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

// jvmReadsRPC calls method and returns the result indented as the CLI's
// --json encoder writes it.
func jvmReadsRPC(t *testing.T, c *daemonclient.Client, method string, params any) string {
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

const jvmReadsJstatGC = ` S0C    S1C    S0U    S1U      EC       EU        OC         OU       MC     MU    CCSC   CCSU   YGC     YGCT    FGC    FGCT     CGC    CGCT     GCT
 0.0   4096.0  0.0   4096.0 61440.0  12288.0  196608.0   98304.5  45056.0 43520.3 5888.0 5312.1     17    0.123   2      0.456     4    0.010    0.589
`

const jvmReadsJstatClass = `Loaded  Bytes  Unloaded  Bytes     Time
  8123 16012.3        5     7.1       2.31
`

// jvmReadsFixture answers the JDK tool invocations behind every per-pid read
// with canned output; ps fails so the static-facts cache stays out of play.
func jvmReadsFixture(t *testing.T) jvm.CmdRunner {
	t.Helper()
	nmtDetail, err := os.ReadFile(filepath.Join("..", "..", "internal", "jvm", "testdata", "nmt_detail_jdk21.txt"))
	if err != nil {
		t.Fatal(err)
	}
	nmtDiff, err := os.ReadFile(filepath.Join("..", "..", "internal", "jvm", "testdata", "nmt_summary_diff_jdk17.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{
		"jcmd VM.version":                  "4242:\nOpenJDK 64-Bit Server VM version 21.0.6+7-LTS\n",
		"jcmd VM.system_properties":        "4242:\njava.home=/nonexistent/jdk-21\njava.vendor=Eclipse Adoptium\njava.version=21.0.6\n",
		"jcmd VM.command_line":             "4242:\nVM Arguments:\njvm_args: -Xmx512m -XX:NativeMemoryTracking=detail <&>\njava_command: com.example.Main\n",
		"jcmd Thread.print":                sampleThreadDump,
		"jcmd GC.class_histogram":          histAfter,
		"jcmd GC.heap_info":                "4242:\n garbage-first heap   total 262144K, used 110592K\n",
		"jcmd VM.metaspace":                "4242:\nTotal Usage - 812 loaders, 8123 classes:\n  Both:  44.00 MB capacity, 43.50 MB committed,  42.50 MB used\n",
		"jcmd VM.native_memory":            string(nmtDetail),
		"jcmd VM.classloader_stats":        "4242:\nTotal = 812 8123 50331648 46137344\n",
		"jcmd Compiler.codecache":          "4242:\nCodeCache: size=245760Kb, used=20480Kb, max_used=21504Kb, free=225280Kb\n",
		"jcmd Compiler.CodeHeap_Analytics": "4242:\nCodeHeap analytics unavailable\n",
		"jstat -gc":                        jvmReadsJstatGC,
		"jstat -class":                     jvmReadsJstatClass,
	}
	return func(name string, args ...string) ([]byte, error) {
		// jcmd <pid> <command> ...; jstat <option> <pid>.
		key := name
		switch {
		case name == "jstat" && len(args) > 0:
			key += " " + args[0]
		case len(args) > 1:
			key += " " + args[1]
		}
		if key == "jcmd VM.native_memory" && len(args) > 2 && args[2] == "summary.diff" {
			return nmtDiff, nil
		}
		if s, ok := out[key]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("exit status 1")
	}
}

func jvmReadsEnv(stdout, stderr *bytes.Buffer, run jvm.CmdRunner, now time.Time) jvmReadEnv {
	return jvmReadEnv{stdout: stdout, stderr: stderr, run: run, now: func() time.Time { return now }}
}

type jvmReadsCase struct {
	name   string
	cli    func(args []string, stdout, stderr *bytes.Buffer) int
	args   []string
	method string
	params any
}

func jvmReadsCases(run jvm.CmdRunner, now time.Time) []jvmReadsCase {
	readEnv := func(f func([]string, jvmReadEnv) int) func([]string, *bytes.Buffer, *bytes.Buffer) int {
		return func(args []string, stdout, stderr *bytes.Buffer) int {
			return f(args, jvmReadsEnv(stdout, stderr, run, now))
		}
	}
	nmt := func(args []string, stdout, stderr *bytes.Buffer) int {
		return runJVMNMTWith(args, nmtEnv{stdout: stdout, stderr: stderr, run: run, readFile: os.ReadFile})
	}
	pid := "4242"
	soft := false
	return []jvmReadsCase{
		{"explain", readEnv(runJVMExplainWith), []string{pid}, daemonjvm.MethodExplain, daemonjvm.ExplainParams{PID: jvmReadsPID}},
		{"explain trend", readEnv(runJVMExplainWith), []string{"--samples", "2", "--interval", "5ms", "--soft-refs=false", pid},
			daemonjvm.MethodExplain, daemonjvm.ExplainParams{PID: jvmReadsPID, Samples: 2, IntervalMS: 5, SoftRefs: &soft}},
		{"gc-stats", readEnv(runJVMGCStatsWith), []string{pid}, daemonjvm.MethodGCStats, daemonjvm.PIDParams{PID: jvmReadsPID}},
		{"heap-histogram", readEnv(runJVMHeapHistogramWith), []string{pid}, daemonjvm.MethodHeapHistogram, daemonjvm.HeapHistogramParams{PID: jvmReadsPID}},
		{"heap-histogram suspects", readEnv(runJVMHeapHistogramWith), []string{"--suspects", "1", pid},
			daemonjvm.MethodHeapHistogram, daemonjvm.HeapHistogramParams{PID: jvmReadsPID, Suspects: 1}},
		{"thread-dump", readEnv(runJVMThreadDumpWith), []string{pid}, daemonjvm.MethodThreadDump, daemonjvm.PIDParams{PID: jvmReadsPID}},
		{"vm-memory", readEnv(runJVMVMMemoryWith), []string{pid}, daemonjvm.MethodVMMemory, daemonjvm.PIDParams{PID: jvmReadsPID}},
		{"nmt", nmt, []string{pid}, daemonjvm.MethodNMT, daemonjvm.NMTParams{PID: jvmReadsPID}},
		{"nmt detail", nmt, []string{"detail", pid}, daemonjvm.MethodNMT, daemonjvm.NMTParams{PID: jvmReadsPID, Mode: "detail"}},
		{"nmt diff", nmt, []string{"diff", pid}, daemonjvm.MethodNMT, daemonjvm.NMTParams{PID: jvmReadsPID, Mode: "diff"}},
	}
}

// withJSONFlag inserts --json after any leading subcommand word.
func withJSONFlag(args []string) []string {
	if len(args) > 0 && (args[0] == "detail" || args[0] == "diff") {
		return append([]string{args[0], "--json"}, args[1:]...)
	}
	return append([]string{"--json"}, args...)
}

func TestDaemonJVMReadsMatchCLIJSON(t *testing.T) {
	run := jvmReadsFixture(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	c := jvmReadsClient(t, run, now)
	// Each fixture must reach the parsed payload, not an empty shell.
	want := map[string]string{
		"explain": `"soft_refs"`, "explain trend": `"trend"`, "gc-stats": "0.589",
		"heap-histogram": `"suspects"`, "heap-histogram suspects": `"suspects"`,
		"thread-dump": "worker-1", "vm-memory": "garbage-first heap",
		"nmt": `"categories"`, "nmt detail": `"frames"`, "nmt diff": `"delta_committed_kib"`,
	}
	for _, tc := range jvmReadsCases(run, now) {
		var cli, errb bytes.Buffer
		if rc := tc.cli(withJSONFlag(tc.args), &cli, &errb); rc != 0 {
			t.Fatalf("%s: exit %d: %s", tc.name, rc, errb.String())
		}
		if !strings.Contains(cli.String(), want[tc.name]) {
			t.Fatalf("%s: CLI JSON lacks %s:\n%s", tc.name, want[tc.name], cli.String())
		}
		rpc := jvmReadsRPC(t, c, tc.method, tc.params)
		if cli.String() != rpc {
			t.Fatalf("%s: daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", tc.name, cli.String(), rpc)
		}
	}
}

// A read the CLI fails is an error over RPC too, with the CLI's stderr text.
func TestDaemonJVMReadsErrorsMatchCLI(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	noJDK := func(name string, _ ...string) ([]byte, error) {
		return nil, errors.New(`exec: "` + name + `": executable file not found in $PATH`)
	}
	nmtOff := func(string, ...string) ([]byte, error) {
		return []byte("4242:\nNative memory tracking is not enabled\n"), nil
	}
	for _, fx := range []struct {
		name string
		run  jvm.CmdRunner
		skip map[string]bool
	}{
		// vm-memory reports per-section errors inside its JSON instead.
		{"no jdk", noJDK, map[string]bool{"vm-memory": true}},
		{"nmt off", nmtOff, nil},
	} {
		c := jvmReadsClient(t, fx.run, now)
		for _, tc := range jvmReadsCases(fx.run, now) {
			if fx.skip[tc.name] || (fx.name == "nmt off" && !strings.HasPrefix(tc.name, "nmt")) {
				continue
			}
			var cli, errb bytes.Buffer
			if rc := tc.cli(withJSONFlag(tc.args), &cli, &errb); rc != 1 {
				t.Fatalf("%s/%s: exit %d", fx.name, tc.name, rc)
			}
			err := c.Call(context.Background(), tc.method, tc.params, nil)
			var rpcErr *daemonclient.RPCError
			if !errors.As(err, &rpcErr) || rpcErr.Message+"\n" != errb.String() {
				t.Fatalf("%s/%s: rpc error %v, CLI stderr %q", fx.name, tc.name, err, errb.String())
			}
		}
	}
}

// With every section failing the CLI still prints the JSON (and exits 1);
// the daemon returns that same JSON.
func TestDaemonJVMVMMemoryAllFailedMatchesCLIJSON(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fail := func(string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }
	var cli, errb bytes.Buffer
	if rc := runJVMVMMemoryWith([]string{"--json", "4242"}, jvmReadsEnv(&cli, &errb, fail, now)); rc != 1 {
		t.Fatalf("exit %d", rc)
	}
	c := jvmReadsClient(t, fail, now)
	if rpc := jvmReadsRPC(t, c, daemonjvm.MethodVMMemory, daemonjvm.PIDParams{PID: jvmReadsPID}); rpc != cli.String() {
		t.Fatalf("daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", cli.String(), rpc)
	}
}
