//go:build !darwin

package main

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	return nil
}
