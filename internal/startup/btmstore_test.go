package startup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"howett.net/plist"
)

// Synthetic store user keys: the test user's generated UUID, nobody's
// well-known one (uid -2), and another user's that is never resolved.
const (
	testUserUUID  = "11111111-2222-3333-4444-555555555501"
	otherUserUUID = "11111111-2222-3333-4444-555555555502"
	nobodyUUID    = wellKnownUUIDPrefix + "FFFFFFFE"
)

// archive builds an NSKeyedArchiver binary plist the way Foundation lays one
// out: $objects[0] is $null, objects refer to each other by UID, and each
// object names its class through a $class reference.
type archive struct {
	objects []any
	classes map[string]plist.UID
}

func newArchive() *archive {
	return &archive{objects: []any{"$null"}, classes: map[string]plist.UID{}}
}

func (a *archive) add(v any) plist.UID {
	a.objects = append(a.objects, v)
	return plist.UID(len(a.objects) - 1)
}

func (a *archive) class(name string) plist.UID {
	if u, ok := a.classes[name]; ok {
		return u
	}
	u := a.add(map[string]any{"$classname": name, "$classes": []any{name, "NSObject"}})
	a.classes[name] = u
	return u
}

func (a *archive) obj(class string, fields map[string]any) plist.UID {
	fields["$class"] = a.class(class)
	return a.add(fields)
}

// str archives s; "" is $null, as Foundation archives a nil string.
func (a *archive) str(s string) plist.UID {
	if s == "" {
		return 0
	}
	return a.add(s)
}

func (a *archive) array(class string, refs []plist.UID) plist.UID {
	objs := make([]any, len(refs))
	for i, r := range refs {
		objs[i] = r
	}
	return a.obj(class, map[string]any{"NS.objects": objs})
}

func (a *archive) dict(keys, vals []plist.UID) plist.UID {
	k, v := make([]any, len(keys)), make([]any, len(vals))
	for i := range keys {
		k[i], v[i] = keys[i], vals[i]
	}
	return a.obj("NSMutableDictionary", map[string]any{"NS.keys": k, "NS.objects": v})
}

// url archives an NSURL; base is the parent bundle's URL for a relative one.
func (a *archive) url(rel, base string) plist.UID {
	if rel == "" {
		return 0
	}
	b := plist.UID(0)
	if base != "" {
		b = a.url(base, "")
	}
	return a.obj("NSURL", map[string]any{"NS.base": b, "NS.relative": a.str(rel)})
}

// rec is one synthetic ItemRecord.
type rec struct {
	name, dev, team, id, url, base, exe, bundle, parent string
	typ, disp                                           int64
	assoc                                               []string
}

func (a *archive) record(r rec) plist.UID {
	assoc := plist.UID(0)
	if r.assoc != nil {
		refs := make([]plist.UID, len(r.assoc))
		for i, s := range r.assoc {
			refs[i] = a.str(s)
		}
		assoc = a.array("NSArray", refs)
	}
	return a.obj("ItemRecord", map[string]any{
		"name": a.str(r.name), "developerName": a.str(r.dev), "teamIdentifier": a.str(r.team),
		"identifier": a.str(r.id), "url": a.url(r.url, r.base), "executablePath": a.str(r.exe),
		"bundleIdentifier": a.str(r.bundle), "container": a.str(r.parent),
		"type": r.typ, "disposition": r.disp, "flags": int64(0), "generation": int64(1),
		"associatedBundleIdentifiers": assoc, "items": a.array("NSArray", nil),
		"uuid":             a.obj("NSUUID", map[string]any{"NS.uuidbytes": make([]byte, 16)}),
		"modificationDate": 0.0, "executableModificationDate": 0.0,
	})
}

