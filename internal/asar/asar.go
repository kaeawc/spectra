// Package asar reads the header of an Electron app.asar archive without
// extracting it. An asar file begins with a Chromium "pickle" header — a small
// run of little-endian uint32 fields followed by a JSON directory that lists
// every logical file with its size and (in modern archives) a SHA256 integrity
// hash. This package parses that directory into a flat file inventory so two
// versions of an app can be diffed. It reads header bytes only and never
// executes or fully extracts the archive.
package asar

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// maxHeaderBytes caps the JSON directory size we will read, as a sanity guard
// against a corrupt or hostile length field.
const maxHeaderBytes = 128 << 20 // 128 MiB

// FileEntry is one logical file inside an asar archive.
type FileEntry struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256,omitempty"`
	Unpacked bool   `json:"unpacked,omitempty"`
}

// Archive is the parsed file inventory of an asar header, sorted by path.
type Archive struct {
	Files []FileEntry `json:"files"`
}

type rawHeader struct {
	Files map[string]rawNode `json:"files"`
}

type rawNode struct {
	Size      *int64             `json:"size"`
	Offset    string             `json:"offset"`
	Unpacked  bool               `json:"unpacked"`
	Integrity *rawIntegrity      `json:"integrity"`
	Files     map[string]rawNode `json:"files"`
}

type rawIntegrity struct {
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
}

// ParseFile reads and parses the asar header at path.
func ParseFile(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	prefix := make([]byte, 16)
	if _, err := io.ReadFull(f, prefix); err != nil {
		return nil, fmt.Errorf("asar: read header prefix: %w", err)
	}
	jsonLen := binary.LittleEndian.Uint32(prefix[12:16])
	if jsonLen == 0 || jsonLen > maxHeaderBytes {
		return nil, fmt.Errorf("asar: implausible header size %d", jsonLen)
	}
	jsonBuf := make([]byte, jsonLen)
	if _, err := io.ReadFull(f, jsonBuf); err != nil {
		return nil, fmt.Errorf("asar: read header json: %w", err)
	}
	return parseHeaderJSON(jsonBuf)
}

// Parse parses an asar archive already held in memory.
func Parse(data []byte) (*Archive, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("asar: too short (%d bytes)", len(data))
	}
	jsonLen := binary.LittleEndian.Uint32(data[12:16])
	if jsonLen == 0 || jsonLen > maxHeaderBytes {
		return nil, fmt.Errorf("asar: implausible header size %d", jsonLen)
	}
	if int64(16)+int64(jsonLen) > int64(len(data)) {
		return nil, fmt.Errorf("asar: header size %d exceeds data (%d bytes)", jsonLen, len(data))
	}
	return parseHeaderJSON(data[16 : 16+jsonLen])
}

func parseHeaderJSON(jsonBuf []byte) (*Archive, error) {
	var h rawHeader
	if err := json.Unmarshal(jsonBuf, &h); err != nil {
		return nil, fmt.Errorf("asar: parse header json: %w", err)
	}
	a := &Archive{}
	walk("", h.Files, a)
	sort.Slice(a.Files, func(i, j int) bool { return a.Files[i].Path < a.Files[j].Path })
	return a, nil
}

func walk(prefix string, nodes map[string]rawNode, a *Archive) {
	for name, node := range nodes {
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		if node.Size != nil { // a file has a size; a directory does not
			entry := FileEntry{Path: path, Size: *node.Size, Unpacked: node.Unpacked}
			if node.Integrity != nil {
				entry.SHA256 = node.Integrity.Hash
			}
			a.Files = append(a.Files, entry)
			continue
		}
		walk(path, node.Files, a) // directory: recurse
	}
}

// Diff is the file-level difference between two archives.
type Diff struct {
	Added   []FileEntry `json:"added,omitempty"`
	Removed []FileEntry `json:"removed,omitempty"`
	Changed []FileEntry `json:"changed,omitempty"`
}

// Empty reports whether the two archives are identical at the file level.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// DiffArchives compares archive a (older) with b (newer): files only in b are
// Added, files only in a are Removed, files present in both whose SHA256
// differs are Changed. Results are sorted by path.
func DiffArchives(a, b *Archive) Diff {
	oldByPath := indexByPath(a)
	newByPath := indexByPath(b)
	var d Diff
	for path, ne := range newByPath {
		oe, ok := oldByPath[path]
		if !ok {
			d.Added = append(d.Added, ne)
			continue
		}
		if changed(oe, ne) {
			d.Changed = append(d.Changed, ne)
		}
	}
	for path, oe := range oldByPath {
		if _, ok := newByPath[path]; !ok {
			d.Removed = append(d.Removed, oe)
		}
	}
	sortEntries(d.Added)
	sortEntries(d.Removed)
	sortEntries(d.Changed)
	return d
}

// changed reports whether two entries for the same path differ. When both
// carry a SHA256 we trust it; otherwise we fall back to size.
func changed(oldE, newE FileEntry) bool {
	if oldE.SHA256 != "" && newE.SHA256 != "" {
		return oldE.SHA256 != newE.SHA256
	}
	return oldE.Size != newE.Size
}

func indexByPath(a *Archive) map[string]FileEntry {
	m := make(map[string]FileEntry, len(a.Files))
	for _, e := range a.Files {
		m[e.Path] = e
	}
	return m
}

func sortEntries(es []FileEntry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Path < es[j].Path })
}

// NativeModulePaths returns the archive-relative paths of bundled native
// add-ons (*.node), which are the highest-signal capability surface.
func (a *Archive) NativeModulePaths() []string {
	var out []string
	for _, e := range a.Files {
		if strings.HasSuffix(e.Path, ".node") {
			out = append(out, e.Path)
		}
	}
	sort.Strings(out)
	return out
}
