package main

import (
	"bytes"
	"strings"
	"testing"
)

const sampleMap = `{"version":3,"sources":["orig.js"],"names":["boom"],"mappings":"AAAAA"}`

func TestParsePosition(t *testing.T) {
	line, col, err := parsePosition("12:340")
	if err != nil || line != 12 || col != 340 {
		t.Fatalf("parsePosition = %d,%d,%v", line, col, err)
	}
	if _, _, err := parsePosition("nope"); err == nil {
		t.Error("expected error for bad position")
	}
	if _, _, err := parsePosition("x:1"); err == nil {
		t.Error("expected error for non-numeric line")
	}
}

func TestFormatSym(t *testing.T) {
	got := formatSym(symResult{Input: "1:0", Mapped: true, Source: "orig.js", Line: 1, Column: 0, Name: "boom"})
	if got != "1:0 -> orig.js:1:0 (boom)" {
		t.Errorf("formatSym = %q", got)
	}
	if formatSym(symResult{Input: "9:9"}) != "9:9 -> no mapping" {
		t.Errorf("unmapped format wrong")
	}
}

func TestRunWebSymbolicate(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte(sampleMap), nil }
	var out, errBuf bytes.Buffer
	if code := runWebSymbolicate([]string{"map.js.map", "1:0"}, &out, &errBuf, read); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "orig.js:1:0 (boom)") {
		t.Errorf("output = %q", out.String())
	}
}

func TestRunWebSymbolicateJSON(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte(sampleMap), nil }
	var out, errBuf bytes.Buffer
	if code := runWebSymbolicate([]string{"--json", "map.js.map", "1:0"}, &out, &errBuf, read); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"name": "boom"`) {
		t.Errorf("json = %q", out.String())
	}
}

func TestRunWebSymbolicateReadError(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, bytesErr("nope") }
	var out, errBuf bytes.Buffer
	if code := runWebSymbolicate([]string{"missing.map", "1:0"}, &out, &errBuf, read); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunWebSymbolicateWrongArgc(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte(sampleMap), nil }
	var out, errBuf bytes.Buffer
	if code := runWebSymbolicate([]string{"only-map.map"}, &out, &errBuf, read); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

type bytesErr string

func (e bytesErr) Error() string { return string(e) }
