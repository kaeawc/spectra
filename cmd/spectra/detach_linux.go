//go:build linux

package main

import "syscall"

// detachedSysProcAttr starts the daemon in a new session so it survives the
// parent shell exiting (equivalent to the Darwin path).
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
