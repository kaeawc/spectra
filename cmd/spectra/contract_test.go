package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	protocol "github.com/kaeawc/spectra-protocol/protocol/v1"
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
	got, err := protocol.DecodeSpectraCapabilities([]byte(output))
	if err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if got.Schema != (protocol.SchemaRef{Name: protocol.SchemaCapabilities, Version: protocol.CapabilitiesSchemaVersion}) || got.SpectraVersion != strings.TrimSpace(versionOutput) {
		t.Fatalf("schema/version = %+v; version output = %q", got, versionOutput)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("platform = %s/%s", got.OS, got.Arch)
	}
	assertResultSchema := func(op protocol.Operation, name string) {
		t.Helper()
		gotSchema, err := got.ResultSchemaFor(op)
		if err != nil {
			t.Fatalf("result schema for %s: %v", op, err)
		}
		version, ok := protocol.SupportedResultSchemaVersion(name)
		if !ok || gotSchema != (protocol.SchemaRef{Name: name, Version: version}) {
			t.Fatalf("result schema for %s = %+v; supported version = %d (known=%t)", op, gotSchema, version, ok)
		}
	}
	assertResultSchema(protocol.OperationSnapshotCreate, protocol.SchemaSnapshot)
	if runtime.GOOS == "darwin" {
		assertResultSchema(protocol.OperationInspect, protocol.SchemaInspect)
	} else {
		_, err := got.ResultSchemaFor(protocol.OperationInspect)
		if err == nil || protocol.CodeOf(err) != protocol.CodeIncompatibleSpectra {
			t.Fatalf("inspect result schema error = %v, code = %q", err, protocol.CodeOf(err))
		}
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