// encode archives users (UUID key -> records) as store version topVersion.
func (a *archive) encode(t *testing.T, topVersion int, keys []string, users map[string][]rec) []byte {
	t.Helper()
	var ks, vs []plist.UID
	for _, k := range keys {
		var refs []plist.UID
		for _, r := range users[k] {
			refs = append(refs, a.record(r))
		}
		ks = append(ks, a.str(k))
		vs = append(vs, a.array("NSMutableArray", refs))
	}
	store := a.obj("Storage", map[string]any{
		"itemsByUserIdentifier":        a.dict(ks, vs),
		"userSettingsByUserIdentifier": a.dict(nil, nil),
	})
	data, err := plist.Marshal(map[string]any{
		"$archiver": "NSKeyedArchiver", "$version": int64(100000),
		"$top":     map[string]any{"version": int64(topVersion), "store": store},
		"$objects": a.objects,
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixtureStore is testdata/dumpbtm.txt as store records: the same made-up
// apps, so a store read and an sfltool read can be compared row for row.
func fixtureStore(t *testing.T) []byte {
	t.Helper()
	const (
		meet  = "file:///Applications/Example%20Meet.app/"
		notes = "file:///Applications/Example%20Notes.app/"
		chat  = "file:///Applications/Example%20Chat%20Classic.app/"
		std   = "file:///Applications/Example%20Studio.app/"
	)
	meetExe := "/Applications/Example Meet.app/Contents/Library/LaunchAgents/ExampleMeetUpdater.app/Contents/MacOS/ExampleMeetUpdater"
	users := map[string][]rec{
		nobodyUUID: {
			{name: "Example Network Ltd", dev: "Example Network Ltd", id: "Example Network Ltd", typ: 0x20, disp: 0x2},
			{name: "com.example.vpnd", dev: "Example Network Ltd", team: "BCDEF23456", typ: 0x10010, disp: 0xb, id: "16.com.example.vpnd",
				url: "file:///Library/LaunchDaemons/com.example.vpnd.plist", exe: "/Library/PrivilegedHelperTools/com.example.vpnd",
				assoc: []string{"com.example.vpn"}, parent: "Example Network Ltd"},
			{name: "Example Meet", team: "CDEFG34567", typ: 0x2, disp: 0x2, id: "2.com.example.meet", url: meet, bundle: "com.example.meet"},
			{name: "com.example.meet.daemon", dev: "Example Meet Inc.", team: "CDEFG34567", typ: 0x10010, disp: 0xb, id: "16.com.example.meet.daemon",
				url: "file:///Library/LaunchDaemons/com.example.meet.daemon.plist", exe: "/Library/PrivilegedHelperTools/com.example.meet.daemon",
				assoc: []string{"com.example.meet"}, parent: "2.com.example.meet"},
		},
		testUserUUID: {
			{id: "Unknown Developer", typ: 0x20, disp: 0x2},
			{name: "example_autoupdate", typ: 0x10008, disp: 0xb, id: "8.com.example.autoupdate",
				url: "file:///Users/user/Library/LaunchAgents/com.example.autoupdate.plist",
				exe: "/Users/user/Library/Application Support/com.example.autoupdate/example_autoupdate", parent: "Unknown Developer"},
			{name: "Example Notes", dev: "Example Software Inc.", team: "ABCDE12345", typ: 0x2, disp: 0x2, id: "2.com.example.notes", url: notes, bundle: "com.example.notes"},
			{name: "Example Notes Browser Helper", dev: "Example Software Inc.", team: "ABCDE12345", typ: 0x4, disp: 0xb,
				id: "4.ABCDE12345.com.example.notes.browser-helper", url: "Contents/Library/LoginItems/Example%20Notes%20Browser%20Helper.app", base: notes,
				bundle: "ABCDE12345.com.example.notes.browser-helper", parent: "2.com.example.notes"},
			{name: "Example Notes Launcher", dev: "Example Software Inc.", team: "ABCDE12345", typ: 0x4, disp: 0xb,
				id: "4.com.example.notes.launcher", url: "Contents/Library/LoginItems/Example%20Notes%20Launcher.app", base: notes,
				bundle: "com.example.notes.launcher", parent: "2.com.example.notes"},
			{name: "Example Clipboard 2", team: "DEFGH45678", typ: 0x2, disp: 0xb, id: "2.com.example.clipboard",
				url: "file:///Applications/Example%20Clipboard%202.app/", bundle: "com.example.clipboard"},
			{name: "Example Chat", dev: "Example Chat LLC", team: "EFGHI56789", typ: 0x2, disp: 0x2, id: "2.com.example.chat", url: chat, bundle: "com.example.chat"},
			{name: "ExampleChatHelper", team: "EFGHI56789", typ: 0x8, disp: 0xb, id: "8.com.example.chat.helper",
				url: "Contents/Library/LaunchAgents/com.example.chat.helper.plist", base: chat, exe: "Contents/Resources/ExampleChatHelper", parent: "2.com.example.chat"},
			{name: "Example Studio", team: "FGHIJ67890", typ: 0x2, disp: 0xa, id: "2.com.example.studio", url: std, bundle: "com.example.studio"},
			{name: "ExampleStudioQuickLook.appex", team: "FGHIJ67890", typ: 0x800, disp: 0x3, id: "2048.com.example.studio.QuickLook",
				url: "Contents/PlugIns/ExampleStudioQuickLook.appex", base: std, bundle: "com.example.studio.QuickLook", parent: "2.com.example.studio"},
			{name: "ExampleStudioSpotlight.appex", team: "FGHIJ67890", typ: 0x1000, disp: 0x3, id: "4096.com.example.studio.Spotlight",
				url: "Contents/Library/Spotlight/ExampleStudioSpotlight.appex", base: std, bundle: "com.example.studio.Spotlight", parent: "2.com.example.studio"},
			{name: "ExampleStudioDockTile.plugin", team: "FGHIJ67890", typ: 0x2000, disp: 0x3, id: "8192.com.example.studio.DockTile",
				url: "Contents/PlugIns/ExampleStudioDockTile.plugin", base: std, bundle: "com.example.studio.DockTile", parent: "2.com.example.studio"},
			{name: "Example Tools s.r.o.", dev: "Example Tools s.r.o.", id: "Example Tools s.r.o.", typ: 0x20, disp: 0x2},
			{name: "Example Toolbox", dev: "Example Tools s.r.o.", team: "GHIJK78901", typ: 0x10008, disp: 0xb, id: "8.com.example.toolbox",
				url: "file:///Users/user/Library/LaunchAgents/com.example.toolbox.plist",
				exe: "/Applications/Example Toolbox.app/Contents/MacOS/example-toolbox", parent: "Example Tools s.r.o."},
			{name: "Example Meet", team: "CDEFG34567", typ: 0x2, disp: 0xa, id: "2.com.example.meet", url: meet, bundle: "com.example.meet"},
			{name: "Example Meet Updater", dev: "Example Meet Inc.", team: "CDEFG34567", typ: 0x10008, disp: 0xb, id: "8.com.example.meet.updater.login.check",
				url: "file:///Library/LaunchAgents/com.example.meet.updater.login.check.plist", exe: meetExe,
				assoc: []string{"com.example.meet"}, parent: "2.com.example.meet"},
			{name: "Example Meet Updater", dev: "Example Meet Inc.", team: "CDEFG34567", typ: 0x10008, disp: 0xb, id: "8.com.example.meet.updater",
				url: "file:///Library/LaunchAgents/com.example.meet.updater.plist", exe: meetExe,
				assoc: []string{"com.example.meet"}, parent: "2.com.example.meet"},
		},
		otherUserUUID: {
			{name: "Example Network Ltd", dev: "Example Network Ltd", id: "Example Network Ltd", typ: 0x20, disp: 0xa},
			{name: "other_user_agent", typ: 0x10008, disp: 0xb, id: "8.com.example.otheruser",
				url: "file:///Users/other/Library/LaunchAgents/com.example.otheruser.plist", exe: "/Users/other/bin/other_user_agent", parent: "Unknown Developer"},
		},
	}
	return newArchive().encode(t, 16, []string{nobodyUUID, testUserUUID, otherUserUUID}, users)
}

func writeStore(t *testing.T, tr tree, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(tr.root, "btm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func testUIDFor(key string) (int, bool) {
	return env{uid: testUID}.btmUID(key, testUserUUID)
}

func TestDecodeBTMStoreMatchesDumpBTM(t *testing.T) {
	got, err := decodeBTMStore(fixtureStore(t), 16, testUIDFor)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		uid int
		id  string
	}
	want := map[key]btmItem{}
	for _, it := range parseDumpBTM([]byte(readBTMFixture(t))) {
		if it.UID != 502 { // another user's random UUID is not resolved
			want[key{it.UID, it.Identifier}] = it
		}
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d items, sfltool fixture has %d", len(got), len(want))
	}
	for _, g := range got {
		w, ok := want[key{g.UID, g.Identifier}]
		if !ok {
			t.Fatalf("unexpected item %+v", g)
		}
		// The fixture's spotlight and dock tile bits are not ones the store
		// decoder names; they must still never count as launching.
		if w.Type == "spotlight" || w.Type == "dock tile" {
			if !strings.HasPrefix(g.Type, "0x") || g.startsAtLogin() {
				t.Fatalf("%s: type %q", g.Identifier, g.Type)
			}
			g.Type = w.Type
		}
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("store item\n%+v\nsfltool item\n%+v", g, w)
		}
	}
}

// A store read without root gives the same rows as sfltool under sudo.
func TestListFromStoreMatchesSfltool(t *testing.T) {
	tr := newTree(t)
	writeJob(t, tr.userAgents, "com.example.toolbox", `<key>Program</key><string>/opt/t</string><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.libAgents, "com.example.meet.updater", `<key>Program</key><string>/opt/meet</string><key>StartInterval</key><integer>60</integer>`)
	f := newFakeLaunchd()
	f.btm = readBTMFixture(t)
	f.loaded["gui/501"]["com.example.notes.launcher"] = serviceState{PID: 31}

	rootOpts := tr.opts(f)
	rootOpts.EUID = func() int { return 0 }
	rootOpts.Getenv = func(string) string { return "501" }
	viaSfltool, err := List(context.Background(), rootOpts)
	if err != nil {
		t.Fatal(err)
	}

	writeStore(t, tr, "BackgroundItems-v16.btm", fixtureStore(t))
	writeStore(t, tr, "BackgroundItems-v13.btm", []byte("stale, older version"))
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	viaStore, err := List(context.Background(), tr.opts(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls() {
		if c.Name == sfltoolPath {
			t.Fatal("sfltool ran without root")
		}
	}
	if s := sourceStatus(viaStore, SourceBTMStore); s.Status != StatusOK || !strings.Contains(s.Detail, "BackgroundItems-v16.btm") {
		t.Fatalf("btm_store source = %+v (sources %+v)", s, viaStore.Sources)
	}
	if s := sourceStatus(viaStore, SourceBTM); s.Name != "" {
		t.Fatalf("store read also reported %+v", s)
	}
	for i := range viaStore.Items {
		for j, s := range viaStore.Items[i].Sources {
			if s == SourceBTMStore {
				viaStore.Items[i].Sources[j] = SourceBTM
			}
		}
	}
	if len(viaStore.Items) < 10 || !reflect.DeepEqual(viaStore.Items, viaSfltool.Items) {
		t.Fatalf("store rows\n%+v\nsfltool rows\n%+v", viaStore.Items, viaSfltool.Items)
	}
}

func TestBTMStoreFallsBackToNeedsRoot(t *testing.T) {
	valid := fixtureStore(t)
	cases := []struct {
		name   string
		files  map[string][]byte
		uuid   func(int) (string, error)
		detail string
	}{
		{name: "no store", detail: "store directory"},
		{name: "no store file", files: map[string][]byte{"other.db": []byte("x")}, detail: "no BackgroundItems"},
		{name: "newer unverified version", files: map[string][]byte{"BackgroundItems-v16.btm": valid, "BackgroundItems-v17.btm": valid},
			detail: "unsupported store version 17"},
		{name: "older version only", files: map[string][]byte{"BackgroundItems-v13.btm": valid}, detail: "unsupported store version 13"},
		{name: "not a plist", files: map[string][]byte{"BackgroundItems-v16.btm": []byte("garbage")}, detail: "decode"},
		{name: "version mismatch", files: map[string][]byte{"BackgroundItems-v16.btm": newArchive().encode(t, 15, nil, nil)}, detail: "$top.version"},
		{name: "uuid lookup fails", files: map[string][]byte{"BackgroundItems-v16.btm": valid},
			uuid: func(int) (string, error) { return "", os.ErrNotExist }, detail: "generated uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTree(t)
			for name, data := range tc.files {
				writeStore(t, tr, name, data)
			}
			f := newFakeLaunchd()
			opts := tr.opts(f)
			if tc.uuid != nil {
				opts.LookupUserUUID = tc.uuid
			}
			rep, err := List(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			s := sourceStatus(rep, SourceBTM)
			if s.Status != StatusNeedsRoot || !strings.Contains(s.Detail, tc.detail) || !strings.Contains(s.Detail, "sudo spectra startup list") {
				t.Fatalf("btm source = %+v, want needs_root mentioning %q", s, tc.detail)
			}
			if len(rep.Items) != 0 {
				t.Fatalf("items = %+v", rep.Items)
			}
		})
	}
}

func TestDecodeBTMStoreRejectsMalformedArchives(t *testing.T) {
	bad := func(mut func(a *archive) []byte) []byte { return mut(newArchive()) }
	cases := map[string][]byte{
		"not keyed archiver": func() []byte {
			d, _ := plist.Marshal(map[string]any{"$archiver": "Other"}, plist.BinaryFormat)
			return d
		}(),
		"dangling reference": bad(func(a *archive) []byte {
			d, _ := plist.Marshal(map[string]any{"$archiver": "NSKeyedArchiver", "$objects": a.objects,
				"$top": map[string]any{"version": int64(16), "store": plist.UID(99)}}, plist.BinaryFormat)
			return d
		}),
		"wrong record class": bad(func(a *archive) []byte {
			r := a.obj("SomethingElse", map[string]any{})
			users := a.dict([]plist.UID{a.str(testUserUUID)}, []plist.UID{a.array("NSArray", []plist.UID{r})})
			store := a.obj("Storage", map[string]any{"itemsByUserIdentifier": users})
			d, _ := plist.Marshal(map[string]any{"$archiver": "NSKeyedArchiver", "$objects": a.objects,
				"$top": map[string]any{"version": int64(16), "store": store}}, plist.BinaryFormat)
			return d
		}),
		"string where a reference belongs": bad(func(a *archive) []byte {
			store := a.obj("Storage", map[string]any{"itemsByUserIdentifier": "inline"})
			d, _ := plist.Marshal(map[string]any{"$archiver": "NSKeyedArchiver", "$objects": a.objects,
				"$top": map[string]any{"version": int64(16), "store": store}}, plist.BinaryFormat)
			return d
		}),
	}
	for name, data := range cases {
		if _, err := decodeBTMStore(data, 16, testUIDFor); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

func TestBTMUID(t *testing.T) {
	cases := map[string]struct {
		uid int
		ok  bool
	}{
		testUserUUID:                          {testUID, true},
		strings.ToLower(testUserUUID):         {testUID, true},
		nobodyUUID:                            {-2, true},
		wellKnownUUIDPrefix + "00000000":      {0, true},
		otherUserUUID:                         {0, false},
		wellKnownUUIDPrefix + "nothex00":      {0, false},
		"FFFFEEEE-DDDD-CCCC-BBBB-AAAA0000000": {0, false},
	}
	for key, want := range cases {
		if uid, ok := testUIDFor(key); uid != want.uid || ok != want.ok {
			t.Errorf("%s: (%d, %v), want (%d, %v)", key, uid, ok, want.uid, want.ok)
		}
	}
}

func TestBTMTypeAndDisposition(t *testing.T) {
	for typ, want := range map[int64]string{0x2: "app", 0x4: "login item", 0x10008: "legacy agent", 0x10010: "legacy daemon", 0x40: "0x40", 0x10080: "0x10080"} {
		if got := btmTypeName(typ); got != want {
			t.Errorf("type %#x = %q, want %q", typ, got, want)
		}
	}
	for d, want := range map[int64]string{0xb: "enabled allowed notified", 0x2: "disabled allowed not notified", 0x0: "disabled disallowed not notified"} {
		if got := strings.Join(btmDisposition(d), " "); got != want {
			t.Errorf("disposition %#x = %q, want %q", d, got, want)
		}
	}
}

func TestParseGeneratedUID(t *testing.T) {
	if got, err := parseGeneratedUID([]byte("GeneratedUID: " + testUserUUID + "\n")); err != nil || got != testUserUUID {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, out := range []string{"", "No such key: GeneratedUID\n", "GeneratedUID: not-a-uuid\n"} {
		if _, err := parseGeneratedUID([]byte(out)); err == nil {
			t.Errorf("%q: no error", out)
		}
	}
}

func TestChangeWithStoreKnowsLoginItems(t *testing.T) {
	tr, f, _ := changeFixture(t)
	writeStore(t, tr, "BackgroundItems-v16.btm", fixtureStore(t))
	res, err := Disable(context.Background(), tr.opts(f), "com.example.notes.launcher", "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != RefuseBTMOnly {
		t.Fatalf("res = %+v", res)
	}
	res, err = Disable(context.Background(), tr.opts(f), "com.example.missing", "user")
	if err != nil || res.Reason != RefuseUnknown || strings.Contains(res.Message, "root") {
		t.Fatalf("unknown with the store read = %+v, %v", res, err)
	}
	if m := mutatingCalls(f.Calls()); len(m) != 0 {
		t.Fatalf("ran %v", m)
	}
}
