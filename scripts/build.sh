#!/bin/sh
# Build agentnet for the usual laptop and server platforms into dist/.
# Usage: scripts/build.sh [VERSION]
set -eu
version=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
cd "$(dirname "$0")/.."
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os=${target%/*}
	arch=${target#*/}
	out=dist/agentnet-$os-$arch
	[ "$os" = windows ] && out=$out.exe
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X github.com/misunders2d/agentnet/internal/protocol.Version=$version" \
		-o "$out" ./cmd/agentnet
	echo "$out"
done
