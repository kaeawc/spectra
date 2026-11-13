package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// shortSocketDir keeps Unix socket paths under the ~104-byte macOS limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDispatchArgsNeverServesForNonEmptyArgs(t *testing.T) {
	cases := []struct {
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{args: []string{"--help"}, wantCode: 0, wantOut: "usage: spectra-helper"},
		{args: []string{"-h"}, wantCode: 0, wantOut: "usage: spectra-helper"},
		{args: []string{"help"}, wantCode: 0, wantOut: "usage: spectra-helper"},
		{args: []string{"--version"}, wantCode: 0, wantOut: version},
		{args: []string{"--hepl"}, wantCode: 2, wantErr: "unexpected arguments"},
		{args: []string{"--help", "extra"}, wantCode: 2, wantErr: "usage: spectra-helper"},
		{args: []string{"serve"}, wantCode: 2, wantErr: "unexpected arguments"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		served := false
		code := dispatchArgs(tc.args, &stdout, &stderr, func() int { served = true; return 0 })
		if served {
			t.Errorf("%v: serve was called", tc.args)
		}
		if code != tc.wantCode {
			t.Errorf("%v: code = %d, want %d", tc.args, code, tc.wantCode)
		}
		if tc.wantOut != "" && !strings.Contains(stdout.String(), tc.wantOut) {
			t.Errorf("%v: stdout = %q, want %q", tc.args, stdout.String(), tc.wantOut)
		}
		if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
			t.Errorf("%v: stderr = %q, want %q", tc.args, stderr.String(), tc.wantErr)
		}
	}
}

func TestDispatchArgsServesWithNoArgs(t *testing.T) {
	served := false
	code := dispatchArgs(nil, &bytes.Buffer{}, &bytes.Buffer{}, func() int { served = true; return 7 })
	if !served || code != 7 {
		t.Fatalf("served=%v code=%d, want serve called and its code returned", served, code)
	}
}

func TestRunWithArgsHelpLeavesLiveSocket(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	orig := sockPath
	sockPath = path
	t.Cleanup(func() { sockPath = orig })

	for _, args := range [][]string{{"--help"}, {"--bogus"}} {
		_ = runWithArgs(args)
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%v removed the live socket: %v", args, err)
		}
	}
}

func TestClearStaleSocketMissingPath(t *testing.T) {
	if err := clearStaleSocket(filepath.Join(shortSocketDir(t), "none.sock")); err != nil {
		t.Fatalf("missing path: %v", err)
	}
}

func TestClearStaleSocketRefusesLiveHelper(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	err = clearStaleSocket(path)
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("err = %v, want already-listening error", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("live socket was removed: %v", err)
	}
}

func TestClearStaleSocketRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "h.sock")
	// A bound but never-listening socket refuses connections like the
	// leftover from a crashed helper.
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	_ = syscall.Close(fd)

	if err := clearStaleSocket(path); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket still present: %v", err)
	}
}

func TestClearStaleSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "h.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := clearStaleSocket(path)
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("err = %v, want not-a-socket error", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("regular file was removed: %v", err)
	}
}

func TestHelperVersionArg(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		if !helperVersionArg(args) {
			t.Fatalf("%v should be a version arg", args)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"version", "extra"}} {
		if helperVersionArg(args) {
			t.Fatalf("%v should not be a version arg", args)
		}
	}
}

func TestGroupIDParsesLookupResult(t *testing.T) {
	orig := lookupGroup
	lookupGroup = func(name string) (*user.Group, error) {
		if name != helperGroup {
			t.Fatalf("lookup name = %q, want %q", name, helperGroup)
		}
		return &user.Group{Gid: "123"}, nil
	}
	t.Cleanup(func() { lookupGroup = orig })

	gid, err := groupID(helperGroup)
	if err != nil {
		t.Fatal(err)
	}
	if gid != 123 {
		t.Fatalf("gid = %d, want 123", gid)
	}
}

func TestGroupIDRejectsInvalidGID(t *testing.T) {
	orig := lookupGroup
	lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: "not-a-number"}, nil
	}
	t.Cleanup(func() { lookupGroup = orig })

	if _, err := groupID(helperGroup); err == nil {
		t.Fatal("expected invalid gid error")
	}
}

func TestSecureSocketAppliesGroupAndMode(t *testing.T) {
	orig := lookupGroup
	lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: "456"}, nil
	}
	t.Cleanup(func() { lookupGroup = orig })

	var gotPath string
	var gotUID, gotGID int
	var gotMode os.FileMode
	err := secureSocket("sock", helperGroup,
		func(path string, uid, gid int) error {
			gotPath, gotUID, gotGID = path, uid, gid
			return nil
		},
		func(_ string, mode os.FileMode) error {
			gotMode = mode
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "sock" || gotUID != 0 || gotGID != 456 || gotMode != 0o660 {
		t.Fatalf("secureSocket applied path=%q uid=%d gid=%d mode=%o", gotPath, gotUID, gotGID, gotMode)
	}
}

func TestSecureSocketReturnsChownError(t *testing.T) {
	orig := lookupGroup
	lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: "456"}, nil
	}
	t.Cleanup(func() { lookupGroup = orig })

	want := errors.New("chown failed")
	err := secureSocket("sock", helperGroup,
		func(string, int, int) error { return want },
		func(string, os.FileMode) error { return nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrapped chown error", err)
	}
}
