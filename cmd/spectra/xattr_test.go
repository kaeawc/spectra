package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/kaeawc/spectra/internal/xattrinspect"
)

func TestRunXattrInspectExitStatus(t *testing.T) {
	deps := xattrinspect.Deps{
		Run: func(...string) ([]byte, error) { return nil, nil },
		Files: func(root string) ([]string, error) {
			if root == "/nonexistent" {
				return nil, fmt.Errorf("stat %s: no such file or directory", root)
			}
			return []string{root}, nil
		},
		Exists: func(string) bool { return false },
	}
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"ok", []string{"/bin/ls"}, 0},
		{"missing", []string{"/nonexistent"}, 1},
		{"missing json", []string{"--json", "/nonexistent"}, 1},
		{"one of several missing", []string{"/bin/ls", "/nonexistent"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := runXattrInspectWithIO(tc.args, &stdout, &stderr, deps); got != tc.want {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s", got, tc.want, stdout.String())
			}
		})
	}
}
