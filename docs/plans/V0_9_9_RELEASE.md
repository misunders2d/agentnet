# v0.9.9 corrective release — MEL-709 / MEL-701

Status: frozen product and qualified draft; final macOS native job still running. Publication and Hub switch pending. Root owns this checklist,
integration, publication, deployment and clean handoff. review_patch alone writes
MEL-709's internal/client Go changes; review_history alone writes MEL-701's
disjoint Store and focused UI regressions. Neither edits the other's files.
Participating verifier reviews the stable candidate and exact public payloads.

Owner explicitly requested an immediate new release fixing MEL-709 and including
MEL-701. Existing native permissions, signed/encrypted synchronization, original
identity/topic/attempt, no replay and newer unread generations stay authoritative.
Latest inspected published app is v0.9.8; verify again before choosing the tag.

| ID | Required outcome | Acceptance evidence | Owner | State | Evidence |
|---|---|---|---|---|---|
| T1 | Exact native steering reaches and changes the active admitted run once | Owned native Codex consumption plus focused direct/group and completion/cancellation race regressions | review_patch + verifier | implemented | Native Codex53.46s and focused direct/group checks pass; full qualification pending |
| T2 | Absorbed corrections acquire truthful terminal/result linkage | Success, cancellation, failure, uncertain acceptance and interruption; no blanket answered state or cross-attempt completion | review_patch | implemented | RED cancellation regression; terminal/reaction/manual/attempt cases and corrective review pass |
| T3 | Stuck shipped records have safe supported recovery | Exact legacy fixtures; no replay/manual SQL rewrite; truthful disposition of four identified live records when safe | review_patch + root | implemented | Exact legacy fixtures and conservative recovery pass; four live records inspected read-only, recovery awaits activation |
| T4 | Linked Pending/status/attention converge and stay cleared | Existing signed updates, duplicate/reconnect/restart/stale-status regressions; newer work preserved | review_patch + verifier | implemented | Pending/attention projection and linked reader restart/stale/duplicate checks pass 2.662 s |
| U1 | Confirmed old reads stay cleared; unconfirmed reads report failure and recover | Actual Store rejected/timed-out read, scoped warning/retry and no false clearing; real read/restart/import/read-sync with unseen/newer unread preserved; retained latency/Review evidence | review_history | implemented | Actual Store 12 modes and React/Chromium1280/390 plus canonical build/typecheck pass; original Zenbook 13 recurrence cause remains unverified |
| R1 | Qualified integrated candidate | Required applicable race/native, exact-pin skins, real mixed-version against latest older tag; retained evidence and skips explicit | root + verifier | agreed | Pending |
| R2 | Publish, deploy and leave clean | Immutable tags/checksums/public-byte checks, safe existing Hub rollout, Linear/handoff, clean synced repos/no stray worktrees | root + verifier | agreed | Pending |

## Current evidence

Live read-only metadata confirms all four MEL-709 corrections are still steered,
read and admitted as attempt 1. The older parent is answered with its stored
same-scope result; the other three share a cancelled parent. No live repair ran.
The new exact cancellation regression fails on shipped source (child stays
steered); the first candidate passes success/cancel/failure/interruption cases.
Legacy recovery and races remain under focused qualification.

MEL-701: the later zero was after the owner read everything. The 18:52 UTC batch
contains 14 rows (12 native, 2 imports), now all carrying exact read tombstones;
no V2 attention marks exist in this installation. Retained read-sync copies
start that minute, so earlier durable reads are not established. Isolated
13-row real-Live read/reopen/restart/duplicate checks pass; signed history and
older-reader checks pass separately. This is not a reproduced resurrection.
The actual frontend's swallowed read-save error path is reproduced and corrected.

The actual Store rejected/timed-out read regression now fails: the backend
remains unread, no acknowledgement is recorded and no error appears, including
after Back/visibility retry. This proves a source defect that can leave viewed
messages unread without warning; it does not prove the historical 13 incident's
cause. Post-ack refresh rejection already reports an error, so the correction
must distinguish it from read-write failure and suppress stale-workspace noise.

Native verifier resolved existing Codex 0.165.0 ELF and inherited Sol/xhigh
configuration. No install, account/config change or user session change; the
real native steering test passes 53.46 s with two ordered corrections and signed terminal links.

