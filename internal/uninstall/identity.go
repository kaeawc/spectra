package uninstall

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/plistread"
	"github.com/kaeawc/spectra/internal/proc"
)

// ErrSystemApp is returned for apps on the sealed system volume.
var ErrSystemApp = errors.New("system app on the sealed system volume; it cannot be uninstalled")

// ErrAppNotFound is returned when a bundle id resolves to no installed app.
var ErrAppNotFound = errors.New("no installed app with that bundle id")

// bundleInfo is the subset of Info.plist the planner uses.
type bundleInfo struct {
	id, name, displayName, version string
	privileged                     []string
}

func readBundleInfo(f FS, bundle string) (bundleInfo, error) {
	data, err := f.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return bundleInfo{}, fmt.Errorf("read Info.plist: %w", err)
	}
	root, err := plistread.Decode(data)
	if err != nil {
		return bundleInfo{}, fmt.Errorf("decode Info.plist: %w", err)
	}
	str := func(k string) string {
		if v, ok := root.Lookup(k); ok && v.Kind == plistread.String {
			return v.String
		}
		return ""
	}
	bi := bundleInfo{
		id: str("CFBundleIdentifier"), name: str("CFBundleName"),
		displayName: str("CFBundleDisplayName"), version: str("CFBundleShortVersionString"),
	}
	if v, ok := root.Dict["SMPrivilegedExecutables"]; ok && v.Kind == plistread.Dict {
		for k := range v.Dict {
			bi.privileged = append(bi.privileged, k)
		}
	}
	return bi, nil
}

// resolveApp turns a .app path or a bundle id into a bundle path.
func resolveApp(o Options, target string) (string, error) {
	asPath := func() (string, error) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", target, err)
		}
		return abs, nil
	}
	if strings.Contains(target, "/") {
		return asPath()
	}
	// A bundle id may itself end in ".app" (com.foo.app), so an id lookup
	// comes first and a bare "Foo.app" falls back to a relative path.
	if validID(target) {
		for _, app := range installedApps(o.FS, o.AppRoots) {
			if bi, err := readBundleInfo(o.FS, app); err == nil && strings.EqualFold(bi.id, target) {
				return app, nil
			}
		}
	}
	if strings.HasSuffix(target, ".app") && isDir(o.FS, target) {
		return asPath()
	}
	return "", fmt.Errorf("%s: %w", target, ErrAppNotFound)
}

// installedApps lists the .app bundles under each root: directories and
// top-level symbolic links directly in the root (Safari in /Applications is
// a link into the system volume), and bundle directories one folder below
// it (/Applications/<Vendor>/Foo.app). It only lists; Spotlight is asked
// separately (see otherApps.spotlight).
func installedApps(f FS, roots []string) []string {
	var apps []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			apps = append(apps, p)
		}
	}
	for _, root := range roots {
		entries, err := f.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(root, e.Name())
			meta, err := f.Meta(p)
			switch {
			case err != nil:
			case strings.HasSuffix(e.Name(), ".app"):
				if meta.Mode.IsDir() || meta.IsSymlink() {
					add(p)
				}
			case meta.Mode.IsDir():
				for _, sub := range appDirsIn(f, p) {
					add(sub)
				}
			}
		}
	}
	return apps
}

// appDirsIn lists the .app directories (not symlinks) directly in dir.
func appDirsIn(f FS, dir string) []string {
	entries, err := f.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasSuffix(e.Name(), ".app") && isDir(f, p) {
			out = append(out, p)
		}
	}
	return out
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if inside(path, r) {
			return true
		}
	}
	return false
}

