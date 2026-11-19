package owner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const maxChain = 64

// Finder resolves owner queries. Zero-value fields use the real host.
type Finder struct {
	OS     hostos.Kind
	Runner proc.Runner
	FS     FS
	ProcFS string // default "/proc"
	IsRoot func() bool
	Now    func() time.Time
	// Procs lists running processes; default is process.CollectAll.
	Procs func(ctx context.Context) []process.Info
}

// Find answers the query for t.
func (f Finder) Find(ctx context.Context, t Target) (Result, error) {
	osk := hostos.Resolve(f.OS)
	if osk != hostos.Darwin && osk != hostos.Linux {
		return Result{}, hostos.Unsupported("owner lookup")
	}
	procs := f.listProcs(ctx, osk)
	res := Result{Target: t}
	var err error
	switch t.Kind {
	case KindPID:
		res.Holders = pidHolders(procs, t.PID)
	case KindPort:
		res.Holders, res.Incomplete, err = f.portHolders(ctx, osk, t)
	case KindPath:
		res.Holders, res.Incomplete, err = f.pathHolders(ctx, osk, t)
	default:
		err = fmt.Errorf("unknown target kind %q", t.Kind)
	}
	if err != nil {
		return Result{}, err
	}
	if res.Incomplete && f.isRoot() {
		res.Incomplete = false
	}
	if res.Incomplete {
		res.Hint = rootHint
	}
	res.Holders = dedupe(res.Holders)
	enrich(res.Holders, procs)
	if osk == hostos.Linux {
		f.fillLinuxExe(res.Holders)
	}
	return res, nil
}

// fillLinuxExe covers the Linux process collector, which leaves
// ExecutablePath empty. An unreadable link leaves the field empty.
func (f Finder) fillLinuxExe(hs []Holder) {
	for i := range hs {
		if hs[i].Executable != "" {
			continue
		}
		if t, err := f.fsys().Readlink(filepath.Join(f.procfs(), strconv.Itoa(hs[i].PID), "exe")); err == nil {
			hs[i].Executable = strings.TrimSuffix(t, " (deleted)")
		}
	}
}

func (f Finder) isRoot() bool {
	if f.IsRoot != nil {
		return f.IsRoot()
	}
	return os.Geteuid() == 0
}

func (f Finder) procfs() string {
	if f.ProcFS != "" {
		return f.ProcFS
	}
	return "/proc"
}

func (f Finder) runner() proc.Runner {
	if f.Runner != nil {
		return f.Runner
	}
	return proc.Default
}

func (f Finder) fsys() FS {
	if f.FS != nil {
		return f.FS
	}
	return OSFS{}
}

func (f Finder) listProcs(ctx context.Context, osk hostos.Kind) []process.Info {
	if f.Procs != nil {
		return f.Procs(ctx)
	}
	return process.CollectAll(ctx, process.CollectOptions{OS: osk, ProcFS: f.procfs(), Runner: f.runner(), Now: f.Now})
}

// portHolders returns holders of a local port and whether the answer may be
// missing processes the caller is not allowed to see.
func (f Finder) portHolders(ctx context.Context, osk hostos.Kind, t Target) ([]Holder, bool, error) {
	protos := []string{t.Proto}
	if t.Proto == "" {
		protos = []string{"tcp", "udp"}
	}
	var all []Holder
	hidden := osk == hostos.Darwin
	for _, p := range protos {
		var hs []Holder
		var err error
		if osk == hostos.Darwin {
			hs, err = f.lsofPort(ctx, p, t.Port)
		} else {
			var h bool
			hs, h, err = f.ssPort(ctx, p, t.Port)
			hidden = hidden || h
		}
		if err != nil {
			return nil, false, err
		}
		all = append(all, hs...)
	}
	return all, hidden, nil
}

func (f Finder) lsofPort(ctx context.Context, proto string, port int) ([]Holder, error) {
	out, err := f.lsof(ctx, "-nP", "-i"+upper(proto)+":"+strconv.Itoa(port), "-F", lsofPortFields)
	if err != nil {
		return nil, err
	}
	return lsofPortHolders(parseLsofF(out), port), nil
}

