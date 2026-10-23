package process

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Family kinds.
const (
	FamilySimulator = "simulator"
	FamilyXcode     = "xcode"
	FamilyEmulator  = "emulator"
	FamilyBuild     = "build"
	FamilyBrowser   = "browser"
	FamilyOther     = "other"
)

const (
	maxAncestorHops = 64
	heaviestMembers = 3
)

var simUDIDRe = regexp.MustCompile(`(?i)CoreSimulator/Devices/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// Member is one process inside a Family, reported for the heaviest-members view.
type Member struct {
	PID        int           `json:"pid"`
	Command    string        `json:"command"`
	CPUPct     float64       `json:"cpu_pct"`
	Age        time.Duration `json:"-"`
	AgeSeconds int64         `json:"age_seconds,omitempty"`
}

// Family is a group of related processes whose CPU is summed together.
// CPUPct is in percent of one core (so 250 means 2.5 cores busy); SharePct is
// the family's share of the CPU used by all listed processes, not of machine
// capacity. Age and AgeSeconds are the age of the oldest member.
type Family struct {
	Key        string        `json:"key"`
	Kind       string        `json:"kind"`
	Name       string        `json:"name"`
	UDID       string        `json:"udid,omitempty"`
	Members    int           `json:"members"`
	CPUPct     float64       `json:"cpu_pct"`
	SharePct   float64       `json:"share_pct"`
	RSSKiB     int64         `json:"rss_kib"`
	StartedAt  *time.Time    `json:"started_at,omitempty"` // earliest member start
	Age        time.Duration `json:"-"`
	AgeSeconds int64         `json:"age_seconds,omitempty"`
	Heaviest   []Member      `json:"heaviest,omitempty"`
}

type famID struct{ key, kind, name, udid string }

var (
	xcodeNames = setOf("xcodebuild", "xcode", "xcbbuildservice", "sourcekitservice", "ibtoold", "swift-build", "simulator")
	// buildDrivers start build trees; their children are in the build family.
	buildDrivers = setOf("gradle", "make", "gmake", "ninja", "cmake", "cargo", "bazel", "mvn", "sbt")
	// buildTools only count as build when a driver (or Xcode) is an ancestor.
	buildTools = setOf("clang", "clang++", "ld", "swift-frontend", "swiftc", "javac", "kotlinc", "rustc",
		"compile", "link", "ccache", "cc", "c++", "gcc", "g++")
	browserBundles = map[string]string{
		"google chrome.app": "Google Chrome", "chromium.app": "Chromium", "microsoft edge.app": "Microsoft Edge",
		"brave browser.app": "Brave Browser", "firefox.app": "Firefox", "safari.app": "Safari",
		"arc.app": "Arc", "opera.app": "Opera",
	}
	browserBinaries = map[string]string{
		"chrome": "Google Chrome", "google-chrome": "Google Chrome", "google-chrome-stable": "Google Chrome",
		"chromium": "Chromium", "chromium-browser": "Chromium", "firefox": "Firefox", "firefox-bin": "Firefox",
		"brave": "Brave Browser", "brave-browser": "Brave Browser", "microsoft-edge": "Microsoft Edge", "opera": "Opera",
	}
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// GroupFamilies sums CPU per process family. Processes that match no known
// family are grouped by command name under kind "other", so callers wanting
// only meaningful families should skip FamilyOther. topMembers bounds the
// heaviest-members list per family (0 uses a default). The result is sorted
// by CPU descending. It is pure: pass now for deterministic ages.
func GroupFamilies(procs []Info, now time.Time, topMembers int) []Family {
	if topMembers <= 0 {
		topMembers = heaviestMembers
	}
	byPID := make(map[int]Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	groups := map[string]*Family{}
	members := map[string][]Member{}
	var total float64
	for _, p := range procs {
		id := resolveFamily(p, byPID)
		f := groups[id.key]
		if f == nil {
			f = &Family{Key: id.key, Kind: id.kind, Name: id.name, UDID: id.udid}
			groups[id.key] = f
		}
		cpu := procCPU(p)
		total += cpu
		age := procAge(p, now)
		f.Members++
		f.CPUPct += cpu
		f.RSSKiB += p.RSSKiB
		if started := procStart(p, now, age); !started.IsZero() && (f.StartedAt == nil || started.Before(*f.StartedAt)) {
			s := started
			f.StartedAt = &s
		}
		if age > f.Age {
			f.Age = age
		}
		members[id.key] = append(members[id.key], Member{PID: p.PID, Command: p.Command, CPUPct: cpu, Age: age, AgeSeconds: int64(age.Seconds())})
	}
	out := make([]Family, 0, len(groups))
	for key, f := range groups {
		if total > 0 {
			f.SharePct = f.CPUPct / total * 100
		}
		f.AgeSeconds = int64(f.Age.Seconds())
		f.Heaviest = topMembersByCPU(members[key], topMembers)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPUPct != out[j].CPUPct {
			return out[i].CPUPct > out[j].CPUPct
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func topMembersByCPU(ms []Member, n int) []Member {
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].CPUPct != ms[j].CPUPct {
			return ms[i].CPUPct > ms[j].CPUPct
		}
		return ms[i].PID < ms[j].PID
	})
	if len(ms) > n {
		ms = ms[:n]
	}
	return ms
}

func procCPU(p Info) float64 {
	if p.CPUPct > p.CPUPercent {
		return p.CPUPct
	}
	return p.CPUPercent
}

func procAge(p Info, now time.Time) time.Duration {
	age := p.Elapsed
	if start := procStartRaw(p); !start.IsZero() && !now.IsZero() && now.After(start) {
		if d := now.Sub(start); d > age {
			age = d
		}
	}
	return age
}

func procStartRaw(p Info) time.Time {
	if !p.StartedAt.IsZero() {
		return p.StartedAt
	}
	return p.StartTime
}

func procStart(p Info, now time.Time, age time.Duration) time.Time {
	if start := procStartRaw(p); !start.IsZero() {
		return start
	}
	if age > 0 && !now.IsZero() {
		return now.Add(-age)
	}
	return time.Time{}
}

// ancestors returns p's parent chain, nearest first. It stops at a missing
// parent, a cycle, or a parent that started after its child (PID reuse).
func ancestors(p Info, byPID map[int]Info) []Info {
	var out []Info
	seen := map[int]bool{p.PID: true}
	cur := p
	for len(out) < maxAncestorHops {
		parent, ok := byPID[cur.PPID]
		if !ok || seen[parent.PID] {
			break
		}
		if ps, cs := procStartRaw(parent), procStartRaw(cur); !ps.IsZero() && !cs.IsZero() && ps.After(cs) {
			break
		}
		seen[parent.PID] = true
		out = append(out, parent)
		cur = parent
	}
	return out
}

// resolveFamily decides p's family. A simulator device (own UDID path, or a
// launchd_sim ancestor) always wins, so Safari/WebKit helpers and runtimes
// hosted inside Xcode.app stay with their device. Otherwise the process is
// classified directly, with generic processes and build tools inheriting from
// the nearest Xcode, emulator or build-driver ancestor.
func resolveFamily(p Info, byPID map[int]Info) famID {
	chain := ancestors(p, byPID)
	if id, ok := simulatorFamily(p); ok {
		return id
	}
	for _, a := range chain {
		if id, ok := simulatorFamily(a); ok {
			return id
		}
	}
	id, ok := directFamily(p)
	if ok && id.kind != FamilyBuild && !isBuildTool(p) {
		return id
	}
	for _, a := range chain {
		aid, aok := directFamily(a)
		if !aok {
			continue
		}
		if aid.kind == FamilyXcode || aid.kind == FamilyEmulator {
			return aid
		}
		if aid.kind == FamilyBuild && isBuildTool(p) {
			return aid
		}
	}
	if ok {
		return id
	}
	name := commandName(p)
	return famID{key: FamilyOther + ":" + name, kind: FamilyOther, name: name}
}

func simulatorFamily(p Info) (famID, bool) {
	if m := simUDIDRe.FindStringSubmatch(p.FullCommandLine + " " + p.ExecutablePath); m != nil {
		udid := strings.ToUpper(m[1])
		return famID{key: FamilySimulator + ":" + udid, kind: FamilySimulator, name: "Simulator " + udid, udid: udid}, true
	}
	if strings.ToLower(commandName(p)) == "launchd_sim" {
		return famID{key: fmt.Sprintf("%s:launchd_sim-%d", FamilySimulator, p.PID), kind: FamilySimulator,
			name: fmt.Sprintf("Simulator (launchd_sim pid %d)", p.PID)}, true
	}
	return famID{}, false
}

func isBuildTool(p Info) bool { return buildTools[strings.ToLower(commandName(p))] }

func directFamily(p Info) (famID, bool) {
	name := strings.ToLower(commandName(p))
	if name == "emulator" || strings.HasPrefix(name, "qemu-") {
		return famID{key: FamilyEmulator, kind: FamilyEmulator, name: "Emulator (qemu)"}, true
	}
	if app := browserName(p); app != "" {
		return famID{key: FamilyBrowser + ":" + app, kind: FamilyBrowser, name: app}, true
	}
	exe := strings.ToLower(exeText(p))
	if xcodeNames[name] || strings.Contains(exe, "/xcode.app/") || strings.HasPrefix(name, "com.apple.dt.") {
		return famID{key: FamilyXcode, kind: FamilyXcode, name: "Xcode"}, true
	}
	if buildDrivers[name] || isGradleJVM(name, p.FullCommandLine) {
		return famID{key: FamilyBuild, kind: FamilyBuild, name: "Build tools"}, true
	}
	return famID{}, false
}

// browserName matches the executable's app bundle (macOS) or binary name
// (Linux), never arbitrary arguments or directory names.
func browserName(p Info) string {
	exe := exeText(p)
	lower := strings.ToLower(exe)
	if i := strings.Index(lower, ".app/"); i >= 0 {
		bundle := lower[:i+4]
		if j := strings.LastIndex(bundle, "/"); j >= 0 {
			bundle = bundle[j+1:]
		}
		return browserBundles[bundle]
	}
	return browserBinaries[strings.ToLower(filepath.Base(firstToken(exe)))]
}

func firstToken(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// commandName is the short executable name.
func commandName(p Info) string {
	if p.Command != "" {
		return filepath.Base(p.Command)
	}
	return filepath.Base(p.ExecutablePath)
}

func isGradleJVM(name, cmdline string) bool {
	if name != "java" {
		return false
	}
	s := strings.ToLower(cmdline)
	for _, k := range []string{"gradledaemon", "gradlewrappermain", "gradle-launcher", "kotlin-daemon", "kotlincompilerdaemon"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// exeText is the executable portion of the process, excluding arguments.
func exeText(p Info) string {
	if p.ExecutablePath != "" {
		return p.ExecutablePath
	}
	s := p.FullCommandLine
	if !strings.HasPrefix(s, "/") {
		// relative argv[0]: arguments may contain paths, so use only the name
		return firstToken(s)
	}
	// Executable paths inside an app bundle may contain spaces
	// ("Google Chrome.app/Contents/MacOS/Google Chrome"); allow them only when
	// no later absolute-path argument starts before the bundle marker.
	marker := strings.Index(s, ".app/Contents/")
	argStart := strings.Index(s, " /")
	if marker < 0 || (argStart >= 0 && argStart < marker) {
		return firstToken(s)
	}
	if i := strings.Index(s[marker:], " -"); i >= 0 {
		s = s[:marker+i]
	}
	return s
}
