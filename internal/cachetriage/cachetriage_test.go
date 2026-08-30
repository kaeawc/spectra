package cachetriage

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		markers    []string
		incomplete bool
		want       Class
	}{
		{"go-build", nil, false, ClassSafe},
		{"com.corp.SomeApp", nil, false, ClassRegenerable},
		{"com.corp.SomeApp", []string{"cookie"}, false, ClassRisky},
		{"go-build", []string{".sqlite"}, false, ClassRisky}, // risky wins over a safe name
		{"Homebrew", nil, false, ClassSafe},
		{"go-build", nil, true, ClassRisky}, // an incomplete scan is held back
	}
	for _, c := range cases {
		got, reason := classify(c.name, c.markers, c.incomplete)
		if got != c.want {
			t.Errorf("classify(%q, %v, incomplete=%v) = %s, want %s", c.name, c.markers, c.incomplete, got, c.want)
		}
		if got != ClassRegenerable && reason == "" {
			t.Errorf("classify(%q) gave class %s with no reason", c.name, got)
		}
	}
}

func TestRiskyMarker(t *testing.T) {
	cases := map[string]string{
		"Cookies":      "cookie",
		"state.sqlite": ".sqlite",
		"creds.token":  "token",
		"photo.jpg":    "",
		"000003.ldb":   ".ldb", // a LevelDB table means the subtree holds real data
	}
	for name, want := range cases {
		if got := riskyMarker(name); got != want {
			t.Errorf("riskyMarker(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTriageClassifiesAndTotals(t *testing.T) {
	layout := map[string]map[string]int64{
		"go-build":         {"a.o": 1000, "b.o": 2000},          // safe
		"com.corp.big":     {"blob1": 5000, "blob2": 5000},      // regenerable
		"com.corp.private": {"Cookies": 10, "data.sqlite": 900}, // risky
	}
	deps := Deps{
		Subdirs: func(root string) ([]string, error) {
			return []string{"go-build", "com.corp.big", "com.corp.private"}, nil
		},
		Scan: func(dir string, fn func(string, int64)) (ScanResult, error) {
			files := layout[filepath.Base(dir)]
			for name, sz := range files {
				fn(name, sz)
			}
			return ScanResult{}, nil
		},
	}

	rep, err := Triage("/caches", deps)
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	byName := map[string]Entry{}
	for _, e := range rep.Entries {
		byName[e.Name] = e
	}
	if byName["go-build"].Class != ClassSafe {
		t.Errorf("go-build class = %s", byName["go-build"].Class)
	}
	if byName["com.corp.big"].Class != ClassRegenerable {
		t.Errorf("com.corp.big class = %s", byName["com.corp.big"].Class)
	}
	if byName["com.corp.private"].Class != ClassRisky {
		t.Errorf("com.corp.private class = %s", byName["com.corp.private"].Class)
	}
	if rep.TotalBytes != 3000+10000+910 {
		t.Errorf("total = %d", rep.TotalBytes)
	}
	if rep.ReclaimableBytes != 3000+10000 {
		t.Errorf("reclaimable = %d, want %d", rep.ReclaimableBytes, 13000)
	}
	if rep.RiskyBytes != 910 {
		t.Errorf("risky = %d, want 910", rep.RiskyBytes)
	}
	// entries sorted by size descending
	if rep.Entries[0].Name != "com.corp.big" {
		t.Errorf("largest entry = %s, want com.corp.big", rep.Entries[0].Name)
	}
}

func TestTriageSubdirsError(t *testing.T) {
	deps := Deps{
		Subdirs: func(string) ([]string, error) { return nil, errors.New("cannot read cache root") },
	}
	if _, err := Triage("/nope", deps); err == nil {
		t.Error("expected error when the root cannot be listed")
	}
}

func TestTriageIncompleteScanHeldBack(t *testing.T) {
	deps := Deps{
		Subdirs: func(string) ([]string, error) { return []string{"go-build"}, nil },
		// A build cache that would normally be "safe", but its scan is truncated.
		Scan: func(dir string, fn func(string, int64)) (ScanResult, error) {
			fn("a.o", 1000)
			return ScanResult{Truncated: true}, nil
		},
	}
	rep, err := Triage("/caches", deps)
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	e := rep.Entries[0]
	if e.Class != ClassRisky || !e.Incomplete {
		t.Errorf("a truncated scan must be risky/incomplete, got class=%s incomplete=%v", e.Class, e.Incomplete)
	}
	if rep.ReclaimableBytes != 0 || rep.RiskyBytes != 1000 {
		t.Errorf("truncated bytes must be held back: reclaimable=%d risky=%d", rep.ReclaimableBytes, rep.RiskyBytes)
	}
}

func TestTriageScanErrorHeldBack(t *testing.T) {
	deps := Deps{
		Subdirs: func(string) ([]string, error) { return []string{"com.corp.app"}, nil },
		Scan: func(dir string, fn func(string, int64)) (ScanResult, error) {
			return ScanResult{}, errors.New("permission denied")
		},
	}
	rep, err := Triage("/caches", deps)
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if rep.Entries[0].Class != ClassRisky || rep.ReclaimableBytes != 0 {
		t.Errorf("a scan error must hold the entry back, got %+v", rep.Entries[0])
	}
}
