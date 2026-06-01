package plistread

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"howett.net/plist"
)

func TestReadPathXML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.plist")
	created := "2026-05-28T13:38:24Z"
	data := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Name</key><string>Spectra</string>
  <key>Count</key><integer>42</integer>
  <key>Ratio</key><real>1.25</real>
  <key>Enabled</key><true/>
  <key>Created</key><date>` + created + `</date>
  <key>Payload</key><data>AQID</data>
  <key>Nested</key>
  <dict><key>Value</key><string>ok</string></dict>
  <key>Items</key>
  <array><string>a</string><string>b</string></array>
</dict>
</plist>`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := ReadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Path != path || d.Size == 0 || d.Mtime.IsZero() {
		t.Fatalf("metadata not populated: %+v", d)
	}
	assertLookup(t, d.Root, "Name", Value{Kind: String, String: "Spectra"})
	assertLookup(t, d.Root, "Count", Value{Kind: Int, Int: 42})
	assertLookup(t, d.Root, "Ratio", Value{Kind: Float, Float: 1.25})
	assertLookup(t, d.Root, "Enabled", Value{Kind: Bool, Bool: true})
	wantDate, _ := time.Parse(time.RFC3339, created)
	assertLookup(t, d.Root, "Created", Value{Kind: Date, Date: wantDate})
	assertLookup(t, d.Root, "Payload", Value{Kind: Data, Data: []byte{1, 2, 3}})
	assertLookup(t, d.Root, "Nested.Value", Value{Kind: String, String: "ok"})
	gotItems, ok := d.Root.Lookup("Items")
	if !ok || gotItems.Kind != Array || len(gotItems.Array) != 2 {
		t.Fatalf("Items = %+v, want two-element array", gotItems)
	}
}

func TestReadPathBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.plist")
	data, err := plist.Marshal(map[string]any{
		"Name":    "Binary",
		"Enabled": true,
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := ReadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	assertLookup(t, d.Root, "Name", Value{Kind: String, String: "Binary"})
	assertLookup(t, d.Root, "Enabled", Value{Kind: Bool, Bool: true})
}

func TestReadKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.plist")
	data := `<plist version="1.0"><dict><key>Outer</key><dict><key>Inner</key><string>value</string></dict></dict></plist>`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadKey(path, "Outer.Inner")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != String || got.String != "value" {
		t.Fatalf("ReadKey = %+v, want string value", got)
	}
	if _, err := ReadKey(path, "Missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadKey missing err = %v, want fs.ErrNotExist", err)
	}
}

func TestLookupPrefersExactDottedKey(t *testing.T) {
	root := Value{Kind: Dict, Dict: map[string]Value{
		"com.apple.example": {Kind: String, String: "exact"},
		"com": {Kind: Dict, Dict: map[string]Value{
			"apple": {Kind: String, String: "nested"},
		}},
	}}

	got, ok := root.Lookup("com.apple.example")
	if !ok || got.Kind != String || got.String != "exact" {
		t.Fatalf("Lookup exact dotted key = %+v, %v", got, ok)
	}
}

func TestResolveDomain(t *testing.T) {
	got, err := ResolveDomain("com.apple.TimeMachine", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Library/Preferences/com.apple.TimeMachine.plist" {
		t.Fatalf("ResolveDomain = %q", got)
	}
	got, err = ResolveDomain("/tmp/example.plist", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/example.plist" {
		t.Fatalf("absolute ResolveDomain = %q", got)
	}
}

func TestResolveCurrentHostUsesExistingByHostMatch(t *testing.T) {
	files := fakeReader{
		glob: []string{"/Library/Preferences/ByHost/com.apple.Spotlight.ABC.plist"},
	}
	got, err := resolveDomainWithReader("com.apple.Spotlight", true, files)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Library/Preferences/ByHost/com.apple.Spotlight.ABC.plist" {
		t.Fatalf("ResolveDomain ByHost = %q", got)
	}
}

func TestPreferencePermissionMapsToFullDiskAccess(t *testing.T) {
	err := mapPreferenceReadErr("/Library/Preferences/com.apple.TimeMachine.plist", syscall.EACCES)
	if !errors.Is(err, ErrNeedsFullDiskAccess) {
		t.Fatalf("err = %v, want ErrNeedsFullDiskAccess", err)
	}
	err = mapPreferenceReadErr("/tmp/com.apple.TimeMachine.plist", syscall.EACCES)
	if errors.Is(err, ErrNeedsFullDiskAccess) {
		t.Fatalf("non-preference err = %v, did not want ErrNeedsFullDiskAccess", err)
	}
}

func TestTypedTimeMachinePrefs(t *testing.T) {
	root := Value{Kind: Dict, Dict: map[string]Value{
		"AutoBackup":         {Kind: Bool, Bool: true},
		"PreferencesVersion": {Kind: Int, Int: 5},
		"RequiresACPower":    {Kind: Bool, Bool: true},
		"SkipPaths": {Kind: Array, Array: []Value{
			{Kind: String, String: "/tmp/skip"},
		}},
		"Destinations": {Kind: Array, Array: []Value{
			{Kind: Dict, Dict: map[string]Value{
				"Name": {Kind: String, String: "Backup"},
				"ID":   {Kind: Int, Int: 7},
			}},
		}},
	}}

	got := timeMachinePrefsFromValue(root)
	if !got.AutoBackup || !got.RequiresACPower || got.PreferencesVersion != 5 {
		t.Fatalf("prefs booleans/version = %+v", got)
	}
	if !reflect.DeepEqual(got.SkipPaths, []string{"/tmp/skip"}) {
		t.Fatalf("SkipPaths = %#v", got.SkipPaths)
	}
	if len(got.Destinations) != 1 || got.Destinations[0]["Name"] != "Backup" || got.Destinations[0]["ID"] != int64(7) {
		t.Fatalf("Destinations = %#v", got.Destinations)
	}
}

func TestTypedSoftwareUpdateAndSpotlightPrefs(t *testing.T) {
	now := time.Date(2026, 5, 28, 13, 38, 24, 0, time.UTC)
	suRoot := Value{Kind: Dict, Dict: map[string]Value{
		"AutomaticCheckEnabled":                  {Kind: Bool, Bool: true},
		"AutomaticDownload":                      {Kind: Bool, Bool: true},
		"AutomaticallyInstallMacOSUpdates":       {Kind: Bool, Bool: false},
		"ConfigDataInstall":                      {Kind: Bool, Bool: true},
		"CriticalUpdateInstall":                  {Kind: Bool, Bool: true},
		"LastAttemptSystemVersion":               {Kind: String, String: "15.6.1"},
		"LastRecommendedMajorOSBundleIdentifier": {Kind: String, String: "com.apple.InstallAssistant.macOS"},
		"LastSuccessfulDate":                     {Kind: Date, Date: now},
		"LastRecommendedUpdatesAvailable":        {Kind: Int, Int: 2},
		"PrimaryLanguages": {Kind: Array, Array: []Value{
			{Kind: String, String: "en-US"},
		}},
	}}
	su := softwareUpdatePrefsFromValue(suRoot)
	if !su.AutomaticCheckEnabled || !su.AutomaticDownload || su.AutomaticallyInstallMacOSUpdates {
		t.Fatalf("software update booleans = %+v", su)
	}
	if su.LastAttemptSystemVersion != "15.6.1" || su.LastRecommendedMajorOSBundleID == "" || !su.LastSuccessfulDate.Equal(now) {
		t.Fatalf("software update fields = %+v", su)
	}
	if su.LastRecommendedUpdatesAvailable != 2 || !reflect.DeepEqual(su.PrimaryLanguages, []string{"en-US"}) {
		t.Fatalf("software update slices/count = %+v", su)
	}

	spotRoot := Value{Kind: Dict, Dict: map[string]Value{
		"IndexingEnabled": {Kind: Bool, Bool: true},
		"MenuItemHidden":  {Kind: Bool, Bool: true},
		"StoresDisabled":  {Kind: Bool, Bool: false},
		"Exclusions": {Kind: Array, Array: []Value{
			{Kind: String, String: "/Volumes/Build"},
		}},
		"Volumes": {Kind: Array, Array: []Value{
			{Kind: String, String: "/"},
		}},
	}}
	spot := spotlightPrefsFromValue(spotRoot)
	if !spot.IndexingEnabled || !spot.MenuItemHidden || spot.StoresDisabled {
		t.Fatalf("spotlight booleans = %+v", spot)
	}
	if !reflect.DeepEqual(spot.Exclusions, []string{"/Volumes/Build"}) || !reflect.DeepEqual(spot.Volumes, []string{"/"}) {
		t.Fatalf("spotlight paths = %+v", spot)
	}
}

func assertLookup(t *testing.T, root Value, key string, want Value) {
	t.Helper()
	got, ok := root.Lookup(key)
	if !ok {
		t.Fatalf("Lookup(%q) missing", key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup(%q) = %#v, want %#v", key, got, want)
	}
}

type fakeReader struct {
	glob []string
}

func (f fakeReader) Stat(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
func (f fakeReader) ReadFile(string) ([]byte, error)  { return nil, fs.ErrNotExist }
func (f fakeReader) Glob(string) ([]string, error)    { return f.glob, nil }
