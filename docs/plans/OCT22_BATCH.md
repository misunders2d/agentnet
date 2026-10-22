# AgentNet v0.9.3 — October 22 batch and release checklist

Status: active, authorized by Sergey on October 22, 2026. Root Codex is the
sole editor. Scope and acceptance: [goal](OCT22_BATCH_GOAL.md) and
[handoff](OCT22_BATCH_HANDOFF.md). All targets remain required.
The owner's later instruction explicitly includes the next public release,
matching gallery, independent artifact verification and usual backed-up relay
rollout; it supersedes the initial candidate-only boundary.

Candidate: `.agentnet/oct22-batch`, branch `fix/oct22-complete-batch`, based on
`acb798a1`. Prepared Drive fixes integrated as `bf0d49dc` and `d196537c`.
The dirty main checkout and paused Play work are preserved.

States: agreed -> implemented -> verified. Pending assignment is not peer
agreement. Existing shipped behavior may be verified without new product code.
Failures, unavailable evidence and skips remain explicit.

| ID | Target outcome | Acceptance check | Implementer | Verifier | State | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| S1 | Every person's messages/history, reactions, edits/deletions, reads and reminders converge without stale attention or work replay | Existing fixtures plus two ordinary-user native/browser journeys; online, offline, duplicate/reordered history, restart and new-device backfill; all attention consumers | oct22-state | Root / Claude | agreed | Real-Hub two ordinary-person journey passes on six native/browser devices, including offline/restart/stale/duplicate/backfill and deletion. Missing actual browser rem1 ad reproduced and fixed; five-parent race149.243s plus final six-device assertion14.032s; IndexedDB285 checks pass |
| H1 | Read chat is not replayed to native sessions and unrelated backlog does not block Stop | Read sync across linked devices; existing/new/resumed Codex/Claude/Pi sessions; >8 arrivals, filtered cursor progress; preserve decisions, exact-session replies and helper context | oct22-state | Root / Claude | implemented | Read-generation filtering, captured-top cursor progress and unconditional generic Stop suppression; exact selected receivers preserved. Final H1/Q1 35-parent races pass (client262.647s, command22.334s); own-linked task settlement race16.464s |
| R1 | Linux reactions appear without reported delay | Before/after selection, durable API, event and paint timings; add/remove/failure/offline/sync | oct22-interaction | Root / Claude | implemented | Synthetic 24/400-row stale-history reproduction: durable 260/286ms, paint 1567/1245ms before; durable 262/269ms, paint 299/363ms after. Field timing remains open |
| F1 | Group-picture Sending latency is corrected at measured stage | Enqueue/preparation/upload/fanout/custody/delivery/paint timings on representative group/office hardware; offline recipients, retries and concurrent messaging | oct22-storage | Root / Claude | implemented | Faithful 641KiB group fixture: blocked recipient reproduced serial stall; four recipient FIFO lanes yield final enqueue30.1ms and healthy custody69.5ms. Historical device incident not causally proven |
| U1 | Mac toolbar and context menu target the right message at every window size | Actual macOS fullscreen/ordinary/half-width; scroll/reflow/resize/focus/keyboard and rendered responsive checks | oct22-interaction | Root / Claude | implemented | Narrow mouse actions and stale mouse-dismissed focus toolbar RED/GREEN corrected; keyboard targeting retained. Same regression supports WebKit; actual macOS placement qualification still open. Owner has no Mac |
| D1 | Drive small-file throughput improves without starving live traffic | Bounded equivalent delayed-provider before/after files/s and bytes/s with live traffic; stable IDs/restart/backoff/checksum proof | oct22-storage | Root / Claude | implemented | Equivalent delayed-provider workload: 8.81 to final31.64 files/s; new-file remote custody911 to168ms. Four bounded slots, oldest/newest fairness and backoff; no live remigration |
| C1 | Device/queued/relay/Drive storage scope, limits and cleanup are understandable and safe | Existing APIs/settings; persistence/restart, low/full warnings, protected sends/custody/last copy and retrieval after cleanup | oct22-storage | Root / Claude | implemented | Persisted admin quota and conservative confirmed cache controls; native/browser journeys and 320/390/1280 light/dark rendered controls green. Final type check and quota connection-cycle race1.914s pass; client225.928s and Hub30.856s focused races pass |
| E1 | Send failures show one correct actionable reason and preserve retry identity | Quota HTTP 413, disk full, file size, Drive full and network errors through producer/API/UI; recovery without duplicate sends | oct22-storage backend / oct22-interaction rendering | Root / Claude | implemented | Producer codes plus native/browser durable detail and one actionable renderer; exact-ID proven-never-handed-over retry, legacy rows excluded. Rendered member/admin/double-click checks green |
| M1 | Compressed photo/video choices and exact-original file work in shared UI | Review 1 video decision; desktop/Android/browser; original hashes, usable outputs, cancellation, CPU/RAM bounds, CPU-only and offline/retry | oct22-interaction | Root / Claude | implemented | Shared photo/video and exact originals; lazy mediabunny 1.62.0, capability-probed MP4 or VP9/Opus WebM, bounded Linux fallback. Real CPU-only Chrome output/audio and native cap checks green; cancellation/photo orientation and complete180s native video(9.36MB/9.895s conversion) pass; CPU-only ordinary ZenBook synthetic workload |
| D2 | Prepared Drive consent/quiet-refresh fixes work end to end; remote-only files remain recoverable | Real Go OAuth URL -> UI; no background flicker or typing fetches; explicit refresh feedback; migrated download and remote-only backup/restore | Root integrates; storage/interaction helpers verify | Root / Claude | implemented | Prepared cherry-picks integrated; fresh consent/SDK races pass and real OAuth producer plus six rendered refresh cases pass (21.169s). Migrated-file and remote-only backup/restore acceptance remains open |
| A1 | Upgraded Pixel 6 connection status does not create false unread attention | Actual registered channel/recovery, real arrivals/read clearing and reliable delivery; preserve preferences and foreground service | oct22-state verifies existing source | Root / Claude / device operator | agreed | Existing registered-channel inspection, recovery and instrumentation source inventoried. Physical upgraded Pixel access/result remains open |
| Q1 / MEL-497 | Ordinary clarification uses tagged chat and a valid tagged reply continues without a sticky decision gate | Actual group-worker saved-answer/repeated-round regression; ask/tagged-answer/same-context continuation and stale-attention clearing; thirty-minute delay split into queue/start/run/response; true native permission/authorization boundaries preserved | oct22-state backend / oct22-interaction UI | Root / Claude | implemented | Actual group prompt now includes saved continuation rounds; tagged follow-up claim settles only exact matching stale needs_human under normal authority. Actual repeated group-worker, tagged/authority boundaries and own-human linked task claim regressions pass; original attempts1/no duplicate rerun; Linear comment 8f7d6f61-f0c8-4b54-a286-3f30bec024ba |

