package crashreport

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const (
	appUUID  = "11111111-2222-3333-4444-555555555555"
	libPath  = "/Applications/MyApp.app/Contents/Frameworks/Lib.framework/Versions/A/Lib"
	libUUID  = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	homeDir  = "/Users/me"
	mainSrc  = "/Users/me/src/MyApp/main.swift"
	appDSYM  = "/Applications/MyApp.app.dSYM/Contents/Resources/DWARF/MyApp"
	atosMain = "main (in MyApp) (/Users/me/src/MyApp/main.swift:42)"
)

// dwarfMachO is thinMachO plus an empty __DWARF segment, like an unstripped
// binary or a dSYM's DWARF file.
func dwarfMachO(uuid string) []byte {
	b := thinMachO(macho.CpuArm64, 0, uuid)
	le := binary.LittleEndian
	le.PutUint32(b[16:], 2)     // ncmds
	le.PutUint32(b[20:], 24+72) // sizeofcmds
	var seg bytes.Buffer
	_ = binary.Write(&seg, le, []uint32{uint32(macho.LoadCmdSegment64), 72})
	name := make([]byte, 16)
	copy(name, "__DWARF")
	seg.Write(name)
	seg.Write(make([]byte, 72-8-16))
	return append(b, seg.Bytes()...)
}

type memFS map[string][]byte

type memInfo struct {
	name string
	dir  bool
}

func (i memInfo) Name() string               { return i.name }
func (i memInfo) Size() int64                { return 0 }
func (i memInfo) Mode() fs.FileMode          { return 0 }
func (i memInfo) ModTime() time.Time         { return time.Time{} }
func (i memInfo) IsDir() bool                { return i.dir }
func (i memInfo) Sys() any                   { return nil }
func (i memInfo) Type() fs.FileMode          { return 0 }
func (i memInfo) Info() (fs.FileInfo, error) { return i, nil }

func (m memFS) Open(p string) (BinaryFile, error) { return memOpener(m)(p) }

