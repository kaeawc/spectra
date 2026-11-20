package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const svgBody = `<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`

// artifactJVMs writes real files where a tool asks, like asprof and jcmd do.
type artifactJVMs struct{ fakeJVMs }

func (artifactJVMs) CaptureFlamegraph(_ context.Context, _ int, o jvm.FlamegraphOptions) error {
	return os.WriteFile(o.OutputPath, []byte(svgBody), 0o600)
}

func (artifactJVMs) HeapDump(_ context.Context, _ int, dest string) error {
	return os.WriteFile(dest, []byte("JAVA PROFILE 1.0.2"), 0o600)
}

type sampleProcs struct {
	fakeProcessCollector
	out string
}

func (f *sampleProcs) SampleProcess(context.Context, int, int, int) (string, error) {
	return f.out, nil
}

func artifactSession(t *testing.T) (*sdk.ClientSession, *Server) {
	t.Helper()
	s := structuredTestServer(t)
	s.SetArtifactDir(filepath.Join(t.TempDir(), "artifacts"))
	s.collect.JVMs = artifactJVMs{}
	s.collect.Processes = &sampleProcs{out: "Call graph:\n  main"}
	return startServerSession(t, s, "2025-11-25", nil), s
}

func callLinks(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, []*sdk.ResourceLink) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: err %v res %+v", name, err, res)
	}
	var links []*sdk.ResourceLink
	for _, c := range res.Content {
		if l, ok := c.(*sdk.ResourceLink); ok {
			links = append(links, l)
		}
	}
	return res, links
}

func readURI(cs *sdk.ClientSession, uri string) (*sdk.ReadResourceResult, error) {
	return cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: uri})
}

func TestFlamegraphToolLinksReadableSVG(t *testing.T) {
	cs, _ := artifactSession(t)
	dest := filepath.Join(t.TempDir(), "out.svg")
	res, links := callLinks(t, cs, "jvm", map[string]any{"operation": "flamegraph", "pid": 20, "confirm_sensitive": true, "dest": dest})
	if len(links) != 1 || len(res.Content) != 2 {
		t.Fatalf("content = %d, links = %d", len(res.Content), len(links))
	}
	if _, ok := res.Content[0].(*sdk.TextContent); !ok {
		t.Fatalf("first content is %T, want text unchanged", res.Content[0])
	}
	l := links[0]
	if !strings.HasPrefix(l.URI, "spectra://artifact/flamegraph/") || l.MIMEType != "image/svg+xml" {
		t.Fatalf("link = %+v", l)
	}
	got, err := readURI(cs, l.URI)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Contents[0]; c.Text != svgBody || c.MIMEType != "image/svg+xml" {
		t.Fatalf("contents = %+v", c)
	}
}

func TestSampleToolLinksReport(t *testing.T) {
	cs, _ := artifactSession(t)
	_, links := callLinks(t, cs, "process", map[string]any{"operation": "sample", "pid": 20})
	if len(links) != 1 || !strings.HasPrefix(links[0].URI, "spectra://artifact/sample/") {
		t.Fatalf("links = %+v", links)
	}
	got, err := readURI(cs, links[0].URI)
	if err != nil || got.Contents[0].Text != "Call graph:\n  main" {
		t.Fatalf("read: %v %+v", err, got)
	}
}

func TestJobResultKeepsArtifactLink(t *testing.T) {
	cs, _ := artifactSession(t)
	res, _ := jobCall(t, cs, "job_start", `{"tool":"process","arguments":{"operation":"sample","pid":20}}`)
	id := mustJobID(t, res)
	waitFor(t, "job completion", func() bool { return jobRec(t, cs, "job_status", idArg(id)).Status == jobCompleted })
	got, text := jobCall(t, cs, "job_result", idArg(id))
	if got.IsError || len(got.Content) != 2 {
		t.Fatalf("job_result = %d items: %s", len(got.Content), text)
	}
	l, ok := got.Content[1].(*sdk.ResourceLink)
	if !ok || !strings.HasPrefix(l.URI, "spectra://artifact/sample/") {
		t.Fatalf("second content = %#v, want a sample resource_link", got.Content[1])
	}
	read, err := readURI(cs, l.URI)
	if err != nil || read.Contents[0].Text != "Call graph:\n  main" {
		t.Fatalf("read: %v %+v", err, read)
	}
}

