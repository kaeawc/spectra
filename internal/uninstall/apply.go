package uninstall

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

// Apply errors. Each aborts before anything is moved.
var (
	ErrProcessesRunning = errors.New("the app is running; quit it or pass --quit")
	ErrQuitTimeout      = errors.New("processes still running after SIGTERM")
	ErrPlanRejected     = errors.New("plan rejected")
	ErrRunAsRoot        = errors.New("run apply as the user who owns the files, not root")
	// ErrBundleMissing means the plan's bundle is gone or is now another app.
	ErrBundleMissing = errors.New("the plan's app bundle is gone or is no longer that app; pass --leftovers to move only its exact bundle-id folders")
	// ErrBundleNotMovable means the bundle would stay while its data went.
	ErrBundleNotMovable = errors.New("the app bundle will not be moved, so its data is left alone; pass --data-only to move the data anyway")
	// ErrRelaunched means an app process appeared after the agents were
	// booted out, for example one a KeepAlive agent started during the quit.
	ErrRelaunched = errors.New("the app started again after it quit")
)

// quitWait bounds how long apply waits for SIGTERMed processes to exit.
const quitWait = 10 * time.Second

// ApplyOptions controls Apply.
type ApplyOptions struct {
	IncludeLowConfidence bool
	DryRun               bool
	Quit                 bool
	// Leftovers allows a plan whose bundle is gone. Only exact
	// <location>/<bundle id> paths are acted on.
	Leftovers bool
	// DataOnly moves the app's data even when the bundle cannot be moved.
	DataOnly bool
}

// Moved is an item moved (or, in a dry run, to be moved) to the Trash.
type Moved struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	To   string `json:"to"`
}

// Skipped is a planned item that was left alone.
type Skipped struct {
	Path   string `json:"path"`
	Kind   Kind   `json:"kind"`
	Reason string `json:"reason"`
}

// NeedsRoot is an item Spectra will not touch, with a command for the user.
// Command is empty when an earlier entry's command already covers it.
type NeedsRoot struct {
	Path    string `json:"path"`
	Kind    Kind   `json:"kind"`
	Command string `json:"command"`
}

// Bootout records a `launchctl bootout` of a user launch agent.
type Bootout struct {
	Target string `json:"target"`
	Error  string `json:"error,omitempty"`
	// Failed is set when the bootout failed for a reason other than the
	// job not being loaded; the agent's plist is then left in place.
	Failed bool `json:"failed,omitempty"`
}

// Result is the outcome of Apply.
type Result struct {
	DryRun     bool        `json:"dry_run"`
	Leftovers  bool        `json:"leftovers,omitempty"`
	App        string      `json:"app"`
	BundleID   string      `json:"bundle_id"`
	TrashDir   string      `json:"trash_dir,omitempty"`
	Moved      []Moved     `json:"moved"`
	Skipped    []Skipped   `json:"skipped"`
	NeedsRoot  []NeedsRoot `json:"needs_root"`
	Running    []Process   `json:"running"`
	Terminated []Process   `json:"terminated"`
	Bootouts   []Bootout   `json:"bootouts"`
	Relaunched []Process   `json:"relaunched,omitempty"`
}

func newResult(p Plan, dry bool) Result {
	return Result{
		DryRun: dry, App: p.App.Path, BundleID: p.App.BundleID,
		Moved: []Moved{}, Skipped: []Skipped{}, NeedsRoot: []NeedsRoot{},
		Running: []Process{}, Terminated: []Process{}, Bootouts: []Bootout{},
	}
}

