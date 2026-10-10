# Claude handoff: mobile failures after v0.8.16

October 10, 2026. **The owner stopped Codex implementation and assigned the work
to Claude. All three GPT-6.1-sol helpers have stopped. No release or deployment
is in progress. The checkpoint below is unfinished, not a release candidate.**

## Repository and safe starting point

- Current branch: `fix/mobile-startup-after-v0816`, local WIP checkpoint above
  `548d135be58af3b888a2b0a14279a5c67082c059`.
- Local `main`, `origin/main` and `release/v0.8.16` were aligned at `548d135b`.
  The WIP is not merged into main, tagged, pushed or deployed.
- Published v0.8.16 tag: `45e38feea7b7e7ae9454247b2fdda28b362ceea6`;
  application source: `14d214dd384dc453de0113ee122de6aa22cce592`.
  Existing Contabo relay was deployed overnight. Publication/qualification
  evidence remains in [V0_8_16.md](plans/V0_8_16.md).
- Clean-up during handoff removed **24 stale worktree registrations** whose
  directories no longer existed. Only the primary checkout remains registered.
  Local main was fast-forwarded from `7e0ea2ef`; two fully merged historical
  helper refs (`fix/v089-antigravity`, `fix/v089-model-report`) were deleted.
  Other historical fix/release branches remain: many were cherry-picked and
  are not ancestors of main. Do not blindly delete or cherry-pick them.

## What the owner actually reports

The phone is updated, but the user-visible outcome is still broken:

1. More than 20 seconds on the **Comic Chats skeleton**, before any saved chat
   appears. The latest screenshot shows mounted navigation with placeholder rows.
2. Replies created minutes earlier are absent on the phone. The October 10
   09:49 Kyiv screenshot shows the 09:36 request "morning" as Delivered with no
   reply; earlier requests exhibit the same symptom.
3. Hundreds/thousands of held notices remain, including a screenshot with groups
   of 514 participation-binding mismatches and 320/2/109 missing-proof records.
   These counts are envelopes/sync records, not proven distinct chat messages.
4. Amazon_team agent sandbox still says Waiting; the Valerii/SKU topic still
   lists old requests as pending, including requests 7 through 14 in the latest
   screenshot. Do not infer completion from unrelated answer text or rerun jobs.
5. The owner asks why these internal failures are in OKs with no useful action.
   Archiving or hiding them is not a repair. Recover valid records; preserve
   invalid records and show a concise actionable service status when necessary.

These are recurrences of the same reported failures. Prior green tests and
component fixes **did not establish the phone outcome**. Keep MEL-546, MEL-558,
MEL-580 and MEL-588 open. User asked to keep recording bugs in Linear; current
report/screenshot evidence and synthetic measurements were added this morning.

## Two reproduced startup bottlenecks

### A. Notification discovery blocks saved chats

The actual device -> loader -> Comic -> real IndexedDB path with 500 generated
saved messages reproduces the empty list when relay requests are held:

`Store.reload -> Engine.overview -> notifyView -> notifyInfo -> features -> /v1/version`

The version request is also behind `workspaceRealmFetch`. Its 30-second bound
can block local UI data even with notifications disabled. This is not a wait in
`device.start` itself. The previous bootstrap test stubbed Engine.load/API and
treated script load as mounting; it did not prove first useful paint.

Measured scratch trace: normal first chat 916 ms; version held 3 seconds ->
first chat 3,156 ms (notifyView spent 2,591.7 ms); cached DM projection had already
finished at 480.6 ms. Holding skin-catalog fetch separately also delayed startup
to 3,689 ms. Asset loading remains an independent limitation, not repaired here.

Partial fix in `internal/ui/static/engine.mjs` makes notifyView use cached
feature/notification information and report unknown honestly. notifyInfo notifies
existing listeners after an offer arrives. Actions/reconnection retain actual
network checks. Review the asynchronous refresh and unknown/unsupported cases.

New files:

- `internal/ui/testdata/notify_cached_view_check.mjs`: Node check passed.
- `internal/ui/testdata/device_startup_rendered.cjs`: actual bootstrap, loader,
  Comic and real IndexedDB with generated data and held/offline relay routes.
- `internal/ui/device_startup_rendered_test.go`: wrappers added; not yet run
  through Go or checked with gofmt at handoff.

**Rendered regression is not passing.** Before the fix it failed at the empty
Chats list (5-second assertion). After the notification edit, overview completed
at 779 ms and the opened DM at 1,217 ms; all 500 generated messages were in the
synthetic DOM. The test still failed trying to make the final row 499 visible via
`[data-card]`. Correct the first-visible-message/count or intentional-scroll
assertion without lengthening the deadline. Offline mode was not reached.
The trace predates the later unfinished read-view edit below.

Logs: `/tmp/agentnet-mobile-startup-{before,after,real}.log`.
Scratch harness: `/tmp/agentnet-mobile-startup-real.cjs`.

### B. Overview repeatedly reads and decorates all conversations

Real desktop Chromium + real IndexedDB, generated persisted rows, no network:

| Fixture | Overview | Other evidence |
| --- | --- | --- |
| 1,500 inbox + 300 outbox, 10 DMs, 512-byte bodies | 3.862 s | load 6.1 ms; one chat 272 ms |
| 5,000 inbox + 1,000 outbox, 30 DMs | 43.388 s | load 20.9 ms; one chat 939 ms |

The larger overview made 393 full inbox reads (1.965 million materialized rows),
573 full outbox reads (573,000 rows), and 7,971 person-table reads. Overview
constructs complete DM/group views for list previews; helper chains repeatedly
read the same tables and participation data. Prior tiny/single-DM fixtures missed
this scaling. These are synthetic measurements, not phone timings.

