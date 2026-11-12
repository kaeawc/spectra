// Package xpcattr attributes a busy XPC/Mach service to the client processes
// driving it. NSXPCConnection names the queue of each accepted connection
// "com.apple.NSXPCConnection.user.<service>.<pid>" (or ".user.anonymous.<pid>"
// for anonymous listeners), so sample(1) and spindump output carries the
// client PID; when it does not, lsmp port ownership is the fallback. Parsing is pure Go so it runs on any host;
// only capturing the evidence is macOS-only.
package xpcattr

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
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
	// names a server-side NSXPCConnection queue. Group 2 is the full label;
	// its last dot-separated component is the client PID. Client-side queues
	// ("...NSXPCConnection.m-user.<service>") carry no PID and do not match.
	queueLine = regexp.MustCompile(`^[\s+!:|]*(\d+)\s+.*?(com\.apple\.NSXPCConnection\.user\.\S+)`)
	// lsmpPID matches the "(N) procname" peer annotation on an lsmp row.
	lsmpPID = regexp.MustCompile(`\((\d+)\)`)
	lsmpHex = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	// launchers are build/test drivers worth naming as "launched by".
	launchers = map[string]bool{
		"xcodebuild": true, "Xcode": true, "swift-build": true, "swift-test": true,
		"gradle": true, "make": true, "ninja": true, "bazel": true,
	}
	processLine = regexp.MustCompile(`(?m)^Process:\s+(.+?)\s+\[\d+\]`)
)

// maxQueueLabel is the length at which sample(1) truncates dispatch queue
// labels. A label this long may have lost digits of its PID, so it is dropped
// rather than attributed to the wrong process.
const maxQueueLabel = 127

// ParseQueueClients sums the sample counts of threads whose dispatch queue is
// a server-side NSXPCConnection queue, keyed by client PID. Lines without a
// leading count, with an unparseable or possibly truncated PID, or of any
// other shape are ignored.
func ParseQueueClients(out string) map[int]int {
	res := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		m := queueLine.FindStringSubmatch(line)
		if m == nil || len(m[2]) >= maxQueueLabel {
			continue
		}
		n, err1 := strconv.Atoi(m[1])
		pid, err2 := strconv.Atoi(m[2][strings.LastIndexByte(m[2], '.')+1:])
		if err1 != nil || err2 != nil || pid <= 0 || n <= 0 {
			continue
		}
		res[pid] += n
	}
	return res
}

// ParseLsmpHolders counts, per remote PID, the rights in `lsmp -p` output
// that tie a client to the service. lsmp lists other tasks' send and
// send-once rights to one of the service's receive ports as indented
// "+ send ... <- 0x... (PID) name" rows under that port, and a send-once
// right the service holds to a client's reply port as a top-level
// "send-once ... -> ... (PID) name" row. Top-level send rows are the
// service's own outbound connections and are ignored, as are launchd (which
// holds every Mach service port) and the service's own PID.
func ParseLsmpHolders(out string, self int) map[int]int {
	res := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		arrow := lsmpClientArrow(strings.Fields(line))
		if arrow == "" {
			continue
		}
		m := lsmpPID.FindStringSubmatch(line[strings.Index(line, arrow):])
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		res[pid]++
	}
	return res
}

// lsmpClientArrow returns the arrow that precedes the peer annotation on a
// row naming a client, or "" for any other row.
func lsmpClientArrow(fields []string) string {
	switch {
	case len(fields) > 2 && fields[0] == "+" && (fields[1] == "send" || fields[1] == "send-once") && slices.Contains(fields, "<-"):
		return "<-"
	case len(fields) > 3 && lsmpHex.MatchString(fields[0]) && fields[2] == "send-once" && slices.Contains(fields, "->"):
		return "->"
	}
	return ""
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
	// -file /dev/stdout stops sample from also leaving a report in /tmp.
	sampleOut, err := o.run(ctx, "sample", "/usr/bin/sample", strconv.Itoa(o.PID), strconv.Itoa(secs), "10", "-file", "/dev/stdout")
	if err != nil {
		return Attribution{}, err
	}
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
	return o.run(ctx, "lsmp", "/usr/bin/lsmp", "-p", strconv.Itoa(o.PID))
}

// run executes a capture tool, under non-interactive sudo when o.Sudo is set,
// and returns its stdout. Failures name the tool and, where the cause is
// known, what to do about it.
func (o Options) run(ctx context.Context, tool, path string, args ...string) (string, error) {
	cmd := proc.Cmd{Name: path, Args: args}
	if o.Sudo {
		cmd = proc.Cmd{Name: "/usr/bin/sudo", Args: append([]string{"-n", path}, args...)}
	}
	res, err := o.Runner.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("%s pid %d: %w", tool, o.PID, err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		return "", fmt.Errorf("%s pid %d: exit %d: %s%s", tool, o.PID, res.ExitCode, msg, hint(msg, o.Sudo))
	}
	return string(res.Stdout), nil
}

// hint suggests a fix for the permission failures sample, lsmp and sudo -n
// report on macOS.
func hint(stderr string, sudo bool) string {
	switch {
	case strings.Contains(stderr, "a password is required"):
		return " (--sudo runs sudo -n, which cannot prompt; run `sudo -v` first, then retry)"
	case sudo:
		return ""
	case strings.Contains(stderr, "appropriate privileges"), strings.Contains(stderr, "task_for_pid"):
		return " (the process is not inspectable as this user; run `sudo -v`, then retry with --sudo)"
	}
	return ""
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
