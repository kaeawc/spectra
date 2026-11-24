// Package devices reports the mobile devices an agent's work may be using:
// Android devices and emulators adb can reach (local, USB, or remote through
// `adb connect`), booted iOS simulators (macOS), and emulator and simulator
// processes running on this host, attributed to the --root they started
// under.
//
// It never leaves a process behind: `adb devices` starts an adb server when
// none is running, and that server outlives the call, so adb is asked only
// when a server is already up (or the caller opts in with StartADB).
package devices

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	protocol "github.com/kaeawc/spectra-protocol/protocol/v1"

	"github.com/kaeawc/spectra/internal/capabilities"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const commandTimeout = 10 * time.Second

// avdQueryTimeout bounds each `adb emu avd name` fallback: the console of a
// hung emulator would otherwise hold up the whole report.
const avdQueryTimeout = 2 * time.Second

// Probe statuses.
const (
	StatusOK               = "ok"
	StatusNotFound         = "not_found"          // the tool is not installed
	StatusServerNotRunning = "server_not_running" // adb only: not asked, to avoid starting a server
	StatusUnsupported      = "unsupported"        // not on this OS
	StatusError            = "error"
)

// Report is the `spectra devices --json` result (schema spectra.devices).
type Report struct {
	Schema        protocol.SchemaRef `json:"schema"`
	OS            string             `json:"os"`
	Android       AndroidReport      `json:"android"`
	IOSSimulators SimulatorReport    `json:"ios_simulators"`
	Processes     hostload.Devices   `json:"processes"`
	Limitations   []string           `json:"limitations"`
}

// AndroidReport is what adb sees.
type AndroidReport struct {
	Status  string          `json:"status"`
	ADB     string          `json:"adb,omitempty"`
	Message string          `json:"message,omitempty"`
	Devices []AndroidDevice `json:"devices"`
}

// AndroidDevice is one `adb devices -l` row.
type AndroidDevice struct {
	Serial string `json:"serial"`
	// State is adb's: device (ready), offline, unauthorized, recovery, ...
	State string `json:"state"`
	// Emulator is an emulator-<port> serial; Remote a host:port or mDNS
	// serial reached over the network (adb connect).
	Emulator    bool   `json:"emulator"`
	Remote      bool   `json:"remote"`
	Model       string `json:"model,omitempty"`
	Product     string `json:"product,omitempty"`
	Device      string `json:"device,omitempty"`
	TransportID string `json:"transport_id,omitempty"`
	// AVD is the emulator's AVD name, when known.
	AVD string `json:"avd,omitempty"`
}

// SimulatorReport is what `xcrun simctl` sees.
type SimulatorReport struct {
	Status  string      `json:"status"`
	Message string      `json:"message,omitempty"`
	Booted  []Simulator `json:"booted"`
	// All is every simulator in any state, present only when asked for
	// (Options.AllSimulators).
	All []Simulator `json:"all,omitzero"`
}

// Simulator is one iOS/tvOS/watchOS/visionOS simulator.
type Simulator struct {
	UDID    string `json:"udid"`
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
	State   string `json:"state"`
}

// Options are the seams Collect reads the host through. Nil fields use the
// real host.
type Options struct {
	OS       hostos.Kind
	Runner   proc.Runner
	Procs    func(ctx context.Context) []process.Info
	Cwds     func(ctx context.Context, pids []int) map[int]string
	LookPath func(string) (string, error)
	Getenv   func(string) string
	Stat     func(string) (os.FileInfo, error)
	Now      func() time.Time
	Roots    []hostload.Root
	// StartADB asks adb even when no adb server is running, which starts
	// one that keeps running after Spectra exits.
	StartADB bool
	// AllSimulators also lists simulators that are not booted.
	AllSimulators bool
}

