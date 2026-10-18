# dropwarden-desktop container

Linux GUI build of dropwarden inside a web desktop, for running the watcher on a
headless box (the rake.pro cluster's node4) instead of a logged-in PC. Prep work
as of 2026-10-18: nothing here has run on real hardware yet.

## What is in the image

| Piece | Source | Why |
| --- | --- | --- |
| Base | `ghcr.io/linuxserver/baseimage-selkies:debiantrixie` | Xvfb `:1` with glamor/DRI3, openbox, Selkies web UI on 3000 (http) / 3001 (https); same base linuxserver's own chromium image uses |
| App | `cmd/dropwarden` + `cmd/dropwarden-cli`, `-tags "gtk3 production"` | Same build as release.yml's linux job; GUI needs `libgtk-3-0t64` + `libwebkit2gtk-4.1-0` |
| Browser | `google-chrome-stable` from `dl.google.com/linux/chrome/deb` | `internal/browser/locate.go` finds `/usr/bin/google-chrome-stable` first |
| Autostart | `deploy/container/autostart` -> `/defaults/autostart` | Restart loop around the app, run by the X11 session as user `abc` |

- Image name: `ghcr.io/rake-pro/dropwarden-desktop:vX.Y.Z` (private GHCR). Built by
  `.github/workflows/container.yml` on every `v*` tag and on dispatch; tag = git tag only.
- `DROPWARDEN_HOME=/config/dropwarden` puts config.yaml, the sqlite DB and the Chrome
  profiles on the `/config` volume (Selkies' persistent home).
- `PIXELFLUX_WAYLAND=false` forces the X11 session. The GTK3 backend and Chrome's
  `--window-position`/`--window-size` flags assume X11.

## Runtime requirements (the k8s chart in GitOps `cluster-apps/dropwarden` sets all of these)

| Need | How | If missing |
| --- | --- | --- |
| Display | provided by the base (Xvfb :1) | n/a |
| GPU render node | `/dev/dri/renderD*` in the container + `DRINODE=/dev/dri/renderD128` | Xvfb falls back to software GL; Chrome reports SwiftShader as the WebGL renderer, a fingerprint change on top of the 2026-09 integrity incident |
| `/dev/shm` >= 1 GiB | tmpfs mount | Chrome tab crashes on heavy pages |
| Chrome sandbox | unprivileged user namespaces | "No usable sandbox!" and Chrome exits (see below) |
| Persistent `/config` | volume | logins and progress lost on restart |
| Access control | front the 3000 port with SSO (gpm `sso-lan` profile); Selkies has no auth unless `CUSTOM_USER`/`PASSWORD` are set | anyone reaching :3000 drives the desktop |

## Chrome sandbox on Ubuntu 24.04+ hosts

- Ubuntu restricts unprivileged user namespaces through AppArmor
  (`kernel.apparmor_restrict_unprivileged_userns`). Inside a container that means
  Chrome's namespace sandbox cannot start and Chrome exits with "No usable sandbox!".
- `internal/browser/flags.go` deliberately does NOT pass `--no-sandbox`. Three ways out,
  decide per host:
  1. Host sysctl `kernel.apparmor_restrict_unprivileged_userns=0` on the node (loosens
     the whole node, simplest).
  2. A per-pod AppArmor profile that carries a `userns,` rule
     (`securityContext.appArmorProfile: {type: Localhost, localhostProfile: ...}`),
     loaded on the node. Tightest; needs a profile file on the host.
  3. `browser.extra_flags: ["--no-sandbox"]` in config.yaml. What linuxserver's
     chromium image does ("the container is the sandbox boundary"); gives up Chrome's
     own renderer isolation for the Twitch session cookies.
- First spin-up: try with none of the three. If Chrome does not launch, read the
  dropwarden log, then pick 1 or 2 before 3.

## Verify-live gates (owed before trusting any drop to it)

1. `navigator.webdriver === false` in the dedicated Chrome (DevTools on the Selkies desktop).
2. WebGL renderer names the AMD/radeonsi driver, not SwiftShader (`chrome://gpu`).
3. Campaigns page populated; first `claim: gql` line in the log; minute-watched credit
   actually accruing on one drop (never verified on real Linux hardware, STATUS.md).
4. Tray behaviour with no StatusNotifier host in the session: the dashboard window must
   stay reachable (close-to-hide could leave no way back; the autostart loop is the
   fallback).
5. `systemd-inhibit` is absent in the container; the sleep hold logs an error and the
   watcher must keep running regardless.

## Local build (any docker host)

```
docker build -f deploy/container/Dockerfile --build-arg VERSION=0.0.0-local -t dropwarden-desktop:local .
docker run --rm -p 3000:3000 --shm-size=1g --device /dev/dri -e PUID=1000 -e PGID=1000 \
  -v "$PWD/.container-config:/config" dropwarden-desktop:local
```
Open http://localhost:3000 for the desktop.
