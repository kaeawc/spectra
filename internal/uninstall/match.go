package uninstall

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
)

// idLocation is a per-bundle-id location: dir/<id><suffix>.
type idLocation struct {
	dir    string
	suffix string
	kind   Kind
}

func userIDLocations(lib string) []idLocation {
	return []idLocation{
		{filepath.Join(lib, "Application Support"), "", KindSupport},
		{filepath.Join(lib, "Caches"), "", KindCache},
		{filepath.Join(lib, "Preferences"), ".plist", KindPreferences},
		{filepath.Join(lib, "Containers"), "", KindContainer},
		{filepath.Join(lib, "Saved Application State"), ".savedState", KindSavedState},
		{filepath.Join(lib, "HTTPStorages"), "", KindHTTPStorage},
		{filepath.Join(lib, "HTTPStorages"), ".binarycookies", KindHTTPStorage},
		{filepath.Join(lib, "WebKit"), "", KindWebKit},
		{filepath.Join(lib, "Logs"), "", KindLogs},
		{filepath.Join(lib, "Cookies"), ".binarycookies", KindCookies},
		{filepath.Join(lib, "Application Scripts"), "", KindOther},
	}
}

func systemIDLocations(lib string) []idLocation {
	return []idLocation{
		{filepath.Join(lib, "Application Support"), "", KindSupport},
		{filepath.Join(lib, "Caches"), "", KindCache},
		{filepath.Join(lib, "Preferences"), ".plist", KindPreferences},
		{filepath.Join(lib, "Logs"), "", KindLogs},
	}
}

// genericNames are display names too common to tie a folder to one app,
// even at low confidence: vendor folders and generic helper names.
var genericNames = map[string]bool{
	"google": true, "microsoft": true, "apple": true, "adobe": true, "jetbrains": true,
	"mozilla": true, "oracle": true, "utilities": true, "helper": true, "update": true,
	"updater": true, "installer": true, "launcher": true, "agent": true, "shared": true,
	"common": true, "support": true, "data": true,
}

// matcher accumulates items keyed by path; the first, strongest match wins.
type matcher struct {
	// refused lists items the structural guard dropped, with the reason.
	refused []string
	ctx     context.Context
	o       Options
	app     App
	items   map[string]*Item
	labels  []string
	others  *otherApps
}

func newMatcher(ctx context.Context, o Options, app App) *matcher {
	return &matcher{ctx: ctx, o: o, app: app, items: map[string]*Item{}, others: &otherApps{o: o, self: app.Path}}
}

func (m *matcher) add(path string, kind Kind, reason Reason, detail string) *Item {
	path = filepath.Clean(path)
	if it, ok := m.items[path]; ok {
		return it
	}
	if !exists(m.o.FS, path) {
		return nil
	}
	it := &Item{Path: path, Kind: kind, Reason: reason, Detail: detail, Confidence: ConfidenceHigh}
	m.items[path] = it
	return it
}

// addShared adds a path that is reported but never acted on. An existing
// item matched by a stronger rule is left as it is.
func (m *matcher) addShared(path string, kind Kind, reason Reason, detail, note string) {
	if _, ok := m.items[filepath.Clean(path)]; ok {
		return
	}
	if it := m.add(path, kind, reason, detail); it != nil {
		it.Shared, it.Note = true, note
	}
}

// matchIDAt adds <loc.dir>/<id><suffix> for each location.
func (m *matcher) matchIDAt(locs []idLocation, id string, reason Reason) {
	for _, l := range locs {
		if p, ok := idPath(l.dir, id+l.suffix); ok {
			m.add(p, l.kind, reason, id)
		}
	}
}

// matchIDs covers every location named exactly after the bundle id or a
// declared helper id.
func (m *matcher) matchIDs() {
	locs := append(userIDLocations(m.o.UserLibrary), systemIDLocations(m.o.SystemLibrary)...)
	m.matchIDAt(locs, m.app.BundleID, ReasonBundleID)
	m.matchByHost(m.app.BundleID, ReasonBundleID)
	for _, h := range m.app.HelperIDs {
		if !extendsID(h, m.app.BundleID) {
			continue
		}
		m.matchIDAt(locs, h, ReasonHelperOfBundle)
		m.matchByHost(h, ReasonHelperOfBundle)
	}
}

