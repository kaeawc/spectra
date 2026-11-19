// Package nettop answers "who is using the network right now": it samples
// per-process cumulative byte counters on fixed deadlines, turns consecutive
// samples into per-interval deltas and rates, and ranks processes by the
// bytes they moved over the window.
package nettop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/netstate"
)

// Bases name where the counters came from.
const (
	BasisNettop = "nettop"
	BasisSSTCP  = "ss-tcp"
)

// MaxRemotes bounds the remote endpoints reported per process.
const MaxRemotes = 3

var (
	// ErrBadConfig reports a non-positive interval or duration.
	ErrBadConfig = errors.New("interval and duration must be positive")
	// ErrNoData reports that no sample produced any counters.
	ErrNoData = errors.New("no usable sample")
	// ErrToolMissing reports that the sampling tool is not installed.
	ErrToolMissing = errors.New("sampling tool not found")
)

// Counter is one cumulative counter pair from a sample. Socket is empty when
// the source counts per process (nettop) and names the connection when it
// counts per socket (ss), so a closed socket and a new one are never diffed.
type Counter struct {
	PID int
	// Start is the process start time (unix seconds) when the source knows
	// it; 0 means unknown and the pid alone identifies the process.
	Start   int64
	Command string
	Socket  string
	Remote  string
	In      int64
	Out     int64
}

// Sampler returns the current cumulative counters.
type Sampler func(ctx context.Context) ([]Counter, error)

// Tick is the host-wide rate over one interval, emitted as it is measured.
type Tick struct {
	ElapsedMS int64   `json:"elapsed_ms"`
	SpanMS    int64   `json:"span_ms"`
	InRate    float64 `json:"in_bytes_per_sec"`
	OutRate   float64 `json:"out_bytes_per_sec"`
	Active    int     `json:"active_processes"`
}

// Config drives Run.
type Config struct {
	Sample Sampler
	// Tool names the sampling tool in errors.
	Tool string
	// Sleep waits d or until ctx ends, returning the context error then.
	Sleep    func(ctx context.Context, d time.Duration) error
	Clock    clock.Clock
	Interval time.Duration
	Duration time.Duration
	// Preexisting holds the pids alive before the baseline sample. A process
	// absent from it started inside the window, so its whole counter counts
	// on first appearance. Nil means unknown: first appearances count 0.
	Preexisting map[int]bool
	// Emit, if set, receives each interval's rate; EmitInclude, if set,
	// limits which pids it sums.
	Emit        func(Tick)
	EmitInclude func(pid int) bool
}

type procKey struct {
	pid   int
	start int64
}

type sockKey struct {
	proc   procKey
	socket string
}

type delta struct {
	span    int
	in, out int64
}

type procAcc struct {
	command     string
	remotes     map[string]int64
	remoteIdx   int
	first, last int
	deltas      []delta
}

// Trace is the raw outcome of Run; Summarize turns it into a Report.
type Trace struct {
	stamps   []time.Duration
	procs    map[procKey]*procAcc
	interval time.Duration
	// Failed counts samples that errored or returned nothing.
	Failed int
	// Skipped counts deadlines missed because a sample overran them.
	Skipped int
}

type run struct {
	cfg     Config
	began   time.Time
	tr      *Trace
	last    map[sockKey]Counter
	lastErr error
}

// Run samples until Duration elapses on cfg.Clock or ctx ends. A cancelled
// context ends the window early and still returns what was measured. It
// fails when the tool is missing or no sample produced counters.
func Run(ctx context.Context, cfg Config) (*Trace, error) {
	if cfg.Interval <= 0 || cfg.Duration <= 0 {
		return nil, fmt.Errorf("nettop: %w", ErrBadConfig)
	}
	r := &run{
		cfg: cfg, began: cfg.Clock.Now(), last: map[sockKey]Counter{},
		tr: &Trace{procs: map[procKey]*procAcc{}, interval: cfg.Interval},
	}
	r.take(ctx)
	if errors.Is(r.lastErr, ErrToolMissing) {
		return nil, fmt.Errorf("%s: %w", cfg.Tool, r.lastErr)
	}
	r.loop(ctx)
	if len(r.tr.stamps) == 0 {
		if r.lastErr != nil {
			return nil, fmt.Errorf("%w from %s in %d attempts: %w", ErrNoData, cfg.Tool, r.tr.Failed, r.lastErr)
		}
		return nil, fmt.Errorf("%w from %s in %d attempts (every sample was empty)", ErrNoData, cfg.Tool, r.tr.Failed)
	}
	return r.tr, nil
}

