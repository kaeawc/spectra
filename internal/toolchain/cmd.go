package toolchain

import (
	"os/exec"
	"strings"
)

// execRunner is the default CmdRunner that shells out for real.
func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// lines splits output into trimmed non-empty lines.
func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