func (o Options) withDefaults() Options {
	o.OS = hostos.Resolve(o.OS)
	if o.Runner == nil {
		o.Runner = proc.OS{}
	}
	if o.Procs == nil {
		o.Procs = func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{Runner: o.Runner, OS: o.OS,
				DetailCollector: func([]process.Info) map[int]process.Details { return map[int]process.Details{} }})
		}
	}
	if o.Cwds == nil {
		host := hostload.Host{OS: o.OS, Runner: o.Runner}
		o.Cwds = host.Cwds
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Stat == nil {
		o.Stat = os.Stat
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Collect gathers every source. A source that fails or does not apply is
// reported in its status; Collect itself does not fail.
func Collect(ctx context.Context, opts Options) Report {
	o := opts.withDefaults()
	procs := o.Procs(ctx)
	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}
	version, _ := capabilities.ResultSchemaVersion(capabilities.SchemaDevices)
	r := Report{
		Schema:        protocol.SchemaRef{Name: capabilities.SchemaDevices, Version: version},
		OS:            o.OS.String(),
		Android:       collectAndroid(ctx, o, procs),
		IOSSimulators: collectSimulators(ctx, o),
		Processes:     hostload.CollectDevices(procs, o.Cwds(ctx, pids), o.Roots, o.Now(), hostload.DefaultBootWindow),
		Limitations: []string{
			"Android devices come from adb, which is asked only when an adb server is already running (or with --start-adb): asking starts a server that outlives Spectra.",
			"A device that adb or simctl lists may belong to another user or session; processes attribute local emulators and simulators to a --root only through their working directory or parent chain.",
		},
	}
	return r
}

func collectAndroid(ctx context.Context, o Options, procs []process.Info) AndroidReport {
	r := AndroidReport{Devices: []AndroidDevice{}}
	adb := findADB(o)
	if adb == "" {
		r.Status = StatusNotFound
		r.Message = "adb not found on PATH or under $ANDROID_HOME/platform-tools or $ANDROID_SDK_ROOT/platform-tools"
		return r
	}
	r.ADB = adb
	if !o.StartADB && !adbServerReachable(o, procs) {
		r.Status = StatusServerNotRunning
		r.Message = "no adb server is running, so no device is attached through it; pass --start-adb to start one and ask"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	res, err := o.Runner.Run(ctx, proc.Cmd{Name: adb, Args: []string{"devices", "-l"}})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	if err != nil {
		r.Status = StatusError
		r.Message = "adb devices -l: " + err.Error()
		return r
	}
	r.Status = StatusOK
	r.Devices = ParseADBDevices(string(res.Stdout))
	resolveAVDs(ctx, o, adb, r.Devices, procs)
	return r
}

// resolveAVDs names each emulator's AVD from the emulator process whose
// console port matches the serial, asking the emulator console through adb
// only for serials no process explains.
func resolveAVDs(ctx context.Context, o Options, adb string, devs []AndroidDevice, procs []process.Info) {
	byPort := EmulatorAVDsByPort(procs)
	for i := range devs {
		port, ok := EmulatorConsolePort(devs[i].Serial)
		if !ok {
			continue
		}
		if avd := byPort[port]; avd != "" {
			devs[i].AVD = avd
			continue
		}
		devs[i].AVD = queryAVDName(ctx, o, adb, devs[i].Serial)
	}
}

// EmulatorConsolePort returns the console port of an emulator-<port> serial.
func EmulatorConsolePort(serial string) (int, bool) {
	rest, ok := strings.CutPrefix(serial, "emulator-")
	if !ok {
		return 0, false
	}
	port, err := strconv.Atoi(rest)
	return port, err == nil && port > 0
}

// EmulatorAVDsByPort maps console ports to AVD names from emulator command
// lines that carry both: "-port N" / "-port=N" or "-ports N,M" (console
// first), and "-avd NAME" / "@NAME".
func EmulatorAVDsByPort(procs []process.Info) map[int]string {
	out := map[int]string{}
	for _, p := range procs {
		avd := hostload.AVDName(p.FullCommandLine)
		if avd == "" {
			continue
		}
		if port, ok := consolePortArg(p.FullCommandLine); ok {
			out[port] = avd
		}
	}
	return out
}

func consolePortArg(cmdline string) (int, bool) {
	f := strings.Fields(cmdline)
	for i, tok := range f {
		var v string
		switch {
		case (tok == "-port" || tok == "-ports") && i+1 < len(f):
			v = f[i+1]
		case strings.HasPrefix(tok, "-port="):
			v = strings.TrimPrefix(tok, "-port=")
		case strings.HasPrefix(tok, "-ports="):
			v = strings.TrimPrefix(tok, "-ports=")
		default:
			continue
		}
		v, _, _ = strings.Cut(v, ",")
		if port, err := strconv.Atoi(v); err == nil && port > 0 {
			return port, true
		}
	}
	return 0, false
}

func queryAVDName(ctx context.Context, o Options, adb, serial string) string {
	ctx, cancel := context.WithTimeout(ctx, avdQueryTimeout)
	defer cancel()
	res, err := o.Runner.Run(ctx, proc.Cmd{Name: adb, Args: []string{"-s", serial, "emu", "avd", "name"}})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	return ParseEmuAVDName(string(res.Stdout))
}

// ParseEmuAVDName parses `adb emu avd name`: the name, then the console's
// "OK" line. A "KO" reply, an error, or an empty answer yields "".
func ParseEmuAVDName(out string) string {
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case line == "OK", strings.HasPrefix(line, "KO"), strings.HasPrefix(line, "error"):
			return ""
		default:
			return line
		}
	}
	return ""
}

