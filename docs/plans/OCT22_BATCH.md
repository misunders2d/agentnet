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
| S1 | Every person's messages/history, reactions, edits/deletions, reads and reminders converge without stale attention or work replay | Existing fixtures plus two ordinary-user native/browser journeys; online, offline, duplicate/reordered history, restart and new-device backfill; all attention consumers | oct22-state | Root / Claude | implemented | Real-Hub two ordinary-person journey passes on six native/browser devices, including offline/restart/stale/duplicate/backfill and deletion. Missing actual browser rem1 ad reproduced and fixed; five-parent race149.243s plus final six-device assertion14.032s; IndexedDB285 checks pass |
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

| G1 | Claude reviews every GUI area and interaction; all demonstrated gaps corrected for smooth direct chat UX | Complete reachable control/state/role inventory; rendered wide/narrow/light/dark, keyboard/pointer/touch/Back/focus/loading/offline/error/recovery and every gallery skin; independent recheck of corrections | Root integrates / existing helpers | Existing Claude tab 2 | in progress | Comprehensive review oct22-gui-review active; private shared GUI checklist, prior source reviews do not replace rendered coverage |
| P1 | Every interaction responds promptly on every supported system | Visible-feedback, durable API, projection/paint and frame measurements for all reachable interaction families on real Linux/macOS/Windows shells, Android WebView and browsers; fix demonstrated delays, retain raw timings and distinguish emulator/physical | oct22-interaction fixtures / Root CI | Root / Claude | in progress | Seventeen portable workloads extend all16 family groups; current role/state map and174 prior raw actions retained. New 1000-person invite cost is being corrected; routine native/browser timings above200ms and actual complete system cells remain open |

## Reviews

| Checkpoint | Required result | Reviewer | State | Revision / findings / root disposition |
| --- | --- | --- | --- | --- |
| Review 1 | Minimal state/hook approach, bounded Drive change and practical complete video path | Claude 2f109062-e9c2-4dde-ad01-4c3f9f9221dc | collected; root decisions recorded | Reviewed d196537c; complete marker 69c31ae7; private review/dispositions in .agentnet/oct22-run and .agentnet/herdr |
| Review 1 Q1/H1 addendum | Tagged-chat correction and read-reply exception challenge | Same Claude session | collected; accepted | Complete marker 70c4db19; broad read-reply exception withdrawn, generic Stop never blocks; exact selected receivers preserved |
| Review 2 | Significant implementations reviewed with focused before/after evidence | Existing Claude tab 2 | collected; refinements completed | Marker 3f9a41d6; duration budget and own-human settle coverage accepted; uploader-only status shortcut declined after API inspection; dispositions in private review2 record |
| Review 3 | Frozen integrated candidate independently reviewed before costly gates | Existing Claude tab 2 | collected; accepted with CI portability fix | Frozen57806d62; marker c6138f2a; grep replaces an unnecessary CI rg dependency; stable enough for integrated qualification; physical/platform/release gates remain open |

## Integrated qualification

Focused checks precede this table. Retain valid unchanged results; rerun only
affected selections after a correction. Never weaken assertions or bounds.

