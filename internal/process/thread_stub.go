//go:build !darwin || !cgo

package process

func collectThreadCounts(_ []Info) map[int]int {
	return nil
}
