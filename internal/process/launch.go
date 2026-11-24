package process

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// Argv returns pid's argument vector, read from the kernel without
// touching the process: /proc/<pid>/cmdline on Linux, kern.procargs2 on
// macOS.
func Argv(pid int) ([]string, error) {
	return hostArgv(hostos.Current(), "/proc", pid)
}

// CwdReader reads another process's working directory. The zero value uses
// the host OS, ExecRunner and os.Readlink.
type CwdReader struct {
	OS       hostos.Kind
	Runner   CommandRunner
	Readlink func(string) (string, error)
}

const cwdTimeout = 2 * time.Second

// Cwd returns pid's working directory: the /proc/<pid>/cwd link on Linux,
// one single-pid lsof on macOS.
func (c CwdReader) Cwd(ctx context.Context, pid int) (string, error) {
	switch hostos.Resolve(c.OS) {
	case hostos.Linux:
		readlink := c.Readlink
		if readlink == nil {
			readlink = os.Readlink
		}
		d, err := readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
		if err != nil {
			return "", fmt.Errorf("cwd pid %d: %w", pid, err)
		}
		return d, nil
	case hostos.Darwin:
		run := c.Runner
		if run == nil {
			run = ExecRunner{}
		}
		ctx, cancel := context.WithTimeout(ctx, cwdTimeout)
		defer cancel()
		out, err := run.Run(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
		if err != nil && len(out) == 0 {
			return "", fmt.Errorf("cwd pid %d: %w", pid, err)
		}
		return parseLsofName(string(out))
	default:
		return "", hostos.Unsupported("process working directory")
	}
}

// parseLsofName returns the first "n<path>" field of lsof -F output.
func parseLsofName(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "n") && len(line) > 1 {
			return line[1:], nil
		}
	}
	return "", fmt.Errorf("lsof: no cwd in output")
}