The WIP adds explicit `readView()` data reuse through overview, dm, groupThread,
convMessages, peerWordsFn, personOfFp, authorityRows, convEvents, dmMembers,
participationsOf, agentsOf, proposalProvenance and topic/thread summaries.
Intended boundary: one display operation only; no persistent cache/global mode,
schema or wire change. Action/admission paths must keep current reads and CAS.

**This edit is partial and unverified.** Only `node --check engine.mjs` passed.
No response parity, authority, performance or integrated checks ran afterward.
Root already spotted a concrete issue to fix first: inside `dm`'s `ctlView`,
the new `personOfFp(targetFp, view)` references the inner `const view` before its
initialization, shadowing the outer read context. Syntax checking misses this.
Do not merge or deploy the checkpoint as-is.

Scale probes/logs: `/tmp/agentnet-mobile-data-profile.mjs`,
`/tmp/agentnet-mobile-data-profile-runner.mjs`,
`/tmp/agentnet-mobile-data-profile.log`.
Runner uses `/usr/bin/chromium` and the existing temporary playwright-core at
`/tmp/agentnet-v0814-ui-tools/node_modules/playwright-core/`.
The runner was subsequently changed to a mixed fixture; restore `{n:5000,
conversations:30}` for the recorded large baseline. Its attempted two-group
fixture omitted `g.records`, so groups were excluded: **not valid group evidence**.
Build valid signed group/participation fixtures before claiming coverage.

## Missing reply and held-record diagnosis

Read-only inspection confirmed the exact morning request was answered locally
in approximately seven seconds. Its answer and two status controls remained
in Hub **custody**, not delivered to the phone. V1 request/answer physical IDs
match exactly; neither has conv/LID/topic fields, so that local case is not an
established logical-ID or hidden-topic mismatch. Phone admission/rendering is
still untraced. Do not equate custody with delivery.

Exact IDs, timestamps and private local aggregates are kept only in
`/tmp/agentnet-claude-private-diagnostics-20261010.md`, not this public-ready doc.
No database/private-key copy was made. Automatic approval review rejected posting
private local DB counts to Linear; the safe owner-supplied screenshot report was
posted instead. Do not upload private diagnostics without specific authorization.

The local terminal executable reports v0.8.14. This does **not** establish the
running app/backend version: the command tool's process namespace exposes only
its own processes. The app registration points to the permanent
`~/.local/share/agentnet/AgentNet.AppImage`, not Downloads. Verify actual runtime
separately before attributing retained failures to an old installation.

Relevant code: `internal/client/lifecyclehistory.go`, `historyrecovery.go`,
`history.go`, `worker.go`, `conversation.go`, browser `heldHistoryChecks`,
`retryPasses`, `admitHistory`, `dmLifecycleHistorySource`, `pendingTopicRows`,
`summarizeChatTopicView`, `chatTopicSummaries`, `threadSummaries`.
One-time recovery eligibility and repeat-copy production still need tracing;
do not assume a historical migration completion means new valid records converge.
Amazon_team and Valerii pending-state roots are not resolved by this handoff.

## Native app question and team discussion

Three GPT-6.1-sol helpers discussed the current architecture. Recommendation:
repair these demonstrated foreground defects first. A native WebView wrapper
would retain the current JS/IndexedDB work. A native client sharing the existing
Go messaging core could improve lifecycle/push/storage integration and avoid two
protocol implementations, but requires a mobile library boundary and qualification;
desktop's separate Go sidecar is not automatically portable to phones.
The owner has asked about native, not approved an unbounded rewrite.

Primary references inspected:

- [TDLib architecture](https://core.telegram.org/tdlib) and
  [history/updates](https://core.telegram.org/tdlib/getting-started).
- [Apple background notification limits](https://developer.apple.com/documentation/usernotifications/pushing-background-updates-to-your-app).
- [Android Doze](https://developer.android.com/training/monitoring-device-state/doze-standby)
  and [FCM priorities](https://firebase.google.com/docs/cloud-messaging/android-message-priority).
- [WebKit Web Push](https://webkit.org/blog/12945/meet-web-push/).
- [Go mobile bindings](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile).

Background push is a wake hint, not guaranteed delivery or authority. Keep
content-free push and device-held keys. Any browser-to-native enrollment/migration
must preserve the existing phone and require an explicit plan.

## Next steps and constraints

1. Review or discard the unfinished read-view portion against the proven baseline;
   fix its shadowed variable before executing the candidate.
2. Complete the actual-startup regression and correct scaling fixtures. Measure
   first visible saved chat while relay/version/notification/recovery waits.
3. Independently verify exact response parity, current authority after changes,
   delivery/ordering/restart behavior and current group/participation projection.
4. Trace the exact missing reply and proof backlog on the phone. Existing local
   native counts are not a substitute for phone storage/stream evidence.
5. Fix notice causes and stale request state; expose useful status/action without
   suppressing invalid evidence, inventing completion or running old requests.
6. Batch focused checks, then run affected integrated/native release gates once
   stable. Prior v0.8.16 green suites do not qualify this edited engine.
7. Verify actual owner-phone behavior before claiming resolution. No new tag,
   published fix, migration or physical improvement is claimed here.

Preserve encryption, signed authority, identities, history, native permissions
and live sessions. No reset/relink/purge, automatic task replay, live grant edits
or user-client restart. Deployment authority covers the existing relay only;
clients update through their controls. Use Linear directly, not Orca; Herdr for
peer coordination, not AgentNet. No further Codex implementation continues after
this handoff. The original investigation checklist is
[MOBILE_STARTUP_FOLLOWUP.md](plans/MOBILE_STARTUP_FOLLOWUP.md).
