package process

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
)

// Bounds on one process's argv. Both are well above any interpreter command
// line the classifier needs; past them the flattened command line is used.
const (
	maxArgvBytes = 1 << 20 // kern.argmax on macOS; Linux reads stop here
	maxArgvCount = 4096
)

// argvFor reads pid's argv and returns it only when it still matches full,
// the command line from the listing; nil otherwise. Injectable for tests.
var argvFor = func(pid int, full string) []string {
	argv, err := hostArgv(hostos.Current(), "/proc", pid)
	if err != nil || !argvMatches(argv, full) {
		return nil
	}
	return argv
}

func hostArgv(kind hostos.Kind, procfs string, pid int) ([]string, error) {
	switch kind {
	case hostos.Linux:
		data, err := readBounded(filepath.Join(procfs, strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return nil, err
		}
		return splitCmdline(data), nil
	case hostos.Darwin:
		raw, err := sysctlProcArgs(pid)
		if err != nil {
			return nil, fmt.Errorf("kern.procargs2 pid %d: %w", pid, err)
		}
		return parseProcArgs2(raw)
	default:
		return nil, hostos.Unsupported("process argv")
	}
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxArgvBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArgvBytes {
		return nil, errors.New("cmdline exceeds argv bound")
	}
	return data, nil
}

// argvMatches reports whether argv is the process the listing saw. The
// listing's command line is argv joined by spaces (whitespace runs collapsed
// on macOS), so anything else is a recycled pid or a rewritten argv.
func argvMatches(argv []string, full string) bool {
	if len(argv) == 0 {
		return false
	}
	return strings.Join(strings.Fields(strings.Join(argv, " ")), " ") == strings.Join(strings.Fields(full), " ")
}

// splitCmdline splits /proc/<pid>/cmdline: NUL-separated, trailing NUL.
func splitCmdline(data []byte) []string {
	s := strings.TrimRight(string(data), "\x00")
	if s == "" {
		return nil
	}
	args := strings.Split(s, "\x00")
	if len(args) > maxArgvCount {
		return nil
	}
	return args
}

// parseProcArgs2 decodes kern.procargs2: a native-endian int32 argc, the
// exec path, NUL padding, then argc NUL-terminated arguments (environment
// strings follow and are not read).
func parseProcArgs2(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, errors.New("procargs2: short response")
	}
	n := binary.NativeEndian.Uint32(raw[:4])
	if n == 0 {
		return nil, nil
	}
	if n > maxArgvCount {
		return nil, fmt.Errorf("procargs2: argc %d exceeds bound", n)
	}
	argc := int(n)
	b := raw[4:]
	end := indexNUL(b)
	if end < 0 {
		return nil, errors.New("procargs2: missing executable terminator")
	}
	b = b[end+1:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc {
		end = indexNUL(b)
		if end < 0 {
			return nil, errors.New("procargs2: truncated argument")
		}
		args = append(args, string(b[:end]))
		b = b[end+1:]
	}
	return args, nil
}

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}