// inside reports whether path is root or below it, lexically.
func inside(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// insideFold is inside ignoring case, as the default APFS volume compares
// names.
func insideFold(path, root string) bool {
	return inside(strings.ToLower(path), strings.ToLower(root))
}

// readIdentity builds the app identity from the bundle on disk.
func readIdentity(ctx context.Context, o Options, bundle string) (App, error) {
	if underAny(bundle, o.SystemRoots) {
		return App{}, fmt.Errorf("%s: %w", bundle, ErrSystemApp)
	}
	if meta, err := o.FS.Meta(bundle); err == nil && meta.IsSymlink() {
		return App{}, symlinkedBundleError(o, bundle)
	}
	if !isDir(o.FS, bundle) {
		return App{}, fmt.Errorf("%s: not an app bundle directory", bundle)
	}
	bi, err := readBundleInfo(o.FS, bundle)
	if err != nil {
		return App{}, fmt.Errorf("%s: %w", bundle, err)
	}
	if !validID(bi.id) {
		return App{}, fmt.Errorf("%s: bundle id %q is not a usable reverse-DNS id", bundle, bi.id)
	}
	app := App{
		Path: bundle, BundleID: bi.id, Name: strings.TrimSuffix(filepath.Base(bundle), ".app"),
		Version: bi.version, BundlePresent: true,
	}
	app.DisplayNames = uniqueSorted(nonEmpty(app.Name, bi.name, bi.displayName))
	app.HelperIDs = helperIDs(o.FS, bundle, bi.id)
	app.DeclaredJobs = declaredJobs(o.FS, bundle, bi)
	app.TeamID = readTeamID(ctx, o.Runner, bundle)
	app.AppGroups = readAppGroups(ctx, o.Runner, bundle)
	if app.usesAppleID() {
		app.AppleSigned = runOK(ctx, o.Runner, "codesign", "--verify", "--strict", "-R=anchor apple", bundle)
	}
	return app, nil
}

// symlinkedBundleError explains a .app path that is a symbolic link, such as
// /Applications/Safari.app pointing into the system volume.
func symlinkedBundleError(o Options, bundle string) error {
	target, err := o.FS.Readlink(bundle)
	if err != nil {
		return fmt.Errorf("%s is a symbolic link, not an app bundle; plan the bundle it points to", bundle)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(bundle), target)
	}
	target = filepath.Clean(target)
	if underAny(target, o.SystemRoots) {
		return fmt.Errorf("%s is a symbolic link to %s: %w", bundle, target, ErrSystemApp)
	}
	return fmt.Errorf("%s is a symbolic link to %s, not an app bundle; plan %s itself", bundle, target, target)
}

// helperIDs are bundle ids of sub-bundles that extend the main id
// (id + "."), e.g. com.tinyspeck.slackmacgap.helper. Sub-bundles with an
// unrelated id are ignored: their caches may belong to a shared framework.
func helperIDs(f FS, bundle, mainID string) []string {
	dirs := []string{
		filepath.Join(bundle, "Contents", "Frameworks"),
		filepath.Join(bundle, "Contents", "Library", "LoginItems"),
		filepath.Join(bundle, "Contents", "XPCServices"),
		filepath.Join(bundle, "Contents", "PlugIns"),
		filepath.Join(bundle, "Contents", "Library", "SystemExtensions"),
	}
	var ids []string
	for _, d := range dirs {
		entries, err := f.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !isSubBundle(e.Name()) {
				continue
			}
			bi, err := readBundleInfo(f, filepath.Join(d, e.Name()))
			if err == nil && extendsID(bi.id, mainID) {
				ids = append(ids, bi.id)
			}
		}
	}
	return uniqueSorted(ids)
}

func isSubBundle(name string) bool {
	switch filepath.Ext(name) {
	case ".app", ".xpc", ".appex", ".systemextension":
		return true
	}
	return false
}

// declaredJobs are launchd labels the app declares it installs:
// SMPrivilegedExecutables keys and tools in Contents/Library/LaunchServices.
// Any app can declare any label, so the matcher trusts only the ones under
// the app's own bundle id and reports the rest as shared.
func declaredJobs(f FS, bundle string, bi bundleInfo) []string {
	var labels []string
	for _, k := range bi.privileged {
		if validID(k) {
			labels = append(labels, k)
		}
	}
	if entries, err := f.ReadDir(filepath.Join(bundle, "Contents", "Library", "LaunchServices")); err == nil {
		for _, e := range entries {
			if validID(e.Name()) {
				labels = append(labels, e.Name())
			}
		}
	}
	return uniqueSorted(labels)
}

// readTeamID returns the bundle's verified team id, or "" when the
// signature does not verify, codesign fails, the signature is ad hoc, or no
// team is set. `codesign -dv` alone only reads what the signature claims;
// `--verify --strict` (not deep, which is codesign's default) checks it.
func readTeamID(ctx context.Context, r proc.Runner, bundle string) string {
	if !runOK(ctx, r, "codesign", "--verify", "--strict", bundle) {
		return ""
	}
	res, err := r.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"-dv", bundle}})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	out := string(res.Stderr) + "\n" + string(res.Stdout)
	team, _ := detect.ParseSigningOutput(out)
	if strings.Contains(out, "Signature=adhoc") || !validTeamID(team) {
		return ""
	}
	return team
}

// runOK reports whether the command ran and exited 0.
func runOK(ctx context.Context, r proc.Runner, name string, args ...string) bool {
	res, err := r.Run(ctx, proc.Cmd{Name: name, Args: args})
	return err == nil && res.ExitCode == 0
}

