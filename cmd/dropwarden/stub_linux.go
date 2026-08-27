//go:build linux

// The dropwarden GUI targets Windows and macOS (WebView2 / WKWebView). Wails v3
// on Linux requires GTK4 + WebKitGTK, which is out of scope for this app, so the
// Linux build is a stub that points at the CLI harness. This also keeps
// `go build ./...` green on Linux CI/dev machines.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "dropwarden GUI is built for Windows and macOS only.")
	fmt.Fprintln(os.Stderr, "On Linux, use the headless harness: go run ./cmd/dropwarden-cli <command>")
	os.Exit(1)
}
