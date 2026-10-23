// Package xpcattr attributes a busy XPC/Mach service to the client processes
// driving it. NSXPCConnection names its dispatch queues
// "com.apple.NSXPCConnection.user.anonymous.<pid>", so sample(1) and
// spindump output carries the client PID; when it does not, lsmp port
// ownership is the fallback. Parsing is pure Go so it runs on any host;
// only capturing the evidence is macOS-only.
package xpcattr

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// Evidence sources reported in Attribution.Source.
const (
	SourceSample = "sample"
	SourceLsmp   = "lsmp"
)

// ProcRef names one process in a parent chain.
type ProcRef struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

// Client is one process driving the service.
type Client struct {
	PID         int       `json:"pid"`
	Name        string    `json:"name,omitempty"`
	CommandLine string    `json:"command_line,omitempty"`
	Weight      int       `json:"weight"` // samples (sample) or ports (lsmp)
	Share       float64   `json:"share"`  // fraction of attributed weight, 0..1
	Chain       []ProcRef `json:"chain,omitempty"`
	Launcher    *ProcRef  `json:"launcher,omitempty"`
}

// Attribution is the resolved client picture of one service.
type Attribution struct {
	PID     int      `json:"pid"`
	Name    string   `json:"name,omitempty"`
	Source  string   `json:"source"`
	Clients []Client `json:"clients"`
}

var (
	// queueLine matches a call-graph line that starts with a sample count and
	// names an anonymous NSXPCConnection queue carrying the client PID.
	queueLine = regexp.MustCompile(`^[\s+!:|]*(\d+)\s+.*com\.apple\.NSXPCConnection\.[\w.\-]*?anonymous\.(\d+)`)
	// lsmpPID matches the peer annotation on an lsmp port row: either
	// "(pid N)" or "(N) procname". The exact column format is unverified on
	// real macOS output, so both shapes are accepted.
	lsmpPID = regexp.MustCompile(`\((?:pid )?(\d+)\)`)
	lsmpHex = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	// launchers are build/test drivers worth naming as "launched by".
	launchers = map[string]bool{
		"xcodebuild": true, "Xcode": true, "swift-build": true, "swift-test": true,
		"gradle": true, "make": true, "ninja": true, "bazel": true,
	}
	processLine = regexp.MustCompile(`(?m)^Process:\s+(.+?)\s+\[\d+\]`)
)

// ParseQueueClients sums the sample counts of threads whose dispatch queue is
// an anonymous NSXPCConnection queue, keyed by client PID. Lines without a
// leading count, with an unparseable PID, or of any other shape are ignored.
func ParseQueueClients(out string) map[int]int {
	res := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		m := queueLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err1 := strconv.Atoi(m[1])
		pid, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || pid <= 0 || n <= 0 {
			continue
		}
		res[pid] += n
	}
	return res
}

// ParseLsmpHolders counts, per remote PID, the send and send-once port rows of
// `lsmp -p` output that name a peer. Receive rights belong to the service
// itself and are ignored, as is the service's own PID.
func ParseLsmpHolders(out string, self int) map[int]int {
	res := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !lsmpHex.MatchString(fields[0]) || !hasClientRight(fields) {
			continue
		}
		m := lsmpPID.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		res[pid]++
	}
	return res
}

func hasClientRight(fields []string) bool {
	for _, f := range fields {
		switch f {
		case "send", "send-once":
			return true
		case "recv", "port-set":
			return false
		}
	}
	return false
}

// Resolve turns raw per-PID weights into clients with command line, parent
// chain and launching tool, sorted by descending share.
func Resolve(weights map[int]int, procs []process.Info) []Client {
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	clients := make([]Client, 0, len(weights))
	for pid, w := range weights {
		c := Client{PID: pid, Weight: w}
		if total > 0 {
			c.Share = float64(w) / float64(total)
		}
		if p, ok := byPID[pid]; ok {
			c.Name = procName(p)
			c.CommandLine = p.FullCommandLine
			c.Chain = parentChain(p, byPID)
			c.Launcher = launcherOf(c.Chain)
		}
		clients = append(clients, c)
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Weight != clients[j].Weight {
			return clients[i].Weight > clients[j].Weight
		}
		return clients[i].PID < clients[j].PID
	})
	return clients
}

// procName prefers the executable basename: Command is the comm field, which
// the kernel truncates to 16 characters.
func procName(p process.Info) string {
	if p.ExecutablePath != "" {
		return filepath.Base(p.ExecutablePath)
	}
	return filepath.Base(p.Command)
}

// parentChain lists the ancestors of p, nearest first, stopping at launchd,
// a missing parent, or a cycle.
func parentChain(p process.Info, byPID map[int]process.Info) []ProcRef {
	var chain []ProcRef
	seen := map[int]bool{p.PID: true}
	for cur := p; cur.PPID > 1 && !seen[cur.PPID]; {
		parent, ok := byPID[cur.PPID]
		if !ok {
			break
		}
		seen[parent.PID] = true
		chain = append(chain, ProcRef{PID: parent.PID, Name: procName(parent)})
		cur = parent
	}
	return chain
}

