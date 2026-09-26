//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedProcAttr starts the console-subsystem update helper
// (dropwarden-cli.exe) without flashing a console window, in its own process
// group so it outlives this GUI process.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
