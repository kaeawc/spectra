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

// Cwds reads the working directory of every process in procs. denied is
// true when a process owned by someone else (or, on Linux, any process)
// could not be read. macOS issues one `lsof -d cwd`; Linux readlinks
// /proc/<pid>/cwd and keeps the " (deleted)" suffix of a removed directory.
func (f Finder) Cwds(ctx context.Context, procs []process.Info) (cwds map[int]string, denied bool, err error) {
	osk := hostos.Resolve(f.OS)
	switch osk {
	case hostos.Linux:
		cwds, denied = f.procCwds(ctx, procs)
	case hostos.Darwin:
		cwds, err = f.lsofCwds(ctx)
		if err != nil {
			return nil, false, err
		}
		denied = !f.isRoot() && othersMissing(procs, cwds, f.euid())
	default:
		return nil, false, hostos.Unsupported("cwd lookup")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("reading working directories: %w", err)
	}
	return cwds, denied, nil
}

func (f Finder) procCwds(ctx context.Context, procs []process.Info) (map[int]string, bool) {
	out := make(map[int]string, len(procs))
	denied := false
	for _, p := range procs {
		if ctx.Err() != nil {
			break
		}
		d, err := f.fsys().Readlink(filepath.Join(f.procfs(), strconv.Itoa(p.PID), "cwd"))
		switch {
		case err == nil && d != "":
			out[p.PID] = d
		case errors.Is(err, fs.ErrPermission):
			denied = true
		}
	}
	return out, denied
}

func (f Finder) lsofCwds(ctx context.Context) (map[int]string, error) {
	out, err := f.lsof(ctx, "-w", "-nP", "-a", "-d", "cwd", "-Fpn")
	if err != nil {
		return nil, err
	}
	cwds := map[int]string{}
	for _, r := range parseLsofF(out) {
		if r.FD == "cwd" && r.Name != "" && r.PID > 0 {
			cwds[r.PID] = r.Name
		}
	}
	return cwds, nil
}

// othersMissing reports whether a process owned by another user has no cwd:
// lsof without root silently skips those.
func othersMissing(procs []process.Info, cwds map[int]string, uid int) bool {
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
// given and with symlinks resolved. Spectra itself and its own children
// (the lsof it runs) are left out. Each dir must be an absolute path to an
// existing directory.
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
	cwds, denied, err := f.Cwds(ctx, procs)
	if err != nil {
		return UnderResult{}, err
	}
	self := f.selfPID()
	var hs []Holder
	for _, p := range procs {
		if p.PID == self || p.PPID == self {
			continue
		}
		raw, ok := cwds[p.PID]
		if !ok {
			continue
		}
		cwd := strings.TrimSuffix(raw, deletedSuffix)
		if !f.cwdUnder(cwd, roots) {
			continue
		}
		hs = append(hs, Holder{PID: p.PID, Hold: HoldCwd, Cwd: cwd})
	}
	if err := ctx.Err(); err != nil {
		return UnderResult{}, fmt.Errorf("owner --under: %w", err)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].PID < hs[j].PID })
	enrich(hs, procs)
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

// cwdUnder matches lexically first; only a miss pays for resolving the cwd,
// which the kernel normally reports resolved already.
func (f Finder) cwdUnder(cwd string, roots []string) bool {
	if pathUnderAny(cwd, roots) {
		return true
	}
	r, err := f.fsys().EvalSymlinks(cwd)
	return err == nil && r != cwd && pathUnderAny(r, roots)
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