// launcherOf picks the outermost known build/test driver in the chain, or nil
// when the chain holds none.
func launcherOf(chain []ProcRef) *ProcRef {
	if len(chain) == 0 {
		return nil
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if launchers[chain[i].Name] {
			r := chain[i]
			return &r
		}
	}
	return nil
}

// FromSample attributes clients from sample/spindump text, falling back to
// lsmp output when the text carries no queue PIDs. lsmpOut may be empty.
func FromSample(pid int, name, sampleOut, lsmpOut string, procs []process.Info) Attribution {
	a := Attribution{PID: pid, Name: name, Source: SourceSample}
	w := ParseQueueClients(sampleOut)
	if len(w) == 0 && lsmpOut != "" {
		a.Source = SourceLsmp
		w = ParseLsmpHolders(lsmpOut, pid)
	}
	a.Clients = Resolve(w, procs)
	return a
}

// Options controls a live attribution.
type Options struct {
	PID      int
	Duration time.Duration
	Sudo     bool // run lsmp under sudo
	Runner   proc.Runner
	Procs    func(context.Context) []process.Info
}

// Capture samples the service (and, if the sample names no clients, reads its
// Mach ports) and resolves the clients. macOS only.
func Capture(ctx context.Context, o Options) (Attribution, error) {
	if hostos.Current() != hostos.Darwin {
		return Attribution{}, hostos.Unsupported("XPC client attribution (sample/lsmp)")
	}
	if o.PID <= 0 {
		return Attribution{}, fmt.Errorf("xpc attribution requires positive pid")
	}
	if o.Runner == nil {
		o.Runner = proc.OS{}
	}
	if o.Duration <= 0 {
		o.Duration = 3 * time.Second
	}
	secs := int(o.Duration.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	// Snapshot processes before sampling so a PID recycled during the sample
	// is less likely to be mislabeled.
	var procs []process.Info
	if o.Procs != nil {
		procs = o.Procs(ctx)
	} else {
		procs = process.CollectAll(ctx, process.CollectOptions{})
	}
	res, err := o.Runner.Run(ctx, proc.Cmd{Name: "/usr/bin/sample", Args: []string{strconv.Itoa(o.PID), strconv.Itoa(secs), "10"}})
	if err != nil {
		return Attribution{}, fmt.Errorf("sample pid %d: %w", o.PID, err)
	}
	if res.ExitCode != 0 {
		return Attribution{}, fmt.Errorf("sample pid %d: exit %d: %s", o.PID, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	sampleOut := string(res.Stdout)
	var lsmpOut string
	if len(ParseQueueClients(sampleOut)) == 0 {
		var lerr error
		lsmpOut, lerr = o.lsmp(ctx)
		if lerr != nil && o.Sudo {
			return Attribution{}, lerr
		}
	}
	return FromSample(o.PID, nameFromSample(sampleOut), sampleOut, lsmpOut, procs), nil
}

func (o Options) lsmp(ctx context.Context) (string, error) {
	cmd := proc.Cmd{Name: "/usr/bin/lsmp", Args: []string{"-p", strconv.Itoa(o.PID)}}
	if o.Sudo {
		cmd = proc.Cmd{Name: "/usr/bin/sudo", Args: append([]string{"-n", cmd.Name}, cmd.Args...)}
	}
	res, err := o.Runner.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("lsmp pid %d: %w", o.PID, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("lsmp pid %d: exit %d: %s", o.PID, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return string(res.Stdout), nil
}

func nameFromSample(out string) string {
	if m := processLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// Summary renders the one-line whatswrong phrasing, e.g.
// "OSLogService (17874) busy, driven by Runner (17649), launched by xcodebuild (17491)".
// It returns "" when no client was found.
func (a Attribution) Summary() string {
	if len(a.Clients) == 0 {
		return ""
	}
	top := a.Clients[0]
	name := a.Name
	if name == "" {
		name = "process"
	}
	s := fmt.Sprintf("%s (%d) busy, driven by %s (%d)", name, a.PID, clientName(top), top.PID)
	if top.Launcher != nil {
		s += fmt.Sprintf(", launched by %s (%d)", top.Launcher.Name, top.Launcher.PID)
	}
	return s
}

// knownServices are daemons that commonly serve XPC/Mach clients but live
// outside the path patterns LooksLikeService checks.
var knownServices = map[string]bool{
	"OSLogService": true, "diagnosticd": true, "mds": true, "mds_stores": true,
	"logd": true, "distnoted": true, "cfprefsd": true, "trustd": true,
}

// LooksLikeService reports whether p plausibly serves XPC clients, so callers
// avoid sampling arbitrary busy processes: an executable under /usr/libexec,
// inside an XPCServices directory or .xpc bundle, or a known daemon.
func LooksLikeService(p process.Info) bool {
	path := p.ExecutablePath
	if strings.HasPrefix(path, "/usr/libexec/") ||
		strings.Contains(path, "/XPCServices/") ||
		strings.Contains(path, ".xpc/") {
		return true
	}
	return knownServices[procName(p)]
}

func clientName(c Client) string {
	if c.Name == "" {
		return "unknown process"
	}
	return c.Name
}
