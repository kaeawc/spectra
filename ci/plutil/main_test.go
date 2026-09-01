package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"howett.net/plist"
)

const fixtureXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>Sample</string>
	<key>BuildNum</key><integer>7</integer>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><false/>
</dict>
</plist>
`

func writeFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runShim(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func binaryFixture(t *testing.T) string {
	t.Helper()
	var root any
	if _, err := plist.Unmarshal([]byte(fixtureXML), &root); err != nil {
		t.Fatal(err)
	}
	data, err := plist.Marshal(root, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return writeFixture(t, "bin.plist", data)
}

func TestConvertJSON(t *testing.T) {
	for name, path := range map[string]string{
		"xml":    writeFixture(t, "f.plist", []byte(fixtureXML)),
		"binary": binaryFixture(t),
	} {
		stdout, _, code := runShim(t, "-convert", "json", "-o", "-", path)
		if code != 0 {
			t.Fatalf("%s: exit = %d", name, code)
		}
		var dict map[string]any
		if err := json.Unmarshal([]byte(stdout), &dict); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if dict["CFBundleExecutable"] != "Sample" {
			t.Errorf("%s: CFBundleExecutable = %v", name, dict["CFBundleExecutable"])
		}
		if dict["BuildNum"] != float64(7) {
			t.Errorf("%s: BuildNum = %v", name, dict["BuildNum"])
		}
		if dict["RunAtLoad"] != true || dict["KeepAlive"] != false {
			t.Errorf("%s: flags = %v / %v", name, dict["RunAtLoad"], dict["KeepAlive"])
		}
	}
}

func TestConvertJSONRejectsNonCollectionRoot(t *testing.T) {
	path := writeFixture(t, "s.plist", []byte("garbage"))
	_, stderr, code := runShim(t, "-convert", "json", "-o", "-", path)
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

// internal/detect's plistXMLBool requires <key>Name</key> to be followed by
// a literal self-closing <true/>; this pins that output shape.
func TestConvertXMLTrueElementForm(t *testing.T) {
	stdout, _, code := runShim(t, "-convert", "xml1", "-o", "-", binaryFixture(t))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !bytes.Contains([]byte(stdout), []byte("<key>RunAtLoad</key><true/>")) {
		t.Errorf("xml output missing <key>RunAtLoad</key><true/>: %s", stdout)
	}
}

func TestExtractRaw(t *testing.T) {
	path := writeFixture(t, "f.plist", []byte(fixtureXML))
	stdout, _, code := runShim(t, "-extract", "CFBundleExecutable", "raw", "-o", "-", path)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := string(bytes.TrimSpace([]byte(stdout))); got != "Sample" {
		t.Errorf("raw = %q, want Sample", got)
	}

	if _, _, code := runShim(t, "-extract", "NopeKey", "raw", "-o", "-", path); code != 1 {
		t.Errorf("missing key exit = %d, want 1", code)
	}
}
