// Package xattrinspect surfaces the macOS extended attributes that record a
// file's origin and security state — Gatekeeper quarantine, provenance,
// where-froms source URLs — plus AppleDouble sidecar files. It reads only;
// it never modifies attributes.
package xattrinspect

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	attrQuarantine = "com.apple.quarantine"
	attrProvenance = "com.apple.provenance"
	attrWhereFroms = "com.apple.metadata:kMDItemWhereFroms"
)

// urlPattern matches an http(s) URL up to the first control byte; where-froms
// values are binary plists whose URL strings are plain ASCII substrings.
var urlPattern = regexp.MustCompile(`https?://[^\x00-\x1f\x7f]+`)

// Deps injects the external access xattrinspect needs so parsing can be tested
// without touching the filesystem.
type Deps struct {
	// Run executes the `xattr` tool with args and returns its stdout.
	Run func(args ...string) ([]byte, error)
	// Files returns the regular-file paths to inspect for a root argument: the
	// root itself if it is a file, or its files (bounded) if it is a directory.
	Files func(root string) ([]string, error)
	// Exists reports whether a path exists (used to find AppleDouble sidecars).
	Exists func(path string) bool
}

// Attr is one extended attribute present on a file.
type Attr struct {
	Name  string `json:"name"`
	Class string `json:"class"`
}

// Quarantine is the parsed com.apple.quarantine record.
type Quarantine struct {
	Agent     string `json:"agent,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Flags     string `json:"flags,omitempty"`
}

// FileReport is the extended-attribute picture of one file.
type FileReport struct {
	Path        string      `json:"path"`
	Attrs       []Attr      `json:"attrs,omitempty"`
	Quarantine  *Quarantine `json:"quarantine,omitempty"`
	WhereFroms  []string    `json:"where_froms,omitempty"`
	AppleDouble bool        `json:"apple_double,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// Report is the combined result plus summary counts.
type Report struct {
	Files           []FileReport `json:"files"`
	Scanned         int          `json:"scanned"`
	Quarantined     int          `json:"quarantined"`
	Provenanced     int          `json:"provenanced"`
	WithWhereFroms  int          `json:"with_where_froms"`
	WithAppleDouble int          `json:"with_apple_double"`
}

// Inspect expands each path to its files and reports their extended attributes.
func Inspect(paths []string, deps Deps) Report {
	var rep Report
	for _, p := range paths {
		files, err := deps.Files(p)
		if err != nil {
			rep.Files = append(rep.Files, FileReport{Path: p, Error: fmt.Errorf("scan %s: %w", p, err).Error()})
			continue
		}
		for _, f := range files {
			fr := inspectFile(f, deps)
			tally(&rep, fr)
			rep.Files = append(rep.Files, fr)
		}
	}
	rep.Scanned = len(rep.Files)
	return rep
}

func tally(rep *Report, fr FileReport) {
	if fr.Quarantine != nil {
		rep.Quarantined++
	}
	if fr.AppleDouble {
		rep.WithAppleDouble++
	}
	if len(fr.WhereFroms) > 0 {
		rep.WithWhereFroms++
	}
	for _, a := range fr.Attrs {
		if a.Class == "provenance" {
			rep.Provenanced++
		}
	}
}

func inspectFile(path string, deps Deps) FileReport {
	fr := FileReport{Path: path}
	out, err := deps.Run(path)
	if err != nil {
		fr.Error = fmt.Errorf("xattr %s: %w", path, err).Error()
		return fr
	}
	names := splitNames(out)
	present := map[string]bool{}
	for _, n := range names {
		present[n] = true
		fr.Attrs = append(fr.Attrs, Attr{Name: n, Class: classifyAttr(n)})
	}

	if present[attrQuarantine] {
		if raw, err := deps.Run("-p", attrQuarantine, path); err == nil {
			if q, ok := parseQuarantine(string(raw)); ok {
				fr.Quarantine = &q
			}
		}
	}
	if present[attrWhereFroms] {
		if raw, err := deps.Run("-p", attrWhereFroms, path); err == nil {
			fr.WhereFroms = extractURLs(raw)
		}
	}
	if deps.Exists != nil {
		fr.AppleDouble = deps.Exists(appleDoubleSibling(path))
	}
	return fr
}

func splitNames(out []byte) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// classifyAttr labels an attribute name by the concern it reveals.
func classifyAttr(name string) string {
	switch {
	case name == attrQuarantine:
		return "quarantine"
	case name == attrProvenance:
		return "provenance"
	case name == attrWhereFroms:
		return "where-froms"
	case name == "com.apple.macl" || name == "com.apple.rootless" ||
		strings.HasPrefix(name, "com.apple.cs.") || strings.HasPrefix(name, "com.apple.security"):
		return "security"
	default:
		return "other"
	}
}

// parseQuarantine parses "flags;hex-timestamp;agent;uuid" into a record.
func parseQuarantine(raw string) (Quarantine, bool) {
	fields := strings.Split(strings.TrimSpace(raw), ";")
	if len(fields) < 3 {
		return Quarantine{}, false
	}
	q := Quarantine{Flags: fields[0], Agent: fields[2]}
	if ts, err := strconv.ParseInt(fields[1], 16, 64); err == nil && ts > 0 {
		q.Timestamp = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	return q, true
}

// extractURLs pulls the source URLs out of a where-froms attribute value.
func extractURLs(raw []byte) []string {
	seen := map[string]bool{}
	var urls []string
	for _, m := range urlPattern.FindAll(raw, -1) {
		u := strings.TrimRight(string(m), " ")
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// appleDoubleSibling returns the AppleDouble ._ sidecar path for a file.
func appleDoubleSibling(path string) string {
	return filepath.Join(filepath.Dir(path), "._"+filepath.Base(path))
}

// SortedAttrs returns a file's attribute names sorted, for stable rendering.
func SortedAttrs(fr FileReport) []Attr {
	out := append([]Attr(nil), fr.Attrs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
