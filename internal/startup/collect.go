package startup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/services"
)

// Launchctl and sfltool paths. Fixed absolute paths so a PATH entry cannot
// substitute either tool.
const (
	launchctlPath = "/bin/launchctl"
	sfltoolPath   = "/usr/bin/sfltool"
)

const (
	readTimeout = 30 * time.Second
	maxPlist    = 1 << 20
)

// FileSystem is the filesystem seam: plist directories, plists, app bundles
// and executables are all read through it.
type FileSystem interface {
	classify.FileSystem
	ReadDir(dir string) ([]fs.DirEntry, error)
}

// OSFileSystem reads the real filesystem.
type OSFileSystem struct{ classify.OSFileSystem }

// ReadDir implements FileSystem.
func (OSFileSystem) ReadDir(dir string) ([]fs.DirEntry, error) { return os.ReadDir(dir) }

// PlistDir is one launchd plist directory and what its plists are.
type PlistDir struct {
	Path  string
	Scope Scope
	Kind  string
	// Apple marks /System/Library directories, read only with IncludeApple.
	Apple bool
}

// Options configures List. Zero values pick production defaults.
type Options struct {
	OS     hostos.Kind
	Runner proc.Runner
	FS     FileSystem
	// EUID returns the effective uid (default os.Geteuid).
	EUID func() int
	// Getenv reads SUDO_UID so `sudo spectra startup list` reports the
	// invoking user's session rather than root's (default os.Getenv).
	Getenv func(string) string
	// LookupHome returns a uid's home directory (default os/user).
	LookupHome func(uid int) (string, error)
	// LookupUser names a file owner uid (default os/user, falling back to the number).
	LookupUser func(uid uint32) string
	Now        func() time.Time
	// IncludeApple also reads /System/Library/LaunchAgents and LaunchDaemons.
	IncludeApple bool
	// Scope filters the result: "user", "system", or "" / "all".
	Scope string
	// PlistDirs overrides the launchd directories (tests).
	PlistDirs []PlistDir
	// AppDirs are searched for apps whose bundle id matches a label
	// (default /Applications, /Applications/Utilities, ~/Applications).
	AppDirs []string
}

type env struct {
	opts   Options
	runner proc.Runner
	files  FileSystem
	uid    int
	home   string
	root   bool
	// ownersFor limits owner attribution to one label: a change reads every
	// plist directory, /System included, but reports only its own item.
	ownersFor string
}

func newEnv(opts Options) env {
	e := env{opts: opts, runner: opts.Runner, files: opts.FS}
	if e.runner == nil {
		e.runner = proc.OS{}
	}
	if e.files == nil {
		e.files = OSFileSystem{}
	}
	euid := os.Geteuid
	if opts.EUID != nil {
		euid = opts.EUID
	}
	getenv := os.Getenv
	if opts.Getenv != nil {
		getenv = opts.Getenv
	}
	e.uid = euid()
	e.root = e.uid == 0
	if e.root {
		if n, err := strconv.Atoi(getenv("SUDO_UID")); err == nil && n > 0 {
			e.uid = n
		}
	}
	lookupHome := opts.LookupHome
	if lookupHome == nil {
		lookupHome = func(uid int) (string, error) {
			u, err := user.LookupId(strconv.Itoa(uid))
			if err != nil {
				return "", fmt.Errorf("lookup uid %d: %w", uid, err)
			}
			return u.HomeDir, nil
		}
	}
	e.home, _ = lookupHome(e.uid)
	return e
}

func (e env) guiDomain() string { return "gui/" + strconv.Itoa(e.uid) }

func (e env) plistDirs() []PlistDir {
	if e.opts.PlistDirs != nil {
		return e.opts.PlistDirs
	}
	var dirs []PlistDir
	if e.home != "" {
		dirs = append(dirs, PlistDir{Path: filepath.Join(e.home, "Library", "LaunchAgents"), Scope: ScopeUser, Kind: KindLaunchAgent})
	}
	dirs = append(dirs,
		PlistDir{Path: "/Library/LaunchAgents", Scope: ScopeSystem, Kind: KindLaunchAgent},
		PlistDir{Path: "/Library/LaunchDaemons", Scope: ScopeSystem, Kind: KindLaunchDaemon},
		PlistDir{Path: "/System/Library/LaunchAgents", Scope: ScopeSystem, Kind: KindLaunchAgent, Apple: true},
		PlistDir{Path: "/System/Library/LaunchDaemons", Scope: ScopeSystem, Kind: KindLaunchDaemon, Apple: true},
	)
	return dirs
}