// Apply acts on the items of plan only. Each item is re-derived from the
// current state and skipped on drift; an item the app no longer matches at
// all rejects the whole plan before anything changes.
func Apply(ctx context.Context, opts Options, plan Plan, ao ApplyOptions) (Result, error) {
	res := newResult(plan, ao.DryRun)
	if err := supported(opts); err != nil {
		return res, err
	}
	o := opts.withDefaults()
	if o.UID == 0 {
		return res, ErrRunAsRoot
	}
	ident, leftovers, err := verifyPlanIdentity(ctx, o, plan, ao)
	if err != nil {
		return res, err
	}
	res.Leftovers = leftovers
	if err := checkPlanPaths(o, plan); err != nil {
		return res, err
	}
	var freshItems []Item
	if leftovers {
		freshItems = collectLeftovers(ctx, o, ident)
	} else {
		freshItems = collect(ctx, o, ident).items
	}
	todo, err := triage(o, ident, plan, freshItems, ao, leftovers, &res)
	if err != nil {
		return res, err
	}
	if !isDir(o.FS, o.Trash) {
		return res, fmt.Errorf("trash %s is not a directory", o.Trash)
	}
	if !leftovers && !ao.DataOnly && !hasBundle(todo) {
		return res, fmt.Errorf("%w (%s)", ErrBundleNotMovable, bundleBlocker(res, ident.Path))
	}
	return res, quitAndMove(ctx, o, ident, todo, ao, &res)
}

// quitAndMove stops the app, moves the bundle, boots out its agents, then
// moves the remaining items. Agents are booted out only after every process
// has exited and the bundle has moved, so a failed quit or a bundle that
// will not move leaves launchd untouched.
func quitAndMove(ctx context.Context, o Options, ident App, todo []Item, ao ApplyOptions, res *Result) error {
	running := appProcesses(o.Procs(ctx), ident.Path)
	res.Running = append(res.Running, running...)
	if len(running) > 0 && !ao.Quit {
		return fmt.Errorf("%w (%d processes)", ErrProcessesRunning, len(running))
	}
	if err := stopProcesses(ctx, o, ident.Path, running, ao.DryRun, res); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}
	mv, err := newMover(o, ident, ao, res)
	if err != nil {
		mv.skipAll(todo, "move failed: "+err.Error())
		return fmt.Errorf("no Trash folder, so nothing was booted out or moved: %w", err)
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].Kind == KindBundle && todo[j].Kind != KindBundle })
	rest, bundleDest := todo, ""
	if todo[0].Kind == KindBundle {
		rest = todo[1:]
		dest, ok := mv.move(todo[0])
		switch {
		case ok:
			bundleDest = dest
		case !ao.DataOnly:
			mv.skipAll(rest, "not moved: the app bundle could not be moved, so no launch agent was booted out (pass --data-only to move data anyway)")
			return nil
		}
	}
	rest = keepLoaded(rest, bootoutAgents(ctx, o, ident, rest, ao.DryRun, res), mv)
	if !ao.DryRun {
		if err := checkRelaunch(ctx, o, ident, bundleDest, res); err != nil {
			mv.skipAll(rest, "not moved: the app started again")
			return err
		}
	}
	for _, it := range rest {
		mv.move(it)
	}
	return nil
}

// checkRelaunch looks for app processes that appeared after the quit, from
// the bundle's old path or from where it was moved in the Trash.
func checkRelaunch(ctx context.Context, o Options, ident App, bundleDest string, res *Result) error {
	procs := o.Procs(ctx)
	again := appProcesses(procs, ident.Path)
	if bundleDest != "" {
		again = append(again, appProcesses(procs, bundleDest)...)
	}
	if len(again) == 0 {
		return nil
	}
	res.Relaunched = again
	where := "nothing was moved"
	if bundleDest != "" {
		where = "the bundle is already in " + bundleDest + " and nothing else was moved; quit the app, move the bundle back to " + ident.Path + ", then plan and apply again"
	}
	return fmt.Errorf("%w: %d processes run from the bundle after %d launch agents were booted out; %s",
		ErrRelaunched, len(again), len(res.Bootouts), where)
}

func hasBundle(todo []Item) bool {
	for _, it := range todo {
		if it.Kind == KindBundle {
			return true
		}
	}
	return false
}

// bundleBlocker explains why the bundle is not among the items to move.
func bundleBlocker(res Result, bundle string) string {
	for _, n := range res.NeedsRoot {
		if n.Path == bundle {
			return "you cannot move " + bundle + ": " + n.Command
		}
	}
	for _, s := range res.Skipped {
		if s.Path == bundle {
			return bundle + " skipped: " + s.Reason
		}
	}
	return bundle + " is not in the plan"
}

