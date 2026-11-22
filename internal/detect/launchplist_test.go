package detect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

// statVersion mimics syscall.Stat_t's Linux field names on any host.
type statVersion struct {
	Dev, Ino uint64
	Ctim     struct{ Sec, Nsec int64 }
}

type memoFileInfo struct {
	fs.FileInfo
	size  int64
	mtime time.Time
	sys   *statVersion
}

func (f memoFileInfo) Size() int64        { return f.size }
func (f memoFileInfo) ModTime() time.Time { return f.mtime }
func (f memoFileInfo) Sys() any           { return f.sys }

// memoFiles is a stat seam whose per-path versions tests rewrite.
type memoFiles struct {
	mu    sync.Mutex
	infos map[string]memoFileInfo
}

func (m *memoFiles) set(path string, size, mtime int64, ino uint64, ctime int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &statVersion{Ino: ino}
	st.Ctim.Sec = ctime
	m.infos[path] = memoFileInfo{size: size, mtime: time.Unix(mtime, 0), sys: st}
}

func (m *memoFiles) stat(path string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fi, ok := m.infos[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fi, nil
}

func countingConvert(n *atomic.Int32, out string, err error) func(string) ([]byte, error) {
	return func(string) ([]byte, error) {
		n.Add(1)
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

func TestLaunchPlistMemoConvertsEachVersionOnce(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newLaunchPlistMemo(files.stat)
	const path = "/Library/LaunchAgents/com.example.agent.plist"
	files.set(path, 100, 10, 1, 10)
	var n atomic.Int32
	for range 3 {
		got, err := memo.xml(path, countingConvert(&n, "v1", nil))
		if err != nil || string(got) != "v1" {
			t.Fatalf("xml = %q, %v", got, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("conversions = %d, want 1 for an unchanged file", n.Load())
	}
	steps := []struct {
		name        string
		size, mtime int64
		ino         uint64
		ctime       int64
	}{
		{"grown", 120, 10, 1, 11},
		{"same size and mtime, rewritten in place", 120, 10, 1, 12},
		{"same size and mtime, replaced", 120, 10, 2, 12},
		{"touched", 120, 11, 2, 13},
	}
	for i, s := range steps {
		files.set(path, s.size, s.mtime, s.ino, s.ctime)
		want := s.name
		got, err := memo.xml(path, countingConvert(&n, want, nil))
		if err != nil || string(got) != want {
			t.Fatalf("%s: xml = %q, %v; want a fresh conversion", s.name, got, err)
		}
		if int(n.Load()) != i+2 {
			t.Fatalf("%s: conversions = %d, want %d", s.name, n.Load(), i+2)
		}
	}
}

func TestLaunchPlistMemoDoesNotHoldFailures(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newLaunchPlistMemo(files.stat)
	var n atomic.Int32
	// Unstattable: converted (and failing) on every lookup, as before.
	for range 2 {
		if _, err := memo.xml("/missing.plist", countingConvert(&n, "", errors.New("no such file"))); err == nil {
			t.Fatal("missing plist converted without error")
		}
	}
	if n.Load() != 2 {
		t.Fatalf("missing-file conversions = %d, want 2", n.Load())
	}
	// A failed conversion of a present file is retried on the next lookup.
	files.set("/a.plist", 1, 1, 1, 1)
	if _, err := memo.xml("/a.plist", countingConvert(&n, "", errors.New("timeout"))); err == nil {
		t.Fatal("want the conversion error")
	}
	got, err := memo.xml("/a.plist", countingConvert(&n, "ok", nil))
	if err != nil || string(got) != "ok" || n.Load() != 4 {
		t.Fatalf("after a failure xml = %q, %v, conversions = %d; want a retried conversion", got, err, n.Load())
	}
}

func TestLaunchPlistMemoSharesConcurrentConversion(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	files.set("/a.plist", 1, 1, 1, 1)
	memo := newLaunchPlistMemo(files.stat)
	var n atomic.Int32
	release := make(chan struct{})
	convert := func(string) ([]byte, error) {
		n.Add(1)
		<-release
		return []byte("x"), nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := memo.xml("/a.plist", convert); err != nil || string(got) != "x" {
				t.Errorf("xml = %q, %v", got, err)
			}
		}()
	}
	for n.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("conversions = %d, want 1 shared by concurrent lookups", n.Load())
	}
}

func TestLaunchPlistMemoIsBounded(t *testing.T) {
	files := &memoFiles{infos: map[string]memoFileInfo{}}
	memo := newLaunchPlistMemo(files.stat)
	var n atomic.Int32
	for i := range launchPlistMemoMax + 5 {
		p := fmt.Sprintf("/d/%d.plist", i)
		files.set(p, 1, 1, 1, 1)
		if _, err := memo.xml(p, countingConvert(&n, "x", nil)); err != nil {
			t.Fatal(err)
		}
	}
	if len(memo.m) > launchPlistMemoMax {
		t.Fatalf("memo holds %d entries, want at most %d", len(memo.m), launchPlistMemoMax)
	}
}

// countingRunner counts plutil conversions of plists under root.
type countingRunner struct {
	*toolFake
	root string
	n    atomic.Int32
}

func (c *countingRunner) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	if cmd.Name == "plutil" && len(cmd.Args) == 5 && strings.HasPrefix(cmd.Args[4], c.root) {
		c.n.Add(1)
	}
	return c.toolFake.Run(ctx, cmd)
}

// TestScanLoginItemsSharedMemoKeepsOutput pins that serving launchd plist
// conversions from a memo shared across detectors (one per inspected app)
// changes neither attribution nor flags, and converts each plist once.
func TestScanLoginItemsSharedMemoKeepsOutput(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	appA := "/Applications/Alpha.app"
	appB := "/Applications/Beta.app"
	plists := map[string]string{
		// Attributed to Alpha by bundle-ID prefix.
		"com.alpha.helper.plist": `<plist><dict><key>RunAtLoad</key><true/><key>KeepAlive</key><false/></dict></plist>`,
		// Attributed to Beta by its program path.
		"org.other.updater.plist": `<plist><dict><key>ProgramArguments</key><array><string>` + appB + `/Contents/MacOS/upd</string></array><key>KeepAlive</key><true/></dict></plist>`,
		// Attributed to nobody.
		"net.unrelated.plist": `<plist><dict><key>RunAtLoad</key><true/></dict></plist>`,
	}
	fake := newToolFake()
	for name, xml := range plists {
		p := filepath.Join(agents, name)
		if err := os.WriteFile(p, []byte(xml), 0o600); err != nil {
			t.Fatal(err)
		}
		fake.xmlPlist(p, xml)
	}
	apps := []struct{ path, bundleID string }{{appA, "com.alpha.app"}, {appB, "com.beta.app"}}

	scan := func(memo *launchPlistMemo) ([][]LoginItem, int32) {
		r := &countingRunner{toolFake: fake, root: home}
		var out [][]LoginItem
		for _, a := range apps {
			d := newDetector(Options{Home: home, Runner: r})
			d.launch = memo
			out = append(out, d.scanLoginItems(a.path, a.bundleID))
		}
		return out, r.n.Load()
	}
	want, unmemoized := scan(nil)
	got, memoized := scan(newLaunchPlistMemo(os.Stat))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memoized login items = %+v, want %+v", got, want)
	}
	if len(want[0]) != 1 || !want[0][0].RunAtLoad || len(want[1]) != 1 || !want[1][0].KeepAlive {
		t.Fatalf("unexpected attribution %+v", want)
	}
	if memoized != int32(len(plists)) || unmemoized <= memoized {
		t.Fatalf("plutil conversions memoized=%d unmemoized=%d, want %d (one per plist) and fewer than unmemoized", memoized, unmemoized, len(plists))
	}
}

func TestNewDetectorScopesMemoToInjectedRunner(t *testing.T) {
	if d := newDetector(Options{}); d.launch != sharedLaunchPlists {
		t.Fatal("real-plutil detector does not use the shared memo")
	}
	d1, d2 := newDetector(Options{Runner: newToolFake()}), newDetector(Options{Runner: newToolFake()})
	if d1.launch == sharedLaunchPlists || d1.launch == nil || d1.launch == d2.launch {
		t.Fatal("injected-runner detectors must each get their own memo")
	}
}
