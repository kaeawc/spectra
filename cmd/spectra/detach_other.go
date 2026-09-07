//go:build !darwin && !linux

package main

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	return nil
}
