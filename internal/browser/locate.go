package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindChrome resolves the Chrome executable to launch. A non-empty override
// (config browser.chrome_path) wins outright and must point at an existing
// executable file; otherwise the well-known per-OS install locations are probed
// before falling back to PATH. The error names every location that was tried so
// a user with an unusual install knows exactly what to put in chrome_path.
func FindChrome(override string) (string, error) {
	if override != "" {
		if err := executableAt(override); err != nil {
			return "", fmt.Errorf("browser: chrome_path %s: %w", override, err)
		}
		return override, nil
	}

	tried := make([]string, 0, 12)
	for _, p := range wellKnownPaths() {
		if p == "" {
			continue
		}
		tried = append(tried, p)
		if executableAt(p) == nil {
			return p, nil
		}
	}
	for _, name := range pathNames() {
		tried = append(tried, name+" (PATH)")
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	// Last resort: Microsoft Edge is Chromium and ships with every Windows
	// install, so a machine without Chrome can still run (the caller logs it).
	for _, p := range edgePaths() {
		tried = append(tried, p)
		if executableAt(p) == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("browser: no Chrome or Chromium found; looked in: %s (set browser.chrome_path)",
		strings.Join(tried, ", "))
}

// executableAt reports whether path is an existing, runnable file. Windows has
// no executable mode bit, so there existence plus "not a directory" is the whole
// check.
func executableAt(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("not executable")
	}
	return nil
}

// wellKnownPaths lists the default install locations for the current OS, most
// preferred first. Chrome always outranks Chromium.
func wellKnownPaths() []string {
	switch runtime.GOOS {
	case "windows":
		rel := filepath.Join("Google", "Chrome", "Application", "chrome.exe")
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if root := os.Getenv(env); root != "" {
				out = append(out, filepath.Join(root, rel))
			}
		}
		return out
	case "darwin":
		out := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		if home, err := os.UserHomeDir(); err == nil {
			out = append(out,
				filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
				filepath.Join(home, "Applications/Chromium.app/Contents/MacOS/Chromium"),
			)
		}
		return out
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/opt/google/chrome/chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
			"/usr/local/bin/google-chrome",
			"/usr/local/bin/chromium",
		}
	}
}

// edgePaths lists Microsoft Edge's standard install locations, probed only
// after every Chrome/Chromium candidate. Windows only; empty elsewhere.
func edgePaths() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	rel := filepath.Join("Microsoft", "Edge", "Application", "msedge.exe")
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if root := os.Getenv(env); root != "" {
			out = append(out, filepath.Join(root, rel))
		}
	}
	return out
}

// IsEdge reports whether a resolved browser path is the Microsoft Edge
// fallback rather than Chrome/Chromium.
func IsEdge(path string) bool {
	return strings.EqualFold(filepath.Base(path), "msedge.exe")
}

// pathNames lists the binary names to look for on PATH once the well-known
// locations have come up empty.
func pathNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"chrome.exe"}
	case "darwin":
		return []string{"google-chrome", "chromium"}
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	}
}
