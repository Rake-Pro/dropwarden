//go:build darwin || linux

package main

import "syscall"

// detachedProcAttr starts the update helper in its own session so it
// outlives this process (and any signal sent to its process group).
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
