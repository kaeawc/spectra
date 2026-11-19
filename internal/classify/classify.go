// Package classify gives every running process a role (app, helper, system,
// shell, terminal, AI agent, CLI), a language runtime, its owning .app and,
// on macOS, a code-signing summary, with the evidence for each choice.
//
// Per-process signals (paths, argv) are cheap and computed every call. The
// expensive per-executable work (reading the binary, running codesign) is
// cached by (path, size, mtime) in memory and, when a Store is given, on disk,
// so a second invocation stays warm and a rebuilt binary misses.
package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// Input is the per-process identity the classifier reads.
type Input struct {
	PID            int
	PPID           int
	UID            int
	Command        string // argv[0] base name
	ExecutablePath string // kernel-reported executable; may be empty
	CommandLine    string // argv joined by spaces
}

// App is the outermost .app that owns a process.
type App struct {
	Name       string `json:"name"`
	BundlePath string `json:"bundle_path"`
	BundleID   string `json:"bundle_id,omitempty"`
}

// Result is one process's classification.
type Result struct {
	Role     Role     `json:"role"`
	Runtime  Runtime  `json:"runtime"`
	App      *App     `json:"app,omitempty"`
	Signing  *Signing `json:"signing,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// Store is the on-disk cache seam; *cache.ShardedStore satisfies it.
type Store interface {
	Get(key []byte) ([]byte, bool, error)
	Put(key, data []byte) error
}

// Options configures a Classifier. Zero values pick production defaults.
type Options struct {
	// OS selects platform behaviour; the zero value resolves to the host.
	OS hostos.Kind
	// Runner runs codesign (macOS only). Defaults to proc.OS{}.
	Runner proc.Runner
	// FS reads executables, Info.plists and bundle layout. Defaults to the OS.
	FS FileSystem
	// Store persists per-executable results across invocations. Optional.
	Store Store
	// Workers bounds concurrent cold classifications. Defaults to 8.
	Workers int
	// MaxScanBytes caps the Rust marker scan per binary.
	MaxScanBytes int64
}

// Classifier classifies processes. It is safe for concurrent use and keeps
// its in-memory caches for its lifetime.
type Classifier struct {
	os      hostos.Kind
	runner  proc.Runner
	files   FileSystem
	store   Store
	workers int
	maxScan int64

	mu      sync.Mutex
	exes    map[exeKey]exeEntry
	bundles map[string]bundleInfo
}

// New returns a Classifier.
func New(opts Options) *Classifier {
	c := &Classifier{
		os:      hostos.Resolve(opts.OS),
		runner:  opts.Runner,
		files:   opts.FS,
		store:   opts.Store,
		workers: opts.Workers,
		maxScan: opts.MaxScanBytes,
		exes:    map[exeKey]exeEntry{},
		bundles: map[string]bundleInfo{},
	}
	if c.runner == nil {
		c.runner = proc.OS{}
	}
	if c.files == nil {
		c.files = OSFileSystem{}
	}
	if c.workers <= 0 {
		c.workers = 8
	}
	if c.maxScan <= 0 {
		c.maxScan = DefaultMaxScanBytes
	}
	return c
}

// exeKey identifies one version of an executable file.
type exeKey struct {
	path  string
	size  int64
	mtime int64
}

// exeEntry is the cached per-executable work. A nil field was not needed yet.
type exeEntry struct {
	Binary  *binaryVerdict  `json:"binary,omitempty"`
	Signing *signingVerdict `json:"signing,omitempty"`
}

// plan is the per-process state gathered before the expensive phase.
type plan struct {
	in       Input
	exe      string
	loc      bundleLoc
	bundle   bundleInfo
	runtime  Runtime
	evidence string
	key      exeKey
	keyed    bool
	needBin  bool
	needSign bool
}

// Classify returns one Result per input, in order. A cancelled ctx stops the
// cold phase early: affected processes get runtime unknown, and ctx.Err() is
// returned alongside the otherwise complete results.
func (c *Classifier) Classify(ctx context.Context, inputs []Input) ([]Result, error) {
	plans := make([]plan, len(inputs))
	for i, in := range inputs {
		plans[i] = c.planFor(in)
	}
	err := c.fill(ctx, plans)
	out := make([]Result, len(plans))
	for i := range plans {
		out[i] = c.finish(plans[i])
	}
	return out, err
}

func (c *Classifier) planFor(in Input) plan {
	p := plan{in: in, exe: c.resolveExe(in)}
	if c.os == hostos.Darwin {
		p.loc = locateBundle(p.exe)
		if p.loc.root != "" && !p.loc.frameworkInternal {
			p.bundle = c.bundle(p.loc.root)
		}
	}
	p.runtime, p.evidence = c.nameRuntime(p)
	p.needBin = p.runtime == ""
	p.needSign = c.os == hostos.Darwin && p.exe != ""
	if !p.needBin && !p.needSign {
		return p
	}
	fi, err := c.files.Stat(p.exe)
	if p.exe == "" || err != nil {
		if p.needBin {
			p.runtime, p.evidence = RuntimeUnknown, unstatEvidence(p.exe, err)
			p.needBin = false
		}
		p.needSign = false
		return p
	}
	p.key = exeKey{path: p.exe, size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	p.keyed = true
	return p
}

func unstatEvidence(exe string, err error) string {
	if exe == "" {
		return "executable path not visible"
	}
	return fmt.Sprintf("executable unreadable: %v", err)
}

// resolveExe prefers the kernel-reported path and falls back to an absolute
// argv[0] (processes of other users on Linux hide /proc/<pid>/exe).
func (c *Classifier) resolveExe(in Input) string {
	if in.ExecutablePath != "" {
		return in.ExecutablePath
	}
	fields := strings.Fields(in.CommandLine)
	if len(fields) > 0 && filepath.IsAbs(fields[0]) {
		if _, err := c.files.Stat(fields[0]); err == nil {
			return fields[0]
		}
	}
	return ""
}

// nameRuntime applies the cheap, path-only runtime rules in order; an empty
// runtime means the binary itself must be inspected.
func (c *Classifier) nameRuntime(p plan) (Runtime, string) {
	base := strings.TrimPrefix(p.in.Command, "-")
	if p.exe != "" {
		base = filepath.Base(p.exe)
	}
	if c.os == hostos.Linux && isKernelThread(roleInput{exe: p.exe, pid: p.in.PID, ppid: p.in.PPID, cmdline: p.in.CommandLine}) {
		return RuntimeNative, "kernel thread"
	}
	if rt, ok := interpreterRuntime(base); ok {
		return rt, fmt.Sprintf("interpreter executable %s", base)
	}
	if p.loc.appOwned() && p.bundle.electron {
		return RuntimeElectron, fmt.Sprintf("%s contains Electron Framework.framework", filepath.Base(p.loc.root))
	}
	if p.loc.appOwned() && p.bundle.jvm != "" && p.bundle.isMainExecutable(p.loc.root, p.exe) {
		return RuntimeJVM, fmt.Sprintf("app bundles a Java runtime at %s", p.bundle.jvm)
	}
	if p.exe == "" {
		return RuntimeUnknown, "executable path not visible"
	}
	return "", ""
}

func (c *Classifier) bundle(root string) bundleInfo {
	c.mu.Lock()
	b, ok := c.bundles[root]
	c.mu.Unlock()
	if ok {
		return b
	}
	b = readBundle(c.files, root)
	c.mu.Lock()
	c.bundles[root] = b
	c.mu.Unlock()
	return b
}

// job is the cold work for one executable version.
type job struct {
	key      exeKey
	needBin  bool
	needSign bool
}

// fill makes sure every keyed plan's executable has the entry parts it needs,
// from memory, then disk, then a bounded worker pool.
func (c *Classifier) fill(ctx context.Context, plans []plan) error {
	jobs := c.pendingJobs(plans)
	if len(jobs) == 0 {
		return nil
	}
	work := make(chan job)
	var wg sync.WaitGroup
	for range min(c.workers, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				c.run(ctx, j)
			}
		}()
	}
	var err error
feed:
	for _, j := range jobs {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		case work <- j:
		}
	}
	close(work)
	wg.Wait()
	if err == nil {
		err = ctx.Err()
	}
	return err
}

func (c *Classifier) pendingJobs(plans []plan) []job {
	byKey := map[exeKey]*job{}
	var order []exeKey
	for _, p := range plans {
		if !p.keyed {
			continue
		}
		j, ok := byKey[p.key]
		if !ok {
			j = &job{key: p.key}
			byKey[p.key] = j
			order = append(order, p.key)
		}
		j.needBin = j.needBin || p.needBin
		j.needSign = j.needSign || p.needSign
	}
	var jobs []job
	for _, k := range order {
		j := *byKey[k]
		e := c.lookup(k)
		if j.needBin && e.Binary == nil || j.needSign && e.Signing == nil {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

// lookup returns the cached entry for k, promoting a disk hit into memory.
func (c *Classifier) lookup(k exeKey) exeEntry {
	c.mu.Lock()
	e, ok := c.exes[k]
	c.mu.Unlock()
	if ok || c.store == nil {
		return e
	}
	data, hit, err := c.store.Get(c.diskKey(k))
	if err != nil || !hit || json.Unmarshal(data, &e) != nil {
		return exeEntry{}
	}
	c.mu.Lock()
	c.exes[k] = e
	c.mu.Unlock()
	return e
}

func (c *Classifier) run(ctx context.Context, j job) {
	e := c.lookup(j.key)
	persist := true
	if j.needBin && e.Binary == nil {
		v := inspectBinary(c.files, j.key.path, c.maxScan)
		e.Binary = &v
	}
	if j.needSign && e.Signing == nil && ctx.Err() == nil {
		v, err := readSigning(ctx, c.os, c.runner, j.key.path)
		if err != nil {
			// A runner failure (cancel, missing codesign) says nothing durable
			// about the file: keep it for this run only.
			persist = false
		}
		e.Signing = &v
	}
	c.mu.Lock()
	c.exes[j.key] = e
	c.mu.Unlock()
	if persist && c.store != nil {
		if data, err := json.Marshal(e); err == nil {
			_ = c.store.Put(c.diskKey(j.key), data)
		}
	}
}

func (c *Classifier) diskKey(k exeKey) []byte {
	return cache.Key([]byte("classify/v1\x00"), []byte(c.os.String()+"\x00"), []byte(k.path+"\x00"),
		[]byte(strconv.FormatInt(k.size, 10)+"\x00"), []byte(strconv.FormatInt(k.mtime, 10)))
}

func (c *Classifier) finish(p plan) Result {
	var e exeEntry
	if p.keyed {
		c.mu.Lock()
		e = c.exes[p.key]
		c.mu.Unlock()
	}
	r := Result{Runtime: p.runtime}
	runtimeEv := p.evidence
	if p.needBin {
		if e.Binary != nil {
			r.Runtime, runtimeEv = e.Binary.Runtime, e.Binary.Evidence
		} else {
			r.Runtime, runtimeEv = RuntimeUnknown, "classification cancelled before the binary was read"
		}
	}
	var platform bool
	if p.needSign && e.Signing != nil {
		s := e.Signing.Signing
		r.Signing = &s
		platform = e.Signing.Platform
	}
	if p.loc.root != "" && !p.loc.frameworkInternal {
		r.App = &App{Name: p.bundle.name, BundlePath: p.loc.root, BundleID: p.bundle.bundleID}
	}
	role, roleEv := decideRole(roleInput{
		os: c.os, pid: p.in.PID, ppid: p.in.PPID, uid: p.in.UID,
		exe: p.exe, argv0: p.in.Command, cmdline: p.in.CommandLine,
		runtime: r.Runtime, loc: p.loc, bundle: p.bundle, platform: platform,
	})
	r.Role = role
	r.Evidence = []string{"role: " + roleEv, "runtime: " + runtimeEv}
	return r
}