// verifyPlanIdentity checks the plan against the rules and against the
// bundle on disk. It reports leftovers mode when the bundle is gone and
// ao.Leftovers allows that.
func verifyPlanIdentity(ctx context.Context, o Options, plan Plan, ao ApplyOptions) (App, bool, error) {
	reject := func(err error) (App, bool, error) { return App{}, false, fmt.Errorf("%w: %w", ErrPlanRejected, err) }
	if plan.Schema != PlanSchema {
		return reject(fmt.Errorf("schema %q, want %q", plan.Schema, PlanSchema))
	}
	if plan.UID != o.UID || filepath.Clean(plan.Home) != filepath.Clean(o.Home) {
		return reject(fmt.Errorf("built for uid %d home %s, running as uid %d home %s", plan.UID, plan.Home, o.UID, o.Home))
	}
	if err := validateIdentity(plan.App); err != nil {
		return reject(err)
	}
	if underAny(plan.App.Path, o.SystemRoots) {
		return reject(fmt.Errorf("%s: %w", plan.App.Path, ErrSystemApp))
	}
	if exists(o.FS, plan.App.Path) {
		fresh, err := readIdentity(ctx, o, plan.App.Path)
		if err != nil {
			return reject(err)
		}
		if strings.EqualFold(fresh.BundleID, plan.App.BundleID) {
			if ao.Leftovers {
				return reject(fmt.Errorf("%s is still installed; --leftovers is only for an app whose bundle is gone", plan.App.Path))
			}
			if err := sameIdentity(plan.App, fresh); err != nil {
				return reject(err)
			}
			fresh.Receipts = plan.App.Receipts
			return fresh, false, nil
		}
		if !ao.Leftovers {
			return App{}, false, fmt.Errorf("%w: %s now has bundle id %s, not %s", ErrBundleMissing, plan.App.Path, fresh.BundleID, plan.App.BundleID)
		}
	} else if !ao.Leftovers {
		return App{}, false, fmt.Errorf("%w: %s does not exist", ErrBundleMissing, plan.App.Path)
	}
	return leftoversIdentity(ctx, o, plan.App)
}

// leftoversIdentity keeps only the bundle id of a plan whose bundle is gone.
// With no bundle there is no signature to check, so it refuses ids that
// belong to Apple, ids of fewer than three segments, ids an installed app
// has or sits under, and any id when Spotlight cannot be asked.
func leftoversIdentity(ctx context.Context, o Options, a App) (App, bool, error) {
	refuse := func(format string, args ...any) (App, bool, error) {
		return App{}, false, fmt.Errorf("%w: %s", ErrPlanRejected, fmt.Sprintf(format, args...))
	}
	if isAppleID(a.BundleID) {
		return refuse("--leftovers never acts on com.apple.* ids (%s)", a.BundleID)
	}
	if idSegments(a.BundleID) < 3 {
		return refuse("--leftovers needs a bundle id of at least three segments; %s may be a vendor prefix", a.BundleID)
	}
	others := &otherApps{o: o}
	if p := others.sameID(a.BundleID); p != "" {
		return refuse("%s belongs to the installed app %s", a.BundleID, p)
	}
	if !others.spotlight(ctx, a.BundleID, nil) {
		return refuse("Spotlight (mdfind) did not answer, so other installed copies of %s cannot be ruled out", a.BundleID)
	}
	if p := others.sameID(a.BundleID); p != "" {
		return refuse("%s belongs to the installed app %s", a.BundleID, p)
	}
	if id, p := others.under(a.BundleID); id != "" {
		return refuse("the installed app %s has the id %s, under %s", p, id, a.BundleID)
	}
	return App{Path: a.Path, BundleID: a.BundleID, Name: a.Name, HelperIDs: []string{}, DeclaredJobs: []string{}, AppGroups: []string{}, Receipts: []string{}}, true, nil
}