func (m memFS) Stat(p string) (fs.FileInfo, error) {
	if _, ok := m[p]; ok {
		return memInfo{name: path.Base(p)}, nil
	}
	for k := range m {
		if strings.HasPrefix(k, p+"/") {
			return memInfo{name: path.Base(p), dir: true}, nil
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
}

func (m memFS) ReadDir(p string) ([]fs.DirEntry, error) {
	seen := map[string]bool{}
	var out []fs.DirEntry
	for k := range m {
		rest, ok := strings.CutPrefix(k, p+"/")
		if !ok {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			out = append(out, memInfo{name: name, dir: isDir})
		}
	}
	if len(out) == 0 {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// fakeRunner answers atos and mdfind through funcs and records every call.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []proc.Cmd
	atos   func(ctx context.Context, args []string) (string, error)
	mdfind func(query string) string
}

func (f *fakeRunner) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	switch {
	case cmd.Name == "atos" && f.atos != nil:
		out, err := f.atos(ctx, cmd.Args)
		return proc.Result{Stdout: []byte(out)}, err
	case cmd.Name == "mdfind" && f.mdfind != nil:
		return proc.Result{Stdout: []byte(f.mdfind(cmd.Args[0]))}, nil
	}
	return proc.Result{}, nil
}

func (f *fakeRunner) named(name string) []proc.Cmd {
	var out []proc.Cmd
	for _, c := range f.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func atosReturning(lines ...string) func(context.Context, []string) (string, error) {
	return func(context.Context, []string) (string, error) { return strings.Join(lines, "\n") + "\n", nil }
}

func detail(img Image, offset uint64) FrameDetail {
	d := FrameDetail{Image: img.Name, ImagePath: img.Path, ImageUUID: img.UUID, Arch: img.Arch, ImageBase: img.Base, ImageOffset: hexAddr(offset)}
	var base uint64
	for _, c := range strings.TrimPrefix(img.Base, "0x") {
		base = base*16 + uint64(strings.IndexRune("0123456789abcdef", c))
	}
	d.Address = hexAddr(base + offset)
	return d
}

// sourceReport has MyApp (two distinct offsets, one repeated across
// threads), a shared-cache libsystem_kernel frame and a /System AppKit frame.
func sourceReport() *Report {
	app := Image{Name: "MyApp", Path: appPath, UUID: appUUID, Arch: "arm64", Base: "0x100000000"}
	kernel := Image{Name: "libsystem_kernel.dylib", Path: kernelPath, UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Arch: "arm64e", Base: "0x18e0b0000", inSharedCache: true}
	appKit := Image{Name: "AppKit", Path: "/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit", UUID: "44444444-0000-0000-0000-000000000000", Arch: "arm64e", Base: "0x190000000"}
	return &Report{
		Images: []Image{app, kernel, appKit},
		Threads: []Thread{
			{FrameDetails: []FrameDetail{detail(app, 0x4000)}},
			{FrameDetails: []FrameDetail{detail(kernel, 0x92cc), detail(appKit, 0x10), detail(app, 0x29f50), detail(app, 0x4000), {ImageOffset: "0x75bcd15"}}},
		},
	}
}

func darwinOpts(fsys memFS, r *fakeRunner) SourceLineOptions {
	return SourceLineOptions{OS: hostos.Darwin, Runner: r, FS: fsys, Home: homeDir, Timeout: time.Second}
}

func TestParseLocation(t *testing.T) {
	for _, c := range []struct {
		in        string
		file      string
		line, col int
	}{
		{"/src/File.swift:42", "/src/File.swift", 42, 0},
		{"/src/File.swift:42:7", "/src/File.swift", 42, 7},
		{"File.swift:42", "File.swift", 42, 0},
		{"", "", 0, 0},
		{"File.swift", "", 0, 0},
	} {
		f, l, col := parseLocation(c.in)
		if f != c.file || l != c.line || col != c.col {
			t.Errorf("parseLocation(%q) = %q, %d, %d; want %q, %d, %d", c.in, f, l, col, c.file, c.line, c.col)
		}
	}
}

func TestResolveSourceLinesFromBinary(t *testing.T) {
	fsys := memFS{appPath: dwarfMachO(appUUID), mainSrc: []byte("// main")}
	run := &fakeRunner{atos: atosReturning(atosMain, "helper() (in MyApp) (/Users/me/src/MyApp/Gone.swift:10:7)")}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))

	atos := run.named("atos")
	if len(atos) != 1 {
		t.Fatalf("atos calls = %d, want one for MyApp only: %v", len(atos), run.calls)
	}
	want := "-o " + appPath + " -l 0x100000000 -arch arm64 -fullPath 0x100004000 0x100029f50"
	if got := strings.Join(atos[0].Args, " "); got != want {
		t.Errorf("atos args = %q, want %q", got, want)
	}
	if n := len(run.named("mdfind")); n != 0 {
		t.Errorf("mdfind calls = %d, want 0 when the binary has debug info", n)
	}

	main := r.Threads[0].FrameDetails[0]
	if main.File != mainSrc || main.Line != 42 || main.Column != 0 || main.ResolvedBy != ResolvedByBinary {
		t.Errorf("main frame = %+v", main)
	}
	if dup := r.Threads[1].FrameDetails[3]; dup.File != mainSrc || dup.ResolvedBy != ResolvedByBinary {
		t.Errorf("repeated offset not filled from cache: %+v", dup)
	}
	gone := r.Threads[1].FrameDetails[2]
	if gone.File != "" || gone.Line != 10 || gone.Column != 7 || gone.ResolvedBy != ResolvedByBinary {
		t.Errorf("missing source file should drop file only: %+v", gone)
	}
	for _, i := range []int{0, 1, 4} {
		if d := r.Threads[1].FrameDetails[i]; d.ResolvedBy != ResolvedByNone || d.File != "" || d.Line != 0 {
			t.Errorf("frame %d = %+v, want resolved_by none", i, d)
		}
	}
}

func TestResolveSourceLinesNoLocation(t *testing.T) {
	fsys := memFS{appPath: dwarfMachO(appUUID)}
	run := &fakeRunner{atos: atosReturning("main (in MyApp) + 12", "0x100029f50")}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))
	for _, d := range []FrameDetail{r.Threads[0].FrameDetails[0], r.Threads[1].FrameDetails[2]} {
		if d.ResolvedBy != ResolvedByNone || d.Line != 0 || d.File != "" {
			t.Errorf("frame without (file:line) = %+v, want none", d)
		}
	}
}

