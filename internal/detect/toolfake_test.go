package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// toolFake is a proc.Runner that replays canned plutil output, so the
// Info.plist- and launchd-plist-driven detections can run on any host OS.
// Unscripted commands fail the way a missing binary does, which is how the
// production code already treats an absent tool.
//
// JSON replies mirror `plutil -convert json -o - <plist>` (compact JSON of the
// top-level dict); XML replies mirror `plutil -convert xml1 -o - <plist>`.
type toolFake struct {
	json map[string][]byte
	xml  map[string][]byte
}

func newToolFake() *toolFake {
	return &toolFake{json: map[string][]byte{}, xml: map[string][]byte{}}
}

// plist scripts the JSON conversion of the plist at path.
func (f *toolFake) plist(tb testing.TB, path string, dict map[string]any) *toolFake {
	tb.Helper()
	b, err := json.Marshal(dict)
	if err != nil {
		tb.Fatal(err)
	}
	f.json[path] = b
	return f
}

// bundlePlist scripts the Info.plist written by makeBundle, plus extra keys.
func (f *toolFake) bundlePlist(tb testing.TB, app string, extra map[string]any) *toolFake {
	tb.Helper()
	dict := map[string]any{"CFBundleExecutable": "main"}
	for k, v := range extra {
		dict[k] = v
	}
	return f.plist(tb, filepath.Join(app, "Contents", "Info.plist"), dict)
}

// frameworkPlist scripts a framework Info.plist the way writeFrameworkPlist
// writes it on disk.
func (f *toolFake) frameworkPlist(tb testing.TB, path, version string) *toolFake {
	tb.Helper()
	return f.plist(tb, path, map[string]any{
		"CFBundleShortVersionString": version,
		"CFBundleVersion":            version,
	})
}

// xmlPlist scripts the xml1 conversion of the plist at path.
func (f *toolFake) xmlPlist(path, xml string) *toolFake {
	f.xml[path] = []byte(xml)
	return f
}

func (f *toolFake) Run(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
	if cmd.Name == "plutil" && len(cmd.Args) == 5 && cmd.Args[0] == "-convert" && cmd.Args[2] == "-o" && cmd.Args[3] == "-" {
		table := map[string]map[string][]byte{"json": f.json, "xml1": f.xml}[cmd.Args[1]]
		if out, ok := table[cmd.Args[4]]; ok {
			return proc.Result{Stdout: out}, nil
		}
		return proc.Result{ExitCode: 1, Stderr: []byte(cmd.Args[4] + ": No such file or directory")}, nil
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("toolFake: %s not scripted", cmd.Name)
}

// detectDarwinFaked runs the full detection pipeline as if on macOS with the
// given fake standing in for plutil. codesign/spctl go through the same
// runner and fail as unscripted; otool is not routed through the runner and
// is simply absent off macOS, which classifyByLinkedLibs treats as no libs.
func detectDarwinFaked(tb testing.TB, app string, f *toolFake, opts Options) (Result, error) {
	tb.Helper()
	opts.OS = hostos.Darwin
	opts.Runner = f
	return DetectWith(app, opts)
}

// plutilVariant lets one test body run against both the canned fake and the
// real plutil, so converting a test to the fake does not drop its macOS
// coverage.
type plutilVariant struct{ real bool }

// detect runs the pipeline through the fake, or through the host's real tools
// (the original Detect path) for the real variant; f is ignored there.
func (v plutilVariant) detect(tb testing.TB, app string, f *toolFake, opts Options) (Result, error) {
	tb.Helper()
	if v.real {
		return DetectWith(app, opts)
	}
	return detectDarwinFaked(tb, app, f, opts)
}

// runner returns the Runner to inject: the fake, or nil for real plutil.
func (v plutilVariant) runner(f *toolFake) proc.Runner {
	if v.real {
		return nil
	}
	return f
}

// withPlutil runs body as a "fake plutil" subtest everywhere and as a
// "real plutil" subtest that is skipped off macOS.
func withPlutil(t *testing.T, body func(t *testing.T, v plutilVariant)) {
	t.Helper()
	t.Run("fake plutil", func(t *testing.T) { body(t, plutilVariant{}) })
	t.Run("real plutil", func(t *testing.T) {
		requireMacOS(t)
		body(t, plutilVariant{real: true})
	})
}
