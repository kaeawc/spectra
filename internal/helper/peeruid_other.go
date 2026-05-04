//go:build !darwin

package helper

import "net"

func peerUID(conn net.Conn) uint32 { return 0 }
