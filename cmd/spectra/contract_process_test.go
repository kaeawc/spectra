package main

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/contracttest"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// contractProcNow is the collection time the process and lineage cases
// inject; contractProcTZ is the local zone (seconds east of UTC) they render
// start times in.
const (
	contractProcNow = "2026-11-23T10:00:00Z"
	contractProcTZ  = -21600
)

const (
	contractProcPSArgs   = "-axwwo pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command="
	contractProcCommArgs = "-axwwo pid=,comm="
	contractProcMemBytes = 17179869184
)

// contractProcDetail is one libproc answer (process.Details) in case.json.
type contractProcDetail struct {
	ThreadCount    int    `json:"thread_count"`
	BSDName        string `json:"bsd_name"`
	ExecutablePath string `json:"executable_path"`
}

// contractProcRow is one row of a synthetic macOS ps listing.
type contractProcRow struct {
	pid, ppid int
	cpu       float64
	rss, vsz  int64
	uid       int
	user      string
	ago       time.Duration // start time before contractProcNow; 0 = unparseable lstart
	cmd       string
	comm      string // argv[0] for the comm listing; empty = not listed
}

func contractProcTime(t *testing.T) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, contractProcNow)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func contractProcPS(now time.Time, rows []contractProcRow) (listing, comm string) {
	var ps, cm strings.Builder
	for _, r := range rows {
		lstart := "Xyz Nov 23 07:00:00 2026"
		if r.ago > 0 {
			lstart = now.Add(-r.ago).UTC().Format("Mon Jan _2 15:04:05 2006")
		}
		fmt.Fprintf(&ps, "%5d %5d %5.1f %8d %10d %5d %-14s %s %s\n", r.pid, r.ppid, r.cpu, r.rss, r.vsz, r.uid, r.user, lstart, r.cmd)
		if r.comm != "" {
			fmt.Fprintf(&cm, "%5d %s\n", r.pid, r.comm)
		}
	}
	ps.WriteString("  999 short row\n")
	return ps.String(), cm.String()
}

const (
	contractProcSimUDID = "0A1B2C3D-1111-2222-3333-444455556666"
	contractProcChrome  = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
)

// contractProcDarwinRows is the macOS table most process and lineage cases
// share. RSS, VSZ, CPU, start times and command names are distinct so every
// --sort key orders it without ties.
func contractProcDarwinRows() []contractProcRow {
	sim := "/Library/Developer/CoreSimulator/Devices/" + contractProcSimUDID
	return []contractProcRow{
		{1, 0, 0.6, 20480, 420000, 0, "root", 72 * time.Hour, "/sbin/launchd", "/sbin/launchd"},
		{88, 1, 12.3, 812000, 41000000, 88, "_windowserver", 71 * time.Hour, "/System/Library/PrivateFrameworks/SkyLight.framework/Resources/WindowServer -daemon", ""},
		{400, 1, 25.4, 1536000, 451000000, 501, "user", 5 * time.Hour, contractProcChrome + " --restore-last-session", contractProcChrome},
		{401, 400, 41.7, 640000, 452000000, 501, "user", 4*time.Hour + 30*time.Minute, "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper (Renderer).app/Contents/MacOS/Google Chrome Helper (Renderer) --type=renderer", "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper (Renderer).app/Contents/MacOS/Google Chrome Helper (Renderer)"},
		{500, 1, 5.2, 2100000, 433000000, 501, "user", 3 * time.Hour, "/Applications/Xcode.app/Contents/MacOS/Xcode", ""},
		{510, 500, 88.8, 302000, 412000000, 501, "user", 20 * time.Minute, "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild build -scheme App", ""},
		{600, 1, 3.3, 41000, 410000000, 501, "user", 2 * time.Hour, "/Library/Developer/CoreSimulator/Runtimes/iOS.simruntime/RuntimeRoot/sbin/launchd_sim " + sim + "/data/var/run/launchd_bootstrap.plist", ""},
		{610, 600, 18.1, 205000, 415000000, 501, "user", 110 * time.Minute, "/Library/Developer/CoreSimulator/Runtimes/iOS.simruntime/RuntimeRoot/System/Library/CoreServices/SpringBoard.app/SpringBoard", ""},
		{650, 1, 1.1, 98000, 409000000, 501, "user", 48 * time.Hour, "/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", ""},
		{700, 650, 0.2, 6100, 408000000, 501, "user", 47 * time.Hour, "-zsh", "-zsh"},
		{710, 700, 150.5, 1800000, 440000000, 501, "user", 90 * time.Minute, "/usr/bin/java -cp gradle-launcher.jar org.gradle.launcher.daemon.bootstrap.GradleDaemon 8.10", ""},
		{720, 710, 97.9, 52000, 407000000, 501, "user", 2 * time.Minute, "/usr/bin/clang -c src/main.c -o build/main.o", ""},
		{730, 700, 7.7, 401000, 421000000, 501, "user", 26 * time.Hour, "/opt/homebrew/bin/node /Users/user/.npm-global/lib/node_modules/@anthropic-ai/claude-code/cli.js", "/opt/homebrew/bin/node"},
		{740, 700, 0.0, 0, 0, 501, "user", 25 * time.Hour, "<defunct>", ""},
		{750, 1, 0.4, 7300, 406000000, 501, "user", 0, "/usr/local/bin/stale-agent --serve", "/usr/local/bin/recycled"},
	}
}

