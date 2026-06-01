//go:build !darwin || !cgo

package memstate

// Collect returns ErrNotSupported outside the macOS cgo build.
func Collect() (MemoryState, error) {
	return CollectWithOptions(Options{})
}

// CollectWithOptions returns ErrNotSupported outside the macOS cgo build.
func CollectWithOptions(Options) (MemoryState, error) {
	return MemoryState{}, ErrNotSupported
}
