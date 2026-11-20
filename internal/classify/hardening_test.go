package classify

import (
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const (
	// An ad-hoc signature whose CodeDirectory claims a platform identifier:
	// codesign -dv displays it, codesign --verify rejects it.
	spoofPlatformCodesign = `Executable=/opt/evil
CodeDirectory v=20400 size=325 flags=0x2(adhoc) hashes=5+2 location=embedded
Platform identifier=15
Signature=adhoc
TeamIdentifier=not set
`
	spoofLeafCodesign = `Executable=/opt/evil
CodeDirectory v=20400 size=325 flags=0x0(none) hashes=5+2 location=embedded
Authority=Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
`
)

func verifyFake(display string, verifyExit int) *proc.Fake {
	return proc.NewFake().
		On(proc.MatchPrefix("codesign", "-dv"), proc.Response{Result: proc.Result{Stderr: []byte(display)}}).
		On(proc.MatchExact("codesign", "--verify", "--ignore-resources", "-R=anchor apple", "/opt/evil"), proc.Response{Result: proc.Result{ExitCode: verifyExit}})
}

func TestAppleClaimNeedsVerification(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/evil", buildMachO(macho.CpuArm64))
	in := Input{PID: 9, UID: 0, Command: "evil", ExecutablePath: "/opt/evil", CommandLine: "/opt/evil"}
	cases := []struct {
		name    string
		display string
		exit    int
		status  SigningStatus
		role    Role
		ev      string
	}{
		{"forged platform id", spoofPlatformCodesign, 3, SigningInvalid, RoleCLI, "claims platform binary but"},
		{"forged Software Signing leaf", spoofLeafCodesign, 3, SigningInvalid, RoleCLI, "claims Software Signing leaf but"},
		{"genuine platform binary", appleCodesign, 0, SigningApple, RoleSystem, "platform binary validated by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := verifyFake(tc.display, tc.exit)
			r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake}), in)
			if r.Signing == nil || r.Signing.Status != tc.status || r.Role != tc.role || r.Signing.Validated != (tc.status == SigningApple) {
				t.Fatalf("signing=%+v role=%s, want %s/%s (evidence %v)", r.Signing, r.Role, tc.status, tc.role, r.Evidence)
			}
			if ev := strings.Join(r.Evidence, "\n"); !strings.Contains(ev, tc.ev) || !strings.Contains(ev, "file on disk") {
				t.Fatalf("evidence %q lacks %q", ev, tc.ev)
			}
		})
	}
}

func TestVerifyRunsOnlyForAppleClaimsAndAppleIssuedCerts(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/x", buildMachO(macho.CpuArm64))
	for _, display := range []string{adhocCodesign, unsignedCodesign} {
		fake := codesignFake(display)
		r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake}), Input{UID: 501, Command: "x", ExecutablePath: "/opt/x"})
		if fake.CallCount() != 1 || r.Signing == nil || r.Signing.Validated {
			t.Fatalf("codesign ran %d times, signing=%+v: %v", fake.CallCount(), r.Signing, fake.Calls())
		}
		if ev := strings.Join(r.Evidence, "\n"); !strings.Contains(ev, "as displayed by codesign -dv, not validated; verdict is for the file on disk") {
			t.Fatalf("evidence = %q", ev)
		}
	}
}

func thirdPartyVerifyFake(display string, verify proc.Response) *proc.Fake {
	return proc.NewFake().
		On(proc.MatchPrefix("codesign", "-dv"), proc.Response{Result: proc.Result{Stderr: []byte(display)}}).
		On(proc.MatchExact("codesign", "--verify", "--strict", "/opt/x"), verify)
}

