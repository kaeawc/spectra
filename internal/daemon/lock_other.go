//go:build !darwin && !linux

package daemon

import (
	"github.com/kaeawc/spectra/internal/peercred"
	"os"
)

func lockFile(_ *os.File) error { return peercred.ErrUnsupported }
func unlockFile(_ *os.File)     {}
func lockHeld(error) bool       { return false }
