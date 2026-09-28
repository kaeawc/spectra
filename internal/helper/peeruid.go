package helper

import (
	"net"

	"github.com/kaeawc/spectra/internal/peercred"
)

func peerUID(conn net.Conn) uint32 {
	uid, err := peercred.PeerUID(conn)
	if err != nil {
		return 0
	}
	return uid
}
