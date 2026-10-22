# AgentNet v0.9.3 — complete October 22 batch and verified public release

**ACTIVE — authorized by Sergey on October 22, 2026.** The owner requires the
complete new release published by morning, with Claude reviewing every GUI
area and measured speed for every interaction on every supported system. The owner instructed:
“okay, activate the goal and start working,” then required the tagged-chat
correction in Linear and the next release: “make sure ‘release’ is in your goal.”
**GPT-6.1-sol/xhigh Codex helpers
implement in Herdr tab 3**, root orchestrates, and **the existing Claude
consultant in tab 2 oversees design, implementation and the integrated
candidate**, challenging drift and overengineering.

Finish and verify **every item in the October 22 batch plus Q1**, and drive the
integrated candidate to a **verified public v0.9.3 release**, starting
from published v0.9.2 (`628f30dbcbaff103a6edff6657a03dd2b1efbe7e`) and its
documentation child `acb798a143a3fdc5a20f1954d688969cd464737c`. Deliver one
integrated candidate with minimal fixes, the complete media feature, independent
review, focused regressions, required combined qualification, published app and
matching skin gallery, independently verified public artifacts and the usual
preserved-data rollout to the existing relay with a verified full backup. A patch,
build, queued test or eventual delivery alone is not completion. Implementation
order is an engineering choice; no listed item may be silently deferred.

Use [the batch handoff](OCT22_BATCH_HANDOFF.md), current AGENTS.md and the linked
Linear descriptions/comments as the acceptance inventory. Recheck the baseline
and concurrent checkout state before mutations. Distinguish released behavior,
prepared fixes, hypotheses and verified corrections.

## Complete included scope

1. **S1 — MEL-693: shared state for every person.** Messages and granted history,
   reaction add/remove, edits/deletions, read/unread state and personal reminders
   converge across each person's current verified linked desktop, Android and
   browser devices. Reading anywhere clears that exact item's attention after
   synchronization; complete/cancel clears reminder attention; snooze/reschedule
   clears the old due attention. Chat/topic counts, Review, Outbox, badges,
   notifications and harness hooks use the same effective state. Preserve newer
   unseen messages, deliberate unread generations, unresolved decisions and
   exact selected-session replies. Verify two independent ordinary users with
   multiple linked native/browser devices, changes originating at each endpoint,
   online propagation, offline/reordered/duplicate delivery, restart and newly
   linked-device backfill. Cleared attention and completed work must not return.
   Start as verification of the shipped MEL-670/674/561/477/558 implementation
   using existing fixtures. Add focused coverage and minimal producer/projection/
   consumer corrections only for demonstrated gaps; do not rebuild working sync
   or create a new comprehensive test framework. Reuse existing signed/encrypted
   synchronization; each person's private read/reminder state stays within that
   verified person.

2. **H1 — MEL-692: stop stale harness notices and unrelated Stop blocks.** Start
   from `attention.go`, its obsolete read-independent tests and `hook.go`.
   Reproduce a normal chat read on one linked device and synchronized to another,
   then exercise existing, new and resumed Codex/Claude/Pi sessions. Already-read
   chat must not be announced again; ordinary unrelated chat backlog must not
   block unrelated completion. Cover more than eight arrivals, filtering and
   cursor progress, mixed read/unread tails and restart. Start with the expected
   narrow correction to arrival filtering, cursor advancement past filtered
   rows, Stop handling and obsolete tests. Read rows must neither reappear nor
   stall later unread or selected-session arrivals. Expand only when a focused
   regression demonstrates another necessary seam. Preserve genuine
   decisions, exact session-bound replies and invited helpers' authorized live
   context; reading never accepts or runs work. Correct the shared consumer seam
   without clearing the live inbox or disabling hooks wholesale.

3. **R1 — MEL-687: correct reaction latency.** Reproduce the reported Linux delay
   and separately measure picker selection, durable action/API completion,
   pushed event/projection and visible paint. Fix the demonstrated delaying seam
   and retain controlled before/after evidence. Verify add/remove, failed actions,
   repeated changes, offline recovery and linked-device convergence. Immediate
   optimistic presentation alone cannot establish that the real delay is fixed.