func contractProcDarwinDetails() map[string]contractProcDetail {
	return map[string]contractProcDetail{
		"1":   {ThreadCount: 4, BSDName: "launchd", ExecutablePath: "/sbin/launchd"},
		"88":  {ThreadCount: 21, BSDName: "WindowServer", ExecutablePath: "/System/Library/PrivateFrameworks/SkyLight.framework/Versions/A/Resources/WindowServer"},
		"400": {ThreadCount: 42, BSDName: "Google Chrome", ExecutablePath: contractProcChrome},
		"401": {ThreadCount: 17},
		"500": {BSDName: "Xcode"},
		"730": {ThreadCount: 11, BSDName: "node", ExecutablePath: "/opt/homebrew/bin/node"},
	}
}

func contractProcDetailMap(in map[string]contractProcDetail) map[int]process.Details {
	out := make(map[int]process.Details, len(in))
	for k, d := range in {
		pid, err := strconv.Atoi(k)
		if err != nil {
			panic(err)
		}
		out[pid] = process.Details{ThreadCount: d.ThreadCount, BSDName: d.BSDName, ExecutablePath: d.ExecutablePath}
	}
	return out
}

// contractProcLsof is `lsof -p` output for the macOS table: descriptors of
// every type, listening and outbound TCP, log files and a cwd with spaces.
const contractProcLsof = `COMMAND     PID   USER   FD     TYPE             DEVICE SIZE/OFF      NODE NAME
launchd       1   root  cwd      DIR               1,16      640         2 /
launchd       1   root    3u  KQUEUE                                       count=0, state=0xa
Google      400   user  cwd      DIR               1,16      640    123456 /Users/user/my project
Google      400   user   12u    IPv4 0x1234567890abcdef      0t0       TCP 127.0.0.1:9222 (LISTEN)
Google      400   user   13u    IPv4 0x1234567890abcdf0      0t0       TCP 10.0.0.2:50000->142.250.0.1:443 (ESTABLISHED)
Google      400   user   14u    IPv6 0x1234567890abcdf1      0t0       TCP *:8080 (LISTEN)
Google      400   user   15w     REG               1,16    10240    777777 /Users/user/Library/Logs/Chrome/chrome.log
Google      400   user   16w     REG               1,16    10240    777777 /Users/user/Library/Logs/Chrome/chrome.log
Google      400   user   17r     REG               1,16     2048    777778 /Applications/Google Chrome.app/Contents/Info.plist
Google      400   user   18u     CHR               16,2      0t0       909 /dev/ttys002
Google      400   user   19     PIPE 0x1111111111111111    16384           ->0x2222222222222222
Google      400   user   20u  KQUEUE                                       count=0, state=0x8
Google      400   user   21u    unix 0x3333333333333333      0t0           ->0x4444444444444444
Google      400   user   22u     CHR                3,2      0t0       333 /dev/null
Google      400   user   23u    IPv4 0x1234567890abcdf2      0t0       UDP *:5353
-zsh        700   user  cwd      DIR               1,16      960    222222 /Users/user
-zsh        700   user    0u     CHR               16,3      0t0      1011 /dev/ttys003
-zsh        700   user   10w     REG               1,16        0    888888 /tmp/zsh-history.log (deleted)
java        710   user   40u    IPv6 0x1234567890abcdf3      0t0       TCP [::1]:52000->[::1]:52001 (ESTABLISHED)
`

func contractProcFake(listing, comm string) *proc.Fake {
	fake := proc.NewFake()
	fake.OnExact("ps", strings.Fields(contractProcPSArgs), proc.Response{Result: proc.Result{Stdout: []byte(listing)}})
	fake.OnExact("ps", strings.Fields(contractProcCommArgs), proc.Response{Result: proc.Result{Stdout: []byte(comm)}})
	fake.On(proc.MatchPrefix("lsof", "-p"), proc.Response{Result: proc.Result{Stdout: []byte(contractProcLsof)}})
	return fake
}