// matchByHost finds ByHost/<id>.<host-uuid>.plist. The middle part must be
// a host identifier so com.foo.app never claims com.foo.app.helper.<uuid>.
func (m *matcher) matchByHost(id string, reason Reason) {
	dir := filepath.Join(m.o.UserLibrary, "Preferences", "ByHost")
	entries, err := m.o.FS.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !hasFoldPrefix(e.Name(), id+".") {
			continue
		}
		host, ok := strings.CutSuffix(e.Name()[len(id)+1:], ".plist")
		if p, inDir := idPath(dir, e.Name()); ok && inDir && isHostID(host) {
			m.add(p, KindPreferences, reason, id)
		}
	}
}

func isHostID(s string) bool {
	if len(s) < 12 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') && (c < 'a' || c > 'f') && c != '-' {
			return false
		}
	}
	return true
}

// matchGroups covers application-group containers. A group the app's
// entitlements list is owned only when it is group.<bundle id>[.*] or
// <team id>.* for the app's verified team, and no other installed app lists
// it; any other listed group, and any unlisted group carrying the team id,
// is reported as shared.
func (m *matcher) matchGroups() {
	dir := filepath.Join(m.o.UserLibrary, "Group Containers")
	listed := map[string]bool{}
	for _, g := range m.app.AppGroups {
		listed[strings.ToLower(g)] = true
		p, ok := idPath(dir, g)
		if !ok {
			continue
		}
		reason := ReasonEntitlementGroup
		if m.app.TeamID != "" && hasFoldPrefix(g, m.app.TeamID+".") {
			reason = ReasonTeamID
		}
		it := m.add(p, KindGroupContainer, reason, g)
		switch {
		case it == nil:
		case !m.app.ownsGroup(g):
			it.Shared = true
			it.Note = "the entitlements list this group, but it is not group.<bundle id> or <verified team id>.*"
		case m.others.listsGroup(m.ctx, g):
			it.Shared = true
			it.Note = "another installed app lists this app group"
		}
	}
	if m.app.TeamID == "" {
		return
	}
	entries, err := m.o.FS.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if listed[strings.ToLower(e.Name())] || !hasFoldPrefix(e.Name(), m.app.TeamID+".") {
			continue
		}
		if p, ok := idPath(dir, e.Name()); ok {
			m.addShared(p, KindGroupContainer, ReasonTeamID, e.Name(), "same team id, but this app's entitlements do not list the group")
		}
	}
}

// matchNames reports Application Support, Caches and Logs folders named
// exactly after the app, at low confidence, when the name is not generic,
// no other installed app uses it and no id match already covers them.
func (m *matcher) matchNames() {
	for _, n := range m.app.DisplayNames {
		if genericNames[strings.ToLower(strings.TrimSpace(n))] || m.others.claimsName(n) {
			continue
		}
		for _, sub := range []struct {
			dir  string
			kind Kind
		}{{"Application Support", KindSupport}, {"Caches", KindCache}, {"Logs", KindLogs}} {
			p, ok := idPath(filepath.Join(m.o.UserLibrary, sub.dir), n)
			if !ok || !isDir(m.o.FS, p) {
				continue
			}
			if it := m.add(p, sub.kind, ReasonName, n); it != nil && it.Reason == ReasonName {
				it.Confidence = ConfidenceLow
				it.Note = "matched by folder name only"
			}
		}
	}
}

// idDerived reports whether an item was tied to the app only through an id
// (bundle, helper, label, group) or a name, as opposed to the bundle itself,
// a job that runs from it, or a receipt that lists it.
func idDerived(it *Item) bool {
	return it.Kind != KindBundle && it.Reason != ReasonLaunchdProgram && it.Reason != ReasonReceipt
}

