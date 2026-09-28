package peercred

import (
	"errors"
	"fmt"
	"net"
)

var ErrUnsupported = errors.New("peercred: unsupported platform")

// PeerUID returns the kernel-authenticated UID of a Unix socket peer.
func PeerUID(c net.Conn) (uint32, error) {
	u, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peercred: expected unix connection: %w", ErrUnsupported)
	}
	return peerUID(u)
}
