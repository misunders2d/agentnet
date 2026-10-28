#!/usr/bin/env bash
# Existing instrumentation and synthetic offline fixture, on a disposable CI
# emulator only. Never targets an attached phone or a real AgentNet profile.
set -euo pipefail
[[ ${CI:-} == true && ${GITHUB_ACTIONS:-} == true ]]
: "${RUNNER_TEMP:?}"
: "${AGENTNET_ANDROID_FIXTURE_DIR:?}"
# Optional affected-workload reruns retain the complete default qualification.
all_suites=(history navigation settings unread actions topics groups compose review outbox agents teams profile storage workspaces permissions media)
suites=("${all_suites[@]}")
if [[ -n ${AGENTNET_ANDROID_LATENCY_SUITES:-} ]]; then
  selection=$AGENTNET_ANDROID_LATENCY_SUITES
  if [[ ! $selection =~ ^[a-z,]+$ || $selection == ,* || $selection == *, || $selection == *,,* ]]; then
    printf '%s\n' 'Invalid empty Android workload' >&2; exit 2
  fi
  IFS=, read -r -a suites <<< "$selection"
  seen=,
  for suite in "${suites[@]}"; do
    valid=false
    for known in "${all_suites[@]}"; do [[ $suite != "$known" ]] || valid=true; done
    if [[ $valid != true || $seen == *",$suite,"* ]]; then
      printf 'Invalid or duplicate Android workload: %s\n' "$suite" >&2; exit 2
    fi
    seen+="$suite,"
  done
