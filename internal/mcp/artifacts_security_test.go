package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// cancellingSnapshots cancels the call while the snapshot is being built,
// the way a client cancel lands mid-collection.
type cancellingSnapshots struct{ cancel context.CancelFunc }

func (c cancellingSnapshots) BuildSnapshot(context.Context, snapshot.Options) snapshot.Snapshot {
	c.cancel()
	return snapshot.Snapshot{ID: "snap-cancelled"}
}

func storedSnapshotCount(t *testing.T) int {
	t.Helper()
	db, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.ListSnapshots(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestCancelledCallsNeverPersist(t *testing.T) {
	for name, run := range map[string]func(*Server, context.Context) ToolResult{
		"snapshot store": func(s *Server, ctx context.Context) ToolResult {
			return s.toolSnapshot(ctx, json.RawMessage(`{"operation":"create","store":true}`))
		},
		"diagnose persist": func(s *Server, ctx context.Context) ToolResult {
			return s.toolDiagnose(ctx, json.RawMessage(`{"persist":true}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := structuredTestServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.collect.Snapshots = cancellingSnapshots{cancel: cancel}
			if res := run(s, ctx); !res.IsError {
				t.Errorf("cancelled call succeeded: %s", res.Content[0].Text)
			}
			if n := storedSnapshotCount(t); n != 0 {
				t.Fatalf("a cancelled call stored %d snapshot(s)", n)
			}
		})
	}
}

func TestStoredSnapshotKeepsItsIndexedRows(t *testing.T) {
	s := structuredTestServer(t)
	s.collect.Snapshots = fakeSnapshotCollector{snap: snapshot.Snapshot{ID: "snap-rows", Processes: []process.Info{{PID: 10, PPID: 1, Command: "java"}}}}
	if res := s.toolSnapshot(context.Background(), json.RawMessage(`{"operation":"create","store":true}`)); res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	db, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if procs, err := db.GetSnapshotProcesses(context.Background(), "snap-rows"); err != nil || len(procs) != 1 {
		t.Fatalf("stored snapshot has %d process row(s), %v; want 1", len(procs), err)
	}
}

// homeServer serves artifacts from the default ~/.spectra/artifacts under an
// injected home directory.
func homeServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := structuredTestServer(t)
	home := t.TempDir()
	s.SetHomeDir(func() (string, error) { return home, nil })
	s.collect.JVMs = artifactJVMs{}
	s.collect.Processes = &sampleProcs{out: "Call graph:\n  main"}
	return s, home
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sampleLinks(t *testing.T, cs *sdk.ClientSession) []*sdk.ResourceLink {
	t.Helper()
	_, links := callLinks(t, cs, "process", map[string]any{"operation": "sample", "pid": 20})
	return links
}

func TestSymlinkedArtifactDirsAreRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, home, secret string){
		"kind dir": func(t *testing.T, home, secret string) {
			mkdirs(t, filepath.Join(home, ".spectra", "artifacts"))
			if err := os.Symlink(secret, filepath.Join(home, ".spectra", "artifacts", "sample")); err != nil {
				t.Fatal(err)
			}
		},
		"artifacts root": func(t *testing.T, home, secret string) {
			mkdirs(t, filepath.Join(home, ".spectra"), filepath.Join(secret, "sample"))
			writeFile(t, filepath.Join(secret, "sample", "id_rsa"), "PRIVATE KEY")
			if err := os.Symlink(secret, filepath.Join(home, ".spectra", "artifacts")); err != nil {
				t.Fatal(err)
			}
		},
		"spectra dir": func(t *testing.T, home, secret string) {
			mkdirs(t, filepath.Join(secret, "artifacts", "sample"))
			writeFile(t, filepath.Join(secret, "artifacts", "sample", "id_rsa"), "PRIVATE KEY")
			if err := os.Symlink(secret, filepath.Join(home, ".spectra")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s, home := homeServer(t)
			secret := t.TempDir() // stands in for ~/.ssh
			writeFile(t, filepath.Join(secret, "id_rsa"), "PRIVATE KEY")
			setup(t, home, secret)
			cs := startServerSession(t, s, "2025-11-25", nil)
			if got, err := readURI(cs, "spectra://artifact/sample/id_rsa"); err == nil {
				t.Fatalf("served through a symlinked directory: %+v", got.Contents[0])
			}
			if got := complete(t, cs, artifactRef, "id", "", map[string]string{"kind": "sample"}); len(got) != 0 {
				t.Errorf("completion listed %v through a symlinked directory", got)
			}
			if links := sampleLinks(t, cs); len(links) != 0 {
				t.Errorf("saved through a symlinked directory: %+v", links)
			}
			entries, _ := os.ReadDir(secret)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "sample-") {
					t.Errorf("wrote %s into the symlink target", e.Name())
				}
			}
		})
	}
}

func TestArtifactDirPermissions(t *testing.T) {
	s, home := homeServer(t)
	root := filepath.Join(home, ".spectra", "artifacts")
	mkdirs(t, filepath.Join(root, "flamegraph"))
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	cs := startServerSession(t, s, "2025-11-25", nil)
	links := sampleLinks(t, cs)
	if len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
	for _, d := range []string{root, filepath.Join(root, "sample")} {
		info, err := os.Lstat(d)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, %v; want 0700", d, info.Mode().Perm(), err)
		}
	}
	// A group-writable kind directory is refused, not served.
	writeFile(t, filepath.Join(root, "flamegraph", "x.svg"), svgBody)
	if err := os.Chmod(filepath.Join(root, "flamegraph"), 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := readURI(cs, "spectra://artifact/flamegraph/x.svg"); err == nil {
		t.Fatal("served from a group-writable kind directory")
	}
	if _, err := readURI(cs, links[0].URI); err != nil {
		t.Fatalf("own sample link: %v", err)
	}
}

func TestArtifactSymlinkInsideKindDirIsRefused(t *testing.T) {
	cs, s := artifactSession(t)
	root, _, _ := s.artifactsRoot()
	dir := filepath.Join(root, "sample")
	mkdirs(t, dir)
	writeFile(t, filepath.Join(dir, "real.txt"), "report")
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if got, err := readURI(cs, "spectra://artifact/sample/alias.txt"); err == nil {
		t.Fatalf("served a symlink: %+v", got.Contents[0])
	}
	if got, err := readURI(cs, "spectra://artifact/sample/real.txt"); err != nil || got.Contents[0].Text != "report" {
		t.Fatalf("real file: %v", err)
	}
}

func TestReadCappedStopsAtTheCap(t *testing.T) {
	data, over, err := readCapped(bytes.NewReader(make([]byte, 11)), 10)
	if err != nil || !over || data != nil {
		t.Fatalf("over: %d bytes, %v, %v", len(data), over, err)
	}
	data, over, err = readCapped(bytes.NewReader(make([]byte, 10)), 10)
	if err != nil || over || len(data) != 10 {
		t.Fatalf("at cap: %d bytes, %v, %v", len(data), over, err)
	}
}

// silentJVMs is an asprof that succeeds without writing anything.
type silentJVMs struct{ fakeJVMs }

func (silentJVMs) CaptureFlamegraph(context.Context, int, jvm.FlamegraphOptions) error { return nil }

func TestFlamegraphNeverAdoptsAPreexistingDest(t *testing.T) {
	s := artifactServer(t)
	s.collect.JVMs = silentJVMs{}
	cs := startServerSession(t, s, "2025-11-25", nil)
	secret := filepath.Join(t.TempDir(), "id_rsa.html")
	writeFile(t, secret, "PRIVATE KEY")
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "jvm", Arguments: map[string]any{
		"operation": "flamegraph", "pid": 20, "confirm_sensitive": true, "dest": secret,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Content {
		if l, ok := c.(*sdk.ResourceLink); ok {
			got, _ := readURI(cs, l.URI)
			t.Fatalf("linked %s: %+v", l.URI, got)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "PRIVATE KEY" {
		t.Fatalf("dest was rewritten to %q", b)
	}
	root, _, _ := s.artifactsRoot()
	if entries, _ := os.ReadDir(filepath.Join(root, "flamegraph")); len(entries) != 0 {
		t.Fatalf("artifacts left behind: %v", entries)
	}
}

func TestArtifactRetention(t *testing.T) {
	s := artifactServer(t)
	s.artifactKindBytes = 100
	root, _, _ := s.artifactsRoot()
	dir := filepath.Join(root, "sample")
	mkdirs(t, dir)
	now := s.now()
	files := map[string]time.Duration{"old.txt": 8 * 24 * time.Hour, "a.txt": 3 * time.Hour, "b.txt": 2 * time.Hour, "c.txt": time.Hour}
	for name, age := range files {
		writeFile(t, filepath.Join(dir, name), strings.Repeat("x", 40))
		if err := os.Chtimes(filepath.Join(dir, name), now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	cs := startServerSession(t, s, "2025-11-25", nil)
	if links := sampleLinks(t, cs); len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	// old.txt is past the age limit; a.txt, the oldest, goes to fit 100
	// bytes beside b.txt and c.txt (80) and the new sample (18).
	if len(left) != 3 || left[0] != "b.txt" || left[1] != "c.txt" || !strings.HasPrefix(left[2], "sample-20-") {
		t.Fatalf("left = %v", left)
	}
}

// stepClock is a wall clock a test advances.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestSnapshotIDCompletionsAreCached(t *testing.T) {
	s := artifactServer(t)
	clock := &stepClock{t: time.Now()}
	s.wallClock = clock
	ctx := context.Background()
	if err := saveSnapshot(ctx, snapshot.Snapshot{ID: "snap-a"}); err != nil {
		t.Fatal(err)
	}
	cs := startServerSession(t, s, "2025-11-25", nil)
	ref := &sdk.CompleteReference{Type: "ref/prompt", Name: "baseline-diff-review"}
	if got := complete(t, cs, ref, "id_a", "snap", nil); strings.Join(got, ",") != "snap-a" {
		t.Fatalf("first = %v", got)
	}
	if err := saveSnapshot(ctx, snapshot.Snapshot{ID: "snap-b"}); err != nil {
		t.Fatal(err)
	}
	if got := complete(t, cs, ref, "id_a", "snap", nil); strings.Join(got, ",") != "snap-a" {
		t.Fatalf("within the ttl the listing is reused, got %v", got)
	}
	clock.add(completionTTL + time.Second)
	waitFor(t, "a refreshed listing", func() bool { return len(complete(t, cs, ref, "id_a", "snap", nil)) == 2 })
}

// lateProcs answers its first listing slower than the completion budget.
type lateProcs struct{ fakeProcessCollector }

func (*lateProcs) CollectProcesses(context.Context, process.CollectOptions) []process.Info {
	time.Sleep(3 * completionBudget)
	return []process.Info{{PID: 4242, Command: "java"}}
}

func TestPIDCompletionsAreWarmAtStart(t *testing.T) {
	s := artifactServer(t)
	s.collect.Processes = &lateProcs{}
	cs := startServerSession(t, s, "2025-11-25", nil)
	time.Sleep(6 * completionBudget)
	if got := complete(t, cs, triagePrompt, "pid", "", nil); strings.Join(got, ",") != "4242" {
		t.Fatalf("first completion = %v, want the warmed listing", got)
	}
}
