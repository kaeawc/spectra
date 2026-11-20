// Package classify gives every running process a role (app, helper, system,
// shell, terminal, AI agent, CLI), a language runtime, its owning .app and,
// on macOS, a code-signing summary, with the evidence for each choice.
//
// Per-process signals (paths, argv) are cheap and computed every call. The
// expensive per-executable work (reading the binary, running codesign) is
// cached by (path, size, mtime, device, inode, ctime) in memory and, when a
// Store is given, on disk, so a second invocation stays warm and a rebuilt
// or replaced binary misses.
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
	Command        string // argv[0] base name (comm on Linux); never trusted as a path
	ExecutablePath string // kernel-reported executable; may be empty
	// ExecutableDeleted is set when the kernel reports the executable as
	// deleted (Linux "(deleted)" suffix): the path no longer holds the code.
	ExecutableDeleted bool
	CommandLine       string // argv joined by spaces
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
	// ProcFS is the procfs root on Linux (default "/proc"). Binaries are read
	// through <ProcFS>/<pid>/exe so a replaced or deleted file still yields
	// the code the process runs.
	ProcFS string
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
	procfs  string

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
		procfs:  opts.ProcFS,
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
	if c.procfs == "" {
		c.procfs = "/proc"
	}
	return c
}

// exeKey identifies one version of an executable file. Size and mtime alone
// survive cp -p, rsync and tar; the inode and kernel-set ctime do not.
type exeKey struct {
	path  string
	size  int64
	mtime int64
	id    fileID
	// seal is the identity of the enclosing bundle's CodeResources.
	// codesign --verify --strict checks the bundle's sealed resources, so a
	// signing verdict goes stale when they change though the executable
	// does not. The zero value means not in a bundle or no seal file.
	seal sealID
}

type sealID struct {
	size, mtime int64
	id          fileID
}

// cacheVersion prefixes every disk key; bump it whenever exeEntry (or
// anything it contains) changes shape or meaning.
const cacheVersion = "classify/v5"

// exeEntry is the cached per-executable work. A nil field was not needed yet.
type exeEntry struct {
	Binary  *binaryVerdict  `json:"binary,omitempty"`
	Signing *signingVerdict `json:"signing,omitempty"`

	// retryBin and retrySign mark a part that failed for a reason that says
	// nothing durable about the file (a read race, a cancelled or timed-out
	// codesign). The part answers the current call as unknown, is never
	// written to disk, and is redone on the next Classify call.
	retryBin  bool
	retrySign bool
}