U1 focused candidate: actual Store reject-open, timeout-after-Back, reopen and
Android visibility retry, stopped/disconnected/stale host, confirmed newer view,
newer unread, wrong author and duplicate-error checks pass. The test fails on
the exact baseline (expected warning 1, actual 0). Actual React/Chromium at 1280
and 390 widths shows failure-after-Back warning in 34/10 ms, retaining the Review
row until retry's fresh view proves read. Reopen clears only confirmed reads;
previous retained navigation measures 205/232 ms and confirmed return 176/36 ms.
The canonical builder supplies generated shared-module staging before tsc;
raw-tree tsc alone lacks that staging and is not authoritative build evidence.

Do not change execution authority or manufacture completion from acceptance.
No user desktop/native session may be closed or restarted. No live read-state
clearing or database repair is authorized merely to make Review empty. Record
verified causal evidence before repairs; prefer source transition plus bounded
recovery over a new public operation.

Final focused checkpoint: vet passes. Current candidate single-case reaction
history race passes 41.19 s; exact released f23 baseline passes 39.59 s with the
same test bytes/command. Prior suite failure remains unattributed. No deadline
was extended, source guard weakened or race warning ignored. The initial full
focused run had an obsolete post-completion steered assertion, corrected to
stronger exact answered/result/no-replay checks. The affected race passes all
changed cases; linked status/Pending assertions are integrated as regressions.

## Integrated qualification checkpoint — October 29