func (e env) domainFor(kind string) string {
	if kind == KindLaunchDaemon {
		return "system"
	}
	return e.guiDomain()
}

// run is a read-only subprocess call bounded by readTimeout.
func (e env) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return proc.Output(ctx, e.runner, name, args...)
}

// List reports what launches at login or boot. Only read-only commands run:
// launchctl print-disabled, launchctl print, codesign -dv/--verify and, as
// root, sfltool dumpbtm. Unreadable sources are reported in Report.Sources.
func List(ctx context.Context, opts Options) (Report, error) {
	if hostos.Resolve(opts.OS) != hostos.Darwin {
		return Report{}, hostos.Unsupported("startup items")
	}
	e := newEnv(opts)
	return e.list(ctx, e.readBTM(ctx))
}

// list builds the report from a BTM read the caller made, so a change can
// reuse one `sfltool dumpbtm` for its before and after listings.
func (e env) list(ctx context.Context, b btmRead) (Report, error) {
	now := time.Now
	if e.opts.Now != nil {
		now = e.opts.Now
	}
	rep := Report{UID: e.uid, CollectedAt: now().UTC()}
	m := newMerger()
	rep.Errors = e.readPlists(m)
	rep.Sources = append(rep.Sources, SourceStatus{Name: SourcePlist, Status: StatusOK,
		Detail: fmt.Sprintf("%d plists read, %d unreadable", len(m.rows), len(rep.Errors))})

	btmStatus, btm := b.status, b.items
	m.mergeBTM(btm, e, e.opts.IncludeApple)

	disabled, st := e.readDisabled(ctx)
	rep.Sources = append(rep.Sources, st)
	loaded, st := e.readLoaded(ctx)
	rep.Sources = append(rep.Sources, st, btmStatus)
	for _, r := range m.rows {
		applyState(r, disabled[r.Domain], loaded[r.Domain])
	}

	rep.Sources = append(rep.Sources, e.attributeOwners(ctx, m))
	rep.Items = m.items(Scope(e.opts.Scope))
	return rep, nil
}

func (e env) readPlists(m *merger) []PlistError {
	var errs []PlistError
	for _, dir := range e.plistDirs() {
		if dir.Apple && !e.opts.IncludeApple {
			continue
		}
		entries, err := e.files.ReadDir(dir.Path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, PlistError{Path: dir.Path, Error: err.Error()})
			}
			continue
		}
		for _, ent := range entries {
			if ent.IsDir() || filepath.Ext(ent.Name()) != ".plist" {
				continue
			}
			path := filepath.Join(dir.Path, ent.Name())
			it, err := e.readPlist(path, dir)
			if err != nil {
				errs = append(errs, PlistError{Path: path, Error: err.Error()})
				continue
			}
			m.add(it)
		}
	}
	return errs
}

func (e env) readPlist(path string, dir PlistDir) (*Item, error) {
	fi, err := e.files.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	data, err := e.files.ReadFile(path, maxPlist)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	job, err := services.ParseLaunchPlist(data, path, string(dir.Scope), fi.ModTime())
	if err != nil {
		return nil, err
	}
	if job.Label == "" {
		return nil, errors.New("plist has no Label")
	}
	program := job.Program
	if program == "" && len(job.ProgramArguments) > 0 {
		program = job.ProgramArguments[0]
	}
	trig := triggers(job, dir.Kind == KindLaunchDaemon)
	it := &Item{
		Label: job.Label, Kind: dir.Kind, Scope: dir.Scope, Domain: e.domainFor(dir.Kind),
		PlistPath: path, PlistOwner: e.fileOwner(fi),
		Program: program, ProgramArguments: job.ProgramArguments,
		Trigger: trig[0], Triggers: trig, RunAtLoad: job.RunAtLoad,
		Apple: isApple(job.Label), Sources: []string{SourcePlist}, appleDir: dir.Apple,
	}
	if job.Disabled {
		it.Disabled, it.DisabledBy = true, "plist Disabled key"
	}
	it.assoc = job.AssociatedBundleIdentifiers
	return it, nil
}

func (e env) fileOwner(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if e.opts.LookupUser != nil {
		return e.opts.LookupUser(st.Uid)
	}
	id := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(id); err == nil {
		return u.Username
	}
	return id
}