// contractProcEnv is one recording environment: the OS, the collect and
// classify seams, and what case.json records about them.
type contractProcEnv struct {
	os       hostos.Kind
	opts     process.CollectOptions
	rec      *contracttest.Recorder
	classify func(ctx context.Context, procs []process.Info) error
	input    map[string]any
}

func contractProcDarwinEnv(t *testing.T, rows []contractProcRow, details map[string]contractProcDetail) *contractProcEnv {
	t.Helper()
	now := contractProcTime(t)
	listing, comm := contractProcPS(now, rows)
	rec := &contracttest.Recorder{Inner: contractProcFake(listing, comm)}
	dm := contractProcDetailMap(details)
	return &contractProcEnv{
		os:  hostos.Darwin,
		rec: rec,
		opts: process.CollectOptions{
			OS: hostos.Darwin, Runner: rec, Now: func() time.Time { return now },
			MemoryBytes:     func() uint64 { return contractProcMemBytes },
			DetailCollector: func([]process.Info) map[int]process.Details { return dm },
		},
		input: map[string]any{"os": "darwin", "now": contractProcNow, "tz_offset": contractProcTZ, "memory_bytes": contractProcMemBytes, "details": details},
	}
}

// --- Linux /proc tree ---------------------------------------------------

// contractProcStat is a /proc/<pid>/stat line with the fields the collector
// reads; the rest are plausible constants.
func contractProcStat(pid int, comm, state string, ppid int, utime, stime int64, threads int, start, vsize, rss int64) string {
	return fmt.Sprintf("%d (%s) %s %d %d %d 0 -1 4194560 100 0 0 0 %d %d 0 0 20 0 %d 0 %d %d %d 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0\n",
		pid, comm, state, ppid, pid, pid, utime, stime, threads, start, vsize, rss)
}

func contractProcStatus(name string, uid int) string {
	return fmt.Sprintf("Name:\t%s\nState:\tS (sleeping)\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n", name, uid, uid, uid, uid, uid, uid, uid, uid)
}

// contractProcLinuxTree is a /proc tree (files, and exe symlinks) plus
// /etc/passwd. Exe targets that a --classify run would read do not exist
// on any host.
func contractProcLinuxTree() (files, links map[string]string) {
	files = map[string]string{
		"proc/stat":  "cpu  1 2 3 4\nbtime 1795248000\nprocesses 900\n",
		"etc/passwd": "# fixture\nroot:x:0:0:root:/root:/bin/bash\n\nuser:x:1000:1000:Fixture User:/home/user:/bin/bash\n",
	}
	links = map[string]string{}
	add := func(pid int, comm, state string, ppid int, utime, stime int64, threads int, start, vsize, rss int64, cmdline []string, uid int, exe string) {
		p := "proc/" + strconv.Itoa(pid) + "/"
		files[p+"stat"] = contractProcStat(pid, comm, state, ppid, utime, stime, threads, start, vsize, rss)
		if len(cmdline) > 0 {
			files[p+"cmdline"] = strings.Join(cmdline, "\x00") + "\x00"
		} else {
			files[p+"cmdline"] = ""
		}
		files[p+"status"] = contractProcStatus(comm, uid)
		if exe != "" {
			links[p+"exe"] = exe
		}
	}
	// btime is 2026-11-21T08:00:00Z; start ticks are 100/s after it.
	add(1, "init", "S", 0, 500, 300, 1, 10, 170000000, 3100, []string{"/sbin/init", "splash"}, 0, "/opt/spectra-fixture-missing/init")
	add(2, "kthreadd", "S", 0, 0, 10, 1, 11, 0, 0, nil, 0, "")
	add(3, "kworker/0:1-events", "I", 2, 0, 250, 1, 12, 0, 1, nil, 0, "")
	add(100, "bash", "S", 1, 120, 80, 1, 360000, 9100000, 1302, []string{"-bash"}, 1000, "/usr/bin/bash")
	add(101, "python3", "S", 100, 9000, 1000, 3, 14000000, 31000000, 24576, []string{"python3", "/home/user/app.py", "--port", "8000"}, 1000, "/usr/bin/python3.12 (deleted)")
	add(102, "node", "R", 100, 360000, 40000, 11, 15000000, 1100000000, 102400, []string{"node", "/home/user/my tools/node_modules/@anthropic-ai/claude-code/cli.js"}, 1000, "/usr/bin/node")
	add(103, "cron", "S", 1, 20, 30, 1, 400, 8000000, 812, []string{"/usr/sbin/cron", "-f"}, 0, "")
	add(105, "worker (x) y", "S", 1, 10, 10, 2, 16000000, 52000000, 2048, []string{"/opt/app/bin/worker", "--id", "7"}, 1001, "/opt/spectra-fixture-missing/worker")
	add(106, "sleep", "Z", 100, 0, 0, 1, 16500000, 0, 2, nil, 1000, "")
	files["proc/104/stat"] = "104 garbage without fields\n"
	files["proc/104/cmdline"] = "x\x00"
	files["proc/self"] = "not a pid directory\n"
	return files, links
}

