package sysinfo

import "github.com/kaeawc/spectra/internal/proc"

// CmdRunner abstracts exec.Command for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return proc.BoundedOutput(name, args...)
}