func TestHeapDumpIsMetadataOnly(t *testing.T) {
	cs, _ := artifactSession(t)
	dest := filepath.Join(t.TempDir(), "h.hprof")
	_, links := callLinks(t, cs, "jvm", map[string]any{"operation": "heap_dump", "pid": 20, "confirm_sensitive": true, "dest": dest})
	if len(links) != 1 {
		t.Fatalf("links = %+v", links)
	}
	got, err := readURI(cs, links[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got.Contents[0].Text), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["path"] != dest || doc["served"] != false || strings.Contains(got.Contents[0].Text, "JAVA PROFILE") {
		t.Fatalf("doc = %v", doc)
	}
}

func TestSnapshotLinkReadsStoredSnapshot(t *testing.T) {
	cs, _ := artifactSession(t)
	_, links := callLinks(t, cs, "snapshot", map[string]any{"operation": "create", "store": true})
	if len(links) != 1 || links[0].URI != "spectra://artifact/snapshot/snap-1" {
		t.Fatalf("links = %+v", links)
	}
	got, err := readURI(cs, links[0].URI)
	if err != nil || !strings.Contains(got.Contents[0].Text, "snap-1") {
		t.Fatalf("read: %v %+v", err, got)
	}
}

func TestArtifactReadRejectsBadIDs(t *testing.T) {
	cs, s := artifactSession(t)
	root, _ := s.artifactsRoot()
	if err := os.MkdirAll(filepath.Join(root, "sample"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "sample", "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{
		"spectra://artifact/sample/../../etc/passwd",
		"spectra://artifact/sample/..%2F..%2Fetc%2Fpasswd",
		"spectra://artifact/sample/a..b.txt",
		"spectra://artifact/sample/.hidden",
		"spectra://artifact/sample/missing.txt",
		"spectra://artifact/sample/link.txt",
		"spectra://artifact/bogus/x.txt",
		"spectra://artifact/snapshot/unknown-id",
		"spectra://artifact/sample",
	} {
		if got, err := readURI(cs, uri); err == nil {
			t.Errorf("%s: read succeeded: %+v", uri, got.Contents[0])
		}
	}
}

func TestArtifactSizeCapAndBlob(t *testing.T) {
	cs, s := artifactSession(t)
	root, _ := s.artifactsRoot()
	dir := filepath.Join(root, "flamegraph")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.html"), make([]byte, maxArtifactBlob+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	big, err := readURI(cs, "spectra://artifact/flamegraph/big.html")
	if err != nil || !strings.Contains(big.Contents[0].Text, `"truncated":true`) || big.Contents[0].Blob != nil {
		t.Fatalf("big: %v %+v", err, big)
	}
	raw, err := readURI(cs, "spectra://artifact/flamegraph/raw.bin")
	if err != nil || len(raw.Contents[0].Blob) != 3 || raw.Contents[0].MIMEType != "application/octet-stream" {
		t.Fatalf("blob: %v %+v", err, raw)
	}
}

func TestArtifactTemplateListed(t *testing.T) {
	cs, _ := artifactSession(t)
	tpls, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil || len(tpls.ResourceTemplates) != 1 || tpls.ResourceTemplates[0].URITemplate != artifactURITemplate {
		t.Fatalf("templates: %v %+v", err, tpls)
	}
}

func complete(t *testing.T, cs *sdk.ClientSession, ref *sdk.CompleteReference, name, value string, ctxArgs map[string]string) []string {
	t.Helper()
	p := &sdk.CompleteParams{Ref: ref, Argument: sdk.CompleteParamsArgument{Name: name, Value: value}}
	if ctxArgs != nil {
		p.Context = &sdk.CompleteContext{Arguments: ctxArgs}
	}
	res, err := cs.Complete(context.Background(), p)
	if err != nil {
		t.Fatalf("complete %s=%q: %v", name, value, err)
	}
	return res.Completion.Values
}

var (
	jvmPromptRef = &sdk.CompleteReference{Type: "ref/prompt", Name: "jvm-memory-debug"}
	triagePrompt = &sdk.CompleteReference{Type: "ref/prompt", Name: "incident-triage"}
	artifactRef  = &sdk.CompleteReference{Type: "ref/resource", URI: artifactURITemplate}
)

func TestCompletePIDs(t *testing.T) {
	cs, s := artifactSession(t)
	s.collect.Processes = &fakeProcessCollector{procs: []process.Info{
		{PID: 100, Command: "launchd"}, {PID: 1234, Command: "java"}, {PID: 1299, Command: "zsh"}, {PID: 7, Command: "Gradle Daemon"},
	}}
	if got := complete(t, cs, triagePrompt, "pid", "12", nil); strings.Join(got, ",") != "1234,1299" {
		t.Errorf("pid prefix = %v", got)
	}
	if got := complete(t, cs, triagePrompt, "pid", "gradle", nil); strings.Join(got, ",") != "7" {
		t.Errorf("name match = %v", got)
	}
	if got := complete(t, cs, jvmPromptRef, "pid", "", nil); strings.Join(got, ",") != "20" {
		t.Errorf("jvm pids = %v", got)
	}
	if got := complete(t, cs, triagePrompt, "no_such_arg", "1", nil); len(got) != 0 {
		t.Errorf("unknown arg = %v", got)
	}
}

func TestCompleteDirs(t *testing.T) {
	cs, _ := artifactSession(t)
	root := t.TempDir()
	for _, d := range []string{"alpha", "alps", "beta", ".hid"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "alfile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := complete(t, cs, triagePrompt, "roots", root+"/al", nil)
	want := []string{filepath.Join(root, "alpha"), filepath.Join(root, "alps")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dirs = %v, want %v", got, want)
	}
	if got := complete(t, cs, triagePrompt, "under", root+"/", nil); len(got) != 3 {
		t.Errorf("all dirs = %v", got)
	}
	if got := complete(t, cs, triagePrompt, "roots", "relative/path", nil); len(got) != 0 {
		t.Errorf("relative = %v", got)
	}
}

func TestCompleteArtifactVariables(t *testing.T) {
	cs, s := artifactSession(t)
	root, _ := s.artifactsRoot()
	dir := filepath.Join(root, "sample")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"sample-1-20260101T000000Z-aa.txt", "sample-2-20260102T000000Z-bb.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := complete(t, cs, artifactRef, "kind", "s", nil); strings.Join(got, ",") != "sample,snapshot" {
		t.Errorf("kinds = %v", got)
	}
	if got := complete(t, cs, artifactRef, "id", "sample-2", map[string]string{"kind": "sample"}); len(got) != 1 {
		t.Errorf("ids = %v", got)
	}
	if got := complete(t, cs, artifactRef, "id", "", map[string]string{"kind": "sample"}); len(got) != 2 || !strings.Contains(got[0], "sample-2") {
		t.Errorf("ids newest first = %v", got)
	}
	if got := complete(t, cs, artifactRef, "id", "", map[string]string{"kind": "../x"}); len(got) != 0 {
		t.Errorf("bad kind = %v", got)
	}
}

type slowProcs struct{ fakeProcessCollector }

func (*slowProcs) CollectProcesses(context.Context, process.CollectOptions) []process.Info {
	time.Sleep(2 * time.Second)
	return nil
}

func TestCompletionLatencyBounds(t *testing.T) {
	big := make([]process.Info, 20000)
	for i := range big {
		big[i] = process.Info{PID: i + 1, Command: "proc"}
	}
	for name, procs := range map[string]ProcessCollector{"large table": &fakeProcessCollector{procs: big}, "slow collector": &slowProcs{}} {
		t.Run(name, func(t *testing.T) {
			cs, s := artifactSession(t)
			s.collect.Processes = procs
			for i := 0; i < 3; i++ {
				start := time.Now()
				got := complete(t, cs, triagePrompt, "pid", "1", nil)
				if d := time.Since(start); d > 250*time.Millisecond {
					t.Fatalf("completion took %v", d)
				}
				if len(got) > maxCompletions {
					t.Fatalf("%d values", len(got))
				}
				if name == "large table" && i == 2 && len(got) != maxCompletions {
					t.Fatalf("large table gave %d values", len(got))
				}
			}
		})
	}
}