// loop samples at fixed deadlines (start + k×interval, the last clamped to
// Duration), sleeping only the remainder so a slow sample does not stretch
// the cadence; deadlines a sample overran are skipped and counted.
func (r *run) loop(ctx context.Context) {
	for k := time.Duration(1); ; k++ {
		due := min(k*r.cfg.Interval, r.cfg.Duration)
		if wait := due - r.elapsed(); wait < 0 {
			r.tr.Skipped++
		} else {
			if wait > 0 && (r.cfg.Sleep(ctx, wait) != nil || ctx.Err() != nil) {
				return
			}
			r.take(ctx)
		}
		if due >= r.cfg.Duration || ctx.Err() != nil {
			return
		}
	}
}

func (r *run) elapsed() time.Duration { return r.cfg.Clock.Now().Sub(r.began) }

// take stamps the sample with the time its collection finished.
func (r *run) take(ctx context.Context) {
	cs, err := r.cfg.Sample(ctx)
	at := r.elapsed()
	if err != nil || len(cs) == 0 {
		r.tr.Failed++
		if err != nil {
			r.lastErr = err
		}
		return
	}
	r.record(cs, at)
}

func aggregate(cs []Counter) map[sockKey]Counter {
	cur := make(map[sockKey]Counter, len(cs))
	for _, c := range cs {
		k := sockKey{proc: procKey{pid: c.PID, start: c.Start}, socket: c.Socket}
		prev := cur[k]
		c.In, c.Out = prev.In+max(c.In, 0), prev.Out+max(c.Out, 0)
		cur[k] = c
	}
	return cur
}

func (r *run) record(cs []Counter, at time.Duration) {
	idx := len(r.tr.stamps)
	r.tr.stamps = append(r.tr.stamps, at)
	cur := aggregate(cs)
	for k, c := range cur {
		acc := r.tr.procs[k.proc]
		if acc == nil {
			acc = &procAcc{first: idx, remoteIdx: -1}
			r.tr.procs[k.proc] = acc
		}
		acc.last = idx
		if c.Command != "" {
			acc.command = c.Command
		}
		acc.noteRemote(idx, c)
		if idx == 0 {
			continue
		}
		din, dout := r.deltaFor(k, c)
		if din > 0 || dout > 0 {
			acc.deltas = append(acc.deltas, delta{span: idx - 1, in: din, out: dout})
		}
	}
	r.last = cur
	if idx > 0 && r.cfg.Emit != nil {
		r.cfg.Emit(r.tick(idx - 1))
	}
}

// deltaFor diffs c against the previous sample. A key not in the previous
// sample counts in full only when its process started inside the window;
// otherwise its bytes before now are unknowable and it contributes 0. A
// counter that went backwards (reset, restart) contributes 0 for the interval.
func (r *run) deltaFor(k sockKey, c Counter) (int64, int64) {
	prev, ok := r.last[k]
	if !ok {
		if r.cfg.Preexisting != nil && !r.cfg.Preexisting[k.proc.pid] {
			return c.In, c.Out
		}
		return 0, 0
	}
	return grow(prev.In, c.In), grow(prev.Out, c.Out)
}

func grow(prev, cur int64) int64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// noteRemote keeps the remotes seen in the process's latest sample with
// their cumulative bytes, so the busiest are reported.
func (a *procAcc) noteRemote(idx int, c Counter) {
	if a.remoteIdx != idx {
		a.remotes, a.remoteIdx = map[string]int64{}, idx
	}
	if c.Remote != "" {
		a.remotes[c.Remote] += c.In + c.Out
	}
}

func (a *procAcc) topRemotes() []string {
	out := make([]string, 0, len(a.remotes))
	for r := range a.remotes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		bi, bj := a.remotes[out[i]], a.remotes[out[j]]
		if bi != bj {
			return bi > bj
		}
		return out[i] < out[j]
	})
	if len(out) > MaxRemotes {
		out = out[:MaxRemotes]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *run) tick(span int) Tick {
	var in, out int64
	active := 0
	for k, acc := range r.tr.procs {
		if r.cfg.EmitInclude != nil && !r.cfg.EmitInclude(k.pid) {
			continue
		}
		moved := false
		for i := len(acc.deltas) - 1; i >= 0 && acc.deltas[i].span == span; i-- {
			in, out, moved = in+acc.deltas[i].in, out+acc.deltas[i].out, true
		}
		if moved {
			active++
		}
	}
	d := r.tr.spanDur(span)
	sec := d.Seconds()
	return Tick{
		ElapsedMS: r.tr.stamps[span+1].Milliseconds(), SpanMS: d.Milliseconds(),
		InRate: Rate(in, sec), OutRate: Rate(out, sec), Active: active,
	}
}

func (t *Trace) spanDur(span int) time.Duration { return t.stamps[span+1] - t.stamps[span] }

