package hostwatch

import (
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

type platformSpawnBackend struct{}

func newSpawnBackend() SpawnBackend { return platformSpawnBackend{} }

func (platformSpawnBackend) List() ([]SpawnProcess, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("kern.proc.all: %w", err)
	}
	out := make([]SpawnProcess, 0, len(rows))
	for _, row := range rows {
		comm := string(row.Proc.P_comm[:])
		comm, _, _ = strings.Cut(comm, "\x00")
		out = append(out, SpawnProcess{PID: int(row.Proc.P_pid), PPID: int(row.Eproc.Ppid), UID: int(row.Eproc.Pcred.P_ruid), Comm: comm})
	}
	return out, nil
}

func (platformSpawnBackend) Argv(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", fmt.Errorf("kern.procargs2 pid %d: %w", pid, err)
	}
	if len(raw) < 4 {
		return "", fmt.Errorf("kern.procargs2 pid %d: short response", pid)
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc <= 0 {
		return "", nil
	}
	b := raw[4:]
	end := strings.IndexByte(string(b), 0)
	if end < 0 {
		return "", fmt.Errorf("kern.procargs2 pid %d: missing executable terminator", pid)
	}
	b = b[end+1:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	args := make([]string, 0, min(argc, 32))
	for len(b) > 0 && len(args) < argc {
		end = strings.IndexByte(string(b), 0)
		if end < 0 {
			break
		}
		args = append(args, string(b[:end]))
		b = b[end+1:]
	}
	return strings.Join(args, " "), nil
}

func (platformSpawnBackend) Limits() (int, int) {
	uid, _ := unix.SysctlUint32("kern.maxprocperuid")
	total, _ := unix.SysctlUint32("kern.maxproc")
	return int(uid), int(total)
}
