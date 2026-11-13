package storagestate

import "github.com/kaeawc/spectra/internal/proc"

func execRunner(name string, args ...string) ([]byte, error) {
	return proc.BoundedOutput(name, args...)
}
