package uninstall

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/bundleid"
)

// maxIDLen bounds an id used as a path component; a file name is at most
// 255 bytes and a suffix such as ".savedState" is appended to some.
const maxIDLen = 200

// validID reports whether s is safe to use as one path component: a
// reverse-DNS id of at least two non-empty dot-separated segments, made only
// of the characters bundleid allows. It rejects ".", "..", "a/b", "com..x",
// ".x" and "x." so filepath.Join can never climb out of a location.
func validID(s string) bool {
	if len(s) > maxIDLen || !bundleid.Valid(s) {
		return false
	}
	segs := strings.Split(s, ".")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
	}
	return true
}

// hasFoldPrefix is strings.HasPrefix ignoring ASCII case: bundle ids name
// folders on a case-insensitive volume, so com.Foo.App and com.foo.app are
// the same folder.
func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// ownsID reports whether id is mainID itself or mainID followed by more dot
// segments, ignoring case.
func ownsID(mainID, id string) bool {
	return validID(id) && (strings.EqualFold(id, mainID) || extendsID(id, mainID))
}

// extendsID reports whether id is mainID followed by more dot segments.
func extendsID(id, mainID string) bool {
	return validID(id) && len(id) > len(mainID)+1 && hasFoldPrefix(id, mainID+".")
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// idPath joins an id-derived file name onto a location directory and
// returns it only when the result sits directly in that directory.
func idPath(loc, name string) (string, bool) {
	loc = filepath.Clean(loc)
	p := filepath.Join(loc, name)
	if filepath.Dir(p) != loc || filepath.Base(p) != name {
		return "", false
	}
	return p, true
}

// allowedParents maps each item kind to the directories its items must sit
// directly in. The bundle and receipt files are checked separately.
func allowedParents(o Options) map[Kind][]string {
	out := map[Kind][]string{}
	for _, l := range append(userIDLocations(o.UserLibrary), systemIDLocations(o.SystemLibrary)...) {
		out[l.kind] = append(out[l.kind], filepath.Clean(l.dir))
	}
	out[KindPreferences] = append(out[KindPreferences], filepath.Join(o.UserLibrary, "Preferences", "ByHost"))
	out[KindGroupContainer] = []string{filepath.Join(o.UserLibrary, "Group Containers")}
	out[KindLaunchAgent] = []string{filepath.Join(o.UserLibrary, "LaunchAgents"), filepath.Join(o.SystemLibrary, "LaunchAgents")}
	out[KindLaunchDaemon] = []string{filepath.Join(o.SystemLibrary, "LaunchDaemons")}
	out[KindPrivilegedHelper] = []string{filepath.Join(o.SystemLibrary, "PrivilegedHelperTools")}
	out[KindReceipt] = []string{filepath.Clean(o.Receipts)}
	return out
}

// protectedRoots are directories no item may be, or contain.
func protectedRoots(o Options) []string {
	roots := []string{"/", "/Applications", "/Library", "/System", "/Users", o.Home, o.UserLibrary, o.SystemLibrary, o.Trash, o.Receipts}
	roots = append(roots, o.AppRoots...)
	roots = append(roots, o.SystemAppRoots...)
	roots = append(roots, o.SystemRoots...)
	for _, dirs := range allowedParents(o) {
		roots = append(roots, dirs...)
	}
	return roots
}

// checkItemPath is the structural guard run at plan time and again at apply
// time: an item is never a protected location or an ancestor of one, the
// bundle item is exactly the app path, and every other id-derived item sits
// directly in a location directory for its kind.
func checkItemPath(o Options, appPath string, it Item) error {
	p := filepath.Clean(it.Path)
	if p != it.Path || !filepath.IsAbs(p) {
		return fmt.Errorf("item path %q is not a clean absolute path", it.Path)
	}
	for _, r := range protectedRoots(o) {
		if r != "" && inside(r, p) {
			return fmt.Errorf("item %s is, or contains, the protected location %s", p, filepath.Clean(r))
		}
	}
	switch it.Kind {
	case KindBundle:
		if p != filepath.Clean(appPath) {
			return fmt.Errorf("bundle item %s is not the app path %s", p, appPath)
		}
		return nil
	case KindReceiptFile:
		return nil // never moved; see actionFor
	}
	for _, dir := range allowedParents(o)[it.Kind] {
		if filepath.Dir(p) == dir {
			return nil
		}
	}
	return fmt.Errorf("%s item %s is not directly inside a %s location", it.Kind, p, it.Kind)
}
