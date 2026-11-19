package classify

import (
	"context"
	"debug/macho"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const (
	appleCodesign = `Executable=/bin/ls
Identifier=com.apple.ls
Format=Mach-O universal (x86_64 arm64e)
CodeDirectory v=20400 size=325 flags=0x0(none) hashes=5+2 location=embedded
Platform identifier=26
Authority=macOS Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
`
	devIDCodesign = `Executable=/Applications/Foo.app/Contents/MacOS/Foo
CodeDirectory v=20500 size=645 flags=0x10000(runtime) hashes=9+7 location=embedded
Authority=Developer ID Application: Foo Inc. (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
`
	masCodesign = `CodeDirectory v=20500 size=645 flags=0x10000(runtime) hashes=9+7 location=embedded
Authority=Apple Mac OS Application Signing
Authority=Apple Worldwide Developer Relations Certification Authority
Authority=Apple Root CA
TeamIdentifier=BQR82RBBHL
`
	adhocCodesign = `Executable=/opt/homebrew/Cellar/ripgrep/15.2.0/bin/rg
CodeDirectory v=20400 size=50564 flags=0x2(adhoc) hashes=1574+2 location=embedded
Signature=adhoc
TeamIdentifier=not set
`
	unsignedCodesign = "/tmp/a.out: code object is not signed at all\n"
)

func TestParseCodesign(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		want  Signing
		claim string
	}{
		// An Apple claim is only a claim until codesign --verify settles it.
		{"apple", appleCodesign, Signing{Status: SigningUnknown}, "platform binary"},
		{"developer_id", devIDCodesign, Signing{Status: SigningDeveloperID, TeamID: "ABCDE12345", HardenedRuntime: true}, ""},
		{"mac app store", masCodesign, Signing{Status: SigningDeveloperID, TeamID: "BQR82RBBHL", HardenedRuntime: true}, ""},
		{"adhoc", adhocCodesign, Signing{Status: SigningAdhoc}, ""},
		{"unsigned", unsignedCodesign, Signing{Status: SigningUnsigned}, ""},
		{"garbage", "codesign: No such file or directory\n", Signing{Status: SigningUnknown}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCodesign(tc.out)
			if got.Signing != tc.want || got.Claim != tc.claim || got.Platform || got.Validated {
				t.Fatalf("parseCodesign = %+v, want %+v claim=%q and nothing granted", got, tc.want, tc.claim)
			}
		})
	}
}

func classifyOne(t *testing.T, c *Classifier, in Input) Result {
	t.Helper()
	res, err := c.Classify(context.Background(), []Input{in})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return res[0]
}

func rustData() []byte {
	return []byte("xx/rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/core/src/panicking.rs\x00RUST_BACKTRACE\x00")
}

func TestBinaryRuntime(t *testing.T) {
	goMacho := buildMachO(macho.CpuArm64, sect{"__DATA", "__go_buildinfo", []byte("\xff Go buildinf:")})
	rustMacho := buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", rustData()})
	plain := buildMachO(macho.CpuArm64, sect{"__TEXT", "__cstring", []byte("hello\x00")})
	bunMacho := buildMachO(macho.CpuArm64, sect{"__BUN", "__bun", []byte("js")})
	fat := buildFat(map[macho.Cpu][]byte{
		macho.CpuAmd64: buildMachO(macho.CpuAmd64, sect{"__DATA", "__go_buildinfo", []byte("x")}),
		macho.CpuArm64: buildMachO(macho.CpuArm64, sect{"__DATA", "__go_buildinfo", []byte("x")}),
	}, macho.CpuAmd64, macho.CpuArm64)
	cases := []struct {
		name string
		data []byte
		os   hostos.Kind
		want Runtime
	}{
		{"go macho", goMacho, hostos.Linux, RuntimeGo},
		{"rust macho", rustMacho, hostos.Linux, RuntimeRust},
		{"plain macho", plain, hostos.Linux, RuntimeNative},
		{"bun macho", bunMacho, hostos.Linux, RuntimeBun},
		{"fat go", fat, hostos.Linux, RuntimeGo},
		{"go elf", buildELF(sect{name: ".go.buildinfo", data: []byte("\xff Go buildinf:")}), hostos.Linux, RuntimeGo},
		{"rust elf", buildELF(sect{name: ".rodata", data: rustData()}), hostos.Linux, RuntimeRust},
		{"plain elf", buildELF(sect{name: ".rodata", data: []byte("hello")}), hostos.Linux, RuntimeNative},
		{"not a binary", []byte("#!/bin/sh\necho hi\n"), hostos.Linux, RuntimeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Only /proc/10/exe holds the code: the classifier must read the
			// running image, not the (possibly replaced) path on disk.
			files := newFakeFS()
			files.add("/proc/10/exe", tc.data)
			c := New(Options{OS: tc.os, FS: files, Runner: proc.NewFake()})
			r := classifyOne(t, c, Input{PID: 10, PPID: 1, UID: 501, Command: "tool", ExecutablePath: "/opt/tool/bin/tool", CommandLine: "/opt/tool/bin/tool"})
			if r.Runtime != tc.want {
				t.Fatalf("runtime = %s, want %s (evidence %v)", r.Runtime, tc.want, r.Evidence)
			}
		})
	}
}

