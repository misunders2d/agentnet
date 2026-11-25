#!/usr/bin/env bash
set -euo pipefail
[[ $# == 2 ]] || { printf '%s\n' 'Usage: verify-apk.sh APK vMAJOR.MINOR.PATCH' >&2; exit 2; }
apk=$1
version=$2
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-}}"
: "${ANDROID_HOME:?Set ANDROID_HOME to the pinned SDK.}"
build_tools="$ANDROID_HOME/build-tools/36.0.0"
certificate=e1d06246fc1e49d5cc24dddb4dea618c2410cc35e3a98efa28daa8ae450b0659
verified=$("$build_tools/apksigner" verify --verbose --print-certs "$apk")
printf '%s\n' "$verified"
[[ $(printf '%s\n' "$verified" | sed -n 's/^Signer #1 certificate SHA-256 digest: //p') == "$certificate" ]]
[[ $(printf '%s\n' "$verified" | sed -n 's/^Number of signers: //p') == 1 ]]
"$build_tools/zipalign" -c -P 16 4 "$apk"
badging=$("$build_tools/aapt" dump badging "$apk")
APK_BADGING="$badging" python3 - "$apk" "$version" <<'PY'
import os, re, struct, sys, zipfile
path, version = sys.argv[1:]
match = re.fullmatch(r'v(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})', version)
assert match, 'Invalid release version'
major, minor, patch = map(int, match.groups())
code = major * 1000000 + minor * 1000 + patch
badging = os.environ['APK_BADGING']
assert f"package: name='io.github.misunders2d.agentnet.preview' versionCode='{code}' versionName='{version}'" in badging, 'Wrong package/version'
assert 'application-debuggable' not in badging, 'Published APK is debuggable'
assert "uses-permission: name='android.permission.REQUEST_INSTALL_PACKAGES'" in badging, 'Preview update permission missing'
with zipfile.ZipFile(path) as archive:
    libraries = [name for name in archive.namelist() if name.startswith('lib/') and name.endswith('.so')]
    assert {name.split('/')[1] for name in libraries} == {'arm64-v8a', 'x86_64'}, 'Both supported ABIs must ship'
    for name in libraries:
        data = archive.read(name)
        assert data[:6] == b'\x7fELF\x02\x01', f'Expected ELF64 little endian: {name}'
        offset = struct.unpack_from('<Q', data, 32)[0]
        size, count = struct.unpack_from('<HH', data, 54)
        loads = 0
        for i in range(count):
            header = offset + i * size
            if struct.unpack_from('<I', data, header)[0] == 1:
                loads += 1
                assert struct.unpack_from('<Q', data, header + 48)[0] >= 16384, f'Unaligned LOAD segment: {name}'
        assert loads, f'No LOAD segments: {name}'
print(f'Verified {version}: original signing key, package/code, nondebuggable, both ABIs, 16KiB alignment')
PY
