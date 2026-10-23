# AgentNet v0.9.3 — October 22 batch and release checklist

Status: active, authorized by Sergey on October 22, 2026. Root Codex is the
sole editor. Scope and acceptance: [goal](OCT22_BATCH_GOAL.md) and
[handoff](OCT22_BATCH_HANDOFF.md). The owner explicitly approved publication on October23 with the named Android first-menu, >200ms timing and physical Pixel limits left open; those are not passes.
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
| U1 | Mac toolbar and context menu target the right message at every window size | Actual macOS fullscreen/ordinary/half-width; scroll/reflow/resize/focus/keyboard and rendered responsive checks | oct22-interaction | Root / Claude | implemented | Narrow mouse actions and stale mouse-dismissed focus toolbar RED/GREEN corrected; keyboard targeting retained. Same regression supports WebKit; actual headed macOS/WebKit and installer qualification passes; owner has no Mac |
| D1 | Drive small-file throughput improves without starving live traffic | Bounded equivalent delayed-provider before/after files/s and bytes/s with live traffic; stable IDs/restart/backoff/checksum proof | oct22-storage | Root / Claude | implemented | Equivalent delayed-provider workload: 8.81 to final31.64 files/s; new-file remote custody911 to168ms. Four bounded slots, oldest/newest fairness and backoff; no live remigration |
| C1 | Device/queued/relay/Drive storage scope, limits and cleanup are understandable and safe | Existing APIs/settings; persistence/restart, low/full warnings, protected sends/custody/last copy and retrieval after cleanup | oct22-storage | Root / Claude | implemented | Persisted admin quota and conservative confirmed cache controls; native/browser journeys and 320/390/1280 light/dark rendered controls green. Final type check and quota connection-cycle race1.914s pass; client225.928s and Hub30.856s focused races pass |
| E1 | Send failures show one correct actionable reason and preserve retry identity | Quota HTTP 413, disk full, file size, Drive full and network errors through producer/API/UI; recovery without duplicate sends | oct22-storage backend / oct22-interaction rendering | Root / Claude | implemented | Producer codes plus native/browser durable detail and one actionable renderer; exact-ID proven-never-handed-over retry, legacy rows excluded. Rendered member/admin/double-click checks green |
| M1 | Compressed photo/video choices and exact-original file work in shared UI | Review 1 video decision; desktop/Android/browser; original hashes, usable outputs, cancellation, CPU/RAM bounds, CPU-only and offline/retry | oct22-interaction | Root / Claude | implemented | Shared photo/video and exact originals; lazy mediabunny 1.62.0, capability-probed MP4 or VP9/Opus WebM, bounded Linux fallback. Real CPU-only Chrome output/audio and native cap checks green; cancellation/photo orientation and complete180s native video(9.36MB/9.895s conversion) pass; CPU-only ordinary ZenBook synthetic workload |
| D2 | Prepared Drive consent/quiet-refresh fixes work end to end; remote-only files remain recoverable | Real Go OAuth URL -> UI; no background flicker or typing fetches; explicit refresh feedback; migrated download and remote-only backup/restore | Root integrates; storage/interaction helpers verify | Root / Claude | implemented | Prepared cherry-picks integrated; fresh consent/SDK races pass and real OAuth producer plus six rendered refresh cases pass (21.169s). All8,380 real stored ciphertexts verified in the native full backup; both old/new offline restores preserve rows/files/binding; normal authorized migrated-download fixtures pass |
| A1 | Upgraded Pixel 6 connection status does not create false unread attention | Actual registered channel/recovery, real arrivals/read clearing and reliable delivery; preserve preferences and foreground service | oct22-state verifies existing source | Root / Claude / device operator | agreed | Existing registered-channel inspection, recovery and instrumentation source inventoried. Native upgrade/notification/indicator/read-dismissal checks pass; physical upgraded Pixel result remains open by owner publication decision |
| Q1 / MEL-497 | Ordinary clarification uses tagged chat and a valid tagged reply continues without a sticky decision gate | Actual group-worker saved-answer/repeated-round regression; ask/tagged-answer/same-context continuation and stale-attention clearing; thirty-minute delay split into queue/start/run/response; true native permission/authorization boundaries preserved | oct22-state backend / oct22-interaction UI | Root / Claude | implemented | Actual group prompt now includes saved continuation rounds; tagged follow-up claim settles only exact matching stale needs_human under normal authority. Actual repeated group-worker, tagged/authority boundaries and own-human linked task claim regressions pass; original attempts1/no duplicate rerun; Linear comment 8f7d6f61-f0c8-4b54-a286-3f30bec024ba |

