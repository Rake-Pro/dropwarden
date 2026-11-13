module github.com/rake-pro/dropwarden

go 1.27.4

// No Multipath TCP listeners: nothing on the fleet dials MPTCP, and a listening
// MPTCP socket was the precondition of the CVE-2026-98432 host-kernel exploit.
godebug multipathtcp=0

require (
	github.com/go-rod/rod v0.116.2
	github.com/rs/zerolog v1.35.1
	github.com/wailsapp/wails/v3 v3.1.0
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.50.0
	modernc.org/sqlite v1.61.1
)

require (
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/ysmood/fetchup v0.2.3 // indirect
	github.com/ysmood/goob v0.4.0 // indirect
	github.com/ysmood/got v0.40.0 // indirect
	github.com/ysmood/gson v0.7.3 // indirect
	github.com/ysmood/leakless v0.9.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