// triggers summarises the plist's start conditions, strongest first.
func triggers(job services.LaunchJob, daemon bool) []string {
	var out []string
	if job.KeepAlive {
		out = append(out, TriggerKeepAlive)
	}
	if job.RunAtLoad {
		if daemon {
			out = append(out, TriggerBoot)
		} else {
			out = append(out, TriggerLogin)
		}
	}
	if job.StartInterval > 0 {
		out = append(out, TriggerInterval)
	}
	if job.StartCalendar != "" {
		out = append(out, TriggerCalendar)
	}
	if len(out) == 0 {
		out = append(out, TriggerOnDemand)
	}
	return out
}

func isApple(label string) bool { return strings.HasPrefix(label, "com.apple.") }

func (e env) readDisabled(ctx context.Context) (map[string]map[string]bool, SourceStatus) {
	got := map[string]map[string]bool{}
	var failed []string
	for _, d := range []string{e.guiDomain(), "system"} {
		out, err := e.run(ctx, launchctlPath, "print-disabled", d)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d, err))
			continue
		}
		got[d] = parsePrintDisabled(out)
	}
	return got, status(SourcePrintDisabled, failed)
}

// readLoaded reads loaded and running state from `launchctl print <domain>`:
// one call per domain lists every loaded job with its pid, where a
// per-label `launchctl print gui/<uid>/<label>` would cost one call per item,
// and `launchctl list` only covers the caller's own domain.
func (e env) readLoaded(ctx context.Context) (map[string]map[string]serviceState, SourceStatus) {
	got := map[string]map[string]serviceState{}
	var failed []string
	for _, d := range []string{e.guiDomain(), "system"} {
		out, err := e.run(ctx, launchctlPath, "print", d)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d, err))
			continue
		}
		got[d] = parseDomainServices(out)
	}
	return got, status(SourceLaunchctl, failed)
}

func status(name string, failed []string) SourceStatus {
	if len(failed) == 0 {
		return SourceStatus{Name: name, Status: StatusOK}
	}
	return SourceStatus{Name: name, Status: StatusError, Detail: strings.Join(failed, "; ")}
}

// btmRead is one `sfltool dumpbtm` result.
type btmRead struct {
	status SourceStatus
	items  []btmItem
}

// readBTM runs `sfltool dumpbtm` only as root: for any other user it asks
// for administrator authorization in a dialog and blocks until answered.
func (e env) readBTM(ctx context.Context) btmRead {
	if !e.root {
		return btmRead{status: SourceStatus{Name: SourceBTM, Status: StatusNeedsRoot,
			Detail: "sfltool dumpbtm asks for administrator authorization unless run as root; run `sudo spectra startup list` to include Login Items and SMAppService jobs"}}
	}
	out, err := e.run(ctx, sfltoolPath, "dumpbtm")
	if err != nil {
		return btmRead{status: SourceStatus{Name: SourceBTM, Status: StatusError, Detail: err.Error()}}
	}
	items := parseDumpBTM(out)
	return btmRead{status: SourceStatus{Name: SourceBTM, Status: StatusOK, Detail: fmt.Sprintf("%d items", len(items))}, items: items}
}

// applyState sets disabled and loaded state from launchctl. A BTM-only item
// keeps BTM's disposition: that is what System Settings shows and changes.
func applyState(r *Item, disabled map[string]bool, loaded map[string]serviceState) {
	if d, ok := disabled[r.Label]; ok && r.PlistPath != "" {
		r.Disabled, r.DisabledBy = d, "launchctl override"
	}
	if st, ok := loaded[r.Label]; ok {
		r.Loaded = true
		r.PID = st.PID
		r.Running = st.PID > 0
		r.LastExit = st.LastExit
		addSource(r, SourceLaunchctl)
	}
}

func addSource(r *Item, src string) {
	for _, s := range r.Sources {
		if s == src {
			return
		}
	}
	r.Sources = append(r.Sources, src)
}

// merger keys rows by domain and label, and also indexes plist paths, so a
// BTM legacy item and its launchd plist become one row.
type merger struct {
	rows    []*Item
	byKey   map[string][]*Item
	byPath  map[string]*Item
	byLabel map[string]*Item
}

func newMerger() *merger {
	return &merger{byKey: map[string][]*Item{}, byPath: map[string]*Item{}, byLabel: map[string]*Item{}}
}

// add keeps every plist row. A second plist with the same domain and label
// stays a separate row marked DuplicateOf, since dropping it would hide which
// file launchd may have loaded.
func (m *merger) add(it *Item) {
	key := it.Domain + "/" + it.Label
	m.rows = append(m.rows, it)
	if it.PlistPath != "" {
		m.byPath[it.PlistPath] = it
	}
	for _, prev := range m.byKey[key] {
		prev.DuplicateOf = append(prev.DuplicateOf, it.PlistPath)
		it.DuplicateOf = append(it.DuplicateOf, prev.PlistPath)
	}
	m.byKey[key] = append(m.byKey[key], it)
	if _, ok := m.byLabel[it.Label]; !ok {
		m.byLabel[it.Label] = it
	}
}

