package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
)

// With no code sources the report encodes exactly as before the field existed.
func TestThreadDumpReportJSONByteIdentical(t *testing.T) {
	dump := jvm.ParseThreadDump(sampleThreadDump, time.Unix(0, 0).UTC())
	got, err := json.MarshalIndent(threadDumpReport{Summary: jvm.SummarizeThreads(dump), Dump: dump}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/thread_dump_report.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("thread-dump --json changed:\n%s", got)
	}
}
