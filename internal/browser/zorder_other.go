//go:build !windows

package browser

import "errors"

// sendToBottom is Windows-only: macOS and Linux have no portable, permission-free
// way to reorder another process's window. Callers log this once and stop.
func sendToBottom(pid uint32) (int, error) { return 0, errors.ErrUnsupported }
