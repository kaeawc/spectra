package main

import (
	"bytes"
	"strings"
	"testing"
)

const resourceSampleIPS = `{"app_name":"Helper","bug_type":"309","name":"Helper"}
{"procName":"Helper","pid":900,"exception":{"type":"EXC_RESOURCE","subtype":"CPU (Limit 80% Was 95% over 60s)"},"termination":{"namespace":"RESOURCE","indicator":"CPU"},"faultingThread":0,"threads":[{"queue":"worker","triggered":true,"frames":[{"imageIndex":0,"imageOffset":10,"symbol":"busyLoop","symbolLocation":3}]}],"usedImages":[{"name":"Helper"}]}`

func TestCrashResourceRenders(t *testing.T) {
	p := writeCrashTemp(t, "Helper.ips", resourceSampleIPS)
	var out, errBuf bytes.Buffer
	if code := runCrashWithIO([]string{"resource", p}, &out, &errBuf, nil); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"resource-limit kill", "CPU", "offending thread", "busyLoop", "window=60s"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestCrashResourceNonResource(t *testing.T) {
	p := writeCrashTemp(t, "App.ips", inspectSampleIPS) // EXC_BAD_ACCESS fixture
	var out, errBuf bytes.Buffer
	if code := runCrashWithIO([]string{"resource", p}, &out, &errBuf, nil); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "not a resource-limit kill") {
		t.Errorf("output should note non-resource; got:\n%s", out.String())
	}
}