| G1 | Claude reviews every GUI area and interaction; all demonstrated gaps corrected for smooth direct chat UX | Complete reachable control/state/role inventory; rendered wide/narrow/light/dark, keyboard/pointer/touch/Back/focus/loading/offline/error/recovery and every gallery skin; independent recheck of corrections | Root integrates / existing helpers | Existing Claude tab 2 | reviewed with explicit limits | Exact archived candidate GUI/source rechecks and final downloaded-artifact review collected; dirty-main exploratory interactions excluded; physical/timing limits remain open |
| P1 | Every interaction responds promptly on every supported system | Visible-feedback, durable API, projection/paint and frame measurements for all reachable interaction families on real Linux/macOS/Windows shells, Android WebView and browsers; fix demonstrated delays, retain raw timings and distinguish emulator/physical | oct22-interaction fixtures / Root CI | Root / Claude | in progress | Seventeen portable workloads extend all16 family groups; current role/state map and174 prior raw actions retained. Bounded NewChat/invite rendering and measured render/paging delays corrected; composed full/affected system workloads pass functionality, but raw>200ms and exhaustive physical state cells remain open |

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
| go vet ./... | verified on integrated product source | Retained final vet and native CI; current Go equals final qualified76da source |
| Client race shards0–17, count18, existing600s bound | verified through composed full/affected qualification | Run39877403457 plus only affected reruns; all18 current selections accounted, unchanged passes retained |
| Other race packages and three UI partitions | verified through composed full/affected qualification | Non-UI others passes retained; UI0/1/2 corrected selections pass, including39889553253; no timeout increases |
| Native Linux/macOS/Windows and six Windows client runners | verified through composed qualification | Run39888155855 plus affected39895057632/39908576827; Windows last two parents count3 pass39895055440, retaining183 unchanged parents and explicit skips |
| Android signed upgrade/native instrumentation and17 workloads | native modes and16 workloads verified; Actions FAIL retained | FullAA228 run39912731615; diagnostic3f5 run39916719795 confirms frozen timeline/frames on first menu. Owner explicitly approves release with this open |
| Actual Mac controls and physical Pixel | Mac verified; physical Pixel open | Actual headed macOS/WebKit and installer checks pass; original registered-channel/notification correction has native instrumentation, not upgraded physical Pixel proof |
| Office-hardware/media and interaction speed | measured; P1 remains open | CPU-only photo/video and actual native/browser workloads pass functionality; retain raw>200ms and emulator/physical distinctions |
| Required real MixedVersion | verified on final Go source | Final3f5 vs publishedv0.9.2, required compatibility, named PASS72.564s. Old86.024s is superseded |
| Historical aggregate itest hooks failure | resolved with named evidence; original FAIL retained | Both hook parents pass focused regular/race;25e full-itest Linux/macOS/Windows and others-race passes retained; relevant direct code unchanged |
| Matching skin collection | verified through complete/affected gates and final byte equality | Finalgallery5a12224 pins3f5; all5 package digests and20 previews equal76da; final static/parser checks pass, retained complete runtime gates recorded in gallery PROVENANCE |
| Immutable app and matching gallery | published | v0.9.3 source3f5 public07:49:58UTC; galleryv2026.10.23.1 source5a12224 public07:50:19UTC; both Latest |
| Independent draft/public verification | verified | Claude independent draft review9e2c716b; public12/12 payload checksums/provenance/signer/media match draft; publicgallery5packages/129files/20previews,151HTTPSrequests pass |
| Existing relay full stopped backup, old/new restores and rollout | verified | Healthy v0.9.3 from 08:54:05 UTC; archive1,264,958,640 bytes/8,388 files verifies all 8,380 stored ciphertexts; old/new restores preserve rows/files/Drive binding; schema24→25quota-only, exact public binary and normal publicHTTPS pass; original config/realm/volume/neighbor preserved |
| Docs/reviews/owned cleanup | verified; post-rollout review finalizing | All outputs/markers captured, three finished owned helper panes closed, user-owned Claude preserved; public/rollout docs record actual results and owner-approved open limits |

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


## Corrective checkpoint after d12df327 — October23

The d12 child passes strict canonical build/vet and Claude's cheap source gate;
its matching five gallery packages and twenty previews are built and visually
inspected. The integrated25e qualification has now run; its failures are being
corrected together while unchanged passing jobs remain valid. Normal
chat-row `last_id` was mistakenly treated as an exact latest-message jump;
ordinary preview/topic selection is now separated from exact quote/notification
focus, with focused verification in progress. No read-entry guard was broadened.

Bulk Confirm now calls the existing API immediately with captured IDs/counts,
disables repeated input and reports only the host result. Cancel before Confirm
sends nothing. The delayed panel Undo is removed; move/merge keeps its real
server-backed Undo. Delete states exact count/scope and irreversibility. Claude
reviewed this simplification and all three shipped contract descriptions are
updated. Focused component action/lifetime/partial/error cases pass.