func TestThirdPartySignatureVerification(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/x", buildMachO(macho.CpuArm64))
	fail := func(stderr string) proc.Response {
		return proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte(stderr)}}
	}
	fail2 := func(stderr string) proc.Response {
		return proc.Response{Result: proc.Result{ExitCode: 2, Stderr: []byte(stderr)}}
	}
	cases := []struct {
		name      string
		display   string
		verify    proc.Response
		status    SigningStatus
		validated bool
		persisted bool
		ev        string
	}{
		{"intact Developer ID", devIDCodesign, proc.Response{}, SigningDeveloperID, true, true,
			"signing: developer_id, team ABCDE12345 validated by codesign --verify --strict; verdict is for the file on disk"},
		{"intact Mac App Store", masCodesign, proc.Response{}, SigningDeveloperID, true, true, "team BQR82RBBHL validated by"},
		{"tampered Developer ID", devIDCodesign, fail("/opt/x: invalid signature (code or signature have been modified)\nIn architecture: arm64\n"),
			SigningInvalid, false, true, "displays an Apple-issued certificate but codesign --verify --strict failed"},
		{"only --strict detritus", devIDCodesign, fail("/opt/x: resource fork, Finder information, or similar detritus not allowed\n"),
			SigningDeveloperID, false, true, "rejected extended attributes or resource forks"},
		{"executable vanished", devIDCodesign, fail("/opt/x: No such file or directory\n"), SigningDeveloperID, false, false, "could not run"},
		{"verify killed by a signal", devIDCodesign, proc.Response{Result: proc.Result{ExitCode: -1}}, SigningDeveloperID, false, false, "could not run"},
		{"verify usage error", devIDCodesign, fail2("codesign: internal error\n"), SigningDeveloperID, false, false, "could not run"},
		{"verify timed out", devIDCodesign, proc.Response{Err: context.DeadlineExceeded}, SigningDeveloperID, false, false, "could not run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := thirdPartyVerifyFake(tc.display, tc.verify)
			store := newMemStore()
			r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake, Store: store}), Input{UID: 501, Command: "x", ExecutablePath: "/opt/x"})
			if r.Signing == nil || r.Signing.Status != tc.status || r.Signing.Validated != tc.validated {
				t.Fatalf("signing=%+v, want %s validated=%v (calls %v)", r.Signing, tc.status, tc.validated, fake.Calls())
			}
			if (store.puts > 0) != tc.persisted {
				t.Fatalf("puts=%d, want persisted=%v", store.puts, tc.persisted)
			}
			if ev := strings.Join(r.Evidence, "\n"); !strings.Contains(ev, tc.ev) {
				t.Fatalf("evidence %q lacks %q", ev, tc.ev)
			}
		})
	}
}

func TestOnlyDetritus(t *testing.T) {
	for out, want := range map[string]bool{
		"/a: resource fork, Finder information, or similar detritus not allowed\n":                                   true,
		"/a: resource fork, Finder information, or similar detritus not allowed\nIn subcomponent: /a/b\n":            true,
		"/a: resource fork, Finder information, or similar detritus not allowed\n/a: a sealed resource is missing\n": false,
		"/a: invalid signature (code or signature have been modified)\n":                                             false,
		"": false,
	} {
		if got := onlyDetritus(out); got != want {
			t.Errorf("onlyDetritus(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestVerifyRunnerFailureIsUnknownAndNotPersisted(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/evil", buildMachO(macho.CpuArm64))
	fake := proc.NewFake().
		On(proc.MatchPrefix("codesign", "-dv"), proc.Response{Result: proc.Result{Stderr: []byte(appleCodesign)}}).
		On(proc.MatchPrefix("codesign", "--verify"), proc.Response{Err: context.DeadlineExceeded})
	store := newMemStore()
	r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake, Store: store}), Input{UID: 0, Command: "evil", ExecutablePath: "/opt/evil"})
	if r.Signing == nil || r.Signing.Status != SigningUnknown || r.Role == RoleSystem || store.puts != 0 {
		t.Fatalf("signing=%+v role=%s puts=%d", r.Signing, r.Role, store.puts)
	}
}

