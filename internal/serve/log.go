package serve

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/hostos"
)

// DefaultLogPath returns the canonical daemon JSONL log path: the macOS
// unified log convention (~/Library/Logs/Spectra) on Darwin, and the XDG
// state directory ($XDG_STATE_HOME or ~/.local/state) on Linux.
func DefaultLogPath() (string, error) {
	if hostos.Current() == hostos.Linux {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, "spectra", "daemon.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "Spectra", "daemon.jsonl"), nil
}

// OpenLogFile creates parent directories and opens path for append-only daemon
// logging. Callers own the returned file.
func OpenLogFile(path string) (*os.File, error) {
	if path == "" {
		var err error
		path, err = DefaultLogPath()
		if err != nil {
			return nil, fmt.Errorf("resolve daemon log path: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon log file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("set daemon log file mode: %w", err)
	}
	return f, nil
}
