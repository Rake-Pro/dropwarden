# Wails v3 notes (beta.14)

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
- Tray: `app.SystemTray.New()` -> `SetLabel`, `SetTooltip`, `SetMenu(*Menu)`,
  `AttachWindow(win)`, `OnClick(func())`, `SetIcon`/`SetDarkModeIcon`.
- Menu: `application.NewMenu()`, `m.Add(label) *MenuItem`, `m.AddSeparator()`.
- `MenuItem.OnClick(func(*application.Context))` - note the `*Context` argument;
  a `func()` will not compile (the documented gotcha).
- Events: `app.Event.Emit(name string, data ...any)`.
- `app.Run() error`, `app.Quit()`, window `Show()/Hide()`.

## Bindings + live data

The frontend renders from sample data until bindings are wired (see the header
of `frontend/api.js`). On the dev machine:

1. `wails3 generate bindings -d frontend/bindings` (or `task generate`).
2. In a small bridge (or in `api.js`), import the generated services and set
   `window.dw = {CampaignService, QueueService, WatcherService, HistoryService,
   SettingsService}`.
3. Subscribe to engine events with `@wailsio/runtime` `Events.On(name, cb)` and
   call the `api.onEvent` seam.

Add `frontend/bindings` to the `//go:embed` list in `frontend/embed.go` before a
packaged (non-dev) build so the bindings ship.

## Known risks to re-check on beta.14

- Start-hidden window + Events + FS assets froze in an alpha (issue #4103). If
  the window freezes at startup, create it visible off-screen, wait for the
  first DomReady, then move-and-hide as a workaround.
- Beta cadence is fast; the version is pinned in `go.mod`. Upgrade deliberately
  and re-verify the APIs above.
- Live tray menu-item label updates are done from a goroutine
  (`updateTrayStatus`); confirm they reflect on Windows/macOS. If not, move the
  update onto a UI-thread callback.