## Reviews

| Checkpoint | Required result | Reviewer | State | Revision / findings / root disposition |
| --- | --- | --- | --- | --- |
| Review 1 | Minimal state/hook approach, bounded Drive change and practical complete video path | Claude 2f109062-e9c2-4dde-ad01-4c3f9f9221dc | collected; root decisions recorded | Reviewed d196537c; complete marker 69c31ae7; private review/dispositions in .agentnet/oct22-run and .agentnet/herdr |
| Review 1 Q1/H1 addendum | Tagged-chat correction and read-reply exception challenge | Same Claude session | collected; accepted | Complete marker 70c4db19; broad read-reply exception withdrawn, generic Stop never blocks; exact selected receivers preserved |
| Review 2 | Significant implementations reviewed with focused before/after evidence | Existing Claude tab 2 | collected; refinements in progress | Marker 3f9a41d6; duration budget and own-human settle coverage accepted; uploader-only status shortcut declined after API inspection; dispositions in private review2 record |
| Review 3 | Frozen integrated candidate independently reviewed before costly gates | Existing Claude tab 2 | collected; accepted with CI portability fix | Frozen57806d62; marker c6138f2a; grep replaces an unnecessary CI rg dependency; stable enough for integrated qualification; physical/platform/release gates remain open |

## Integrated qualification

Focused checks precede this table. Retain valid unchanged results; rerun only
affected selections after a correction. Never weaken assertions or bounds.

| Gate | State | Exact evidence |
| --- | --- | --- |
| go vet ./... | pending | Not run for this batch |
| Client race shards 0–17, count 18, existing 600s package bound | pending | Not run for this batch |
| Other race packages and three sequential UI partitions | pending | Not run for this batch |
| Required native Linux/macOS/Windows; six Windows client runners at 1800s | pending | No batch native run |
| Android unit/lint/build/affected instrumentation and preserved upgrade | pending | No batch run; physical Pixel access unknown |
| Actual Mac window cases and physical Pixel acceptance | pending | Need device access and candidate delivery route |
| Representative office-hardware latency and CPU-only media behavior | pending | No batch measurement |
| Required real MixedVersion with published tag | pending | No batch run |
| Complete skin collection pinned to exact candidate | pending | No batch run |
| Immutable app release and matching gallery publication | pending | Target v0.9.3; verify next free version before tagging; no publication yet |
| Independent public artifact/checksum/signer/gallery verification | pending | No public batch artifacts |
| Existing relay stopped full backup, old/new restore proof and preserved-data rollout | pending | Not started; preserve connected remote-only Drive, realm, volume, config and neighboring service |
| Documentation, collected reviews and owned-resource cleanup | in progress | All three helper final packets and source hashes collected; canonical assets/types/API docs pass; Review3 then qualification and owned-pane cleanup |

## Authority and preservation

The goal now authorizes the normal qualified release and existing-relay rollout.
Q1's scoped Linear record is owner-authorized; do not resend historical private
diagnostics. Unrelated live resets/configuration/infrastructure and Play/push
work remain excluded. Prepare concrete device candidates/procedures before
requesting genuinely missing install authority. Ignore unrelated AgentNet
backlog. Keep operational details private.
