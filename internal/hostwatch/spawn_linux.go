package hostwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type platformSpawnBackend struct{}

func newSpawnBackend() SpawnBackend { return platformSpawnBackend{} }

func (platformSpawnBackend) List() ([]SpawnProcess, error) {
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
		p, err := readLinuxSpawnProcess(pid)
		if err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func readLinuxSpawnProcess(pid int) (SpawnProcess, error) {
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
	return SpawnProcess{PID: pid, PPID: ppid, UID: uid, Comm: text[left+1 : right]}, nil
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
