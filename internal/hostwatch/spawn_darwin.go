package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/proc"
)

type platformSpawnBackend struct{}

func newSpawnBackend(_ logger.Logger) SpawnBackend { return platformSpawnBackend{} }

func (platformSpawnBackend) List() ([]SpawnProcess, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("kern.proc.all: %w", err)
	}
	out := make([]SpawnProcess, 0, len(rows))
	for _, row := range rows {
		comm := string(row.Proc.P_comm[:])
		comm, _, _ = strings.Cut(comm, "\x00")
		start := time.Unix(row.Proc.P_starttime.Sec, int64(row.Proc.P_starttime.Usec)*1000)
		out = append(out, SpawnProcess{PID: int(row.Proc.P_pid), PPID: int(row.Eproc.Ppid), UID: int(row.Eproc.Pcred.P_ruid), Comm: comm, Start: start})
	}
	return out, nil
}

func (platformSpawnBackend) Argv(pid int) (string, error) {
	bp := procArgsPool.Get().(*[]byte)
	defer procArgsPool.Put(bp)
	raw, err := readProcArgs2(pid, *bp)
	if err != nil {
		return "", fmt.Errorf("kern.procargs2 pid %d: %w", pid, err)
	}
	out, err := joinProcArgs2(raw)
	if err != nil {
		return "", fmt.Errorf("kern.procargs2 pid %d: %w", pid, err)
	}
	return out, nil
}

// kernProcArgs2 is KERN_PROCARGS2 from <sys/sysctl.h>.
const kernProcArgs2 = 49

// procArgsPool reuses kern.procargs2 read buffers. The kernel accepts any
// size up to kern.argmax (1 MiB), so every call would otherwise allocate that
// much; the joined result is a fresh string, so nothing aliases the buffer.
var procArgsPool = sync.Pool{New: func() any {
	n, _ := unix.SysctlUint32("kern.argmax")
	if n == 0 {
		n = 1 << 20
	}
	b := make([]byte, n)
	return &b
}}

// readProcArgs2 reads kern.procargs2 for pid into buf and returns the filled
// prefix. x/sys/unix only exposes an allocating SysctlRaw, hence the direct
// sysctl(2) call.
func readProcArgs2(pid int, buf []byte) ([]byte, error) {
	if len(buf) == 0 {
		return nil, errors.New("empty buffer")
	}
	if pid < 0 || pid > math.MaxInt32 {
		return nil, errors.New("pid out of range")
	}
	mib := [3]int32{unix.CTL_KERN, kernProcArgs2, int32(pid)}
	n := uintptr(len(buf))
	_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 {
		return nil, e
	}
	return buf[:n], nil
}

func (platformSpawnBackend) Limits() (int, int) {
	uid, _ := unix.SysctlUint32("kern.maxprocperuid")
	total, _ := unix.SysctlUint32("kern.maxproc")
	return int(uid), int(total)
}

// Cwds runs one lsof for the given pids. lsof exits 1 when some pids are
// gone or unreadable but still prints the rest.
func (platformSpawnBackend) Cwds(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 {
		return map[int]string{}
	}
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := proc.OS{}.Run(ctx, proc.Cmd{Name: "lsof", Args: []string{"-a", "-d", "cwd", "-p", strings.Join(list, ","), "-Fpn"}})
	if err != nil && len(res.Stdout) == 0 {
		return map[int]string{}
	}
	return hostload.ParseLsofCwd(string(res.Stdout))
}
