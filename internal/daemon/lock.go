package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrAlreadyRunning = errors.New("daemon: already running")

// Probe checks the daemon lock and removes stale PID and socket files while holding it.
func Probe(paths Paths) (bool, int, error) {
	f, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("daemon: open lock: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		if !lockHeld(err) {
			return false, 0, fmt.Errorf("daemon: probe lock: %w", err)
		}
		pid, err := readLockedPID(paths.PID)
		return true, pid, err
	}
	defer unlockFile(f)
	return false, 0, cleanupUnlocked(paths)
}

func readLockedPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("daemon: read locked pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("daemon: invalid locked pid file")
	}
	return pid, nil
}

func cleanupUnlocked(paths Paths) error {
	info, err := os.Lstat(paths.Socket)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("daemon: inspect stale socket: %w", err)
	}
	if err == nil && info.Mode()&os.ModeSocket != 0 {
		conn, dialErr := net.DialTimeout("unix", paths.Socket, 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return ErrAlreadyRunning
		}
		if err := os.Remove(paths.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("daemon: remove stale socket: %w", err)
		}
	}
	if err := os.Remove(paths.PID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("daemon: remove stale pid: %w", err)
	}
	return nil
}

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
