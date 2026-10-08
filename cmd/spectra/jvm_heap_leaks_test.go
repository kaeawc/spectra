package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/heap"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/store"
)

// writeLeakHPROF writes an 8-byte-id dump in which class com.acme.Cache holds,
// through a static field, a Holder (0x64) whose only field references a
// 1000-byte array (0x65). Both classes are sticky-class GC roots.
func writeLeakHPROF(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	be := func(w *bytes.Buffer, v any) { _ = binary.Write(w, binary.BigEndian, v) }
	buf.WriteString("JAVA PROFILE 1.0.2\x00")
	be(&buf, uint32(8))
	be(&buf, uint64(0))
	record := func(tag byte, body []byte) {
		buf.WriteByte(tag)
		be(&buf, uint32(0))
		be(&buf, uint32(len(body)))
		buf.Write(body)
	}
	for i, name := range []string{"com/acme/Cache", "com/acme/Holder"} {
		var s, lc bytes.Buffer
		be(&s, uint64(10+i))
		s.WriteString(name)
		record(0x01, s.Bytes())
		be(&lc, uint32(i+1))
		be(&lc, uint64(i+1)) // class object id
		be(&lc, uint32(0))
		be(&lc, uint64(10+i)) // name string id
		record(0x02, lc.Bytes())
	}

	var seg bytes.Buffer
	classDump := func(id uint64, instSize uint32, static uint64, objectFields uint16) {
		seg.WriteByte(0x20)
		be(&seg, id)
		be(&seg, uint32(0))
		for i := 0; i < 6; i++ {
			be(&seg, uint64(0))
		}
		be(&seg, instSize)
		be(&seg, uint16(0)) // const pool
		if static != 0 {
			be(&seg, uint16(1))
			be(&seg, uint64(0)) // name id
			seg.WriteByte(2)    // object
			be(&seg, static)
		} else {
			be(&seg, uint16(0))
		}
		be(&seg, objectFields)
		for i := uint16(0); i < objectFields; i++ {
			be(&seg, uint64(0))
			seg.WriteByte(2)
		}
	}
	classDump(1, 0, 0x64, 0)
	classDump(2, 16, 0, 1)
	seg.WriteByte(0x21) // Holder instance -> array
	be(&seg, uint64(0x64))
	be(&seg, uint32(0))
	be(&seg, uint64(2))
	be(&seg, uint32(8))
	be(&seg, uint64(0x65))
	seg.WriteByte(0x23) // byte[1000]
	be(&seg, uint64(0x65))
	be(&seg, uint32(0))
	be(&seg, uint32(1000))
	seg.WriteByte(8)
	seg.Write(make([]byte, 1000))
	for _, id := range []uint64{1, 2} {
		seg.WriteByte(0x05) // sticky class root
		be(&seg, id)
	}
	record(0x1C, seg.Bytes())
	record(0x2C, nil)

	p := filepath.Join(t.TempDir(), "leak.hprof")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunJVMHeapHPROFLeakSuspectsAndPaths(t *testing.T) {
	path := writeLeakHPROF(t)
	for _, args := range [][]string{
		{"--leak-suspects", path},
		{"--leak-suspects", "--json", "--threshold", "50", path},
		{"--paths", "0x65", path},
		{"--paths", "com.acme.Holder", "--json", path},
	} {
		if code := runJVMHeapHPROF(args); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
	}
	if code := runJVMHeapHPROF([]string{"--paths", "com.acme.Missing", path}); code != 1 {
		t.Errorf("unknown paths target: exit = %d, want 1", code)
	}
}

func TestRunJVMHeapHPROFGraphFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--retained", "--leak-suspects", "a.hprof"},
		{"--leak-suspects", "--paths", "0x1", "a.hprof"},
		{"--file-issues", "a.hprof"},
		{"--leak-suspects", "--threshold", "0", "a.hprof"},
		{"--paths", "0x1", "--max-paths", "0", "a.hprof"},
	} {
		if code := runJVMHeapHPROF(args); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func leakReportFor(t *testing.T, path string) heap.LeakReport {
	t.Helper()
	g, err := heap.ParseObjectGraphFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return heap.LeakSuspects(heap.ComputeRetained(g), heap.LeakOptions{})
}

func TestPrintLeakSuspects(t *testing.T) {
	rep := leakReportFor(t, writeLeakHPROF(t))
	var buf bytes.Buffer
	printLeakSuspects(&buf, "leak.hprof", rep)
	out := buf.String()
	for _, want := range []string{
		"Suspect 1: class com.acme.Cache",
		"accumulation point: [B (0x65)",
		"path from GC root [sticky_class]:",
		"→ com.acme.Holder (0x64)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestLeakSuspectFindings(t *testing.T) {
	path := writeLeakHPROF(t)
	findings := leakSuspectFindings(path, leakReportFor(t, path))
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	f := findings[0]
	if f.RuleID != leakSuspectRuleID || f.Severity != rules.SeverityHigh || f.Subject != "heap leak suspect: class com.acme.Cache" {
		t.Errorf("finding = %+v", f)
	}
	if !strings.Contains(f.Message, path) || !strings.Contains(f.Fix, "--paths 0x65") {
		t.Errorf("finding message/fix = %q / %q", f.Message, f.Fix)
	}

	classFinding := leakSuspectFindings("x.hprof", heap.LeakReport{Suspects: []heap.LeakSuspect{{Kind: heap.LeakSuspectClass, ClassName: "com.acme.Session", PercentOfHeap: 12}}})
	if classFinding[0].Severity != rules.SeverityMedium || !strings.Contains(classFinding[0].Fix, `--paths "com.acme.Session"`) {
		t.Errorf("class finding = %+v", classFinding[0])
	}
}

type fakeIssueUpserter struct {
	machine  string
	findings []store.FindingInput
	err      error
}

func (f *fakeIssueUpserter) UpsertIssues(_ context.Context, machineUUID, _ string, findings []store.FindingInput) ([]string, error) {
	f.machine, f.findings = machineUUID, findings
	if f.err != nil {
		return nil, f.err
	}
	return []string{"issue-1"}, nil
}

func TestFileLeakIssues(t *testing.T) {
	up := &fakeIssueUpserter{}
	findings := []rules.Finding{{RuleID: leakSuspectRuleID, Severity: rules.SeverityHigh, Subject: "heap leak suspect: X", Message: "m", Fix: "f"}}
	ids, err := fileLeakIssues(context.Background(), up, "host-1", findings)
	if err != nil || len(ids) != 1 || up.machine != "host-1" || len(up.findings) != 1 || up.findings[0].RuleID != leakSuspectRuleID {
		t.Fatalf("ids=%v err=%v upserter=%+v", ids, err, up)
	}
	boom := errors.New("boom")
	if _, err := fileLeakIssues(context.Background(), &fakeIssueUpserter{err: boom}, "h", findings); !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}
