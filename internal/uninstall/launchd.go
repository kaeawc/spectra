package uninstall

import (
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/plistread"
)

// launchJob is the subset of a launchd plist used for matching.
type launchJob struct {
	label      string
	program    string
	associated []string
}

func readLaunchJob(f FS, path string) (launchJob, bool) {
	data, err := f.ReadFile(path)
	if err != nil {
		return launchJob{}, false
	}
	root, err := plistread.Decode(data)
	if err != nil || root.Kind != plistread.Dict {
		return launchJob{}, false
	}
	var j launchJob
	if v, ok := root.Dict["Label"]; ok && v.Kind == plistread.String {
		j.label = v.String
	}
	if v, ok := root.Dict["Program"]; ok && v.Kind == plistread.String {
		j.program = v.String
	} else if v, ok := root.Dict["ProgramArguments"]; ok && v.Kind == plistread.Array && len(v.Array) > 0 && v.Array[0].Kind == plistread.String {
		j.program = v.Array[0].String
	}
	if v, ok := root.Dict["AssociatedBundleIdentifiers"]; ok {
		j.associated = stringsOf(v)
	}
	return j, true
}

func stringsOf(v plistread.Value) []string {
	switch v.Kind {
	case plistread.String:
		return []string{v.String}
	case plistread.Array:
		var out []string
		for _, e := range v.Array {
			if e.Kind == plistread.String {
				out = append(out, e.String)
			}
		}
		return out
	}
	return nil
}

// jobMatch ties a launchd job to the app: its program runs from inside the
// bundle, its label is the bundle id or an id the app declares, or it names
// the app in AssociatedBundleIdentifiers. A shared reverse-DNS prefix is not
// enough. The job is the app's own only when its label is under the bundle
// id (or its program is in the bundle, which is evidence from the job's
// side); otherwise sharedNote says why it is reported only.
func (m *matcher) jobMatch(j launchJob) (reason Reason, sharedNote string, ok bool) {
	if j.program != "" && filepath.IsAbs(j.program) && inside(j.program, m.app.Path) && filepath.Clean(j.program) != filepath.Clean(m.app.Path) {
		return ReasonLaunchdProgram, "", true
	}
	own := ownsID(m.app.BundleID, j.label)
	switch {
	case strings.EqualFold(j.label, m.app.BundleID):
		return ReasonBundleID, "", true
	case containsFold(m.app.HelperIDs, j.label):
		return ReasonHelperOfBundle, "", true
	case containsFold(m.app.DeclaredJobs, j.label):
		if !own {
			return ReasonHelperOfBundle, "declared by the app, but the label is not under its bundle id", true
		}
		return ReasonHelperOfBundle, "", true
	}
	associated, allOwn := false, true
	for _, a := range j.associated {
		if ownsID(m.app.BundleID, a) {
			associated = true
		} else {
			allOwn = false
		}
	}
	switch {
	case !associated:
		return "", "", false
	case !own || !allOwn:
		return ReasonBundleID, "AssociatedBundleIdentifiers names this app, but the job's label or other entries are not under its bundle id", true
	}
	return ReasonBundleID, "", true
}

func (m *matcher) matchLaunchd() {
	dirs := []struct {
		path string
		kind Kind
	}{
		{filepath.Join(m.o.UserLibrary, "LaunchAgents"), KindLaunchAgent},
		{filepath.Join(m.o.SystemLibrary, "LaunchAgents"), KindLaunchAgent},
		{filepath.Join(m.o.SystemLibrary, "LaunchDaemons"), KindLaunchDaemon},
	}
	helpers := filepath.Join(m.o.SystemLibrary, "PrivilegedHelperTools")
	for _, d := range dirs {
		entries, err := m.o.FS.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".plist") {
				continue
			}
			m.matchJob(filepath.Join(d.path, e.Name()), d.kind, helpers)
		}
	}
	for _, label := range m.app.DeclaredJobs {
		p, ok := idPath(helpers, label)
		switch {
		case !ok:
		case ownsID(m.app.BundleID, label):
			m.add(p, KindPrivilegedHelper, ReasonHelperOfBundle, label)
		default:
			m.addShared(p, KindPrivilegedHelper, ReasonHelperOfBundle, label, "declared by the app, but the label is not under its bundle id")
		}
	}
}

func (m *matcher) matchJob(path string, kind Kind, helpers string) {
	meta, err := m.o.FS.Meta(path)
	if err != nil || meta.IsSymlink() {
		return
	}
	j, ok := readLaunchJob(m.o.FS, path)
	if !ok || j.label == "" {
		return
	}
	reason, note, ok := m.jobMatch(j)
	if !ok {
		return
	}
	add := func(p string, k Kind) *Item {
		if note != "" {
			m.addShared(p, k, reason, j.label, note)
			return m.items[filepath.Clean(p)]
		}
		return m.add(p, k, reason, j.label)
	}
	if add(path, kind) == nil {
		return
	}
	m.labels = append(m.labels, j.label)
	if j.program != "" && filepath.IsAbs(j.program) && filepath.Dir(filepath.Clean(j.program)) == helpers {
		add(filepath.Clean(j.program), KindPrivilegedHelper)
	}
}
