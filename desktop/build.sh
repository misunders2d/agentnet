#!/bin/sh
# Build the AgentNet app for this computer's system into desktop/out/:
# the agentnet program (Go, marked as the app's: it refuses `agentnet
# update`, the app updates as a whole) bundled as the shell's sidecar, then
# the shell (Tauri v2, the system webview) and its installers, copied to
# names without a version (static/getapp.json names them for downloads):
#   Linux    AgentNet-linux-x86_64.AppImage  AgentNet-linux-amd64.deb  AgentNet-linux-x86_64.rpm
#   Windows  AgentNet-windows-x64-setup.exe  (per user, no admin rights)
#   macOS    AgentNet-macos-universal.dmg    (Intel and Apple silicon)
# Needs Go, Rust (stable) and Node; Linux also needs WebKitGTK 4.1 and
# libayatana-appindicator development files. The Tauri bundler downloads
# linuxdeploy for the AppImage. Builds are not signed.
# Usage: desktop/build.sh [VERSION]
set -eu
version=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
cd "$(dirname "$0")"
desktop=$(pwd)
# The bundles need a plain version number: a release's, else 0.0.0.
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) semver=${version#v}; case "$semver" in *[!0-9.]*) semver=0.0.0 ;; esac ;;
*) semver=0.0.0 ;;
esac
ldflags="-s -w -X github.com/misunders2d/agentnet/internal/protocol.Version=$version -X main.bundledWith=app"
gobuild() { # GOOS GOARCH OUT
	(cd .. && CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "$ldflags" -o "$desktop/$3" ./cmd/agentnet)
}
mkdir -p src-tauri/binaries out
case "$(uname -s)" in
Linux)
	triple=x86_64-unknown-linux-gnu
	gobuild linux amd64 src-tauri/binaries/agentnet-$triple
	bundles=appimage,deb,rpm
	target=
	;;
Darwin)
	triple=universal-apple-darwin
	gobuild darwin amd64 src-tauri/binaries/agentnet-x86_64-apple-darwin
	gobuild darwin arm64 src-tauri/binaries/agentnet-aarch64-apple-darwin
	lipo -create -output src-tauri/binaries/agentnet-$triple src-tauri/binaries/agentnet-x86_64-apple-darwin src-tauri/binaries/agentnet-aarch64-apple-darwin
	bundles=app,dmg
	target="--target $triple"
	;;
MINGW* | MSYS* | CYGWIN* | Windows_NT)
	triple=x86_64-pc-windows-msvc
	gobuild windows amd64 src-tauri/binaries/agentnet-$triple.exe
	bundles=nsis
	target=
	;;
*)
	echo "desktop/build.sh: no AgentNet app for $(uname -s)" >&2
	exit 1
	;;
esac
[ -d node_modules ] || npm ci --no-audit --no-fund
# shellcheck disable=SC2086 # target is empty or two words
npx tauri build $target --bundles "$bundles" --config "{\"version\":\"$semver\"}"
bundle=src-tauri/target/release/bundle
[ -z "$target" ] || bundle=src-tauri/target/$triple/release/bundle
one() { # the single file a glob names, or fail
	for f in "$@"; do
		[ -f "$f" ] || { echo "desktop/build.sh: missing $*" >&2; exit 1; }
		echo "$f"
		return
	done
}
case "$triple" in
*linux*)
	cp "$(one "$bundle"/appimage/*.AppImage)" out/AgentNet-linux-x86_64.AppImage
	cp "$(one "$bundle"/deb/*.deb)" out/AgentNet-linux-amd64.deb
	cp "$(one "$bundle"/rpm/*.rpm)" out/AgentNet-linux-x86_64.rpm
	;;
*darwin*)
	cp "$(one "$bundle"/dmg/*.dmg)" out/AgentNet-macos-universal.dmg
	;;
*windows*)
	cp "$(one "$bundle"/nsis/*-setup.exe)" out/AgentNet-windows-x64-setup.exe
	;;
esac
ls -l out
