package startup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"
)

// The Background Task Management store. The file is world-readable, so it
// lists Login Items without root and without sfltool's authorization dialog.
const (
	btmStoreDir = "/private/var/db/com.apple.backgroundtaskmanagement"
	dsclPath    = "/usr/bin/dscl"
	maxBTMStore = 16 << 20
)

// btmStoreVersions are the store versions whose layout has been checked
// against a real store. Any other version falls back to needs_root rather
// than guessing at a layout that changes between macOS releases.
var btmStoreVersions = map[int]bool{16: true}

var (
	btmStoreFileRE = regexp.MustCompile(`^BackgroundItems-v(\d+)\.btm$`)
	uuidRE         = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

// wellKnownUUIDPrefix is how Open Directory derives the generated UUID of
// accounts that have no random one (root, nobody): the prefix plus the uid
// as 8 hex digits.
const wellKnownUUIDPrefix = "FFFFEEEE-DDDD-CCCC-BBBB-AAAA"

// BTM record type bits, as `sfltool dumpbtm` names them. Only bits seen in
// both sfltool output and a real store are named; anything else is printed
// as its hex value and never counts as something that launches.
const (
	btmTypeLegacy  = 0x10000
	btmTypeCurated = 0x80000
	btmTypeUser    = 0x1
)

var btmTypeNames = map[int64]string{
	0x2:   "app",
	0x4:   "login item",
	0x8:   "agent",
	0x10:  "daemon",
	0x20:  "developer",
	0x40:  "spotlight",
	0x80:  "dock tile",
	0x800: "quicklook",
}

// BTM disposition bits.
const (
	btmDispEnabled  = 0x1
	btmDispAllowed  = 0x2
	btmDispNotified = 0x8
)

// readBTMStore lists BTM items from the newest store file.
func (e env) readBTMStore(ctx context.Context) (btmRead, error) {
	path, version, err := e.btmStorePath()
	if err != nil {
		return btmRead{}, err
	}
	if !btmStoreVersions[version] {
		return btmRead{}, fmt.Errorf("%s: unsupported store version %d", filepath.Base(path), version)
	}
	me, err := e.userUUID(ctx)
	if err != nil {
		return btmRead{}, fmt.Errorf("generated uuid of uid %d: %w", e.uid, err)
	}
	data, err := e.files.ReadFile(path, maxBTMStore)
	if err != nil {
		return btmRead{}, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	items, err := decodeBTMStore(ctx, data, version, func(key string) (int, bool) { return e.btmUID(key, me) })
	if err != nil {
		return btmRead{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return btmRead{
		status: SourceStatus{Name: SourceBTMStore, Status: StatusOK, Detail: fmt.Sprintf("%d items from %s", len(items), filepath.Base(path))},
		items:  items, source: SourceBTMStore,
	}, nil
}

// btmStorePath is the BackgroundItems-v<N>.btm with the highest N. Older
// versions stay on disk after a macOS upgrade and are stale.
func (e env) btmStorePath() (string, int, error) {
	dir := e.opts.BTMStoreDir
	if dir == "" {
		dir = btmStoreDir
	}
	entries, err := e.files.ReadDir(dir)
	if err != nil {
		return "", 0, fmt.Errorf("read store directory: %w", err)
	}
	best, version := "", -1
	for _, ent := range entries {
		m := btmStoreFileRE.FindStringSubmatch(ent.Name())
		if m == nil || ent.IsDir() {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > version {
			best, version = filepath.Join(dir, ent.Name()), n
		}
	}
	if best == "" {
		return "", 0, fmt.Errorf("no BackgroundItems-v<N>.btm in %s", dir)
	}
	return best, version, nil
}

// btmUID maps a store's user key (an Open Directory generated UUID) to a
// uid: the caller's own, or one derived from a well-known UUID. Other
// users' random UUIDs are not resolved, and their records are dropped.
func (e env) btmUID(key, me string) (int, bool) {
	if strings.EqualFold(key, me) {
		return e.uid, true
	}
	up := strings.ToUpper(key)
	if !uuidRE.MatchString(key) || !strings.HasPrefix(up, wellKnownUUIDPrefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(up[len(wellKnownUUIDPrefix):], 16, 32)
	if err != nil {
		return 0, false
	}
	// uids are 32-bit two's complement: FFFFFFFE is nobody (-2).
	v := int64(n)
	if v > math.MaxInt32 {
		v -= 1 << 32
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

func (e env) userUUID(ctx context.Context) (string, error) {
	if e.opts.LookupUserUUID != nil {
		return e.opts.LookupUserUUID(e.uid)
	}
	u, err := user.LookupId(strconv.Itoa(e.uid))
	if err != nil {
		return "", fmt.Errorf("lookup uid: %w", err)
	}
	out, err := e.run(ctx, dsclPath, ".", "-read", "/Users/"+u.Username, "GeneratedUID")
	if err != nil {
		return "", err
	}
	return parseGeneratedUID(out)
}

func parseGeneratedUID(out []byte) (string, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "GeneratedUID:"); ok {
			if v = strings.TrimSpace(v); uuidRE.MatchString(v) {
				return v, nil
			}
		}
	}
	return "", errors.New("no GeneratedUID in dscl output")
}

// decodeBTMStore reads the store's NSKeyedArchiver archive:
// $top.store is a Storage whose itemsByUserIdentifier maps a user UUID to
// an array of ItemRecord. Records are mapped to the rows `sfltool dumpbtm`
// prints, minus developer group headers, as parseDumpBTM does.
func decodeBTMStore(ctx context.Context, data []byte, version int, uidFor func(string) (int, bool)) ([]btmItem, error) {
	a, store, err := openBTMArchive(ctx, data, version)
	if err != nil {
		return nil, err
	}
	keys, vals, err := a.dict(store["itemsByUserIdentifier"])
	if err != nil {
		return nil, fmt.Errorf("itemsByUserIdentifier: %w", err)
	}
	var items []btmItem
	for i, k := range keys {
		key, err := a.str(k)
		if err != nil {
			return nil, fmt.Errorf("user key %d: %w", i, err)
		}
		uid, ok := uidFor(key)
		if !ok {
			continue
		}
		recs, err := a.array(vals[i])
		if err != nil {
			return nil, fmt.Errorf("user %d records: %w", i, err)
		}
		for j, r := range recs {
			it, err := a.record(r, uid)
			if err != nil {
				return nil, fmt.Errorf("user %d record %d: %w", i, j, err)
			}
			if it.Identifier != "" && it.Type != "developer" {
				items = append(items, it)
			}
		}
	}
	return items, nil
}

func openBTMArchive(ctx context.Context, data []byte, version int) (keyedArchive, map[string]any, error) {
	root, err := unmarshalBTM(ctx, data)
	if err != nil {
		return keyedArchive{}, nil, err
	}
	if root["$archiver"] != "NSKeyedArchiver" {
		return keyedArchive{}, nil, errors.New("not an NSKeyedArchiver archive")
	}
	objects, ok := root["$objects"].([]any)
	if !ok {
		return keyedArchive{}, nil, errors.New("no $objects")
	}
	a := keyedArchive{objects: objects}
	top, ok := root["$top"].(map[string]any)
	if !ok {
		return a, nil, errors.New("no $top")
	}
	if v, ok := intValue(top["version"]); !ok || v != int64(version) {
		return a, nil, fmt.Errorf("$top.version %v does not match file version %d", top["version"], version)
	}
	store, err := a.object(top["store"], "Storage")
	return a, store, err
}

// Limits on what is handed to the plist decoder. The decoder recurses without
// a depth limit and is superlinear on deeply nested binary plists, so the
// input is vetted first and the decode is time-bounded.
const (
	maxBTMObjects   = 16384
	btmDecodeBudget = 10 * time.Second
	bplistTrailer   = 32
)

// checkBplist requires the bplist00 magic and a sane trailer: offset int
// size, object ref size, object count, top object and offset table offset.
func checkBplist(data []byte) error {
	if !bytes.HasPrefix(data, []byte("bplist00")) {
		return errors.New("not a binary plist")
	}
	if len(data) < 8+bplistTrailer {
		return errors.New("binary plist trailer is truncated")
	}
	t := data[len(data)-bplistTrailer:]
	offSize, refSize := t[6], t[7]
	n := binary.BigEndian.Uint64(t[8:16])
	top := binary.BigEndian.Uint64(t[16:24])
	end := uint64(len(data) - bplistTrailer) // #nosec G115 -- len is non-negative
	tab := binary.BigEndian.Uint64(t[24:32])
	switch {
	case offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8:
		return errors.New("binary plist trailer has an invalid integer size")
	case n == 0 || n > maxBTMObjects:
		return fmt.Errorf("binary plist has %d objects (limit %d)", n, maxBTMObjects)
	case top >= n:
		return errors.New("binary plist top object is out of range")
	case tab < 8 || tab > end || n*uint64(offSize) > end-tab:
		return errors.New("binary plist offset table is out of range")
	}
	return nil
}

// unmarshalBTM decodes the store in a goroutine so a slow decode, which
// cannot be interrupted, does not hold up the listing.
func unmarshalBTM(ctx context.Context, data []byte) (map[string]any, error) {
	if err := checkBplist(data); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, btmDecodeBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		root map[string]any
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var root map[string]any
		_, err := plist.Unmarshal(data, &root)
		ch <- result{root, err}
	}()
	select {
	case r := <-ch:
		return r.root, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("decode abandoned: %w", ctx.Err())
	}
}

// keyedArchive resolves NSKeyedArchiver UID references into $objects.
type keyedArchive struct{ objects []any }

// deref follows a UID; nil is $null.
func (a keyedArchive) deref(v any) (any, error) {
	u, ok := v.(plist.UID)
	if !ok {
		return nil, fmt.Errorf("expected a reference, got %T", v)
	}
	if u == 0 {
		return nil, nil
	}
	if uint64(u) >= uint64(len(a.objects)) {
		return nil, fmt.Errorf("reference %d out of range", u)
	}
	return a.objects[u], nil
}

// object derefs v to an archived object of one of the given classes.
func (a keyedArchive) object(v any, classes ...string) (map[string]any, error) {
	o, err := a.deref(v)
	if err != nil {
		return nil, err
	}
	obj, ok := o.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected %s, got %T", classes[0], o)
	}
	c, err := a.deref(obj["$class"])
	if err != nil {
		return nil, fmt.Errorf("$class: %w", err)
	}
	cm, _ := c.(map[string]any)
	name, _ := cm["$classname"].(string)
	for _, want := range classes {
		if name == want {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("expected %s, got class %q", classes[0], name)
}

// str derefs v to a string; $null is "".
func (a keyedArchive) str(v any) (string, error) {
	o, err := a.deref(v)
	if err != nil || o == nil {
		return "", err
	}
	s, ok := o.(string)
	if !ok {
		return "", fmt.Errorf("expected a string, got %T", o)
	}
	return s, nil
}

func (a keyedArchive) array(v any) ([]any, error) {
	if o, err := a.deref(v); err != nil || o == nil {
		return nil, err
	}
	obj, err := a.object(v, "NSArray", "NSMutableArray")
	if err != nil {
		return nil, err
	}
	refs, ok := obj["NS.objects"].([]any)
	if !ok {
		return nil, errors.New("array without NS.objects")
	}
	return refs, nil
}

func (a keyedArchive) dict(v any) (keys, vals []any, err error) {
	obj, err := a.object(v, "NSDictionary", "NSMutableDictionary")
	if err != nil {
		return nil, nil, err
	}
	keys, ok1 := obj["NS.keys"].([]any)
	vals, ok2 := obj["NS.objects"].([]any)
	if !ok1 || !ok2 || len(keys) != len(vals) {
		return nil, nil, errors.New("malformed dictionary")
	}
	return keys, vals, nil
}

func (a keyedArchive) strList(v any) ([]string, error) {
	refs, err := a.array(v)
	if err != nil || refs == nil {
		return nil, err
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		s, err := a.str(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// url is an NSURL's string as sfltool prints it: NS.relative, which is a
// full file URL, or a path relative to the parent bundle when NS.base is set
// (btmItem.resolve joins those with the parent's URL).
func (a keyedArchive) url(v any) (string, error) {
	if o, err := a.deref(v); err != nil || o == nil {
		return "", err
	}
	obj, err := a.object(v, "NSURL")
	if err != nil {
		return "", err
	}
	return a.str(obj["NS.relative"])
}

func (a keyedArchive) record(v any, uid int) (btmItem, error) {
	obj, err := a.object(v, "ItemRecord")
	if err != nil {
		return btmItem{}, err
	}
	typ, ok1 := intValue(obj["type"])
	disp, ok2 := intValue(obj["disposition"])
	if !ok1 || !ok2 {
		return btmItem{}, errors.New("record without integer type and disposition")
	}
	it := btmItem{BTM: BTM{UID: uid, Type: btmTypeName(typ), Disposition: btmDisposition(disp)}}
	strs := []struct {
		key string
		dst *string
	}{
		{"name", &it.Name}, {"identifier", &it.Identifier}, {"executablePath", &it.ExecutablePath},
		{"bundleIdentifier", &it.BundleID}, {"teamIdentifier", &it.TeamID},
		{"developerName", &it.DeveloperName}, {"container", &it.ParentIdentifier},
	}
	for _, s := range strs {
		if *s.dst, err = a.str(obj[s.key]); err != nil {
			return btmItem{}, fmt.Errorf("%s: %w", s.key, err)
		}
	}
	if it.URL, err = a.url(obj["url"]); err != nil {
		return btmItem{}, fmt.Errorf("url: %w", err)
	}
	if it.AssocBundleIDs, err = a.strList(obj["associatedBundleIdentifiers"]); err != nil {
		return btmItem{}, fmt.Errorf("associatedBundleIdentifiers: %w", err)
	}
	return it, nil
}

// btmTypeName names the kind bits of a record type. Legacy, curated and
// user-item bits are modifiers: a curated legacy agent (0x90008) is still an
// agent.
func btmTypeName(t int64) string {
	name, ok := btmTypeNames[t&^(btmTypeLegacy|btmTypeCurated|btmTypeUser)]
	if !ok {
		return fmt.Sprintf("0x%x", t)
	}
	if t&btmTypeLegacy != 0 {
		name = "legacy " + name
	}
	if t&btmTypeCurated != 0 {
		name = "curated " + name
	}
	return name
}

// btmDisposition spells the disposition bits the way sfltool does:
// "[enabled, allowed, notified] (0xb)".
func btmDisposition(d int64) []string {
	pick := func(bit int64, on, off string) string {
		if d&bit != 0 {
			return on
		}
		return off
	}
	return []string{
		pick(btmDispEnabled, "enabled", "disabled"),
		pick(btmDispAllowed, "allowed", "disallowed"),
		pick(btmDispNotified, "notified", "not notified"),
	}
}

func intValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true // #nosec G115 -- bounded above
	}
	return 0, false
}