func (m *merger) mergeBTM(items []btmItem, e env, includeApple bool) {
	byID := map[string]btmItem{}
	for _, b := range items {
		if prev, ok := byID[b.Identifier]; !ok || prev.URL == "" || b.UID == e.uid {
			byID[b.Identifier] = b
		}
	}
	for _, b := range items {
		if (b.UID > 0 && b.UID != e.uid) || !b.startsAtLogin() {
			continue
		}
		b.resolve(byID)
		if row := m.rowFor(b, e, includeApple); row != nil {
			attachBTM(row, b, byID)
		}
	}
}

// rowFor returns the launchd row a BTM item describes, matched by plist path
// then label, or a new BTM-only row; nil for an Apple item left out.
func (m *merger) rowFor(b btmItem, e env, includeApple bool) *Item {
	label := b.label()
	if row := m.byPath[b.plistPath()]; row != nil {
		return row
	}
	if row := m.byLabel[label]; row != nil {
		return row
	}
	if isApple(label) && !includeApple {
		return nil
	}
	row := btmRow(b, label, e)
	m.add(row)
	return row
}

func attachBTM(row *Item, b btmItem, byID map[string]btmItem) {
	bi := b.BTM
	row.BTM = &bi
	if row.Name == "" {
		row.Name = b.Name
	}
	if len(row.assoc) == 0 {
		row.assoc = b.AssocBundleIDs
	}
	if b.disabled() && row.DisabledBy == "" {
		row.Disabled, row.DisabledBy = true, "btm disposition"
	}
	addSource(row, SourceBTM)
	if row.Owner == nil {
		row.Owner = btmOwner(b, byID)
	}
}

func btmRow(b btmItem, label string, e env) *Item {
	it := &Item{
		Label: label, Name: b.Name, Kind: KindLoginItem, Scope: b.scope(),
		Program: b.ExecutablePath, Trigger: TriggerLogin, Triggers: []string{TriggerLogin},
		Apple: isApple(label),
	}
	if it.Program == "" {
		it.Program = fileURLPath(b.URL, ".app")
	}
	switch {
	case strings.Contains(b.Type, "daemon"):
		it.Domain, it.Trigger, it.Triggers = "system", TriggerBoot, []string{TriggerBoot}
	case strings.Contains(b.Type, "agent"), strings.Contains(b.Type, "login item"):
		// SMAppService agents and login items run as launchd jobs in the
		// user's GUI domain under their label, so launchctl reports their pid.
		it.Domain = e.guiDomain()
	}
	return it
}

// btmOwner names the app BTM attributes an item to: its parent app when it
// has one, else the item itself when it is an app; nil when BTM names no
// app (legacy agents under "Unknown Developer" or a developer group).
func btmOwner(b btmItem, byID map[string]btmItem) *Owner {
	app := b
	if p, ok := byID[b.ParentIdentifier]; ok && fileURLPath(p.URL, ".app") != "" {
		app = p
	}
	path := fileURLPath(app.URL, ".app")
	if path == "" {
		return nil
	}
	o := &Owner{
		App: strings.TrimSuffix(filepath.Base(path), ".app"), BundlePath: path,
		BundleID: app.BundleID, TeamID: app.TeamID, Developer: app.DeveloperName, Via: "btm",
	}
	if o.TeamID == "" {
		o.TeamID = b.TeamID
	}
	if o.Developer == "" {
		o.Developer = b.DeveloperName
	}
	return o
}

// fillFromBTM completes an owner found on disk with the team and developer
// BTM recorded, or records just those when no app was found.
func fillFromBTM(r *Item) {
	if r.BTM == nil || (r.BTM.TeamID == "" && r.BTM.DeveloperName == "") {
		return
	}
	if r.Owner == nil {
		r.Owner = &Owner{Via: "btm"}
	}
	if r.Owner.TeamID == "" {
		r.Owner.TeamID = r.BTM.TeamID
	}
	if r.Owner.Developer == "" {
		r.Owner.Developer = r.BTM.DeveloperName
	}
}

func (m *merger) items(scope Scope) []Item {
	out := make([]Item, 0, len(m.rows))
	for _, r := range m.rows {
		if scope != "" && scope != "all" && r.Scope != scope {
			continue
		}
		out = append(out, *r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == ScopeUser
		}
		return out[i].Label < out[j].Label
	})
	return out
}
