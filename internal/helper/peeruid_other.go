//go:build !darwin

package helper

import "net"

func peerUID(_ net.Conn) uint32 { return 0 }