// Rate is bytes per second, 0 for an empty or invalid span.
func Rate(b int64, sec float64) float64 {
	if sec <= 0 {
		return 0
	}
	r := float64(b) / sec
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return 0
	}
	return r
}

// Totals summarizes the included processes over the window.
type Totals struct {
	BytesIn         int64   `json:"bytes_in"`
	BytesOut        int64   `json:"bytes_out"`
	AvgInRate       float64 `json:"avg_in_bytes_per_sec"`
	AvgOutRate      float64 `json:"avg_out_bytes_per_sec"`
	PeakInRate      float64 `json:"peak_in_bytes_per_sec"`
	PeakOutRate     float64 `json:"peak_out_bytes_per_sec"`
	PeakTotalRate   float64 `json:"peak_total_bytes_per_sec"`
	ActiveProcesses int     `json:"active_processes"`
}

// Process is one process's traffic over the window.
type Process struct {
	PID         int      `json:"pid"`
	Command     string   `json:"command"`
	User        string   `json:"user,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	BytesIn     int64    `json:"bytes_in"`
	BytesOut    int64    `json:"bytes_out"`
	AvgInRate   float64  `json:"avg_in_bytes_per_sec"`
	AvgOutRate  float64  `json:"avg_out_bytes_per_sec"`
	PeakInRate  float64  `json:"peak_in_bytes_per_sec"`
	PeakOutRate float64  `json:"peak_out_bytes_per_sec"`
	Remotes     []string `json:"remotes,omitempty"`
	Appeared    bool     `json:"appeared,omitempty"`
	Disappeared bool     `json:"disappeared,omitempty"`
}

// Ref names a process that appeared or disappeared during the window.
type Ref struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// Report is the summary of a Trace.
type Report struct {
	DurationMS     int64     `json:"duration_ms"`
	IntervalMS     int64     `json:"interval_ms"`
	Samples        int       `json:"samples"`
	SamplesFailed  int       `json:"samples_failed"`
	SamplesSkipped int       `json:"samples_skipped"`
	Basis          string    `json:"basis"`
	PID            int       `json:"pid,omitempty"`
	Totals         Totals    `json:"totals"`
	Processes      []Process `json:"processes"`
	Appeared       []Ref     `json:"appeared"`
	Disappeared    []Ref     `json:"disappeared"`
	Limitations    []string  `json:"limitations"`
}

// Summarize ranks the processes include accepts (nil accepts all) by total
// bytes, keeping the top (all when top <= 0) that moved any bytes. Duration
// is the span between the first and last samples, which is what the bytes
// cover.
func Summarize(t *Trace, include func(pid int) bool, top int) Report {
	rep := Report{
		IntervalMS: t.interval.Milliseconds(), Samples: len(t.stamps),
		SamplesFailed: t.Failed, SamplesSkipped: t.Skipped,
		Processes: []Process{}, Appeared: []Ref{}, Disappeared: []Ref{}, Limitations: []string{},
	}
	if len(t.stamps) == 0 {
		return rep
	}
	window := t.stamps[len(t.stamps)-1] - t.stamps[0]
	rep.DurationMS = window.Milliseconds()
	spans := len(t.stamps) - 1
	spanIn, spanOut := make([]int64, spans), make([]int64, spans)
	var all []Process
	for k, acc := range t.procs {
		if include != nil && !include(k.pid) {
			continue
		}
		p := t.process(k, acc, window.Seconds(), spanIn, spanOut)
		if p.Appeared {
			rep.Appeared = append(rep.Appeared, Ref{PID: p.PID, Command: p.Command})
		}
		if p.Disappeared {
			rep.Disappeared = append(rep.Disappeared, Ref{PID: p.PID, Command: p.Command})
		}
		if p.BytesIn+p.BytesOut > 0 {
			all = append(all, p)
		}
	}
	rank(all)
	rep.Totals = t.totals(all, window.Seconds(), spanIn, spanOut)
	if top > 0 && len(all) > top {
		all = all[:top]
	}
	rep.Processes = append(rep.Processes, all...)
	sortRefs(rep.Appeared)
	sortRefs(rep.Disappeared)
	return rep
}

func (t *Trace) process(k procKey, acc *procAcc, windowSec float64, spanIn, spanOut []int64) Process {
	p := Process{
		PID: k.pid, Command: acc.command, Remotes: acc.topRemotes(),
		Appeared: acc.first > 0, Disappeared: acc.last < len(t.stamps)-1,
	}
	perIn, perOut := map[int]int64{}, map[int]int64{}
	for _, d := range acc.deltas {
		p.BytesIn += d.in
		p.BytesOut += d.out
		perIn[d.span] += d.in
		perOut[d.span] += d.out
		spanIn[d.span] += d.in
		spanOut[d.span] += d.out
	}
	for span, in := range perIn {
		sec := t.spanDur(span).Seconds()
		p.PeakInRate = max(p.PeakInRate, Rate(in, sec))
		p.PeakOutRate = max(p.PeakOutRate, Rate(perOut[span], sec))
	}
	p.AvgInRate, p.AvgOutRate = Rate(p.BytesIn, windowSec), Rate(p.BytesOut, windowSec)
	return p
}

func (t *Trace) totals(ranked []Process, windowSec float64, spanIn, spanOut []int64) Totals {
	tot := Totals{ActiveProcesses: len(ranked)}
	for _, p := range ranked {
		tot.BytesIn += p.BytesIn
		tot.BytesOut += p.BytesOut
	}
	tot.AvgInRate, tot.AvgOutRate = Rate(tot.BytesIn, windowSec), Rate(tot.BytesOut, windowSec)
	for i := range spanIn {
		sec := t.spanDur(i).Seconds()
		tot.PeakInRate = max(tot.PeakInRate, Rate(spanIn[i], sec))
		tot.PeakOutRate = max(tot.PeakOutRate, Rate(spanOut[i], sec))
		tot.PeakTotalRate = max(tot.PeakTotalRate, Rate(spanIn[i]+spanOut[i], sec))
	}
	return tot
}

func rank(ps []Process) {
	sort.Slice(ps, func(i, j int) bool {
		ti, tj := ps[i].BytesIn+ps[i].BytesOut, ps[j].BytesIn+ps[j].BytesOut
		if ti != tj {
			return ti > tj
		}
		return ps[i].PID < ps[j].PID
	})
}

func sortRefs(rs []Ref) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].PID < rs[j].PID })
}

// PIDs returns every pid the trace saw, ascending.
func (t *Trace) PIDs() []int {
	seen := map[int]bool{}
	var out []int
	for k := range t.procs {
		if !seen[k.pid] {
			seen[k.pid] = true
			out = append(out, k.pid)
		}
	}
	sort.Ints(out)
	return out
}

// Limitations describes what a basis cannot see.
func Limitations(basis string) []string {
	common := "a process or socket first seen after the baseline sample counts 0 bytes for that first appearance unless its process started inside the window, and traffic of one that opens and closes between samples is missed"
	switch basis {
	case BasisNettop:
		return []string{
			"nettop -t external: loopback traffic is not counted",
			common,
			"nettop reports no start time, so a pid reused within the window is not distinguished; a counter that goes backwards counts 0 for that interval",
		}
	case BasisSSTCP:
		return []string{
			"TCP only: UDP and QUIC traffic are not counted",
			"sockets of other users are invisible without root",
			"loopback sockets are not counted",
			common,
			"bytes out is bytes_acked, so unacknowledged in-flight data is not counted; a socket shared by several processes is attributed to the lowest pid",
		}
	}
	return nil
}

// SamplerFor returns the sampler, basis and tool name for kind.
func SamplerFor(kind hostos.Kind, run netstate.CmdRunner) (Sampler, string, string, error) {
	switch hostos.Resolve(kind) {
	case hostos.Darwin:
		return nettopSampler(run), BasisNettop, "nettop", nil
	case hostos.Linux:
		return ssSampler(run), BasisSSTCP, "ss", nil
	}
	return nil, "", "", hostos.Unsupported("network top")
}

func toolErr(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrToolMissing, err)
	}
	return err
}

func nettopSampler(run netstate.CmdRunner) Sampler {
	return func(context.Context) ([]Counter, error) {
		rows, err := netstate.CollectProcessBytes(run)
		if err != nil {
			return nil, toolErr(err)
		}
		cs := make([]Counter, 0, len(rows))
		for _, r := range rows {
			cs = append(cs, Counter{PID: r.PID, Command: r.Command, In: r.BytesIn, Out: r.BytesOut})
		}
		return cs, nil
	}
}

func ssSampler(run netstate.CmdRunner) Sampler {
	return func(context.Context) ([]Counter, error) {
		socks, err := netstate.CollectSocketBytes(run)
		if err != nil {
			return nil, toolErr(err)
		}
		cs := make([]Counter, 0, len(socks))
		for _, s := range socks {
			if isLoopback(s.RemoteAddr) {
				continue
			}
			cs = append(cs, Counter{
				PID: s.PID, Command: s.Command, Socket: s.LocalAddr + "->" + s.RemoteAddr,
				Remote: s.RemoteAddr, In: s.BytesIn, Out: s.BytesOut,
			})
		}
		return cs, nil
	}
}

// isLoopback reports whether an ss "addr:port" ("[v6]:port", "v6%zone:port")
// is a loopback address.
func isLoopback(addrPort string) bool {
	host := addrPort
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if i := strings.Index(host, "%"); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
