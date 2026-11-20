package process

import "golang.org/x/sys/unix"

func sysctlProcArgs(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}
