package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var ErrAlreadyRunning = errors.New("daemon: already running")

func acquireLock(path, pidPath string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
				return nil, fmt.Errorf("%w (pid %d): %w", ErrAlreadyRunning, pid, err)
			}
		}
		return nil, fmt.Errorf("%w: %w", ErrAlreadyRunning, err)
	}
	return f, nil
}
