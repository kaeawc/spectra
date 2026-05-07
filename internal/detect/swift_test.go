package detect

import (
	"reflect"
	"testing"
)

type fakeSwiftInspectionSource struct {
	libs      []string
	appGroups []string
}

func (f fakeSwiftInspectionSource) LinkedLibraries(string) []string {
	return f.libs
}

func (f fakeSwiftInspectionSource) ApplicationGroups(string) []string {
	return f.appGroups
}

func TestInspectSwiftAppCollectsRuntimeFrameworksAndAppGroups(t *testing.T) {
	got := inspectSwiftApp("/Applications/Fake.app", "/Applications/Fake.app/Contents/MacOS/Fake", fakeSwiftInspectionSource{
		libs: []string{
			"/usr/lib/libobjc.A.dylib",
			"/usr/lib/swift/libswiftCore.dylib",
			"/usr/lib/swift/libswiftDispatch.dylib",
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/AppIntents.framework/Versions/A/AppIntents",
			"/System/Library/Frameworks/ScreenCaptureKit.framework/Versions/A/ScreenCaptureKit",
			"/System/Library/Frameworks/SwiftUI.framework/Versions/A/SwiftUI",
			"/System/Library/PrivateFrameworks/WorkflowKit.framework/Versions/A/WorkflowKit",
			"@rpath/PrivateVendor.framework/PrivateVendor",
		},
		appGroups: []string{"group.com.example.fake", "group.com.example.shared"},
	})

	if got == nil {
		t.Fatal("inspectSwiftApp returned nil")
	}
	assertStrings(t, got.RuntimeLibraries, []string{"libswiftCore.dylib", "libswiftDispatch.dylib"})
	assertStrings(t, got.AppleFrameworks, []string{
		"AppIntents.framework",
		"AppKit.framework",
		"ScreenCaptureKit.framework",
		"SwiftUI.framework",
		"WorkflowKit.framework",
	})
	assertStrings(t, got.AppGroups, []string{"group.com.example.fake", "group.com.example.shared"})
	if !got.UsesSwiftUI {
		t.Error("UsesSwiftUI = false, want true")
	}
	if !got.UsesAppIntents {
		t.Error("UsesAppIntents = false, want true")
	}
	if !got.UsesScreenCapture {
		t.Error("UsesScreenCapture = false, want true")
	}
}

func TestInspectSwiftAppDetectsAppKitSwiftWithoutSwiftUI(t *testing.T) {
	got := inspectSwiftApp("/Applications/Fake.app", "/Applications/Fake.app/Contents/MacOS/Fake", fakeSwiftInspectionSource{
		libs: []string{
			"/usr/lib/swift/libswiftCore.dylib",
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/WebKit.framework/Versions/A/WebKit",
		},
	})

	if got == nil {
		t.Fatal("inspectSwiftApp returned nil")
	}
	assertStrings(t, got.RuntimeLibraries, []string{"libswiftCore.dylib"})
	assertStrings(t, got.AppleFrameworks, []string{"AppKit.framework", "WebKit.framework"})
	if got.UsesSwiftUI {
		t.Error("UsesSwiftUI = true, want false")
	}
}

func TestInspectSwiftAppReturnsNilWithoutSwiftSignals(t *testing.T) {
	got := inspectSwiftApp("/Applications/Fake.app", "/Applications/Fake.app/Contents/MacOS/Fake", fakeSwiftInspectionSource{
		libs: []string{
			"/usr/lib/libobjc.A.dylib",
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/WebKit.framework/Versions/A/WebKit",
		},
	})
	if got != nil {
		t.Fatalf("inspectSwiftApp returned %#v, want nil", got)
	}
}

func TestParseOtoolLibraries(t *testing.T) {
	got := parseOtoolLibraries([]byte(`/Applications/Fake.app/Contents/MacOS/Fake:
	/usr/lib/libobjc.A.dylib (compatibility version 1.0.0, current version 228.0.0)
	/System/Library/Frameworks/SwiftUI.framework/Versions/A/SwiftUI (compatibility version 1.0.0, current version 7.0.0)
`))
	assertStrings(t, got, []string{
		"/usr/lib/libobjc.A.dylib",
		"/System/Library/Frameworks/SwiftUI.framework/Versions/A/SwiftUI",
	})
}

func TestApplicationGroupsParsesEntitlementsXML(t *testing.T) {
	got := applicationGroups(`<plist version="1.0"><dict>
<key>com.apple.security.application-groups</key>
<array>
<string>group.com.example.fake</string>
<string>group.com.example.shared</string>
<string>group.com.example.fake</string>
</array>
</dict></plist>`)
	assertStrings(t, got, []string{"group.com.example.fake", "group.com.example.shared"})
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
