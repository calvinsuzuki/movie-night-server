#!/usr/bin/env bash
# Builds Movie night for Ubuntu (.deb) and Windows into dist/, like build.ps1.
#   ./build.sh                  # nightly
#   ./build.sh -Version 1.2.0   # release (or: ./build.sh 1.2.0)
# The Windows installer needs Inno Setup (ISCC), so it is built only where that
# is found: Git Bash on Windows, or an `iscc` wrapper around Wine on Linux
# (set FFMPEG_EXE to a Windows ffmpeg.exe there). Elsewhere you get the bare
# dist/windows/movienight.exe.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="nightly"
case "${1:-}" in
  -Version|--version|-v) VERSION="${2:?missing version}" ;;
  "") ;;
  *) VERSION="$1" ;;
esac

step() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31mbuild: %s\033[0m\n' "$*" >&2; exit 1; }

# A nightly is named "nightly" but carries a real version number for Windows and
# apt: the last release tag plus the build time, e.g. 0.2.1+nightly.202610181930,
# so it installs over that release and the next release installs over it.
NAME="$VERSION"
if [ "$VERSION" = "nightly" ]; then
  last="$(git describe --tags --abbrev=0 2>/dev/null || true)"
  last="${last#v}"
  VERSION="${last:-0.0.0}+nightly.$(date +%Y%m%d%H%M)"
fi
NUMERIC="${VERSION%%[+~-]*}"

command -v go >/dev/null || die "Go not found: https://go.dev/dl"
GOBIN="$(go env GOPATH)/bin"
tool() { # find a Go tool on PATH or in GOPATH/bin
  command -v "$1" 2>/dev/null || { [ -x "$GOBIN/$1" ] && echo "$GOBIN/$1"; } ||
    { [ -x "$GOBIN/$1.exe" ] && echo "$GOBIN/$1.exe"; } || true
}

# cloudflared (Apache-2.0) gives the invite link its public address. Pinned, with
# the SHA256 sums published in the release notes on github.com/cloudflare/cloudflared.
CLOUDFLARED_VERSION="2026.9.3"
declare -A CLOUDFLARED_SHA256=(
  [cloudflared-windows-amd64.exe]="f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2"
  [cloudflared-linux-amd64]="77e26d8d900e0b8469f416239d14b5f296525fdf79fee6f511ef55609e3fbac2"
)
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
get_cloudflared() { # prints the cached, verified path
  local name="$1" dir="build/cache/cloudflared-$CLOUDFLARED_VERSION"
  local path="$dir/$name"
  if [ ! -f "$path" ]; then
    mkdir -p "$dir"
    step "Downloading $name $CLOUDFLARED_VERSION" >&2
    curl -fsSL -o "$path.part" "https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION/$name"
    mv -f "$path.part" "$path"
  fi
  local hash; hash="$(sha256 "$path")"
  if [ "$hash" != "${CLOUDFLARED_SHA256[$name]}" ]; then
    rm -f "$path"
    die "$name checksum mismatch (got $hash)"
  fi
  echo "$path"
}

[ -f assets/icon.ico ] || { step "Drawing icons"; go run ./tools/genicon; }

step "Checking assets/sounds.json"
go test ./assets/

rm -rf dist/windows dist/linux
mkdir -p dist/windows dist/linux

# --- Ubuntu ----------------------------------------------------------------

NFPM="$(tool nfpm)"
[ -n "$NFPM" ] || die "nfpm not found: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest"

step "Building Linux app"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/linux/movienight .
cp "$(get_cloudflared cloudflared-linux-amd64)" dist/linux/cloudflared
chmod 755 dist/linux/movienight dist/linux/cloudflared

step "Building Ubuntu .deb"
VERSION="$VERSION" "$NFPM" package --config packaging/linux/nfpm.yaml --packager deb \
  --target "dist/movienight_${NAME}_amd64.deb"

# --- Windows ---------------------------------------------------------------

WINRES="$(tool go-winres)"
if [ -n "$WINRES" ]; then
  step "Embedding Windows icon and version info"
  "$WINRES" simply --arch amd64 --manifest gui --icon assets/icon.ico \
    --product-name "Movie night" --file-description "Movie night" --original-filename movienight.exe \
    --copyright "calvinsuzuki" \
    --product-version "$NUMERIC" --file-version "$NUMERIC" --out rsrc
else
  echo "go-winres not found, so the .exe gets no icon: go install github.com/tc-hib/go-winres@latest"
fi

step "Building Windows app"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/windows/movienight.exe .
cp "$(get_cloudflared cloudflared-windows-amd64.exe)" dist/windows/cloudflared.exe

ISCC=""
for c in "${LOCALAPPDATA:-}/Programs/Inno Setup 6/ISCC.exe" "/c/Program Files (x86)/Inno Setup 6/ISCC.exe" "$(command -v iscc || true)"; do
  [ -n "$c" ] && [ -f "$c" ] && { ISCC="$c"; break; }
done
FFMPEG="${FFMPEG_EXE:-}"
if [ -z "$FFMPEG" ] && command -v ffmpeg.exe >/dev/null; then FFMPEG="$(command -v ffmpeg.exe)"; fi

if [ -n "$ISCC" ] && [ -n "$FFMPEG" ]; then
  step "Building Windows installer"
  cp "$FFMPEG" dist/windows/ffmpeg.exe
  # -D/-Q rather than /D/Q, so Git Bash doesn't rewrite them as paths.
  "$ISCC" -Q "-DAppVersion=$VERSION" "-DOutputName=$NAME" packaging/windows/movienight.iss
else
  echo "Skipping the Windows installer (needs Inno Setup and a Windows ffmpeg.exe); dist/windows/ has the bare app."
fi

step "Done: $VERSION"
ls -lh dist/*.deb dist/*.exe 2>/dev/null | awk '{print $5 "\t" $NF}'
