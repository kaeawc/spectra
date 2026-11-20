package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/idgen"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Artifacts are files a tool produced that a client can read back through a
// resource link. Only files Spectra itself wrote under its artifacts
// directory are ever served: a request names a kind and an id, never a path.
const (
	artifactURIPrefix   = "spectra://artifact/"
	artifactURITemplate = "spectra://artifact/{kind}/{id}"
	// maxArtifactBlob caps the raw bytes of one artifact a resource read
	// returns (a binary one goes out base64-encoded, about 10.7 MiB on the
	// wire). Larger ones answer with metadata and the local path instead.
	maxArtifactBlob = 8 << 20
	// maxArtifactCopy caps the copy of a captured artifact written to a
	// caller's dest.
	maxArtifactCopy = 64 << 20

	kindSample     = "sample"
	kindFlamegraph = "flamegraph"
	kindHeap       = "heap"
	kindSnapshot   = "snapshot"
)

var artifactKinds = []string{kindSample, kindFlamegraph, kindHeap, kindSnapshot}

// artifactIDPattern admits file names Spectra generates: no separators and
// no leading dot. ".." is rejected separately.
var artifactIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validArtifactKind(kind string) bool {
	for _, k := range artifactKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func validArtifactID(id string) bool {
	return artifactIDPattern.MatchString(id) && !strings.Contains(id, "..")
}

// artifactLink is a resource_link content item a tool result carries.
type artifactLink struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
	Size        int64
}

func (l artifactLink) content() *sdk.ResourceLink {
	link := &sdk.ResourceLink{URI: l.URI, Name: l.Name, Description: l.Description, MIMEType: l.MIMEType}
	if l.Size > 0 {
		size := l.Size
		link.Size = &size
	}
	return link
}

func artifactURI(kind, id string) string { return artifactURIPrefix + kind + "/" + id }

// artifactsRoot is where Spectra keeps artifacts: ~/.spectra/artifacts unless
// the server was given another directory. spectraDir is the ~/.spectra parent
// that must also be safe, or "" for an overridden root.
func (s *Server) artifactsRoot() (root, spectraDir string, err error) {
	if s.artifactDir != "" {
		return s.artifactDir, "", nil
	}
	home, err := s.home()
	if err != nil {
		return "", "", fmt.Errorf("artifacts directory: %w", err)
	}
	spectraDir = filepath.Join(home, ".spectra")
	return filepath.Join(spectraDir, "artifacts"), spectraDir, nil
}

func (s *Server) home() (string, error) {
	if s.homeDir != nil {
		return s.homeDir()
	}
	return os.UserHomeDir()
}

// SetArtifactDir overrides the artifacts directory (mainly for tests).
func (s *Server) SetArtifactDir(dir string) { s.artifactDir = dir }

// SetHomeDir overrides how the home directory is found (mainly for tests).
func (s *Server) SetHomeDir(home func() (string, error)) { s.homeDir = home }

func artifactMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".html":
		return "text/html"
	case ".svg":
		return "image/svg+xml"
	case ".txt", ".collapsed", ".folded":
		return "text/plain"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
}

func textMIME(mime string) bool {
	return strings.HasPrefix(mime, "text/") || mime == "image/svg+xml" || mime == "application/json"
}

// flamegraphFormats are the dest extensions jvm flamegraph accepts. asprof
// picks its output format from the output file's extension (.html flame
// graph, .jfr recording, .collapsed/.folded stacks, .svg on async-profiler
// 1.x, anything else a text summary), so the capture file keeps it.
var flamegraphFormats = []string{".html", ".svg", ".jfr", ".collapsed", ".folded", ".txt"}

// flamegraphExt is the capture's extension for dest: its own when asprof
// supports it, .html when dest is empty, and "" (unsupported) otherwise.
func flamegraphExt(dest string) string {
	if dest == "" {
		return ".html"
	}
	ext := strings.ToLower(filepath.Ext(dest))
	for _, f := range flamegraphFormats {
		if ext == f {
			return ext
		}
	}
	return ""
}

