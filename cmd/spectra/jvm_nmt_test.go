package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

const nmtSummaryBefore = `123:

Native Memory Tracking:

Total: reserved=1000KB, committed=500KB

-                 Java Heap (reserved=600KB, committed=300KB)
-                    Thread (reserved=300KB, committed=100KB)
-                     Other (reserved=100KB, committed=100KB)
`

const nmtSummaryAfter = `123:

Native Memory Tracking:

Total: reserved=1900KB, committed=1400KB

-                 Java Heap (reserved=600KB, committed=300KB)
-                    Thread (reserved=400KB, committed=200KB)
-                     Other (reserved=900KB, committed=900KB)
`

func nmtTestEnv(run jvm.CmdRunner) (nmtEnv, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return nmtEnv{stdout: &stdout, stderr: &stderr, run: run, readFile: os.ReadFile}, &stdout, &stderr
}

func fakeNMTRunner(t *testing.T, wantMode, output string) jvm.CmdRunner {
	t.Helper()
	return func(name string, args ...string) ([]byte, error) {
		if name != "jcmd" || len(args) != 3 || args[1] != "VM.native_memory" || args[2] != wantMode {
			t.Fatalf("unexpected command %s %q", name, args)
		}
		return []byte(output), nil
	}
}

func TestJVMNMTSummary(t *testing.T) {
	env, stdout, _ := nmtTestEnv(fakeNMTRunner(t, "summary", nmtSummaryAfter))
	if code := runJVMNMTWith([]string{"123"}, env); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "committed 1400 KB") || !strings.Contains(out, "Other") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestJVMNMTSummaryDisabled(t *testing.T) {
	env, _, stderr := nmtTestEnv(fakeNMTRunner(t, "summary", "123:\nNative memory tracking is not enabled\n"))
	if code := runJVMNMTWith([]string{"123"}, env); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "-XX:NativeMemoryTracking") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestJVMNMTDetailJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "jvm", "testdata", "nmt_detail_jdk17.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env, stdout, _ := nmtTestEnv(fakeNMTRunner(t, "detail", string(data)))
	if code := runJVMNMTWith([]string{"detail", "--json", "123"}, env); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var d jvm.NMTDetail
	if err := json.Unmarshal(stdout.Bytes(), &d); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if len(d.Sites) != 5 || len(d.Regions) != 3 {
		t.Fatalf("sites=%d regions=%d", len(d.Sites), len(d.Regions))
	}
}

func TestJVMNMTDetailText(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "jvm", "testdata", "nmt_detail_jdk21.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env, stdout, _ := nmtTestEnv(fakeNMTRunner(t, "detail", string(data)))
	if code := runJVMNMTWith([]string{"detail", "--top", "2", "123"}, env); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Unsafe_AllocateMemory0+0x8c") || strings.Contains(out, "Arena::grow") {
		t.Fatalf("expected top-2 call sites only:\n%s", out)
	}
}

func TestJVMNMTBaselineAndDiff(t *testing.T) {
	env, stdout, _ := nmtTestEnv(fakeNMTRunner(t, "baseline", "123:\nBaseline taken\n"))
	if code := runJVMNMTWith([]string{"baseline", "123"}, env); code != 0 {
		t.Fatalf("baseline exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "baseline recorded") {
		t.Fatalf("baseline output = %q", stdout.String())
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "jvm", "testdata", "nmt_summary_diff_jdk17.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env, stdout, _ = nmtTestEnv(fakeNMTRunner(t, "summary.diff", string(data)))
	if code := runJVMNMTWith([]string{"diff", "--json", "123"}, env); code != 0 {
		t.Fatalf("diff exit = %d", code)
	}
	var d jvm.NMTDiff
	if err := json.Unmarshal(stdout.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Categories[0].Name != "Thread" || d.DeltaCommittedKiB != 4100 {
		t.Fatalf("diff = %+v", d)
	}
}

func TestJVMNMTDiffWithoutBaseline(t *testing.T) {
	env, _, stderr := nmtTestEnv(fakeNMTRunner(t, "summary.diff", "123:\nNo baseline for comparison\n"))
	if code := runJVMNMTWith([]string{"diff", "123"}, env); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "nmt baseline") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestJVMNMTCompareFiles(t *testing.T) {
	before := writeTemp(t, "before.txt", nmtSummaryBefore)
	after := writeTemp(t, "after.txt", nmtSummaryAfter)
	env, stdout, _ := nmtTestEnv(nil)
	if code := runJVMNMTWith([]string{"compare", before, after}, env); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := strings.Split(stdout.String(), "\n")
	if !strings.Contains(lines[0], "committed +900 KB") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[2], "Other") || !strings.Contains(lines[2], "+800") {
		t.Fatalf("top growth row = %q\n%s", lines[2], stdout.String())
	}

	env, stdout, _ = nmtTestEnv(nil)
	if code := runJVMNMTWith([]string{"compare", "--json", before, after}, env); code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	var report nmtCompareReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Categories[0].Name != "Other" || report.Sites != nil {
		t.Fatalf("report = %+v", report)
	}
}

func TestJVMNMTCompareRejectsNonNMT(t *testing.T) {
	bad := writeTemp(t, "bad.txt", "not nmt output\n")
	env, _, stderr := nmtTestEnv(nil)
	if code := runJVMNMTWith([]string{"compare", bad, bad}, env); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "not a native memory tracking") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestJVMNMTUsageAndErrors(t *testing.T) {
	env, _, stderr := nmtTestEnv(nil)
	if code := runJVMNMTWith(nil, env); code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("no-args exit = %d, stderr = %q", code, stderr.String())
	}
	env, _, _ = nmtTestEnv(nil)
	if code := runJVMNMTWith([]string{"detail", "abc"}, env); code != 2 {
		t.Fatalf("bad pid exit = %d", code)
	}
	failing := func(string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	env, _, stderr = nmtTestEnv(failing)
	if code := runJVMNMTWith([]string{"123"}, env); code != 1 || !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("jcmd failure exit = %d, stderr = %q", code, stderr.String())
	}
}
