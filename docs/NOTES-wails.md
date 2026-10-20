# Wails v3 notes (beta.14)

> SUPERSEDED 2026-08-30: the "Linux is a stub" paragraph below predates the
> GTK3 backend. Linux now builds the real GUI via `-tags gtk3` (GTK3 +
> webkit2gtk-4.1), not a stub - see README.md. Left as-is
> for history; do not treat it as current.

Verified against `github.com/wailsapp/wails/v3 v3.0.0-beta.14` via `go doc`. The
GUI (`cmd/dropwarden`, build-tagged `windows || darwin`) cross-compiles for
Windows from any host (Wails Windows is CGO-free), which is how it is verified
in CI-less development: `GOOS=windows go build ./...`. macOS is verified on a
Mac (it uses CGO/WKWebView). Linux is a stub (`stub_linux.go`) because v3 needs
GTK4 + WebKitGTK there.

## Confirmed APIs used

- `application.New(application.Options{...})` returns `*application.App`.
- `Options.Services []application.Service`, built with
  `application.NewService(ptr)`.
- `Options.Assets application.AssetOptions{Handler: http.Handler}` - we pass
  `webui.Handler()` (embedded frontend via `http.FileServerFS`).
- `Options.SingleInstance *application.SingleInstanceOptions{UniqueID: "..."}`.
- Windows: `app.Window.NewWithOptions(application.WebviewWindowOptions{...})`
  with `Hidden`, `BackgroundColour application.RGBA`, `Windows:
  application.WindowsWindow{Theme: application.Dark}`, `Width/Height/URL/Title`.
- Tray: `app.SystemTray.New()` -> `SetIcon([]byte)` (44x44 PNG from
  `cmd/dropwarden/icons`, swapped per status level), `SetTooltip`,
  `SetMenu(*Menu)`, `AttachWindow(win)`, `OnClick(func())`. No `SetLabel`:
  the icon is the mark. `app.SetIcon` sets the macOS Dock icon, used for the
  same status dot, and is only called on darwin (`setAppIcon`): Windows
  ignores it, and Linux gtk3 only seeds future windows and leaks the old
  pixbuf. Wails keeps the pointer to the icon bytes and reads them after the
  call, so the icons package caches every PNG for the process lifetime.
- Menu: `application.NewMenu()`, `m.Add(label) *MenuItem`, `m.AddSeparator()`.
- macOS menu bar: `menu.AddRole(application.AppMenu/EditMenu/WindowMenu)` +
  `app.Menu.Set(menu)` (`cmd/dropwarden/appmenu_darwin.go`). Off macOS a set
  menu renders inside the window, so it is a no-op there.
- Close-to-hide: `win.RegisterHook(events.Common.WindowClosing, ...)` with
  `e.Cancel()` + `win.Hide()`. Hooks run before Wails' own closing listener
  (which destroys the window); plain `OnWindowEvent` listeners run after it
  and cannot stop it. `Options.Windows/Linux.DisableQuitOnLastWindowClosed`
  is the belt to that suspender. Quit still fires the hook (window hides),
  but `App.Quit` ends the process without waiting on windows, so it is safe.
- `MenuItem.OnClick(func(*application.Context))` - note the `*Context` argument;
  a `func()` will not compile (the documented gotcha).
- Events: `app.Event.Emit(name string, data ...any)`.
- `app.Run() error`, `app.Quit()`, window `Show()/Hide()`.

## Live data: HTTP API, not generated bindings

The frontend does NOT use `wails3 generate bindings`. Instead the asset handler
is a mux that serves `/api/*` from `internal/service/httpapi.go` (JSON) and
everything else from the embedded frontend. The frontend calls it with plain
`fetch` and polls `/api/state` every ~5s. This works identically under
`go run ./cmd/dropwarden`, `wails3 dev`, and a plain browser, and needs no
generated code. Engine events are still emitted via `app.Event.Emit` (see
`emitter.go`) for future push-based updates, but the UI does not depend on them.

If you later prefer typed bindings, they can be added alongside; the HTTP API is
the supported path for MVP.

## Known risks to re-check on beta.14

- Start-hidden window + Events + FS assets froze in an alpha (issue #4103). If
  the window freezes at startup, create it visible off-screen, wait for the
  first DomReady, then move-and-hide as a workaround.
- Beta cadence is fast; the version is pinned in `go.mod`. Upgrade deliberately
  and re-verify the APIs above.
- Live tray menu-item label updates are done from a goroutine
  (`updateTrayStatus`); confirm they reflect on Windows/macOS. If not, move the
  update onto a UI-thread callback.