func contractProcLinuxEnv(t *testing.T, c contracttest.Case) *contractProcEnv {
	t.Helper()
	files, links := contractProcLinuxTree()
	root := caseRoot(t, c, files)
	contractProcLinks(t, root, links)
	now := contractProcTime(t)
	rec := &contracttest.Recorder{Inner: proc.NewFake()}
	return &contractProcEnv{
		os:  hostos.Linux,
		rec: rec,
		opts: process.CollectOptions{
			OS: hostos.Linux, Runner: rec, Now: func() time.Time { return now },
			ProcFS: filepath.Join(root, "proc"), PageSize: 4096,
			// A Linux build has no physical-memory reading (memory_other.go):
			// without this the recording host's hw.memsize would leak in.
			MemoryBytes: func() uint64 { return 0 },
			LookupUser:  contractProcPasswd(filepath.Join(root, "etc", "passwd")),
		},
		classify: func(ctx context.Context, procs []process.Info) error {
			c := classify.New(classify.Options{OS: hostos.Linux, FS: contractProcFS{root: root}, Runner: rec, Workers: 1, ProcFS: "/proc"})
			return process.ClassifyWithArgv(ctx, procs, c, func(pid int, full string) []string {
				data, err := os.ReadFile(filepath.Join(root, "proc", strconv.Itoa(pid), "cmdline")) // #nosec G304 -- fixture under contract/cases
				if err != nil {
					return nil
				}
				return contractProcArgv(contractProcSplitCmdline(data), full)
			})
		},
		input: map[string]any{"os": "linux", "now": contractProcNow, "tz_offset": contractProcTZ, "page_size": 4096},
	}
}

// contractProcLinks writes (or checks) the symlinks of a case's root/ tree.
func contractProcLinks(t *testing.T, root string, links map[string]string) {
	t.Helper()
	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if *contracttest.Update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path)
			if err := os.Symlink(links[name], path); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.Readlink(path)
		if err != nil {
			t.Fatalf("%s: %v (write it with -contract-update)", path, err)
		}
		if got != links[name] {
			t.Fatalf("%s points at %q, want %q; rewrite it with -contract-update", path, got, links[name])
		}
	}
}

// contractProcPasswd resolves uids from a passwd file the way Go's pure-Go
// os/user does: name:password:uid:..., comments and blank lines skipped.
func contractProcPasswd(path string) func(int) (string, error) {
	return func(uid int) (string, error) {
		data, err := os.ReadFile(path) // #nosec G304 -- fixture under contract/cases
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) >= 3 && parts[2] == strconv.Itoa(uid) {
				return parts[0], nil
			}
		}
		return "", fmt.Errorf("user: unknown userid %d", uid)
	}
}

func contractProcSplitCmdline(data []byte) []string {
	s := strings.TrimRight(string(data), "\x00")
	if s == "" {
		return nil
	}
	args := strings.Split(s, "\x00")
	if len(args) > 4096 {
		return nil
	}
	return args
}

// contractProcArgv is process.argvFor's check: argv only when it still
// matches the listed command line.
func contractProcArgv(argv []string, full string) []string {
	if len(argv) == 0 || strings.Join(strings.Fields(strings.Join(argv, " ")), " ") != strings.Join(strings.Fields(full), " ") {
		return nil
	}
	return argv
}

// --- classify fixtures ----------------------------------------------------

// contractProcFS is a classify.FileSystem serving absolute paths from root,
// with errors naming the logical path (what the Rust replay's RootedFs
// reports).
type contractProcFS struct{ root string }

func (f contractProcFS) host(path string) string {
	return filepath.Join(f.root, filepath.FromSlash(path))
}

func contractProcPathErr(op, path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return &fs.PathError{Op: op, Path: path, Err: err}
}

func (f contractProcFS) Stat(path string) (fs.FileInfo, error) {
	fi, err := os.Stat(f.host(path))
	if err != nil {
		return nil, contractProcPathErr("stat", path, err)
	}
	return fi, nil
}

func (f contractProcFS) Open(path string) (classify.ReadAtCloser, error) {
	fi, err := f.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: classify.ErrNotRegular}
	}
	file, err := os.Open(f.host(path)) // #nosec G304 -- fixture under contract/cases
	if err != nil {
		return nil, contractProcPathErr("open", path, err)
	}
	return file, nil
}

