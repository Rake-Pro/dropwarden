//go:build darwin

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// launchChrome starts Chrome as an ordinary child process.
//
// verify live: a Chrome started this way (rather than via `open -a`) does not
// go through LaunchServices activation, so it should come up without pulling
// focus from the frontmost app. Confirm on a real desktop; if it does steal
// focus, the fix is a small LSUIElement helper or an AppleScript
// "set visible of process to false" pass after launch.
func launchChrome(path string, flags []string) (*launchedChrome, error) {
	cmd := exec.Command(path, flags...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("browser: start chrome: %w", err)
	}

	var once sync.Once
	kill := func() error {
		var err error
		once.Do(func() {
			if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
				err = fmt.Errorf("browser: kill chrome: %w", kerr)
			}
			_ = cmd.Wait()
		})
		return err
	}
	return &launchedChrome{pid: cmd.Process.Pid, kill: kill}, nil
}

// shiftWindow is a no-op on macOS.
//
// verify live: moving another app's window requires either the Accessibility
// API (which needs a TCC grant the user must click through) or an AppleScript
// "set position of window 1" via System Events. Both are future improvements;
// the dim overlay covers burn-in in the meantime.
func shiftWindow(pid, dx, dy int) error { return nil }
