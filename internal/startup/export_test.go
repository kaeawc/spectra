package startup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func TestParseDumpBTMKeepsFlags(t *testing.T) {
	recs := ParseDumpBTM([]byte(readBTMFixture(t)))
	flags := map[string][]string{}
	for _, r := range recs {
		flags[r.Identifier] = r.Flags
	}
	if got := strings.Join(flags["16.com.example.vpnd"], ","); got != "legacy,curated" {
		t.Fatalf("vpnd flags = %q", got)
	}
	if f, ok := flags["2.com.example.meet"]; !ok || f == nil || len(f) != 0 {
		t.Fatalf("empty Flags line must give a non-nil empty slice, got %#v", f)
	}
	if !HasBTMRecordHeader([]byte(readBTMFixture(t))) || HasBTMRecordHeader([]byte("garbage")) {
		t.Fatal("record header detection")
	}
	missing := ParseDumpBTM([]byte("Records for UID 0 : x\n #1:\n Identifier: 16.com.example.x\n Type: legacy daemon\n"))
	if len(missing) != 1 || missing[0].Flags != nil {
		t.Fatalf("missing Flags line = %#v", missing)
	}
}

func TestSystemScopeRefusalPrintsSudoCommandsAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>com.example.d</string><key>Program</key><string>/opt/d</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "com.example.d.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := proc.NewFake().
		On(proc.MatchPrefix(launchctlPath, "print-disabled"), proc.Response{Result: proc.Result{Stdout: []byte("disabled services = {\n}\n")}}).
		On(proc.MatchPrefix(launchctlPath, "print"), proc.Response{Result: proc.Result{Stdout: []byte("x = {\n\tservices = {\n\t}\n}\n")}})
	opts := Options{
		OS: hostos.Darwin, Runner: fake, EUID: func() int { return 501 }, Getenv: func(string) string { return "" },
		LookupHome: func(int) (string, error) { return t.TempDir(), nil }, LookupUser: func(uint32) string { return "root" },
		PlistDirs: []PlistDir{{Path: dir, Scope: ScopeSystem, Kind: KindLaunchDaemon}}, AppDirs: []string{}, BTMStoreDir: t.TempDir(),
	}
	res, err := SystemScopeRefusal(context.Background(), opts, "enable", "com.example.d")
	if err != nil {
		t.Fatal(err)
	}
	want := "sudo launchctl enable system/com.example.d;sudo launchctl bootstrap system " + filepath.Join(dir, "com.example.d.plist")
	if !res.Refused || res.Reason != RefuseSystem || strings.Join(res.Suggested, ";") != want {
		t.Fatalf("res = %+v", res)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] != "print" && c.Args[0] != "print-disabled" {
			t.Fatalf("ran %v", c)
		}
	}
	res, _ = SystemScopeRefusal(context.Background(), opts, "disable", "com.example.none")
	if res.Reason != RefuseUnknown {
		t.Fatalf("unknown = %+v", res)
	}
}
