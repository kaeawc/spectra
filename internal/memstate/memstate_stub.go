//go:build !darwin || !cgo

package memstate

import "github.com/kaeawc/spectra/internal/hostos"

// Collect gathers current host memory state on Linux and returns
// ErrNotSupported elsewhere outside the macOS cgo build.
func Collect() (MemoryState, error) {
	return CollectWithOptions(Options{})
}

// CollectWithOptions gathers current host memory state with testable options.
func CollectWithOptions(opts Options) (MemoryState, error) {
	if hostos.Current() == hostos.Linux {
		return collectLinux(opts)
	}
	return MemoryState{}, ErrNotSupported
}
