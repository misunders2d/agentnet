# Native Android development

## Owner correction — current app is the minimum, October 10

The owner rejected the preview's primitive presentation and reduced feature set.
The target is the same Comic interface and at least the existing mobile app's
functionality, with native local storage/sync and Android platform integration.
The old A1–A6 evidence below establishes an engine prototype, not product parity.
Do not hand over another APK as a replacement until the parity journeys pass.

Implementation reuses the bundled Comic UI in Android WebView and the embedded
Go UI host/SQLite/client. No hosted website or browser crypto/storage engine is
used. This replaces the hand-written Kotlin chat UI. Root owns this checklist,
Comic host adaptation and integration; android_core owns mobile Go host;
android_build owns Android shell and platform integration. Both helpers are
GPT-6.1-sol. Claude's main checkout/release remains separate.

| ID | Target outcome | Acceptance check | Owner | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| P1 | Exact Comic design and existing mobile features | Render actual bundled screens; Chats, agents, OKs, settings, topic navigation, composer and message actions | root | rendered verified | Actual shared Comic checks at 320/390px pass; installed API35 APK renders the exact bundled asset bytes and saved native thread. |
| P2 | Native local data and safe host lifecycle | Offline first render, one push stream, restart preserves identity/drafts, token/origin tests, no phone harness execution | core / root | focused verified | Merged mobile/core package2.589s; authenticated mobile host and hidden-read regression pass. Stable origin, SQLite, human-only transport and explicit ended-enrollment preservation tests. |
| P3 | Android file and navigation integration | Document picker, encrypted attachment stage/download and save, Back, keyboard, external links | build / root | emulator verified | Actual SAF selection/upload, decrypt/download/Save byte comparison, cancel/reopen, clipboard, keyboard-first Back and rotation passed. Encrypted real-peer transfer remains a phone gate. |
| P4 | Human-device controls and notifications | Remote proposal decisions, human-only local capabilities, notification preferences and exact destination | core / root | focused verified | Existing controls retained; HumanOnly guards, exact workspace routing and native permission/connection settings tested at model/rendered boundaries. Real notification click and remote decisions remain phone gates. |
| P5 | Replacement quality and truthful performance | Rendered narrow screens, restart/offline send, update retains preview data; measured startup/send; no regressions hidden | root / helpers | emulator verified / phone pending | In-place update kept the database and old draft; new draft and two exact queued sends survived force-stop/restart. Real-phone parity/performance not yet claimed. |

No change to native harness permissions, transport receipts, encryption, admission,
existing user data or relay is authorized by this presentation correction.

Sergey assigned Codex to start a native Android app in a separate worktree while
Claude owns v0.8.17. This branch is `feat/native-android`, initially based on
`67d33c2900120536f342636a14b516e88b889e25`. It is not part of that release.
Root Codex edits this checklist. GPT-6.1-sol helpers implement/review the Go
bridge and build plumbing. Claude was informed through Herdr; no waiting or
release dependency was introduced.

The original engine prototype (superseded presentation) linked a separate human device, opens saved chats offline, shows
updates from the existing push stream, and sends text through the existing
durable outbox. At that checkpoint native Kotlin views displayed data from an embedded Go client,
bound using official gomobile. That original checkpoint had no WebView or loopback HTTP server. The shared
Comic replacement above now has both; neither introduces a second sync protocol
or new cryptography. Existing signature, roster, audience, key,
receipt and task-grant checks stay in the Go core.

| ID | Target outcome | Acceptance check | Owner | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| A1 | A separate native Android build | arm64/x86_64 AAR and APK compile with pinned tools | build helper / root | emulator verified | both AAR libraries and packaged APK aligned to 16 KB; API35 emulator runtime below |
| A2 | Saved data does not wait for a connection | offline reads plus one-stream lifecycle and concurrent read/stop under blocked sends | core helper / root | emulator / Go verified | saved 200-message fixture opens without relay; focused mobile race tests 28.290s; actual phone timing pending |
| A3 | Phones never launch agent harnesses | reject responder homes; HumanOnly skips worker startup/recovery | core helper | verified at Go boundary | focused client race regression 3.365s |
| A4 | Retries retain the original request and authority | persist exact send ID/body/kind/target before sending; restart retry tests | root / core reviewer | emulator / model verified | actual offline send survives process restart once; 8 Kotlin cases cover immutable retries, exact-ID acknowledgement, crash-after-admission reconciliation, mismatched targets and duplicate text |
| A5 | Native projection preserves canonical state | edits/deletes, verified authorship, receipt labels and read markers | core helper / root | verified at model boundary | 11 Kotlin projection cases; Go narrow read action tests pass |
| A6 | App respects foreground/background lifecycle | no polling; explicit background connection with stop action; private storage excluded from backups | root / core reviewer | implemented | manifest/source review; native runtime still pending |

