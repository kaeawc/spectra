package hostwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// platformSpawnBackend reads btime once: it can drift by about a second
// between reads, which would shift every start time and split lifetimes.
type platformSpawnBackend struct {
	btimeOnce *sync.Once
	btime     *time.Time
}

func newSpawnBackend() SpawnBackend {
	return platformSpawnBackend{btimeOnce: &sync.Once{}, btime: &time.Time{}}
}

func (b platformSpawnBackend) bootTime() time.Time {
	b.btimeOnce.Do(func() {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "btime "); ok {
				if secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
					*b.btime = time.Unix(secs, 0)
				}
				break
			}
		}
	})
	return *b.btime
}

func (b platformSpawnBackend) List() ([]SpawnProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	out := make([]SpawnProcess, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		p, err := readLinuxSpawnProcess(pid, b.bootTime())
		if err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func readLinuxSpawnProcess(pid int, btime time.Time) (SpawnProcess, error) {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return SpawnProcess{}, fmt.Errorf("read stat pid %d: %w", pid, err)
	}
	text := string(stat)
	left, right := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if left < 0 || right <= left {
		return SpawnProcess{}, fmt.Errorf("invalid stat pid %d", pid)
	}
	fields := strings.Fields(text[right+1:])
	if len(fields) < 2 {
		return SpawnProcess{}, fmt.Errorf("short stat pid %d", pid)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return SpawnProcess{}, fmt.Errorf("ppid pid %d: %w", pid, err)
	}
	status, err := os.ReadFile(filepath.Join(base, "status"))
	if err != nil {
		return SpawnProcess{}, fmt.Errorf("read status pid %d: %w", pid, err)
	}
	uid := -1
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			values := strings.Fields(line)
			if len(values) > 1 {
				uid, err = strconv.Atoi(values[1])
			}
			break
		}
	}
	if uid < 0 || err != nil {
		return SpawnProcess{}, fmt.Errorf("invalid uid pid %d", pid)
	}
	return SpawnProcess{PID: pid, PPID: ppid, UID: uid, Comm: text[left+1 : right], Start: linuxStart(fields, btime)}, nil
}

// Cwds readlinks /proc/<pid>/cwd, keeping a " (deleted)" suffix.
func (platformSpawnBackend) Cwds(_ context.Context, pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	for _, pid := range pids {
		if d, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd")); err == nil && d != "" {
			out[pid] = d
		}
	}
	return out
}

func (platformSpawnBackend) Argv(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", fmt.Errorf("cmdline pid %d: %w", pid, err)
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")), nil
}

func (platformSpawnBackend) Limits() (int, int) {
	var limit unix.Rlimit
	uid := 0
	if unix.Getrlimit(unix.RLIMIT_NPROC, &limit) == nil {
		uid = finiteLimit(limit.Cur)
	}
	total := 0
	if b, err := os.ReadFile("/proc/sys/kernel/threads-max"); err == nil {
		total, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	return uid, total
}
