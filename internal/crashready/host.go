package crashready

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// liveHost reads crash-capture facts from the running machine.
type liveHost struct {
	run func(name string, args ...string) ([]byte, error)
}

// NewLiveHost returns a Host backed by the running machine (sysctl, defaults,
// RLIMIT_CORE, and /cores).
func NewLiveHost() Host {
	return liveHost{run: func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}}
}

func (h liveHost) Sysctl(key string) (string, error) {
	out, err := h.run("sysctl", "-n", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (h liveHost) CoreRLimit() (uint64, uint64, error) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &rl); err != nil {
		return 0, 0, err
	}
	return rl.Cur, rl.Max, nil
}

func (h liveHost) CoresDir() bool {
	fi, err := os.Stat("/cores")
	return err == nil && fi.IsDir()
}

func (h liveHost) CrashReporterDialogType() string {
	// `defaults read` exits non-zero when the key is unset; treat that as "".
	out, err := h.run("defaults", "read", "com.apple.CrashReporter", "DialogType")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