func (f contractProcFS) ReadFile(path string, limit int64) ([]byte, error) {
	file, err := f.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.NewSectionReader(file, 0, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w (%d bytes)", path, classify.ErrTooLarge, limit)
	}
	return data, nil
}

func (f contractProcFS) EvalSymlinks(path string) (string, error) {
	root, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		return "", err
	}
	got, err := filepath.EvalSymlinks(f.host(path))
	if err != nil {
		return "", contractProcPathErr("lstat", path, err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s leaves the fixture root", path)
	}
	return "/" + filepath.ToSlash(rel), nil
}

// contractProcSect is one section of a synthetic Mach-O.
type contractProcSect struct{ seg, name, data string }

// contractProcMachO is a minimal arm64 Mach-O executable holding sects, one
// LC_SEGMENT_64 per distinct segment (classify's buildMachO).
func contractProcMachO(sects ...contractProcSect) string {
	var segs []string
	bySeg := map[string][]contractProcSect{}
	for _, s := range sects {
		if _, ok := bySeg[s.seg]; !ok {
			segs = append(segs, s.seg)
		}
		bySeg[s.seg] = append(bySeg[s.seg], s)
	}
	const hdr, segCmd, sectHdr = 32, 72, 80
	cmdsSize := 0
	for _, seg := range segs {
		cmdsSize += segCmd + sectHdr*len(bySeg[seg])
	}
	dataOff := hdr + cmdsSize
	le := binary.LittleEndian
	name16 := func(s string) []byte { b := make([]byte, 16); copy(b, s); return b }
	var cmds, data bytes.Buffer
	for _, seg := range segs {
		ss := bySeg[seg]
		segStart, segSize := dataOff+data.Len(), 0
		for _, s := range ss {
			segSize += len(s.data)
		}
		_ = binary.Write(&cmds, le, uint32(macho.LoadCmdSegment64))
		_ = binary.Write(&cmds, le, uint32(segCmd+sectHdr*len(ss)))
		cmds.Write(name16(seg))
		_ = binary.Write(&cmds, le, []uint64{0x1000, uint64(segSize), uint64(segStart), uint64(segSize)})
		_ = binary.Write(&cmds, le, []uint32{7, 5, uint32(len(ss)), 0})
		for _, s := range ss {
			off := dataOff + data.Len()
			cmds.Write(name16(s.name))
			cmds.Write(name16(seg))
			_ = binary.Write(&cmds, le, []uint64{0x1000, uint64(len(s.data))})
			_ = binary.Write(&cmds, le, []uint32{uint32(off), 0, 0, 0, 0, 0, 0, 0})
			data.WriteString(s.data)
		}
	}
	var out bytes.Buffer
	_ = binary.Write(&out, le, []uint32{macho.Magic64, uint32(macho.CpuArm64), 0, uint32(macho.TypeExec), uint32(len(segs)), uint32(cmdsSize), 0, 0})
	out.Write(cmds.Bytes())
	out.Write(data.Bytes())
	return out.String()
}

func contractProcPlist(id, exe string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + id + `</string>
<key>CFBundleExecutable</key><string>` + exe + `</string>
</dict></plist>
`
}

const (
	contractProcFixtureApp = "/Applications/Fixture.app/Contents/MacOS/Fixture"
	contractProcFixtureHlp = "/Applications/Fixture.app/Contents/MacOS/Fixture Helper"
	contractProcElectron   = "/Applications/Elec.app/Contents/MacOS/Elec"
	contractProcGoTool     = "/usr/local/bin/gotool"
	contractProcRustTool   = "/Users/user/.cargo/bin/rtool"
	contractProcLs         = "/bin/ls"
	contractProcNode       = "/opt/homebrew/bin/node"
	contractProcGone       = "/opt/gone/app"
	contractProcClaudeJS   = "/Users/user/.npm-global/lib/node_modules/@anthropic-ai/claude-code/cli.js"
)

