#!/usr/bin/env bash
set -euo pipefail
android_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
repo_dir=$(cd -- "$android_dir/../.." && pwd)
if [[ ${1:-} == --help ]]; then
  printf '%s\n' 'Usage: build-core.sh [--bootstrap]' 'Requires Go1.26.8+, JDK17+, Android SDK35/build-tools35.0.0 and NDK28.2.13676358.' '--bootstrap explicitly permits fetching pinned Go tools and dependencies through the Go checksum database.' 'VERSION optionally overrides the default git describe --tags --always --dirty stamp.' 'Set ANDROID_HOME; ANDROID_NDK_HOME defaults to its pinned side-by-side NDK.' 'ANDROID_ABIS defaults to android/arm64; use android/arm64,android/amd64 for an emulator too.'
  exit 0
fi
[[ $# -eq 0 || ( $# -eq 1 && $1 == --bootstrap ) ]] || { printf '%s\n' 'Unknown argument; use --help.' >&2; exit 2; }
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-}}"
: "${ANDROID_HOME:?Set ANDROID_HOME to the Android SDK directory.}"
: "${ANDROID_NDK_HOME:=$ANDROID_HOME/ndk/28.2.13676358}"
export ANDROID_HOME ANDROID_NDK_HOME
[[ -d $ANDROID_HOME/platforms/android-35 && -d $ANDROID_HOME/build-tools/35.0.0 ]] || { printf '%s\n' 'Install SDK35 and build-tools35.0.0 after reviewing SDK licenses.' >&2; exit 1; }
[[ -f $ANDROID_NDK_HOME/source.properties ]] || { printf '%s\n' 'Missing pinned Android NDK28.2.13676358.' >&2; exit 1; }
grep -Eq '^Pkg.Revision *= *28\.2\.13676358[[:space:]]*$' "$ANDROID_NDK_HOME/source.properties" || { printf '%s\n' 'ANDROID_NDK_HOME must point to NDK28.2.13676358.' >&2; exit 1; }
command -v go >/dev/null
command -v javac >/dev/null
# Match scripts/build.sh: previews report their exact checkout, including dirt.
version=${VERSION:-$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || printf '%s' dev)}
[[ $version =~ ^[A-Za-z0-9._+-]+$ ]] || { printf '%s\n' 'VERSION must contain only letters, digits, dots, underscores, pluses or hyphens.' >&2; exit 2; }
export GOMAXPROCS="${GOMAXPROCS:-2}"
# The generated temporary gomobile module has no source checkout of its own.
# protocol.Version above records this checkout explicitly.
export GOFLAGS="-p=$GOMAXPROCS -buildvcs=false"
export GOCACHE="${GOCACHE:-$android_dir/.tools/go-build}"
tool_dir="$android_dir/.tools/bin"
mkdir -p "$tool_dir" "$android_dir/app/libs"
cd "$repo_dir/mobile/tools"
if [[ ${1:-} == --bootstrap ]]; then
  go mod download all
  go build -mod=mod -o "$tool_dir/gomobile" golang.org/x/mobile/cmd/gomobile
  go build -mod=mod -o "$tool_dir/gobind" golang.org/x/mobile/cmd/gobind
else
  export GOPROXY=off
  [[ -x $tool_dir/gomobile && -x $tool_dir/gobind ]] || { printf '%s\n' 'Pinned Go tools absent. Run with --bootstrap to fetch and build them.' >&2; exit 1; }
fi
export PATH="$tool_dir:$PATH"
# gomobile init fetches gobind@latest, so prepare its work directory locally
# and use the exact gobind built above instead.
export GOMODCACHE="$(go env GOMODCACHE)"
mkdir -p "$android_dir/.tools/gopath/pkg/gomobile"
export GOPATH="$android_dir/.tools/gopath:$(go env GOPATH)"
# The tools module supplies x/mobile without changing the runtime root go.mod.
gomobile bind -tags=sqlite_omit_load_extension -target="${ANDROID_ABIS:-android/arm64}" -androidapi=26 \
  -javapkg=io.github.misunders2d.agentnet.core \
  -ldflags "-s -w -X github.com/misunders2d/agentnet/internal/protocol.Version=$version" \
  -o "$android_dir/app/libs/agentnet-core.aar" \
  github.com/misunders2d/agentnet/mobile/core
