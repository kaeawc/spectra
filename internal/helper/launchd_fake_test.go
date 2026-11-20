package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// --- fake filesystem with owners, modes and inodes ---

type fakeNode struct {
	meta FileMeta
	data []byte
	link string
}

type fakeFS struct {
	mu    sync.Mutex
	nodes map[string]*fakeNode
	ino   uint64
	clock time.Time
}

func newFakeFS() *fakeFS {
	f := &fakeFS{nodes: map[string]*fakeNode{}, clock: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	f.nodes["/"] = &fakeNode{meta: f.newMeta(fs.ModeDir|0o755, 0, 0, 0)}
	return f
}

func (f *fakeFS) newMeta(mode fs.FileMode, uid, gid uint32, size int64) FileMeta {
	f.ino++
	f.clock = f.clock.Add(time.Second)
	return FileMeta{Mode: mode, UID: uid, GID: gid, Dev: 1, Ino: f.ino, Size: size, ModTime: f.clock}
}

// ensureParents creates missing parent directories root:wheel 0755.
func (f *fakeFS) ensureParents(p string) {
	dir := filepath.Dir(p)
	if dir == p {
		return
	}
	if _, ok := f.nodes[dir]; !ok {
		f.ensureParents(dir)
		f.nodes[dir] = &fakeNode{meta: f.newMeta(fs.ModeDir|0o755, 0, 0, 0)}
	}
}

func (f *fakeFS) dir(p string, uid, gid uint32, perm fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureParents(p)
	f.nodes[p] = &fakeNode{meta: f.newMeta(fs.ModeDir|perm, uid, gid, 0)}
}

func (f *fakeFS) file(p string, data string, uid, gid uint32, perm fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureParents(p)
	f.nodes[p] = &fakeNode{meta: f.newMeta(perm, uid, gid, int64(len(data))), data: []byte(data)}
}

// rewrite replaces a file's bytes in place (same inode, new size and mtime).
func (f *fakeFS) rewrite(p, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[p]
	f.clock = f.clock.Add(time.Second)
	n.data, n.meta.Size, n.meta.ModTime = []byte(data), int64(len(data)), f.clock
}

func (f *fakeFS) symlink(p, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureParents(p)
	f.nodes[p] = &fakeNode{meta: f.newMeta(fs.ModeSymlink|0o755, 0, 0, int64(len(target))), link: target}
}

func (f *fakeFS) chown(p string, uid, gid uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[p].meta.UID, f.nodes[p].meta.GID = uid, gid
}

func (f *fakeFS) chmod(p string, perm fs.FileMode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[p]
	n.meta.Mode = n.meta.Mode.Type() | perm
}

func notExist(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

func (f *fakeFS) Lstat(p string) (FileMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[p]
	if !ok {
		return FileMeta{}, notExist("lstat", p)
	}
	return n.meta, nil
}

func (f *fakeFS) ReadDir(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[dir]
	if !ok {
		return nil, notExist("readdir", dir)
	}
	if !n.meta.Mode.IsDir() {
		return nil, fmt.Errorf("readdir %s: not a directory", dir)
	}
	var names []string
	for p := range f.nodes {
		if p != "/" && filepath.Dir(p) == dir {
			names = append(names, filepath.Base(p))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeFS) OpenNoFollow(p string) (io.ReadCloser, FileMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[p]
	switch {
	case !ok:
		return nil, FileMeta{}, notExist("open", p)
	case n.meta.Mode&fs.ModeSymlink != 0:
		return nil, FileMeta{}, &fs.PathError{Op: "open", Path: p, Err: syscall.ELOOP}
	}
	return io.NopCloser(bytes.NewReader(append([]byte{}, n.data...))), n.meta, nil
}

func (f *fakeFS) EvalSymlinks(p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for range 40 {
		changed := false
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		cur := "/"
		for i, part := range parts {
			cur = filepath.Join(cur, part)
			n, ok := f.nodes[cur]
			if !ok {
				return "", notExist("evalsymlinks", cur)
			}
			if n.link != "" {
				p = filepath.Join(append([]string{n.link}, parts[i+1:]...)...)
				changed = true
				break
			}
		}
		if !changed {
			return p, nil
		}
	}
	return "", errors.New("too many links")
}

// --- fake host: launchctl, codesign, sfltool ---

type fakeHost struct {
	mu       sync.Mutex
	calls    [][]string
	label    string
	loaded   bool
	disabled bool
	pid      int
	// jobPath and jobProgram are what launchd reports when loaded.
	jobPath, jobProgram string

	team        string
	adhoc       bool
	unsigned    map[string]bool
	detritus    map[string]bool
	wrongTeam   map[string]bool
	appleSigned map[string]bool

	btm     string
	btmExit int

	// hook may answer a command first (failures, state races).
	hook func(argv []string) (proc.Result, bool)
}

func (h *fakeHost) Run(_ context.Context, c proc.Cmd) (proc.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	argv := append([]string{c.Name}, c.Args...)
	h.calls = append(h.calls, argv)
	if h.hook != nil {
		if r, ok := h.hook(argv); ok {
			return r, nil
		}
	}
	switch c.Name {
	case launchctlBin:
		return h.launchctl(c.Args), nil
	case codesignBin:
		return h.codesign(c.Args), nil
	case sfltoolBin:
		return proc.Result{Stdout: []byte(h.btm), ExitCode: h.btmExit}, nil
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("unexpected command %v", argv)
}

func (h *fakeHost) launchctl(args []string) proc.Result {
	switch args[0] {
	case "print":
		if !h.loaded || args[1] != "system/"+h.label {
			return proc.Result{ExitCode: 113, Stderr: []byte("Bad request.\nCould not find service \"x\" in domain for system\n")}
		}
		out := fmt.Sprintf("system/%s = {\n\tactive count = 1\n\tpath = %s\n\ttype = LaunchDaemon\n\tstate = running\n\n\tprogram = %s\n\targuments = {\n\t\t%s\n\t}\n\n\tsockets = {\n\t\t\"s\" = {\n\t\t\tpath = /var/run/fake.sock\n\t\t}\n\t}\n\tdomain = system\n\tpid = %d\n}\n",
			h.label, h.jobPath, h.jobProgram, h.jobProgram, h.pid)
		return proc.Result{Stdout: []byte(out)}
	case "print-disabled":
		state := map[bool]string{true: "disabled", false: "enabled"}[h.disabled]
		return proc.Result{Stdout: []byte("disabled services = {\n\t\"" + h.label + "\" => " + state + "\n\t\"com.apple.other\" => disabled\n}\n")}
	case "disable":
		h.disabled = true
	case "enable":
		h.disabled = false
	case "bootout":
		h.loaded, h.pid = false, 0
	case "bootstrap":
		h.loaded, h.pid = true, 77
	default:
		return proc.Result{ExitCode: 64}
	}
	return proc.Result{}
}

func (h *fakeHost) codesign(args []string) proc.Result {
	path := args[len(args)-1]
	if args[0] == "-dv" {
		if h.adhoc {
			return proc.Result{Stderr: []byte("Executable=" + path + "\nSignature=adhoc\nTeamIdentifier=not set\n")}
		}
		return proc.Result{Stderr: []byte("Executable=" + path + "\nAuthority=Developer ID Application: Example\nTeamIdentifier=" + h.team + "\n")}
	}
	strict := len(args) > 1 && args[1] == "--strict"
	req := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-R=") {
			req = a
		}
	}
	switch {
	case req == "-R=anchor apple":
		if h.appleSigned[path] {
			return proc.Result{}
		}
		return proc.Result{ExitCode: 3, Stderr: []byte(path + ": test-requirement: code failed to satisfy specified code requirement(s)\n")}
	case h.unsigned[path]:
		return proc.Result{ExitCode: 1, Stderr: []byte(path + ": code object is not signed at all\n")}
	case strict && h.detritus[path]:
		return proc.Result{ExitCode: 1, Stderr: []byte(path + ": resource fork, Finder information, or similar detritus not allowed\n")}
	case req != "" && (h.wrongTeam[path] || !strings.Contains(req, `"`+h.team+`"`)):
		return proc.Result{ExitCode: 3}
	}
	return proc.Result{}
}

func (h *fakeHost) mutations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if c[0] == launchctlBin && c[1] != "print" && c[1] != "print-disabled" {
			out = append(out, strings.Join(c[1:], " "))
		}
	}
	return out
}

func (h *fakeHost) set(fn func(h *fakeHost)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

// --- the default world: one verified third-party daemon ---

const (
	testLabel   = "com.example.agentd"
	testPlist   = "/Library/LaunchDaemons/com.example.agentd.plist"
	testBundle  = "/Applications/Example.app"
	testProgram = "/Applications/Example.app/Contents/Library/LaunchServices/agentd"
	testTeam    = "ABCDE12345"
)

func daemonPlist(label, program string, extra string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + label +
		`</string><key>ProgramArguments</key><array><string>` + program + `</string></array>` + extra + `</dict></plist>`
}

func infoPlist(id string) string {
	return `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>` + id + `</string></dict></plist>`
}

const testBTM = `========================
 Records for UID -2 : 00000000-0000-0000-0000-000000000000
========================

 Items:

 #1:
                 UUID: 00000000-0000-0000-0000-000000000001
                 Name: com.example.agentd
      Team Identifier: ABCDE12345
                 Type: legacy daemon (0x10010)
                Flags: [ legacy ] (0x1)
          Disposition: [enabled, allowed, notified] (0xb)
           Identifier: 16.com.example.agentd
                  URL: file:///Library/LaunchDaemons/com.example.agentd.plist
      Executable Path: /Applications/Example.app/Contents/Library/LaunchServices/agentd

 #2:
                 UUID: 00000000-0000-0000-0000-000000000002
                 Name: com.example.other
                 Type: legacy daemon (0x10010)
                Flags: [ weird ] (0x80)
          Disposition: [enabled, allowed, notified] (0xb)
           Identifier: 16.com.example.other
                  URL: file:///Library/LaunchDaemons/com.example.other.plist
`

type world struct {
	t     *testing.T
	fs    *fakeFS
	host  *fakeHost
	now   time.Time
	admin map[uint32]bool
	svc   *launchdService
	audit *bytes.Buffer
	d     *Dispatcher
}

func newWorld(t *testing.T) *world {
	t.Helper()
	f := newFakeFS()
	f.dir("/Library", 0, 0, 0o755)
	f.dir(launchDaemonsDir, 0, 0, 0o755)
	f.file(testPlist, daemonPlist(testLabel, testProgram, ""), 0, 0, 0o644)
	f.file("/Library/LaunchDaemons/com.example.other.plist", daemonPlist("com.example.other", "/opt/other", ""), 0, 0, 0o644)
	f.file("/Library/LaunchDaemons/.DS_Store", "\x00\x00\x00\x01Bud1 not a plist", 0, 0, 0o644)
	f.file("/Library/LaunchDaemons/README", "not a plist", 0, 0, 0o644)
	f.file("/Library/LaunchDaemons/empty.plist", "", 0, 0, 0o644)
	f.dir(systemLaunchDaemonsDir, 0, 0, 0o755)
	f.file(systemLaunchDaemonsDir+"/com.apple.fake.plist", daemonPlist("com.apple.fake", "/usr/libexec/fake", ""), 0, 0, 0o644)
	f.dir(privilegedHelperToolsDir, 0, 0, 0o755)
	f.dir(applicationsDir, 0, 80, 0o775)
	for _, d := range []string{testBundle, testBundle + "/Contents", testBundle + "/Contents/Library", testBundle + "/Contents/Library/LaunchServices"} {
		f.dir(d, 0, 0, 0o755)
	}
	f.file(testBundle+"/Contents/Info.plist", infoPlist("com.example.app"), 0, 0, 0o644)
	f.file(testProgram, "\xcf\xfa\xed\xfe", 0, 0, 0o755)

	h := &fakeHost{label: testLabel, loaded: true, pid: 412, jobPath: testPlist, jobProgram: testProgram, team: testTeam,
		unsigned: map[string]bool{}, detritus: map[string]bool{}, wrongTeam: map[string]bool{}, appleSigned: map[string]bool{}, btm: testBTM}
	w := &world{t: t, fs: f, host: h, now: time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC), admin: map[uint32]bool{501: true}, audit: &bytes.Buffer{}}
	w.d = NewDispatcher()
	w.d.SetAuditWriter(w.audit)
	w.svc = registerLaunchd(w.d, LaunchdOptions{
		OS: hostos.Darwin, Runner: h, FS: f,
		Now:  func() time.Time { return w.now },
		Rand: &countingRand{},
		IsAdmin: func(_ context.Context, uid uint32) (bool, error) {
			return w.admin[uid], nil
		},
	})
	return w
}

// countingRand returns distinct deterministic bytes on every read.
type countingRand struct {
	mu sync.Mutex
	n  byte
}

func (r *countingRand) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	for i := range p {
		p[i] = r.n + byte(i)
	}
	return len(p), nil
}

func (w *world) prepare(uid uint32, label, action string) *LaunchdPrepareResult {
	w.t.Helper()
	res, err := w.svc.prepare(context.Background(), uid, label, action)
	if err != nil {
		w.t.Fatalf("prepare %s %s: %v", action, label, err)
	}
	return res
}

func (w *world) execute(uid uint32, action, token string) *LaunchdChangeResult {
	w.t.Helper()
	res, err := w.svc.execute(context.Background(), uid, action, token)
	if err != nil {
		w.t.Fatalf("execute %s: %v", action, err)
	}
	return res
}