| Gate | State | Exact evidence |
| --- | --- | --- |
| go vet ./... | verified on frozen9b | All three native jobs pass vet in [39827811802](https://github.com/misunders2d/agentnet/actions/runs/39827811802) |
| Client race shards 0–17, count18, existing600s package bound | 14 original passes retained; focused corrections pass; integrated replacement pending | Same run retains0–3,5,7–10,12–15,17;4/6/11/16 fail known queued-output/prompt/error assertions.All four failed selections now have bounded focused regular/race corrections, including the causally reproduced catalog publication race; new1421-parent registration changes partition membership, so qualify the current18 selections |
| Other race packages and three sequential UI partitions | partial; affected replacement pending | Original non-UI others fails two obsolete hook tests; corrected CLI peek/read assertions pass focused races. Other original package results are retained; current three UI partitions remain required |
| Required native Linux/macOS/Windows; six Windows client runners at1800s | partial; failures batched | Same run: Windows client1/2/3/5 and all three desktop builds pass. Native jobs and Windows client0/4 fail collected assertions; Windows Drive backoff fixture assumption was corrected using provider start timestamps; actual affected Windows rerun remains required |
| Android unit/lint/build/affected instrumentation and preserved upgrade | original build verified; corrected upgrade/expanded instrumentation pending | Same run signed build, lint, unit tests, prior checksum/signer checks pass. Snapshot assertion now matches exactly exported200;17 actual WebView workloads are staged independently. Current emulator instrumentation has not run |
| Actual Mac window cases and physical Pixel acceptance | Mac rendered check passed; physical open | Same run actual macOS headed WebKit TestMessageInteractionRendered17.67s passes normal/fullscreen/half-width controls. Native shell/full interaction timings remain open; owner has no Mac. Physical upgraded Pixel remains open |
| Representative office-hardware latency and CPU-only media behavior | selective checks passed; P1 open | ZenBook i7-1165G7 CPU-only actual photo/video/frame measurements retained; complete all-interaction/native-system timing coverage in progress |
| Required real MixedVersion with published tag | exact result accounting pending | Linux native run requires compatibility, but aggregate itest fails hooks; collect named result/replacement without reporting skips as passes |
| Complete skin collection pinned to exact candidate | in progress | Isolated gallery pinned9b; GE1 focused8/8 passes and all five rebuilt. Full collection/previews/published synchronization waits for GUI source stability |
| Immutable app release and matching gallery publication | pending | User explicitly requires public v0.9.3 by morning; next free version recheck before immutable tag; no publication yet |
| Independent public artifact/checksum/signer/gallery verification | pending | No public batch artifacts |
| Existing relay stopped full backup, old/new restore proof and preserved-data rollout | pending | Preserve connected remote-only Drive, realm, volume, config and neighboring service; no live change yet |
| Documentation, collected reviews and owned-resource cleanup | in progress | Review1/2/3 and original helper packets collected; new G1/P1 and CI followups active. Root retains owned panes until current outputs collected |


## Authority and preservation

The goal now authorizes the normal qualified release and existing-relay rollout.
Q1's scoped Linear record is owner-authorized; do not resend historical private
diagnostics. Unrelated live resets/configuration/infrastructure and Play/push
work remain excluded. Prepare concrete device candidates/procedures before
requesting genuinely missing install authority. Ignore unrelated AgentNet
backlog. Keep operational details private.

## GUI review corrections after the initial qualification

The original9b qualification is retained by exact result; no new expensive full
run is accepted while the demonstrated GUI corrections are still in progress.
Claude pass1 and continued remaining review found real input/state defects:
long-press release activating the sheet, Escape/composer modes, unread entry
and WebKit late-height anchoring, delayed reaction feedback, search/Back/focus,
confirmed batch cancellation on close, schedule navigation, moved destination
names, coarse unread burst ordering, guest event wording and short-window menus.
Focused actual component/native/browser regressions precede the next stable gate.
The private root GUI checklist records each finding/disposition/evidence; the
accessibility-name finding was withdrawn after real AX inspection and its changes
removed. Existing own-agent invite closure is proven, with no invented repair.

Collected new packets: Q1 UI91c64b0e (actual desktop/Android ordinary Attempt0 plus
16 rendered cases/32 sends); reaction27b0c39e (16 boundaries, race14.457s,7–32ms local
pending while API is held); topic destinationc719f2b8 (native/browser original
anchor, both directions/reload/inverse/replay and race); schedulea710e45c (existing
journal intact, chat stays visible, exact-ID/file/audience recovery and race).
Root actual navigation keys/panel Escape320/390/1280 pass15.023s; browser Back and
three standalone registered handlers pass, with full gallery requalification open.
Unread subsecond native/provider correction retains a173.82s parent race pass from
an aggregate180s timeout, then executes only remaining parents in50.834s. The
aggregate timeout remains recorded; it is not represented as a passing command.

Complete all-interaction speed remains open: the old400-row unread mount is a
measured0.6–1.2s defect. Initial24-group rendering, explicit80-group paging, real-height containment and
shared timestamp formatting are integrated. Raw native/browser feedback above200ms cannot be
called smooth merely because a2s catastrophic guard passes. Root prepared actual
Linux/macOS/Windows shell and named browser runners plus owned Android WebView
staging, not yet dispatched. Physical upgraded Pixel acceptance remains open.

Release preparation read-only preflight confirms currentv0.9.2/same realm and
connected remote-only Drive. A subsequent metadata count is8343files/1.085GB.
Automatic approval rejected optional backup prefetch that could copy OAuth config;
that approach is abandoned. Use the existing native full stopped backup in its
original volume, which excludes OAuth configuration, for the authorized rollout.
No live app change, public tag or release has been made yet.

The final GUI asset refresh passes strict TypeScript; Comic entry SHA256 is
`3847a12ec2d89a188dae389ef959d90b646dd138fde7b7c4a05a00715c234e30`.
Integrated `go vet ./...` passes. Four previously failing opt-in GUI parents pass
together in54.156s, with exact public notification routing for unbound historical
requests and the pending-invitation prerequisite preserved. P7 person grants pass
1280/390. Four additional trust/model/onboarding/gallery parents pass together
in54.896s, including exact keys/harnesses, retired-model exclusion and fresh
consent for changed gallery code. Workspace reconnect normal passes7.949s; a
subsequent aggregate race retains six parent passes but times out in that fixture.
The causal fixture-only subscription correction and final normal/race evidence
are recorded in the corrective-child section below; original failure is retained.

Claude remaining-review marker603ed19a is collected. It disclosed that its main
interactive worlds used dirty main rather than the candidate. All those coverage
rows and timings are excluded from candidate acceptance; clean-candidate fixtures
and explicit9b reproductions remain distinct. Final exact-frozen-source review
must rebuild/fingerprint and re-establish every GUI row. Its original New-topic/
Reply finding did not reproduce on9b; the independent actual Composer regression
and current hidden-mode/escape protections retain their own evidence.

Guest event packetf02a48d6 proves human group/name/room and structured lifecycle
semantics, including final native Overview preview (six final races89.019s),
browser712checks and four rendered native/browser×wide/narrow cases. P1 packet
21c5a94e freezes product changes but does not close performance acceptance: its
174 raw action observations and above200ms results are retained. The same portable
runner is being extended test-only to the remaining reachable interaction families.
See [release qualification record](V0_9_3_RELEASE.md) for still-open gates.

## Corrective child after6a6d3fcb

The final workspace fixture failure is now causally distinguished: its fake skin
discarded the host.listen disposer on every mount, leaving six SSE responses
occupying Chromium's HTTP connections. The stale overview lookup before reconnect
then never reached the fake server. Listener disposal corrects this test-only
seam; normal8.941s and race11.037s pass at the original bounds, with cleanup before
the success marker. All16 assertions remain. Deliberate stuck-evaluation and
cleanup faults exit nonzero and preserve the original error. The six earlier
aggregate race passes are retained rather than rerun.

Speed extension found another actual source defect: the group invite picker
mounted1000people at once (2018.5ms feedback). Bounded40-row per-kind display,
explicit reveal, complete search/reset and out-of-page exact preselection pass
33 focused functional actions. A richer1000people+45agents workload still takes
1003/1071ms before useful paint; its synchronous pre-effect cause remains under
investigation. This is a different workload, not a claimed equivalent speed
comparison or no-lag pass. Full qualification waits for this demonstrated seam.

The corrective product source is frozen after the accepted roster and setup
checks. The canonical build (including strict TypeScript) and `go vet ./...`
pass; Comic entry SHA256 is
`b97e4b62ce084678205c048f630c643e4d7ae951f10cf05c01fdcd8ebe6c24cc`.
The six changed component/model files stayed byte-identical throughout the build.
The new-group form retains every eligible person with first40/reveal40/full
name-or-device search: paired opening201→74ms wide and174→39ms narrow, with
initial selections and all1001 choices preserved. The accepted people/device
selectors retain exact choices and pending disabling; final wide create348→180ms
and reopen199→59ms. Extra fieldset and presence-index experiments were rejected.

Setup review uses prepared-focus activation rather than claiming a synthetic
DOM click supplies native pointer focus. An isolated control disproved the first
focus-removal proposal, which was reverted. Only the real offscreen refusal and
Back-context gaps were corrected: the same error appears below the heading, and
Back restores the exact Connect opener. Both widths pass reviewed-token/harness,
visible refusal and exact returned agent-ID checks, with17–24ms feedback.
Topic typing/Clear feedback is5–11ms; results still complete after the existing
250ms debounce (262–270ms), separately recorded. These focused proofs do not
replace remaining all-system/platform/GUI and full qualification gates.
