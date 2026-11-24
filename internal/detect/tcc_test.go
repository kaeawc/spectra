package detect

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// legacyGrantedPermissions is the per-bundle query scanGrantedPermissions ran
// before it read each database once; the bundle-agnostic read must match it.
func legacyGrantedPermissions(t *testing.T, dbs []string, bundleID string) []string {
	t.Helper()
	seen := map[string]struct{}{}
	for _, db := range dbs {
		query := fmt.Sprintf("SELECT service FROM access WHERE client = '%s' AND auth_value >= 2;", bundleID)
		out, err := exec.Command("sqlite3", db, query).Output()
		if err != nil {
			t.Fatalf("legacy query: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[strings.TrimPrefix(line, "kTCCService")] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func makeTCCDB(t *testing.T, dir, name string, rows []string) string {
	t.Helper()
	db := filepath.Join(dir, name)
	sql := "CREATE TABLE access (service TEXT NOT NULL, client TEXT NOT NULL, auth_value INTEGER NOT NULL);" +
		strings.Join(rows, "")
	if out, err := exec.Command("sqlite3", db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 setup: %v: %s", err, out)
	}
	return db
}

// TestGrantedPermissionsMatchesPerBundleQuery pins that filtering one
// all-clients read in Go yields exactly what the per-bundle SQL filter did,
// including clients that try to forge rows through '|' or newlines.
func TestGrantedPermissionsMatchesPerBundleQuery(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	dir := t.TempDir()
	user := makeTCCDB(t, dir, "user.db", []string{
		"INSERT INTO access VALUES ('kTCCServiceCamera', 'com.alpha.app', 2);",
		"INSERT INTO access VALUES ('kTCCServiceMicrophone', 'com.alpha.app', 3);",
		"INSERT INTO access VALUES ('kTCCServiceScreenCapture', 'com.alpha.app', 0);",
		"INSERT INTO access VALUES ('kTCCServiceCamera', 'com.beta.app', 2);",
		"INSERT INTO access VALUES ('  kTCCServicePadded  ', 'com.beta.app', 2);",
		"INSERT INTO access VALUES ('kTCCServiceA' || char(10) || 'kTCCServiceB', 'com.beta.app', 2);",
		"INSERT INTO access VALUES ('kTCCServiceBlob', CAST('com.beta.app' AS BLOB), 2);",
		"INSERT INTO access VALUES ('kTCCServiceForged', '/x' || char(10) || 'com.gamma.app|kTCCServiceEvil', 2);",
		"INSERT INTO access VALUES ('kTCCServiceCase', 'COM.ALPHA.APP', 2);",
		"INSERT INTO access VALUES ('kTCCServiceLike', 'com.alpha.app.helper', 2);",
	})
	system := makeTCCDB(t, dir, "system.db", []string{
		"INSERT INTO access VALUES ('kTCCServiceSystemPolicyAllFiles', 'com.alpha.app', 2);",
		"INSERT INTO access VALUES ('kTCCServiceCamera', 'com.alpha.app', 2);",
	})
	dbs := []string{user, system}
	d := newDetector(Options{})
	d.tcc = newTCCMemo(os.Stat)
	for _, id := range []string{"com.alpha.app", "com.beta.app", "com.gamma.app", "COM.ALPHA.APP", "com.alpha.app.helper", "com.none"} {
		want := legacyGrantedPermissions(t, dbs, id)
		if got := d.grantedPermissionsIn(dbs, id); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
	if got := d.grantedPermissionsIn(dbs, "com.alpha.app"); strings.Join(got, ",") != "Camera,Microphone,SystemPolicyAllFiles" {
		t.Errorf("com.alpha.app = %q", got)
	}
	if got := d.grantedPermissionsIn(dbs, "com.alpha.app' OR '1'='1"); got != nil {
		t.Errorf("invalid bundle ID = %q, want nil", got)
	}
}

func countingRead(n *atomic.Int32, out string, err error) func(string) ([]byte, error) {
	return func(string) ([]byte, error) {
		n.Add(1)
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

func hexRow(client, service string) string {
	return fmt.Sprintf("%X|%X\n", client, service)
}

func TestTCCMemoReadsEachVersionOnce(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newTCCMemo(files.stat)
	const db = "/Library/Application Support/com.apple.TCC/TCC.db"
	files.set(db, 100, 10, 1, 10)
	var n atomic.Int32
	read := countingRead(&n, hexRow("com.a", "kTCCServiceCamera"), nil)
	for range 3 {
		g, err := memo.grants(db, read)
		if err != nil || !reflect.DeepEqual(g["com.a"], []string{"kTCCServiceCamera"}) {
			t.Fatalf("grants = %v, %v", g, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("reads = %d, want 1", n.Load())
	}

	// A write that lands only in the WAL leaves TCC.db's identity unchanged.
	files.set(db+"-wal", 4096, 11, 2, 11)
	if _, err := memo.grants(db, read); err != nil || n.Load() != 2 {
		t.Fatalf("new WAL: reads = %d, err %v, want 2", n.Load(), err)
	}
	files.set(db+"-wal", 8192, 12, 2, 12)
	if _, err := memo.grants(db, read); err != nil || n.Load() != 3 {
		t.Fatalf("grown WAL: reads = %d, want 3", n.Load())
	}
	files.set(db, 100, 10, 1, 13) // ctime only
	if _, err := memo.grants(db, read); err != nil || n.Load() != 4 {
		t.Fatalf("db ctime change: reads = %d, want 4", n.Load())
	}
	if _, err := memo.grants(db, read); err != nil || n.Load() != 4 {
		t.Fatalf("unchanged: reads = %d, want 4", n.Load())
	}
}

func TestTCCMemoWALStatErrorIsNotAbsentWAL(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	const db = "/db"
	files.set(db, 1, 1, 1, 1)
	walErr := errors.New("permission denied")
	memo := newTCCMemo(func(p string) (fs.FileInfo, error) {
		if p == db+"-wal" {
			return nil, walErr
		}
		return files.stat(p)
	})
	var n atomic.Int32
	read := countingRead(&n, hexRow("com.a", "kTCCServiceCamera"), nil)
	for i := 1; i <= 2; i++ {
		if _, err := memo.grants(db, read); err != nil || int(n.Load()) != i {
			t.Fatalf("lookup %d: reads = %d, err %v; want %d (uncached)", i, n.Load(), err, i)
		}
	}
}

func TestTCCMemoDoesNotCacheFailures(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newTCCMemo(files.stat)
	const db = "/db"
	files.set(db, 1, 1, 1, 1)
	var n atomic.Int32
	denied := countingRead(&n, "", errors.New("authorization denied"))
	for range 2 {
		if _, err := memo.grants(db, denied); err == nil {
			t.Fatal("want error")
		}
	}
	if n.Load() != 2 {
		t.Fatalf("failed reads = %d, want 2 (not cached)", n.Load())
	}
	// An unstat-able database is read every time.
	if _, err := memo.grants("/missing", countingRead(&n, "", nil)); err != nil || n.Load() != 3 {
		t.Fatalf("unstat-able: reads = %d, err %v", n.Load(), err)
	}
	if _, err := memo.grants("/missing", countingRead(&n, "", nil)); err != nil || n.Load() != 4 {
		t.Fatalf("unstat-able again: reads = %d", n.Load())
	}
}

func TestTCCMemoSharesConcurrentRead(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newTCCMemo(files.stat)
	const db = "/db"
	files.set(db, 1, 1, 1, 1)
	var n atomic.Int32
	release := make(chan struct{})
	slow := func(string) ([]byte, error) {
		n.Add(1)
		<-release
		return []byte(hexRow("com.a", "kTCCServiceCamera")), nil
	}
	var wg sync.WaitGroup
	results := make([]tccGrants, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = memo.grants(db, slow)
		}()
	}
	// Let the first lookup register its flight before releasing it.
	for n.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("reads = %d, want 1", n.Load())
	}
	for _, g := range results {
		if !reflect.DeepEqual(g["com.a"], []string{"kTCCServiceCamera"}) {
			t.Fatalf("result = %v", g)
		}
	}
}

func TestTCCMemoEvictionKeepsInFlight(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newTCCMemo(files.stat)
	var done atomic.Int32
	for i := range tccMemoMax - 1 {
		p := fmt.Sprintf("/d/%d.db", i)
		files.set(p, 1, 1, 1, 1)
		if _, err := memo.grants(p, countingRead(&done, "", nil)); err != nil {
			t.Fatal(err)
		}
	}
	files.set("/p.db", 1, 1, 1, 1)
	files.set("/q.db", 1, 1, 1, 1)
	var n atomic.Int32
	release := make(chan struct{})
	slow := func(string) ([]byte, error) {
		n.Add(1)
		<-release
		return nil, nil
	}
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = memo.grants("/p.db", slow)
	}()
	for n.Load() == 0 {
		runtime.Gosched()
	}
	// The memo is full; this lookup evicts completed entries only.
	if _, err := memo.grants("/q.db", countingRead(&done, "", nil)); err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, _ = memo.grants("/p.db", slow)
	}()
	close(release)
	<-first
	<-joined
	if n.Load() != 1 {
		t.Fatalf("in-flight read ran %d times, want 1", n.Load())
	}
	memo.mu.Lock()
	size := len(memo.m)
	memo.mu.Unlock()
	if size > tccMemoMax {
		t.Fatalf("memo size %d exceeds bound %d", size, tccMemoMax)
	}
}

func TestParseTCCGrants(t *testing.T) {
	out := hexRow("com.a", "kTCCServiceCamera") + hexRow("com.a", " x \n\n y ") + "garbage\nZZ|00\n" + hexRow("com.b", "")
	g := parseTCCGrants([]byte(out))
	want := tccGrants{"com.a": {"kTCCServiceCamera", "x", "y"}}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("parse = %#v, want %#v", g, want)
	}
}

func TestNewDetectorScopesTCCMemoToInjectedRunner(t *testing.T) {
	if d := newDetector(Options{}); d.tcc != sharedTCCGrants {
		t.Fatal("real-sqlite3 detector does not use the shared memo")
	}
	d1, d2 := newDetector(Options{Runner: newToolFake()}), newDetector(Options{Runner: newToolFake()})
	if d1.tcc == sharedTCCGrants || d1.tcc == nil || d1.tcc == d2.tcc {
		t.Fatal("injected-runner detectors must each get their own memo")
	}
}
