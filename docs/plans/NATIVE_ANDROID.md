# Native Android development

Sergey assigned Codex to start a native Android app in a separate worktree while
Claude owns v0.8.17. This branch is `feat/native-android`, initially based on
`67d33c2900120536f342636a14b516e88b889e25`. It is not part of that release.
Root Codex edits this checklist. GPT-6.1-sol helpers implement/review the Go
bridge and build plumbing. Claude was informed through Herdr; no waiting or
release dependency was introduced.

The first slice links a separate human device, opens saved chats offline, shows
updates from the existing push stream, and sends text through the existing
durable outbox. Native Kotlin views display data from an embedded Go client,
bound using official gomobile. There is no WebView, loopback HTTP server, second
sync protocol, or new cryptography. Existing signature, roster, audience, key,
receipt and task-grant checks stay in the Go core.

| ID | Target outcome | Acceptance check | Owner | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| A1 | A separate native Android build | arm64/x86_64 AAR and APK compile with pinned tools | build helper / root | emulator verified | both AAR libraries and packaged APK aligned to 16 KB; API35 emulator runtime below |
| A2 | Saved data does not wait for a connection | offline reads plus one-stream lifecycle and concurrent read/stop under blocked sends | core helper / root | emulator / Go verified | saved 200-message fixture opens without relay; focused mobile race tests 28.290s; actual phone timing pending |
| A3 | Phones never launch agent harnesses | reject responder homes; HumanOnly skips worker startup/recovery | core helper | verified at Go boundary | focused client race regression 3.365s |
| A4 | Retries retain the original request and authority | persist exact send ID/body/kind/target before sending; restart retry tests | root / core reviewer | emulator / model verified | actual offline send survives process restart once; 8 Kotlin cases cover immutable retries, exact-ID acknowledgement, crash-after-admission reconciliation, mismatched targets and duplicate text |
| A5 | Native projection preserves canonical state | edits/deletes, verified authorship, receipt labels and read markers | core helper / root | verified at model boundary | 11 Kotlin projection cases; Go narrow read action tests pass |
| A6 | App respects foreground/background lifecycle | no polling; explicit background connection with stop action; private storage excluded from backups | root / core reviewer | implemented | manifest/source review; native runtime still pending |

## Build