## Shared Comic integration evidence — October 10

Checkpoint `ff1a3878` replaces the Kotlin screens. Merge `1a03a1aa` includes
Claude's `release/v0.8.17` runtime source through `1d055408`; the final released
tag `4d6557ab` adds only qualification tests/docs and is also merged. This does not modify
Claude's checkout or publish that release. Android uses the exact bundled Comic
package, embedded authenticated loopback host and the native Go database/client.
The browser engine is not booted.

Focused evidence: shared rendered navigation8s; mobile/core2.589s; mobile
visibility and host-token/origin checks pass; Android bridge export/origin tests,
legacy exact-draft binding tests,5send-journal tests and10admission-proof tests
pass. The old preview's drafts remain in private preferences until exact import
acknowledgement. Android journals frozen send requests before clearing drafts;
recovery is explicit and checks stored original messages before retrying.
Attachment stage IDs expire across process restart, as in the current native UI;
unprovable saved sends remain visible rather than falsely acknowledged. No
physical-phone timing, relay enrollment or encrypted real-device file transfer
is claimed by these synthetic checks.

Preview core stamp uses `v0.8.17+android.g<commit>.preview`, identifying the source
baseline without claiming a published release. Android cannot install CLI binaries;
its current upgrade path is installing a newer APK over the existing preview.

## Corrected Comic APK — tested October 10

The local APK is `~/Downloads/AgentNet-native-preview-0.2.0-comic.apk`,
version code 2, development version `0.2.0-comic-preview`, package
`io.github.misunders2d.agentnet.preview`. Install over the earlier preview;
no uninstall or re-enrollment is needed for an already linked preview. It
uses the original preview certificate (SHA-256
`e1d06246fc1e49d5cc24dddb4dea618c2410cc35e3a98efa28daa8ae450b0659`).
The browser/PWA installation remains separate.

- APK SHA-256: `039b8155fb57a9f3a85e56b6ce8f7c1d702147dd3a541761d91b5ddb2f83f60f`.
- Core source stamp: `v0.8.17+android.g28365384.preview`; later merge contains
  only the final v0.8.17 relay test/documentation qualification.
- AAR SHA-256: `4578b408393659b32b1f687b11de9469809be1af7e036688888842fcb2b96d29`.
- Both ARM64 and x86_64 libraries contain all 14 finished Comic assets. Native
  ELF LOAD alignment is 16 KB; APK ZIP alignment and signature verification pass.
- Gradle unit tests: 27 passed, no failures/skips. Lint has no errors. Final
  affected-package Go vet passes; rendered Comic/mobile policy checks pass in
  8.207s. Fifteen send-journal/recovery tests and native bridge checks pass.
- API35 emulator, generated offline identity/thread only: an in-place upgrade
  preserved the 200-message database and old native draft. Two queued text
  sends and a new draft survived force-stop/restart exactly once. Later APK
  update preserved a third attachment message and the rotation draft.
- Actual SAF chooser selection uploaded 55 generated bytes through the native
  API. After APK restart, Comic downloaded/decrypted the retained attachment;
  Android Save produced byte-identical output. Cancelling and reopening Save
  works. A real mobile-only CSP defect found here is fixed and regression tested;
  desktop policy is unchanged, no external connection source is added.
- Actual system Back dismisses keyboard before leaving chat. Rotation retained
  the draft. Android clipboard write passed. Shared rendered screens at 320/390px
  cover menus, dialogs, mentions, dark mode, OKs and notification settings.
- Observations on software-rendered emulator: cold Activity 2.363s; earlier
  WebView first-contentful-paint 285ms after page navigation; offline text
  admission/display 1.585s; attachment upload/admission 4.241s. These measure
  different intervals on a small synthetic thread, not real-phone latency or
  successful delivery to a peer. They do not establish whole-product parity.

The initial failed native export and failed test selectors are retained in
local development logs. They are not reported as passing evidence. No live
identity, grant, installed desktop, relay, human message or phone data was changed.

## Build

