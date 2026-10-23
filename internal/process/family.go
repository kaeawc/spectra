package process

import (
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
	PID     int           `json:"pid"`
	Command string        `json:"command"`
	CPUPct  float64       `json:"cpu_pct"`
	Age     time.Duration `json:"age,omitempty"`
}

// Family is a group of related processes whose CPU is summed together.
type Family struct {
	Key       string        `json:"key"`
	Kind      string        `json:"kind"`
	Name      string        `json:"name"`
	UDID      string        `json:"udid,omitempty"`
	Members   int           `json:"members"`
	CPUPct    float64       `json:"cpu_pct"`
	SharePct  float64       `json:"share_pct"` // of CPU used by all processes
	RSSKiB    int64         `json:"rss_kib"`
	StartedAt time.Time     `json:"started_at,omitempty"` // earliest member start
	Age       time.Duration `json:"age,omitempty"`        // age of the oldest member
	Heaviest  []Member      `json:"heaviest,omitempty"`
}

type famID struct{ key, kind, name, udid string }

var (
	xcodeNames = setOf("xcodebuild", "xcode", "xcbbuildservice", "sourcekitservice", "ibtoold", "swift-build", "simulator")
	buildNames = setOf("gradle", "make", "gmake", "ninja", "cmake", "cargo", "rustc", "clang", "clang++", "ld",
		"swift-frontend", "swiftc", "javac", "kotlinc", "bazel", "mvn", "sbt", "compile", "link", "ccache")
	browserApps = []struct{ match, name string }{
		{"google chrome", "Google Chrome"}, {"chromium", "Chromium"}, {"microsoft edge", "Microsoft Edge"},
		{"brave browser", "Brave Browser"}, {"firefox", "Firefox"}, {"safari", "Safari"},
		{"com.apple.webkit", "Safari"}, {"/opera", "Opera"}, {"/arc.app/", "Arc"}, {"/chrome", "Google Chrome"},
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
		if started := procStart(p, now, age); !started.IsZero() && (f.StartedAt.IsZero() || started.Before(f.StartedAt)) {
			f.StartedAt = started
		}
		if age > f.Age {
			f.Age = age
		}
		members[id.key] = append(members[id.key], Member{PID: p.PID, Command: p.Command, CPUPct: cpu, Age: age})
	}
	out := make([]Family, 0, len(groups))
	for key, f := range groups {
		if total > 0 {
			f.SharePct = f.CPUPct / total * 100
		}
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

// resolveFamily classifies p directly, then lets unclassified or generic
// build processes inherit the family of the nearest simulator/Xcode/emulator
// ancestor. Missing parents and cycles end the walk.
func resolveFamily(p Info, byPID map[int]Info) famID {
	id, ok := directFamily(p)
	if ok && id.kind != FamilyBuild {
		return id
	}
	seen := map[int]bool{p.PID: true}
	cur := p
	for hops := 0; hops < maxAncestorHops; hops++ {
		parent, found := byPID[cur.PPID]
		if !found || seen[parent.PID] {
			break
		}
		seen[parent.PID] = true
		if pid, pok := directFamily(parent); pok && inherits(pid.kind) {
			return pid
		}
		cur = parent
	}
	if ok {
		return id
	}
	name := commandName(p)
	return famID{key: FamilyOther + ":" + name, kind: FamilyOther, name: name}
}

func inherits(kind string) bool {
	return kind == FamilySimulator || kind == FamilyXcode || kind == FamilyEmulator
}

func directFamily(p Info) (famID, bool) {
	lower := strings.ToLower(exeText(p))
	name := strings.ToLower(commandName(p))
	if m := simUDIDRe.FindStringSubmatch(p.FullCommandLine + " " + p.ExecutablePath); m != nil {
		udid := strings.ToUpper(m[1])
		return famID{key: FamilySimulator + ":" + udid, kind: FamilySimulator, name: "Simulator " + udid, udid: udid}, true
	}
	if name == "launchd_sim" {
		return famID{key: FamilySimulator + ":unknown", kind: FamilySimulator, name: "Simulator (unknown device)"}, true
	}
	if name == "emulator" || strings.HasPrefix(name, "qemu-") {
		return famID{key: FamilyEmulator, kind: FamilyEmulator, name: "Emulator (qemu)"}, true
	}
	for _, b := range browserApps {
		if strings.Contains(lower, b.match) {
			return famID{key: FamilyBrowser + ":" + b.name, kind: FamilyBrowser, name: b.name}, true
		}
	}
	if xcodeNames[name] || strings.Contains(lower, "/xcode.app/") || strings.Contains(lower, "com.apple.dt.") {
		return famID{key: FamilyXcode, kind: FamilyXcode, name: "Xcode"}, true
	}
	if buildNames[name] || isGradleJVM(name, p.FullCommandLine) {
		return famID{key: FamilyBuild, kind: FamilyBuild, name: "Build tools"}, true
	}
	return famID{}, false
}

// commandName is the short executable name, lowercase-agnostic.
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

// exeText is the executable portion of the process, excluding arguments, so
// a "grep chrome" argument does not classify as a browser.
func exeText(p Info) string {
	if p.ExecutablePath != "" {
		return p.ExecutablePath
	}
	s := p.FullCommandLine
	if i := strings.Index(s, " -"); i >= 0 {
		s = s[:i]
	}
	return s
}
