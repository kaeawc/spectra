package helper

import (
	"net"

	"github.com/kaeawc/spectra/internal/peercred"
)

func peerUID(conn net.Conn) (uint32, error) { return peercred.PeerUID(conn) }
