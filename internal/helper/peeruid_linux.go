//go:build linux

package helper

import (
	"net"
	"syscall"
)

// peerUID returns the real UID of the process on the other end of a Unix
// socket connection, read from the kernel via SO_PEERCRED. The value is
// authenticated by the kernel and cannot be forged by the client, so it is
// safe to use for per-caller rate limiting, audit, and ownership of
// caller-scoped artifacts.
//
// On any failure it falls back to 0, matching the socket's 0660 root:spectra
// permission model (only root or the spectra group can connect at all).
func peerUID(conn net.Conn) uint32 {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	var ucred *syscall.Ucred
	var credErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		ucred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); ctrlErr != nil || credErr != nil || ucred == nil {
		return 0
	}
	return ucred.Uid
}
