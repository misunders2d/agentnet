# October 22 batch — handoff for the next session

Updated: 2026-10-22 15:55 UTC. Repository: AgentNet Go revival.

## Owner instruction and required outcome

**The entire batch below is required. Do not choose a preferred subset and
defer the rest.** The owner's latest instruction was: “what do you mean
‘prioritize’? they all need to be fixed. Write a handoff, I'll start a new
session.” Earlier, the owner authorized working on these after restarting.
This turn saves the handoff; it does not start implementation or a release.

The next implementation session should finish all listed fixes/features and
their acceptance journeys. Implementation order is an engineering choice,
not permission to drop requirements. Use cheap focused regressions first,
integrate a stable candidate, then run the costly combined checks. Do not
replace a real latency correction with an optimistic label or close a ticket
because a synthetic test passed.

**Play Store publication, Firebase/shared push-gateway design and deployment
remain paused.** Preserve the saved Play work. The existing Android
false-attention-dot correction remains in this non-Play batch; it does not
authorize reopening the push architecture.

Read AGENTS.md, this handoff and the linked Linear descriptions/comments.
Linear project: [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580).
The issues and comments below were read live on October 22. Some older issue
descriptions still say “unreleased”; compare them with the actual v0.9.2
release evidence before repeating already-shipped work.

## Complete acceptance inventory

