package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const inspectSampleIPS = `{"app_name":"MyApp","app_version":"2.1","bug_type":"309","os_version":"macOS 15.6.1","incident_id":"ABC-123","name":"MyApp"}
{"procName":"MyApp","pid":4012,"exception":{"type":"EXC_BAD_ACCESS","signal":"SIGSEGV","codes":"0x1, 0x0"},"termination":{"namespace":"SIGNAL","indicator":"Segmentation fault: 11"},"faultingThread":0,"threads":[{"queue":"com.apple.main-thread","triggered":true,"frames":[{"imageIndex":0,"imageOffset":1000,"symbol":"main","symbolLocation":42}]}],"usedImages":[{"name":"MyApp","arch":"arm64"}]}`

func writeCrashTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCrashInspectRenders(t *testing.T) {
	p := writeCrashTemp(t, "MyApp.ips", inspectSampleIPS)
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", p}, &out, &errBuf, nil)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"MyApp", "EXC_BAD_ACCESS", "crashed", "main + 42"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestCrashInspectJSON(t *testing.T) {
	p := writeCrashTemp(t, "MyApp.ips", inspectSampleIPS)
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", "--json", p}, &out, &errBuf, nil)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"exception": "EXC_BAD_ACCESS (SIGSEGV)"`) {
		t.Errorf("json output missing decoded exception; got:\n%s", out.String())
	}
}

func TestCrashInspectJSONImagesOnDisk(t *testing.T) {
	notExist := func(p string) (crashreport.BinaryFile, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	var out, errBuf bytes.Buffer
	code := runCrashInspect([]string{"--json", "../../internal/crashreport/testdata/frames.ips"}, &out, &errBuf, os.ReadFile, notExist, crashreport.SourceLineOptions{})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	var r crashreport.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, im := range r.Images {
		got[im.Name] = im.OnDisk
	}
	if got["MyApp"] != "missing" || got["libsystem_kernel.dylib"] != "shared_cache" {
		t.Errorf("image_on_disk = %v", got)
	}
	if n := len(r.Threads[1].FrameDetails); n != len(r.Threads[1].Frames) {
		t.Errorf("frame_details = %d, want one per frame", n)
	}
}

type panicRunner struct{ t *testing.T }

func (p panicRunner) Run(context.Context, proc.Cmd) (proc.Result, error) {
	p.t.Errorf("crash inspect ran a subprocess it should not have")
	return proc.Result{}, nil
}

func TestCrashInspectJSONDefaultHasNoSourceLines(t *testing.T) {
	var out, errBuf bytes.Buffer
	opts := crashreport.SourceLineOptions{OS: hostos.Darwin, Runner: panicRunner{t}}
	code := runCrashInspect([]string{"--json", "../../internal/crashreport/testdata/frames.ips"}, &out, &errBuf, os.ReadFile, crashreport.OpenOS, opts)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	for _, k := range []string{`"resolved_by"`, `"source_lines_unsupported"`, `"file"`, `"line"`} {
		if strings.Contains(out.String(), k) {
			t.Errorf("default JSON contains %s", k)
		}
	}
}

func TestCrashInspectSourceLinesUnsupported(t *testing.T) {
	var out, errBuf bytes.Buffer
	opts := crashreport.SourceLineOptions{OS: hostos.Linux, Runner: panicRunner{t}}
	code := runCrashInspect([]string{"--json", "--source-lines", "../../internal/crashreport/testdata/frames.ips"}, &out, &errBuf, os.ReadFile, crashreport.OpenOS, opts)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	var r crashreport.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if u := r.SourceLinesUnsupported; u == nil || u.Error != "unsupported_platform" || u.OS != "linux" {
		t.Errorf("source_lines_unsupported = %+v", u)
	}
	for _, th := range r.Threads {
		for _, d := range th.FrameDetails {
			if d.ResolvedBy != crashreport.ResolvedByNone {
				t.Errorf("frame = %+v, want resolved_by none", d)
			}
		}
	}

	out.Reset()
	errBuf.Reset()
	code = runCrashInspect([]string{"--source-lines", "../../internal/crashreport/testdata/frames.ips"}, &out, &errBuf, os.ReadFile, crashreport.OpenOS, opts)
	if code != 0 || !strings.Contains(errBuf.String(), "unsupported") || !strings.Contains(out.String(), "crashed") {
		t.Errorf("text mode: exit=%d stderr=%q stdout=%q", code, errBuf.String(), out.String())
	}
}

func TestFrameSource(t *testing.T) {
	th := crashreport.Thread{Frames: []string{"a", "b", "c"}, FrameDetails: []crashreport.FrameDetail{
		{File: "/src/main.swift", Line: 42},
		{File: "/src/a.swift", Line: 3, Column: 7},
		{Line: 9},
	}}
	for i, want := range []string{"  at /src/main.swift:42", "  at /src/a.swift:3:7", "", ""} {
		if got := frameSource(th, i); got != want {
			t.Errorf("frameSource(%d) = %q, want %q", i, got, want)
		}
	}
}

func TestCrashInspectLegacy(t *testing.T) {
	p := writeCrashTemp(t, "old.crash", "Process: Foo [1]\nIdentifier: com.example.foo\n")
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", p}, &out, &errBuf, nil)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "legacy") {
		t.Errorf("stderr = %q, want legacy notice", errBuf.String())
	}
}

func TestCrashInspectNoPath(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashWithIO([]string{"inspect"}, &out, &errBuf, nil); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
