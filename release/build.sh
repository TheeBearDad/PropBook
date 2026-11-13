#!/usr/bin/env bash
# Builds the PropBook release into dist/: Windows and macOS zips, SHA256SUMS.txt, and (when
# PROPBOOK_SIGNING_KEY is set) SHA256SUMS.txt.sig, which the app's updater checks.
# usage: release/build.sh 3.3.0        (needs Go 1.24+ and Python 3 with Pillow)
#   PROPBOOK_REPO=owner/name  where the app looks for updates (default TheeBearDad/PropBook)
#   PROPBOOK_LINUX=1          also build a Linux zip (used by the update tests)
set -euo pipefail
VERSION="${1:?usage: release/build.sh <version, e.g. 3.3.0>}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version must look like 3.3.0" >&2; exit 1; }
REPO="${PROPBOOK_REPO:-TheeBearDad/PropBook}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
export CGO_ENABLED=0 GOFLAGS=-mod=mod
LDV="-s -w -X main.version=$VERSION -X main.updateRepo=$REPO"

cd "$ROOT"
echo "== icons"
python3 tools/icons/make_icons.py "$WORK/icons"
cp "$WORK/icons/icon.png" web/icon.png
for n in 16 32 48 128; do cp "$WORK/icons/ext-$n.png" "extension/icons/$n.png"; done

echo "== tools"
go build -o "$WORK/macsign" ./tools/macsign
go build -o "$WORK/sign" ./tools/sign

echo "== Windows"
WIN="$WORK/win/PropBook"
mkdir -p "$WIN"
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui $LDV" -o "$WIN/PropBook.exe" .
cp "$WORK/icons/PropBook.ico" "$WIN/PropBook.ico"
sed 's/$/\r/' "release/Read Me - Windows.txt" > "$WIN/Read Me.txt"

echo "== macOS (universal: Apple Silicon + Intel)"
MAC="$WORK/mac/PropBook"
APP="$MAC/PropBook.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$LDV" -o "$WORK/pb-arm64" .
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$LDV" -o "$WORK/pb-amd64" .
"$WORK/macsign" lipo "$APP/Contents/MacOS/PropBook" "$WORK/pb-arm64" "$WORK/pb-amd64"
sed "s/__VERSION__/$VERSION/g" release/Info.plist > "$APP/Contents/Info.plist"
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$WORK/icons/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
"$WORK/macsign" sign "$APP"
"$WORK/macsign" verify "$APP"
cp "release/Read Me - macOS.txt" "$MAC/Read Me.txt"

rm -rf "$DIST" && mkdir -p "$DIST"
if [[ "${PROPBOOK_LINUX:-}" == 1 ]]; then
  echo "== Linux"
  mkdir -p "$WORK/lin/PropBook"
  GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$LDV" -o "$WORK/lin/PropBook/PropBook" .
  python3 release/zipdir.py "$WORK/lin" "$DIST/PropBook-$VERSION-Linux.zip"
fi

echo "== zip"
python3 release/zipdir.py "$WORK/win" "$DIST/PropBook-$VERSION-Windows.zip"
python3 release/zipdir.py "$WORK/mac" "$DIST/PropBook-$VERSION-macOS.zip"

echo "== checksums"
(cd "$DIST" && sha256sum PropBook-*.zip > SHA256SUMS.txt && cat SHA256SUMS.txt)
if [[ -n "${PROPBOOK_SIGNING_KEY:-}" ]]; then
  "$WORK/sign" sign "$DIST/SHA256SUMS.txt"
  echo "signed SHA256SUMS.txt"
else
  echo "WARNING: PROPBOOK_SIGNING_KEY not set; the release is unsigned and apps won't install it automatically." >&2
fi
ls -la "$DIST"