// classifyWithin fails the test instead of hanging when Classify blocks.
func classifyWithin(t *testing.T, c *Classifier, ctx context.Context, in []Input) ([]Result, error) {
	t.Helper()
	type out struct {
		res []Result
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := c.Classify(ctx, in)
		done <- out{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-time.After(10 * time.Second):
		t.Fatal("Classify blocked")
		return nil, nil
	}
}

func TestFIFOsAndOversizeFilesDoNotBlock(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Foo.app", "Contents")
	if err := os.MkdirAll(filepath.Join(app, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(app, "Info.plist"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	fifoExe := filepath.Join(app, "MacOS", "Foo")
	if err := syscall.Mkfifo(fifoExe, 0o755); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "Big.app", "Contents")
	if err := os.MkdirAll(filepath.Join(big, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	sparse, err := os.Create(filepath.Join(big, "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(4 << 30); err != nil {
		t.Fatal(err)
	}
	_ = sparse.Close()
	bigExe := filepath.Join(big, "MacOS", "Big")
	if err := os.WriteFile(bigExe, buildMachO(macho.CpuArm64), 0o755); err != nil {
		t.Fatal(err)
	}

	fake := codesignFake(adhocCodesign)
	c := New(Options{OS: hostos.Darwin, Runner: fake})
	res, err := classifyWithin(t, c, context.Background(), []Input{
		{PID: 1, UID: 501, Command: "Foo", ExecutablePath: fifoExe},
		{PID: 2, UID: 501, Command: "Big", ExecutablePath: bigExe},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Runtime != RuntimeUnknown || !strings.Contains(strings.Join(res[0].Evidence, " "), "not a regular file") {
		t.Fatalf("FIFO executable = %+v", res[0])
	}
	if res[1].Runtime != RuntimeNative || res[1].App == nil || res[1].App.BundleID != "" {
		t.Fatalf("oversize Info.plist bundle = %+v app=%+v", res[1], res[1].App)
	}
	for _, call := range fake.Calls() {
		if call.Args[len(call.Args)-1] == fifoExe {
			t.Fatalf("codesign was run on a FIFO: %v", call)
		}
	}

	var osfs OSFileSystem
	if _, err := osfs.Open(fifoExe); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open(FIFO) = %v, want ErrNotRegular", err)
	}
	if _, err := osfs.ReadFile(filepath.Join(app, "Info.plist"), maxPlistBytes); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("ReadFile(FIFO) = %v, want ErrNotRegular", err)
	}
	if _, err := osfs.ReadFile(filepath.Join(big, "Info.plist"), maxPlistBytes); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadFile(4 GiB) = %v, want ErrTooLarge", err)
	}
}

// cancellingFS cancels its context on the first Info.plist read.
type cancellingFS struct {
	*fakeFS
	cancel func()
	reads  int
}

func (f *cancellingFS) ReadFile(path string, limit int64) ([]byte, error) {
	f.reads++
	f.cancel()
	return f.fakeFS.ReadFile(path, limit)
}

func TestBundleReadsStopOnCancel(t *testing.T) {
	base := newFakeFS()
	var in []Input
	for i := range 20 {
		exe := fmt.Sprintf("/Applications/A%d.app/Contents/MacOS/A%d", i, i)
		base.add(fmt.Sprintf("/Applications/A%d.app/Contents/Info.plist", i), infoPlist("a", "A"))
		base.add(exe, buildMachO(macho.CpuArm64))
		in = append(in, Input{PID: i + 1, UID: 501, ExecutablePath: exe})
	}
	ctx, cancel := context.WithCancel(context.Background())
	files := &cancellingFS{fakeFS: base, cancel: cancel}
	_, err := classifyWithin(t, New(Options{OS: hostos.Darwin, FS: files, Runner: codesignFake(adhocCodesign), Workers: 1}), ctx, in)
	if !errors.Is(err, context.Canceled) || files.reads != 1 {
		t.Fatalf("err=%v after %d bundle reads, want exactly the first read before cancel stops the phase", err, files.reads)
	}
}

// linuxStat mimics syscall.Stat_t's Linux field names on any host.
type linuxStat struct {
	Dev, Ino uint64
	Ctim     struct{ Sec, Nsec int64 }
}

func TestIdentityReadsInodeAndCtime(t *testing.T) {
	st := &linuxStat{Dev: 7, Ino: 42}
	st.Ctim.Sec, st.Ctim.Nsec = 3, 4
	if got := identity(fakeInfo{sys: st}); got != (fileID{dev: 7, ino: 42, ctime: 3e9 + 4}) {
		t.Fatalf("linux-shaped identity = %+v", got)
	}
	fi, err := os.Stat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := identity(fi); got.ino == 0 || got.ctime == 0 {
		t.Fatalf("host identity = %+v, want inode and ctime", got)
	}
	if got := identity(fakeInfo{}); got != (fileID{}) {
		t.Fatalf("no Sys identity = %+v", got)
	}
}

type fakeInfo struct {
	os.FileInfo
	sys any
}

func (f fakeInfo) Sys() any { return f.sys }

func TestCacheMissesOnSameSizeSameMtimeReplacement(t *testing.T) {
	files := newFakeFS()
	exe := "/Users/a/bin/tool"
	rust := buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", rustData()})
	goBin := buildMachO(macho.CpuArm64, sect{"__DATA", "__go_buildinfo", make([]byte, len(rustData()))})
	if len(rust) != len(goBin) {
		t.Fatalf("fixtures differ in size: %d vs %d", len(rust), len(goBin))
	}
	stat := func(ino uint64, ctime int64) *linuxStat {
		s := &linuxStat{Ino: ino}
		s.Ctim.Sec = ctime
		return s
	}
	files.files[exe[1:]] = &fstest.MapFile{Data: goBin, Mode: 0o755, ModTime: fixtureTime, Sys: stat(1, 100)}
	store := newMemStore()
	in := Input{UID: 501, Command: "tool", ExecutablePath: exe}
	run := func() Runtime {
		return classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: codesignFake(adhocCodesign), Store: store}), in).Runtime
	}
	if got := run(); got != RuntimeGo {
		t.Fatalf("cold = %s", got)
	}
	// cp -p over the file: same size and mtime, new inode and ctime.
	files.files[exe[1:]] = &fstest.MapFile{Data: rust, Mode: 0o755, ModTime: fixtureTime, Sys: stat(2, 200)}
	if got := run(); got != RuntimeRust {
		t.Fatalf("after same-size same-mtime replacement = %s, want a cache miss", got)
	}
	// Rewritten in place (same inode), mtime restored: only ctime moves.
	files.files[exe[1:]] = &fstest.MapFile{Data: goBin, Mode: 0o755, ModTime: fixtureTime, Sys: stat(2, 300)}
	if got := run(); got != RuntimeGo {
		t.Fatalf("after in-place rewrite = %s, want a cache miss", got)
	}
}

// TestCacheVersionPinsEntryShape fails when the cached entry changes shape
// without a cacheVersion bump: a stale disk entry would decode into the new
// struct with fields silently zeroed. Bump cacheVersion, then update both pins.
func TestCacheVersionPinsEntryShape(t *testing.T) {
	const (
		pinnedVersion = "classify/v5"
		pinnedShape   = "5e05118381521607"
	)
	var b strings.Builder
	describeType(&b, reflect.TypeOf(exeEntry{}), map[reflect.Type]bool{})
	sum := sha256.Sum256([]byte(b.String()))
	shape := hex.EncodeToString(sum[:8])
	if cacheVersion != pinnedVersion || shape != pinnedShape {
		t.Fatalf("cacheVersion=%q shape=%s; pinned %q/%s. If exeEntry changed, bump cacheVersion and update both pins.\n%s",
			cacheVersion, shape, pinnedVersion, pinnedShape, b.String())
	}
}

func describeType(b *strings.Builder, t reflect.Type, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		b.WriteString(t.Kind().String() + " ")
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		b.WriteString(t.String() + "\n")
		return
	}
	seen[t] = true
	b.WriteString(t.String() + "{\n")
	for i := range t.NumField() {
		f := t.Field(i)
		fmt.Fprintf(b, "%s `%s` ", f.Name, f.Tag)
		describeType(b, f.Type, seen)
	}
	b.WriteString("}\n")
}

func TestAgentNeedsMoreThanAName(t *testing.T) {
	files := newFakeFS()
	bin := buildMachO(macho.CpuArm64, sect{"__TEXT", "__cstring", []byte("c\x00")})
	for _, p := range []string{"/Users/a/bin/x", "/opt/homebrew/bin/amp", "/opt/homebrew/bin/goose", "/opt/homebrew/bin/crush",
		"/opt/homebrew/bin/gemini", "/Users/a/.local/bin/goose", "/Users/a/.amp/bin/amp", "/opt/homebrew/bin/node",
		"/Applications/Goose.app/Contents/MacOS/goose"} {
		files.add(p, bin)
	}
	files.add("/Applications/Goose.app/Contents/Info.plist", infoPlist("com.block.goose", "Goose"))
	files.links["/opt/homebrew/bin/gemini"] = "/opt/homebrew/lib/node_modules/@google/gemini-cli/dist/index.js"
	c := New(Options{OS: hostos.Darwin, FS: files, Runner: codesignFake(adhocCodesign)})
	cases := []struct {
		name string
		in   Input
		want Role
	}{
		{"exec -a claude", Input{Command: "claude", ExecutablePath: "/Users/a/bin/x", CommandLine: "claude"}, RoleCLI},
		{"bare amp", Input{Command: "amp", ExecutablePath: "/opt/homebrew/bin/amp"}, RoleCLI},
		{"bare goose (pressly migrations)", Input{Command: "goose", ExecutablePath: "/opt/homebrew/bin/goose"}, RoleCLI},
		{"bare crush", Input{Command: "crush", ExecutablePath: "/opt/homebrew/bin/crush"}, RoleCLI},
		{"bare gemini binary", Input{Command: "gemini", ExecutablePath: "/opt/homebrew/bin/gemini"}, RoleCLI},
		{"goose at pressly install path", Input{Command: "goose", ExecutablePath: "/Users/a/.local/bin/goose"}, RoleCLI},
		{"amp at installer path", Input{Command: "amp", ExecutablePath: "/Users/a/.amp/bin/amp"}, RoleAIAgent},
		{"goose by bundle id", Input{Command: "goose", ExecutablePath: "/Applications/Goose.app/Contents/MacOS/goose"}, RoleAIAgent},
		{"gemini script through npm bin link", Input{Command: "node", ExecutablePath: "/opt/homebrew/bin/node", CommandLine: "node /opt/homebrew/bin/gemini"}, RoleAIAgent},
		{"argv0 only, exe hidden", Input{Command: "claude", CommandLine: "claude"}, RoleAIAgent},
		{"ambiguous argv0 only", Input{Command: "goose", CommandLine: "goose"}, RoleCLI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.UID = 501
			if tc.in.CommandLine == "" {
				tc.in.CommandLine = tc.in.ExecutablePath
			}
			r := classifyOne(t, c, tc.in)
			if r.Role != tc.want {
				t.Fatalf("role = %s, want %s (evidence %v)", r.Role, tc.want, r.Evidence)
			}
			if tc.in.ExecutablePath == "" && r.Role == RoleAIAgent && !strings.Contains(r.Evidence[0], "unverified") {
				t.Fatalf("argv[0] agent match not marked unverified: %v", r.Evidence)
			}
		})
	}
	crush := roleInput{os: hostos.Darwin, exe: "/opt/homebrew/bin/crush", goModule: "github.com/charmbracelet/crush"}
	if role, ev := decideRole(crush); role != RoleAIAgent || !strings.Contains(ev, "Go module") {
		t.Fatalf("crush with its Go module = %s (%s)", role, ev)
	}
}