4. **F1 — MEL-547: correct group-picture send latency.** Investigate the observed
   roughly 30-second Sending state with the composer already empty. Measure
   enqueue, ciphertext preparation/upload, per-device fanout, relay custody,
   delivery and repaint separately, using a representative group and office
   laptop. Fix the measured cause; verify text and file-only sends, online and
   unavailable recipients, retry and concurrent messaging. Preserve exact send
   identity, encryption, durable custody and truthful per-device status. Eventual
   delivery does not prove responsiveness, and an unavailable recipient must not
   hold current participants' progress hostage.

5. **U1 — MEL-681: Mac message controls.** Reproduce and fix toolbar placement
   several rows above the hovered message and failure to open the context menu
   in a window about half the screen wide. Do not assume a shared cause. Verify
   actual macOS fullscreen, ordinary and half-width windows; scrolling, reflow,
   resizing, keyboard/focus, dismissal and exact message/action target. Include
   rendered responsive regressions and preserve touch and other desktop use.

6. **D1 — MEL-682: improve bulk Drive throughput.** Use existing Drive worker
   tests and one bounded representative before/after run to measure small-file
   ciphertext files/s and bytes/s, including new files and messages during the
   run. Implement the smallest justified change to the existing worker's bounded
   concurrency, fairness and provider backoff; do not build a benchmark program
   or replacement scheduler. Compare equivalent workloads and record messaging/
   send latency alongside migration throughput. Preserve
   stable remote IDs, resumability, idempotent retry, encryption and verified
   remote checksum/custody before local eviction. The completed live migration
   is existing state to preserve; do not queue it again for a benchmark.

7. **C1 — MEL-683: understandable storage management.** Expose device cache,
   unsent/staged files and spool separately from administrator-controlled relay
   logical quota, relay-local ciphertext and Drive usage. Explain overlapping
   counts and scope, persist supported limit changes and warn before full.
   Distinguish member actions from administrator actions. Cleanup requires a
   clear reviewed selection and protects pending sends, undelivered custody and
   the last recoverable copy. Bound this work to existing storage/cleanup APIs,
   accounting and settings: improve their presentation, supported controls and
   necessary safety checks, adding only a demonstrated missing operation. Do not
   invent new retention tiers, accounting services or a cache-management engine.
   Verify limit changes across restart, low/full states, safe reclamation and
   normal attachment retrieval afterward; keep cleanup non-destructive by default.

8. **E1 — MEL-684: actionable storage/send errors.** Preserve and present the real
   reason once, with a useful member/admin next action. Distinguish relay logical
   quota, local/relay disk full, file-size limits, Drive full and network failure
   through the normal producer/API/UI path. Cover the confirmed HTTP 413 quota
   failure, transient failure and recovery. Keep text, files and exact retry
   identity intact; no duplicate send or false success after an uncertain result.

9. **M1 — MEL-685: compressed photos/videos and exact originals.** Implement
   explicit photo and video sending choices, including sending the exact
   original as a file. Prepare media on the sender before encryption, keep
   interaction responsive, bound CPU/RAM and temporary work, and support
   cancellation and ordinary hardware without a dedicated GPU. Documents and
   archives retain their bytes. Implement the sending choices and preparation in
   the shared web UI used by desktop, Android WebView and browser; verify all
   three hosts rather than building three separate media implementations.
   At Review 1, choose the practical video-processing path, supported formats,
   resource bounds and fallback behavior with Claude, using existing facilities
   first and justifying any required dependency. This decision must retain real
   video compression in scope, including ordinary CPU-only hardware, rather than
   turning the whole video feature into original-only fallback. Verify supported
   receiving/playback paths, original hashes, transformed media
   usability/orientation/audio as applicable, sizes, resource behavior,
   cancellation, unsupported/error handling and durable offline/retry. A
   photo-only implementation does not complete this target; offer clear original
   fallback when a particular media format cannot be processed.

10. **D2 — MEL-686/MEL-677: integrate and verify prepared Drive fixes.** Reuse
    `6fe3099ed293bb3e11f65fa7050474d459422f5a` and
    `bf6b399a30bb63a87c91a9c73929c6ed00d28507` from
    `fix/drive-consent-endpoint`. Background status refresh must not flicker or
    disable controls; typing/presence must not fetch storage; explicit Refresh
    retains feedback and errors. Verify the real Go-produced canonical Web OAuth
    URL through the UI validator, desktop OAuth compatibility and useful setup
    guidance. Verify retrieval of already-migrated files through normal authorized
    chat downloads without a member Google account, plus self-contained backup
    and restore with remote-only ciphertext. Preserve the connected live Drive;
    do not disconnect or remigrate it to test consent.

