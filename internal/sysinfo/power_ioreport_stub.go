//go:build !darwin || !arm64 || !cgo

package sysinfo

import "time"

// SampleSoCPower returns ErrUnsupportedHardware on platforms without
// Apple Silicon IOReport (non-darwin, Intel macs, cgo-disabled builds).
func SampleSoCPower(_ time.Duration) (SoCPower, error) {
	return SoCPower{}, ErrUnsupportedHardware
}
