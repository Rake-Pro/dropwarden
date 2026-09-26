//go:build windows

package update

import (
	"time"

	"golang.org/x/sys/windows"
)

// WaitExit blocks until the process with pid has exited or timeout passes.
// It returns true when the process is gone (including "never existed").
func WaitExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // no such process (or no access, in which case we cannot wait anyway)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}
