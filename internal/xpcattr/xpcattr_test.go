package xpcattr

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The sample fixtures are real macOS 26 captures of a throwaway NSXPC service
// (pid 93407): clients 93403 and 93404 connected through an anonymous
// listener endpoint, 93405 through the named Mach service.
func testProcs() []process.Info {
	return []process.Info{
		{PID: 1, Command: "launchd"},
		{PID: 17491, PPID: 1, Command: "xcodebuil", ExecutablePath: "/usr/bin/xcodebuild", FullCommandLine: "xcodebuild test"},
		{PID: 17500, PPID: 17491, Command: "xctest-helper"},
		{PID: 93404, PPID: 17500, Command: "CtrlProxyUITests-Ru",
			ExecutablePath: "/x/CtrlProxyUITests-Runner", FullCommandLine: "/x/CtrlProxyUITests-Runner"},
		{PID: 93403, PPID: 1, Command: "Terminal"},
	}
}

func TestParseQueueClientsMulti(t *testing.T) {
	got := ParseQueueClients(fixture(t, "sample_multi_client.txt"))
	// Both anonymous-listener and named-service queues carry the client PID.
	if len(got) != 3 || got[93404] != 270 || got[93403] != 269 || got[93405] != 269 {
		t.Errorf("got %v", got)
	}
}

func TestParseQueueClientsLabelForms(t *testing.T) {
	cases := map[string]int{
		"    7 Thread_1   DispatchQueue_3: com.apple.NSXPCConnection.user.anonymous.42  (serial)":                42,
		"    7 Thread_<multiple>   DispatchQueue_3: com.apple.NSXPCConnection.user.com.example.svc.42  (serial)": 42,
		"    7 Thread_1: Main Thread   DispatchQueue_3: com.apple.NSXPCConnection.user.anonymous.42":             42,
		"    + 7 Thread_1   DispatchQueue_3: com.apple.NSXPCConnection.user.anonymous.42  (serial)":              42,
		"    7 Thread_1   DispatchQueue_3: com.apple.NSXPCConnection.m-user.com.apple.logd  (serial)":            0,
		"    7 Thread_1   DispatchQueue_3: com.apple.NSXPCConnection.user.com.example.svc  (serial)":             0,
		"    7 Thread_1   DispatchQueue_3: com.apple.NSXPCConnection.user.anonymous.-3  (serial)":                0,
	}
	for line, want := range cases {
		got := ParseQueueClients(line)
		if want == 0 && len(got) != 0 || want != 0 && (len(got) != 1 || got[want] != 7) {
			t.Errorf("%q: got %v", line, got)
		}
	}
}

func TestParseQueueClientsDropsTruncatedLabel(t *testing.T) {
	// sample cuts queue labels at 127 characters; the PID suffix of this long
	// service name was lost, and a partial PID must not be attributed.
	out := fixture(t, "sample_truncated_label.txt")
	if !strings.Contains(out, "com.apple.NSXPCConnection.user.") {
		t.Fatal("fixture lost its queue line")
	}
	if got := ParseQueueClients(out); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	label := "com.apple.NSXPCConnection.user.anonymous." + strings.Repeat("9", maxQueueLabel-len("com.apple.NSXPCConnection.user.anonymous."))
	if got := ParseQueueClients("  5 Thread_1   DispatchQueue_2: " + label + "  (serial)"); len(got) != 0 {
		t.Errorf("label at the cutoff: got %v", got)
	}
}

func TestParseQueueClientsNoPIDAndGarbage(t *testing.T) {
	if got := ParseQueueClients(fixture(t, "sample_no_pid.txt")); len(got) != 0 {
		t.Errorf("no-pid fixture: %v", got)
	}
	if got := ParseQueueClients(fixture(t, "garbage.txt")); len(got) != 0 {
		t.Errorf("garbage: %v", got)
	}
	if len(ParseQueueClients("")) != 0 {
		t.Error("empty input")
	}
}

func TestResolveChainAndLauncher(t *testing.T) {
	cs := Resolve(map[int]int{93404: 800, 93403: 200}, testProcs())
	top := cs[0]
	if top.PID != 93404 || top.Share != 0.8 || top.Name != "CtrlProxyUITests-Runner" {
		t.Fatalf("top = %+v", top)
	}
	if len(top.Chain) != 2 || top.Launcher == nil || top.Launcher.Name != "xcodebuild" || top.Launcher.PID != 17491 {
		t.Errorf("chain/launcher = %+v %+v", top.Chain, top.Launcher)
	}
	if cs[1].PID != 93403 || cs[1].Launcher != nil {
		t.Errorf("Terminal under launchd should have no launcher: %+v", cs[1])
	}
	cs = Resolve(map[int]int{999: 1}, nil)
	last := cs[0]
	if last.PID != 999 || last.Name != "" {
		t.Errorf("unknown pid = %+v", last)
	}
}

func TestResolveCycle(t *testing.T) {
	procs := []process.Info{{PID: 10, PPID: 11, Command: "a"}, {PID: 11, PPID: 10, Command: "b"}}
	cs := Resolve(map[int]int{10: 1}, procs)
	if len(cs[0].Chain) != 1 {
		t.Errorf("chain = %+v", cs[0].Chain)
	}
}

