package uninstall

import (
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

const finderDetritus = "/x/App.app: resource fork, Finder information, or similar detritus not allowed\nIn subcomponent: /x/App.app/Contents/Frameworks/F.framework\n"

func TestReadTeamIDFinderMetadata(t *testing.T) {
	display := proc.Response{Result: proc.Result{Stderr: []byte("Identifier=x\nTeamIdentifier=ABCDE12345\n")}}
	strict := func(code int, msg string) proc.Response {
		return proc.Response{Result: proc.Result{ExitCode: code, Stderr: []byte(msg)}}
	}
	cases := []struct {
		name   string
		strict proc.Response
		plain  proc.Response
		want   string
		plainN int
	}{
		{"detritus only, plain passes", strict(1, finderDetritus), proc.Response{}, "ABCDE12345", 1},
		{"detritus only, plain fails", strict(1, finderDetritus), strict(1, "invalid signature\n"), "", 1},
		{"other strict failure is not retried", strict(1, "invalid signature (code or signature have been modified)\n"), proc.Response{}, "", 0},
		{"detritus plus other failure is not retried", strict(1, finderDetritus+"/x/App.app: a sealed resource is missing\n"), proc.Response{}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := proc.NewFake().
				On(proc.MatchExact("codesign", "--verify", "--strict", "/x/App.app"), tc.strict).
				On(proc.MatchExact("codesign", "--verify", "/x/App.app"), tc.plain).
				On(proc.MatchPrefix("codesign", "-dv"), display)
			if got := readTeamID(context.Background(), fake, "/x/App.app"); got != tc.want {
				t.Fatalf("team id = %q, want %q (calls %v)", got, tc.want, fake.Calls())
			}
			plain := 0
			for _, c := range fake.Calls() {
				if c.Name == "codesign" && strings.Join(c.Args, " ") == "--verify /x/App.app" {
					plain++
				}
			}
			if plain != tc.plainN {
				t.Fatalf("plain verify ran %d times, want %d", plain, tc.plainN)
			}
		})
	}
}
