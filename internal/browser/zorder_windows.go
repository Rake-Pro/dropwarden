//go:build windows

package browser

import (
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Extra SetWindowPos flags for the z-order push (the move-only ones live in
// launch_windows.go). SWP_NOACTIVATE is mandatory on every SetWindowPos call in
// this codebase.
const (
	swpNoMove        = 0x0002
	swpNoOwnerZOrder = 0x0200

	gwOwner    = 4 // GetWindow GW_OWNER
	hwndBottom = 1 // HWND_BOTTOM

	// chromeFrameClass prefixes Chrome's top-level browser frame window class
	// (Chrome_WidgetWin_1 today; the suffix has changed across versions).
	chromeFrameClass = "Chrome_WidgetWin"
)

var procGetWindow = user32.NewProc("GetWindow")

var (
	// enumMu serializes visibleTopWindows: the EnumWindows callback is created
	// ONCE (the Go runtime caps the number of callbacks a process may ever
	// create, so a fresh closure per call would eventually panic) and reports
	// into the package-level enum state below, which only the goroutine holding
	// enumMu touches (EnumWindows runs the callback synchronously on the
	// calling thread).
	enumMu     sync.Mutex
	enumCBOnce sync.Once
	enumCB     uintptr
	enumPID    uint32
	enumFound  []windows.HWND
)

// enumVisibleTop is the shared EnumWindows callback: it keeps visible top-level
// windows belonging to enumPID, in z-order (topmost first).
func enumVisibleTop(hwnd windows.HWND, _ uintptr) uintptr {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid != enumPID {
		return 1 // keep enumerating
	}
	if windows.IsWindowVisible(hwnd) {
		enumFound = append(enumFound, hwnd)
	}
	return 1 // never stop early, so an EnumWindows failure is always a real one
}

// visibleTopWindows returns every visible top-level window owned by pid, in
// z-order (topmost first). Empty with a nil error means the process has none
// mapped (not yet, or already gone).
func visibleTopWindows(pid uint32) ([]windows.HWND, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumCBOnce.Do(func() { enumCB = windows.NewCallback(enumVisibleTop) })

	enumPID, enumFound = pid, nil
	err := windows.EnumWindows(enumCB, nil)
	found := enumFound
	enumFound = nil
	if err != nil {
		return nil, fmt.Errorf("browser: enumerate windows: %w", err)
	}
	return found, nil
}

// isChromeFrame reports whether hwnd is an unowned top-level Chrome frame. Owned
// popups (menus, tooltips, bubbles) have a GW_OWNER and are skipped; hidden
// helper windows (Chrome_WidgetWin_0 and friends) were already dropped by the
// visibility filter.
func isChromeFrame(hwnd windows.HWND) bool {
	if owner, _, _ := procGetWindow.Call(uintptr(hwnd), gwOwner); owner != 0 {
		return false
	}
	var buf [256]uint16
	n, err := windows.GetClassName(hwnd, &buf[0], int32(len(buf)))
	if err != nil || n <= 0 {
		return false
	}
	return strings.HasPrefix(windows.UTF16ToString(buf[:n]), chromeFrameClass)
}

// sendToBottom moves every visible top-level Chrome frame window owned by pid to
// the bottom of the z-order (HWND_BOTTOM), without activating, moving or
// resizing it. It returns how many windows were pushed. Zero with a nil error
// means the window is not mapped yet (or already gone); the caller re-asserts
// periodically anyway.
func sendToBottom(pid uint32) (int, error) {
	wins, err := visibleTopWindows(pid)
	if err != nil {
		return 0, err
	}
	var found []windows.HWND
	for _, hwnd := range wins {
		if isChromeFrame(hwnd) {
			found = append(found, hwnd) // a profile can own more than one frame
		}
	}

	pushed := 0
	var lastErr error
	for _, hwnd := range found {
		ret, _, callErr := procSetWindowPos.Call(
			uintptr(hwnd), hwndBottom,
			0, 0, 0, 0,
			uintptr(swpNoMove|swpNoSize|swpNoActivate|swpNoOwnerZOrder),
		)
		if ret == 0 {
			lastErr = fmt.Errorf("browser: send window to bottom: %w", callErr)
			continue
		}
		pushed++
	}
	if pushed == 0 && lastErr != nil {
		return 0, lastErr
	}
	return pushed, nil
}