func (s *Server) newArtifactID(prefix, ext string) string {
	return fmt.Sprintf("%s-%s-%s%s", prefix, s.now().Format("20060102T150405Z"), idgen.Default.Next()[:8], ext)
}

// saveArtifact stores data as a new artifact and returns its link. A failure
// to store only means the result carries no link.
func (s *Server) saveArtifact(kind, prefix, ext, description string, data []byte) (artifactLink, bool) {
	dir, err := s.artifactKindDir(kind, true)
	if err != nil {
		return artifactLink{}, false
	}
	id := s.newArtifactID(prefix, ext)
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, id), data, 0o600); err != nil {
		return artifactLink{}, false
	}
	s.sweepArtifacts(dir, id)
	return artifactLink{URI: artifactURI(kind, id), Name: id, Description: description, MIMEType: artifactMIME(ext), Size: int64(len(data))}, true
}

// stagedSize checks a capture a tool wrote into a file newStagedArtifact
// created. An empty or unreadable capture is removed and reports false.
func stagedSize(staging, id string) (int64, bool) {
	f, info, err := openArtifact(staging, id)
	if err != nil {
		return 0, false
	}
	f.Close()
	if info.Size() == 0 {
		_ = os.Remove(filepath.Join(staging, id))
		return 0, false
	}
	return info.Size(), true
}

// publishStaged moves a finished capture from the staging directory into its
// kind directory and links it. Only then can a sweep see it.
func (s *Server) publishStaged(kind, staging, id, description string, size int64) (artifactLink, bool) {
	dir, err := s.artifactKindDir(kind, true)
	if err != nil {
		return artifactLink{}, false
	}
	if err := os.Rename(filepath.Join(staging, id), filepath.Join(dir, id)); err != nil {
		return artifactLink{}, false
	}
	s.sweepArtifacts(dir, id)
	return artifactLink{URI: artifactURI(kind, id), Name: id, Description: description, MIMEType: artifactMIME(filepath.Ext(id)), Size: size}, true
}

func (s *Server) artifactCopyLimit() int64 {
	if s.artifactCopyBytes > 0 {
		return s.artifactCopyBytes
	}
	return maxArtifactCopy
}

// copyArtifactTo writes the caller's requested copy of a staged capture.
// dest is replaced atomically, so a symlink there is replaced, not followed,
// and a capture over the copy cap fails without touching dest.
func (s *Server) copyArtifactTo(staging, id, dest string) error {
	f, _, err := openArtifact(staging, id)
	if err != nil {
		return err
	}
	defer f.Close()
	limit := s.artifactCopyLimit()
	return fsutil.WriteFileAtomicStream(dest, 0o600, func(w io.Writer) error {
		n, err := io.Copy(w, io.LimitReader(f, limit+1))
		if err != nil {
			return err
		}
		if n > limit {
			return fmt.Errorf("capture is over the %d-byte copy limit", limit)
		}
		return nil
	})
}

// heapRecord is the sidecar for a heap dump: dumps are large and sensitive,
// so a read returns this metadata and never the dump's bytes.
type heapRecord struct {
	PID     int       `json:"pid"`
	Path    string    `json:"path"`
	Size    int64     `json:"size_bytes"`
	Created time.Time `json:"created"`
}

func (s *Server) registerHeapDump(pid int, path string) (artifactLink, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return artifactLink{}, false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return artifactLink{}, false
	}
	data, err := json.Marshal(heapRecord{PID: pid, Path: abs, Size: info.Size(), Created: s.now()})
	if err != nil {
		return artifactLink{}, false
	}
	link, ok := s.saveArtifact(kindHeap, fmt.Sprintf("heap-%d", pid), ".json", fmt.Sprintf("Heap dump of pid %d (metadata and local path; the dump itself is not served)", pid), data)
	link.Size = info.Size()
	return link, ok
}

// withLinks attaches resource links to a successful result.
func withLinks(res ToolResult, links ...artifactLink) ToolResult {
	if res.IsError {
		return res
	}
	res.Links = append(res.Links, links...)
	return res
}