func TestInterpreterScriptSkipsFlagValues(t *testing.T) {
	cases := []struct {
		cmdline string
		rt      Runtime
		want    string
	}{
		{"node --require x app.js", RuntimeNode, "app.js"},
		{"node -r ./hook.js --import tsx /opt/x/cli.js serve", RuntimeNode, "/opt/x/cli.js"},
		{"node --max-old-space-size=4096 server.js", RuntimeNode, "server.js"},
		{"node -e console.log(1)", RuntimeNode, ""},
		{"node -- app.js", RuntimeNode, "app.js"},
		{"bun --cwd /srv run dev", RuntimeBun, "dev"},
		{"bun -r ./preload.ts index.ts", RuntimeBun, "index.ts"},
		{"deno run -c deno.json -A main.ts", RuntimeDeno, "main.ts"},
		{"deno run --inspect /tmp/claude.js", RuntimeDeno, "/tmp/claude.js"},
		{"deno run --inspect-brk /tmp/claude.js", RuntimeDeno, "/tmp/claude.js"},
		{"deno run --inspect-wait=127.0.0.1:9229 /tmp/claude.js", RuntimeDeno, "/tmp/claude.js"},
		{"deno run --config deno.json /tmp/claude.js", RuntimeDeno, "/tmp/claude.js"},
		{"node --inspect /tmp/claude.js", RuntimeNode, "/tmp/claude.js"},
		{"bun --inspect /tmp/claude.js", RuntimeBun, "/tmp/claude.js"},
		{"python3 -W ignore -X dev -m aider", RuntimePython, "aider"},
		{"python3 -X utf8 tool.py", RuntimePython, "tool.py"},
		{"python3 -c print(1)", RuntimePython, ""},
	}
	for _, tc := range cases {
		if got := interpreterScript(tc.cmdline, tc.rt); got != tc.want {
			t.Errorf("interpreterScript(%q) = %q, want %q", tc.cmdline, got, tc.want)
		}
	}
}

