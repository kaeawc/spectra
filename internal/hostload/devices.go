package hostload

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// DefaultBootWindow is how far back a device start counts as a recent boot.
const DefaultBootWindow = 5 * time.Minute

// Device kinds.
const (
	DeviceSimulator = "simulator"
	DeviceEmulator  = "emulator"
)

// Device is one booted simulator or running emulator instance.
type Device struct {
	Kind string `json:"kind"`
	// ID is the simulator UDID, the emulator's AVD name, or a pid label
	// when neither is known.
	ID         string     `json:"id"`
	UDID       string     `json:"udid,omitempty"`
	AVD        string     `json:"avd,omitempty"`
	PID        int        `json:"pid"`
	Members    int        `json:"members"`
	CPUPct     float64    `json:"cpu_pct"`
	RSSKiB     int64      `json:"rss_kib"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	AgeSeconds int64      `json:"age_seconds"`
	Recent     bool       `json:"booted_recently"`
	// Root is the --root a member's cwd or parent chain lies under, if any.
	Root string `json:"root,omitempty"`
}

// RootDevices counts the devices one --root started.
type RootDevices struct {
	Root           string `json:"root"`
	Booted         int    `json:"booted"`
	BootedRecently int    `json:"booted_recently"`
}

// Devices summarises booted simulators and running emulators.
type Devices struct {
	Simulators        int           `json:"simulators"`
	Emulators         int           `json:"emulators"`
	Booted            int           `json:"booted"`
	BootedRecently    int           `json:"booted_recently"`
	BootWindowSeconds int64         `json:"boot_window_seconds"`
	ByRoot            []RootDevices `json:"by_root"`
	List              []Device      `json:"list"`
}

type deviceScan struct {
	byPID  map[int]process.Info
	cwds   map[int]string
	roots  []Root
	now    time.Time
	window time.Duration
}

// CollectDevices counts booted simulators (one per CoreSimulator device, by
// UDID, from the process families) and emulator instances (one per qemu
// process, or per emulator launcher with no qemu below it). A device is a
// recent boot when its earliest process started within window of now.
func CollectDevices(procs []process.Info, cwds map[int]string, roots []Root, now time.Time, window time.Duration) Devices {
	if window <= 0 {
		window = DefaultBootWindow
	}
	s := deviceScan{byPID: make(map[int]process.Info, len(procs)), cwds: cwds, roots: roots, now: now, window: window}
	for _, p := range procs {
		s.byPID[p.PID] = p
	}
	list := append([]Device{}, s.simulators(procs)...) // [] not null in JSON when empty
	list = append(list, s.emulators(procs)...)
	d := Devices{BootWindowSeconds: int64(window.Seconds()), ByRoot: []RootDevices{}, List: list}
	perRoot := map[string]*RootDevices{}
	for _, dev := range list {
		d.Booted++
		if dev.Kind == DeviceSimulator {
			d.Simulators++
		} else {
			d.Emulators++
		}
		if dev.Recent {
			d.BootedRecently++
		}
		if dev.Root != "" {
			perRoot[dev.Root] = countRoot(perRoot[dev.Root], dev)
		}
	}
	for _, r := range roots {
		if rd := perRoot[r.Path]; rd != nil {
			d.ByRoot = append(d.ByRoot, *rd)
			delete(perRoot, r.Path) // a root typed twice is listed once
		}
	}
	return d
}

func countRoot(rd *RootDevices, dev Device) *RootDevices {
	if rd == nil {
		rd = &RootDevices{Root: dev.Root}
	}
	rd.Booted++
	if dev.Recent {
		rd.BootedRecently++
	}
	return rd
}

func (s deviceScan) simulators(procs []process.Info) []Device {
	var out []Device
	for _, f := range process.GroupFamilies(procs, s.now, len(procs)+1) {
		if f.Kind != process.FamilySimulator {
			continue
		}
		pids := make([]int, len(f.Heaviest))
		for i, m := range f.Heaviest {
			pids[i] = m.PID
		}
		dev := Device{Kind: DeviceSimulator, UDID: f.UDID, ID: f.UDID, PID: simPrimary(f), Members: f.Members,
			CPUPct: f.CPUPct, RSSKiB: f.RSSKiB, Root: s.rootOf(pids)}
		if dev.ID == "" {
			dev.ID = fmt.Sprintf("launchd_sim pid %d", dev.PID)
		}
		s.stamp(&dev, f.StartedAt)
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// simPrimary is the device's launchd_sim, else its lowest pid.
func simPrimary(f process.Family) int {
	best := 0
	for _, m := range f.Heaviest {
		if strings.EqualFold(path.Base(m.Command), "launchd_sim") {
			return m.PID
		}
		if best == 0 || m.PID < best {
			best = m.PID
		}
	}
	return best
}

// isQemu reports an Android emulator's qemu. Any qemu-system-* also backs
// colima, lima, podman machine and libvirt, so it counts only with an
// emulator launcher ancestor, an AVD name, or the SDK's emulator/qemu path.
func (s deviceScan) isQemu(p process.Info) bool {
	if !strings.HasPrefix(strings.ToLower(path.Base(p.Command)), "qemu-system-") {
		return false
	}
	if AVDName(p.FullCommandLine) != "" || strings.Contains(p.ExecutablePath, "/emulator/qemu/") || strings.Contains(p.FullCommandLine, "/emulator/qemu/") {
		return true
	}
	for _, a := range ancestorsOf(p, s.byPID) {
		if isEmulatorLauncher(a) {
			return true
		}
	}
	return false
}

func isEmulatorLauncher(p process.Info) bool {
	return strings.ToLower(path.Base(p.Command)) == "emulator"
}

func (s deviceScan) emulators(procs []process.Info) []Device {
	launchersWithQemu := map[int]bool{}
	for _, p := range procs {
		if !s.isQemu(p) {
			continue
		}
		for _, a := range ancestorsOf(p, s.byPID) {
			launchersWithQemu[a.PID] = true
		}
	}
	var out []Device
	for _, p := range procs {
		if s.isQemu(p) || (isEmulatorLauncher(p) && !launchersWithQemu[p.PID] && !s.underEmulator(p)) {
			out = append(out, s.emulator(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func (s deviceScan) underEmulator(p process.Info) bool {
	for _, a := range ancestorsOf(p, s.byPID) {
		if isEmulatorLauncher(a) || s.isQemu(a) {
			return true
		}
	}
	return false
}

// emulator sums the instance with its emulator launcher ancestors and takes
// the AVD name from the nearest command line that names one.
func (s deviceScan) emulator(p process.Info) Device {
	dev := Device{Kind: DeviceEmulator, PID: p.PID, Members: 1, CPUPct: procCPU(p), RSSKiB: p.RSSKiB, AVD: AVDName(p.FullCommandLine)}
	earliest := startAt(p, s.now)
	for _, a := range ancestorsOf(p, s.byPID) {
		if !isEmulatorLauncher(a) {
			continue
		}
		dev.Members++
		dev.CPUPct += procCPU(a)
		dev.RSSKiB += a.RSSKiB
		if dev.AVD == "" {
			dev.AVD = AVDName(a.FullCommandLine)
		}
		if st := startAt(a, s.now); !st.IsZero() && (earliest.IsZero() || st.Before(earliest)) {
			earliest = st
		}
	}
	dev.ID = dev.AVD
	if dev.ID == "" {
		dev.ID = fmt.Sprintf("%s pid %d", path.Base(p.Command), p.PID)
	}
	dev.Root = s.rootOf([]int{p.PID})
	if !earliest.IsZero() {
		s.stamp(&dev, &earliest)
	}
	return dev
}

// AVDName returns the AVD an emulator command line boots: "-avd NAME",
// "-avd=NAME" or "@NAME".
func AVDName(cmdline string) string {
	f := strings.Fields(cmdline)
	for i, tok := range f {
		switch {
		case tok == "-avd" && i+1 < len(f):
			return f[i+1]
		case strings.HasPrefix(tok, "-avd="):
			return strings.TrimPrefix(tok, "-avd=")
		case i > 0 && len(tok) > 1 && tok[0] == '@':
			return tok[1:]
		}
	}
	return ""
}

func (s deviceScan) stamp(dev *Device, started *time.Time) {
	if started == nil || started.IsZero() {
		return
	}
	st := *started
	dev.StartedAt = &st
	if age := s.now.Sub(st); age > 0 {
		dev.AgeSeconds = int64(age.Seconds())
	}
	dev.Recent = !s.now.IsZero() && s.now.Sub(st) <= s.window
}

// rootOf returns the first --root that any pid's cwd, or a cwd up its parent
// chain, lies under.
func (s deviceScan) rootOf(pids []int) string {
	if len(s.roots) == 0 {
		return ""
	}
	for _, pid := range pids {
		p, ok := s.byPID[pid]
		if !ok {
			continue
		}
		for _, cur := range append([]process.Info{p}, ancestorsOf(p, s.byPID)...) {
			if r := MatchRoot(s.cwds[cur.PID], s.roots); r != "" {
				return r
			}
		}
	}
	return ""
}

// startOf is the process start time, or zero when unknown.
func startOf(p process.Info) time.Time {
	if !p.StartedAt.IsZero() {
		return p.StartedAt
	}
	return p.StartTime
}

// startAt is startOf, falling back to now minus the elapsed time.
func startAt(p process.Info, now time.Time) time.Time {
	if st := startOf(p); !st.IsZero() {
		return st
	}
	if p.Elapsed > 0 && !now.IsZero() {
		return now.Add(-p.Elapsed)
	}
	return time.Time{}
}