const appleNote = "a com.apple.* id, and the bundle is not signed by Apple (anchor apple)"

// appleNoSpotlightNote marks com.apple.* items when Spotlight did not
// answer: a system app outside the scanned folders may use the same id.
const appleNoSpotlightNote = "a com.apple.* id, and Spotlight did not answer, so another copy of the Apple app cannot be ruled out"

// wholeAppSharedNote explains why none of the app's id-derived items may
// be acted on, or returns "".
func (m *matcher) wholeAppSharedNote() string {
	id := m.app.BundleID
	if dup := m.others.sameID(id); dup != "" {
		return "another installed app has the same bundle id (ignoring case): " + dup
	}
	if other, p := m.others.under(id); other != "" {
		return "another installed app's bundle id " + other + " is under this app's id: " + p
	}
	if isAppleID(id) && !m.app.AppleSigned {
		return appleNote
	}
	if isAppleID(id) && !m.others.spotlightOK {
		return appleNoSpotlightNote
	}
	if idSegments(id) < 3 && m.app.TeamID == "" {
		return "the bundle id has fewer than three segments and the app has no verified team id"
	}
	return ""
}

// itemSharedNote explains why one id-derived item may not be acted on: its
// id is Apple's, or is another installed app's bundle id.
func (m *matcher) itemSharedNote(it *Item) string {
	switch {
	case it.Detail == "":
		return ""
	case isAppleID(it.Detail) && !m.app.AppleSigned:
		return appleNote
	case strings.EqualFold(it.Detail, m.app.BundleID):
		return ""
	}
	if p := m.others.sameID(it.Detail); p != "" {
		return "another installed app has the bundle id " + it.Detail + ": " + p
	}
	return ""
}

// markShared reports id-derived items as shared when the ids they came from
// are not this app's alone. The bundle itself, jobs that run from it and
// receipts that list it are judged on their own evidence, except that any
// item but the bundle carrying a com.apple.* id is shared when Spotlight did
// not answer.
func (m *matcher) markShared() {
	ids := append(append([]string{}, m.app.HelperIDs...), m.app.DeclaredJobs...)
	m.others.spotlight(m.ctx, m.app.BundleID, ids)
	whole := m.wholeAppSharedNote()
	for _, it := range m.items {
		note := ""
		if idDerived(it) {
			note = whole
			if note == "" {
				note = m.itemSharedNote(it)
			}
		}
		if note == "" && it.Kind != KindBundle && !m.others.spotlightOK && isAppleID(it.Detail) {
			note = appleNoSpotlightNote
		}
		if note != "" && !it.Shared {
			it.Shared, it.Note = true, note
		}
	}
}

// collected is what collect found.
type collected struct {
	items  []Item
	labels []string
	// refused lists items dropped by the guard, as warnings.
	refused []string
	// spotlightOK is false when mdfind failed or timed out, so only the
	// app folders were searched for other copies.
	spotlightOK bool
}

// collect runs every matcher and returns the items in a stable order.
func collect(ctx context.Context, o Options, app App) collected {
	m := newMatcher(ctx, o, app)
	if app.BundlePresent || exists(o.FS, app.Path) {
		m.add(app.Path, KindBundle, ReasonBundleID, app.BundleID)
	}
	m.matchIDs()
	m.matchGroups()
	m.matchLaunchd()
	m.matchReceipts()
	m.matchNames()
	m.markShared()
	items := m.finish()
	return collected{items: items, labels: uniqueSorted(m.labels), refused: m.refused, spotlightOK: m.others.spotlightOK}
}

// collectLeftovers matches only <location>/<bundle id><suffix> for an app
// whose bundle is gone: no helpers, jobs, groups, receipts or names.
func collectLeftovers(ctx context.Context, o Options, app App) []Item {
	m := newMatcher(ctx, o, app)
	m.matchIDAt(append(userIDLocations(o.UserLibrary), systemIDLocations(o.SystemLibrary)...), app.BundleID, ReasonBundleID)
	return m.finish()
}

