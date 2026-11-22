package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/jsonrpc"
)

// isPprof reports whether the file is a gzip-compressed pprof profile.
func isPprof(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		t.Fatalf("%s is not a gzip pprof file", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode %v, want 0600", path, info.Mode().Perm())
	}
}

func TestProfileKinds(t *testing.T) {
	p := testPaths(t)
	fake := clock.NewFake(time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC))
	s := New(Options{Paths: p, Clock: fake})
	for _, kind := range []string{"heap", "allocs", "goroutine"} {
		fake.Advance(time.Second)
		res, err := s.profile(context.Background(), nil, json.RawMessage(`{"kind":"`+kind+`"}`))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if res.Kind != kind || filepath.Dir(res.Path) != filepath.Join(p.Dir, "profiles") || !strings.HasPrefix(filepath.Base(res.Path), kind+"-") || res.NumGoroutine == 0 || res.HeapInuse == 0 {
			t.Fatalf("%s result = %+v", kind, res)
		}
		isPprof(t, res.Path)
	}
	fake.Advance(time.Second)
	res, err := s.profile(context.Background(), nil, json.RawMessage(`{"kind":"cpu","seconds":1}`))
	if err != nil || res.Seconds != 1 {
		t.Fatalf("cpu = %+v, %v", res, err)
	}
	isPprof(t, res.Path)
}

func TestProfileInvalidParams(t *testing.T) {
	s := New(Options{Paths: testPaths(t)})
	for _, raw := range []string{``, `{"kind":"block"}`, `{"kind":"heap","seconds":5}`, `{"kind":"cpu","seconds":121}`, `{"kind":"cpu","seconds":-1}`, `{"kind":`} {
		_, err := s.profile(context.Background(), nil, json.RawMessage(raw))
		var invalid invalidParamsError
		if !errors.As(err, &invalid) {
			t.Fatalf("%q: err = %v, want invalid params", raw, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(s.opts.Paths.Dir, "profiles")); len(entries) != 0 {
		t.Fatalf("invalid requests wrote %d files", len(entries))
	}
}

// A CPU profile ends early when the client goes away, and a second one
// cannot start while the first runs.
func TestProfileCPUCancelAndExclusive(t *testing.T) {
	s := New(Options{Paths: testPaths(t), Clock: clock.NewFake(time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC))})
	done := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := s.profile(context.Background(), done, json.RawMessage(`{"kind":"cpu","seconds":60}`))
		finished <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for cpuProfileMu.TryLock() {
		cpuProfileMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("cpu profile did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := s.profile(context.Background(), nil, json.RawMessage(`{"kind":"cpu","seconds":1}`)); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent cpu profile err = %v", err)
	}
	close(done)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cpu profile did not stop when the client left")
	}
}

func TestProfileOverSocket(t *testing.T) {
	p := testPaths(t)
	cancel, done := startServer(t, Options{Paths: p, Version: "v1"})
	defer func() { cancel(); <-done }()
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	call := func(payload string) map[string]json.RawMessage {
		t.Helper()
		if _, err := c.Write([]byte(payload + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := jsonrpc.ReadMessage(r)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(line, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	var st StatusResult
	if err := json.Unmarshal(call(`{"jsonrpc":"2.0","id":1,"method":"daemon.status"}`)["result"], &st); err != nil || !strings.Contains(strings.Join(st.Methods, ","), MethodProfile) {
		t.Fatalf("status methods = %v, %v", st.Methods, err)
	}
	var res ProfileResult
	if err := json.Unmarshal(call(`{"jsonrpc":"2.0","id":2,"method":"daemon.profile","params":{"kind":"goroutine"}}`)["result"], &res); err != nil {
		t.Fatal(err)
	}
	isPprof(t, res.Path)
	var rpcErr jsonrpc.Error
	if err := json.Unmarshal(call(`{"jsonrpc":"2.0","id":3,"method":"daemon.profile","params":{"kind":"nope"}}`)["error"], &rpcErr); err != nil || rpcErr.Code != CodeInvalidParams {
		t.Fatalf("invalid kind error = %+v, %v", rpcErr, err)
	}
}

// After a write only the newest keepProfiles profiles remain; other files
// in the directory are left alone.
func TestProfilePrunesOldest(t *testing.T) {
	p := testPaths(t)
	dir := filepath.Join(p.Dir, "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for i := 0; i < keepProfiles+5; i++ {
		path := filepath.Join(dir, fmt.Sprintf("heap-old-%02d.pprof", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Paths: p})
	res, err := s.profile(context.Background(), nil, json.RawMessage(`{"kind":"goroutine"}`))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if len(names) != keepProfiles+1 || !names[filepath.Base(res.Path)] || !names["notes.txt"] {
		t.Fatalf("kept %d files: %v", len(names), names)
	}
	// The six oldest (00..05) went: 25 old + 1 new = 26, keep 20.
	for i := 0; i < 6; i++ {
		if names[fmt.Sprintf("heap-old-%02d.pprof", i)] {
			t.Fatalf("old profile %02d kept", i)
		}
	}
	if !names["heap-old-06.pprof"] {
		t.Fatal("newer old profile removed")
	}
}

// A capture still being written (staging .partial name) survives a prune run
// by a concurrent capture, however old it looks.
func TestPruneKeepsActiveCapture(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	active := filepath.Join(dir, "cpu-active.pprof"+partialSuffix)
	for i, name := range []string{"cpu-active.pprof" + partialSuffix, "heap-a.pprof", "heap-b.pprof", "heap-c.pprof"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Second) // the active capture is oldest
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneProfiles(dir, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active capture pruned: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("kept %d files, want the active capture and heap-c", len(entries))
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// A write error inside the runtime's CPU profile writer is reported, and the
// RPC leaves no file behind.
func TestCPUProfileWriteErrorReported(t *testing.T) {
	err := writeCPUProfile(context.Background(), nil, failingWriter{}, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("err = %v, want the write error", err)
	}
}

func TestProfileSuccessLeavesNoStaging(t *testing.T) {
	p := testPaths(t)
	s := New(Options{Paths: p})
	res, err := s.profile(context.Background(), nil, json.RawMessage(`{"kind":"heap"}`))
	if err != nil || !strings.HasSuffix(res.Path, ".pprof") {
		t.Fatalf("res = %+v, %v", res, err)
	}
	entries, _ := os.ReadDir(filepath.Join(p.Dir, "profiles"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partialSuffix) {
			t.Fatalf("staging file left: %s", e.Name())
		}
	}
}
