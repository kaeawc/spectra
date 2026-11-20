package classify

import (
	"debug/macho"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/codesignout"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func TestFinderMetadataPlainVerify(t *testing.T) {
	files := newFakeFS()
	files.add("/opt/x", buildMachO(macho.CpuArm64))
	detritus := proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte("/opt/x: " + codesignout.DetritusComplaint + "\nIn subcomponent: /opt/x/F\n")}}
	invalid := proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte("/opt/x: invalid signature\n")}}
	cases := []struct {
		name      string
		strict    proc.Response
		plain     proc.Response
		validated bool
		plainRuns int
		persisted bool
		ev        string
	}{
		{"plain passes", detritus, proc.Response{}, true, 1, true, "has Finder metadata (codesign --strict rejects it); code signature verified without --strict"},
		{"plain fails", detritus, invalid, false, 1, true, "rejected extended attributes or resource forks"},
		{"plain crashed with -1", detritus, proc.Response{Result: proc.Result{ExitCode: -1}}, false, 1, false, "could not run"},
		{"plain usage error", detritus, proc.Response{Result: proc.Result{ExitCode: 2}}, false, 1, false, "could not run"},
		{"non-detritus failure is not retried", invalid, proc.Response{}, false, 0, true, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := thirdPartyVerifyFake(devIDCodesign, tc.strict, tc.plain)
			store := newMemStore()
			r := classifyOne(t, New(Options{OS: hostos.Darwin, FS: files, Runner: fake, Store: store}), Input{UID: 501, Command: "x", ExecutablePath: "/opt/x"})
			if r.Signing == nil || r.Signing.Validated != tc.validated {
				t.Fatalf("signing=%+v, want validated=%v", r.Signing, tc.validated)
			}
			if tc.validated && r.Signing.Status != SigningDeveloperID {
				t.Fatalf("status = %s", r.Signing.Status)
			}
			plain := 0
			for _, c := range fake.Calls() {
				if c.Name == "codesign" && strings.Join(c.Args, " ") == "--verify /opt/x" {
					plain++
				}
			}
			if plain != tc.plainRuns || (store.puts > 0) != tc.persisted {
				t.Fatalf("plain runs=%d puts=%d, want %d/persisted=%v", plain, store.puts, tc.plainRuns, tc.persisted)
			}
			if ev := strings.Join(r.Evidence, "\n"); !strings.Contains(ev, tc.ev) {
				t.Fatalf("evidence %q lacks %q", ev, tc.ev)
			}
		})
	}
}