func upper(proto string) string {
	if proto == "udp" {
		return "UDP"
	}
	return "TCP"
}

// lsof exits 1 with empty output when nothing matches; only a failure that
// also wrote to stderr is an error.
func (f Finder) lsof(ctx context.Context, args ...string) ([]byte, error) {
	res, err := f.runner().Run(ctx, proc.Cmd{Name: "lsof", Args: args})
	if err != nil {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	if res.ExitCode != 0 && len(res.Stdout) == 0 && len(res.Stderr) > 0 {
		return nil, fmt.Errorf("lsof exited %d: %s", res.ExitCode, string(res.Stderr))
	}
	return res.Stdout, nil
}

func (f Finder) ssPort(ctx context.Context, proto string, port int) ([]Holder, bool, error) {
	var all []Holder
	hidden := false
	for _, args := range [][]string{{"-H", "-lntup"}, {"-H", "-ntup"}} {
		res, err := f.runner().Run(ctx, proc.Cmd{Name: "ss", Args: args})
		if err != nil {
			return nil, false, fmt.Errorf("ss: %w", err)
		}
		if res.ExitCode != 0 {
			return nil, false, fmt.Errorf("ss exited %d: %s", res.ExitCode, string(res.Stderr))
		}
		hs, h := parseSS(res.Stdout, proto, port)
		all = append(all, hs...)
		hidden = hidden || h
	}
	return all, hidden, nil
}

func (f Finder) pathHolders(ctx context.Context, osk hostos.Kind, t Target) ([]Holder, bool, error) {
	fsys := f.fsys()
	if _, err := fsys.Stat(t.Path); errors.Is(err, fs.ErrNotExist) {
		return nil, false, fmt.Errorf("no such file or directory: %s", t.Path)
	}
	// The kernel and lsof report resolved names (/tmp -> /private/tmp,
	// /var/run -> /run), so match against both spellings.
	paths := []string{t.Path}
	if r, err := fsys.EvalSymlinks(t.Path); err == nil && r != t.Path {
		paths = append(paths, r)
	}
	if osk == hostos.Darwin {
		out, err := f.lsof(ctx, "-nP", "-F", lsofPathFields, "--", t.Path)
		if err != nil {
			return nil, false, err
		}
		return lsofPathHolders(parseLsofF(out), paths), true, nil
	}
	hs, denied := procPathHolders(fsys, f.procfs(), paths)
	return hs, denied, nil
}

func pidHolders(procs []process.Info, pid int) []Holder {
	for _, p := range procs {
		if p.PID == pid {
			return []Holder{{PID: pid, Hold: HoldProcess}}
		}
	}
	return nil
}

func dedupe(hs []Holder) []Holder {
	type key struct {
		pid                int
		hold, mode, detail string
	}
	seen := map[key]bool{}
	var out []Holder
	for _, h := range hs {
		k := key{h.PID, h.Hold, h.Mode, h.Detail}
		if !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return out
}

// enrich fills user, executable, start time and the parent chain from the
// process table, keeping fields the finding tool already reported.
func enrich(hs []Holder, procs []process.Info) {
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	for i := range hs {
		p, ok := byPID[hs[i].PID]
		if !ok {
			continue
		}
		if hs[i].User == "" {
			hs[i].User = p.User
		}
		if hs[i].Command == "" {
			hs[i].Command = p.Command
		}
		hs[i].Executable = p.ExecutablePath
		hs[i].Started = p.StartTime
		hs[i].Chain = parentChain(byPID, p.PPID)
	}
}

func parentChain(byPID map[int]process.Info, ppid int) []Link {
	var chain []Link
	seen := map[int]bool{}
	for pid := ppid; pid > 0 && !seen[pid] && len(chain) < maxChain; {
		seen[pid] = true
		p, ok := byPID[pid]
		if !ok {
			break
		}
		chain = append(chain, Link{PID: pid, Command: p.Command})
		pid = p.PPID
	}
	return chain
}