func contractProcClassifyFiles() map[string]string {
	native := contractProcMachO(contractProcSect{"__TEXT", "__text", "\x1f\x20\x03\xd5native code"})
	return map[string]string{
		"Applications/Fixture.app/Contents/Info.plist":                                                contractProcPlist("com.example.fixture", "Fixture"),
		"Applications/Fixture.app/Contents/MacOS/Fixture":                                             native,
		"Applications/Fixture.app/Contents/MacOS/Fixture Helper":                                      "#!/bin/sh\nexec true\n",
		"Applications/Elec.app/Contents/Info.plist":                                                   contractProcPlist("com.example.elec", "Elec"),
		"Applications/Elec.app/Contents/MacOS/Elec":                                                   native,
		"Applications/Elec.app/Contents/Frameworks/Electron Framework.framework/Resources/Info.plist": contractProcPlist("com.github.Electron.framework", "Electron Framework"),
		"usr/local/bin/gotool":                                                                        contractProcMachO(contractProcSect{"__TEXT", "__text", "go code"}, contractProcSect{"__DATA", "__go_buildinfo", "not a build info blob"}),
		"Users/user/.cargo/bin/rtool":                                                                 contractProcMachO(contractProcSect{"__TEXT", "__text", "rust code"}, contractProcSect{"__TEXT", "__cstring", "/rustc/0123abcd/library/std/src/panicking.rs\x00RUST_BACKTRACE\x00"}),
		"bin/ls":                                                                                      native,
		"opt/homebrew/bin/node":                                                                       "#!/fixture node stand-in\n",
	}
}

const (
	contractProcDevID = `Executable=/Applications/Fixture.app/Contents/MacOS/Fixture
CodeDirectory v=20500 size=645 flags=0x10000(runtime) hashes=9+7 location=embedded
Authority=Developer ID Application: Fixture Inc. (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
`
	contractProcAdhoc = `CodeDirectory v=20400 size=50564 flags=0x2(adhoc) hashes=1574+2 location=embedded
Signature=adhoc
TeamIdentifier=not set
`
	contractProcApple = `Executable=/bin/ls
Identifier=com.apple.ls
Format=Mach-O universal (x86_64 arm64e)
CodeDirectory v=20400 size=325 flags=0x0(none) hashes=5+2 location=embedded
Platform identifier=26
Authority=macOS Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
`
)

func contractProcCodesign(fake *proc.Fake) {
	display := func(exe, out string) {
		fake.OnExact("codesign", []string{"-dv", "--verbose=2", exe}, proc.Response{Result: proc.Result{Stderr: []byte(out)}})
	}
	display(contractProcFixtureApp, contractProcDevID)
	fake.OnExact("codesign", []string{"--verify", "--strict", contractProcFixtureApp}, proc.Response{})
	display(contractProcFixtureHlp, contractProcAdhoc)
	display(contractProcElectron, contractProcElectron+": code object is not signed at all\n")
	display(contractProcGoTool, contractProcGoTool+": code object is not signed at all\n")
	display(contractProcRustTool, contractProcAdhoc)
	display(contractProcLs, contractProcApple)
	fake.OnExact("codesign", []string{"--verify", "--ignore-resources", "-R=anchor apple", contractProcLs}, proc.Response{})
	display(contractProcNode, contractProcAdhoc)
}

func contractProcClassifyEnv(t *testing.T, c contracttest.Case) *contractProcEnv {
	t.Helper()
	root := caseRoot(t, c, contractProcClassifyFiles())
	rows := []contractProcRow{
		{1, 0, 0.6, 20480, 420000, 0, "root", 72 * time.Hour, "/sbin/launchd", ""},
		{300, 1, 2.5, 150000, 410000000, 501, "user", 3 * time.Hour, contractProcFixtureApp, ""},
		{301, 300, 1.5, 50000, 409000000, 501, "user", 2 * time.Hour, contractProcFixtureHlp + " --type=gpu", contractProcFixtureHlp},
		{310, 1, 9.5, 450000, 411000000, 501, "user", 4 * time.Hour, contractProcElectron, ""},
		{320, 1, 0.9, 30000, 408000000, 501, "user", time.Hour, contractProcGoTool + " serve", ""},
		{330, 1, 0.8, 31000, 407000000, 501, "user", 50 * time.Minute, contractProcRustTool + " watch", ""},
		{340, 1, 0.3, 2000, 406000000, 0, "root", 40 * time.Minute, contractProcLs + " -la /", ""},
		{350, 1, 7.7, 401000, 421000000, 501, "user", 26 * time.Hour, contractProcNode + " " + contractProcClaudeJS, ""},
		{360, 1, 0.2, 6100, 405000000, 501, "user", 47 * time.Hour, "-zsh", ""},
		{370, 1, 0.1, 7000, 404000000, 501, "user", 30 * time.Minute, contractProcGone + " --run", ""},
	}
	details := map[string]contractProcDetail{
		"1":   {ThreadCount: 4, BSDName: "launchd"},
		"300": {ThreadCount: 30, BSDName: "Fixture", ExecutablePath: contractProcFixtureApp},
		"301": {ThreadCount: 9, BSDName: "Fixture Helper", ExecutablePath: contractProcFixtureHlp},
		"310": {ThreadCount: 40, BSDName: "Elec", ExecutablePath: contractProcElectron},
		"320": {ThreadCount: 12, BSDName: "gotool", ExecutablePath: contractProcGoTool},
		"330": {ThreadCount: 5, BSDName: "rtool", ExecutablePath: contractProcRustTool},
		"340": {ThreadCount: 1, BSDName: "ls", ExecutablePath: contractProcLs},
		"350": {ThreadCount: 11, BSDName: "node", ExecutablePath: contractProcNode},
		"370": {ThreadCount: 2, BSDName: "app", ExecutablePath: contractProcGone},
	}
	argv := map[string][]string{
		"350": {contractProcNode, contractProcClaudeJS},
		"360": {"-zsh", "stale"},
	}
	env := contractProcDarwinEnv(t, rows, details)
	contractProcCodesign(env.rec.Inner.(*proc.Fake))
	env.input["argv"] = argv
	env.classify = func(ctx context.Context, procs []process.Info) error {
		c := classify.New(classify.Options{OS: hostos.Darwin, FS: contractProcFS{root: root}, Runner: env.rec, Workers: 1})
		return process.ClassifyWithArgv(ctx, procs, c, func(pid int, full string) []string {
			return contractProcArgv(argv[strconv.Itoa(pid)], full)
		})
	}
	return env
}

