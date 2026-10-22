#!/usr/bin/env bash
# Existing instrumentation and synthetic offline fixture, on a disposable CI
# emulator only. Never targets an attached phone or a real AgentNet profile.
set -euo pipefail
[[ ${CI:-} == true && ${GITHUB_ACTIONS:-} == true ]]
: "${RUNNER_TEMP:?}"
: "${AGENTNET_ANDROID_FIXTURE_DIR:?}"
serial="emulator-${EMULATOR_PORT:-5554}"
adb -s "$serial" get-state
test "$(adb -s "$serial" shell getprop ro.kernel.qemu | tr -d '\r')" = 1
package=io.github.misunders2d.agentnet.preview
runner="$package.test/io.github.misunders2d.agentnet.UpdaterInstrumentation"
adb -s "$serial" root
adb -s "$serial" wait-for-device
adb -s "$serial" install -r "$RUNNER_TEMP/previous-android/AgentNet-android.apk"
adb -s "$serial" install -r -t mobile/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
adb -s "$serial" shell am force-stop "$package"
app_data="/data/user/0/$package"
app_uid=$(adb -s "$serial" shell stat -c %u "$app_data" | tr -d '\r')
[[ $app_uid =~ ^[0-9]+$ ]]
adb -s "$serial" shell mkdir -p "$app_data/no_backup/agentnet"
for file in identity.json agent.db; do
  adb -s "$serial" push "$AGENTNET_ANDROID_FIXTURE_DIR/$file" "$app_data/no_backup/agentnet/$file"
done
adb -s "$serial" shell chown -R "$app_uid:$app_uid" "$app_data/no_backup"
adb -s "$serial" shell chmod -R go-rwx "$app_data/no_backup"
adb -s "$serial" shell pm grant "$package" android.permission.POST_NOTIFICATIONS
instrument() {
  local mode=$1 output=$2
  adb -s "$serial" shell am instrument -w -r -e mode "$mode" "$runner" > "$output"
  cat "$output"
  rg -q '^INSTRUMENTATION_CODE: -1\r?$' "$output"
}
instrument snapshot "$RUNNER_TEMP/android-before-upgrade.txt"
adb -s "$serial" shell am force-stop "$package"
adb -s "$serial" install -r dist/AgentNet-android.apk
instrument snapshot "$RUNNER_TEMP/android-after-upgrade.txt"
python3 - "$RUNNER_TEMP/android-before-upgrade.txt" "$RUNNER_TEMP/android-after-upgrade.txt" <<'PY'
import json, re, sys
def snapshot(path):
    match = re.search(r'SNAPSHOT_JSON=(\{[^\n]+\})', open(path).read())
    assert match, 'Missing completed Android snapshot'
    return json.loads(match.group(1))
before, after = map(snapshot, sys.argv[1:])
assert before['preserved'] == after['preserved'], 'Upgrade changed saved fixture state'
assert len(after['preserved']['messages']) == 200
print('PASS signed Android upgrade: original identity, 200 saved messages, origin and drafts retained')
PY
instrument notifications "$RUNNER_TEMP/android-notifications.txt"
instrument indicators-video "$RUNNER_TEMP/android-indicators-video.txt"
adb -s "$serial" shell pm revoke "$package" android.permission.POST_NOTIFICATIONS
instrument notifications-denied "$RUNNER_TEMP/android-notifications-denied.txt"
printf '%s\n' 'Emulator evidence only; physical Pixel launcher and live delivery are separate checks.'
