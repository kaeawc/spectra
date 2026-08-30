package cachetriage

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		markers []string
		want    Class
	}{
		{"go-build", nil, ClassSafe},
		{"com.corp.SomeApp", nil, ClassRegenerable},
		{"com.corp.SomeApp", []string{"cookie"}, ClassRisky},
		{"go-build", []string{".sqlite"}, ClassRisky}, // risky wins over a safe name
		{"Homebrew", nil, ClassSafe},
	}
	for _, c := range cases {
		got, reason := classify(c.name, c.markers)
		if got != c.want {
			t.Errorf("classify(%q, %v) = %s, want %s", c.name, c.markers, got, c.want)
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
		"index.ldb":    "", // a bare .ldb is a leveldb table, not itself a marker name here
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
		Scan: func(dir string, fn func(string, int64)) error {
			files := layout[filepath.Base(dir)]
			for name, sz := range files {
				fn(name, sz)
			}
			return nil
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
