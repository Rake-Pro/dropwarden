# syntax=docker/dockerfile:1
# dropwarden headless service: cmd/dropwarden-serve on the minimal Xvfb +
# Chrome base. No desktop, no window manager, no VNC: the dashboard is plain
# HTTP on 8080 and Twitch/Kick logins go through its remote-login view, which
# mirrors the watcher Chrome tab over CDP (see deploy/container/README.md).
#
#   - Stage 1 builds dropwarden-serve and dropwarden-cli, pure Go (CGO off;
#     the sqlite driver is modernc.org/sqlite).
#   - Stage 2 is the base as is: its tini + Xvfb entrypoint (DISPLAY=:1), user
#     uid 1000 with home /home/chrome, and /usr/local/bin/chrome-gpu
#     (google-chrome-stable with the GPU flags). Chrome's sandbox, GPU device
#     and seccomp come from the base image and the pod spec, not from here.
ARG GO_VERSION=1.27.3

FROM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-dev
ENV CGO_ENABLED=0
RUN go build -tags production -trimpath \
      -ldflags "-s -w -X github.com/rake-pro/dropwarden/internal/version.Version=${VERSION}" \
      -o /out/dropwarden-serve ./cmd/dropwarden-serve \
 && go build -tags production -trimpath \
      -ldflags "-s -w -X github.com/rake-pro/dropwarden/internal/version.Version=${VERSION}" \
      -o /out/dropwarden-cli ./cmd/dropwarden-cli

# Placeholder tag: bump to the cluster-chrome release this is verified on.
FROM ghcr.io/rake-pro/cluster-chrome:1.0.3
ARG VERSION=0.0.0-dev
LABEL org.opencontainers.image.title="dropwarden" \
      org.opencontainers.image.description="dropwarden headless service (Twitch/Kick drops) on Xvfb + Chrome" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/Rake-Pro/dropwarden"
COPY --from=build --chmod=0755 /out/dropwarden-serve /out/dropwarden-cli /usr/local/bin/
ENV DROPWARDEN_HOME=/home/chrome/dropwarden
EXPOSE 8080
CMD ["dropwarden-serve", "-listen", "0.0.0.0:8080", "-chrome-path", "/usr/local/bin/chrome-gpu"]
