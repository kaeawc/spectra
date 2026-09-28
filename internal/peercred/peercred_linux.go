//go:build linux

package peercred

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: syscall conn: %w", err)
	}
	var uid uint32
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil {
			sockErr = e
			return
		}
		uid = cred.Uid
	})
	if err != nil {
		return 0, fmt.Errorf("peercred: control: %w", err)
	}
	if sockErr != nil {
		return 0, fmt.Errorf("peercred: get credentials: %w", sockErr)
	}
	return uid, nil
}
