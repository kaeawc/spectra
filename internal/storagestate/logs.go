package storagestate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// LogFile is one log-shaped file discovered on disk. Owner is best-effort
// attribution to the macOS app bundle the file lives under (e.g. "Slack");
// it is empty for files whose path doesn't match a recognizable owner.
type LogFile struct {
	Path      string    `json:"path"`
	Owner     string    `json:"owner,omitempty"`
	SizeBytes int64     `json:"size_bytes"`
	ModTime   time.Time `json:"mtime"`
}

// CollectLogFiles walks well-known macOS log directories and returns each
// log-shaped regular file. A file is "log-shaped" if it sits under a /Logs/
// directory or has a .log suffix.
//
// Roots scanned:
//   - $HOME/Library/Logs
//   - $HOME/Library/Application Support/<app>/Logs (1 level deep)
//   - /Library/Logs
//   - /var/log
//
// Heavy directories (Application Support) are walked one level deep to
// keep cost bounded; per-app subtree scanning belongs in the demand-driven
// inspect_app path, not the default snapshot.
func CollectLogFiles(home string, osKind hostos.Kind) []LogFile {
	w := newLogWalker(maxLogWalkWorkers)
	for _, root := range logRoots(hostos.Resolve(osKind), home) {
		w.root(root, "")
	}
	if hostos.Resolve(osKind) != hostos.Linux {
		w.appSupport(filepath.Join(home, "Library", "Application Support"))
	}
	out := w.wait()
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes > out[j].SizeBytes
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// logRoots returns the log directories to scan for the given OS.
func logRoots(osKind hostos.Kind, home string) []string {
	if osKind == hostos.Linux {
		stateDir := filepath.Join(home, ".local", "state")
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			stateDir = xdg
		}
		return []string{"/var/log", stateDir}
	}
	return []string{
		filepath.Join(home, "Library", "Logs"),
		"/Library/Logs",
		"/var/log",
	}
}

// maxLogWalkWorkers bounds concurrent directory reads. ~/Library/Logs can
// hold tens of thousands of entries (CoreSimulator device logs), and a
// serial walk of it dominated a cached `spectra storage` run.
const maxLogWalkWorkers = 16

// logWalker scans log roots concurrently. Directories are read by up to
// a bounded number of goroutines; when all are busy the caller reads the
// directory inline, so recursion never blocks on the semaphore. Result
// order is unspecified (CollectLogFiles sorts). Symlinks are not followed
// and unreadable entries are skipped silently.
type logWalker struct {
	sem chan struct{}
	wg  sync.WaitGroup
	mu  sync.Mutex
	out []LogFile
}

func newLogWalker(workers int) *logWalker {
	return &logWalker{sem: make(chan struct{}, max(workers, 1))}
}

// root scans one log root; owner, when non-empty, is attached to every
// result (for Application Support attribution).
func (w *logWalker) root(root, owner string) {
	fi, err := os.Lstat(root)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		if isLogShapedFile(root) {
			w.add(logFileFor(root, owner, fi))
		}
		return
	}
	w.dir(root, owner)
}

// appSupport scans ~/Library/Application Support/<app>/Logs for each
// top-level app directory. Other subdirectories are ignored to keep cost low.
func (w *logWalker) appSupport(appSupport string) {
	entries, err := os.ReadDir(appSupport)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			w.root(filepath.Join(appSupport, e.Name(), "Logs"), e.Name())
		}
	}
}

// dir reads path on a free worker, or inline when none is free.
func (w *logWalker) dir(path, owner string) {
	select {
	case w.sem <- struct{}{}:
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			defer func() { <-w.sem }()
			w.readDir(path, owner)
		}()
	default:
		w.readDir(path, owner)
	}
}

func (w *logWalker) readDir(path, owner string) {
	entries, _ := os.ReadDir(path) // partial entries on error are still walked
	var found []LogFile
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		if e.IsDir() {
			w.dir(full, owner)
			continue
		}
		if !isLogShapedFile(full) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, logFileFor(full, owner, fi))
	}
	w.add(found...)
}

func (w *logWalker) add(files ...LogFile) {
	if len(files) == 0 {
		return
	}
	w.mu.Lock()
	w.out = append(w.out, files...)
	w.mu.Unlock()
}

func (w *logWalker) wait() []LogFile {
	w.wg.Wait()
	return w.out
}

func logFileFor(path, owner string, fi os.FileInfo) LogFile {
	return LogFile{
		Path:      path,
		Owner:     owner,
		SizeBytes: diskBytes(fi),
		ModTime:   fi.ModTime(),
	}
}

// isLogShapedFile classifies a file path as log-like. Same predicate as the
// process-deep heuristic so output is consistent across the two paths.
func isLogShapedFile(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasSuffix(path, ".log") {
		return true
	}
	if strings.Contains(path, "/Logs/") {
		return true
	}
	return false
}