// parseArtifactURI splits spectra://artifact/<kind>/<id> and validates both.
func parseArtifactURI(uri string) (kind, id string, ok bool) {
	rest, found := strings.CutPrefix(uri, artifactURIPrefix)
	if !found {
		return "", "", false
	}
	kind, id, found = strings.Cut(rest, "/")
	if !found || !validArtifactKind(kind) || !validArtifactID(id) {
		return "", "", false
	}
	return kind, id, true
}

func artifactTemplates() []*sdk.ResourceTemplate {
	return []*sdk.ResourceTemplate{{
		Name:        "Artifact",
		Title:       "Spectra artifact",
		Description: "A sample report, flamegraph, heap dump record or stored snapshot Spectra produced. kind is one of sample, flamegraph, heap, snapshot; tool results link these.",
		URITemplate: artifactURITemplate,
	}}
}

// readArtifact serves spectra://artifact/<kind>/<id>.
func (s *Server) readArtifact(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := req.Params.URI
	kind, id, ok := parseArtifactURI(uri)
	if !ok {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if kind == kindSnapshot {
		return s.readSnapshotArtifact(ctx, uri, id)
	}
	dir, err := s.artifactKindDir(kind, false)
	if err != nil {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	f, _, err := openArtifact(dir, id)
	if err != nil {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	defer f.Close()
	if kind == kindHeap {
		return s.readHeapRecord(uri, f)
	}
	// The cap applies to the open file's bytes, so a file that grows after
	// the open still cannot push a larger read through.
	data, over, err := readCapped(f, maxArtifactBlob)
	if err != nil {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	if over {
		size := int64(maxArtifactBlob + 1)
		if info, err := f.Stat(); err == nil {
			size = info.Size()
		}
		return tooLargeResource(uri, filepath.Join(dir, id), size)
	}
	return contentsResource(uri, artifactMIME(filepath.Ext(id)), data), nil
}

func contentsResource(uri, mime string, data []byte) *sdk.ReadResourceResult {
	c := &sdk.ResourceContents{URI: uri, MIMEType: mime}
	if textMIME(mime) {
		c.Text = string(data)
	} else {
		c.Blob = data // marshals as base64
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{c}}
}

func tooLargeResource(uri, path string, size int64) (*sdk.ReadResourceResult, error) {
	doc, err := json.Marshal(map[string]any{
		"truncated":  true,
		"size_bytes": size,
		"max_bytes":  maxArtifactBlob,
		"path":       path,
		"message":    "artifact is larger than the resource size cap; read it from the local path",
	})
	if err != nil {
		return nil, err
	}
	return contentsResource(uri, "application/json", doc), nil
}

// maxHeapRecord caps a heap sidecar read; real ones are a few hundred bytes.
const maxHeapRecord = 64 << 10

func (s *Server) readHeapRecord(uri string, r io.Reader) (*sdk.ReadResourceResult, error) {
	raw, over, err := readCapped(r, maxHeapRecord)
	if err != nil || over {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	var rec heapRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	doc := map[string]any{
		"kind":       kindHeap,
		"pid":        rec.PID,
		"path":       rec.Path,
		"size_bytes": rec.Size,
		"created":    rec.Created,
		"served":     false,
		"message":    "heap dumps can hold secrets and are large; the dump is not served. Analyze it with jvm heap_leak_suspects using dest=path and confirm_sensitive=true.",
	}
	if info, err := os.Lstat(rec.Path); err == nil && info.Mode().IsRegular() {
		doc["size_bytes"] = info.Size()
		doc["exists"] = true
	} else {
		doc["exists"] = false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return contentsResource(uri, "application/json", out), nil
}

func (s *Server) readSnapshotArtifact(ctx context.Context, uri, id string) (*sdk.ReadResourceResult, error) {
	db, err := openStore()
	if err != nil {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	defer db.Close()
	raw, err := db.GetSnapshotJSON(ctx, id)
	if err != nil || len(raw) == 0 {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	if len(raw) > maxArtifactBlob {
		return tooLargeResource(uri, "snapshot store (spectra snapshot get "+id+")", int64(len(raw)))
	}
	return contentsResource(uri, "application/json", raw), nil
}