func findADB(o Options) string {
	if p, err := o.LookPath("adb"); err == nil {
		return p
	}
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if home := o.Getenv(env); home != "" {
			p := filepath.Join(home, "platform-tools", "adb")
			if fi, err := o.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// adbServerReachable reports a local adb server process, or an
// environment that points adb at a server elsewhere.
func adbServerReachable(o Options, procs []process.Info) bool {
	if o.Getenv("ADB_SERVER_SOCKET") != "" || o.Getenv("ANDROID_ADB_SERVER_ADDRESS") != "" {
		return true
	}
	for _, p := range procs {
		if path.Base(p.Command) == "adb" && strings.Contains(p.FullCommandLine, "server") {
			return true
		}
	}
	return false
}

// ParseADBDevices parses `adb devices -l`.
func ParseADBDevices(out string) []AndroidDevice {
	devs := []AndroidDevice{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "*") || strings.HasPrefix(line, "List of devices") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		d := AndroidDevice{Serial: f[0], State: f[1]}
		d.Emulator = strings.HasPrefix(d.Serial, "emulator-")
		d.Remote = !d.Emulator && (strings.Contains(d.Serial, ":") || strings.Contains(d.Serial, "._adb-tls-"))
		for _, kv := range f[2:] {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch k {
			case "model":
				d.Model = v
			case "product":
				d.Product = v
			case "device":
				d.Device = v
			case "transport_id":
				d.TransportID = v
			}
		}
		devs = append(devs, d)
	}
	return devs
}

func collectSimulators(ctx context.Context, o Options) SimulatorReport {
	r := SimulatorReport{Booted: []Simulator{}}
	if o.AllSimulators {
		r.All = []Simulator{}
	}
	if o.OS != hostos.Darwin {
		r.Status = StatusUnsupported
		r.Message = hostos.Unsupported("iOS simulators (xcrun simctl)").Error()
		return r
	}
	xcrun, err := o.LookPath("xcrun")
	if err != nil {
		r.Status = StatusNotFound
		r.Message = "xcrun not found; install the Xcode command line tools"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	args := []string{"simctl", "list", "devices", "booted", "--json"}
	if o.AllSimulators {
		args = []string{"simctl", "list", "devices", "--json"}
	}
	res, err := o.Runner.Run(ctx, proc.Cmd{Name: xcrun, Args: args})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	var all []Simulator
	if err == nil {
		all, err = ParseSimctl(res.Stdout)
	}
	if err != nil {
		// simctl fails without a full Xcode (only the command line tools).
		r.Status = StatusError
		r.Message = "xcrun " + strings.Join(args[:len(args)-1], " ") + ": " + err.Error()
		return r
	}
	r.Status = StatusOK
	r.Booted = bootedOnly(all)
	if o.AllSimulators {
		r.All = all
	}
	return r
}

// ParseSimctlBooted parses `xcrun simctl list devices booted --json`.
func ParseSimctlBooted(data []byte) ([]Simulator, error) {
	all, err := ParseSimctl(data)
	if err != nil {
		return nil, err
	}
	return bootedOnly(all), nil
}

func bootedOnly(sims []Simulator) []Simulator {
	out := []Simulator{}
	for _, s := range sims {
		if s.State == "Booted" {
			out = append(out, s)
		}
	}
	return out
}

// ParseSimctl parses `xcrun simctl list devices [booted] --json` into every
// simulator listed, in any state, sorted by UDID.
func ParseSimctl(data []byte) ([]Simulator, error) {
	var doc struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse simctl JSON: %w", err)
	}
	sims := []Simulator{}
	for runtime, list := range doc.Devices {
		for _, d := range list {
			sims = append(sims, Simulator{UDID: d.UDID, Name: d.Name, Runtime: runtimeName(runtime), State: d.State})
		}
	}
	sort.Slice(sims, func(i, j int) bool { return sims[i].UDID < sims[j].UDID })
	return sims, nil
}

// runtimeName turns com.apple.CoreSimulator.SimRuntime.iOS-17-2 into "iOS 17.2".
func runtimeName(id string) string {
	id = strings.TrimPrefix(id, "com.apple.CoreSimulator.SimRuntime.")
	platform, version, ok := strings.Cut(id, "-")
	if !ok {
		return id
	}
	return platform + " " + strings.ReplaceAll(version, "-", ".")
}
