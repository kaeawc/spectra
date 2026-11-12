package toolchain

import (
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

// execRunner is the default CmdRunner that shells out for real.
func execRunner(name string, args ...string) ([]byte, error) {
	return proc.BoundedOutput(name, args...)
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
