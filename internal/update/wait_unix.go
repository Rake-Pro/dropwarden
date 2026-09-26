//go:build !windows

package update

import (
	"os"
	"syscall"
	"time"
)

// WaitExit blocks until the process with pid has exited or timeout passes.
// It returns true when the process is gone (including "never existed").
func WaitExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.Signal(0)) != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
