//go:build !darwin

package process

func systemMemoryBytes() uint64 {
	return 0
}
