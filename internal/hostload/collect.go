package hostload

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/proc"
)

const commandTimeout = 10 * time.Second

// Host is the seam over everything the collectors read from the machine.
// Nil fields fall back to the real host.
type Host struct {
	OS       hostos.Kind
	Runner   proc.Runner
	ReadFile func(string) ([]byte, error)
	Readlink func(string) (string, error)
	NumCPU   func() int
	// CgroupPID is whose cgroup Cgroup reads; 0 is this process. The daemon
	// sets it to its caller, which can sit in a different cgroup.
	CgroupPID int
}

func (h Host) os() hostos.Kind { return hostos.Resolve(h.OS) }

func (h Host) runner() proc.Runner {
	if h.Runner != nil {
		return h.Runner
	}
	return proc.OS{}
}

func (h Host) readFile(p string) ([]byte, error) {
	if h.ReadFile != nil {
		return h.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (h Host) readlink(p string) (string, error) {
	if h.Readlink != nil {
		return h.Readlink(p)
	}
	return os.Readlink(p)
}

// CPUs returns the logical CPU count.
func (h Host) CPUs() int {
	if h.NumCPU != nil {
		return h.NumCPU()
	}
	return runtime.NumCPU()
}

// Load1 returns the 1-minute load average.
func (h Host) Load1(ctx context.Context) (float64, error) {
	if h.os() == hostos.Linux {
		data, err := h.readFile("/proc/loadavg")
		if err != nil {
			return 0, fmt.Errorf("read /proc/loadavg: %w", err)
		}
		return ParseProcLoadavg(string(data))
	}
	out, err := h.run(ctx, "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, err
	}
	return ParseSysctlLoadavg(string(out))
}

// Available returns memory available to a new job in bytes.
func (h Host) Available(ctx context.Context) (uint64, error) {
	if h.os() == hostos.Linux {
		data, err := h.readFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("read /proc/meminfo: %w", err)
		}
		return ParseMemAvailable(string(data))
	}
	out, err := h.run(ctx, "vm_stat")
	if err != nil {
		return 0, err
	}
	return ParseVMStatAvailable(string(out))
}

// Cwds returns the working directory of as many of pids as are readable.
// macOS issues one lsof for the whole host; Linux readlinks /proc/<pid>/cwd.
func (h Host) Cwds(ctx context.Context, pids []int) map[int]string {
	out := h.CwdsRaw(ctx, pids)
	for pid, d := range out {
		out[pid] = strings.TrimSuffix(d, DeletedSuffix)
	}
	return out
}

// DeletedSuffix is what Linux appends to the cwd link of a removed directory.
const DeletedSuffix = " (deleted)"

// CwdsRaw is Cwds except that a Linux cwd link keeps its DeletedSuffix, so a
// caller can tell a removed directory from a recreated one at the same path.
func (h Host) CwdsRaw(ctx context.Context, pids []int) map[int]string {
	if h.os() == hostos.Linux {
		out := make(map[int]string, len(pids))
		for _, pid := range pids {
			if d, err := h.readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil && d != "" {
				out[pid] = d
			}
		}
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	// lsof exits 1 when some pids are unreadable but still prints the rest.
	res, err := h.runner().Run(ctx, proc.Cmd{Name: "lsof", Args: []string{"-a", "-d", "cwd", "-Fpn"}})
	if err != nil && len(res.Stdout) == 0 {
		return map[int]string{}
	}
	return ParseLsofCwd(string(res.Stdout))
}

func (h Host) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := proc.Output(ctx, h.runner(), name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// ParseProcLoadavg reads the first field of /proc/loadavg.
func ParseProcLoadavg(s string) (float64, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, errors.New("empty loadavg")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse loadavg %q: %w", f[0], err)
	}
	return v, nil
}

// ParseSysctlLoadavg reads the first number of `sysctl -n vm.loadavg`
// output such as "{ 1.23 4.56 7.89 }".
func ParseSysctlLoadavg(s string) (float64, error) {
	f := strings.Fields(strings.Trim(strings.TrimSpace(s), "{}"))
	if len(f) == 0 {
		return 0, errors.New("empty vm.loadavg")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse vm.loadavg %q: %w", f[0], err)
	}
	return v, nil
}

// ParseMemAvailable returns MemAvailable from /proc/meminfo in bytes.
func ParseMemAvailable(s string) (uint64, error) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			break
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemAvailable %q: %w", f[0], err)
		}
		return kb * 1024, nil
	}
	return 0, errors.New("MemAvailable not found in /proc/meminfo")
}

var pageSizeRE = regexp.MustCompile(`page size of (\d+) bytes`)

// ParseVMStatAvailable returns free + inactive + speculative bytes from
// vm_stat output, taking the page size from its header line.
func ParseVMStatAvailable(s string) (uint64, error) {
	m := pageSizeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("vm_stat header has no page size")
	}
	page, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil || page == 0 {
		return 0, fmt.Errorf("bad vm_stat page size %q", m[1])
	}
	ms, err := memstate.ParseVMStat(strings.NewReader(s), page, 0)
	if err != nil {
		return 0, err
	}
	return ms.Free + ms.Inactive + ms.Speculative, nil
}

// ParseLsofCwd parses `lsof -a -d cwd -Fpn` output: "p<pid>" lines each
// followed by "n<path>". Entries without a path are dropped.
func ParseLsofCwd(s string) map[int]string {
	out := map[int]string{}
	pid := 0
	for _, line := range strings.Split(s, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(line[1:])
			if err != nil {
				pid = 0
				continue
			}
			pid = n
		case 'n':
			if pid > 0 {
				if _, seen := out[pid]; !seen {
					out[pid] = line[1:]
				}
			}
		}
	}
	return out
}
