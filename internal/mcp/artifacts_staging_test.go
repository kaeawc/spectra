package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingJVMs writes a capture where asked and remembers the path asked.
type recordingJVMs struct {
	fakeJVMs
	mu    sync.Mutex
	paths []string
	// during runs after the capture is written, before CaptureFlamegraph returns.
	during func()
}

func (r *recordingJVMs) CaptureFlamegraph(_ context.Context, _ int, o jvm.FlamegraphOptions) error {
	r.mu.Lock()
	r.paths = append(r.paths, o.OutputPath)
	r.mu.Unlock()
	if err := os.WriteFile(o.OutputPath, []byte(svgBody), 0o600); err != nil {
		return err
	}
	if r.during != nil {
		r.during()
	}
	return nil
}

func flamegraphCall(t *testing.T, cs *sdk.ClientSession, dest string) *sdk.CallToolResult {
	t.Helper()
	args := map[string]any{"operation": "flamegraph", "pid": 20, "confirm_sensitive": true}
	if dest != "" {
		args["dest"] = dest
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "jvm", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestFlamegraphCaptureKeepsDestFormat(t *testing.T) {
	s := artifactServer(t)
	rec := &recordingJVMs{}
	s.collect.JVMs = rec
	cs := startServerSession(t, s, "2025-11-25", nil)
	for _, tc := range []struct{ dest, ext, mime string }{
		{"recording.jfr", ".jfr", "application/octet-stream"},
		{"stacks.collapsed", ".collapsed", "text/plain"},
		{"graph.html", ".html", "text/html"},
		{"", ".html", "text/html"},
	} {
		dest := tc.dest
		if dest != "" {
			dest = filepath.Join(t.TempDir(), dest)
		}
		res := flamegraphCall(t, cs, dest)
		if res.IsError {
			t.Fatalf("dest %q: %+v", tc.dest, res.Content[0])
		}
		rec.mu.Lock()
		got := rec.paths[len(rec.paths)-1]
		rec.mu.Unlock()
		if filepath.Ext(got) != tc.ext {
			t.Errorf("dest %q captured to %s, want a %s capture", tc.dest, got, tc.ext)
		}
		link, ok := res.Content[1].(*sdk.ResourceLink)
		if !ok || link.MIMEType != tc.mime || !strings.HasSuffix(link.URI, tc.ext) {
			t.Errorf("dest %q link = %+v, want %s", tc.dest, res.Content[1], tc.mime)
		}
	}
	rec.mu.Lock()
	before := len(rec.paths)
	rec.mu.Unlock()
	res := flamegraphCall(t, cs, filepath.Join(t.TempDir(), "graph.png"))
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "dest must end in") {
		t.Fatalf("unsupported dest = %+v, want refused", res.Content[0])
	}
	if len(rec.paths) != before {
		t.Fatal("an unsupported dest still ran a capture")
	}
}

func TestInProgressFlamegraphSurvivesASweep(t *testing.T) {
	s := artifactServer(t)
	s.artifactKindBytes = 1
	root, _, _ := s.artifactsRoot()
	dir := filepath.Join(root, kindFlamegraph)
	mkdirs(t, dir)
	rec := &recordingJVMs{during: func() {
		// Another flamegraph finishing now sweeps an over-quota directory.
		s.sweepArtifacts(dir, "flamegraph-other.html")
	}}
	s.collect.JVMs = rec
	cs := startServerSession(t, s, "2025-11-25", nil)
	res := flamegraphCall(t, cs, "")
	if res.IsError {
		t.Fatalf("capture lost to a concurrent sweep: %+v", res.Content[0])
	}
	if strings.HasPrefix(rec.paths[0], dir+string(filepath.Separator)) {
		t.Fatalf("captured into the swept directory: %s", rec.paths[0])
	}
	link := res.Content[1].(*sdk.ResourceLink)
	if got, err := readURI(cs, link.URI); err != nil || got.Contents[0].Text != svgBody {
		t.Fatalf("published capture: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, stagingDirName)); len(entries) != 0 {
		t.Fatalf("staging left %d entries", len(entries))
	}
	if _, err := readURI(cs, "spectra://artifact/"+stagingDirName+"/x.html"); err == nil {
		t.Fatal("served the staging directory")
	}
}

func TestOversizedDestCopyKeepsDest(t *testing.T) {
	s := artifactServer(t)
	s.artifactCopyBytes = int64(len(svgBody) - 1)
	cs := startServerSession(t, s, "2025-11-25", nil)
	dest := filepath.Join(t.TempDir(), "out.svg")
	writeFile(t, dest, "previous")
	res := flamegraphCall(t, cs, dest)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "copy limit") {
		t.Fatalf("over-cap copy = %+v, want an error", res.Content[0])
	}
	if b, _ := os.ReadFile(dest); string(b) != "previous" {
		t.Fatalf("dest = %q, want it untouched", b)
	}
	s.artifactCopyBytes = int64(len(svgBody))
	if res := flamegraphCall(t, cs, dest); res.IsError {
		t.Fatalf("copy at exactly the cap: %+v", res.Content[0])
	}
	if b, _ := os.ReadFile(dest); string(b) != svgBody {
		t.Fatalf("dest = %q after a copy at the cap", b)
	}
}

func TestArtifactSweepReadsTheWholeDirectory(t *testing.T) {
	s := artifactServer(t)
	root, _, _ := s.artifactsRoot()
	dir := filepath.Join(root, kindSample)
	mkdirs(t, dir)
	old := s.now().Add(-artifactMaxAge - time.Hour)
	const n = 10050
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("old-%05d.txt", i))
		writeFile(t, p, "x")
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	s.sweepArtifacts(dir, "")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d of %d expired artifacts survived the sweep", len(entries), n)
	}
}
