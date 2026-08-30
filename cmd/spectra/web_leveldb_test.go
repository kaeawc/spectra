package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/leveldbcheck"
)

// fakeLevelFS serves a directory layout to leveldbcheck.Deps.
func fakeLevelFS(dirs map[string][]leveldbcheck.Entry, files map[string]string) leveldbcheck.Deps {
	return leveldbcheck.Deps{
		ReadDir: func(dir string) ([]leveldbcheck.Entry, error) {
			e, ok := dirs[dir]
			if !ok {
				return nil, errors.New("no such directory")
			}
			return e, nil
		},
		ReadFile: func(path string) ([]byte, error) {
			b, ok := files[path]
			if !ok {
				return nil, errors.New("no such file")
			}
			return []byte(b), nil
		},
	}
}

// A store directory whose CURRENT is missing must still be inspected and its
// corruption reported, rather than "no LevelDB stores found".
func TestWebLevelDBHealthExplicitMissingCurrent(t *testing.T) {
	deps := fakeLevelFS(
		map[string][]leveldbcheck.Entry{
			"/store": {{Name: "000003.ldb", Size: 10}},
		},
		nil,
	)
	var out, errBuf bytes.Buffer
	code := runWebLevelDBHealth([]string{"/store"}, &out, &errBuf, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no CURRENT file") {
		t.Errorf("expected the missing-CURRENT store to be flagged, got:\n%s", out.String())
	}
}

func TestWebLevelDBHealthDiscoversParent(t *testing.T) {
	deps := fakeLevelFS(
		map[string][]leveldbcheck.Entry{
			"/root":         {{Name: "leveldb", IsDir: true}},
			"/root/leveldb": {{Name: "CURRENT", Size: 16}, {Name: "MANIFEST-1", Size: 10}},
		},
		map[string]string{"/root/leveldb/CURRENT": "MANIFEST-1"},
	)
	var out, errBuf bytes.Buffer
	if code := runWebLevelDBHealth([]string{"/root"}, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "/root/leveldb") {
		t.Errorf("expected the nested store to be discovered, got:\n%s", out.String())
	}
}

func TestWebLevelDBHealthRequiresArg(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWebLevelDBHealth(nil, &out, &errBuf, leveldbcheck.DefaultDeps()); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