11. **A1 — MEL-675: verify the upgraded Pixel 6 notification-dot correction.**
    Start from the shipped registered-channel inspection/recovery, preserving
    user channel choices and the required foreground-service notification.
    Verify on the reported upgraded Pixel 6 that connection status does not
    impersonate unread-message attention, the exact settings recovery is usable,
    and real unread arrival/read clearing and reliable delivery still work.
    Record physical-device results separately from Kotlin, instrumentation,
    mock, emulator and rendered checks. Repair any reproduced remaining seam.

12. **Q1 — MEL-497, October 22 owner correction: questions through tagged chat.**
    Remove the separate blocking clarification/decision flow for missing
    business information and ordinary choices. Agents ask normal questions in
    the authorized chat, tagging the intended person; the person's tagged reply
    continues the intended request/context using existing verified routing.
    Untagged context alone does not start work. No permanent “Decision needed”
    card, separate answer form or manual “Mark as handled” step for an ordinary
    clarification. A separate blocking prompt needs a concrete reason, such as
    genuine native permission or required sender/task authorization; keep those
    native boundaries and explain the actual reason. Existing stale clarification
    records must not block new tagged work or reappear after a valid continuation.
    Reproduce the supplied screenshots, test the complete ask/tagged-answer/
    continuation/attention-clear journey, and measure queue/start/run/response
    stages behind the reported thirty-minute simple request. Reuse existing
    request links, attempts, conversation context and native continuation; no new
    workflow framework, arbitrary-text completion inference or silent replay.
    Include the supplied v0.9.2 diagnosis: the actual group worker calls
    `agentPrompt()`, which omits saved clarification answers, while the direct
    builder includes them. Fix that group path and replace the misleading group
    test that calls the direct builder. Repeated rounds must retain each prior
    question/answer exactly once under the correct request/attempt.

13. **G1 — mandatory Claude review of the entire GUI.** Claude must review all
    GUI areas and their interactions, including existing screens and every
    release skin, using rendered desktop/phone light/dark evidence and actual
    control flows. Build an explicit screen/control/state inventory so unvisited
    parts remain visible. Remove demonstrated gaps and introduce coherent
    improvements before publication. Use smooth Telegram-style UX as the
    reference: direct chat, clear hierarchy, predictable controls, short paths,
    sensible keyboard/touch/focus/Back behavior and recoverable errors. Avoid
    unnecessary forms, blocking gates, decorative motion or implementation
    jargon in ordinary product flows. This is a complete interaction review,
    not a source-only review or a blanket visual redesign. Claude verifies the
    resulting fixes and records coverage, findings and exact rendered evidence.

14. **P1 — every interaction measured on every supported system.** Inventory
    and measure all user interactions on Linux, macOS, Windows, Android and
    browser, including navigation/search, message actions, composing/sending/
    media/retry, topics and groups, agents/invitations/dismissal, Review/Outbox/
    schedules/reminders, setup/trust and all Settings/admin controls. Separate
    input feedback, durable action/API completion, push/projection and visible
    paint; include repeated actions, long history, offline/error/recovery,
    background refresh, narrow/wide layouts and keyboard/touch/pointer paths.
    Reuse existing responsiveness fixtures and native shell/Android boundaries;
    extend demonstrated coverage gaps without a replacement testing framework.
    Fix observed lag and remove avoidable blocking before publication. Do not
    hide latency with optimistic success or inflate deadlines. Report the exact
    hardware/engine/platform, workload and timings, and keep native execution,
    browser emulation and physical devices distinct. No interaction or system
    may be silently omitted or called verified from compilation alone.

Preserve signatures, exact pinned keys/current rosters, revocation/freeze checks,
membership and history grants, recipient consent, native execution permissions,
one active agent per harness/device, invited helpers' live context and
latest-version isolation. Reuse existing push updates; add no polling,
replacement synchronization framework or extra service. Append schema steps
when required; never edit shipped migrations.

## Preservation and authority

- Work in an isolated non-Play candidate from clean main. Preserve the dirty
  root checkout, private WIP/checkpoints and owner drafts. Never reset, clean,
  blanket-stage or copy that checkout wholesale into the candidate. Carry the
  current shared-state owner rule into the candidate's applicable instructions.
