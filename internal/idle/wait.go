package idle

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// maxWaitPIDs bounds how many processes wait evidence is read for.
const maxWaitPIDs = 128

// WaitReader reads cheap wait evidence. Linux reads /proc/<pid>/wchan and
// the fd links; macOS runs one `lsof -p` over all pids. Nil fields use the
// real host. Unreadable evidence is omitted, never an error.
type WaitReader struct {
	OS       hostos.Kind
	Runner   proc.Runner
	ProcFS   string
	ReadFile func(string) ([]byte, error)
	ReadDir  func(string) ([]os.DirEntry, error)
	Readlink func(string) (string, error)
}

// Read returns evidence for each pid that has any.
func (w WaitReader) Read(ctx context.Context, pids []int) map[int]*Wait {
	if len(pids) > maxWaitPIDs {
		pids = pids[:maxWaitPIDs]
	}
	if len(pids) == 0 {
		return map[int]*Wait{}
	}
	switch hostos.Resolve(w.OS) {
	case hostos.Linux:
		return w.readLinux(ctx, pids)
	case hostos.Darwin:
		return w.readDarwin(ctx, pids)
	}
	return map[int]*Wait{}
}

func (w WaitReader) procfs() string {
	if w.ProcFS != "" {
		return w.ProcFS
	}
	return "/proc"
}

func (w WaitReader) readLinux(ctx context.Context, pids []int) map[int]*Wait {
	readFile, readDir, readlink := w.ReadFile, w.ReadDir, w.Readlink
	if readFile == nil {
		readFile = os.ReadFile
	}
	if readDir == nil {
		readDir = os.ReadDir
	}
	if readlink == nil {
		readlink = os.Readlink
	}
	out := map[int]*Wait{}
	for _, pid := range pids {
		if ctx.Err() != nil {
			break
		}
		dir := filepath.Join(w.procfs(), strconv.Itoa(pid))
		var wt Wait
		if b, err := readFile(filepath.Join(dir, "wchan")); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" && s != "0" {
				wt.WChan = s
			}
		}
		if fds, err := readDir(filepath.Join(dir, "fd")); err == nil {
			for _, fd := range fds {
				if t, err := readlink(filepath.Join(dir, "fd", fd.Name())); err == nil && IsLockPath(t) {
					wt.LockFiles = append(wt.LockFiles, t)
				}
			}
		}
		addWait(out, pid, wt)
	}
	return out
}

func (w WaitReader) readDarwin(ctx context.Context, pids []int) map[int]*Wait {
	run := w.Runner
	if run == nil {
		run = proc.Default
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	// lsof exits 1 when some pid has gone; what it printed is still good.
	res, err := run.Run(ctx, proc.Cmd{Name: "lsof", Args: []string{"-w", "-nP", "-p", strings.Join(list, ","), "-Fpn"}})
	if err != nil {
		return map[int]*Wait{}
	}
	locks := parseLsofLocks(res.Stdout)
	out := map[int]*Wait{}
	for pid, files := range locks {
		addWait(out, pid, Wait{LockFiles: files})
	}
	return out
}

func parseLsofLocks(b []byte) map[int][]string {
	out := map[int][]string{}
	pid := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 && IsLockPath(line[1:]) {
				out[pid] = append(out[pid], line[1:])
			}
		}
	}
	return out
}

func addWait(out map[int]*Wait, pid int, w Wait) {
	if w.WChan == "" && len(w.LockFiles) == 0 {
		return
	}
	sort.Strings(w.LockFiles)
	w.LockFiles = compactStrings(w.LockFiles)
	out[pid] = &w
}

func compactStrings(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// IsLockPath reports whether an absolute path is named like a lock file
// (foo.lock, foo.lck, LOCK, .lock-x, lockfile).
func IsLockPath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.TrimSuffix(p, deletedSuffix)))
	return strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".lck") ||
		base == "lock" || base == "lockfile" || strings.HasPrefix(base, ".lock") || strings.Contains(base, ".lock.")
}