func TestFromSampleSummary(t *testing.T) {
	a := FromSample(93407, "xpcattr-test-service", fixture(t, "sample_multi_client.txt"), "", testProcs())
	want := "xpcattr-test-service (93407) busy, driven by CtrlProxyUITests-Runner (93404), launched by xcodebuild (17491)"
	if a.Source != SourceSample || a.Summary() != want {
		t.Errorf("source=%s summary=%q", a.Source, a.Summary())
	}
}

// lsmp_holders.txt is a real root capture of the same test service (pid
// 4701) while client 4694 used the anonymous endpoint and 4695 the named
// service, both connected and idle.
func TestParseLsmpHoldersRealCapture(t *testing.T) {
	got := ParseLsmpHolders(fixture(t, "lsmp_holders.txt"), 4701)
	// Clients hold send rights to the listener and connection ports, and the
	// service owes each a reply on a send-once right. opendirectoryd, logd and
	// notifyd hold one send right each to ports the service registered with
	// them; launchd and the service itself are never clients.
	want := map[int]int{4694: 5, 4695: 3, 377: 1, 336: 1, 401: 1}
	if !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseLsmpHoldersRowShapes(t *testing.T) {
	cases := map[string]int{
		"                  +     send        --------        ---            1         <-                                       0x00001b03  (4695) client-b": 4695,
		"                  +     send-once   --------        ---            1         <-                                       0x00001b03  (4695) client-b": 4695,
		"0x00003f3f  0x30ba4bae  send-once   --------        ---                     ->        5         0  0x0000000000000000 0x00000a03  (4695) client-b": 4695,
		// The service's own outbound send right is not a client.
		"0x00000903  0x1def4714  send        --------        ---            1         ->                                       0x00000000  (4695) client-b":              0,
		"0x00000203  0xbbc574fb  send        --------        ---            3                                                  0x00000000  TASK-CONTROL SELF (4701) svc": 0,
		" -          0x05e46f83  recv,send   ---GS---     0  ---      1     1         Y        5         0  0x0000000000000000 0x00001003  (4701) svc":                   0,
		"                  +     send        --------        ---            2         <-                                       0x000e2fbf  (1) launchd":                  0,
		"0x00002493  0xba85593c  send        --------        ---            1                                                  0x00000000  (-) Unknown Process":          0,
		"task_threads() failed: pid:0 error: (ipc/send) invalid destination port":                                                                                        0,
	}
	for line, want := range cases {
		got := ParseLsmpHolders(line, 4701)
		if want == 0 && len(got) != 0 || want != 0 && (len(got) != 1 || got[want] != 1) {
			t.Errorf("%q: got %v", line, got)
		}
	}
}

func TestFromSampleLsmpFallback(t *testing.T) {
	procs := []process.Info{
		{PID: 1, Command: "launchd"},
		{PID: 4694, PPID: 1, Command: "xpcattr-test-cli", ExecutablePath: "/x/xpcattr-test-client-with-a-long-name"},
		{PID: 4695, PPID: 1, Command: "client-b"},
	}
	a := FromSample(4701, "xpcattr-test-service", fixture(t, "sample_no_pid.txt"), fixture(t, "lsmp_holders.txt"), procs)
	if a.Source != SourceLsmp || len(a.Clients) != 5 || a.Clients[0].PID != 4694 || a.Clients[0].Weight != 5 || a.Clients[1].PID != 4695 {
		t.Errorf("attribution = %+v", a)
	}
	want := "xpcattr-test-service (4701) busy, driven by xpcattr-test-client-with-a-long-name (4694)"
	if a.Summary() != want {
		t.Errorf("summary = %q", a.Summary())
	}
}

func TestSummaryEmpty(t *testing.T) {
	if s := FromSample(1, "x", "nothing", "", nil).Summary(); s != "" {
		t.Errorf("summary = %q", s)
	}
}

func TestCaptureUnsupportedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	if _, err := Capture(context.Background(), Options{PID: 1}); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestCaptureDarwinFallsBackToLsmp(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	var cmds []string
	fake := runnerFunc(func(c proc.Cmd) (proc.Result, error) {
		cmds = append(cmds, c.Name)
		if c.Name == "/usr/bin/sample" {
			return proc.Result{Stdout: []byte(fixture(t, "sample_no_pid.txt"))}, nil
		}
		return proc.Result{Stdout: []byte(fixture(t, "lsmp_holders.txt"))}, nil
	})
	a, err := Capture(context.Background(), Options{PID: 17874, Runner: fake,
		Procs: func(context.Context) []process.Info { return testProcs() }})
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourceLsmp || a.Name != "cfprefsd" || strings.Join(cmds, ",") != "/usr/bin/sample,/usr/bin/lsmp" {
		t.Errorf("a=%+v cmds=%v", a, cmds)
	}
}

type runnerFunc func(proc.Cmd) (proc.Result, error)

func (f runnerFunc) Run(_ context.Context, c proc.Cmd) (proc.Result, error) { return f(c) }

func TestLooksLikeService(t *testing.T) {
	cases := map[string]process.Info{
		"libexec":  {ExecutablePath: "/usr/libexec/foo"},
		"xpcdir":   {ExecutablePath: "/System/Library/Frameworks/X.framework/XPCServices/Y.xpc/Contents/MacOS/Y"},
		"bundle":   {ExecutablePath: "/Applications/A.app/Contents/XPCServices/B.xpc/Contents/MacOS/B"},
		"known":    {Command: "OSLogService"},
		"knownexe": {ExecutablePath: "/weird/path/diagnosticd"},
	}
	for name, p := range cases {
		if !LooksLikeService(p) {
			t.Errorf("%s should look like a service", name)
		}
	}
	if LooksLikeService(process.Info{ExecutablePath: "/Applications/Slack.app/Contents/MacOS/Slack", Command: "Slack"}) {
		t.Error("Slack is not a service")
	}
}

func TestLauncherNilWithoutKnownTool(t *testing.T) {
	procs := []process.Info{{PID: 5, PPID: 6, Command: "child"}, {PID: 6, PPID: 1, Command: "zsh"}}
	c := Resolve(map[int]int{5: 1}, procs)[0]
	if len(c.Chain) != 1 || c.Launcher != nil {
		t.Errorf("chain=%+v launcher=%+v", c.Chain, c.Launcher)
	}
}

func captureWith(t *testing.T, o Options) (Attribution, error) {
	t.Helper()
	defer hostos.SetForTest(hostos.Darwin)()
	o.PID = 17874
	o.Procs = func(context.Context) []process.Info { return testProcs() }
	return Capture(context.Background(), o)
}

func TestCaptureSampleFailureIncludesStderr(t *testing.T) {
	_, err := captureWith(t, Options{Runner: runnerFunc(func(proc.Cmd) (proc.Result, error) {
		return proc.Result{ExitCode: 1, Stderr: []byte("no such process")}, nil
	})})
	if err == nil || !strings.Contains(err.Error(), "no such process") {
		t.Errorf("err = %v", err)
	}
}

func TestCaptureSudoRunsBothToolsNonInteractively(t *testing.T) {
	var cmds []string
	boom := errors.New("lsmp crashed")
	_, err := captureWith(t, Options{Sudo: true, Runner: runnerFunc(func(c proc.Cmd) (proc.Result, error) {
		cmds = append(cmds, strings.Join(append([]string{c.Name}, c.Args[:2]...), " "))
		if c.Args[1] == "/usr/bin/sample" {
			return proc.Result{Stdout: []byte(fixture(t, "sample_no_pid.txt"))}, nil
		}
		return proc.Result{}, boom
	})})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(cmds, ",") != "/usr/bin/sudo -n /usr/bin/sample,/usr/bin/sudo -n /usr/bin/lsmp" {
		t.Errorf("cmds = %v", cmds)
	}
}

func TestCaptureSampleWritesToStdoutOnly(t *testing.T) {
	var args []string
	_, _ = captureWith(t, Options{Runner: runnerFunc(func(c proc.Cmd) (proc.Result, error) {
		if c.Name == "/usr/bin/sample" {
			args = c.Args
		}
		return proc.Result{Stdout: []byte(fixture(t, "sample_multi_client.txt"))}, nil
	})})
	if strings.Join(args, " ") != "17874 3 10 -file /dev/stdout" {
		t.Errorf("sample args = %v", args)
	}
}

// Real macOS stderr for each permission failure maps to an actionable hint.
func TestCapturePermissionHints(t *testing.T) {
	cases := []struct {
		name, stderr, want string
		exit               int
		sudo               bool
	}{
		{"root service", "sample_permission_denied.stderr", "retry with --sudo", 255, false},
		{"sudo needs password", "sudo_password_required.stderr", "run `sudo -v` first", 1, true},
	}
	for _, c := range cases {
		_, err := captureWith(t, Options{Sudo: c.sudo, Runner: runnerFunc(func(proc.Cmd) (proc.Result, error) {
			return proc.Result{ExitCode: c.exit, Stderr: []byte(fixture(t, c.stderr))}, nil
		})})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if h := hint(fixture(t, "lsmp_task_for_pid_failed.stderr"), false); !strings.Contains(h, "--sudo") {
		t.Errorf("lsmp task_for_pid hint = %q", h)
	}
	if h := hint("no such process", false); h != "" {
		t.Errorf("unknown failure hint = %q", h)
	}
}

func TestCaptureLsmpErrorSwallowedWithoutSudo(t *testing.T) {
	a, err := captureWith(t, Options{Runner: runnerFunc(func(c proc.Cmd) (proc.Result, error) {
		if c.Name == "/usr/bin/sample" {
			return proc.Result{Stdout: []byte(fixture(t, "sample_no_pid.txt"))}, nil
		}
		return proc.Result{ExitCode: 1}, nil
	})})
	if err != nil || len(a.Clients) != 0 {
		t.Errorf("a=%+v err=%v", a, err)
	}
}
