package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inspectSampleIPS = `{"app_name":"MyApp","app_version":"2.1","bug_type":"309","os_version":"macOS 15.6.1","incident_id":"ABC-123","name":"MyApp"}
{"procName":"MyApp","pid":4012,"exception":{"type":"EXC_BAD_ACCESS","signal":"SIGSEGV","codes":"0x1, 0x0"},"termination":{"namespace":"SIGNAL","indicator":"Segmentation fault: 11"},"faultingThread":0,"threads":[{"queue":"com.apple.main-thread","triggered":true,"frames":[{"imageIndex":0,"imageOffset":1000,"symbol":"main","symbolLocation":42}]}],"usedImages":[{"name":"MyApp","arch":"arm64"}]}`

func writeCrashTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCrashInspectRenders(t *testing.T) {
	p := writeCrashTemp(t, "MyApp.ips", inspectSampleIPS)
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", p}, &out, &errBuf, nil)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"MyApp", "EXC_BAD_ACCESS", "crashed", "main + 42"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestCrashInspectJSON(t *testing.T) {
	p := writeCrashTemp(t, "MyApp.ips", inspectSampleIPS)
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", "--json", p}, &out, &errBuf, nil)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"exception": "EXC_BAD_ACCESS (SIGSEGV)"`) {
		t.Errorf("json output missing decoded exception; got:\n%s", out.String())
	}
}

func TestCrashInspectLegacy(t *testing.T) {
	p := writeCrashTemp(t, "old.crash", "Process: Foo [1]\nIdentifier: com.example.foo\n")
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"inspect", p}, &out, &errBuf, nil)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "legacy") {
		t.Errorf("stderr = %q, want legacy notice", errBuf.String())
	}
}

func TestCrashInspectNoPath(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashWithIO([]string{"inspect"}, &out, &errBuf, nil); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
