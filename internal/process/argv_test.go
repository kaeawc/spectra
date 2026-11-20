package process

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/hostos"
)

func procArgs2(argc int, exe string, args ...string) []byte {
	b := binary.NativeEndian.AppendUint32(nil, uint32(argc))
	b = append(b, exe...)
	b = append(b, 0, 0, 0, 0) // terminator plus alignment padding
	for _, a := range args {
		b = append(b, a...)
		b = append(b, 0)
	}
	return b
}

func TestParseProcArgs2(t *testing.T) {
	script := "/Users/Jane Doe/node_modules/@google/gemini-cli/dist/index.js"
	raw := procArgs2(2, "/opt/homebrew/bin/node", "node", script, "HOME=/Users/Jane Doe", "PATH=/bin")
	got, err := parseProcArgs2(raw)
	if err != nil || !reflect.DeepEqual(got, []string{"node", script}) {
		t.Fatalf("parseProcArgs2 = %q, %v; want argv without environment", got, err)
	}
	for name, bad := range map[string][]byte{
		"short":                 {1, 0},
		"no exe terminator":     binary.NativeEndian.AppendUint32(nil, 1),
		"truncated argument":    append(procArgs2(2, "/bin/x", "x"), 'y'),
		"argc beyond the bound": procArgs2(maxArgvCount+1, "/bin/x", "x"),
	} {
		if got, err := parseProcArgs2(bad); err == nil {
			t.Errorf("%s: parseProcArgs2 = %q, want error", name, got)
		}
	}
	if got, err := parseProcArgs2(procArgs2(0, "/bin/x")); err != nil || got != nil {
		t.Fatalf("argc 0 = %q, %v; want nil", got, err)
	}
}

func TestSplitCmdline(t *testing.T) {
	if got := splitCmdline([]byte("node codex\x00server.js\x00")); !reflect.DeepEqual(got, []string{"node codex", "server.js"}) {
		t.Fatalf("splitCmdline = %q", got)
	}
	if got := splitCmdline(nil); got != nil {
		t.Fatalf("empty cmdline (kernel thread) = %q, want nil", got)
	}
}

func TestArgvMatchesListing(t *testing.T) {
	argv := []string{"node", "/a  b/x.js"}
	if !argvMatches(argv, "node /a b/x.js") {
		t.Fatal("argv rejected against ps output with collapsed whitespace")
	}
	if argvMatches(argv, "python3 other.py") || argvMatches(nil, "node") {
		t.Fatal("argv accepted for a different (recycled) process")
	}
}

func TestHostArgvLinuxProcfs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("node\x00/home/Jane Doe/app.js\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := hostArgv(hostos.Linux, root, 42)
	if err != nil || !reflect.DeepEqual(got, []string{"node", "/home/Jane Doe/app.js"}) {
		t.Fatalf("hostArgv = %q, %v", got, err)
	}
	if _, err := hostArgv(hostos.Linux, root, 43); err == nil {
		t.Fatal("missing pid read without error")
	}
}

func TestHostArgvOwnProcess(t *testing.T) {
	// The real OS, not hostos.Current: other tests in this package override
	// the reported OS, but the sysctl and /proc reads depend on the build.
	var kind hostos.Kind
	switch runtime.GOOS {
	case "darwin":
		kind = hostos.Darwin
	case "linux":
		kind = hostos.Linux
	default:
		t.Skip("argv is read on macOS and Linux only")
	}
	got, err := hostArgv(kind, "/proc", os.Getpid())
	if err != nil || !reflect.DeepEqual(got, os.Args) {
		t.Fatalf("own argv = %q, %v; want %q", got, err, os.Args)
	}
}

func TestClassifyReadsArgvOnlyForInterpreters(t *testing.T) {
	script := "/home/Jane Doe/node_modules/@google/gemini-cli/dist/index.js"
	argv := map[int][]string{10: {"node", script}, 11: {"tool", "--flag"}}
	var asked []int
	prev := argvFor
	argvFor = func(pid int, full string) []string {
		asked = append(asked, pid)
		return argv[pid]
	}
	t.Cleanup(func() { argvFor = prev })

	procs := []Info{
		{PID: 10, Command: "node", ExecutablePath: "/opt/x/bin/node", FullCommandLine: "node " + script},
		{PID: 11, Command: "tool", ExecutablePath: "/opt/x/bin/tool", FullCommandLine: "tool --flag"},
	}
	c := classify.New(classify.Options{OS: hostos.Linux, ProcFS: t.TempDir()})
	if err := Classify(context.Background(), procs, c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asked, []int{10}) {
		t.Fatalf("argv read for pids %v, want only the interpreter", asked)
	}
	if procs[0].Role != classify.RoleAIAgent {
		t.Fatalf("script path with a space lost its package: role %s %v", procs[0].Role, procs[0].Evidence)
	}
	if !reflect.DeepEqual(procs[0].Argv, argv[10]) || procs[1].Argv != nil {
		t.Fatalf("argv recorded as %q / %q", procs[0].Argv, procs[1].Argv)
	}
	data, err := json.Marshal(procs[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"argv"`) {
		t.Fatalf("argv emitted for a process it was not read for: %s", data)
	}
}
