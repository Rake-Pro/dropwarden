//go:build darwin || linux

package power

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
)

// procHolder runs the OS's own inhibitor tool as a child in its own process
// group and kills the group to release. macOS: caffeinate -i (idle sleep
// only, display may sleep) tied to our pid so a crash never leaves it
// behind. Linux: systemd-inhibit sleep:idle around a sleeping child.
type procHolder struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

func newHolder() Holder { return &procHolder{} }

func inhibitCommand() (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid())), nil
	case "linux":
		if _, err := exec.LookPath("systemd-inhibit"); err != nil {
			return nil, errors.New("systemd-inhibit not found")
		}
		return exec.Command("systemd-inhibit", "--what=sleep:idle", "--who=dropwarden",
			"--why=watching a drop", "--mode=block", "sleep", "infinity"), nil
	}
	return nil, errors.New("no sleep inhibitor on " + runtime.GOOS)
}

func (h *procHolder) Hold() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd != nil {
		return nil
	}
	cmd, err := inhibitCommand()
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cmd = cmd
	go func() { _ = cmd.Wait() }()
	return nil
}

func (h *procHolder) Release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd == nil {
		return
	}
	// Negative pid = the whole group, so systemd-inhibit's sleep child goes
	// with it instead of lingering.
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	h.cmd = nil
}
