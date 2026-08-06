package crashreport

import (
	"strings"
	"testing"
)

func resourceIPS(subtype, indicator string) string {
	return `{"app_name":"Helper","bug_type":"309","name":"Helper"}
{"procName":"Helper","pid":900,"exception":{"type":"EXC_RESOURCE","subtype":"` + subtype + `","codes":"0x0"},"termination":{"namespace":"RESOURCE","indicator":"` + indicator + `"},"faultingThread":0,"threads":[{"queue":"busy","triggered":true,"frames":[{"imageIndex":0,"imageOffset":10,"symbol":"loop","symbolLocation":5}]}],"usedImages":[{"name":"Helper"}]}`
}

func TestResourceCPUFatal(t *testing.T) {
	r, err := Parse([]byte(resourceIPS("CPU (Limit 80% Was 91% over 180s)", "")))
	if err != nil {
		t.Fatal(err)
	}
	if r.Resource == nil {
		t.Fatal("expected a resource kill")
	}
	if r.Resource.Flavor != "CPU" {
		t.Errorf("flavor = %q, want CPU", r.Resource.Flavor)
	}
	if r.Resource.Limit != "80%" {
		t.Errorf("limit = %q, want 80%%", r.Resource.Limit)
	}
	if r.Resource.Observed != "91%" {
		t.Errorf("observed = %q, want 91%%", r.Resource.Observed)
	}
	if r.Resource.Window != "180s" {
		t.Errorf("window = %q, want 180s", r.Resource.Window)
	}
	if !strings.Contains(r.Resource.Explanation, "CPU") {
		t.Errorf("explanation = %q", r.Resource.Explanation)
	}
}

func TestResourceWakeups(t *testing.T) {
	r, err := Parse([]byte(resourceIPS("WAKEUPS", "")))
	if err != nil {
		t.Fatal(err)
	}
	if r.Resource == nil || r.Resource.Flavor != "WAKEUPS" {
		t.Fatalf("resource = %+v, want flavor WAKEUPS", r.Resource)
	}
	if !strings.Contains(r.Resource.Explanation, "wakeups") {
		t.Errorf("explanation = %q, want wakeups mention", r.Resource.Explanation)
	}
}

func TestResourceUnknownFlavor(t *testing.T) {
	r, err := Parse([]byte(resourceIPS("", "")))
	if err != nil {
		t.Fatal(err)
	}
	if r.Resource == nil || r.Resource.Flavor != "UNKNOWN" {
		t.Fatalf("resource = %+v, want flavor UNKNOWN", r.Resource)
	}
	if r.Resource.Explanation == "" {
		t.Error("unknown flavor should still carry a generic explanation")
	}
}

func TestNonResourceHasNoResource(t *testing.T) {
	r, err := Parse([]byte(sampleIPS)) // EXC_BAD_ACCESS fixture from crashreport_test.go
	if err != nil {
		t.Fatal(err)
	}
	if r.Resource != nil {
		t.Errorf("expected nil Resource for a segfault, got %+v", r.Resource)
	}
}