First frozen candidate `bc514a248844e11d31fe775e2b55714f1d68b8b1`, run
[40772710015](https://github.com/misunders2d/agentnet/actions/runs/40772710015):
27 applicable jobs pass, six fail, one optional Windows Task Scheduler probe
skips. All eighteen client race partitions, Linux/Windows native, all three
desktop qualification jobs and the container pass. Retain that unchanged
client production evidence. The failures are not passes:

- macOS reaction fixture observed the prior React reset generation. Exact
  generation acknowledgement fixes the fixture; all sixteen local cases pass.
  Downstream macOS native and WebKit checks did not execute and remain required.
- Windows client partitions 1/2/4 fail three existing delivery/history cases.
  One exact three-parent batch on public v0.9.8 passes in 40.962 s; this does not
  establish the candidate failure's cause. Candidate diagnostics/recheck pending.
- `others` reaches the aggregate CLI package 600 s bound before UI partitions.
  Split its complete 215-test CLI list into disjoint 108/107 selections, retaining
  the same 600 s bound, all eighteen client partitions and all three UI partitions.
- Android local send feedback is 1023.7 ms, above the strict 1000 ms ceiling.
  A held native POST proves the composer waits for admission before previewing
  an already journaled message. Move its existing local Sending presentation
  before POST; no native admission/queued receipt is invented. Uncertain failure
  retires only the local preview and preserves exact journal/stage IDs for retry.
  Durable journal failure preserves the draft and sends nothing.

Android held-request regression is RED on `bc514a24`; fourteen actual Composer/
MessageView cases (seven each at 1280/390) pass after the fix, with local feedback
75–140 ms. Rejected/lost response, exact retry, quota failure, navigation/newer
drafts, authoritative newer DTO and stopped-store cases execute. New Go wrapper
passes 8.95 s and reaction wrapper 4.90 s, no skips. Existing composer current-
attempt checks pass 10.005 s; canonical staged typecheck/bundle passes. These
Chromium Android-host fixtures do not substitute for native WebView qualification.

The first candidate's exact-pin gallery gate passes 321 Node/browser tests, zero
skips/failures, and all 37 applicable runtime groups. Android changes require a
new core source pin and another matching-gallery qualification before publication.
No user app, desktop or native session has been closed or restarted.

## Frozen product, corrected qualification and draft

Immutable application tag `v0.9.9` points to
`7f15d4578b0bf6dd71fc13c837297914da0a891d`. Gallery tag
`v2026.10.29.1` points to `04b5f08d8792de5e49a7954cb5e0bc102a5a853d`
and pins that exact application source. Both tags are pushed; neither release
is public at this checkpoint. The original native client production code remains
identical to the eighteen passing race partitions on `bc514a24`.

QA-only commits `be7735b6`, `c9295f49` and `617f9af3` change just three test
files: the Android-host rendered fixture, browser-engine test and Android
instrumentation. All other 2,200 tracked files are identical to the frozen
candidate (comparison SHA256
`1e506da5cd66087b351592bace770fc42d5a5fcd252ac0028c1a246646084b38`).

- Windows could not invoke the Unix `.bin/tailwindcss` launcher. Calling the
  installed JavaScript CLI through Node fixes this portability failure. The
  actual Android/reaction fixtures pass on Windows, 8.58/6.02 s.
- The first macOS Android-host fixture timed the driver before click
  actionability and through three sequential IPC waits. The original actual
  renderer latency is unknown. A controlled 1,100 ms Node-only delay yields
  1,236 ms driver observation while real browser feedback/frame are 33/35 ms.
  Measure trusted browser input, visible exact Sending row/empty draft and
  frame opportunity separately; both renderer bounds remain strictly below
  1,000 ms. All fourteen cases pass on Linux, macOS and Windows in
  [40779722977](https://github.com/misunders2d/agentnet/actions/runs/40779722977).
  Successful wrapper output does not expose numerical remote maxima; do not
  invent them. Local corrected-clock feedback is 28.6–46.5 ms.
- UI race 0's browser-engine fixture assumes MarkUnread cannot return its
  documented prewrite scope-conflict refusal during concurrent history/read
  commits. The guard and original fixture are byte-identical to v0.9.8;
  existing deterministic guard tests prove three refused snapshots and no write.
  Retry only that exact refusal, the same captured selected ID, and the existing
  30 s/50 ms wait. All guards/assertions remain. The exact race passes locally
  in 31.94 s; full UI race 0 passes in
  [40779995816](https://github.com/misunders2d/agentnet/actions/runs/40779995816),
  including the formerly failing case (10.17 s). Both CLI and all three UI race
  partitions now pass; all eighteen client race partitions remain retained.
- Android's full run passes sixteen workloads but fails unread startup native
  focus before DOM/input/timing. Add failure-only phase/deadline/poll/native/DOM
  diagnostics, preserving its shared 30 s deadline and focus assertion. The exact
  unread workload passes in
  [40779999648](https://github.com/misunders2d/agentnet/actions/runs/40779999648):
  three actions, maximum 207.1 ms. The earlier focus cause remains unproved.
  Together all seventeen workloads execute 534 measured actions, every feedback
  bound passes, maximum 503.6 ms. This is synthetic DOM input in actual
  MainActivity WebView 124 on an owned SDK 35 x86_64 emulator, CSS 320×592/DPR 1,
  reduced motion and 1,000 people; it excludes OS dispatch and physical devices.

The final exact-pin gallery `npm test` passes 321 Node/browser checks with zero
failures/skips and all 37 applicable runtime groups; five inapplicable groups
remain excluded. Five packages/129 files and all twenty unchanged previews
verify. The disposable company is removed; raw evidence remains private.

Real mixed-version testing on the clean frozen product against published v0.9.8
passes 92.784 s with no skip. Windows' exact three-parent recovery batch passes
33.115 s; earlier published baseline passes 40.962 s without establishing the
original candidate failure's cause. All three desktop qualifications and Linux/
Windows native checks remain retained for unchanged paths. Mac WebKit passes;
its independent full native Go suite is still running in
[40779298515](https://github.com/misunders2d/agentnet/actions/runs/40779298515).

Draft build [40780502512](https://github.com/misunders2d/agentnet/actions/runs/40780502512)
passes all program, three installer and Android jobs. Each build checks out the
immutable tag, independently of dispatch's QA-only HEAD. Independent draft
qualification passes all twelve payload hashes plus SHA256SUMS, metadata digests,
six clean Go source/architecture stamps, Linux executable and AppImage sidecar/
media contents, and Android's original signer, version code 9009, two ABIs and
16 KiB alignment. No installation or publication follows merely from this pass.

Publication follows the owner's direction to ship verified fixes with coverage
limits disclosed. The exhaustive every-control/platform/history matrix and
physical-device evidence remain incomplete and are not passing claims. The
original MEL-701 thirteen-message recurrence remains causally unproved. Four
live MEL-709 records await safe new-daemon activation; only connected v0.9.9
proves startup recovery has run. An app-managed home has no supported daemon-only
update override, so no user GUI/harness session may be closed for verification.