The shared picture editor reproduces actual skin-reset placement at(0,0) and a
15px body-weight heading. `margin:auto` and a20px bold heading pass desktop/phone
centering, crop/pinch/wheel/strict-save checks3.619s; both screenshots inspected.
Group verification waits use typed `group_context_pending` and retain the last
verified view/draft, with a temporary send pause rather than red internal error.
Native barrier and browser/Store/transport/rendered proofs remain distinct;
root's two model parents pass1.963s, actual browser engine812checks35.333s and
full-App1280/390 pending/recovery rendering pass. A removed member's authenticated
terminal notice remains a separate concrete design/implementation gate; generic
refusals or opaque head hashes are not removal proof. No publicv0.9.3 or relay
change has occurred.

### Integrated25e correction batch

The single full run39877403457 completed24 passing jobs, nine failing jobs and
the optional Windows probe skipped. All18 client race partitions, five of six
native Windows client partitions and the container gate passed. Failures are
retained with exact source/job logs; a skipped optional probe is not a pass.
Affected native/platform selections and the three UI race partitions will be
qualified after the corrections stabilize, without repeating passed client
partitions.

| Follow-up | Required result | Owner | State / evidence |
| --- | --- | --- | --- |
| G1 final removal/invite presentation | Typed overview wait; exact current member names; no unusable former-member New topic/Schedule; current invitation replaces only its inviter's stale card; neutral legacy setup folder copy | Root / Claude | Implemented; actual old canonical header RED, current-source two-width rendered GREEN, native signed projection/error checks PASS1.672s |
| Gallery exact read observation | Assert read authorization at dispatch, retain geometry/tokens/scope and never-visible unread proof | Interaction / Root | Fixture-only fix; causal actual refetch retirement reproduced;11 focused checks pass, full collection remains open |
| Native client CI regressions | Current-session fixture readiness, exact catalog/identity/order and group convergence under unchanged deadlines | Storage / Root | Five fixture corrections collected; exact catalog/model and three group parents pass focused regular/race checks, platform reruns pending |
| UI/Android fixture prerequisites | Actual draft-hook/preview contract and neutral wording; notification/updater isolation without altering product permissions | State / Root | Three UI fixtures pass; existing emulator-only mode isolation corrected, actual Android rerun pending |
| P1 actual platform interactions | Faithful supported-codec media, native cleanup/anchor behavior, correct native/browser permission states and controls; every retained slow interaction remains visible | Interaction / Root | Native ordered-anchor regression fixed with exact zero-drift proof; isolated suite storage, portable media, bounded Windows disposal and permission vectors pass locally; actual platform reruns pending |

The25e gallery collection failed one Zoom390 child and its parent out of302
test nodes. Its cumulative ACK comparison used a later watcher snapshot after
the legitimate ACK refetch retired that watcher. Synchronous dispatch evidence
now proves the actual canonical geometry, exact rev/writer, scope and current
visit for every ACK;48 never-visible armed rows remain unread. No gallery
production change was required. Publication, public artifact verification,
matching gallery qualification and relay rollout remain unfinished.

The final corrective child also includes Claude’s current-invitation and local-skin first-use findings, phone trust-card geometry and immediate opening feedback. All collected helper source is frozen. The exact canonical package and focused setup/skin-loading tests pass; the final canonical entry SHA256 is `370046e6d8d1a5aada7c4356428dd007d6aeceb9b1bd277994383d458ec4357f`. AppImage media bundling is implemented, with actual new-installer playback still required. The qualification follow-up retains all 24 unchanged successful jobs and reruns affected native packages, Windows partition 4, all three UI partitions and the existing complete interaction/Android workflow. No public release or relay change is claimed.


### Final affected qualification checkpoint — October23

The required Linux18/client, remaining packages/container, UI0/1/2, native
Windows6 partitions and macOS client/UI results are retained with affected
reruns. Windows' last two parents passed count3 in run39895055440. The full
macOS native17/browser matrix passed in run39888155855; Linux native17 and its
remaining affected browser/native controls passed in run39895057632. Actual
mixed-version v0.9.2/current passed86.024s. None of the opt-in skips are counted.