// plan is the per-process state gathered before the expensive phase.
type plan struct {
	in       Input
	exe      string // kernel-reported executable path
	read     string // where to read it: exe, or /proc/<pid>/exe on Linux
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
// bundle and cold phases early: affected processes get runtime unknown, and
// ctx.Err() is returned alongside the otherwise complete results.
func (c *Classifier) Classify(ctx context.Context, inputs []Input) ([]Result, error) {
	plans := make([]plan, len(inputs))
	for i, in := range inputs {
		plans[i] = c.locate(in)
	}
	err := c.loadBundles(ctx, plans)
	for i := range plans {
		c.prepare(&plans[i])
	}
	if fillErr := c.fill(ctx, plans); err == nil {
		err = fillErr
	}
	out := make([]Result, len(plans))
	for i := range plans {
		out[i] = c.finish(plans[i])
	}
	return out, err
}

// locate resolves the executable and its bundle without touching the disk.
// argv[0] is never used as a path: any process can set it (exec -a).
func (c *Classifier) locate(in Input) plan {
	p := plan{in: in, exe: in.ExecutablePath, read: in.ExecutablePath}
	if c.os == hostos.Linux && p.exe != "" && in.PID > 0 {
		p.read = filepath.Join(c.procfs, strconv.Itoa(in.PID), "exe")
	}
	if c.os == hostos.Darwin {
		p.loc = locateBundle(p.exe)
	}
	return p
}

// loadBundles reads each owning bundle once, on the worker pool, so a slow
// or hostile bundle cannot stall the listing past cancellation.
func (c *Classifier) loadBundles(ctx context.Context, plans []plan) error {
	var roots []string
	seen := map[string]bool{}
	for _, p := range plans {
		root := p.loc.root
		if root == "" || p.loc.frameworkInternal || seen[root] {
			continue
		}
		seen[root] = true
		c.mu.Lock()
		_, cached := c.bundles[root]
		c.mu.Unlock()
		if !cached {
			roots = append(roots, root)
		}
	}
	err := c.pool(ctx, len(roots), func(i int) {
		b := readBundle(c.files, roots[i])
		c.mu.Lock()
		c.bundles[roots[i]] = b
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range plans {
		root := plans[i].loc.root
		if root == "" || plans[i].loc.frameworkInternal {
			continue
		}
		b, ok := c.bundles[root]
		if !ok {
			b = bundleInfo{name: strings.TrimSuffix(filepath.Base(root), ".app")}
		}
		plans[i].bundle = b
	}
	return err
}

// prepare applies the cheap runtime rules and keys the executable for the
// cold phase.
func (c *Classifier) prepare(p *plan) {
	p.runtime, p.evidence = c.nameRuntime(*p)
	p.needBin = p.runtime == ""
	p.needSign = c.os == hostos.Darwin && p.exe != ""
	if !p.needBin && !p.needSign {
		return
	}
	fail := func(ev string) {
		if p.needBin {
			p.runtime, p.evidence = RuntimeUnknown, ev
			p.needBin = false
		}
		p.needSign = false
	}
	if p.exe == "" {
		fail("executable path not visible")
		return
	}
	fi, err := c.files.Stat(p.read)
	if err != nil {
		fail(fmt.Sprintf("executable unreadable: %v", err))
		return
	}
	if !fi.Mode().IsRegular() {
		fail(fmt.Sprintf("executable is not a regular file (%s)", fi.Mode().Type()))
		return
	}
	p.key = exeKey{path: p.exe, size: fi.Size(), mtime: fi.ModTime().UnixNano(), id: identity(fi)}
	p.key.seal = c.sealIdentity(p.exe)
	p.keyed = true
}

// sealIdentity stats the CodeResources file of the nearest .app or
// .framework enclosing exe; zero when there is none.
func (c *Classifier) sealIdentity(exe string) sealID {
	for dir := filepath.Dir(exe); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		var rel string
		switch filepath.Ext(dir) {
		case ".app":
			rel = "Contents/_CodeSignature/CodeResources"
		case ".framework":
			rel = "Versions/Current/_CodeSignature/CodeResources"
		default:
			continue
		}
		fi, err := c.files.Stat(filepath.Join(dir, rel))
		if err != nil {
			return sealID{}
		}
		return sealID{size: fi.Size(), mtime: fi.ModTime().UnixNano(), id: identity(fi)}
	}
	return sealID{}
}

// nameRuntime applies the cheap, path-only runtime rules in order; an empty
// runtime means the binary itself must be inspected.
func (c *Classifier) nameRuntime(p plan) (Runtime, string) {
	if c.os == hostos.Linux && isKernelThread(roleInput{exe: p.exe, pid: p.in.PID, ppid: p.in.PPID, cmdline: p.in.CommandLine}) {
		return RuntimeNative, "kernel thread"
	}
	if p.exe == "" {
		base := strings.TrimPrefix(p.in.Command, "-")
		if rt, ok := interpreterRuntime(base); ok {
			return rt, fmt.Sprintf("argv[0] %s names an interpreter%s", base, unverified)
		}
		return RuntimeUnknown, "executable path not visible"
	}
	base := filepath.Base(p.exe)
	if rt, ok := interpreterRuntime(base); ok {
		return rt, fmt.Sprintf("interpreter executable %s", base)
	}
	if p.loc.appOwned() && p.bundle.electron {
		return RuntimeElectron, fmt.Sprintf("%s contains Electron Framework.framework", filepath.Base(p.loc.root))
	}
	if p.loc.appOwned() && p.bundle.jvm != "" && p.bundle.isMainExecutable(p.loc.root, p.exe) {
		return RuntimeJVM, fmt.Sprintf("app bundles a Java runtime at %s", p.bundle.jvm)
	}
	return "", ""
}

// pool runs do(0..n-1) on at most c.workers goroutines and stops handing out
// work once ctx is done, returning ctx.Err() in that case.
func (c *Classifier) pool(ctx context.Context, n int, do func(int)) error {
	if n == 0 {
		return ctx.Err()
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(c.workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				// A job handed out just before cancellation must not start.
				if ctx.Err() == nil {
					do(i)
				}
			}
		}()
	}
	var err error
feed:
	for i := range n {
		// select alone picks randomly when both cases are ready.
		if err = ctx.Err(); err != nil {
			break feed
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		case work <- i:
		}
	}
	close(work)
	wg.Wait()
	if err == nil {
		err = ctx.Err()
	}
	return err
}

// job is the cold work for one executable version.
type job struct {
	key      exeKey
	read     string
	needBin  bool
	needSign bool
}

// fill makes sure every keyed plan's executable has the entry parts it needs,
// from memory, then disk, then a bounded worker pool.
func (c *Classifier) fill(ctx context.Context, plans []plan) error {
	jobs := c.pendingJobs(plans)
	return c.pool(ctx, len(jobs), func(i int) { c.run(ctx, jobs[i]) })
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
			j = &job{key: p.key, read: p.read}
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
		if j.needBin && (e.Binary == nil || e.retryBin) || j.needSign && (e.Signing == nil || e.retrySign) {
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
	if j.needBin && (e.Binary == nil || e.retryBin) {
		v, err := inspectBinary(c.files, j.read, c.maxScan)
		e.Binary, e.retryBin = &v, err != nil
	}
	if j.needSign && (e.Signing == nil || e.retrySign) && ctx.Err() == nil {
		v, err := readSigning(ctx, c.os, c.runner, j.read)
		e.Signing, e.retrySign = &v, err != nil
	}
	c.mu.Lock()
	c.exes[j.key] = e
	c.mu.Unlock()
	if !e.retryBin && !e.retrySign && c.store != nil {
		if data, err := json.Marshal(e); err == nil {
			_ = c.store.Put(c.diskKey(j.key), data)
		}
	}
}

func (c *Classifier) diskKey(k exeKey) []byte {
	num := func(n int64) []byte { return []byte(strconv.FormatInt(n, 10) + "\x00") }
	unum := func(n uint64) []byte { return []byte(strconv.FormatUint(n, 10) + "\x00") }
	return cache.Key([]byte(cacheVersion+"\x00"), []byte(c.os.String()+"\x00"), []byte(k.path+"\x00"),
		num(k.size), num(k.mtime), unum(k.id.dev), unum(k.id.ino), num(k.id.ctime),
		num(k.seal.size), num(k.seal.mtime), unum(k.seal.id.dev), unum(k.seal.id.ino), num(k.seal.id.ctime))
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
	var goMod string
	if p.needBin {
		if e.Binary != nil {
			r.Runtime, runtimeEv, goMod = e.Binary.Runtime, e.Binary.Evidence, e.Binary.GoModule
		} else {
			r.Runtime, runtimeEv = RuntimeUnknown, "classification cancelled before the binary was read"
		}
	}
	if p.in.ExecutableDeleted {
		runtimeEv += fmt.Sprintf("; %s was deleted, the running image was read through %s", p.exe, p.read)
	}
	var platform bool
	var signEv string
	if p.needSign && e.Signing != nil {
		s := e.Signing.Signing
		r.Signing = &s
		platform = e.Signing.Platform
		signEv = signingEvidence(*e.Signing)
	}
	if p.loc.root != "" && !p.loc.frameworkInternal {
		r.App = &App{Name: p.bundle.name, BundlePath: p.loc.root, BundleID: p.bundle.bundleID}
	}
	role, roleEv := decideRole(roleInput{
		os: c.os, pid: p.in.PID, ppid: p.in.PPID, uid: p.in.UID,
		exe: p.exe, argv0: p.in.Command, cmdline: p.in.CommandLine,
		runtime: r.Runtime, loc: p.loc, bundle: p.bundle, platform: platform,
		goModule: goMod, script: c.script(p.in.CommandLine, r.Runtime),
	})
	r.Role = role
	r.Evidence = []string{"role: " + roleEv, "runtime: " + runtimeEv}
	if signEv != "" {
		r.Evidence = append(r.Evidence, signEv)
	}
	return r
}

// script returns the script an interpreter runs, following symlinks (an npm
// bin/ link) when the path alone names no package.
func (c *Classifier) script(cmdline string, rt Runtime) string {
	switch rt {
	case RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython:
	default:
		return ""
	}
	s := interpreterScript(cmdline, rt)
	if s == "" || npmPackage(s) != "" || !filepath.IsAbs(s) {
		return s
	}
	if resolved, err := c.files.EvalSymlinks(s); err == nil {
		return resolved
	}
	return s
}
