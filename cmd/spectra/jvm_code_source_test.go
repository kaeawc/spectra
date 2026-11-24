package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
)

const codeSourceDump = `"main" #1 prio=5 os_prio=31 tid=0x1 nid=0x103 runnable
   java.lang.Thread.State: RUNNABLE
	at com.example.App.serve(App.java:42)
	at com.example.dep.Pool.take(Pool.java:7)
	at com.example.Generated$$Lambda/0x000000080100a000.run(Unknown Source)
	at java.base@21.0.1/java.lang.Thread.run(Thread.java:1583)
`

func makeJar(t *testing.T, path string, entries ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		if _, err := zw.Create(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadDumpCodeSources(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app.jar")
	makeJar(t, app, "com/example/App.class")
	cwdCalls := 0
	deps := codeSourceDeps{
		argv: func(pid int) ([]string, error) {
			return []string{"/opt/jdk/bin/java", "-Xmx1g", "-cp", "app.jar:missing/*", "com.example.App"}, nil
		},
		cwd: func(_ context.Context, pid int) (string, error) {
			cwdCalls++
			return root, nil
		},
		resolver: jvm.NewCodeSourceResolver(),
	}
	dump := jvm.ParseThreadDump(codeSourceDump, time.Unix(0, 0).UTC())
	got := deps.codeSources(context.Background(), 77, dump)
	if len(got) != 1 || got["com.example.App"] != (jvm.CodeSource{Jar: app, JarEntry: "com/example/App.class"}) {
		t.Fatalf("code sources = %+v", got)
	}
	if cwdCalls != 1 {
		t.Errorf("cwd read %d times, want 1 (relative class path)", cwdCalls)
	}

	data, err := json.Marshal(threadDumpReport{Summary: jvm.SummarizeThreads(dump), Dump: dump, CodeSources: got})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"code_sources":{"com.example.App":{"jar":"`+app+`","jar_entry":"com/example/App.class"}}`) {
		t.Errorf("JSON = %s", data)
	}
}

func TestThreadDumpCodeSourcesUnknownLaunch(t *testing.T) {
	dump := jvm.ParseThreadDump(codeSourceDump, time.Unix(0, 0).UTC())
	noCwd := func(context.Context, int) (string, error) {
		t.Fatal("cwd read for an unusable launch")
		return "", nil
	}
	for name, argv := range map[string]func(int) ([]string, error){
		"argv unreadable": func(int) ([]string, error) { return nil, errors.New("operation not permitted") },
		"argfile":         func(int) ([]string, error) { return []string{"java", "@/tmp/argfile"}, nil },
		"native launcher": func(int) ([]string, error) { return []string{"/opt/idea/bin/idea"}, nil },
	} {
		deps := codeSourceDeps{argv: argv, cwd: noCwd, resolver: jvm.NewCodeSourceResolver()}
		if got := deps.codeSources(context.Background(), 77, dump); got != nil {
			t.Errorf("%s: code sources = %+v", name, got)
		}
	}
}

// With no code sources the report encodes exactly as before the field existed.
func TestThreadDumpReportJSONByteIdentical(t *testing.T) {
	dump := jvm.ParseThreadDump(sampleThreadDump, time.Unix(0, 0).UTC())
	got, err := json.MarshalIndent(threadDumpReport{Summary: jvm.SummarizeThreads(dump), Dump: dump}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/thread_dump_report.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("thread-dump --json changed:\n%s", got)
	}
}
