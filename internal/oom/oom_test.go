package oom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyVariants(t *testing.T) {
	cases := map[string]Variant{
		"Java heap space":                       VariantHeapSpace,
		"GC overhead limit exceeded":            VariantGCOverhead,
		"Metaspace":                             VariantMetaspace,
		"Compressed class space":                VariantCompressedClassSpace,
		"Direct buffer memory":                  VariantDirectBuffer,
		"unable to create new native thread":    VariantNativeThread,
		"Requested array size exceeds VM limit": VariantArraySize,
		"Map failed":                            VariantNativeMemory,
		"Out of swap space?":                    VariantNativeMemory,
		"some brand new message":                VariantUnknown,
	}
	for msg, want := range cases {
		if got := Classify(msg); got != want {
			t.Errorf("Classify(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestClassifyCompressedClassBeforeMetaspace(t *testing.T) {
	// The compressed-class-space message must not be mis-bucketed as metaspace.
	if got := Classify("Compressed class space"); got != VariantCompressedClassSpace {
		t.Fatalf("got %q, want compressed_class_space", got)
	}
}

func TestScanFindsMultipleOccurrences(t *testing.T) {
	log := strings.Join([]string{
		"2026-08-31 10:00:00 INFO starting up",
		`Exception in thread "main" java.lang.OutOfMemoryError: Java heap space`,
		"\tat com.acme.App.main(App.java:12)",
		"2026-08-31 10:05:00 WARN retry",
		"Caused by: java.lang.OutOfMemoryError: Metaspace",
	}, "\n")
	events := Scan(strings.NewReader(log))
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	if events[0].Variant != VariantHeapSpace || events[0].LineNo != 2 {
		t.Errorf("event 0 = %+v", events[0])
	}
	if events[1].Variant != VariantMetaspace || events[1].LineNo != 5 {
		t.Errorf("event 1 = %+v", events[1])
	}
}

func TestScanNoFalsePositiveOnPlainText(t *testing.T) {
	if e := Scan(strings.NewReader("all good\nno errors here\n")); len(e) != 0 {
		t.Fatalf("expected no events, got %+v", e)
	}
}

func TestScanFileMissing(t *testing.T) {
	if e, err := ScanFile(filepath.Join(t.TempDir(), "nope.log"), 1<<20); err == nil || e != nil {
		t.Fatalf("missing file should return (nil, err), got (%v, %v)", e, err)
	}
}

func TestScanFileTailBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	// Prefix padding, then an OOM near the end. Read a small tail that only
	// covers the OOM line.
	padding := strings.Repeat("x filler line to grow the file\n", 1000)
	content := padding + "java.lang.OutOfMemoryError: Direct buffer memory\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := ScanFile(path, 128) // tiny tail
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Variant != VariantDirectBuffer {
		t.Fatalf("tail scan = %+v", events)
	}
}

func TestVariantHumanAndRemediationCoverAll(t *testing.T) {
	for _, v := range []Variant{
		VariantHeapSpace, VariantGCOverhead, VariantMetaspace, VariantCompressedClassSpace,
		VariantDirectBuffer, VariantNativeThread, VariantArraySize, VariantNativeMemory, VariantUnknown,
	} {
		if v.Human() == "" || v.Remediation() == "" {
			t.Errorf("variant %q missing Human/Remediation", v)
		}
	}
}