| Remaining gate | Current result | Required completion |
| --- | --- | --- |
| Android exact notification cancellation | On0370, run39901217115 passes signed upgrade, native notification read/dismiss/new-arrival regression and history/navigation/actions/groups. Indicators fails restoring exact5; settings/unread/topics/compose fail assertions; media exceeds30s; Review then hangs until reviewed35min cap and cancellation. Remaining modes unexecuted | Batch the collected failures, bound and diagnose native invocations, then complete affected actual emulator checks. Physical Pixel stays separately unverified |
| Windows extra WebKit media port | On0370, actual WebView2 and Chromium/Firefox media pass. Independent same-byte Blob and HTTP controls both fail to complete on the extra hosted Windows WebKit port; exact source/transport/cleanup checks pass | Two precisely guarded extra-port cells are not-executed, retaining original failures and partial actions. This is neither playback proof nor a proven Blob-only cause |
| Matching gallery | Gallery3a92986 pins0370. All305 Node cases and all applicable runtime/provider/overlay gates are accounted for with retained unchanged passes and exact affected Zoom reruns. All20 previews inspected; five packages unchanged from reviewed CBAD bytes | Public matching-gallery publication and independent public verification remain pending; no claim that the original failed full invocation exited0 |
| Public release and existing relay | Prepared, no v0.9.3 public release or live change yet | Draft/public artifact checks, matching gallery, full stopped backup and old/new restore, preserved-data rollout and post-check |

The Android native and diagnostic-only changes leave the frozen Go/UI/gallery
contract bytes unchanged. Retain passing results by exact file/hash equivalence;
repeat affected checks when their code or a demonstrated failure changes. Final
source and release IDs belong in V0_9_3_RELEASE.md. Known timing observations
above200ms and the unavailable physical Pixel remain honest open acceptance,
not a silently waived or simulated pass.


### Integrated Android corrective child — October23

The capped0370 attempt remains failed. Its signed upgrade/native notification
checks and four successful timing families are retained without counting the
unexecuted modes. The corrective batch keeps the original native30s/5s guards,
bounds each host invocation, retains failed reports and adds safe phase/focus/
stack diagnostics. Direct synthetic badge inputs now update the cached host
scalar, wait for the existing WebView-ready condition and respect Android's
notification update rate. Denied-notification evidence precedes decoder work;
media runs last. Neither the native hang nor the old indicator failure is
attributed without the next native evidence.

Three minimal GUI changes are independently accepted by Claude: a compact
New-topic caption below389px, visible storage failure feedback before its action
buttons, and an existing scheduling refusal displayed on the disabled menu item
instead of navigating silently to Outbox. The full-file schedule coverage stays
on supporting hosts; Android separately proves selected-file refusal with zero
writes and exact draft retention, then text-only same-ID uncertain-save recovery.
The corrected fixture preserves every original supported-host file assertion.

Seven affected model/rendered parents pass, retaining six original passes and
rerunning only the screenshot-directory configuration failure. Final canonical
Chromium Android-contract checks pass all15 reports across settings/topics/
compose/storage/permissions at320x592,360x640 and390x844,44.596s. A genuine360px
RED was retained before extending the caption breakpoint. These are synthetic
host-contract checks; routine timings above200ms remain visible and are not a
native or smoothness pass. Strict canonical TypeScript/build, package checksums,
JavaScript/Bash syntax, diff checks and go vet pass. Actual affected native and
Android qualification follows once this integrated child is committed.


### Native Android focus and download checkpoint

On76da, all three affected desktop jobs passed, including each native shell and
45 browser reports per system. Android passed all native upgrade/notification/
indicator/denied checks and14 workloads. Its remaining failures now have retained
focused evidence: an unfocused WebView prevented visible reads; the media fixture
opened the real native document picker; the first action sheet took2333ms.

Claude reviewed the native focus ownership fix and passive startup regression,
the native save-picker cancellation monitor with exact staged-byte verification,
and bounded first-menu long-frame diagnostics. No timing limit is raised. The
focus change affects all Android sessions, so repeat the full default17-workload
Android matrix and its native modes; retain unchanged desktop/gallery results.
The optional workload-selector experiment is omitted. The menu cause remains
unresolved until native attribution is collected. Provider writes are explicitly
not executed by the cancellation fixture; link bytes and native staged bytes
are separate assertions. Source and local functional checks do not count as a
native repair pass, and public release/rollout remain pending.


### Final Android Actions discriminator

ExactAA228 full Android run39912731615 completed: signed retained-data upgrade,
notifications, indicators, denied-notification mode and16/17 portable workloads
passed. Passive document focus/read behavior and exact staged native export
cancellation/cleanup are now native passes. Provider writes remain unexecuted.
Actions alone failed: the first sheet did not satisfy the unchanged visible
feedback predicate within3s. Its prior2333ms failure and this timeout remain
retained; off-screen Details geometry does not explain either failure.

Claude and the interaction helper independently rejected a predicate repair.
The next child changes only tests/CI: bounded first-menu timeline/rAF/ancestor
visibility samples, failed-record frame statistics, and a whitelisted affected
Android workload selector. Default qualification still runs all17 workloads.
The next native check selects Actions only; keep allAA228 native/16-workload
passes and unchanged desktop/gallery evidence. All27 action assertions and the
existing2000ms feedback/3000ms wait bounds stay unchanged. No product repair or
smoothness result is inferred from local synthetic passes.
