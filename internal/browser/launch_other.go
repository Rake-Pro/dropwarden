//go:build !windows && !darwin

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// launchChrome starts Chrome as a child process in its OWN process group, so the
// whole Chrome tree can be killed together on shutdown. This is the Linux
// development path; the focus-stealing problem the Windows file solves does not
// apply the same way here, and X11/Wayland window placement is left to the
// --window-position flag. The process group plus the startup lock reap
// (profile_posix.go) are the stand-in for the Windows kill-on-close job object.
func launchChrome(path string, flags []string) (*launchedChrome, error) {
	cmd := exec.Command(path, flags...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browser: start chrome: %w", err)
	}

	var once sync.Once
	kill := func() error {
		var err error
		once.Do(func() {
			// Kill the process group (negative pid) so Chrome's helpers go too.
			if pgid, perr := syscall.Getpgid(cmd.Process.Pid); perr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
				err = fmt.Errorf("browser: kill chrome: %w", kerr)
			}
			// Reap the child so it does not linger as a zombie. A non-nil error
			// here just means Chrome exited non-zero, which is expected after a
			// kill.
			_ = cmd.Wait()
		})
		return err
	}
	return &launchedChrome{pid: cmd.Process.Pid, kill: kill}, nil
}

// shiftWindow is a no-op on Linux.
//
// verify live: burn-in shifting needs a compositor-specific implementation
// (EWMH _NET_MOVERESIZE_WINDOW on X11, nothing portable on Wayland). The Linux
// target is development only, so the dim overlay carries burn-in duty here.
func shiftWindow(pid, dx, dy int) error { return nil }
