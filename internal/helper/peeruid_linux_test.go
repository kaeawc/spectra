//go:build linux

package helper

import (
	"net"
	"os"
	"testing"
)

// TestPeerUIDReadsSocketpairCredentials verifies SO_PEERCRED returns the
// real UID of the connected peer. Both ends are this process, so the
// expected UID is the test process's own. Runs only on Linux.
func TestPeerUIDReadsSocketpairCredentials(t *testing.T) {
	// Real Unix socket via a listener/dialer; both ends are this process.
	dir := t.TempDir()
	sock := dir + "/peer.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type res struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan res, 1)
	go func() {
		conn, err := ln.Accept()
		accepted <- res{conn, err}
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	a := <-accepted
	if a.err != nil {
		t.Fatalf("accept: %v", a.err)
	}
	defer a.conn.Close()

	got := peerUID(a.conn)
	if got != uint32(os.Getuid()) {
		t.Fatalf("peerUID = %d, want %d (own uid)", got, os.Getuid())
	}
}
