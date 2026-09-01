package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const snapA = `{"taken_at":"2026-01-01T00:00:00Z","storage":{"volumes":[{"mount_point":"/","used_bytes":1000}],"user_library_bytes":500,"app_caches_bytes":200,"largest_apps":[{"path":"/Applications/Foo.app","on_disk_bytes":100}]}}`
const snapB = `{"taken_at":"2026-01-03T00:00:00Z","storage":{"volumes":[{"mount_point":"/","used_bytes":1600}],"user_library_bytes":520,"app_caches_bytes":900,"largest_apps":[{"path":"/Applications/Foo.app","on_disk_bytes":100},{"path":"/Applications/Bar.app","on_disk_bytes":300}]}}`

func writeSnap(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunStorageGrowthHuman(t *testing.T) {
	a, b := writeSnap(t, "a.json", snapA), writeSnap(t, "b.json", snapB)
	var out, errBuf bytes.Buffer
	if code := runStorageGrowth([]string{a, b}, &out, &errBuf); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "app-caches") || !strings.Contains(s, "Bar.app") || !strings.Contains(s, "days") {
		t.Errorf("expected growth summary, got:\n%s", s)
	}
}

func TestRunStorageGrowthJSON(t *testing.T) {
	a, b := writeSnap(t, "a.json", snapA), writeSnap(t, "b.json", snapB)
	var out, errBuf bytes.Buffer
	if code := runStorageGrowth([]string{"--json", a, b}, &out, &errBuf); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"deltas"`) || !strings.Contains(out.String(), `"bytes_per_day"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunStorageGrowthArgValidation(t *testing.T) {
	a := writeSnap(t, "a.json", snapA)
	for _, args := range [][]string{{}, {a}, {a, a, a}, {"--top", "-1", a, a}} {
		var out, errBuf bytes.Buffer
		if code := runStorageGrowth(args, &out, &errBuf); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunStorageGrowthBadInputs(t *testing.T) {
	a := writeSnap(t, "a.json", snapA)
	missing := filepath.Join(t.TempDir(), "nope.json")
	bad := writeSnap(t, "bad.json", "not json")
	noTS := writeSnap(t, "nots.json", `{"storage":{}}`)
	for _, args := range [][]string{{missing, a}, {a, bad}, {a, noTS}} {
		var out, errBuf bytes.Buffer
		if code := runStorageGrowth(args, &out, &errBuf); code != 1 {
			t.Errorf("args %v: exit = %d, want 1", args, code)
		}
	}
}