| Work | Linear | Required result / current evidence |
| --- | --- | --- |
| Shared state for every user | [MEL-693](https://linear.app/mellanni/issue/MEL-693) | Messages and granted history, reactions, edits/deletions, read/unread state and personal reminders converge across each person's verified linked desktop, Android and browser devices. Read anywhere clears that item's attention elsewhere; complete/cancel clears reminder attention; snooze/move clears the old deadline. Offline/restart/backfill must not resurrect stale state. Test ordinary users, not an admin-only path. |
| Stop stale harness notices | [MEL-692](https://linear.app/mellanni/issue/MEL-692) | Already-read chats must not be re-announced independently to every native session. Ordinary unrelated chat backlog must not block a harness's unrelated work at Stop. Preserve exact session-bound replies, unresolved decisions and invited helpers' context. Confirmed cause below; no patch yet. |
| Reaction delay | [MEL-687](https://linear.app/mellanni/issue/MEL-687) | Owner still observes about 10 seconds before a reaction appears on Linux after v0.9.2. Measure selection, durable action/API, event and paint independently; correct the delaying seam and preserve add/remove, failure and sync behavior. No root cause established yet. |
| Group picture send delay | [MEL-547](https://linear.app/mellanni/issue/MEL-547) | A picture stayed Sending about 30 seconds on ThinkPad. The composer was already empty; this specific report is not the older retained-input symptom. Distinguish enqueue, upload, per-device fanout, custody/delivery and repaint. The exact send eventually delivered; cause/duration by stage remain unmeasured. |
| Mac message controls | [MEL-681](https://linear.app/mellanni/issue/MEL-681) | Toolbar appears several rows above the hovered message. Separately, context menu cannot be opened in a non-fullscreen window about half the screen wide. Verify scrolling, reflow, resizing, focus and exact action target in fullscreen/ordinary/half-width windows. Shared cause is unproven. |
| Bulk Drive throughput | [MEL-682](https://linear.app/mellanni/issue/MEL-682) | Improve small-file migration throughput while keeping new attachments and messaging responsive. Measure representative files/s and bytes/s; use bounded work, fair queue progress and provider backoff. Preserve stable IDs, resumability, encryption and remote checksum proof before local removal. The owner's migration finished; this does not resolve the performance requirement for other/busy relays. |
| Storage management | [MEL-683](https://linear.app/mellanni/issue/MEL-683) | Understandable device-cache/unsent/spool management separately from administrator-controlled relay quota, relay-local and Drive usage. Persist supported limit changes, explain overlapping counts and warn before full. Protect unsent custody and the last recoverable copy; do not introduce destructive cleanup by default. |
| Storage-full send errors | [MEL-684](https://linear.app/mellanni/issue/MEL-684) | Replace generic Not sent / duplicated status with the real reason and a useful admin/member action. Distinguish logical quota, disk full, file-size limit, Drive full and network failure. Preserve text/files and exact retry identity. Real HTTP 413 relay-quota failure was verified; no presentation fix yet. |
| Compressed media and originals | [MEL-685](https://linear.app/mellanni/issue/MEL-685) | Implement photo/video sending choices with an exact-original-as-file option. Process before encryption on the sender, keep UI responsive, bound CPU/RAM, support no dedicated GPU and cancellation. Keep documents/archives unchanged. Verify original hashes, media usability and offline/retry. Do not silently omit video or defer the feature because the older ticket called it a design follow-up. Routine implementation choices belong to the implementer; justify any necessary dependency. |
| Drive refresh and consent fixes | [MEL-686](https://linear.app/mellanni/issue/MEL-686), [MEL-677](https://linear.app/mellanni/issue/MEL-677) | Integrate the two prepared commits below. Background refresh must not flicker/disable the button; typing/presence must not fetch storage; manual refresh retains feedback. Canonical Web OAuth URL must pass the real producer-to-UI contract. Retain useful setup/error guidance and verify migrated-file retrieval. |
| Android false notification dot | [MEL-675](https://linear.app/mellanni/issue/MEL-675) | Verify the existing registered-channel recovery on the reported upgraded Pixel 6 installation. Background connection status must not impersonate unread message attention. Preserve the user's channel choices and reliable delivery; do not remove a required foreground-service notification or build new push infrastructure. Source/compiled/mock checks exist; physical Pixel acceptance remains unverified. |

Existing implementation to reuse and verify within shared state:
[MEL-670](https://linear.app/mellanni/issue/MEL-670) reminder replication,
[MEL-674](https://linear.app/mellanni/issue/MEL-674) prompt local read/reminder
attention including archived-topic aggregates,
[MEL-561](https://linear.app/mellanni/issue/MEL-561) exact own-device read sync,
[MEL-477](https://linear.app/mellanni/issue/MEL-477) reaction semantics, and
[MEL-558](https://linear.app/mellanni/issue/MEL-558) linked-device history.
MEL-642 is the broad interaction-performance parent; MEL-489 contains existing
attachment/storage-lifecycle requirements. Do not substitute those broad old
tickets for the concrete acceptance above or reopen their entire history.

## Confirmed hook defect: start from the existing seam

`internal/client/attention.go` explicitly describes a cursor per harness and
session independent of read state. `arrivalsAfterReceiver`,
`countArrivalsAfterReceiver` and `recentArrivals` do not filter read rows.
The result is old read chat being drained again in batches of eight, plus
recent read history in new/resumed-session overviews.

`TestAttentionPerSession` in `internal/client/attention_test.go` explicitly
marks messages read and then requires sessions still to be notified. That
expectation is obsolete under the owner's MEL-693 rule. In
`cmd/agentnet/hook.go`, generic nonempty attention at Stop becomes
`decision=block`, causing the screenshot's unrelated task interruption.
These files were compared with v0.9.2 and were unchanged.

Use a deterministic regression before the correction: read a normal chat on
one own device, synchronize that exact state, then exercise multiple existing,
new and resumed Codex/Claude/Pi sessions without replaying the read item or
blocking unrelated completion. Include a backlog larger than one batch and
correct cursor progress after filtering. Preserve genuinely unresolved
decisions and exact selected-receiver delivery; reading never accepts or runs
work. Do not clear the live inbox or disable every hook as the product fix.

Relevant existing seams: `internal/client/readsync.go`,
`internal/client/remindersync.go`, `internal/ui/livereminders.go`,
`internal/ui/web/src/store.ts`, `internal/ui/static/engine.mjs`, and the
selected-receiver attention tests. No product patch or new regression for
MEL-692 has been written in this session.

The owner told this session to ignore incoming AgentNet messages/hooks. They
are the bug being reported, not a reason to read unrelated group chat while
resuming this batch. No inbox backlog needs to be read to reconstruct scope.

## Source, release and preservation state

- Published app: [v0.9.2](https://github.com/misunders2d/agentnet/releases/tag/v0.9.2),
  immutable tag/source `628f30dbcbaff103a6edff6657a03dd2b1efbe7e`.
- Published gallery: [v2026.10.22.1](https://github.com/misunders2d/agentnet-skins/releases/tag/v2026.10.22.1).
  Release qualification/cutover is finished; see `V0_9_2_RELEASE.md`.
  Do not restart the old v0.9.2 release investigation.
- Main checkout HEAD and origin/main were synced to
  `acb798a143a3fdc5a20f1954d688969cd464737c` (release documentation child).
  **It is deliberately dirty with held Play work and owner notes.** Do not
  reset, clean, blanket-stage or publish that work as part of this batch.
- A private content-verified checkpoint is recorded in
  `.agentnet/active-main-sync.json`. All 2,075 snapshot files were checked
  against the reconciled tree during sync. Only nine reviewed reconciliation
  paths changed; held work was retained unstaged.
- Local safety ref:
  `checkpoint/before-main-sync-20261022T103824Z`, WIP commit
  `1ca61079062c5bed9f8240d6fc820ab5ea66cea5`. **Do not push this private WIP.**
  Checkpoint files: `.agentnet/checkpoints/20261022T103824Z-before-main-sync/`.
- Preserve `.agentnet/checkpoints/2026-10-21T153550Z-play-store-paused/` and
  `docs/plans/ANDROID_PLAY_READINESS.md`, `ANDROID_PLAY_STORE_GOAL.md`,
  `ANDROID_NOTIFICATION_TAP.md`, and `MARK_UNREAD.md`. The latter two are owner
  drafts, not permission to silently implement/rewrite their unrelated scope.
- The new shared-state owner rule is already saved in the working AGENTS.md.
  This handoff and its entrypoint update are also local documentation changes.

Use an isolated candidate worktree from clean released main and integrate the
explicit prepared fixes. Inspect current state again before mutations; other
sessions can share the repository. Never copy the entire dirty root checkout
into the non-Play candidate.

## Prepared fixes: reuse, do not reimplement

Clean worktree: `.agentnet/drive-consent-fix`.
Pushed branch: `fix/drive-consent-endpoint`.

1. `6fe3099ed293bb3e11f65fa7050474d459422f5a` — Web consent now emits
   `/o/oauth2/v2/auth`, matching the UI validator, instead of the SDK's
   `/o/oauth2/auth`. Desktop OAuth is unchanged. Hub RED/GREEN and a real
   Go-produced URL consumed by the UI model are covered.
2. `bf6b399a30bb63a87c91a9c73929c6ed00d28507` — quiet background Drive
   status refresh, ignore typing/presence hints, keep explicit loading
   feedback. Source component, rendered regression, canonical Comic bundle
   and checksum manifest are committed.

**Both are pushed source, not released/deployed fixes.** Worktree was clean
and matched its remote on the handoff check. Focused race checks for SDK,
Drive Hub and native/browser adapters passed, along with vet. The refresh
regression failed before the change and passed at 1280/390/320 in light/dark
themes; canonical build/type checks and focused ui/static tests passed.
Synthetic screenshots are under `/tmp/agentnet-drive-flicker-green/`; `/tmp`
may disappear after reboot. Do not call retained evidence a new run.

## Live Drive and ThinkPad findings

- One administrator connected **one Drive for the entire relay**. Members
  need no individual Google account. This is encrypted attachment custody,
  separate from plaintext Project Space exports.
- Web OAuth operator configuration, test-user consent and Drive API enabling
  were completed. No credential values belong in the handoff, commits, issues
  or build logs. A Google Testing configuration's long-term token/reconnect
  behavior remains an operational follow-up; do not disconnect remote-only
  storage to experiment.
- The owner later showed completed migration: **7,971 files, about 1 GiB in
  Drive, zero relay-local copies**. This is screenshot evidence, not a fresh
  independent backup/download audit. Do not queue the entire migration again
  just to reproduce the performance complaint.
- Those are server blobs, including internal encrypted history-sync archives.
  They do not include the chat SQLite database or the ThinkPad's local cache.
- The quota incident was real: the configurable default **1 GiB relay logical
  blob quota** was full despite free disk. It was persistently raised to
  **10 GiB**, verified through the signed client API. Remote Drive blobs still
  count toward that logical quota. This is not a 1 GiB device-cache limit and
  connecting Drive does not mean unlimited storage.
- Sending an attachment durably stages ciphertext on the relay;
  `storeCompleteAttachment` queues `runAttachmentDrive` asynchronously. The
  upload-complete request does not wait for Google transfer. Local copies are
  removed after remote custody verification. Authorized downloads use the
  local copy or stream from Drive through the relay, decrypting on the client;
  members download from the normal chat without a Drive account/public link.
- `runAttachmentDrive` currently chooses one blob at a time. The initial real
  backlog was dominated by small blobs (about 7,049 below 64 KiB out of 7,890).
  Provider round trips matter; no sustained-throughput benchmark or root cause
  for the later 30-second send has been established.
- Read-only ThinkPad metadata identified the reported group-image send: about
  641 KB with six recipient-device copies. All were delivered without errors
  at inspection, with no pending upload rows. Available rows did not preserve
  per-stage timestamps. **Eventual delivery is proven; the ~30 seconds is the
  owner's observation, and its cause is still unknown.** No resend, live DB
  mutation or application restart was performed during that inspection.

Minimized private operational/incident details are saved locally in
`.agentnet/v092/drive-operator-setup.json`, not in committed documentation or
external issue payloads. Do not dump that file or credentials into logs.
An automatic approval review rejected sending detailed internal send IDs,
topology and operational diagnostics to Linear; the ticket was successfully
updated with the owner's bug report after removing those details. Do not
retry exporting that rejected payload as a workaround.

Useful source entry points: `internal/hub/attachmentdrive*.go`,
`internal/gdrive/ciphertext.go`, `internal/client/files.go`, `sendgroup.go`,
`backgroundsend.go`, `directsend.go`, `delivery.go`,
`internal/ui/web/src/model.ts`, `features/RelayAttachmentStorage.*` and the
existing upload/fairness/read/react/browser/rendered regressions.

## Working method and completion

1. Confirm local HEAD/worktree state, read full live requirements and make an
   acceptance checklist for **every row**. Map existing shipped pieces and
   prepared commits before editing. Do not recreate already-shipped reminder,
   SQLite filename, private topic-name or reminder-date corrections simply
   because an old Linear status is stale.
2. Reproduce each remaining defect cheaply and fix the measured seam. Batch
   the source changes before costly qualification. No timeout increases,
   assertion weakening, polling, replacement sync framework or new service.
3. Owner previously allowed Claude workers on **Opus-5.5-xhigh**. If useful,
   read the Herdr/agent-collaboration skills and verify the native caller and
   workspace identity first. The earlier launcher check failed identity
   verification and launched nobody. Do not adopt a guessed pane or invent
   Claude contributions. No helper is currently running for this batch.
4. Run one integrated qualification after the candidate is stable. Follow
   current AGENTS.md: vet; all 18 client race shards; others with **three UI
   partitions**; native/platform checks for affected paths, including real
   Mac behavior and Pixel channel verification. Retain valid unchanged passes
   and report opt-in/platform skips as skips. A powerful dev machine alone
   does not prove responsiveness on an ordinary office laptop without a GPU.
5. Before any release, complete the real MixedVersion gate and the complete
   skins gate pinned to that exact candidate, plus required release/native
   checks. Do not reuse historical obsolete partition counts or time bounds.
6. Completion requires implementation plus the corresponding user journey
   and honest evidence for all rows. Distinguish source pushed, tests passed,
   physical-device verification and published/deployed behavior. If an input
   or platform is genuinely unavailable, expose the exact remaining check;
   do not silently defer it or claim the whole batch fixed.

Writing this handoff did not run product tests, modify live messages, activate
a goal, launch workers, publish a release or resume Play Store work.
