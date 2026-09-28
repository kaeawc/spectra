//go:build !darwin && !linux

package peercred

import "net"

func peerUID(_ *net.UnixConn) (uint32, error) { return 0, ErrUnsupported }