// checkPlanPaths runs the structural guard on the app path and every item,
// then rejects a plan that lists a path more than once under any kind or
// reason (ignoring case, as the volume does).
func checkPlanPaths(o Options, plan Plan) error {
	if err := checkItemPath(o, plan.App.Path, Item{Path: plan.App.Path, Kind: KindBundle}); err != nil {
		return fmt.Errorf("%w: %w", ErrPlanRejected, err)
	}
	seen := make(map[string]bool, len(plan.Items))
	for _, it := range plan.Items {
		if err := checkItemPath(o, plan.App.Path, it); err != nil {
			return fmt.Errorf("%w: %w", ErrPlanRejected, err)
		}
		if err := checkResolvedParent(o, it); err != nil && exists(o.FS, it.Path) {
			return fmt.Errorf("%w: %w", ErrPlanRejected, err)
		}
		key := strings.ToLower(it.Path)
		if seen[key] {
			return fmt.Errorf("%w: %s is listed more than once", ErrPlanRejected, it.Path)
		}
		seen[key] = true
	}
	return nil
}

// triage compares every planned item with its freshly derived state and
// returns the ones to move. It changes nothing.
func triage(o Options, ident App, plan Plan, freshItems []Item, ao ApplyOptions, leftovers bool, res *Result) ([]Item, error) {
	fresh := make(map[string]Item, len(freshItems))
	for _, it := range freshItems {
		fresh[it.Path] = it
	}
	if !leftovers {
		for _, it := range plan.Items {
			if _, ok := fresh[filepath.Clean(it.Path)]; !ok && exists(o.FS, it.Path) {
				return nil, fmt.Errorf("%w: %s is not something %s owns", ErrPlanRejected, it.Path, plan.App.BundleID)
			}
		}
	}
	var todo []Item
	commands := map[string]bool{}
	for _, planned := range plan.Items {
		cur, ok := fresh[filepath.Clean(planned.Path)]
		switch {
		case !ok && exists(o.FS, planned.Path):
			res.Skipped = append(res.Skipped, Skipped{Path: planned.Path, Kind: planned.Kind, Reason: "not an exact <location>/<bundle id> path; --leftovers acts on nothing else"})
			continue
		case !ok:
			res.Skipped = append(res.Skipped, Skipped{Path: planned.Path, Kind: planned.Kind, Reason: "gone"})
			continue
		}
		if why := drift(planned, cur); why != "" {
			res.Skipped = append(res.Skipped, Skipped{Path: cur.Path, Kind: cur.Kind, Reason: why})
			continue
		}
		switch actionFor(cur, ao.IncludeLowConfidence) {
		case ActionTrash:
			todo = append(todo, cur)
		case ActionNeedsRoot:
			cmd := rootCommand(o, ident, cur)
			if commands[cmd] {
				cmd = ""
			}
			commands[cmd] = true
			res.NeedsRoot = append(res.NeedsRoot, NeedsRoot{Path: cur.Path, Kind: cur.Kind, Command: cmd})
		default:
			res.Skipped = append(res.Skipped, Skipped{Path: cur.Path, Kind: cur.Kind, Reason: reportOnlyReason(cur)})
		}
	}
	return todo, nil
}

func reportOnlyReason(it Item) string {
	switch {
	case it.Kind == KindReceiptFile:
		return "installed by a receipt; never moved (check with " + it.Command + ")"
	case it.Shared:
		return "shared"
	}
	return "low confidence (pass --include-low-confidence)"
}

// drift explains why the current state no longer matches the plan, or "".
func drift(planned, cur Item) string {
	switch {
	case planned.Kind != cur.Kind || planned.Reason != cur.Reason:
		return fmt.Sprintf("match changed: planned %s/%s, now %s/%s", planned.Kind, planned.Reason, cur.Kind, cur.Reason)
	case planned.Shared && !cur.Shared:
		return "the plan reported it as shared"
	case planned.OwnerUID != cur.OwnerUID:
		return fmt.Sprintf("owner changed: planned uid %d, now %d", planned.OwnerUID, cur.OwnerUID)
	case planned.Symlink != cur.Symlink:
		return "file type changed"
	case !sameSizeClass(planned.SizeBytes, cur.SizeBytes):
		return fmt.Sprintf("size changed: planned %d bytes, now %d", planned.SizeBytes, cur.SizeBytes)
	}
	return ""
}

