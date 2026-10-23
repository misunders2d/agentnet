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
  grep -Eq '^INSTRUMENTATION_CODE: -1[[:space:]]*$' "$output"
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
# NotificationManager state outlives an instrumentation invocation. Isolate
# the next matrix on this CI/qemu-only fixture without changing saved data.
adb -s "$serial" shell am force-stop "$package"
instrument indicators-video "$RUNNER_TEMP/android-indicators-video.txt"
# Every invocation consumes its private test JS/seed in finally; restage both.
failed=0
for suite in history navigation settings unread actions topics groups compose media review outbox agents teams profile storage workspaces permissions; do
  for file in latency-fixture.mjs latency-input.json; do
    adb -s "$serial" push "$RUNNER_TEMP/android-latency/$file" "$app_data/cache/$file"
    adb -s "$serial" shell chown "$app_uid:$app_uid" "$app_data/cache/$file"
    adb -s "$serial" shell chmod 600 "$app_data/cache/$file"
  done
  output="$RUNNER_TEMP/android-latency-$suite.txt"
  if ! adb -s "$serial" shell am instrument -w -r -e mode latency -e ownedFixture 1 -e latencySuite "$suite" "$runner" > "$output"; then failed=1; fi
  cat "$output"
  if ! grep -Eq '^INSTRUMENTATION_CODE: -1[[:space:]]*$' "$output" || ! grep -q 'LATENCY_JSON=' "$output"; then failed=1; fi
done
adb -s "$serial" shell pm revoke "$package" android.permission.POST_NOTIFICATIONS
if ! instrument notifications-denied "$RUNNER_TEMP/android-notifications-denied.txt"; then failed=1; fi
printf '%s\n' 'Emulator evidence only; physical Pixel launcher and live delivery are separate checks.'
exit "$failed"
