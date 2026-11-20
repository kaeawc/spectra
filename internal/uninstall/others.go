package uninstall

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

// spotlightWait bounds the mdfind lookup for other copies of the app.
const spotlightWait = 5 * time.Second

// otherApps is the lazily loaded view of every other installed app (the app
// roots and the system app roots, plus Spotlight hits once asked), used to
// mark names, ids and app groups that this app does not own alone.
// Comparisons ignore case, as the default APFS volume does.
type otherApps struct {
	o      Options
	self   string
	apps   []string
	names  map[string]bool
	groups map[string]bool
	ids    map[string]string
	// spotlightRan and spotlightOK record whether mdfind was asked and
	// answered.
	spotlightRan, spotlightOK bool
}

func (a *otherApps) list() []string {
	if a.apps == nil {
		a.apps = []string{}
		roots := append(append([]string{}, a.o.AppRoots...), a.o.SystemAppRoots...)
		for _, p := range installedApps(a.o.FS, roots) {
			if !strings.EqualFold(filepath.Clean(p), filepath.Clean(a.self)) {
				a.apps = append(a.apps, p)
			}
		}
	}
	return a.apps
}

// claimsName reports whether another installed app uses name as its bundle
// name, display name or file name. The comparison is whole-name, so "Code"
// is claimed by a Visual Studio Code whose CFBundleName is "Code" but never
// by "Xcode".
func (a *otherApps) claimsName(name string) bool {
	if a.names == nil {
		a.names = map[string]bool{}
		for _, p := range a.list() {
			a.names[strings.ToLower(strings.TrimSuffix(filepath.Base(p), ".app"))] = true
			if bi, err := readBundleInfo(a.o.FS, p); err == nil {
				a.names[strings.ToLower(bi.name)] = true
				a.names[strings.ToLower(bi.displayName)] = true
			}
		}
	}
	return a.names[strings.ToLower(name)]
}

func (a *otherApps) idMap() map[string]string {
	if a.ids == nil {
		a.ids = map[string]string{}
		for _, p := range a.list() {
			a.addBundle(p)
		}
	}
	return a.ids
}

// addBundle records the bundle id of the bundle at p, and reports whether
// it could read one.
func (a *otherApps) addBundle(p string) bool {
	bi, err := readBundleInfo(a.o.FS, p)
	if err != nil || bi.id == "" {
		return false
	}
	if _, ok := a.ids[strings.ToLower(bi.id)]; !ok {
		a.ids[strings.ToLower(bi.id)] = p
	}
	return true
}

// sameID returns the path of another installed app whose bundle id equals
// id ignoring case, or "".
func (a *otherApps) sameID(id string) string {
	return a.idMap()[strings.ToLower(id)]
}

// under returns the id and path of another installed app whose bundle id
// has mainID as a dotted prefix (com.google.Chrome for com.google), or "".
func (a *otherApps) under(mainID string) (string, string) {
	var ids []string
	for id := range a.idMap() {
		if extendsID(id, mainID) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", ""
	}
	sort.Strings(ids)
	return ids[0], a.ids[ids[0]]
}

// spotlight asks mdfind, once and read-only, for bundles whose id is mainID,
// is under mainID, or is one of ids, and records every hit outside the
// planned bundle and the Trash as installed. It reports whether Spotlight
// answered; on failure nothing is added.
func (a *otherApps) spotlight(ctx context.Context, mainID string, ids []string) bool {
	if a.spotlightRan {
		return a.spotlightOK
	}
	a.spotlightRan = true
	a.idMap()
	terms := []string{
		fmt.Sprintf("kMDItemCFBundleIdentifier == '%s'cd", mainID),
		fmt.Sprintf("kMDItemCFBundleIdentifier == '%s.*'cd", mainID),
	}
	for _, id := range ids {
		if validID(id) && !ownsID(mainID, id) {
			terms = append(terms, fmt.Sprintf("kMDItemCFBundleIdentifier == '%s'cd", id))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, spotlightWait)
	defer cancel()
	res, err := a.o.Runner.Run(ctx, proc.Cmd{Name: "mdfind", Args: []string{strings.Join(uniqueSorted(terms), " || ")}})
	if err != nil || res.ExitCode != 0 || ctx.Err() != nil {
		return false
	}
	a.spotlightOK = true
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		p := strings.TrimSpace(line)
		if !filepath.IsAbs(p) || (a.self != "" && inside(p, a.self)) || inside(p, a.o.Trash) {
			continue
		}
		p = filepath.Clean(p)
		if !a.addBundle(p) {
			// A hit whose Info.plist cannot be read matched one of the
			// ids; count it against the main id.
			if _, ok := a.ids[strings.ToLower(mainID)]; !ok {
				a.ids[strings.ToLower(mainID)] = p
			}
		}
	}
	return true
}

// listsGroup reports whether another installed app's entitlements list g.
func (a *otherApps) listsGroup(ctx context.Context, g string) bool {
	if a.groups == nil {
		a.groups = map[string]bool{}
		for _, p := range a.list() {
			for _, other := range readAppGroups(ctx, a.o.Runner, p) {
				a.groups[strings.ToLower(other)] = true
			}
		}
	}
	return a.groups[strings.ToLower(g)]
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
