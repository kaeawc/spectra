package startup

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/plistread"
)

// installedApp is one .app found under the app directories.
type installedApp struct {
	name, path, bundleID, mainExe string
}

// attributeOwners fills Item.Owner for rows BTM did not already attribute:
// first the .app containing the program, then an app named by the plist's
// AssociatedBundleIdentifiers, then an app whose bundle id equals or
// prefixes the label. Team ids come from classify's codesign reading.
func (e env) attributeOwners(ctx context.Context, m *merger) SourceStatus {
	cl := classify.New(classify.Options{OS: e.opts.OS, Runner: e.runner, FS: e.files})
	rows := m.rows
	if e.ownersFor != "" {
		rows = nil
		for _, r := range m.rows {
			if r.Label == e.ownersFor {
				rows = append(rows, r)
			}
		}
	}
	err := ownersFromPrograms(ctx, cl, rows)
	if err2 := e.ownersFromApps(ctx, cl, rows); err == nil {
		err = err2
	}
	for _, r := range rows {
		fillFromBTM(r)
		if r.Owner != nil {
			addSource(r, SourceOwner)
		}
	}
	if err != nil {
		return SourceStatus{Name: SourceOwner, Status: StatusError, Detail: err.Error()}
	}
	return SourceStatus{Name: SourceOwner, Status: StatusOK}
}

// ownersFromPrograms attributes rows whose program sits inside a .app.
func ownersFromPrograms(ctx context.Context, cl *classify.Classifier, rows []*Item) error {
	var pending []*Item
	var inputs []classify.Input
	for _, r := range rows {
		// Only programs inside a bundle can be owned by one; skipping the
		// rest avoids reading and codesigning every helper binary.
		if r.Owner == nil && filepath.IsAbs(r.Program) && strings.Contains(r.Program, ".app/") {
			pending = append(pending, r)
			inputs = append(inputs, classify.Input{ExecutablePath: r.Program})
		}
	}
	results, err := cl.Classify(ctx, inputs)
	for i, res := range results {
		if res.App != nil {
			pending[i].Owner = &Owner{App: res.App.Name, BundlePath: res.App.BundlePath,
				BundleID: res.App.BundleID, TeamID: teamID(res), Via: "program_in_bundle"}
		}
	}
	return err
}

// ownersFromApps attributes the remaining rows by bundle id, reading the
// installed apps only when some row still needs an owner.
func (e env) ownersFromApps(ctx context.Context, cl *classify.Classifier, rows []*Item) error {
	var apps []installedApp
	loaded := false
	var matched []*Item
	var exes []classify.Input
	for _, r := range rows {
		if r.Owner != nil {
			continue
		}
		if !loaded {
			apps, loaded = e.installedApps(), true
		}
		app, via := matchApp(apps, r)
		if app == nil {
			continue
		}
		r.Owner = &Owner{App: app.name, BundlePath: app.path, BundleID: app.bundleID, Via: via}
		if app.mainExe != "" {
			matched = append(matched, r)
			exes = append(exes, classify.Input{ExecutablePath: app.mainExe})
		}
	}
	results, err := cl.Classify(ctx, exes)
	for i, res := range results {
		matched[i].Owner.TeamID = teamID(res)
	}
	return err
}

func teamID(res classify.Result) string {
	if res.Signing == nil {
		return ""
	}
	return res.Signing.TeamID
}

// matchApp picks the app an item belongs to by bundle id. An exact
// AssociatedBundleIdentifiers match wins; otherwise the longest bundle id
// that equals the label or is a dotted prefix of it.
func matchApp(apps []installedApp, r *Item) (*installedApp, string) {
	for _, id := range r.assoc {
		for i := range apps {
			if apps[i].bundleID == id {
				return &apps[i], "associated_bundle_identifier"
			}
		}
	}
	var best *installedApp
	for i := range apps {
		id := apps[i].bundleID
		if id == "" || (r.Label != id && !strings.HasPrefix(r.Label, id+".")) {
			continue
		}
		if best == nil || len(id) > len(best.bundleID) {
			best = &apps[i]
		}
	}
	if best == nil {
		return nil, ""
	}
	return best, "bundle_id_prefix"
}

func (e env) appDirs() []string {
	if e.opts.AppDirs != nil {
		return e.opts.AppDirs
	}
	dirs := []string{"/Applications", "/Applications/Utilities"}
	if e.home != "" {
		dirs = append(dirs, filepath.Join(e.home, "Applications"))
	}
	return dirs
}

// installedApps lists the .app bundles directly in each app directory and
// one folder level down (/Applications/Setapp/...).
func (e env) installedApps() []installedApp {
	var apps []installedApp
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := e.files.ReadDir(dir)
		if err != nil {
			return
		}
		for _, ent := range entries {
			path := filepath.Join(dir, ent.Name())
			switch {
			case strings.HasSuffix(ent.Name(), ".app"):
				apps = append(apps, e.readApp(path))
			case ent.IsDir() && depth == 0:
				walk(path, 1)
			}
		}
	}
	for _, d := range e.appDirs() {
		walk(d, 0)
	}
	return apps
}

func (e env) readApp(path string) installedApp {
	app := installedApp{name: strings.TrimSuffix(filepath.Base(path), ".app"), path: path}
	data, err := e.files.ReadFile(filepath.Join(path, "Contents", "Info.plist"), maxPlist)
	if err != nil {
		return app
	}
	v, err := plistread.Decode(data)
	if err != nil {
		return app
	}
	if id, ok := v.Lookup("CFBundleIdentifier"); ok && id.Kind == plistread.String {
		app.bundleID = id.String
	}
	if exe, ok := v.Lookup("CFBundleExecutable"); ok && exe.Kind == plistread.String && exe.String != "" && !strings.Contains(exe.String, "/") {
		app.mainExe = filepath.Join(path, "Contents", "MacOS", exe.String)
	}
	return app
}
