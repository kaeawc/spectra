package uninstall

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// otherApps is the lazily loaded view of every other installed app (the app
// roots and the system app roots), used to mark names, ids and app groups
// that this app does not own alone. Comparisons ignore case, as the default
// APFS volume does.
type otherApps struct {
	o      Options
	self   string
	apps   []string
	names  map[string]bool
	groups map[string]bool
	ids    map[string]string
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

// sameID returns the path of another installed app whose bundle id equals
// id ignoring case, or "".
func (a *otherApps) sameID(id string) string {
	if a.ids == nil {
		a.ids = map[string]string{}
		for _, p := range a.list() {
			if bi, err := readBundleInfo(a.o.FS, p); err == nil && bi.id != "" {
				if _, ok := a.ids[strings.ToLower(bi.id)]; !ok {
					a.ids[strings.ToLower(bi.id)] = p
				}
			}
		}
	}
	return a.ids[strings.ToLower(id)]
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
