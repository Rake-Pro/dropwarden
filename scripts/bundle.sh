#!/usr/bin/env bash
# Assemble dist/dropwarden.app from pre-built binaries and Info.plist. macOS
# only (sips/iconutil); release.yml's macos job is the CI caller.
#
# Inputs (env, with defaults):
#   VERSION    marketing/build version written into Info.plist   (default 0.0.0-dev)
#   BUNDLE_ID  CFBundleIdentifier                                 (default pro.rake.dropwarden)
#   BIN        path to the built dropwarden GUI binary           (default dist/dropwarden)
#   CLI_BIN    path to the built dropwarden-cli binary           (default dist/dropwarden-cli)
#   APP        output .app bundle path                           (default dist/dropwarden.app)
#   ICON_SRC   1024px PNG the icns is built from                 (default build/icons/dropwarden_icon.png)
#
# Produces:
#   $APP/Contents/MacOS/dropwarden        (CFBundleExecutable)
#   $APP/Contents/MacOS/dropwarden-cli    (updater helper + headless CLI; the
#                                          GUI finds it as its sibling)
#   $APP/Contents/Info.plist              (placeholders substituted)
#   $APP/Contents/Resources/AppIcon.icns
set -euo pipefail

VERSION="${VERSION:-0.0.0-dev}"
BUNDLE_ID="${BUNDLE_ID:-pro.rake.dropwarden}"
BIN="${BIN:-dist/dropwarden}"
CLI_BIN="${CLI_BIN:-dist/dropwarden-cli}"
APP="${APP:-dist/dropwarden.app}"

# Resolve repo root from this script's location so the run dir does not matter.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PLIST_SRC="$REPO_ROOT/build/macos/Info.plist"
ICON_SRC="${ICON_SRC:-$REPO_ROOT/build/icons/dropwarden_icon.png}"

for f in "$BIN" "$CLI_BIN" "$PLIST_SRC" "$ICON_SRC"; do
	if [ ! -f "$f" ]; then
		echo "bundle: missing $f" >&2
		exit 1
	fi
done
for tool in sips iconutil; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "bundle: $tool not found (bundle.sh runs on macOS only)" >&2
		exit 1
	fi
done

CONTENTS="$APP/Contents"
MACOS_DIR="$CONTENTS/MacOS"
RES_DIR="$CONTENTS/Resources"

# Start clean so stale files never linger in the bundle.
rm -rf "$APP"
mkdir -p "$MACOS_DIR" "$RES_DIR"

cp "$BIN" "$MACOS_DIR/dropwarden"
cp "$CLI_BIN" "$MACOS_DIR/dropwarden-cli"
chmod +x "$MACOS_DIR/dropwarden" "$MACOS_DIR/dropwarden-cli"

sed -e "s|__VERSION__|$VERSION|g" \
    -e "s|__BUNDLE_ID__|$BUNDLE_ID|g" \
    "$PLIST_SRC" > "$CONTENTS/Info.plist"

# AppIcon.icns from the 1024px master via an .iconset.
ICONSET_TMP="$(mktemp -d)"
ICONSET="$ICONSET_TMP/AppIcon.iconset"
mkdir -p "$ICONSET"
for size in 16 32 128 256 512; do
	sips -z "$size" "$size" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
	double=$((size * 2))
	sips -z "$double" "$double" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$RES_DIR/AppIcon.icns"
rm -rf "$ICONSET_TMP"

echo "bundled $APP (version $VERSION, id $BUNDLE_ID)"
