//go:build darwin || linux

package peercred

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUID(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", filepath.Join(dir, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	uid, err := PeerUID(server)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Getuid()) {
		t.Fatalf("uid = %d, want %d", uid, os.Getuid())
	}
}