// finish applies the structural guard, fills each item's state and returns
// the items sorted by path.
func (m *matcher) finish() []Item {
	items := make([]Item, 0, len(m.items))
	for _, it := range m.items {
		if checkItemPath(m.o, m.app.Path, *it) != nil {
			continue
		}
		if err := checkResolvedParent(m.o, *it); err != nil {
			m.refused = append(m.refused, "not planned: "+err.Error())
			continue
		}
		finishItem(m.o, m.app, it)
		items = append(items, *it)
	}
	items = dropNested(items)
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	dedupeCommands(items)
	return items
}

// dedupeCommands keeps the first copy of a repeated root command, such as
// one `pkgutil --forget` for a receipt's .bom and .plist.
func dedupeCommands(items []Item) {
	seen := map[string]string{}
	for i := range items {
		c := items[i].Command
		if c == "" {
			continue
		}
		if first, ok := seen[c]; ok {
			items[i].Command = ""
			items[i].Note = "handled by the command shown for " + first
			continue
		}
		seen[c] = items[i].Path
	}
}

// dropNested removes items that live inside another planned item: moving
// the outer one already moves them.
func dropNested(items []Item) []Item {
	sort.Slice(items, func(i, j int) bool { return len(items[i].Path) < len(items[j].Path) })
	var out []Item
	for _, it := range items {
		nested := false
		for _, outer := range out {
			if it.Path != outer.Path && inside(it.Path, outer.Path) && !outer.Shared && outer.Confidence == ConfidenceHigh {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, it)
		}
	}
	return out
}

// finishItem fills ownership, size, removability, the default action and the
// command from the current state of the path.
func finishItem(o Options, app App, it *Item) {
	meta, err := o.FS.Meta(it.Path)
	if err != nil {
		return
	}
	it.OwnerUID = meta.UID
	it.Symlink = meta.IsSymlink()
	it.SizeBytes = o.FS.Size(it.Path)
	it.RemovableByUser = it.Kind != KindReceipt && it.Kind != KindReceiptFile && removable(o.FS, it.Path, meta, o.UID, o.GIDs)
	it.Action = actionFor(*it, false)
	switch {
	case it.Kind == KindReceiptFile:
		it.Command = "pkgutil --file-info " + shellQuote(it.Path)
		if it.Note == "" {
			it.Note = "installed by receipt " + it.Detail + "; check that no other tool replaced it, then remove it yourself"
		}
	case it.Action == ActionNeedsRoot:
		it.Command = rootCommand(o, app, *it)
	}
}

// actionFor decides what apply does with an item. A file a receipt
// installed is never moved: another installer (Homebrew, a newer pkg) may
// have replaced it with its own file or symlink since.
func actionFor(it Item, includeLow bool) Action {
	switch {
	case it.Kind == KindReceiptFile:
		return ActionReportOnly
	case it.Shared || (it.Confidence != ConfidenceHigh && !includeLow):
		return ActionReportOnly
	case it.RemovableByUser:
		return ActionTrash
	default:
		return ActionNeedsRoot
	}
}

// rootCommand is the command a user could run with sudo for an item Spectra
// will not touch. Paths are single-quoted for a POSIX shell. A bootout is
// suggested only for a label the app owns (see App.bootsOut).
func rootCommand(o Options, app App, it Item) string {
	mv := "sudo mv -n -- " + shellQuote(it.Path) + " " + shellQuote(o.Trash+"/")
	switch {
	case it.Kind == KindReceipt:
		return "sudo pkgutil --forget " + shellQuote(it.Detail)
	case it.Kind == KindLaunchDaemon && app.bootsOut(it.Detail):
		return "sudo launchctl bootout system/" + shellQuote(it.Detail) + " ; " + mv
	case it.Kind == KindLaunchAgent && app.bootsOut(it.Detail):
		return "launchctl bootout gui/" + itoa(o.UID) + "/" + shellQuote(it.Detail) + " ; " + mv
	default:
		return mv
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
