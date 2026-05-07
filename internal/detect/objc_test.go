package detect

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeObjCSources struct {
	libs            []string
	plistXML        []byte
	entitlements    []string
	libsErr         error
	plistErr        error
	entitlementsErr error
}

func (f fakeObjCSources) LinkedLibraries(string) ([]string, error) {
	return f.libs, f.libsErr
}

func (f fakeObjCSources) PlistXML(string) ([]byte, error) {
	return f.plistXML, f.plistErr
}

func (f fakeObjCSources) Entitlements(string) ([]string, error) {
	return f.entitlements, f.entitlementsErr
}

func TestInspectObjCAppBuildsProfileFromInjectedSources(t *testing.T) {
	app := makeBundle(t, "FakeObjCProfile")
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>NSPrincipalClass</key><string>NSApplication</string>
<key>NSMainNibFile</key><string>MainMenu</string>
<key>CFBundleDocumentTypes</key><array>
  <dict>
    <key>CFBundleTypeName</key><string>Spectra Trace</string>
    <key>CFBundleTypeRole</key><string>Editor</string>
    <key>CFBundleTypeExtensions</key><array><string>trace</string><string>sptrace</string><string>trace</string></array>
  </dict>
</array>
<key>CFBundleURLTypes</key><array>
  <dict><key>CFBundleURLSchemes</key><array><string>spectra</string><string>spectra-debug</string></array></dict>
</array>
<key>NSServices</key><array>
  <dict><key>NSPortName</key><string>SpectraService</string></dict>
</array>
</dict></plist>`)

	got := inspectObjCApp(app, filepath.Join(app, "Contents/MacOS/main"), Result{
		UI:        "AppKit",
		Runtime:   "ObjC",
		Packaging: "Sparkle",
	}, fakeObjCSources{
		libs: []string{
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/Security.framework/Versions/A/Security",
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
		},
		plistXML: plist,
		entitlements: []string{
			"com.apple.security.network.client",
			"com.apple.security.automation.apple-events",
		},
	})

	if got == nil {
		t.Fatal("ObjC inspection nil")
	}
	if !reflect.DeepEqual(got.LinkedFrameworks, []string{"AppKit", "Security"}) {
		t.Fatalf("LinkedFrameworks = %v", got.LinkedFrameworks)
	}
	if got.PrincipalClass != "NSApplication" || got.MainNibFile != "MainMenu" {
		t.Fatalf("principal/nib = %q/%q", got.PrincipalClass, got.MainNibFile)
	}
	if len(got.DocumentTypes) != 1 {
		t.Fatalf("DocumentTypes = %#v", got.DocumentTypes)
	}
	doc := got.DocumentTypes[0]
	if doc.Name != "Spectra Trace" || doc.Role != "Editor" || !reflect.DeepEqual(doc.Extensions, []string{"sptrace", "trace"}) {
		t.Fatalf("document type = %#v", doc)
	}
	if !reflect.DeepEqual(got.URLSchemes, []string{"spectra", "spectra-debug"}) {
		t.Fatalf("URLSchemes = %v", got.URLSchemes)
	}
	if !reflect.DeepEqual(got.Services, []string{"SpectraService"}) {
		t.Fatalf("Services = %v", got.Services)
	}
	if !reflect.DeepEqual(got.AutomationEntitlements, []string{"com.apple.security.automation.apple-events"}) {
		t.Fatalf("AutomationEntitlements = %v", got.AutomationEntitlements)
	}
	if got.UpdateMechanism != "Sparkle" {
		t.Fatalf("UpdateMechanism = %q", got.UpdateMechanism)
	}
}

func TestInspectObjCAppIgnoresMissingOptionalSources(t *testing.T) {
	app := makeBundle(t, "FakeObjCPartial")
	got := inspectObjCApp(app, filepath.Join(app, "Contents/MacOS/main"), Result{
		UI:      "AppKit",
		Runtime: "ObjC",
	}, fakeObjCSources{
		libsErr:         errors.New("otool unavailable"),
		plistErr:        errors.New("plist unreadable"),
		entitlementsErr: errors.New("unsigned"),
	})
	if got != nil {
		t.Fatalf("ObjC inspection = %#v, want nil when every source is empty", got)
	}
}

func TestScanObjCInspectionOnlyRunsForPlainAppKitObjC(t *testing.T) {
	app := makeBundle(t, "FakeObjCGate")
	if got := scanObjCInspection(app, filepath.Join(app, "Contents/MacOS/main"), Result{UI: "AppKit+Swift", Runtime: "Swift"}); got != nil {
		t.Fatalf("scanObjCInspection = %#v, want nil for Swift AppKit", got)
	}
}

func TestObjCUpdateMechanismFallsBackToBundleMarkers(t *testing.T) {
	app := makeBundle(t, "FakeObjCUpdater")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Sparkle.framework"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := inspectObjCApp(app, filepath.Join(app, "Contents/MacOS/main"), Result{
		UI:      "AppKit",
		Runtime: "ObjC",
	}, fakeObjCSources{
		plistXML: []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>NSPrincipalClass</key><string>NSApplication</string></dict></plist>`),
	})

	if got == nil {
		t.Fatal("ObjC inspection nil")
	}
	if got.UpdateMechanism != "Sparkle" {
		t.Fatalf("UpdateMechanism = %q, want Sparkle", got.UpdateMechanism)
	}
}

func TestParseEntitlementKeys(t *testing.T) {
	xml := `<?xml version="1.0"?><plist version="1.0"><dict>
<key>com.apple.security.app-sandbox</key><true/>
<key>com.apple.security.network.client</key><false/>
<key>com.apple.security.automation.apple-events</key><true/>
</dict></plist>`
	got := parseEntitlementKeys(xml)
	want := []string{"com.apple.security.app-sandbox", "com.apple.security.automation.apple-events"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEntitlementKeys = %v, want %v", got, want)
	}
}
