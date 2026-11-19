// Package nettop answers "who is using the network right now": it measures
// per-process bytes over consecutive intervals, turns them into per-interval
// deltas and rates, and ranks processes by the bytes they moved over the
// window.
//
// Two kinds of source feed it. A polled source (Linux ss) returns counters
// that grow from the moment a socket opens, sampled on fixed deadlines and
// diffed. A measuring source (macOS nettop in delta mode) runs for one
// interval itself and returns what each process moved in it.
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
	"github.com/kaeawc/spectra/internal/proc"
)

// Bases name where the counters came from.
const (
	BasisNettop = "nettop"
	BasisSSTCP  = "ss-tcp"
)

// MaxRemotes bounds the remote endpoints reported per process.
const MaxRemotes = 3

// measureSlack bounds how long one measuring run may overrun its interval
// before it is killed and counted as failed.
const measureSlack = 30 * time.Second

var (
	// ErrBadConfig reports a non-positive interval or duration, or a config
	// with neither or both of Sample and Measure.
	ErrBadConfig = errors.New("interval and duration must be positive and exactly one source set")
	// ErrNoData reports that no sample or measurement succeeded.
	ErrNoData = errors.New("no usable sample")
	// ErrToolMissing reports that the sampling tool is not installed.
	ErrToolMissing = errors.New("sampling tool not found")
)

// Counter is one counter pair. From a Sampler it is cumulative; from a
// Measurer it is the bytes moved during the interval. Socket is empty when
// the source counts per process and names the connection when it counts per
// socket (ss), so a closed socket and a new one are never diffed.
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

// Measurer runs for interval and returns the bytes each process moved in it.
// It must return promptly when ctx ends.
type Measurer func(ctx context.Context, interval time.Duration) ([]Counter, error)

// Tick is the host-wide rate over one interval, emitted as it is measured.
type Tick struct {
	ElapsedMS int64   `json:"elapsed_ms"`
	SpanMS    int64   `json:"span_ms"`
	InRate    float64 `json:"in_bytes_per_sec"`
	OutRate   float64 `json:"out_bytes_per_sec"`
	Active    int     `json:"active_processes"`
}

// Config drives Run. Exactly one of Sample and Measure is set.
type Config struct {
	Sample  Sampler
	Measure Measurer
	// Tool names the sampling tool in errors.
	Tool string
	// Sleep waits d or until ctx ends, returning the context error then.
	Sleep    func(ctx context.Context, d time.Duration) error
	Clock    clock.Clock
	Interval time.Duration
	Duration time.Duration
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
	// Failed counts samples or measurements that errored.
	Failed int
	// Skipped counts deadlines missed because a sample overran them.
	Skipped int
}

type run struct {
	cfg   Config
	began time.Time
	tr    *Trace
	// last holds the previous sample's counters by diffKey.
	last    map[sockKey]Counter
	ok      int
	lastErr error
}

// Run measures until Duration elapses on cfg.Clock or ctx ends. A cancelled
// context ends the window early and still returns what was measured. A
// sample that succeeds with no counters is a valid observation of an idle
// host. Run fails when the tool is missing, or when no sample or
// measurement succeeded.
func Run(ctx context.Context, cfg Config) (*Trace, error) {
	if cfg.Interval <= 0 || cfg.Duration <= 0 || (cfg.Sample == nil) == (cfg.Measure == nil) {
		return nil, fmt.Errorf("nettop: %w", ErrBadConfig)
	}
	r := &run{
		cfg: cfg, began: cfg.Clock.Now(), last: map[sockKey]Counter{},
		tr: &Trace{procs: map[procKey]*procAcc{}, interval: cfg.Interval},
	}
	if cfg.Measure != nil {
		r.measureLoop(ctx)
	} else if r.take(ctx); !errors.Is(r.lastErr, ErrToolMissing) {
		r.loop(ctx)
	}
	return r.result()
}