func TestRustVerdict(t *testing.T) {
	cases := []struct {
		name string
		hits int
		syms symbolFacts
		libs []string
		want Runtime
		ev   string
	}{
		{"rust main symbol", 5, symbolFacts{rustEntry: true, locals: true}, nil, RuntimeRust, "lang_start"},
		{"C++ main with Rust linked, unstripped", 5, symbolFacts{locals: true, cxx: true}, nil, RuntimeNative, "links Rust std"},
		{"unstripped, links libc++, no lang_start", 5, symbolFacts{locals: true}, []string{"/usr/lib/libc++.1.dylib"}, RuntimeNative, "links Rust std"},
		{"unstripped LTO Rust, no lang_start", 5, symbolFacts{locals: true}, []string{"/usr/lib/libSystem.B.dylib"}, RuntimeRust, "link-time optimization"},
		{"stripped C++ (adb)", 5, symbolFacts{cxx: true}, nil, RuntimeNative, "links Rust std"},
		{"stripped, links libc++ (netsimd)", 5, symbolFacts{}, []string{"/usr/lib/libc++.1.dylib"}, RuntimeNative, "links Rust std"},
		{"stripped ObjC main linking Rust", 5, symbolFacts{}, []string{"/usr/lib/libobjc.A.dylib"}, RuntimeNative, "Swift, Objective-C or Foundation"},
		{"unstripped Swift main linking Rust", 5, symbolFacts{locals: true}, []string{"/usr/lib/swift/libswiftCore.dylib"}, RuntimeNative, "Swift, Objective-C or Foundation"},
		{"Foundation C main linking Rust", 5, symbolFacts{locals: true}, []string{"/System/Library/Frameworks/Foundation.framework/Versions/C/Foundation"}, RuntimeNative, "Swift, Objective-C or Foundation"},
		{"CoreFoundation stripped", 5, symbolFacts{}, []string{"/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"}, RuntimeNative, "Swift, Objective-C or Foundation"},
		{"Rust main with lang_start still wins over ObjC libs", 5, symbolFacts{rustEntry: true}, []string{"/usr/lib/libobjc.A.dylib"}, RuntimeRust, "lang_start"},
		{"stripped Rust (fd)", 5, symbolFacts{}, []string{"/usr/lib/libSystem.B.dylib"}, RuntimeRust, "stripped"},
		{"no markers", 0, symbolFacts{cxx: true}, nil, RuntimeNative, "no Go, Bun or Rust"},
	}
	for _, tc := range cases {
		v := rustVerdict("Mach-O", tc.hits, tc.syms, tc.libs)
		if v.Runtime != tc.want || !strings.Contains(v.Evidence, tc.ev) {
			t.Errorf("%s: %+v, want %s with %q", tc.name, v, tc.want, tc.ev)
		}
	}
	var s symbolFacts
	for _, name := range []string{
		"__RNCINvNtCsaeTlahLr9f4_3std2rt10lang_startNtNtB6_7process8ExitCodeE0Csl86PP5zXZpu_2rg", // v0
		"__ZN3std2rt10lang_start17h0123456789abcdefE",                                            // legacy
	} {
		s = symbolFacts{}
		s.add(name)
		if !s.rustEntry {
			t.Errorf("%s not recognised as std::rt::lang_start", name)
		}
	}
	s = symbolFacts{}
	s.add("__RNCNCNCNvNtCsaeTlahLr9f4_3std2rt19lang_start_internal00s_0B9_")
	s.add("___cxa_thread_atexit_impl") // Rust std on glibc imports this
	if s.rustEntry || s.cxx {
		t.Errorf("lang_start_internal or __cxa_thread_atexit_impl misread: %+v", s)
	}
	for _, name := range []string{"__ZNSt3__112basic_stringIcEE", "___gxx_personality_v0", "__Znwm", "___cxa_throw"} {
		if !cxxSymbol(name) {
			t.Errorf("%s not recognised as C++ runtime", name)
		}
	}
}