Requirements: JDK 17, Android SDK 35/build-tools 35.0.0, NDK 28.2.13676358,
Go 1.26.8. Gradle 8.11.1 and its SHA-256 are pinned in the wrapper; AGP 8.9.2
and Kotlin 2.1.20 are pinned. The separate `mobile/tools` module pins x/mobile;
the Android runtime adds the pinned SQLite driver described below. Official build references:
[Go Mobile](https://go.dev/wiki/Mobile),
[AGP 8.9 compatibility](https://developer.android.com/build/releases/agp-8-9-0-release-notes),
[Android page sizes](https://developer.android.com/guide/practices/page-sizes).

From the repository root, with JAVA_HOME and ANDROID_HOME set:

```sh
mobile/android/scripts/build-core.sh --bootstrap
cd mobile/android
./gradlew :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

The bootstrap option permits downloads of pinned Go tooling and dependencies.
Subsequent core builds use the populated cache offline. Generated AAR, APK,
build tools, signing keys and caches are ignored, never committed.

Use `ANDROID_ABIS=android/arm64,android/amd64` when an x86_64 emulator is needed.
The core version defaults to `git describe --tags --always --dirty`, so a
development binary never presents itself as Claude's released candidate.

## Android storage adapter

The first emulator launch with saved data found a real portability failure:
modernc SQLite's translated Linux libc called raw `SYS_LSTAT` on Android x86_64,
which Android's seccomp filter rejected with `SIGSYS`. A successful cross-build
had not detected it. Android is not in modernc's published
[supported-platform table](https://pkg.go.dev/modernc.org/sqlite#hdr-Supported_platforms_and_architectures).

Android now uses [mattn/go-sqlite3 v1.14.52](https://github.com/mattn/go-sqlite3/blob/v1.14.52/README.md)
through the NDK/Bionic C library, with upstream `sqlite_omit_load_extension`.
Desktop builds retain modernc. The small Android adapter retains the existing
`sqlite` driver name, schemas, file permissions and queries; translates only
the existing supported pragma spellings; and preserves WAL, foreign keys,
busy timeout, immediate transactions and FULL synchronous durability. Unknown
pragmas and weaker synchronous settings fail explicitly. No Android security
policy or system calls are overridden.

The `agentnet_sqlite_cgo` tag lets the existing store/migration/snapshot tests
exercise that driver on the host. Those tests and the mobile core pass with
both drivers. Android runtime checks remain separate from that host evidence.

## Development verification — 2026-10-10

- `:app:testDebugUnitTest`: 19 tests, no failures or skips. APK assembly and
  lint pass; lint retains two preview warnings (default device-name localization
  and a false SAM warning on `stopService(Intent)`).
- Focused `go vet ./mobile/core ./internal/sqlitedb` passes. Existing focused
  race evidence above remains valid. CGo store/core checks pass in 0.022s/0.258s.
- API35 x86_64 emulator: initial setup and invalid-link feedback tested; final
  APK opens a generated, isolated 200-message saved thread with an unreachable
  fake relay. Local reads work despite that failed connection.
- Sending one synthetic ordinary message creates one durable queued outbox
  row. Force-stop/relaunch retains the same message exactly once and displays
  `Waiting to send; retries automatically`; the composer is clear, without a
  false delivery claim or duplicate retry. UI screenshots and a read-only copy
  of the synthetic database confirmed it. No real account or recipient used.
- Two cold activity starts reported 2.678s and 2.324s on this software-rendered
  emulator. UI hierarchy capture confirmed saved chats by 5.894s, including
  the capture's own overhead. These are development observations, not a
  physical-phone first-message benchmark or a large-history performance claim.
- A short idle emulator sample showed 0% app CPU over the measured 5-second
  interval, about 72 MiB PSS / 167 MiB RSS. This small offline fixture does not
  qualify active backlog sync, physical-device battery cost or idle networking.
- AAR SHA-256: `8cb5603a132bc46d900f5bef8d32ed02865a7bdc1530da7e192236e10d3bcab7`.
  Final debug APK SHA-256:
  `2587d11642f94e9e26539de48807b762cc14bc065c4ec77e6a48a07718148d87`.
  APK signature verification and `zipalign -c -P 16 4` pass. ARM64 compiled;
  execution so far is x86_64 only. Artifacts remain ignored build outputs.

The optional `TestExportAndroidOfflineFixture` creates synthetic data only when
`AGENTNET_ANDROID_FIXTURE_DIR` names a new empty directory beneath `/tmp`. It
does not export installed data or prove network admission. Keep such fixtures
inside isolated test emulators; never replace a person's real database.

## Lifecycle and security

- Identity/database/spool live in Android `noBackupFilesDir/agentnet`. Cloud
  backup and device transfer are disabled; a phone enrolls with its own key.
- A lifecycle lane controls the one Go push stream; local reads and sends use
  separate lanes. Invalidations coalesce, with no refresh timer or poll loop.
- The native composer persists the full request before the first send and
  retains it unchanged after uncertainty. Retrying uses that same correlation.
  Discarding a draft does not retract an already queued message.
- The optional, user-started `remoteMessaging` foreground service keeps the
  existing connection alive with a visible Stop control. No boot receiver,
  unconditional wakelock or battery-optimization exemption is added. Android
  may still suspend networking; reconnect uses the existing durable recovery.
  See [foreground-service types](https://developer.android.com/develop/background-work/services/fgs/service-types).
- `RunOptions.HumanOnly` defaults false. It prevents the phone from launching
  the desktop worker, native responder sockets, or interrupted-job recovery.
  It does not relax receive admission or grants.

## Honest limits / next work

This is a development preview, not a replacement offered to existing users.
No installed app, relay, live identity, grant, database or desktop session was
changed. The browser phone remains enrolled separately; no browser key import
or account reset is required by this branch.

Remaining product work includes enrollment and foreground/background testing
on an actual Android device; first-message, reconnect, backlog, send and battery
measurements; message notifications; attachments; topic navigation; new chats;
invitations/OKs; recovery UI for expired/refused enrollment; release
signing/distribution and update delivery. Reading a
native timeline currently uses the existing full Go UI projection; bounded
history pages should reuse existing store queries before large-history claims.
The shared Go core still requires Claude's final v0.8.17 fixes to be integrated
and verified before any release; changing the UI does not cure a core sync bug.

## Battery stop rule

Sergey explicitly requires stopping development below 40% battery. The parent
monitor samples BAT0 every 30 seconds, records state in
`/tmp/agentnet-dev-battery-status.json`, and creates
`/tmp/agentnet-dev-battery-pause` at that threshold. Root checkpoints work,
drains owned helpers, and pauses development without terminating user sessions.