// --- recording ------------------------------------------------------------

// contractProcRecord runs `spectra <args>` through dispatch against env (nil
// for flag errors, which must not collect) and writes case kind/name.
func contractProcRecord(t *testing.T, kind, name string, env *contractProcEnv, args []string) {
	t.Helper()
	c := contracttest.New(t, kind, name)
	contractProcRun(t, c, env, args, func() (int, []byte, []byte) {
		var code int
		stdout, stderr := captureStdio(t, func() { code = dispatch(args) })
		return code, stdout, stderr
	})
}

// contractProcRun installs env's seams, runs fn, and records the case.
func contractProcRun(t *testing.T, c contracttest.Case, env *contractProcEnv, args []string, fn func() (int, []byte, []byte)) {
	t.Helper()
	origCollect, origNow, origClassify, origLocal := collectProcesses, processNow, processClassify, time.Local
	defer func() {
		collectProcesses, processNow, processClassify, time.Local = origCollect, origNow, origClassify, origLocal
	}()
	time.Local = time.FixedZone("TEST", contractProcTZ)
	input := map[string]any{"os": "darwin"}
	kind := hostos.Darwin
	collectProcesses = func(context.Context, process.CollectOptions) []process.Info {
		t.Fatal("a flag error must not collect")
		return nil
	}
	if env != nil {
		input, kind = env.input, env.os
		now := contractProcTime(t)
		processNow = func() time.Time { return now }
		collectProcesses = func(ctx context.Context, o process.CollectOptions) []process.Info {
			opts := env.opts
			opts.Deep = o.Deep
			return process.CollectAll(ctx, opts)
		}
		if env.classify != nil {
			processClassify = env.classify
		}
	}
	restore := hostos.SetForTest(kind)
	defer restore()
	input["args"] = args
	c.Input(t, input)
	code, stdout, stderr := fn()
	c.Expect(t, stdout, code)
	expectStderr(t, c, stderr)
	if env != nil {
		env.rec.Save(t, c)
	}
}

