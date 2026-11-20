package owner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// deletedSuffix is what Linux appends to the cwd link of a removed directory.
const deletedSuffix = " (deleted)"

// UnderResult answers "which processes are working inside these trees?".
type UnderResult struct {
	Dirs    []string `json:"dirs"`
	Holders []Holder `json:"holders"`
	// Incomplete is set when some other user's process could not be read, so
	// a process working under a directory may be missing.
	Incomplete bool   `json:"incomplete,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

const (
	underDeniedHintDarwin = "other users' processes are not visible without root; re-run with sudo"
	underDeniedHintLinux  = "some /proc/<pid>/cwd links were permission-denied (other users, or no CAP_SYS_PTRACE)"
)

// cwdEntry is one process's working directory as the cwd source saw it.
// Command and PPID come from that source too, so a process the process
// table missed can still be reported and excluded when it is Spectra's own.
type cwdEntry struct {
	Cwd     string
	Command string
	PPID    int
}

// Cwds reads the working directory of every process in procs (and, on Linux,
// of any other pid /proc lists; on macOS, of every pid lsof reports). denied
// is true when a process owned by someone else (or, on Linux, any process)
// could not be read. macOS issues one `lsof -d cwd` and decodes its escapes;
// Linux readlinks /proc/<pid>/cwd and keeps the " (deleted)" suffix of a
// removed directory.
func (f Finder) Cwds(ctx context.Context, procs []process.Info) (cwds map[int]string, denied bool, err error) {
	entries, denied, err := f.cwdEntries(ctx, procs)
	if err != nil {
		return nil, false, err
	}
	cwds = make(map[int]string, len(entries))
	for pid, e := range entries {
		cwds[pid] = e.Cwd
	}
	return cwds, denied, nil
}

func (f Finder) cwdEntries(ctx context.Context, procs []process.Info) (map[int]cwdEntry, bool, error) {
	var entries map[int]cwdEntry
	var denied bool
	switch hostos.Resolve(f.OS) {
	case hostos.Linux:
		entries, denied = f.procCwds(ctx, procs)
	case hostos.Darwin:
		var err error
		entries, err = f.lsofCwds(ctx)
		if err != nil {
			return nil, false, err
		}
		denied = !f.isRoot() && othersMissing(procs, entries, f.euid())
	default:
		return nil, false, hostos.Unsupported("cwd lookup")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("reading working directories: %w", err)
	}
	return entries, denied, nil
}

func (f Finder) procCwds(ctx context.Context, procs []process.Info) (map[int]cwdEntry, bool) {
	out := make(map[int]cwdEntry, len(procs))
	denied := false
	known := make(map[int]bool, len(procs))
	for _, p := range procs {
		known[p.PID] = true
	}
	for _, pid := range f.procPIDs(procs) {
		if ctx.Err() != nil {
			break
		}
		dir := filepath.Join(f.procfs(), strconv.Itoa(pid))
		d, err := f.fsys().Readlink(filepath.Join(dir, "cwd"))
		switch {
		case err == nil && d != "":
			e := cwdEntry{Cwd: d}
			if !known[pid] {
				e.Command, e.PPID = f.procStat(dir)
			}
			out[pid] = e
		case errors.Is(err, fs.ErrPermission):
			denied = true
		}
	}
	return out, denied
}

// procPIDs is every pid in procs plus any numeric /proc entry, so a process
// the collector missed is still read.
func (f Finder) procPIDs(procs []process.Info) []int {
	seen := make(map[int]bool, len(procs))
	var pids []int
	for _, p := range procs {
		if !seen[p.PID] {
			seen[p.PID] = true
			pids = append(pids, p.PID)
		}
	}
	ents, err := f.fsys().ReadDir(f.procfs())
	if err != nil {
		return pids
	}
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids
}

// procStat reads comm and ppid from /proc/<pid>/stat: "pid (comm) S ppid ...".
func (f Finder) procStat(dir string) (string, int) {
	b, err := f.fsys().ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return "", 0
	}
	s := string(b)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return "", 0
	}
	fields := strings.Fields(s[end+1:])
	ppid := 0
	if len(fields) >= 2 {
		ppid, _ = strconv.Atoi(fields[1])
	}
	return s[open+1 : end], ppid
}

// lsofCwds runs one host-wide `lsof -d cwd`. Unlike a path or port lookup,
// a non-zero exit here means lsof failed part-way, so any partial output is
// discarded rather than read as "these are all the working directories".
func (f Finder) lsofCwds(ctx context.Context) (map[int]cwdEntry, error) {
	res, err := f.runner().Run(ctx, proc.Cmd{Name: "lsof", Args: []string{"-w", "-nP", "-a", "-d", "cwd", "-FpRcn"}})
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("lsof -d cwd exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	cwds := map[int]cwdEntry{}
	for _, r := range parseLsofF(res.Stdout) {
		if r.FD == "cwd" && r.Name != "" && r.PID > 0 {
			cwds[r.PID] = cwdEntry{Cwd: f.pickSpelling(lsofNameSpellings(r.Name)), Command: r.Command, PPID: r.PPID}
		}
	}
	return cwds, nil
}

// pickSpelling prefers the first decoding of an ambiguous lsof name that
// exists on disk.
func (f Finder) pickSpelling(names []string) string {
	if len(names) > 1 {
		for _, n := range names {
			if _, err := f.fsys().Stat(n); err == nil {
				return n
			}
		}
	}
	return names[0]
}

// othersMissing reports whether a process owned by another user has no cwd:
// lsof without root silently skips those.
func othersMissing(procs []process.Info, cwds map[int]cwdEntry, uid int) bool {
	for _, p := range procs {
		if _, ok := cwds[p.PID]; !ok && p.UID != uid && p.PID > 0 {
			return true
		}
	}
	return false
}

func (f Finder) euid() int {
	if f.UID != nil {
		return f.UID()
	}
	return os.Geteuid()
}

func (f Finder) selfPID() int {
	if f.SelfPID != nil {
		return f.SelfPID()
	}
	return os.Getpid()
}

// Under lists every visible process whose working directory is one of dirs
// or below it. Both the directories and the process cwds are compared as
// given and with symlinks resolved; on macOS a cwd that still does not match
// is compared by file identity, so a different letter case on a
// case-insensitive volume matches. Spectra itself and its own children (the
// lsof it runs) are left out. Each dir must be an absolute path to an
// existing directory. An empty process table is an error, never "none".
func (f Finder) Under(ctx context.Context, dirs []string) (UnderResult, error) {
	osk := hostos.Resolve(f.OS)
	if osk != hostos.Darwin && osk != hostos.Linux {
		return UnderResult{}, hostos.Unsupported("owner --under")
	}
	roots, err := f.underRoots(dirs)
	if err != nil {
		return UnderResult{}, err
	}
	procs := f.listProcs(ctx, osk)
	if err := ctx.Err(); err != nil {
		return UnderResult{}, fmt.Errorf("owner --under: %w", err)
	}
	if len(procs) == 0 {
		return UnderResult{}, errors.New("owner --under: the process list is empty (ps or /proc could not be read)")
	}
	f.OS = osk
	entries, denied, err := f.cwdEntries(ctx, procs)
	if err != nil {
		return UnderResult{}, err
	}
	hs := f.underHolders(entries, procs, roots)
	if err := ctx.Err(); err != nil {
		return UnderResult{}, fmt.Errorf("owner --under: %w", err)
	}
	if osk == hostos.Linux {
		f.fillLinuxExe(hs)
	}
	res := UnderResult{Dirs: dirs, Holders: hs, Incomplete: denied}
	switch {
	case denied && osk == hostos.Darwin:
		res.Hint = underDeniedHintDarwin
	case denied:
		res.Hint = underDeniedHintLinux
	}
	return res, nil
}

// underHolders builds holders from the cwd source, not the process table: a
// pid the cwd source saw but the table missed is still reported, with the
// command and parent the cwd source gave.
func (f Finder) underHolders(entries map[int]cwdEntry, procs []process.Info, roots []string) []Holder {
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	self := f.selfPID()
	m := f.NewCwdMatcher()
	var hs, unknown []Holder
	for pid, e := range entries {
		p, known := byPID[pid]
		ppid := e.PPID
		if known {
			ppid = p.PPID
		}
		if pid == self || ppid == self {
			continue
		}
		cwd := strings.TrimSuffix(e.Cwd, deletedSuffix)
		if !m.Under(cwd, roots) {
			continue
		}
		h := Holder{PID: pid, Hold: HoldCwd, Cwd: cwd}
		if !known {
			h.Command, h.Chain = e.Command, parentChain(byPID, ppid)
			unknown = append(unknown, h)
			continue
		}
		hs = append(hs, h)
	}
	enrich(hs, procs)
	hs = append(hs, unknown...)
	sort.Slice(hs, func(i, j int) bool { return hs[i].PID < hs[j].PID })
	return hs
}

// underRoots returns each dir plus its resolved spelling.
func (f Finder) underRoots(dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, errors.New("no directory given")
	}
	var roots []string
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			return nil, fmt.Errorf("directory %q is not absolute", d)
		}
		d = filepath.Clean(d)
		fi, err := f.fsys().Stat(d)
		if err != nil {
			return nil, fmt.Errorf("directory %s: %w", d, err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", d)
		}
		roots = append(roots, d)
		if r, err := f.fsys().EvalSymlinks(d); err == nil && r != d {
			roots = append(roots, r)
		}
	}
	return roots, nil
}

// maxAncestors bounds the identity walk from a cwd towards /.
const maxAncestors = 64

// CwdMatcher decides whether a process working directory is inside a root.
// It is not safe for concurrent use; it caches stat results for one scan.
type CwdMatcher struct {
	fsys     FS
	identity bool
	stats    map[string]os.FileInfo
}

// NewCwdMatcher returns a matcher for f's OS and filesystem. On macOS it
// falls back to comparing file identity (device and inode) of the cwd and
// each of its ancestors against each root, which catches a different letter
// case on a case-insensitive volume and any other spelling lsof may report.
func (f Finder) NewCwdMatcher() *CwdMatcher {
	return &CwdMatcher{fsys: f.fsys(), identity: hostos.Resolve(f.OS) == hostos.Darwin, stats: map[string]os.FileInfo{}}
}

// Under reports whether cwd is one of roots or below one. It matches
// lexically first; only a miss pays for resolving the cwd (the kernel
// normally reports it resolved already) and then for the identity walk.
func (m *CwdMatcher) Under(cwd string, roots []string) bool {
	if cwd == "" {
		return false
	}
	if pathUnderAny(cwd, roots) {
		return true
	}
	if r, err := m.fsys.EvalSymlinks(cwd); err == nil && r != cwd && pathUnderAny(r, roots) {
		return true
	}
	return m.identity && m.sameAncestor(cwd, roots)
}

func (m *CwdMatcher) sameAncestor(cwd string, roots []string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	var rootInfos []os.FileInfo
	for _, r := range roots {
		if fi := m.stat(r); fi != nil {
			rootInfos = append(rootInfos, fi)
		}
	}
	if len(rootInfos) == 0 {
		return false
	}
	p := filepath.Clean(cwd)
	for range maxAncestors {
		if fi := m.stat(p); fi != nil {
			for _, ri := range rootInfos {
				if m.fsys.SameFile(fi, ri) {
					return true
				}
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return false
}

func (m *CwdMatcher) stat(p string) os.FileInfo {
	if fi, ok := m.stats[p]; ok {
		return fi
	}
	fi, err := m.fsys.Stat(p)
	if err != nil {
		fi = nil
	}
	m.stats[p] = fi
	return fi
}

func pathUnderAny(p string, roots []string) bool {
	for _, r := range roots {
		if PathUnder(p, r) {
			return true
		}
	}
	return false
}

// PathUnder reports whether p is root or inside it, by path components.
func PathUnder(p, root string) bool {
	if p == root || root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, root+"/")
}