func TestResolveSourceLinesDSYMByMdfind(t *testing.T) {
	const found = "/Users/me/Builds/MyApp.app.dSYM"
	dwarf := found + "/Contents/Resources/DWARF/MyApp"
	fsys := memFS{
		appPath: thinMachO(macho.CpuArm64, 0, appUUID), // stripped
		dwarf:   dwarfMachO(appUUID),
		mainSrc: nil,
		appDSYM: dwarfMachO("99999999-0000-0000-0000-000000000000"), // stale sibling
	}
	var query string
	run := &fakeRunner{
		atos:   atosReturning(atosMain, ""),
		mdfind: func(q string) string { query = q; return found + "\n" },
	}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))
	if query != "com_apple_xcode_dsym_uuids == "+strings.ToUpper(appUUID) {
		t.Errorf("mdfind query = %q", query)
	}
	atos := run.named("atos")
	if len(atos) != 1 || atos[0].Args[1] != dwarf {
		t.Fatalf("atos calls = %v, want -o %s", atos, dwarf)
	}
	if d := r.Threads[0].FrameDetails[0]; d.ResolvedBy != ResolvedByDSYM || d.File != mainSrc || d.Line != 42 {
		t.Errorf("frame = %+v", d)
	}
}

func TestResolveSourceLinesSiblingDSYM(t *testing.T) {
	fsys := memFS{appDSYM: dwarfMachO(appUUID), mainSrc: nil}
	run := &fakeRunner{atos: atosReturning(atosMain, "")}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))
	atos := run.named("atos")
	if len(atos) != 1 || atos[0].Args[1] != appDSYM {
		t.Fatalf("atos calls = %v, want -o %s", atos, appDSYM)
	}
	if n := len(run.named("mdfind")); n != 0 {
		t.Errorf("mdfind calls = %d, want 0 after a sibling dSYM matched", n)
	}
}

func TestResolveSourceLinesXcodeArchive(t *testing.T) {
	dwarf := homeDir + "/Library/Developer/Xcode/Archives/2026-11-20/MyApp 11-20-26.xcarchive/dSYMs/MyApp.app.dSYM/Contents/Resources/DWARF/MyApp"
	other := homeDir + "/Library/Developer/Xcode/Archives/2026-11-21/Other.xcarchive/dSYMs/Other.app.dSYM/Contents/Resources/DWARF/Other"
	fsys := memFS{dwarf: dwarfMachO(appUUID), other: dwarfMachO(appUUID)}
	run := &fakeRunner{atos: atosReturning(atosMain, "")}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))
	atos := run.named("atos")
	if len(atos) != 1 || atos[0].Args[1] != dwarf {
		t.Fatalf("atos calls = %v, want -o %s", atos, dwarf)
	}
	if d := r.Threads[0].FrameDetails[0]; d.ResolvedBy != ResolvedByDSYM || d.File != "" || d.Line != 42 {
		t.Errorf("frame = %+v (source file absent, so no file)", d)
	}
}

func TestResolveSourceLinesDerivedDataAndNothing(t *testing.T) {
	dwarf := homeDir + "/Library/Developer/Xcode/DerivedData/MyApp-abc/Build/Products/Debug/MyApp.app.dSYM/Contents/Resources/DWARF/MyApp"
	run := &fakeRunner{atos: atosReturning(atosMain, "")}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(memFS{dwarf: dwarfMachO(appUUID)}, run))
	if atos := run.named("atos"); len(atos) != 1 || atos[0].Args[1] != dwarf {
		t.Fatalf("atos calls = %v, want -o %s", atos, dwarf)
	}

	run = &fakeRunner{atos: atosReturning(atosMain, "")}
	r = sourceReport()
	r.ResolveSourceLines(context.Background(), darwinOpts(memFS{}, run))
	if n := len(run.named("atos")); n != 0 {
		t.Errorf("atos calls = %d, want 0 with no debug info", n)
	}
	if d := r.Threads[0].FrameDetails[0]; d.ResolvedBy != ResolvedByNone {
		t.Errorf("frame = %+v", d)
	}
}

