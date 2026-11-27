#!/usr/bin/env bash
# Builds the Google Play App Bundle: package io.github.misunders2d.agentnet,
# no self-update (Play updates it), signed with the Play upload key.
set -euo pipefail
android_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
repo_dir=$(cd -- "$android_dir/../.." && pwd)
: "${VERSION:?Set VERSION to the release tag vMAJOR.MINOR.PATCH.}"
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-}}"
: "${ANDROID_HOME:?Set ANDROID_HOME to the pinned SDK.}"
: "${AGENTNET_PLAY_KEYSTORE:?The Play upload keystore is required.}"
: "${AGENTNET_PLAY_KEYSTORE_PASSWORD:?The Play upload keystore password is required.}"
[[ $VERSION =~ ^v(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$ ]] || { printf '%s\n' 'Invalid release version.' >&2; exit 2; }
[[ -f $AGENTNET_PLAY_KEYSTORE ]] || { printf '%s\n' 'Missing Play upload keystore.' >&2; exit 1; }
export VERSION ANDROID_HOME AGENTNET_PLAY_KEYSTORE AGENTNET_PLAY_KEYSTORE_PASSWORD
export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.2.13676358"
export AGENTNET_RELEASE_VERSION="$VERSION"
export ANDROID_ABIS=android/arm64,android/amd64
"$android_dir/scripts/build-core.sh" --bootstrap
cd "$android_dir"
./gradlew --no-daemon --max-workers=2 :app:testReleaseUnitTest :app:lintRelease :app:bundleRelease
aab="$android_dir/app/build/outputs/bundle/release/app-release.aab"
mkdir -p "$repo_dir/dist"
cp -- "$aab" "$repo_dir/dist/AgentNet-play.aab"
sha256sum "$repo_dir/dist/AgentNet-play.aab"