func TestFatPicksHostSliceAndSaysSo(t *testing.T) {
	fat := buildFat(map[macho.Cpu][]byte{
		macho.CpuAmd64: buildMachO(macho.CpuAmd64, sect{"__TEXT", "__const", rustData()}),
		macho.CpuArm64: buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", rustData()}),
	}, macho.CpuAmd64, macho.CpuArm64)
	files := newFakeFS()
	files.add("/usr/local/bin/fd", fat)
	v := inspectBinary(files, "/usr/local/bin/fd", DefaultMaxScanBytes)
	if v.Runtime != RuntimeRust || !strings.Contains(v.Evidence, "universal binary") {
		t.Fatalf("fat verdict = %+v", v)
	}
}

func TestRustScanIsBounded(t *testing.T) {
	data := append(make([]byte, 4096), rustData()...)
	files := newFakeFS()
	files.add("/bin/big", buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", data}))
	if v := inspectBinary(files, "/bin/big", 1024); v.Runtime != RuntimeNative {
		t.Fatalf("marker past the byte cap was found: %+v", v)
	}
	if v := inspectBinary(files, "/bin/big", 1<<20); v.Runtime != RuntimeRust {
		t.Fatalf("marker inside the cap missed: %+v", v)
	}
}

func TestCountMarkersAcrossChunkBoundary(t *testing.T) {
	data := make([]byte, 256<<10-3)
	data = append(data, []byte("/rustc/")...)
	data = append(data, make([]byte, 100)...)
	budget := int64(1 << 30)
	if got := countMarkers(strings.NewReader(string(data)), &budget); got != 1 {
		t.Fatalf("countMarkers = %d, want exactly 1", got)
	}
}

func TestUnreadableBinaryIsUnknown(t *testing.T) {
	files := newFakeFS()
	files.add("/proc/5/exe", []byte("x"))
	files.deny["/proc/5/exe"] = true
	c := New(Options{OS: hostos.Linux, FS: files})
	res, err := c.Classify(context.Background(), []Input{
		{PID: 5, Command: "secret", ExecutablePath: "/usr/local/bin/secret", CommandLine: "secret"},
		{PID: 6, Command: "gone", ExecutablePath: "/nope/gone", CommandLine: "gone"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, r := range res {
		if r.Runtime != RuntimeUnknown || !strings.Contains(strings.Join(r.Evidence, " "), "unreadable") {
			t.Fatalf("result = %+v, want unknown with evidence", r)
		}
	}
}

func TestInterpreterAndElectronRuntime(t *testing.T) {
	files := newFakeFS()
	files.add("/Applications/Slack.app/Contents/Info.plist", infoPlist("com.tinyspeck.slackmacgap", "Slack"))
	files.add("/Applications/Slack.app/Contents/MacOS/Slack", []byte("bin"))
	files.add("/Applications/Slack.app/Contents/Frameworks/Electron Framework.framework/Electron Framework", []byte("fw"))
	helper := "/Applications/Slack.app/Contents/Frameworks/Slack Helper (Renderer).app/Contents/MacOS/Slack Helper (Renderer)"
	files.add(helper, []byte("bin"))
	for _, p := range []string{"/usr/bin/java", "/opt/homebrew/bin/node", "/opt/homebrew/bin/bun", "/opt/homebrew/bin/deno",
		"/usr/bin/python3", "/opt/homebrew/bin/python3.12", "/usr/bin/ruby", "/bin/zsh", "/opt/homebrew/bin/fish"} {
		files.add(p, []byte("bin"))
	}
	c := New(Options{OS: hostos.Darwin, FS: files, Runner: codesignFake(adhocCodesign)})
	cases := []struct {
		exe, cmdline string
		want         Runtime
		role         Role
	}{
		{"/usr/bin/java", "/usr/bin/java -jar app.jar", RuntimeJVM, RoleCLI},
		{"/opt/homebrew/bin/node", "node server.js", RuntimeNode, RoleCLI},
		{"/opt/homebrew/bin/bun", "bun run dev", RuntimeBun, RoleCLI},
		{"/opt/homebrew/bin/deno", "deno run -A main.ts", RuntimeDeno, RoleCLI},
		{"/usr/bin/python3", "python3 x.py", RuntimePython, RoleCLI},
		{"/opt/homebrew/bin/python3.12", "python3.12", RuntimePython, RoleCLI},
		{"/usr/bin/ruby", "ruby x.rb", RuntimeRuby, RoleCLI},
		{"/bin/zsh", "-zsh", RuntimeShell, RoleShell},
		{"/opt/homebrew/bin/fish", "fish", RuntimeShell, RoleShell},
		{"/Applications/Slack.app/Contents/MacOS/Slack", "/Applications/Slack.app/Contents/MacOS/Slack", RuntimeElectron, RoleApp},
		{helper, helper + " --type=renderer", RuntimeElectron, RoleAppHelper},
	}
	for _, tc := range cases {
		t.Run(tc.exe, func(t *testing.T) {
			r := classifyOne(t, c, Input{PID: 100, PPID: 1, UID: 501, Command: tc.exe[strings.LastIndex(tc.exe, "/")+1:], ExecutablePath: tc.exe, CommandLine: tc.cmdline})
			if r.Runtime != tc.want || r.Role != tc.role {
				t.Fatalf("got role=%s runtime=%s, want role=%s runtime=%s (evidence %v)", r.Role, r.Runtime, tc.role, tc.want, r.Evidence)
			}
		})
	}
	r := classifyOne(t, c, Input{PID: 7, ExecutablePath: helper, CommandLine: helper})
	if r.App == nil || r.App.Name != "Slack" || r.App.BundleID != "com.tinyspeck.slackmacgap" || r.App.BundlePath != "/Applications/Slack.app" {
		t.Fatalf("helper app = %+v, want outermost Slack.app", r.App)
	}
}

// TestRolePrecedence pins every precedence rule documented in role.go.
func TestRolePrecedence(t *testing.T) {
	files := newFakeFS()
	add := func(p string) { files.add(p, buildMachO(macho.CpuArm64, sect{"__TEXT", "__cstring", []byte("c\x00")})) }
	files.add("/Applications/Ghostty.app/Contents/Info.plist", infoPlist("com.mitchellh.ghostty", "ghostty"))
	files.add("/Applications/Warp.app/Contents/Info.plist", infoPlist("dev.warp.Warp-Stable", "stable"))
	files.add("/Applications/Ollama.app/Contents/Info.plist", infoPlist("com.electron.ollama", "Ollama"))
	files.add("/Applications/Xcode.app/Contents/Info.plist", infoPlist("com.apple.dt.Xcode", "Xcode"))
	for _, p := range []string{
		"/Users/a/.local/share/claude/versions/2.1.220", "/opt/homebrew/bin/node", "/opt/homebrew/bin/python3",
		"/opt/homebrew/bin/codex", "/Applications/Ghostty.app/Contents/MacOS/ghostty", "/Applications/Warp.app/Contents/MacOS/stable",
		"/opt/homebrew/bin/tmux", "/bin/zsh", "/Applications/Ollama.app/Contents/Resources/ollama",
		"/Applications/Ollama.app/Contents/MacOS/Ollama", "/System/Library/CoreServices/Dock.app/Contents/MacOS/Dock",
		"/usr/libexec/trustd", "/usr/sbin/cfprefsd", "/bin/ls", "/usr/bin/sudo",
		"/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild",
		"/opt/homebrew/Cellar/python@3.12/3.12.1/Frameworks/Python.framework/Versions/3.12/Resources/Python.app/Contents/MacOS/Python",
		"/Users/a/go/bin/gopls", "/Applications/Ghostty.app/Contents/MacOS/ghostty-helper",
	} {
		add(p)
	}
	c := New(Options{OS: hostos.Darwin, FS: files, Runner: proc.NewFake().
		On(proc.MatchExact("codesign", "-dv", "--verbose=2", "/bin/ls"), proc.Response{Result: proc.Result{Stderr: []byte(appleCodesign)}}).
		On(proc.MatchExact("codesign", "-dv", "--verbose=2", "/usr/bin/sudo"), proc.Response{Result: proc.Result{Stderr: []byte(appleCodesign)}}).
		On(proc.MatchPrefix("codesign"), proc.Response{Result: proc.Result{Stderr: []byte(adhocCodesign)}})})

	claudeApp := "/Users/a/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude"
	add(claudeApp)
	pyFw := "/opt/homebrew/Cellar/python@3.12/3.12.1/Frameworks/Python.framework/Versions/3.12/Resources/Python.app/Contents/MacOS/Python"
	cases := []struct {
		name    string
		in      Input
		role    Role
		runtime Runtime
	}{
		{"native claude by path shape", Input{Command: "claude", ExecutablePath: "/Users/a/.local/share/claude/versions/2.1.220", CommandLine: "claude --resume"}, RoleAIAgent, RuntimeNative},
		{"agent npm package on node", Input{Command: "node", ExecutablePath: "/opt/homebrew/bin/node", CommandLine: "node /opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, RoleAIAgent, RuntimeNode},
		{"agent script on node", Input{Command: "node", ExecutablePath: "/opt/homebrew/bin/node", CommandLine: "node --no-warnings /opt/homebrew/bin/codex exec"}, RoleAIAgent, RuntimeNode},
		{"agent python module", Input{Command: "python3", ExecutablePath: "/opt/homebrew/bin/python3", CommandLine: "python3 -m aider --model x"}, RoleAIAgent, RuntimePython},
		{"agent word in args is not an agent", Input{Command: "node", ExecutablePath: "/opt/homebrew/bin/node", CommandLine: "node server.js --name claude"}, RoleCLI, RuntimeNode},
		{"agent by exe", Input{Command: "codex", ExecutablePath: "/opt/homebrew/bin/codex", CommandLine: "codex"}, RoleAIAgent, RuntimeNative},
		{"agent inside app bundle", Input{Command: "ollama", ExecutablePath: "/Applications/Ollama.app/Contents/Resources/ollama", CommandLine: "ollama serve"}, RoleAIAgent, RuntimeNative},
		{"agent shipped as its own app", Input{Command: "claude", ExecutablePath: claudeApp}, RoleAIAgent, RuntimeNative},
		{"terminal app by exe", Input{Command: "ghostty", ExecutablePath: "/Applications/Ghostty.app/Contents/MacOS/ghostty"}, RoleTerminal, RuntimeNative},
		{"terminal app by bundle name", Input{Command: "stable", ExecutablePath: "/Applications/Warp.app/Contents/MacOS/stable"}, RoleTerminal, RuntimeNative},
		{"terminal helper stays helper", Input{Command: "ghostty-helper", ExecutablePath: "/Applications/Ghostty.app/Contents/MacOS/ghostty-helper"}, RoleAppHelper, RuntimeNative},
		{"multiplexer", Input{Command: "tmux", ExecutablePath: "/opt/homebrew/bin/tmux"}, RoleTerminal, RuntimeNative},
		{"shell", Input{Command: "-zsh", ExecutablePath: "/bin/zsh"}, RoleShell, RuntimeShell},
		{"app main", Input{Command: "Ollama", ExecutablePath: "/Applications/Ollama.app/Contents/MacOS/Ollama"}, RoleApp, RuntimeNative},
		{"os bundle is system", Input{Command: "Dock", ExecutablePath: "/System/Library/CoreServices/Dock.app/Contents/MacOS/Dock"}, RoleSystem, RuntimeNative},
		{"libexec daemon", Input{Command: "trustd", ExecutablePath: "/usr/libexec/trustd"}, RoleSystem, RuntimeNative},
		{"sbin daemon", Input{Command: "cfprefsd", ExecutablePath: "/usr/sbin/cfprefsd"}, RoleSystem, RuntimeNative},
		{"user platform tool is cli", Input{UID: 501, Command: "ls", ExecutablePath: "/bin/ls"}, RoleCLI, RuntimeNative},
		{"root platform tool is system", Input{UID: 0, Command: "sudo", ExecutablePath: "/usr/bin/sudo"}, RoleSystem, RuntimeNative},
		{"xcode toolchain is cli", Input{Command: "xcodebuild", ExecutablePath: "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild"}, RoleCLI, RuntimeNative},
		{"framework python launcher is cli", Input{Command: "Python", ExecutablePath: pyFw, CommandLine: pyFw + " x.py"}, RoleCLI, RuntimePython},
		{"plain user binary", Input{UID: 501, Command: "gopls", ExecutablePath: "/Users/a/go/bin/gopls"}, RoleCLI, RuntimeNative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.in.CommandLine == "" {
				tc.in.CommandLine = tc.in.ExecutablePath
			}
			if !strings.HasPrefix(tc.name, "root") {
				tc.in.UID = 501
			}
			r := classifyOne(t, c, tc.in)
			if r.Role != tc.role || r.Runtime != tc.runtime {
				t.Fatalf("got role=%s runtime=%s, want role=%s runtime=%s (evidence %v)", r.Role, r.Runtime, tc.role, tc.runtime, r.Evidence)
			}
		})
	}
	xc := classifyOne(t, c, Input{UID: 501, Command: "xcodebuild", ExecutablePath: "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild"})
	if xc.App == nil || xc.App.BundleID != "com.apple.dt.Xcode" {
		t.Fatalf("xcode toolchain app = %+v, want Xcode", xc.App)
	}
	py := classifyOne(t, c, Input{UID: 501, Command: "Python", ExecutablePath: pyFw})
	if py.App != nil {
		t.Fatalf("framework-internal Python.app reported as app %+v", py.App)
	}
}

func TestLinuxRoles(t *testing.T) {
	files := newFakeFS()
	elfBin := buildELF(sect{name: ".rodata", data: []byte("x")})
	for _, pid := range []string{"300", "302", "303", "305"} {
		files.add("/proc/"+pid+"/exe", elfBin)
	}
	// A real file at the argv[0] path: it must still not be trusted.
	files.add("/usr/sbin/sshd", elfBin)
	fake := proc.NewFake()
	c := New(Options{OS: hostos.Linux, FS: files, Runner: fake})
	cases := []struct {
		in      Input
		role    Role
		runtime Runtime
	}{
		{Input{PID: 40, PPID: 2, Command: "kworker/0:1", CommandLine: "[kworker/0:1]"}, RoleSystem, RuntimeNative},
		{Input{PID: 300, PPID: 1, UID: 0, Command: "systemd-journal", ExecutablePath: "/usr/lib/systemd/systemd-journald", CommandLine: "/usr/lib/systemd/systemd-journald"}, RoleSystem, RuntimeNative},
		// exec -a /usr/sbin/sshd ./evil as root: no visible executable, so
		// argv[0] decides nothing about system.
		{Input{PID: 301, PPID: 1, UID: 0, Command: "sshd", CommandLine: "/usr/sbin/sshd -D"}, RoleCLI, RuntimeUnknown},
		{Input{PID: 302, PPID: 1, UID: 1000, Command: "sshd", ExecutablePath: "/usr/sbin/sshd", CommandLine: "sshd"}, RoleCLI, RuntimeNative},
		{Input{PID: 303, PPID: 1, UID: 1000, Command: "htop", ExecutablePath: "/usr/bin/htop", CommandLine: "htop"}, RoleCLI, RuntimeNative},
		// Interpreter guessed from the comm name only, and marked unverified.
		{Input{PID: 304, PPID: 1, UID: 1000, Command: "python3", CommandLine: "python3 x.py"}, RoleCLI, RuntimePython},
		// Root daemon whose argv[0] claims a system path; exe says otherwise.
		{Input{PID: 305, PPID: 1, UID: 0, Command: "sshd", ExecutablePath: "/tmp/evil", CommandLine: "/usr/sbin/sshd -D"}, RoleCLI, RuntimeNative},
	}
	for _, tc := range cases {
		r := classifyOne(t, c, tc.in)
		if r.Role != tc.role || r.Runtime != tc.runtime {
			t.Errorf("pid %d: role=%s runtime=%s, want %s/%s (evidence %v)", tc.in.PID, r.Role, r.Runtime, tc.role, tc.runtime, r.Evidence)
		}
		if r.Signing != nil || r.App != nil {
			t.Errorf("%s: Linux result carries signing/app: %+v", tc.in.Command, r)
		}
		if tc.in.PID == 304 && !strings.Contains(strings.Join(r.Evidence, " "), "from argv[0], unverified") {
			t.Errorf("argv[0] runtime guess not marked unverified: %v", r.Evidence)
		}
	}
	if fake.CallCount() != 0 {
		t.Fatalf("Linux classification ran %d commands: %v", fake.CallCount(), fake.Calls())
	}
	if _, err := readSigning(context.Background(), hostos.Linux, fake, "/usr/bin/htop"); err == nil || fake.CallCount() != 0 {
		t.Fatalf("readSigning on Linux = %v after %d calls, want unsupported without running codesign", err, fake.CallCount())
	}
}

func TestSigningOncePerExecutable(t *testing.T) {
	files := newFakeFS()
	files.add("/Applications/Foo.app/Contents/Info.plist", infoPlist("com.foo", "Foo"))
	files.add("/Applications/Foo.app/Contents/MacOS/Foo", buildMachO(macho.CpuArm64))
	fake := codesignFake(devIDCodesign)
	c := New(Options{OS: hostos.Darwin, FS: files, Runner: fake})
	in := Input{UID: 501, Command: "Foo", ExecutablePath: "/Applications/Foo.app/Contents/MacOS/Foo"}
	res, err := c.Classify(context.Background(), []Input{in, in, in})
	if err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("codesign ran %d times for one executable", fake.CallCount())
	}
	want := Signing{Status: SigningDeveloperID, TeamID: "ABCDE12345", HardenedRuntime: true}
	if res[2].Signing == nil || *res[2].Signing != want || res[2].Role != RoleApp {
		t.Fatalf("result = %+v signing=%+v", res[2], res[2].Signing)
	}
}

func TestDiskCacheHitAndMiss(t *testing.T) {
	files := newFakeFS()
	exe := "/Users/a/go/bin/tool"
	files.add(exe, buildMachO(macho.CpuArm64, sect{"__DATA", "__go_buildinfo", []byte("x")}))
	store := newMemStore()
	in := Input{UID: 501, Command: "tool", ExecutablePath: exe}

	cold := codesignFake(adhocCodesign)
	r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: cold, Store: store}), in)
	if r.Runtime != RuntimeGo || cold.CallCount() != 1 || store.puts != 1 {
		t.Fatalf("cold: runtime=%s codesign=%d puts=%d", r.Runtime, cold.CallCount(), store.puts)
	}

	// A fresh Classifier (a second CLI run) is warm from disk: no codesign,
	// and the binary is not even opened.
	files.deny[exe] = true
	warm := codesignFake(adhocCodesign)
	r = classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: warm, Store: store}), in)
	if r.Runtime != RuntimeGo || r.Signing == nil || r.Signing.Status != SigningAdhoc || warm.CallCount() != 0 {
		t.Fatalf("warm: runtime=%s signing=%+v codesign=%d", r.Runtime, r.Signing, warm.CallCount())
	}
	delete(files.deny, exe)

	// Rebuilt binary, same size but newer mtime: must miss.
	files.files[exe[1:]].ModTime = fixtureTime.Add(time.Minute)
	files.files[exe[1:]].Data = buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", rustData()})
	missMtime := codesignFake(adhocCodesign)
	r = classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: missMtime, Store: store}), in)
	if r.Runtime != RuntimeRust || missMtime.CallCount() != 1 {
		t.Fatalf("after mtime change: runtime=%s codesign=%d", r.Runtime, missMtime.CallCount())
	}

	// Size change with the same mtime also misses.
	files.files[exe[1:]].Data = buildMachO(macho.CpuArm64, sect{"__DATA", "__go_buildinfo", []byte("xy")})
	missSize := codesignFake(adhocCodesign)
	r = classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: missSize, Store: store}), in)
	if r.Runtime != RuntimeGo || missSize.CallCount() != 1 {
		t.Fatalf("after size change: runtime=%s codesign=%d", r.Runtime, missSize.CallCount())
	}
}

func TestRunnerFailureIsNotPersisted(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/x", buildMachO(macho.CpuArm64))
	store := newMemStore()
	r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: proc.NewFake(), Store: store}), Input{UID: 501, Command: "x", ExecutablePath: "/opt/x"})
	if r.Signing == nil || r.Signing.Status != SigningUnknown || store.puts != 0 {
		t.Fatalf("signing=%+v puts=%d, want unknown and nothing persisted", r.Signing, store.puts)
	}
}

func TestCancelledContext(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/x", buildMachO(macho.CpuArm64))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := codesignFake(adhocCodesign)
	res, err := New(Options{OS: hostos.Darwin, FS: files, Runner: fake}).Classify(ctx, []Input{{UID: 501, Command: "x", ExecutablePath: "/opt/x"}})
	if err == nil || len(res) != 1 || res[0].Runtime != RuntimeUnknown || fake.CallCount() != 0 {
		t.Fatalf("err=%v res=%+v calls=%d", err, res, fake.CallCount())
	}
}
