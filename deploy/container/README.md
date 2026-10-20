# dropwarden container (headless service)

`cmd/dropwarden-serve` on a minimal Xvfb + Chrome base, for running the watcher on a
headless node instead of a logged-in PC. Same runtime as the desktop app (profiles,
live reconcile, one real headful Chrome per profile); no tray, no windows, no desktop.
Nothing here has run on real hardware yet: see the verify-live gates below.

## What is in the image

| Piece | Source | Notes |
| --- | --- | --- |
| Base | `ghcr.io/rake-pro/cluster-chrome:<tag>` | tini + Xvfb on `DISPLAY=:1`, uid 1000, home `/home/chrome`, `/usr/local/bin/chrome-gpu` (google-chrome-stable + GPU flags). No window manager, GTK or VNC. Tag in the Dockerfile is a placeholder to bump |
| Service | `cmd/dropwarden-serve`, `-tags production`, `CGO_ENABLED=0` | Dashboard + JSON API over HTTP, `GET /healthz` |
| CLI | `cmd/dropwarden-cli`, same flags | `doctor`, `report` etc. via `kubectl exec` |
| Build | `golang:1.27.2-trixie` stage | Pure Go: sqlite is `modernc.org/sqlite` |

- Image name: `ghcr.io/rake-pro/dropwarden:vX.Y.Z` (private GHCR), built by
  `.github/workflows/release.yml` for every release (tag = git tag), `ci.yml` for every dev push (`dev-<sha>`, plus a floating `dev`), `container.yml` on dispatch to rebuild an existing tag.
- The base's `USER` and `ENTRYPOINT` are kept; the image only adds the two binaries and `CMD`.
- The self-updater is off in serve mode: the image is the update. `/api/state`
  reports `host.updates: false` and the dashboard hides the update banner and buttons.

## Flags and environment

| Name | Default in image | Meaning |
| --- | --- | --- |
| `-listen` | `0.0.0.0:8080` | HTTP address (binary default `127.0.0.1:8080`) |
| `-chrome-path` | `/usr/local/bin/chrome-gpu` | Chrome to launch when `browser.chrome_path` in config.yaml is empty. A configured value wins and is never overwritten |
| `-config` | unset | Config file; same as `DROPWARDEN_CONFIG` |
| `DROPWARDEN_HOME` | `/home/chrome/dropwarden` | App root: config.yaml, per-profile sqlite DBs, Chrome profiles, log |
| `DROPWARDEN_CONFIG` | unset | Config file override (default `$DROPWARDEN_HOME/config.yaml`) |

- Logs are JSON lines on stderr (plus the rotating file when `logging.file` is on).
- `SIGTERM` / `SIGINT`: HTTP server stops, then every engine and Chrome is closed.

## Volumes and ports

| Item | Value | Why |
| --- | --- | --- |
| Volume | `/home/chrome` (at least `/home/chrome/dropwarden`) | Logins (Chrome profiles), queue, history. Lost on restart without it |
| `/dev/shm` | tmpfs >= 1 GiB | Chrome tab crashes on heavy pages otherwise |
| Port | `8080/tcp` | Dashboard, API, remote-login WebSocket |
| Probe | `GET /healthz` -> 200 `ok` | Liveness/readiness once the HTTP server is up |

- There is no auth in dropwarden-serve. Put the port behind SSO at the proxy; anyone who
  reaches it can drive the logged-in Chrome.
- The remote-login WebSocket checks that `Origin` matches the `Host` it was served on, so
  the proxy must pass the original `Host` through (or keep both the same).

## Remote login

| Step | What happens |
| --- | --- |
| 1 | Dashboard card shows "not signed in"; press Log in. Chrome comes up parked on the login page |
| 2 | Press Open remote login (shown only when `/api/state` `host.remote` is true) |
| 3 | The view opens `GET /api/remote/{profile}/{platform}/ws`: CDP `Page.startScreencast` JPEG frames are drawn on a canvas |
| 4 | Mouse (move, buttons, wheel) and keys go back as JSON events, replayed with `Input.dispatchMouseEvent` / `dispatchKeyEvent`; printable text and Ctrl+V paste use `Input.insertText` |
| 5 | Press Login check (or I'm signed in on the card); a confirmed login starts the watcher |

- One mirror per (profile, platform): a second view gets 409 until the first closes.
- Closing the view (or the socket) stops the screencast.
- Nothing in the mirror activates the tab or focuses the window.
- Queue buttons (Link account, Claim on Twitch, Claim on Kick) open the page in your
  own browser: the service's Chrome has no screen to show a tab on. Log in opens the
  remote login view. `POST /api/open` is refused in this mode.

## Chrome sandbox, GPU, seccomp

- None of these are set by this image: they come from the base image and the pod spec.
- dropwarden never passes `--no-sandbox` itself (`internal/browser/flags.go`). If Chrome
  exits with "No usable sandbox!", fix it at the host/pod level (unprivileged user
  namespaces, an AppArmor profile with `userns`, or a seccomp profile that allows
  Chrome's namespace setup). `browser.extra_flags: ["--no-sandbox"]` in config.yaml is
  the last resort.
- GPU: pass the render node (`/dev/dri/renderD*`) as the base image documents; without it
  Chrome's WebGL renderer is SwiftShader, a fingerprint change.

## Verify-live gates (owed before trusting any drop to it)

| # | Gate | Where to look |
| --- | --- | --- |
| 1 | `navigator.webdriver === false` in the dedicated Chrome | no in-app check yet: a one-off CDP eval inside the pod (the mirror shows page content only, no address bar or DevTools) |
| 2 | WebGL renderer names the real GPU driver, not SwiftShader | same: a one-off CDP eval (`WEBGL_debug_renderer_info`) inside the pod |
| 3 | Campaigns page populated after login | dashboard Queue, Debugging page |
| 4 | First `claim: gql` line in the log | container stderr |
| 5 | Minute-watched credit actually accrues on one drop | dashboard card progress |
| 6 | Remote login: frames arrive and input lands, for Twitch and for Kick (Kick is a second tab in the shared window) | remote login view |
| 7 | No `systemd-inhibit` in the image: the sleep hold logs one warning and the watcher keeps running | container stderr |

## Local build (any docker host)

```
docker build -f deploy/container/Dockerfile --build-arg VERSION=0.0.0-local -t dropwarden:local .
docker run --rm -p 8080:8080 --shm-size=1g --device /dev/dri \
  -v "$PWD/.container-home:/home/chrome" dropwarden:local
```

- Open http://localhost:8080 for the dashboard.
