package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindChromeOverrideAccepted(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake chrome: %v", err)
	}

	got, err := FindChrome(bin)
	if err != nil {
		t.Fatalf("FindChrome(%q): %v", bin, err)
	}
	if got != bin {
		t.Fatalf("FindChrome = %q, want the override %q", got, bin)
	}
}

func TestFindChromeOverrideMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := FindChrome(missing); err == nil {
		t.Fatal("FindChrome accepted a nonexistent override")
	}
}

func TestFindChromeOverrideDirectory(t *testing.T) {
	if _, err := FindChrome(t.TempDir()); err == nil {
		t.Fatal("FindChrome accepted a directory as the chrome path")
	}
}

func TestFindChromeOverrideNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable mode bit on Windows")
	}
	f := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := FindChrome(f); err == nil {
		t.Fatal("FindChrome accepted a non-executable override")
	}
}

// With nothing on PATH and no well-known install present, the error has to tell
// the user where it looked so they can set browser.chrome_path.
func TestFindChromeNotFoundListsLocations(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	// Windows well-known paths are derived from these.
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramFiles(x86)", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())

	_, err := FindChrome("")
	if err == nil {
		t.Skip("a real Chrome install is present at a well-known path")
	}
	msg := err.Error()
	if !strings.Contains(msg, "looked in:") || !strings.Contains(msg, "chrome_path") {
		t.Fatalf("unhelpful not-found error: %v", err)
	}
	for _, name := range pathNames() {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not mention PATH candidate %q: %v", name, err)
		}
	}
}
