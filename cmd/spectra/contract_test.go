package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/capabilities"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestCapabilitiesContract(t *testing.T) {
	versionOutput := captureStdout(t, func() {
		if code := dispatch([]string{"version"}); code != 0 {
			t.Fatalf("version exit = %d", code)
		}
	})
	output := captureStdout(t, func() {
		if code := dispatch([]string{"capabilities", "--json"}); code != 0 {
			t.Fatalf("capabilities exit = %d", code)
		}
	})
	var got capabilities.Manifest
	decoder := json.NewDecoder(strings.NewReader(output))
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("expected exactly one JSON object, trailing decode = %v", err)
	}
	if got.Schema != (capabilities.SchemaRef{Name: "spectra.capabilities", Version: 1}) || got.SpectraVersion != strings.TrimSpace(versionOutput) {
		t.Fatalf("schema/version = %+v; version output = %q", got, versionOutput)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("platform = %s/%s", got.OS, got.Arch)
	}
	want := []capabilities.Interface{
		{Name: "version", Argv: []string{"version"}, Output: "text"},
		{Name: "inspect", Argv: []string{"--json", "<app_path>..."}, Output: "json", ResultSchema: &capabilities.SchemaRef{Name: "spectra.inspect", Version: 1}},
		{Name: "snapshot", Argv: []string{"snapshot", "--json", "[--no-apps]"}, Output: "json", ResultSchema: &capabilities.SchemaRef{Name: "spectra.snapshot", Version: 1}},
		{Name: "capabilities", Argv: []string{"capabilities", "--json"}, Output: "json", ResultSchema: &capabilities.SchemaRef{Name: "spectra.capabilities", Version: 1}},
	}
	if !reflect.DeepEqual(got.Interfaces, want) {
		t.Fatalf("interfaces = %+v, want %+v", got.Interfaces, want)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatal(err)
	}
	versionInterface := raw["interfaces"].([]any)[0].(map[string]any)
	if _, ok := versionInterface["result_schema"]; ok {
		t.Fatal("version result_schema must be omitted")
	}
}

func TestInspectJSONContract(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("app inspection requires macOS")
	}
	app := makeContractBundle(t)
	output := captureStdout(t, func() {
		if code := dispatch([]string{"--json", app}); code != 0 {
			t.Fatalf("inspect exit = %d", code)
		}
	})
	var results []map[string]any
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("result count = %d; output = %s", len(results), output)
	}
	r := results[0]
	if r["Path"] != app || r["BundleID"] != "com.example.contract" {
		t.Fatalf("path/bundle ID = %v/%v", r["Path"], r["BundleID"])
	}
	for _, field := range []string{"UI", "Runtime", "Language", "Packaging", "Confidence"} {
		if _, ok := r[field].(string); !ok {
			t.Errorf("%s must be a string, got %T", field, r[field])
		}
	}
}

func makeContractBundle(t *testing.T) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Contract.app")
	macOS := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>main</string>
<key>CFBundleIdentifier</key><string>com.example.contract</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(macOS, "main"), []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestSnapshotJSONContract(t *testing.T) {
	// The CLI capture reads the live host. Its JSON encoder serializes Snapshot directly.
	data, err := json.Marshal(snapshot.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "taken_at", "kind", "host", "host_facts", "apps", "toolchains", "power", "system_limits", "network", "storage"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("snapshot missing %q", key)
		}
	}
}
