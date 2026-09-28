//go:build !darwin && !linux

package daemonclient

import "os/exec"

func setDetached(_ *exec.Cmd) {}
