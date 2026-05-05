package main

import (
	"errors"
	"os"
	"os/user"
	"testing"
)

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
