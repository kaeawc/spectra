package storagestate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
func CollectLogFiles(home string) []LogFile {
	var out []LogFile
	roots := []string{
		filepath.Join(home, "Library", "Logs"),
		"/Library/Logs",
		"/var/log",
	}
	for _, root := range roots {
		out = append(out, walkLogs(root, "")...)
	}
	out = append(out, walkAppSupportLogs(filepath.Join(home, "Library", "Application Support"))...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes > out[j].SizeBytes
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// walkLogs recursively scans a single log root, returning log-shaped files.
// owner, when non-empty, is attached to every result (for Application
// Support attribution). Symlinks are not followed.
func walkLogs(root, owner string) []LogFile {
	var out []LogFile
	filepath.Walk(root, func(path string, fi os.FileInfo, err error) error { //nolint:errcheck
		if err != nil {
			return nil // tolerate permission errors silently
		}
		if fi == nil || fi.IsDir() {
			return nil
		}
		if !isLogShapedFile(path) {
			return nil
		}
		out = append(out, LogFile{
			Path:      path,
			Owner:     owner,
			SizeBytes: diskBytes(fi),
			ModTime:   fi.ModTime(),
		})
		return nil
	})
	return out
}

// walkAppSupportLogs scans ~/Library/Application Support/<app>/Logs for each
// top-level app directory. Other subdirectories are ignored to keep cost low.
func walkAppSupportLogs(appSupport string) []LogFile {
	entries, err := os.ReadDir(appSupport)
	if err != nil {
		return nil
	}
	var out []LogFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		owner := e.Name()
		logsDir := filepath.Join(appSupport, owner, "Logs")
		out = append(out, walkLogs(logsDir, owner)...)
	}
	return out
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