// TestContractProcessCases records `spectra process` (JSON and text) on a
// synthetic macOS ps listing and a Linux /proc tree: tree, deep, families,
// classification, filters, every sort key, and its errors. Replayed by
// crates/cli/tests/contract.rs.
func TestContractProcessCases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	darwin := func() *contractProcEnv {
		return contractProcDarwinEnv(t, contractProcDarwinRows(), contractProcDarwinDetails())
	}
	both := []struct {
		name string
		args []string
	}{
		{"darwin", nil},
		{"darwin-tree", []string{"--tree"}},
		{"darwin-deep", []string{"--deep"}},
		{"darwin-family", []string{"--group-by", "family"}},
		{"darwin-top-cpu", []string{"--top", "3", "--sort", "cpu"}},
	}
	for _, tc := range both {
		for _, mode := range []string{"json", "text"} {
			args := append([]string{"process"}, tc.args...)
			if mode == "json" {
				args = append(args, "--json")
			}
			contractProcRecord(t, "process", mode+"-"+tc.name, darwin(), args)
		}
	}
	text := map[string][]string{
		"text-darwin-fd-breakdown": {"--fd-breakdown"},
		"text-darwin-family-top":   {"--group-by=family", "--top=2"},
		"text-darwin-min-rss":      {"--min-rss", "500M", "--user", "user"},
		"text-darwin-min-vsz":      {"--min-vsz", "0.4T"},
		"text-darwin-min-elapsed":  {"--min-elapsed", "24h"},
		"text-darwin-ppid":         {"--ppid", "700"},
		"text-darwin-ppid-tree":    {"--ppid=1", "--tree"},
		"text-darwin-no-match":     {"--user", "nobody"},
		"text-darwin-bad-sort":     {"--sort", "size"},
		"json-darwin-filters":      {"--json", "--min-rss=1g", "--min-elapsed=1h30m"},
	}
	for _, key := range []string{"pid", "cmd", "rss", "vsz", "cpu", "mem", "elapsed", "startedat"} {
		text["text-darwin-sort-"+key] = []string{"--sort", key}
	}
	for name, args := range text {
		contractProcRecord(t, "process", name, darwin(), append([]string{"process"}, args...))
	}

	// ps fails: no processes, exit 0.
	failing := darwin()
	failing.rec.Inner = proc.NewFake().OnExact("ps", strings.Fields(contractProcPSArgs), proc.Response{Result: proc.Result{ExitCode: -1}, Err: errors.New("exec: \"ps\": executable file not found in $PATH")})
	failing.opts.Runner = failing.rec
	contractProcRecord(t, "process", "text-darwin-ps-error", failing, []string{"process"})

	for _, mode := range []string{"json", "text"} {
		suffix := []string{}
		if mode == "json" {
			suffix = []string{"--json"}
		}
		for _, tc := range []struct {
			name string
			args []string
		}{{"linux", nil}, {"linux-tree", []string{"--tree"}}, {"linux-classify", []string{"--classify"}}} {
			c := contracttest.New(t, "process", mode+"-"+tc.name)
			args := append(append([]string{"process"}, tc.args...), suffix...)
			contractProcRecord(t, "process", mode+"-"+tc.name, contractProcLinuxEnv(t, c), args)
		}
		c := contracttest.New(t, "process", mode+"-darwin-classify")
		contractProcRecord(t, "process", mode+"-darwin-classify", contractProcClassifyEnv(t, c), append([]string{"process", "--classify"}, suffix...))
	}
	c := contracttest.New(t, "process", "text-darwin-classify-tree")
	contractProcRecord(t, "process", "text-darwin-classify-tree", contractProcClassifyEnv(t, c), []string{"process", "--classify", "--tree"})

	for name, args := range map[string][]string{
		"flag-unknown":           {"--nope"},
		"flag-help":              {"-h"},
		"flag-min-rss":           {"--min-rss", "x"},
		"flag-min-vsz-negative":  {"--min-vsz=-1"},
		"flag-min-elapsed":       {"--min-elapsed", "5x"},
		"flag-top":               {"--top", "x"},
		"flag-group-by-bogus":    {"--group-by", "pid"},
		"flag-group-by-conflict": {"--group-by", "family", "--sort", "cpu", "--tree", "--classify"},
	} {
		contractProcRecord(t, "process", name, nil, append([]string{"process"}, args...))
	}
}

// TestContractLineageCases records `spectra lineage` (JSON and text) on the
// macOS and Linux tables, and its errors. Replayed by
// crates/cli/tests/contract.rs.
func TestContractLineageCases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	record := func(name string, env *contractProcEnv, args []string) {
		c := contracttest.New(t, "lineage", name)
		if env == nil {
			env = contractProcDarwinEnv(t, contractProcDarwinRows(), contractProcDarwinDetails())
		}
		contractProcRun(t, c, env, args, func() (int, []byte, []byte) {
			var stdout, stderr bytes.Buffer
			collector := process.CollectorFunc(func(ctx context.Context, _ process.CollectOptions) []process.Info {
				return process.CollectAll(ctx, env.opts)
			})
			code := runLineageWithIO(args[1:], &stdout, &stderr, collector)
			return code, stdout.Bytes(), stderr.Bytes()
		})
	}
	for _, mode := range []string{"json", "text"} {
		flag := []string{}
		if mode == "json" {
			flag = []string{"--json"}
		}
		record(mode+"-darwin", nil, append(append([]string{"lineage"}, flag...), "700"))
		record(mode+"-darwin-reparented", nil, append(append([]string{"lineage"}, flag...), "400"))
		c := contracttest.New(t, "lineage", mode+"-linux")
		record(mode+"-linux", contractProcLinuxEnv(t, c), append(append([]string{"lineage"}, flag...), "100"))
	}
	for name, args := range map[string][]string{
		"no-such-pid":   {"99999"},
		"flag-no-pid":   {},
		"flag-two-pids": {"1", "2"},
		"flag-bad-pid":  {"x"},
		"flag-zero-pid": {"0"},
		"flag-unknown":  {"--nope", "1"},
		"flag-help":     {"-h"},
	} {
		record(name, nil, append([]string{"lineage"}, args...))
	}
}