- Preserve the handoff's Play checkpoints and `ANDROID_PLAY_READINESS.md`,
  `ANDROID_PLAY_STORE_GOAL.md`, `ANDROID_NOTIFICATION_TAP.md` and `MARK_UNREAD.md`.
  Play publication, Firebase/shared push-gateway work and unrelated draft scope
  remain paused. Ignore unrelated incoming AgentNet notices while doing this
  batch; the backlog is not a prerequisite or a source of new assignments.
- This goal includes implementation, review, qualification, release documentation,
  immutable app/tag publication, the matching skin gallery, independent public
  verification and the usual backed-up rollout to the existing relay. Confirm
  that v0.9.3 remains the next available patch version before tagging. Q1's Linear
  requirement is owner-authorized; report scoped outcomes without resending
  historical private diagnostics. Unrelated external messages, infrastructure,
  device resets/configuration and Play work remain outside scope. Prepare
  concrete reviewable artifacts before requesting genuinely missing authority.
  Never export credentials or private incident IDs/topology into external records.

## Orchestration and Claude consultancy

- **Root orchestrates and supervises; Codex helpers implement.** Root owns scope,
  the shared checklist, lean design challenges, integration, independent evidence
  review and final reporting. Delegate production changes and meaningful
  regressions in bounded slices with disjoint file ownership or isolated
  worktrees; sequence shared state/Store/projection/media/UI edits explicitly.
- After activation, establish `docs/plans/OCT22_BATCH.md` as the one shared
  checklist, with target ID, observable acceptance, implementer, verifier,
  `agreed -> implemented -> verified` state and exact revision/evidence. Root is
  its sole editor. Split acceptance rows when necessary without dropping any
  S1–A1 requirement. Each helper agrees its slice and returns changed artifacts,
  before/after evidence, limits and unresolved findings. Receipt is not acceptance.
- Use up to three **GPT-6.1-sol Codex helpers at xhigh reasoning in Herdr tab 3**,
  with native automatic approval/review and existing permission grants. Stagger
  starts to fit measured headroom. Discover actual workspace/tab IDs and verify
  root and helper native identities, model/reasoning, readiness and completion;
  preserve focus and user-owned panes. Follow the Herdr and agent-collaboration
  skills, record owned resources, and do not launch descendants or adopt
  unrelated sessions. If that exact model is unavailable, report the mismatch
  before selecting a substitute.
- **The existing Claude consultant in tab 2 is mandatory throughout.** Verify
  its actual session and location; preserve it and its native configuration.
  Claude independently challenges scope drift, complexity, causal claims and
  missing acceptance, and consults at three recorded checkpoints: **Review 1**,
  before structural state/authority changes, Drive concurrency and the video
  implementation choice; **Review 2**, on significant implementations with
  focused before/after evidence; **Review 3**, on the frozen integrated candidate
  before costly qualification. Include exact reviewed revisions, findings,
  disagreements, corrections and root dispositions. Collect actual completed
  reviews; one early consultation cannot stand in for all three. If unavailable,
  keep the checkpoint open and continue independent authorized work.
- Assign a participating Codex helper as a verifier for changes it did not
  author where useful; reuse participants. Claude provides the independent
  consultant review, root owns acceptance and integration, and no extra reviewer
  is launched merely to fill a checklist role.
- Helpers may not publish, change live services/devices, expand scope or alter
  native permissions/tools/model settings. Capture complete results before
  cleaning up owned ephemeral helpers. Verify owned panes, agent names and test
  processes are gone; preserve user resources and the durable evidence record.

## Active supervision against overthinking

- Before editing, state the concrete failure, one input and expected output.
  Inspect shipped code, prepared commits and retained evidence before rebuilding
  a solution. Separate verified facts, causal hypotheses and acceptance results.
- Root challenges unnecessary mechanisms, extra scans/fetches, N+1 projections,
  contention, allocations, retained state, dependencies and UI steps. New durable
  state/protocol/schema changes require a concrete unmet requirement and review.
- Every investigation has one question, a cheap faithful discriminator, a
  practical time/test bound and a stop condition. After two failed causal
  attempts, stop before a third unchanged edit/run: explain the failed model and
  choose a different discriminator, simplify/rewrite, or withdraw an unnecessary
  patch. Preserve the target when changing approach; an investigation bound is
  not permission to abandon an included feature.