func TestResolveSourceLinesOneCallPerImage(t *testing.T) {
	r := sourceReport()
	lib := Image{Name: "Lib", Path: libPath, UUID: libUUID, Arch: "arm64", Base: "0x101000000"}
	r.Images = append(r.Images, lib)
	r.Threads[0].FrameDetails = append(r.Threads[0].FrameDetails, detail(lib, 0x10), detail(lib, 0x20))
	fsys := memFS{appPath: dwarfMachO(appUUID), libPath: dwarfMachO(libUUID)}
	run := &fakeRunner{atos: atosReturning("a", "b")}
	r.ResolveSourceLines(context.Background(), darwinOpts(fsys, run))
	atos := run.named("atos")
	if len(atos) != 2 {
		t.Fatalf("atos calls = %d, want one per image", len(atos))
	}
	if got := strings.Join(atos[1].Args, " "); !strings.HasSuffix(got, "-fullPath 0x101000010 0x101000020") {
		t.Errorf("Lib atos args = %q", got)
	}
}

func TestResolveSourceLinesDeadline(t *testing.T) {
	var sawDeadline bool
	run := &fakeRunner{atos: func(ctx context.Context, _ []string) (string, error) {
		_, sawDeadline = ctx.Deadline()
		<-ctx.Done()
		return "", ctx.Err()
	}}
	opts := darwinOpts(memFS{appPath: dwarfMachO(appUUID)}, run)
	opts.Timeout = 20 * time.Millisecond
	r := sourceReport()
	start := time.Now()
	r.ResolveSourceLines(context.Background(), opts)
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("ResolveSourceLines took %v; the per-call deadline was not honored", el)
	}
	if !sawDeadline {
		t.Error("atos ran without a deadline")
	}
	if d := r.Threads[0].FrameDetails[0]; d.ResolvedBy != ResolvedByNone {
		t.Errorf("frame = %+v, want none after timeout", d)
	}
}

func TestResolveSourceLinesUnsupportedOffDarwin(t *testing.T) {
	run := &fakeRunner{}
	r := sourceReport()
	r.ResolveSourceLines(context.Background(), SourceLineOptions{OS: hostos.Linux, Runner: run, FS: memFS{}})
	if len(run.calls) != 0 {
		t.Errorf("runner calls on linux = %v", run.calls)
	}
	u := r.SourceLinesUnsupported
	if u == nil || u.Error != "unsupported_platform" || u.OS != "linux" || u.Feature == "" {
		t.Fatalf("source_lines_unsupported = %+v", u)
	}
	for _, th := range r.Threads {
		for _, d := range th.FrameDetails {
			if d.ResolvedBy != ResolvedByNone {
				t.Errorf("frame = %+v, want none", d)
			}
		}
	}
}

func TestSourceLineFieldsAbsentByDefault(t *testing.T) {
	out, err := json.Marshal(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"resolved_by"`, `"file"`, `"line"`, `"column"`, `"source_lines_unsupported"`} {
		if bytes.Contains(out, []byte(k)) {
			t.Errorf("default JSON contains %s", k)
		}
	}
}

func TestDSYMNamedFor(t *testing.T) {
	for _, c := range []struct {
		bundle, image string
		want          bool
	}{
		{"MyApp.app.dSYM", "MyApp", true},
		{"MyApp.dSYM", "MyApp", true},
		{"libfoo.dylib.dSYM", "libfoo.dylib", true},
		{"Lib.framework.dSYM", "Lib", true},
		{"MyAppKit.app.dSYM", "MyApp", false},
		{"MyApp.app", "MyApp", false},
	} {
		if got := dsymNamedFor(c.bundle, c.image); got != c.want {
			t.Errorf("dsymNamedFor(%q, %q) = %v", c.bundle, c.image, got)
		}
	}
}