// isAppleID reports whether id is in Apple's namespace: com.apple,
// com.apple.*, or the app group group.com.apple.*.
func isAppleID(id string) bool {
	return strings.EqualFold(id, "com.apple") || hasFoldPrefix(id, "com.apple.") || hasFoldPrefix(id, "group.com.apple.")
}

// usesAppleID reports whether the main id, a helper id, a declared label or
// an app group is in Apple's namespace.
func (a App) usesAppleID() bool {
	ids := append(append(append([]string{a.BundleID}, a.HelperIDs...), a.DeclaredJobs...), a.AppGroups...)
	for _, id := range ids {
		if isAppleID(id) {
			return true
		}
	}
	return false
}

// bootsOut reports whether apply may run `launchctl bootout` for label: only
// the app's own label (its bundle id or under it), never a com.apple.* one.
// A job that merely runs from the bundle is not booted out by a label that
// belongs to someone else.
func (a App) bootsOut(label string) bool {
	return ownsID(a.BundleID, label) && !isAppleID(label)
}

// idSegments counts the dot-separated segments of id.
func idSegments(id string) int { return strings.Count(id, ".") + 1 }

func validTeamID(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// readAppGroups returns the application-group ids in the bundle's
// entitlements.
func readAppGroups(ctx context.Context, r proc.Runner, bundle string) []string {
	out, err := proc.Output(ctx, r, "codesign", "-d", "--entitlements", ":-", bundle)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return []string{}
	}
	root, err := plistread.Decode(out)
	if err != nil {
		return []string{}
	}
	v, ok := root.Dict["com.apple.security.application-groups"]
	if !ok || v.Kind != plistread.Array {
		return []string{}
	}
	var groups []string
	for _, g := range v.Array {
		if g.Kind == plistread.String && validID(g.String) {
			groups = append(groups, g.String)
		}
	}
	return uniqueSorted(groups)
}

// ownsGroup reports whether the app may claim group g as its own: it is
// group.<bundle id>[.*], or <team id>.* for the app's verified team id.
func (a App) ownsGroup(g string) bool {
	if ownsID("group."+a.BundleID, g) {
		return true
	}
	return a.TeamID != "" && validTeamID(a.TeamID) && validID(g) && hasFoldPrefix(g, a.TeamID+".")
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

func uniqueSorted(vals []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// validateIdentity checks a plan's recorded identity against the rules the
// planner itself applies, so an edited plan cannot widen what is matched.
func validateIdentity(a App) error {
	if !validID(a.BundleID) {
		return fmt.Errorf("plan bundle id %q is invalid", a.BundleID)
	}
	if !strings.HasSuffix(a.Path, ".app") || !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path {
		return fmt.Errorf("plan app path %q is not a clean absolute .app path", a.Path)
	}
	for _, h := range a.HelperIDs {
		if !extendsID(h, a.BundleID) {
			return fmt.Errorf("plan helper id %q does not extend %s", h, a.BundleID)
		}
	}
	for _, l := range a.DeclaredJobs {
		if !validID(l) {
			return fmt.Errorf("plan launchd label %q is invalid", l)
		}
	}
	if a.TeamID != "" && !validTeamID(a.TeamID) {
		return fmt.Errorf("plan team id %q is invalid", a.TeamID)
	}
	for _, g := range a.AppGroups {
		if !validID(g) {
			return fmt.Errorf("plan app group %q is invalid", g)
		}
	}
	for _, r := range a.Receipts {
		if !validID(r) {
			return fmt.Errorf("plan receipt %q is invalid", r)
		}
	}
	return nil
}

// sameIdentity reports whether the plan was built from the bundle now on disk.
func sameIdentity(plan, fresh App) error {
	switch {
	case plan.BundleID != fresh.BundleID:
		return fmt.Errorf("bundle id changed: plan %s, bundle %s", plan.BundleID, fresh.BundleID)
	case plan.TeamID != fresh.TeamID:
		return fmt.Errorf("team id changed: plan %q, bundle %q", plan.TeamID, fresh.TeamID)
	case plan.AppleSigned != fresh.AppleSigned:
		return fmt.Errorf("apple signature changed: plan %v, bundle %v", plan.AppleSigned, fresh.AppleSigned)
	case strings.Join(plan.HelperIDs, ",") != strings.Join(fresh.HelperIDs, ","):
		return errors.New("helper bundle ids differ from the bundle on disk")
	case strings.Join(plan.DeclaredJobs, ",") != strings.Join(fresh.DeclaredJobs, ","):
		return errors.New("declared launchd labels differ from the bundle on disk")
	case strings.Join(plan.AppGroups, ",") != strings.Join(fresh.AppGroups, ","):
		return errors.New("app groups differ from the bundle on disk")
	}
	return nil
}