- Prove behavior through the normal startup/wake/retry/receive/shutdown path.
  Distinguish product defects from missing fixture prerequisites. Do not add
  instrumentation frameworks, repeated scans or elaborate proof machinery when
  an existing fixture and a focused regression answer the question.
- Keep failures and provenance. Do not increase timeouts, weaken assertions,
  relabel skips, repeat unchanged tests for a lucky pass or substitute optimistic
  labels for measured latency improvements. Freeze a slice once its relevant
  focused checks and review pass; reopen it for concrete dependent failures.

## Integrated verification

- Reproduce and fix all known issues with cheap focused checks first. Integrate
  source, generated API/assets, canonical Comic and affected skins; collect
  Review 3; then run the expensive suites for that stable candidate. Retain valid
  unchanged evidence and rerun affected checks only when changes, failures or
  unresolved concerns justify it. Never run a separate full suite per issue.
- Complete `go vet ./...`, `scripts/race-shard.sh others` and all eighteen
  `scripts/race-shard.sh client-K 18` selections, K=0–17, at the existing
  600-second race-package bound. `others` includes the current three sequential
  UI partitions; focused UI reruns use `ui-0`, `ui-1`, `ui-2`. Derive current
  registrations and account for every case; preserve passing unchanged
  selections when fixing failures.
- Complete affected native/platform qualification, including actual Linux,
  macOS controls and required Windows paths. Native Windows client qualification
  uses six disjoint `bash scripts/native-client-shard.sh K 6` selections,
  K=0–5, each on its own runner at the existing 1,800-second bound. Do not
  co-locate those client test processes or inflate individual/aggregate limits.
- Verify Android unit/lint/build and affected instrumentation, plus preserved
  upgrade behavior where changed. Preserve identity, native data, chats, files,
  queues, origin/storage and the existing signer. Run the physical Pixel journey
  and actual Mac window cases; build/cross-compile/mock evidence is separate.
- At activation, establish actual Mac/Pixel access and how the candidate will be
  delivered for device checks. Reuse existing owner authorization; ask Sergey
  only for missing device access, delivery or installation authority, once a
  concrete candidate and procedure are reviewable. Device availability must not
  become a late surprise or a reason to stop independent implementation.
- Verify responsive rendered behavior, focus/keyboard/touch, error/retry paths,
  desktop and phone sizes, and relevant light/dark states. Measure latency and
  media resource use on representative office hardware without requiring a GPU;
  record environment/workload and before/after stages. Strong developer hardware
  or a synthetic-only result cannot prove the reported field failure resolved.
- Run the real `TestMixedVersion` against the newest published release below
  the candidate, with tags and `AGENTNET_REQUIRE_COMPAT=1`; a missing-tag skip is
  not a pass. Pin the complete skin collection to the exact candidate and run
  `AGENTNET_SOURCE=<candidate> npm test` for every standalone/overlay skin.
  Update affected contract documents and fix required skin failures before
  publishing either the app or its matching gallery.
- For unavailable physical devices, incident input or infrastructure, record the
  exact missing discriminator and continue all independent work. Keep its target
  open and surface the missing input promptly. No silent deferral, invented
  acceptance or whole-batch completion while a required journey is unverified.

## Completion

Complete only when every S1–A1, Q1, G1 and P1 requirement has artifact evidence, its
corresponding journey and focused regressions pass, Claude's three completed
reviews and root dispositions are collected, the integrated required gates pass
with exact case accounting, v0.9.3 and its matching gallery are public and
independently verified, the backed-up existing-relay rollout is verified, and
owned resources are cleaned up. For verified
shipped behavior, cite the existing implementation and new acceptance result;
do not manufacture code changes merely to mark a row implemented. Update the shared checklist,
HANDOFF and relevant contract/user documentation with exact candidate revisions,
measurements, retained/new evidence and honest limits. Credit actual contributors
in commit trailers and keep logs/build output/private data out of commits.

Report the candidate location/revision, what changed, meaningful performance
results, checks actually run and any remaining open acceptance. Only an explicit
owner scope change can remove a listed requirement. Source readiness, physical
acceptance and published/deployed behavior remain distinct claims.

The owner's later release instruction supersedes this goal's initial candidate-
only boundary. Play Store/shared push-gateway work stays paused.
