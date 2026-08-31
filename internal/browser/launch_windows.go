//go:build windows

package browser

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SetWindowPos flags. Only the move is wanted: never activate the window, never
// change its z-order, never resize it.
const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
)

// jobObjectExtendedLimitInformation is the JobObjectInformationClass for
// SetInformationJobObject (winnt.h JOBOBJECTINFOCLASS value 9); x/sys/windows
// ships the struct but not this named constant.
const jobObjectExtendedLimitInformation = 9

var (
	jobOnce   sync.Once
	chromeJob windows.Handle
	jobErr    error
)

// killOnCloseJob returns a process-wide job object whose members Windows kills
// the instant the job handle closes - which the OS does automatically when
// dropwarden exits for ANY reason: clean quit, panic, or an outright kill. Every
// Chrome dropwarden launches joins it, so no watcher browser can outlive the app
// as an orphan. Orphans are the whole problem: a leftover Chrome keeps the
// exclusive profile's singleton lock, so the next launch is forwarded to the
// dead instance (opening stray about:blank windows) and never gets its own
// DevTools port. The handle is intentionally never closed for the app's
// lifetime; process teardown closes it and triggers the kill.
func killOnCloseJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = fmt.Errorf("browser: create job object: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(h, jobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.CloseHandle(h)
			jobErr = fmt.Errorf("browser: set job kill-on-close: %w", err)
			return
		}
		chromeJob = h
	})
	return chromeJob, jobErr
}

// launchChrome starts Chrome through CreateProcess rather than os/exec so the
// STARTF_USESHOWWINDOW + SW_SHOWNOACTIVATE hint can be passed in the startup
// info. That hint is what keeps the watcher window from stealing focus from
// whatever the user is doing when dropwarden starts or restarts the browser.
//
// The process starts SUSPENDED so it can be joined to the kill-on-close job
// (killOnCloseJob) before it runs a single instruction - a Chrome that never
// executes outside the job can never spawn a child that escapes it. Once
// assigned, the main thread is resumed.
//
// verify live: Chrome does not always honour the startup ShowWindow hint for
// its first browser window. If it still activates on a real desktop, the
// fallback is a ShowWindow(hwnd, SW_SHOWNOACTIVATE) call on the window found by
// findTopWindow once it appears.
func launchChrome(path string, flags []string) (*launchedChrome, error) {
	appName, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("browser: encode chrome path: %w", err)
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, flags...)))
	if err != nil {
		return nil, fmt.Errorf("browser: encode chrome command line: %w", err)
	}

	si := windows.StartupInfo{
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: uint16(windows.SW_SHOWNOACTIVATE),
	}
	si.Cb = uint32(unsafe.Sizeof(si))

	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		appName, cmdLine,
		nil, nil, false,
		windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_SUSPENDED,
		nil, nil,
		&si, &pi,
	)
	if err != nil {
		return nil, fmt.Errorf("browser: create chrome process: %w", err)
	}

	// Join the kill-on-close job before resuming. Best effort: if the job could
	// not be created, Chrome still runs - it just falls back to relying on the
	// explicit Kill() and the startup stale-lock sweep instead of the OS.
	if job, jerr := killOnCloseJob(); jerr == nil {
		_ = windows.AssignProcessToJobObject(job, pi.Process)
	}

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		return nil, fmt.Errorf("browser: resume chrome: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)

	proc := pi.Process
	var once sync.Once
	kill := func() error {
		var kerr error
		once.Do(func() {
			if err := windows.TerminateProcess(proc, 1); err != nil {
				kerr = fmt.Errorf("browser: terminate chrome: %w", err)
			}
			_ = windows.CloseHandle(proc)
		})
		return kerr
	}
	return &launchedChrome{pid: int(pi.ProcessId), kill: kill}, nil
}

// shiftWindow nudges Chrome's top-level window by (dx,dy) without activating it
// or changing its z-order, for burn-in avoidance.
//
// verify live: Chrome spawns several top-level windows per process (the browser
// frame plus hidden message-only helpers); findTopWindow takes the first
// visible one, which should be the browser frame but has not been confirmed
// against a real Chrome window list.
func shiftWindow(pid, dx, dy int) error {
	hwnd, err := findTopWindow(uint32(pid))
	if err != nil {
		return err
	}
	if hwnd == 0 {
		// Window not mapped yet (or already gone). Not an error: the caller is a
		// periodic burn-in tick that will try again.
		return nil
	}

	var r windows.Rect
	if ret, _, callErr := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r))); ret == 0 {
		return fmt.Errorf("browser: get window rect: %w", callErr)
	}

	ret, _, callErr := procSetWindowPos.Call(
		uintptr(hwnd),
		0,
		uintptr(int(r.Left)+dx),
		uintptr(int(r.Top)+dy),
		0, 0,
		uintptr(swpNoSize|swpNoZOrder|swpNoActivate),
	)
	if ret == 0 {
		return fmt.Errorf("browser: move window: %w", callErr)
	}
	return nil
}

// findTopWindow returns the first visible top-level window owned by pid, or 0
// if the process has none.
func findTopWindow(pid uint32) (windows.HWND, error) {
	var found windows.HWND
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var owner uint32
		_, _, _ = procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1 // keep enumerating
		}
		if visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); visible == 0 {
			return 1
		}
		found = hwnd
		return 0 // stop
	})

	// EnumWindows returns FALSE both on genuine failure and when the callback
	// asked to stop early, so its return value is not a usable error signal.
	// Whether a window was found is the real answer.
	_, _, _ = procEnumWindows.Call(cb, 0)
	return found, nil
}
