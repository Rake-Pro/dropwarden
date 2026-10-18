# syntax=docker/dockerfile:1
# dropwarden-desktop: the Linux GUI build inside a linuxserver.io Selkies web
# desktop, so it runs "in the background" on a headless cluster node while a
# browser tab can still see the dashboard window and the real Chrome window.
#
#   - Stage 1 builds cmd/dropwarden (+ the CLI harness) with the GTK3 backend,
#     exactly like release.yml's linux job.
#   - Stage 2 is the Selkies base (Xvfb :1 with glamor/DRI3, openbox, web UI on
#     3000/3001) + GTK3/WebKitGTK runtime + Google Chrome stable from Google's
#     own apt repo. The session autostart (deploy/container/autostart) launches
#     dropwarden as user abc (PUID/PGID, default 1000).
#
# Runtime contract (see deploy/container/README.md):
#   /config            persistent volume; DROPWARDEN_HOME=/config/dropwarden
#   /dev/dri/renderD*  pass the GPU render node for a real GL renderer
#   /dev/shm >= 1 GiB  Chrome needs it
#   PIXELFLUX_WAYLAND=false   X11 session: the GTK app and Chrome's
#                             --window-position both assume it
ARG GO_VERSION=1.27.2

FROM golang:${GO_VERSION}-trixie AS build
RUN apt-get update \
 && apt-get install -y --no-install-recommends libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-dev
ENV CGO_ENABLED=1
RUN go build -tags "gtk3 production" -trimpath \
      -ldflags "-s -w -X github.com/rake-pro/dropwarden/internal/version.Version=${VERSION}" \
      -o /out/dropwarden ./cmd/dropwarden \
 && go build -tags "gtk3 production" -trimpath \
      -ldflags "-s -w -X github.com/rake-pro/dropwarden/internal/version.Version=${VERSION}" \
      -o /out/dropwarden-cli ./cmd/dropwarden-cli

FROM ghcr.io/linuxserver/baseimage-selkies:debiantrixie
ARG VERSION=0.0.0-dev
LABEL org.opencontainers.image.title="dropwarden-desktop" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/Rake-Pro/dropwarden"
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl libgtk-3-0t64 libwebkit2gtk-4.1-0 fonts-liberation \
 && install -d -m 0755 /etc/apt/keyrings \
 && curl -fsSL https://dl.google.com/linux/linux_signing_key.pub -o /etc/apt/keyrings/google.asc \
 && echo 'deb [arch=amd64 signed-by=/etc/apt/keyrings/google.asc] https://dl.google.com/linux/chrome/deb/ stable main' \
      > /etc/apt/sources.list.d/google-chrome.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends google-chrome-stable \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*
COPY --from=build /out/dropwarden /out/dropwarden-cli /usr/local/bin/
COPY deploy/container/autostart /defaults/autostart
RUN chmod 0755 /defaults/autostart /usr/local/bin/dropwarden /usr/local/bin/dropwarden-cli
ENV TITLE=dropwarden \
    PIXELFLUX_WAYLAND=false \
    DROPWARDEN_HOME=/config/dropwarden
EXPOSE 3000 3001
VOLUME /config
