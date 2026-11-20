package startup

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// btmItem is one item from `sfltool dumpbtm`, before merging.
type btmItem struct {
	BTM
	Name string
}

var (
	btmRecordsRE = regexp.MustCompile(`Records for UID (-?\d+)`)
	btmItemRE    = regexp.MustCompile(`^#\d+:$`)
	btmHexRE     = regexp.MustCompile(`\s*\(0x[0-9a-fA-F]+\)$`)
	btmTypeIDRE  = regexp.MustCompile(`^\d+\.`)
)

// parseDumpBTM reads `sfltool dumpbtm`. The format is undocumented, so the
// parser keys only on the record header, `#N:` item markers and `Key: value`
// lines, ignores everything it does not recognise, and drops "developer"
// items, which are only group headers in System Settings.
func parseDumpBTM(out []byte) []btmItem {
	var items []btmItem
	uid := 0
	var cur *btmItem
	flush := func() {
		if cur != nil && cur.Identifier != "" && cur.Type != "developer" {
			items = append(items, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if m := btmRecordsRE.FindStringSubmatch(trimmed); m != nil {
			flush()
			uid, _ = strconv.Atoi(m[1])
			continue
		}
		if btmItemRE.MatchString(trimmed) {
			flush()
			cur = &btmItem{BTM: BTM{UID: uid}}
			continue
		}
		if cur == nil {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		applyBTMField(cur, key, btmValue(value))
	}
	flush()
	return items
}

func btmValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "(null)" {
		return ""
	}
	return v
}

func applyBTMField(it *btmItem, key, value string) {
	switch key {
	case "Name":
		it.Name = value
	case "Type":
		it.Type = btmHexRE.ReplaceAllString(value, "")
	case "Disposition":
		it.Disposition = btmList(value)
	case "Identifier":
		it.Identifier = value
	case "URL":
		it.URL = value
	case "Executable Path":
		it.ExecutablePath = value
	case "Bundle Identifier":
		it.BundleID = value
	case "Team Identifier":
		it.TeamID = value
	case "Developer Name":
		it.DeveloperName = value
	case "Parent Identifier":
		it.ParentIdentifier = value
	case "Assoc. Bundle IDs":
		it.AssocBundleIDs = btmList(value)
	}
}

// btmList turns "[enabled, allowed, visible] (0xb)" into its words.
func btmList(v string) []string {
	v = strings.Trim(btmHexRE.ReplaceAllString(v, ""), "[] ")
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// label is the launchd label or bundle id BTM tracks, without the numeric
// type prefix it adds ("16.com.example.daemon" -> "com.example.daemon").
func (it btmItem) label() string {
	return btmTypeIDRE.ReplaceAllString(it.Identifier, "")
}

// plistPath is the launchd plist BTM points at for legacy agents and
// daemons, or "" when the URL is not a .plist file URL.
func (it btmItem) plistPath() string {
	return fileURLPath(it.URL, ".plist")
}

func fileURLPath(raw, suffix string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), suffix) {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

// startsAtLogin reports whether the item is something that launches: a
// launchd job, a login item, or an app registered to open at login. App
// items that are disabled are only parents of their embedded items, and
// Quick Look, Spotlight and dock tile plug-ins never launch at login.
func (it btmItem) startsAtLogin() bool {
	switch {
	case strings.Contains(it.Type, "agent"), strings.Contains(it.Type, "daemon"), strings.Contains(it.Type, "login item"):
		return true
	case it.Type == "app":
		return !it.disabled()
	}
	return false
}

// resolve makes the item's URL and executable path absolute. Items embedded
// in an app record them relative to the parent bundle
// ("Contents/Library/LoginItems/Helper.app").
func (it *btmItem) resolve(byID map[string]btmItem) {
	parent := ""
	if p, ok := byID[it.ParentIdentifier]; ok {
		parent = fileURLPath(p.URL, ".app")
	}
	if it.URL != "" && !strings.Contains(it.URL, "://") {
		it.URL = joinBundle(parent, it.URL, true)
	}
	if it.ExecutablePath != "" && !filepath.IsAbs(it.ExecutablePath) {
		it.ExecutablePath = joinBundle(parent, it.ExecutablePath, false)
	}
}

// joinBundle resolves rel against the parent bundle, as a file URL when
// asURL is set; "" when there is no parent to resolve against.
func joinBundle(parent, rel string, asURL bool) string {
	if parent == "" {
		return ""
	}
	if !asURL {
		return filepath.Join(parent, rel)
	}
	u, err := url.Parse(rel)
	if err != nil {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: filepath.Join(parent, u.Path)}).String()
}

func (it btmItem) disabled() bool {
	for _, d := range it.Disposition {
		if d == "disabled" {
			return true
		}
	}
	return false
}

// scope is system for daemons BTM records; everything else in BTM runs in
// a user's session.
func (it btmItem) scope() Scope {
	if strings.Contains(it.Type, "daemon") || it.UID <= 0 {
		return ScopeSystem
	}
	return ScopeUser
}