func (r *run) result() (*Trace, error) {
	switch {
	case r.ok > 0:
		return r.tr, nil
	case errors.Is(r.lastErr, ErrToolMissing):
		return nil, fmt.Errorf("%s: %w", r.cfg.Tool, r.lastErr)
	case r.lastErr != nil:
		return nil, fmt.Errorf("%w from %s in %d attempts: %w", ErrNoData, r.cfg.Tool, r.tr.Failed, r.lastErr)
	}
	return nil, fmt.Errorf("%w from %s: stopped before the first interval completed", ErrNoData, r.cfg.Tool)
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

// measureLoop runs the measurer back to back, each run covering one
// interval (the last clamped to what remains of Duration), so the spans
// tile the window. A run interrupted by ctx is dropped. A failed run still
// takes its interval, so the cadence holds and its span counts no bytes.
func (r *run) measureLoop(ctx context.Context) {
	r.tr.stamps = append(r.tr.stamps, r.elapsed())
	for planned := time.Duration(0); planned < r.cfg.Duration && ctx.Err() == nil; {
		d := min(r.cfg.Interval, r.cfg.Duration-planned)
		planned += d
		start := r.elapsed()
		cs, err := r.cfg.Measure(ctx, d)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			r.ok++
			r.recordSpan(cs, r.elapsed())
			continue
		}
		r.tr.Failed++
		r.lastErr = err
		if errors.Is(err, ErrToolMissing) {
			return
		}
		if wait := start + d - r.elapsed(); wait > 0 && r.cfg.Sleep(ctx, wait) != nil {
			return
		}
		r.tr.stamps = append(r.tr.stamps, r.elapsed())
	}
}

func (r *run) elapsed() time.Duration { return r.cfg.Clock.Now().Sub(r.began) }

