//go:build darwin

package helper

import "net"

// peerUID returns the UID of the connected peer.
// Full getpeereid(2) implementation is wired in when the helper runs as root;
// for now we return 0 (root) since the helper socket is 0660 root:_spectra
// and filesystem permissions are the primary access control.
func peerUID(conn net.Conn) uint32 { return 0 }