fi
serial="emulator-${EMULATOR_PORT:-5554}"
adb -s "$serial" get-state
test "$(adb -s "$serial" shell getprop ro.kernel.qemu | tr -d '\r')" = 1
package=io.github.misunders2d.agentnet.preview
runner="$package.test/io.github.misunders2d.agentnet.UpdaterInstrumentation"
stop_fixture() {
  timeout --signal=KILL 20s adb -s "$serial" shell am force-stop "$package"
}
capture_hang() {
  local mode=$1 suite=$2 status=0
  local output="$RUNNER_TEMP/android-hang-${mode}-${suite:-none}.txt"
  # One overall bound includes lookup, ownership checks and every Java dump.
  timeout --signal=KILL 20s bash -c '
    set -euo pipefail
    serial=$1 package=$2 expected_uid=$3
    qemu=$(adb -s "$serial" shell getprop ro.kernel.qemu | tr -d "\r")
    if [[ $qemu != 1 ]]; then printf "%s\n" "HANG_DIAGNOSTIC rejected: not owned qemu"; exit 1; fi
    adb -s "$serial" shell ps -A -o PID,S,WCHAN,NAME | grep -iE "codec|media|surfaceflinger|agentnet" || true
    pids=$(adb -s "$serial" shell pidof "$package" | tr -d "\r") || { printf "%s\n" "HANG_DIAGNOSTIC no pid"; exit 0; }
    if [[ -z $pids ]]; then printf "%s\n" "HANG_DIAGNOSTIC no pid"; exit 0; fi
    if [[ ! $pids =~ ^[1-9][0-9]*([[:space:]]+[1-9][0-9]*)*$ ]]; then
      printf "%s\n" "HANG_DIAGNOSTIC rejected: malformed pid list"; exit 1
    fi
    for pid in $pids; do
      uid=$(adb -s "$serial" shell stat -c %u "/proc/$pid" | tr -d "\r") || continue
      process=$(adb -s "$serial" shell cat "/proc/$pid/cmdline" | tr -d "\000\r") || continue
      if [[ $uid != "$expected_uid" || $process != "$package" ]]; then
        printf "HANG_DIAGNOSTIC rejected pid=%s: ownership changed\n" "$pid"; continue
      fi
      printf "HANG_DIAGNOSTIC Java threads pid=%s\n" "$pid"
      adb -s "$serial" shell debuggerd -j "$pid" || printf "HANG_DIAGNOSTIC debuggerd failed pid=%s\n" "$pid"
    done
  ' android-hang "$serial" "$package" "$app_uid" > "$output" 2>&1 || status=$?
  printf 'HANG_DIAGNOSTIC exit=%s mode=%s suite=%s\n' "$status" "$mode" "${suite:-none}" >> "$output"
}
adb -s "$serial" root
adb -s "$serial" wait-for-device
adb -s "$serial" install -r "$RUNNER_TEMP/previous-android/AgentNet-android.apk"
adb -s "$serial" install -r -t mobile/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
stop_fixture
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
  local mode=$1 output=$2 suite=${3:-} status=0 seconds=240
  local -a args=(-e mode "$mode" -e ownedFixture 1)
  if [[ $mode == latency ]]; then
    seconds=150
    args+=(-e latencySuite "$suite")
  fi
  timeout --kill-after=5s "${seconds}s" adb -s "$serial" shell am instrument -w -r "${args[@]}" "$runner" > "$output" 2>&1 || status=$?
  if (( status == 124 || status == 137 )); then
    # 137 can also mean an external SIGKILL; never call it a proven timeout.
    printf 'INSTRUMENTATION_INTERRUPTED mode=%s suite=%s status=%s\n' "$mode" "${suite:-none}" "$status" >> "$output"
    if (( status == 124 )); then printf 'TIMEOUT mode=%s suite=%s\n' "$mode" "${suite:-none}" >> "$output"; fi
    capture_hang "$mode" "$suite"
    # End only this fixture app after diagnostics, even for a hard-stop snapshot.
    stop_fixture || printf 'FIXTURE_STOP_FAILED mode=%s suite=%s\n' "$mode" "${suite:-none}" >> "$output"
  fi
  cat "$output"
  if [[ $mode == latency ]] && { ! grep -Eq '^INSTRUMENTATION_CODE: -1[[:space:]]*$' "$output" || ! grep -q 'LATENCY_JSON=' "$output"; }; then
    # A completed workload can lose its final Binder result. Preserve only
    # relevant system failure tags from this disposable emulator, not all logs.
    timeout --signal=KILL 20s adb -s "$serial" logcat -d -v brief -s AndroidRuntime:E ActivityManager:W Binder:W JavaBinder:W "*:S" > "$RUNNER_TEMP/android-incomplete-latency-$suite.txt" 2>&1 || true
  fi
  if (( status != 0 )); then return "$status"; fi
  grep -Eq '^INSTRUMENTATION_CODE: -1[[:space:]]*$' "$output" || return 1
  if [[ $mode == latency ]]; then grep -q 'LATENCY_JSON=' "$output"; fi
}
instrument snapshot "$RUNNER_TEMP/android-before-upgrade.txt"
stop_fixture
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
failed=0
# Keep only Android's rate-limit diagnostic from this owned fixture. Preserve
# the qualifier's exit status and never upload a full application log.
capture_rate() {
  timeout --signal=KILL 20s adb -s "$serial" logcat -d -v brief -s NotificationService:W "*:S" 2>/dev/null | grep -F "Package enqueue rate" > "$RUNNER_TEMP/android-notification-rate-$1.txt" || true
  timeout --signal=KILL 20s adb -s "$serial" logcat -d -b events -v threadtime -s notification_cancel:I notification_cancel_all:I notification_canceled:I "*:S" 2>/dev/null |
    python3 mobile/android/scripts/notification-cancellations.py > "$RUNNER_TEMP/android-notification-cancellations-$1.txt" || true
}
trap 'capture_rate final' EXIT
stop_fixture
if ! instrument notifications "$RUNNER_TEMP/android-notifications.txt"; then failed=1; fi
capture_rate notifications
# NotificationManager state outlives an instrumentation invocation. Isolate
# the next matrix on this CI/qemu-only fixture without changing saved data.
stop_fixture
if ! instrument indicators-video "$RUNNER_TEMP/android-indicators-video.txt"; then failed=1; fi
capture_rate indicators
# Prove the refused-notification path before any decoder workload can wedge
# an emulator service; restore the same granted precondition for timing cases.
stop_fixture
adb -s "$serial" shell pm revoke "$package" android.permission.POST_NOTIFICATIONS
if ! instrument notifications-denied "$RUNNER_TEMP/android-notifications-denied.txt"; then failed=1; fi
stop_fixture
adb -s "$serial" shell pm grant "$package" android.permission.POST_NOTIFICATIONS
# Every invocation consumes its private test JS/seed in finally; restage both.
for suite in "${suites[@]}"; do
  stop_fixture
  for file in latency-fixture.mjs latency-input.json; do
    adb -s "$serial" push "$RUNNER_TEMP/android-latency/$file" "$app_data/cache/$file"
    adb -s "$serial" shell chown "$app_uid:$app_uid" "$app_data/cache/$file"
    adb -s "$serial" shell chmod 600 "$app_data/cache/$file"
  done
  output="$RUNNER_TEMP/android-latency-$suite.txt"
  if ! instrument latency "$output" "$suite"; then failed=1; fi
done
stop_fixture
adb -s "$serial" shell pm revoke "$package" android.permission.POST_NOTIFICATIONS
printf '%s\n' 'Emulator evidence only; physical Pixel launcher and live delivery are separate checks.'
exit "$failed"