// take stamps the sample with the time its collection finished.
func (r *run) take(ctx context.Context) {
	cs, err := r.cfg.Sample(ctx)
	at := r.elapsed()
	if err != nil {
		r.tr.Failed++
		r.lastErr = err
		return
	}
	r.ok++
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

// diffKey is what a cumulative counter is diffed by: the socket alone when
// the source names one (so a socket whose attribution moves to another pid
// is not counted twice), else the process.
func diffKey(k sockKey) sockKey {
	if k.socket != "" {
		return sockKey{socket: k.socket}
	}
	return k
}

// observe notes that the process was seen at stamp idx and returns its
// accumulator.
func (r *run) observe(k procKey, c Counter, idx int) *procAcc {
	acc := r.tr.procs[k]
	if acc == nil {
		acc = &procAcc{first: idx, remoteIdx: -1}
		r.tr.procs[k] = acc
	}
	acc.last = idx
	if c.Command != "" {
		acc.command = c.Command
	}
	acc.noteRemote(idx, c)
	return acc
}

func (a *procAcc) add(span int, in, out int64) {
	if in > 0 || out > 0 {
		a.deltas = append(a.deltas, delta{span: span, in: in, out: out})
	}
}

// record diffs a cumulative sample against the previous one.
func (r *run) record(cs []Counter, at time.Duration) {
	idx := len(r.tr.stamps)
	r.tr.stamps = append(r.tr.stamps, at)
	cur := aggregate(cs)
	next := make(map[sockKey]Counter, len(cur))
	for k, c := range cur {
		acc := r.observe(k.proc, c, idx)
		dk := diffKey(k)
		next[dk] = c
		if idx > 0 {
			din, dout := r.deltaFor(dk, c)
			acc.add(idx-1, din, dout)
		}
	}
	r.last = next
	if idx > 0 && r.cfg.Emit != nil {
		r.cfg.Emit(r.tick(idx - 1))
	}
}

// recordSpan adds one measured interval ending at at. A process seen in it
// counts as present at both of the span's stamps.
func (r *run) recordSpan(cs []Counter, at time.Duration) {
	idx := len(r.tr.stamps)
	r.tr.stamps = append(r.tr.stamps, at)
	for k, c := range aggregate(cs) {
		acc := r.observe(k.proc, c, idx-1)
		acc.last = idx
		acc.add(idx-1, c.In, c.Out)
	}
	if r.cfg.Emit != nil {
		r.cfg.Emit(r.tick(idx - 1))
	}
}

// deltaFor diffs c against the previous sample. A counter first seen after
// the baseline started at 0 when its socket (or process) was created, so its
// whole value moved since then and counts in full. A counter that went
// backwards belongs to a new socket reusing the key and counts in full too.
func (r *run) deltaFor(k sockKey, c Counter) (int64, int64) {
	prev, ok := r.last[k]
	if !ok {
		return c.In, c.Out
	}
	return grow(prev.In, c.In), grow(prev.Out, c.Out)
}

func grow(prev, cur int64) int64 {
	if cur < prev {
		return cur
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
	switch basis {
	case BasisNettop:
		return []string{
			"nettop -t external: loopback traffic is not counted",
			"each interval is one `nettop -d -L 2` run, which counts flows that open and close inside it, but bytes moved in the few milliseconds between runs are missed (about 3% of a steady transfer at a 1s interval)",
			"nettop's delay is whole seconds, so --interval and --duration are rounded up to whole seconds",
			"nettop reports no start time, so a pid reused within the window is merged with its predecessor",
		}
	case BasisSSTCP:
		return []string{
			"TCP only: UDP and QUIC traffic are not counted",
			"sockets of other users are invisible without root",
			"loopback sockets are not counted",
			"a socket first seen after the baseline sample counts its whole counter (ss counters start at 0 when the socket opens); a socket that opens and closes between samples is missed",
			"bytes out is bytes_acked, so unacknowledged in-flight data is not counted; a socket shared by several processes is attributed to the lowest pid",
		}
	}
	return nil
}

// Source is where a basis's counters come from: exactly one of Sample and
// Measure is set.
type Source struct {
	Basis   string
	Tool    string
	Sample  Sampler
	Measure Measurer
	// Step, if set, is the interval granularity the tool supports.
	Step time.Duration
}

// Round rounds d up to a multiple of the source's Step.
func (s Source) Round(d time.Duration) time.Duration {
	if s.Step <= 0 || d <= 0 || d%s.Step == 0 {
		return d
	}
	return (d/s.Step + 1) * s.Step
}

// Configure fills cfg's source fields from s.
func (s Source) Configure(cfg *Config) {
	cfg.Sample, cfg.Measure, cfg.Tool = s.Sample, s.Measure, s.Tool
}

// SourceFor returns the source for kind. run serves polled tools; r runs
// the measuring tool, which must die with its context.
func SourceFor(kind hostos.Kind, run netstate.CmdRunner, r proc.Runner) (Source, error) {
	switch hostos.Resolve(kind) {
	case hostos.Darwin:
		return Source{Basis: BasisNettop, Tool: "nettop", Measure: nettopMeasurer(r), Step: time.Second}, nil
	case hostos.Linux:
		return Source{Basis: BasisSSTCP, Tool: "ss", Sample: ssSampler(run)}, nil
	}
	return Source{}, hostos.Unsupported("network top")
}

func toolErr(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrToolMissing, err)
	}
	return err
}

func nettopMeasurer(r proc.Runner) Measurer {
	return func(ctx context.Context, interval time.Duration) ([]Counter, error) {
		ctx, cancel := context.WithTimeout(ctx, interval+measureSlack)
		defer cancel()
		rows, err := netstate.CollectProcessBytes(ctx, r, interval)
		if err != nil {
			return nil, toolErr(err)
		}
		cs := make([]Counter, 0, len(rows))
		for _, row := range rows {
			cs = append(cs, Counter{PID: row.PID, Command: row.Command, In: row.BytesIn, Out: row.BytesOut})
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