// sameSizeClass tolerates normal churn (within 1 MiB, or within a factor
// of two) and flags anything larger as drift.
func sameSizeClass(a, b uint64) bool {
	lo, hi := min(a, b), max(a, b)
	return hi-lo <= 1<<20 || hi-lo <= hi/2
}

// stopProcesses sends SIGTERM to each identity-checked process and waits
// for all of them to exit; it never escalates to SIGKILL.
func stopProcesses(ctx context.Context, o Options, bundle string, running []Process, dry bool, res *Result) error {
	if len(running) == 0 {
		return nil
	}
	if dry {
		res.Terminated = append(res.Terminated, running...)
		return nil
	}
	for _, p := range running {
		if err := terminate(ctx, o, bundle, p); err != nil {
			return err
		}
		res.Terminated = append(res.Terminated, p)
	}
	return waitGone(ctx, o, running)
}

func sameProcess(a, b Process) bool {
	return a.PID == b.PID && !a.StartTime.IsZero() && a.StartTime.Unix() == b.StartTime.Unix()
}

// terminate re-reads the process table and signals p only if the same
// process (pid and start time) still runs from the bundle.
func terminate(ctx context.Context, o Options, bundle string, p Process) error {
	if p.PID <= 1 || p.PID == o.SelfPID {
		return fmt.Errorf("refusing to signal pid %d", p.PID)
	}
	if p.StartTime.IsZero() {
		return fmt.Errorf("pid %d has no start time; not signalling an unverified process", p.PID)
	}
	for _, cur := range appProcesses(o.Procs(ctx), bundle) {
		if !sameProcess(cur, p) {
			continue
		}
		_, err := proc.Output(ctx, o.Runner, "kill", "-TERM", strconv.Itoa(p.PID))
		if err != nil {
			return fmt.Errorf("SIGTERM pid %d: %w", p.PID, err)
		}
		return nil
	}
	return nil // already gone, or the pid now belongs to another process
}

func waitGone(ctx context.Context, o Options, procs []Process) error {
	deadline := o.Now().Add(quitWait)
	for {
		left := 0
		for _, cur := range o.Procs(ctx) {
			c := Process{PID: cur.PID, StartTime: cur.StartTime}
			for _, p := range procs {
				if sameProcess(c, p) {
					left++
				}
			}
		}
		if left == 0 {
			return nil
		}
		if !o.Now().Before(deadline) || ctx.Err() != nil {
			return fmt.Errorf("%w: %d left after %s; no launch agent was booted out and nothing was moved", ErrQuitTimeout, left, quitWait)
		}
		o.Sleep(ctx, 250*time.Millisecond)
	}
}

// bootoutAgents unloads the user's own launch agents that are about to be
// moved, so launchd does not keep or respawn them. Only labels the app owns
// are booted out (App.bootsOut); a job that merely runs from the bundle
// under someone else's label has its plist moved but is not booted out. It
// returns the plists whose bootout failed for a reason other than the job
// not being loaded, keyed by path.
func bootoutAgents(ctx context.Context, o Options, ident App, todo []Item, dry bool, res *Result) map[string]string {
	failed := map[string]string{}
	userAgents := filepath.Join(o.UserLibrary, "LaunchAgents")
	for _, it := range todo {
		if it.Kind != KindLaunchAgent || filepath.Dir(it.Path) != userAgents || !ident.bootsOut(it.Detail) {
			continue
		}
		target := "gui/" + itoa(o.UID) + "/" + it.Detail
		b := Bootout{Target: target}
		if !dry {
			r, err := o.Runner.Run(ctx, proc.Cmd{Name: "launchctl", Args: []string{"bootout", target}})
			switch {
			case err != nil:
				b.Error = err.Error()
			case r.ExitCode != 0:
				b.Error = strings.TrimSpace(string(r.Stderr))
				if b.Error == "" {
					b.Error = "exit " + strconv.Itoa(r.ExitCode)
				}
			}
			if b.Error != "" && !notLoaded(b.Error) {
				b.Failed = true
				failed[it.Path] = b.Error
			}
		}
		res.Bootouts = append(res.Bootouts, b)
	}
	return failed
}

