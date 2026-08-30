package xattrinspect

import (
	"errors"
	"strings"
	"testing"
)

func TestParseQuarantine(t *testing.T) {
	q, ok := parseQuarantine("0083;66d1a400;Safari;A1B2C3D4-0000\n")
	if !ok {
		t.Fatal("expected a valid quarantine record")
	}
	if q.Agent != "Safari" || q.Flags != "0083" {
		t.Errorf("agent/flags = %q/%q", q.Agent, q.Flags)
	}
	if !strings.HasPrefix(q.Timestamp, "2024-") {
		t.Errorf("timestamp not decoded: %q", q.Timestamp)
	}
	if _, ok := parseQuarantine("garbage"); ok {
		t.Error("a value with too few fields must not parse")
	}
}

func TestExtractURLs(t *testing.T) {
	// A where-froms binary plist has ASCII URL substrings between control bytes.
	raw := []byte("bplist00\x00\x00https://example.com/app.dmg\x00\x08https://example.com/\x00\x00https://example.com/app.dmg")
	urls := extractURLs(raw)
	if len(urls) != 2 {
		t.Fatalf("urls = %v, want 2 deduped", urls)
	}
	if urls[0] != "https://example.com/app.dmg" {
		t.Errorf("first url = %q", urls[0])
	}
}

func TestClassifyAttr(t *testing.T) {
	cases := map[string]string{
		attrQuarantine:                 "quarantine",
		attrProvenance:                 "provenance",
		attrWhereFroms:                 "where-froms",
		"com.apple.macl":               "security",
		"com.apple.cs.CodeDirectory":   "security",
		"com.apple.security.something": "security",
		"com.apple.FinderInfo":         "other",
		"com.apple.lastuseddate#PS":    "other",
	}
	for name, want := range cases {
		if got := classifyAttr(name); got != want {
			t.Errorf("classifyAttr(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAppleDoubleSibling(t *testing.T) {
	if got := appleDoubleSibling("/a/b/file.dmg"); got != "/a/b/._file.dmg" {
		t.Errorf("sibling = %q", got)
	}
}

// fakeXattr serves canned `xattr` output keyed by the joined args.
type fakeXattr struct {
	names      string            // output of `xattr <path>`
	values     map[string]string // "<attr>" -> `xattr -p <attr> <path>` output
	existsPath map[string]bool
}

func (f fakeXattr) deps() Deps {
	return Deps{
		Run: func(args ...string) ([]byte, error) {
			if len(args) == 1 { // list names
				return []byte(f.names), nil
			}
			if len(args) == 3 && args[0] == "-p" { // -p <attr> <path>
				if v, ok := f.values[args[1]]; ok {
					return []byte(v), nil
				}
				return nil, errors.New("no such attr")
			}
			return nil, errors.New("unexpected args")
		},
		Files:  func(root string) ([]string, error) { return []string{root}, nil },
		Exists: func(path string) bool { return f.existsPath[path] },
	}
}

func TestInspectFileFull(t *testing.T) {
	f := fakeXattr{
		names: "com.apple.quarantine\ncom.apple.metadata:kMDItemWhereFroms\ncom.apple.provenance\n",
		values: map[string]string{
			attrQuarantine: "0083;66d1a400;Firefox;X",
			attrWhereFroms: "bplist00\x00https://dl.example.com/tool.pkg\x00",
		},
		existsPath: map[string]bool{"/dir/._tool.pkg": true},
	}
	rep := Inspect([]string{"/dir/tool.pkg"}, f.deps())
	if rep.Scanned != 1 {
		t.Fatalf("scanned = %d", rep.Scanned)
	}
	fr := rep.Files[0]
	if fr.Quarantine == nil || fr.Quarantine.Agent != "Firefox" {
		t.Errorf("quarantine not parsed: %+v", fr.Quarantine)
	}
	if len(fr.WhereFroms) != 1 || fr.WhereFroms[0] != "https://dl.example.com/tool.pkg" {
		t.Errorf("where-froms = %v", fr.WhereFroms)
	}
	if !fr.AppleDouble {
		t.Error("expected AppleDouble sidecar detection")
	}
	if rep.Quarantined != 1 || rep.WithWhereFroms != 1 || rep.Provenanced != 1 || rep.WithAppleDouble != 1 {
		t.Errorf("summary counts wrong: %+v", rep)
	}
}

func TestInspectCountsPresentButUnparseableAttrs(t *testing.T) {
	// quarantine present with an unparseable value, and where-froms present with
	// no HTTP URL: both must still count toward the summary.
	f := fakeXattr{
		names: "com.apple.quarantine\ncom.apple.metadata:kMDItemWhereFroms\n",
		values: map[string]string{
			attrQuarantine: "garbage",
			attrWhereFroms: "bplist00\x00file:///local/only\x00",
		},
	}
	rep := Inspect([]string{"/dir/file"}, f.deps())
	fr := rep.Files[0]
	if fr.Quarantine != nil {
		t.Errorf("garbage quarantine should not parse into detail, got %+v", fr.Quarantine)
	}
	if len(fr.WhereFroms) != 0 {
		t.Errorf("no http url should be extracted, got %v", fr.WhereFroms)
	}
	if rep.Quarantined != 1 || rep.WithWhereFroms != 1 {
		t.Errorf("present attributes must count in the summary: %+v", rep)
	}
}

func TestInspectFileError(t *testing.T) {
	deps := Deps{
		Run:   func(args ...string) ([]byte, error) { return nil, errors.New("no xattr") },
		Files: func(root string) ([]string, error) { return []string{root}, nil },
	}
	rep := Inspect([]string{"/missing"}, deps)
	if rep.Files[0].Error == "" {
		t.Error("expected an error recorded for a failing xattr call")
	}
}

func TestInspectFilesExpansionError(t *testing.T) {
	deps := Deps{
		Files: func(root string) ([]string, error) { return nil, errors.New("stat failed") },
	}
	rep := Inspect([]string{"/nope"}, deps)
	if rep.Files[0].Error == "" {
		t.Error("expected a scan error when the path cannot be expanded")
	}
}