func TestAppleVerifyWithoutVerdictIsNotInvalidOrCached(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/evil", buildMachO(macho.CpuArm64))
	for _, exit := range []int{-1, 2} {
		store := newMemStore()
		fake := verifyFake(spoofPlatformCodesign, exit)
		r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake, Store: store}), Input{UID: 0, Command: "evil", ExecutablePath: "/opt/evil"})
		if r.Signing == nil || r.Signing.Status == SigningInvalid || store.puts != 0 {
			t.Fatalf("exit %d: signing=%+v puts=%d, want not invalid and not cached", exit, r.Signing, store.puts)
		}
	}
}

func TestCacheMissesWhenBundleSealChanges(t *testing.T) {
	files := newFakeFS()
	exe := "/Applications/Tool.app/Contents/MacOS/tool"
	seal := "Applications/Tool.app/Contents/_CodeSignature/CodeResources"
	files.add(exe, buildMachO(macho.CpuArm64))
	files.files[seal] = &fstest.MapFile{Data: []byte("seal-a"), Mode: 0o644, ModTime: fixtureTime}
	store := newMemStore()
	in := Input{UID: 501, Command: "tool", ExecutablePath: exe}
	run := func() int {
		fake := codesignFake(adhocCodesign)
		classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake, Store: store}), in)
		return fake.CallCount()
	}
	if run() == 0 {
		t.Fatal("cold run did not call codesign")
	}
	if n := run(); n != 0 {
		t.Fatalf("unchanged bundle re-ran codesign %d times", n)
	}
	files.files[seal] = &fstest.MapFile{Data: []byte("seal-bb"), Mode: 0o644, ModTime: fixtureTime.Add(time.Hour)}
	if run() == 0 {
		t.Fatal("changed CodeResources did not invalidate the cached signing verdict")
	}
}

func TestOnlyDetritusWithNestedSubcomponents(t *testing.T) {
	d := detritusComplaint
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"nested bundle detritus only", "/opt/X.app: " + d + "\nIn subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n/opt/X.app/Contents/Frameworks/Y.framework/Y: " + d + "\nIn architecture: arm64\n", true},
		{"nested bundle also invalid", "/opt/X.app: " + d + "\nIn subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n/opt/X.app/Contents/Frameworks/Y.framework: invalid signature (code or signature have been modified)\n", false},
		{"subcomponent header alone", "In subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n", false},
	}
	for _, tc := range cases {
		if got := onlyDetritus(tc.out); got != tc.want {
			t.Errorf("%s: onlyDetritus = %v, want %v", tc.name, got, tc.want)
		}
	}
}