// notLoaded reports whether a bootout error only says the job was not
// loaded, which leaves nothing for launchd to keep or respawn.
func notLoaded(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not loaded") || strings.Contains(m, "no such process") ||
		strings.Contains(m, "could not find specified service")
}

// keepLoaded drops the plists whose bootout failed and records them as
// skipped: moving the plist of a job launchd still runs would leave it
// loaded with no file to boot it out by.
func keepLoaded(items []Item, failed map[string]string, mv *mover) []Item {
	if len(failed) == 0 {
		return items
	}
	var out []Item
	for _, it := range items {
		if why, ok := failed[it.Path]; ok {
			mv.res.Skipped = append(mv.res.Skipped, Skipped{Path: it.Path, Kind: it.Kind, Reason: "not moved: launchctl bootout failed: " + why})
			continue
		}
		out = append(out, it)
	}
	return out
}

// mover renames items into one new folder in the Trash. The folder is
// created with Mkdir, which fails if anything already has that name, so a
// file racing into the Trash is never overwritten. A rename moves a symlink
// as the link and never copies or deletes; a move across filesystems fails
// and the item is skipped.
type mover struct {
	o     Options
	dir   string
	dry   bool
	taken map[string]bool
	res   *Result
}

func newMover(o Options, ident App, ao ApplyOptions, res *Result) (*mover, error) {
	m := &mover{o: o, dry: ao.DryRun, taken: map[string]bool{}, res: res}
	dir, err := makeTrashDir(o, ident, ao.DryRun)
	if err != nil {
		return m, err
	}
	m.dir = dir
	res.TrashDir = dir
	return m, nil
}

// move renames one item and records the outcome.
func (m *mover) move(it Item) (string, bool) {
	dest := uniqueDest(m.o.FS, m.dir, filepath.Base(it.Path), m.taken)
	if !m.dry {
		if err := m.o.FS.Rename(it.Path, dest); err != nil {
			m.res.Skipped = append(m.res.Skipped, Skipped{Path: it.Path, Kind: it.Kind, Reason: "move failed: " + err.Error()})
			return "", false
		}
	}
	m.taken[dest] = true
	m.res.Moved = append(m.res.Moved, Moved{Path: it.Path, Kind: it.Kind, To: dest})
	return dest, true
}

func (m *mover) skipAll(items []Item, reason string) {
	for _, it := range items {
		m.res.Skipped = append(m.res.Skipped, Skipped{Path: it.Path, Kind: it.Kind, Reason: reason})
	}
}

// makeTrashDir creates "<bundle id> uninstalled <time>" in the Trash, adding
// " 2", " 3" and so on when the name exists. A dry run only picks the name.
func makeTrashDir(o Options, ident App, dry bool) (string, error) {
	base := ident.BundleID + " uninstalled " + o.Now().UTC().Format("2006-01-02 150405")
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name += " " + strconv.Itoa(i)
		}
		p, ok := idPath(o.Trash, name)
		if !ok {
			return "", fmt.Errorf("bad Trash folder name %q", name)
		}
		if dry {
			if !exists(o.FS, p) {
				return p, nil
			}
			continue
		}
		err := o.FS.Mkdir(p, 0o700)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create %s: %w", p, err)
		}
	}
	return "", errors.New("no free folder name in the Trash")
}

// uniqueDest picks a free name inside the folder Apply just created, so the
// only possible collisions are between items of this plan.
func uniqueDest(f FS, dir, name string, taken map[string]bool) string {
	ext := filepath.Ext(name)
	if ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := name
		if i > 1 {
			cand = stem + " " + strconv.Itoa(i) + ext
		}
		p := filepath.Join(dir, cand)
		if !taken[p] && !exists(f, p) {
			return p
		}
	}
}
