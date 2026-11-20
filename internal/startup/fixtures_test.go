package startup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const testUID = 501

// fakeLaunchd is a proc.Runner that models launchd's disabled and loaded
// state, so a change followed by a re-read sees the new state. It answers
// codesign with a fixed team id and sfltool with a fixture.
type fakeLaunchd struct {
	mu       sync.Mutex
	calls    []proc.Call
	disabled map[string]map[string]bool         // domain -> label -> disabled
	loaded   map[string]map[string]serviceState // domain -> label -> state
	plists   map[string]string                  // plist path -> label, for bootstrap
	btm      string
	fail     map[string]bool // verbs that exit non-zero without changing state
	// result is what a verb returns after it has changed state, for
	// launchctl's partial results (bootout's "No such process").
	result     map[string]proc.Result
	keepLoaded bool // bootout leaves the job loaded (still stopping)
}

func newFakeLaunchd() *fakeLaunchd {
	gui := fmt.Sprintf("gui/%d", testUID)
	return &fakeLaunchd{
		disabled: map[string]map[string]bool{gui: {}, "system": {}},
		loaded:   map[string]map[string]serviceState{gui: {}, "system": {}},
		plists:   map[string]string{},
		fail:     map[string]bool{},
		result:   map[string]proc.Result{},
	}
}

func (f *fakeLaunchd) Run(_ context.Context, c proc.Cmd) (proc.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, proc.Call{Name: c.Name, Args: append([]string{}, c.Args...)})
	switch c.Name {
	case "codesign":
		return proc.Result{Stderr: []byte("Authority=Developer ID Application: Example (TEAM123456)\nTeamIdentifier=TEAM123456\n")}, nil
	case sfltoolPath:
		return proc.Result{Stdout: []byte(f.btm)}, nil
	case launchctlPath:
		return f.launchctl(c.Args)
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("unexpected command %s", c.Name)
}

func (f *fakeLaunchd) launchctl(args []string) (proc.Result, error) {
	if len(args) == 0 {
		return proc.Result{ExitCode: 64}, nil
	}
	if f.fail[args[0]] {
		return proc.Result{ExitCode: 5, Stderr: []byte("Input/output error")}, nil
	}
	switch args[0] {
	case "print-disabled":
		var b strings.Builder
		b.WriteString("disabled services = {\n")
		for label, d := range f.disabled[args[1]] {
			state := "enabled"
			if d {
				state = "disabled"
			}
			fmt.Fprintf(&b, "\t\t%q => %s\n", label, state)
		}
		b.WriteString("}\n")
		return proc.Result{Stdout: []byte(b.String())}, nil
	case "print":
		var b strings.Builder
		fmt.Fprintf(&b, "%s = {\n\ttype = login\n\tservices = {\n", args[1])
		for label, st := range f.loaded[args[1]] {
			exit := st.LastExit
			if exit == "" {
				exit = "-"
			}
			fmt.Fprintf(&b, "\t\t%8d %6s \t%s\n", st.PID, exit, label)
		}
		b.WriteString("\t}\n\n\tdisabled services = {\n\t}\n}\n")
		return proc.Result{Stdout: []byte(b.String())}, nil
	case "disable", "enable":
		domain, label := splitTarget(args[1])
		f.disabled[domain][label] = args[0] == "disable"
	case "bootout":
		domain, label := splitTarget(args[1])
		if !f.keepLoaded {
			delete(f.loaded[domain], label)
		}
	case "bootstrap":
		label, ok := f.plists[args[2]]
		if !ok {
			label = strings.TrimSuffix(filepath.Base(args[2]), ".plist")
		}
		f.loaded[args[1]][label] = serviceState{PID: 4242}
	}
	return f.result[args[0]], nil
}

func splitTarget(t string) (string, string) {
	i := strings.LastIndex(t, "/")
	return t[:i], t[i+1:]
}

func (f *fakeLaunchd) Calls() []proc.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proc.Call(nil), f.calls...)
}

// mutatingVerbs are every launchctl verb that changes launchd state.
var mutatingVerbs = map[string]bool{
	"disable": true, "enable": true, "bootout": true, "bootstrap": true, "load": true,
	"unload": true, "remove": true, "kickstart": true, "kill": true, "submit": true,
	"start": true, "stop": true, "setenv": true, "unsetenv": true, "config": true,
}

func mutatingCalls(calls []proc.Call) []string {
	var out []string
	for _, c := range calls {
		if c.Name == launchctlPath && len(c.Args) > 0 && mutatingVerbs[c.Args[0]] {
			out = append(out, strings.Join(c.Args, " "))
		}
		if c.Name == sfltoolPath && (len(c.Args) == 0 || c.Args[0] != "dumpbtm") {
			out = append(out, "sfltool "+strings.Join(c.Args, " "))
		}
	}
	return out
}

// tree is a fixture filesystem layout under t.TempDir().
type tree struct {
	root, home, userAgents, libAgents, libDaemons, apps string
}

func newTree(t *testing.T) tree {
	t.Helper()
	root := t.TempDir()
	tr := tree{
		root:       root,
		home:       filepath.Join(root, "home"),
		userAgents: filepath.Join(root, "home", "Library", "LaunchAgents"),
		libAgents:  filepath.Join(root, "Library", "LaunchAgents"),
		libDaemons: filepath.Join(root, "Library", "LaunchDaemons"),
		apps:       filepath.Join(root, "Applications"),
	}
	for _, d := range []string{tr.userAgents, tr.libAgents, tr.libDaemons, tr.apps} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func (tr tree) opts(r proc.Runner) Options {
	return Options{
		OS:         hostos.Darwin,
		Runner:     r,
		EUID:       func() int { return testUID },
		Getenv:     func(string) string { return "" },
		LookupHome: func(int) (string, error) { return tr.home, nil },
		LookupUser: func(uint32) string { return "tester" },
		Now:        func() time.Time { return time.Unix(1700000000, 0) },
		PlistDirs: []PlistDir{
			{Path: tr.userAgents, Scope: ScopeUser, Kind: KindLaunchAgent},
			{Path: tr.libAgents, Scope: ScopeSystem, Kind: KindLaunchAgent},
			{Path: tr.libDaemons, Scope: ScopeSystem, Kind: KindLaunchDaemon},
		},
		AppDirs: []string{tr.apps},
		// No store unless a test writes one, so tests never read the host's.
		BTMStoreDir:    filepath.Join(tr.root, "btm"),
		LookupUserUUID: func(int) (string, error) { return testUserUUID, nil },
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeJob writes a launchd plist whose body is the given dict entries.
func writeJob(t *testing.T, dir, label, entries string) string {
	t.Helper()
	path := filepath.Join(dir, label+".plist")
	writeFile(t, path, `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Label</key><string>`+label+`</string>`+entries+`</dict></plist>`)
	return path
}

// writeApp builds a minimal .app with an Info.plist and main executable.
func writeApp(t *testing.T, dir, name, bundleID string) string {
	t.Helper()
	app := filepath.Join(dir, name+".app")
	writeFile(t, filepath.Join(app, "Contents", "Info.plist"), `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>`+bundleID+`</string>
<key>CFBundleExecutable</key><string>`+name+`</string>
</dict></plist>`)
	writeFile(t, filepath.Join(app, "Contents", "MacOS", name), "not a real binary")
	return app
}

func byLabel(items []Item) map[string]Item {
	got := map[string]Item{}
	for _, it := range items {
		got[it.Label] = it
	}
	return got
}

func labels(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Label)
	}
	sort.Strings(out)
	return out
}
