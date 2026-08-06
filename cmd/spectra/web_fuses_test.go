package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fuseSentinel mirrors the literal sentinel electronfuse scans for, so the test
// can synthesize a wire without exporting internals.
const fuseSentinel = "dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX"

func buildFuseWire(statuses string) []byte {
	w := []byte("preceding")
	w = append(w, []byte(fuseSentinel)...)
	w = append(w, 1, byte(len(statuses)))
	w = append(w, []byte(statuses)...)
	return w
}

func TestWebFusesInsecure(t *testing.T) {
	read := func(string) ([]byte, error) { return buildFuseWire("100000000"), nil }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{"/Applications/Foo.app"}, &out, &errBuf, read); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"RunAsNode", "critical"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestWebFusesHardened(t *testing.T) {
	read := func(string) ([]byte, error) { return buildFuseWire("000011011"), nil }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{"/Applications/Foo.app"}, &out, &errBuf, read); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "no dangerous fuse configuration") {
		t.Errorf("hardened app should report clean; got:\n%s", out.String())
	}
}

func TestWebFusesJSON(t *testing.T) {
	read := func(string) ([]byte, error) { return buildFuseWire("100000000"), nil }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{"--json", "/Applications/Foo.app"}, &out, &errBuf, read); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"severity": "critical"`) {
		t.Errorf("json missing critical finding; got:\n%s", out.String())
	}
}

func TestWebFusesReadError(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, errors.New("nope") }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{"/Applications/Foo.app"}, &out, &errBuf, read); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestWebFusesNoSentinel(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("not an electron binary"), nil }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{"/Applications/Foo.app"}, &out, &errBuf, read); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "no Electron fuse wire") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestWebFusesWrongArgc(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, nil }
	var out, errBuf bytes.Buffer
	if code := runWebFuses([]string{}, &out, &errBuf, read); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