### Next preview work in progress — October 10

The next preview adds an offline QR scanner using JourneyApps ZXing 4.3.0,
requested only by a Scan gesture, with optional camera hardware/runtime
permission. Scanned content enters the existing link inspection and explicit
Join flow; it never opens a URL or approves membership automatically. The
pending-link screen names the exact approving device and shows link expiry.
Terminal native receive-loop failures now invalidate the shared Comic view
and expose the failure instead of leaving a seemingly healthy local page.

Focused evidence: real temporary TLS link/approval/receive/restart and terminal
failure race checks pass (13.353s); shared Comic pending-link and failure
rendering passes at 390/320px (8.125s); Android host/QR JavaScript tests pass.
Gradle compilation, all 28 unit tests and lint pass; mobile/core vet passes.
No physical-phone stream-failure cause has been established. Android camera
scanning and the next packaged APK still need native qualification. Integrate
the verified v0.8.18 shared-core sync repair before handing over that APK.

### Build instructions

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

For a local preview update, preserve the signing key used by the earlier APK.
Set `AGENTNET_PREVIEW_KEYSTORE` (or Gradle property `agentnetPreviewKeystore`)
to that existing debug keystore before packaging. The local preview uses
`/tmp/agentnet-android-tools/android-user/debug.keystore`; never commit this key.
An automatic Gradle debug key from another home cannot update the prior APK.
Run the Comic asset build to completion **before** `build-core.sh`; regenerating
the asset directory while Go collects embed paths can omit `skin.json`.

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

## Original engine prototype verification — superseded presentation

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

## Original local phone preview — superseded presentation

The owner requested a local APK while Google finishes developer verification.
Debug builds now use `io.github.misunders2d.agentnet.preview` and display
`AgentNet Native Preview`, separate from the eventual Play package/signing key.
This is a development-signed APK. The Play app will enroll separately; this
preview does not overwrite its private storage or the existing browser phone.

The APK is copied to `~/Downloads/AgentNet-native-preview-0.1.0.apk` (about
61 MiB), SHA-256
`43b8703dc685c517dab62a38fa4b913403804d5a77461bf3190255fbc2d62745`.
Gradle assembly/lint, APK signature verification and 16 KB ZIP alignment pass.
Compiled manifest confirms the preview ID, Android 8+ minimum, ARM64/x86_64
libraries and the existing full activity class. The core and Kotlin behavior
are unchanged from the emulator-tested checkpoint above. No real phone was
installed, linked or approved by Codex.

1. Copy that APK to the phone and install it with the phone's package installer.
2. On the registered desktop, open **Settings/You → Your devices → Add a device**
   and use **Copy link** (or **Copy code**).
3. Open **AgentNet Native Preview** on the phone. Paste the complete link into
   **Device link code**, leave/use the device name `android-native`, and tap
   **Link this phone**. Paste into the app, not the phone browser.
4. On the desktop's **Your devices**, approve that pending phone with
   **Yes, it's mine**. Keep the source desktop online during initial history copy.

The existing `DecodeLinkOffer` accepts the whole copied desktop URL and bare
codes. `TestLinkVectors` passed; no parallel enrollment format was added.
Each installation gets its own key and joins the same verified human roster.

## Honest limits / next work

This is a development preview, not a replacement offered to existing users.
No installed app, relay, live identity, grant, database or desktop session was
changed. The browser phone remains enrolled separately; no browser key import
or account reset is required by this branch.

The shared Comic UI now supplies attachments, topic navigation, new chats,
invitations/OKs and settings. Android supplies the native picker, private storage,
notification bridge and optional background connection. This preserves the
current presentation and features rather than substituting a reduced chat UI.

Remaining qualification includes real enrollment, encrypted peer file delivery,
foreground/background notification clicks and physical-phone first-message,
reconnect, backlog, sending and battery measurements. Release signing and Play
updates remain separate. Desktop-local harness execution and CLI updating are
not phone capabilities. Reading a native timeline still uses the existing full
Go UI projection; large-history performance requires measurement. The merged
v0.8.17 source is recorded above; presentation changes alone do not cure every
core sync problem.

## Battery monitoring — stopped by owner

Sergey initially requested a development pause below 40% battery. He later
returned and explicitly requested stopping monitoring. The owned watcher was
stopped on October 10; `/tmp/agentnet-dev-battery-status.json` records
`monitoring: false`. Do not restart it unless he asks.
