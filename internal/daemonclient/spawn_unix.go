//go:build darwin || linux

package daemonclient

import (
	"os/exec"
	"syscall"
)

func setDetached(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
