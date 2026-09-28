//go:build darwin || linux

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func unlockFile(f *os.File)     { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func lockHeld(err error) bool   { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }
