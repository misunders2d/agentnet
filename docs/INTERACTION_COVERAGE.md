# Next-release interaction coverage and responsiveness

Owner requirement, October 26, 2026: the next release must meet or exceed
Slack/Telegram performance. Every interaction a user can click must be tested.
Publication is blocked until this is demonstrated. The updater-only candidate
`3f9797f641a9707f3f945d35d6de6d5d810165de` is a baseline, not a qualified release.

Release authorization, October 26, 2026: Sergey explicitly authorized driving
the next fully responsive release, including generated assets, commit, push,
deployment and publication, without further approval requests. The earlier
runtime-artifact approval question is resolved. This authorization does not
waive the responsiveness, recovery, platform, skin or release verification gates.

## Hard gate: reuse the working messenger implementation

Owner correction, October 26, 2026: copy what already works in Telegram/Slack.
AgentNet supplies the new way to communicate with native harnesses; messenger
architecture and interaction behavior must come from inspected proven designs.
This is an implementation acceptance gate as well as a publication gate.

- Before changing a messenger flow, inspect its working counterpart. Record the
  pinned source revision and exact functions/flow, what existing code/components
  or pattern is reused, and the necessary AgentNet-specific adaptation. Existing
  applicable reference evidence can be reused. Closed-source counterparts need
  observed behavior and authoritative documentation with unverified parts named.
- Prefer reuse for local state, background operations, acknowledgements, retry,
  synchronization and bounded history loading. Similar styling or an invented
  architecture described as Telegram-like does not satisfy the requirement.
  A new messenger architecture requires evidence that reuse cannot meet the
  requirement and Sergey's explicit approval before implementation.
- Apply the intended local visual change on input, before the backend finishes.
  Sending immediately adds a pending message; reacting changes the reaction;
  dismissing immediately removes active attention; navigation immediately shows
  the chosen destination or its loading state. A spinner beside the unchanged
  old state does not satisfy this gate. Background work reconciles the result;
  failures explicitly identify the action and restore affected current state or
  provide retry. Local presentation never implies an execution permission or
  unproven remote completion.
- Execute rendered delayed/blocked/failing-backend cases. Prove the intended
  local effect happens while the request remains blocked and other controls
  still work. Verify failure/restoration/retry, duplicate input, workspace
  changes, newer synchronization and stale responses. Preserve drafts, files,
  exact targets and native authority. Keep the all-interaction comparative timing
  requirements below; immediate presentation alone does not qualify the app.
- Missing reuse evidence, a backend-dependent local effect, frozen controls,
  silent failure, or failed recovery blocks acceptance and publication. Existing
  functionality tests, a pending indicator, or an implementation claim cannot
  waive this gate.

The gate is recorded; implementation compliance remains unverified. Root owns
the repair. Sergey needs to do nothing while owned fixtures are being tested.

## Acceptance

- Enumerate controls by screen, placement, action, meaningful state, role and
  capability, for Comic and every supported skin/platform. Include menus,
  nested menus, dialogs, links, tabs, inputs, toggles, pickers, file/media controls,
  context actions, navigation/back/cancel and native bridges. Record keyboard and
  touch variants. A shared handler is not evidence that every control was used.
- Reconcile source inventory with rendered discovery and actual executed cases.
  New or unmatched controls, missing states and missing cases remain open.
- Record input to first visible acknowledgement, local action commitment,
  confirmed visible result and relevant remote completion separately. Verify
  correctness and preserved input/focus as well as elapsed time. A spinner or
  optimistic success does not establish completion.
- Exercise normal and 10x history, cold and warm views, synchronization/catch-up,
  repeated input, offline/error/recovery and permission/capability boundaries.
  Record repetitions, distributions and worst observed delays; averages cannot
  conceal stalls.
- Record matching Slack/Telegram actions, versions, hardware, data and network
  conditions before claiming comparative performance. Incomparable or missing
  comparator evidence is unverified, not a pass.
- Report every required case as passed, failed, blocked or unexecuted with its
  exact candidate/platform/fixture and evidence. Unsupported controls must be
  accounted for through their actual unavailable/disabled behavior. Missing
  execution or unresolved latency blocks publication.
- Preserve original identities, exact targets, native permissions, history,
  shared read/reminder generations and truthful transport state. Use owned
  fixtures for destructive actions; never dismiss the owner's real requests
  merely to collect timings.

## October 26 local interaction candidate

This continuation starts from clean `edec2bab` on
`codex/oct25-agent-conversations`. It does not qualify the app-wide requirement.
No release, installed application update, physical-phone pass or Slack/Telegram
latency parity is claimed.

Reuse evidence: Telegram Web A revision
`f67a40879a7d78c2683ec5a296cf55bc22417d56`,
[`messages.ts`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/global/actions/api/messages.ts):
`editMessage` clears the editor before its API call (lines 825–869);
`deleteMessages`/ephemeral deletion project locally and recover after failure
(1203–1268); viewport loading uses a local slice before deferred fetching
(222–327). The previously inspected `reactions.ts` reference below remains
applicable. AgentNet reuses these interaction patterns through its existing
Store, API calls and retained editor text. No new transport, worker service,
permission grant or durable success is inferred from a local preview.

Edits/deletions retain canonical message bodies and execution input, with a
separate presentation recording pending, saved or failed. A changed canonical
revision takes precedence. A failed edit keeps its attempted text even after
newer linked-device content arrives; Retry uses an explicit editor, and Cancel
or Discard releases the draft. Pending writes and failed edit drafts block
automatic reload. Duplicate exact mutations share a flight; different edits
cannot silently inherit an older write's success. Topic previews retain their
original version guard and the existing confirmation boundary. The projections
reuse their message arrays when unrelated draft text changes.

Evidence on the source candidate: `go vet ./...` passes. Focused Go race runs
cover Store action refresh, local changes rendered, responsive editor drafts,
reaction pending/revisit, message interactions, first unread entry, keyboard
timeline, topic-bar selection, clock formatting and timeline projection.
The new rendered fixture uses trusted mouse/touch input and held synthetic
host writes, not real persistence or remote execution. Its four width/history
combinations pass edit/delete/topic local changes, refusal/restoration,
retained edit/rename text, retry/discard, newer synced text and unrelated input.
One final sample has event-to-DOM updates of 8.9–64.4 ms and two-frame presentation
opportunities of 26.5–127.8 ms for edit/delete. These are individual observations,
not a p95 distribution, physical display timing or comparative qualification.

The reaction regression was first reproduced with the unchanged base Store.
The corrected run shows local reaction changes at 7.6–11.7 ms while controlled
writes take 617–645 ms, and passes failure, repeated intent, revisit, independent
emoji responses, newer linked intent, hidden tabs and stopped workspaces.
The draft fixture covers eight existing editor types. Temporary screenshots of
wide/narrow pending and recovered message views were inspected.

The temporary final build also passes 391 functional action observations in
12 suite/viewport partitions: history, groups, media, outbox, agents, teams
(list/group), profile, storage (wide/narrow), workspaces and permissions.
Earlier navigation/settings/unread/actions/compose/review checks and the
corrected three-width topic check pass. These remain synthetic-host functional
checks. Four final-build observations exceed the informational 200 ms budget:
exact old quote parent (218.7 ms), invite open (304.5 ms), member invite open
(248.3 ms) and team creation (209.6 ms). Their original timings remain evidence
of unfinished responsiveness, not waived exceptions.

Follow-up to source commit `753a4795`: profiling the two invite openings finds
repeated full-directory presence lookups and per-person parsing of the same
recent messages. The candidate now indexes presence once per pool build,
parses collective mentions once per recent message, and skips name-regex
construction when both ASCII strings prove there is no match. Non-ASCII names
or text keep the existing Unicode matcher. First-record duplicate semantics,
unknown/stale presence, exact references, Unicode/overlapping boundaries,
latest mentioned recipient, pending invitations and native permissions are
unchanged in the source regression. For 1,000 people and four recent messages,
the test permits only a linear directory pass, four collective parses and no
unnecessary ASCII name matchers.

One comparable profiled groups pair falls from 333.2 to 154.4 ms for opening
the guest invite and from 256.4 to 119.5 ms for member invite. Two-frame
presentation opportunities fall from 376.9/307.9 to 182.1/146.8 ms. This is
one synthetic-host diagnostic pair, still above the 100 ms feedback target;
it does not establish a distribution or close the release gate. The race
detector also exposed the existing latency fixture starting its HTTP server
before assigning the handler's server pointer. It now initializes the provider
before listening; this is a harness repair, not a production concurrency fix.
The corrected race run passes the source-model check, the groups journey and
composer journeys at 1440, 320 and 360 pixels without a race report.

Harness corrections preserve original semantic assertions: the history journey
opens the existing collapsed outcome disclosure and checks both exact target
IDs; complete-history counts include every grouped message ID rather than one
DOM row per send. Topic Reopen waits for confirmation to enable its control.
Touch injection now waits for actual hit-test readiness after a closing modal;
it no longer dispatches through the departing backdrop. Original failed runs
remain in temporary evidence. These fixture repairs are not product fixes.

Reminder local intent now uses the same inspected Telegram mutation/recovery
pattern. Set and Move close their sheet and show the chosen reminder immediately;
Done and Cancel remove its due attention immediately. A separate pending status
names the affected reminder. Failure restores current canonical state and retains
the chosen time for explicit retry. Duplicate actions share one write; conflicting
actions cannot reuse its result. A removed Details sheet stays closed when a
newer linked reminder arrives. Unconfirmed older-provider readback retries reads
only, without replaying the write. Pending/failed intent prevents automatic reload.

The existing native reminder revision and browser revision/writer are exposed as
optional view fields. The existing reminder routes accept an optional expected
generation. Native SQL updates and browser checked IndexedDB writes reject stale
Move/Done/Cancel and duplicate create operations, including a newer generation
with the same due time; an old move cannot resurrect a completed reminder.
No schema, transport, execution permission or native notification authority changes.
Older skins keep their existing unguarded route behavior; complete skin update
and qualification remain necessary before release.

`TestReminderChangesRendered` executes actual Store/Reminders/RemindSheet controls
with trusted mouse input at 1440/390 pixels and 100/1,000 loaded messages. Done,
Cancel, Move, Set, Retry, Dismiss error and read-only confirmation recovery pass
with held/refused writes, newer same-due synchronization, duplicate input and
unrelated typing/navigation. The focused race run observes 4.2–11.1 ms from the
click event to the intended DOM change for measured Done/Move/Set and 18.4–27.9 ms
to a two-frame presentation opportunity. These are individual synthetic-host
observations, not p95, physical paint, touch execution or comparator evidence.
Pending and recovered wide/narrow screenshots were inspected. Native conditional
storage tests, browser generation tests, exact response/readback checks and live
generation projection tests pass; the preceding local edit/delete/topic and
editor-draft regressions also pass under the race detector.
The rebuilt shipping bundle passes its checksum/type contract and the 44-action
Outbox functional journey. Real live-host reminder controls pass New York/Kyiv
date/DST cases at desktop and narrow widths, including Cancel/Create/Done and
separate pending-versus-confirmed completion. Conflicting reminder controls are
disabled until their existing write is confirmed; unrelated input stays usable.
The browser guard executes 42 checks against both memory storage and Chromium
IndexedDB. `go vet ./...` passes. The Outbox diagnostic still observes 123.1 ms
for opening an original message; complete responsiveness qualification is open.

Outstanding scope includes remaining settings/participation/bulk flows,
real-backend large-history startup/catch-up,
complete rendered inventory reconciliation, all skins and native platforms,
and measured comparable messenger baselines. Existing successful representative
suites do not close those gaps. Source/build validation is not installation.

History follow-up after `b8928ab4`: profiling finds full-directory allocation in
every avatar and message-author lookup, before checking an exact first peer.
These lookups now stop at the exact match without copying unrelated people.
Identity precedence remains guest, chat peer/member, own person, then directory;
avatar label ambiguity and exact picture removal retain their existing behavior.
Source regressions verify 10,000-person directories require zero unrelated reads
for the own/chat peer and one read for the first directory match. No stale cache,
history-window change or authority decision is introduced. Three affected model
and picture race parents plus the 14-action rendered history journey pass.
One isolated profiled before/after pair does **not** establish an overall latency
improvement: exact notification/quote feedback is 122.3/153.2 ms before and
162.8/186.9 ms after. Geometry/layout, message rendering and pane animation work
remain material, and those interactions still exceed the feedback target.

Release verification at `b8928ab4`: the real-binary `TestMixedVersion` passes
against the newest published lower release, with required compatibility enabled
(97.24 seconds; not skipped). All 18 client race partitions passed in
[CI run 40397384917](https://github.com/misunders2d/agentnet/actions/runs/40397384917).
The later lookup-only delta does not change client source. No publication or
deployment is claimed while the interaction/platform/skin gates remain open.

The next pane-change delta replaces a synchronous computed-animation scan of
the whole incoming pane with DOM matching of its existing entrance nodes. Their
entrances settle through CSS; later arrivals and continuous working indicators
keep animating. Actual-component Chromium checks cover wide/narrow layouts,
both motion preferences, inert leaving content and later class updates. Eight
alternating history runs (four per build, identical binary/backend fixture,
Chromium 155 on this Linux laptop) pass the existing 14 functional actions.
Median notification/quote feedback changes from 221.4/239.6 to 185.4/198.6 ms;
ranges overlap, host load varies, and sending does not improve. These synthetic
diagnostics support removing the measured scan, not a claim of app-wide speed
or release qualification. Exact history targets, retained rows and scroll-anchor
assertions remain unchanged. Raw profiles/logs stay in temporary local storage.

History navigation also recreated every row's focus/jump callbacks when its
target or visit changed. Stable callbacks now read the last committed navigation
context. The actual Timeline/MessageView regression first fails on the previous
source: a notification redraws all 100 retained bubbles. The corrected source
redraws two for notification/visit changes and two to four for quote jumps, with
100/1,000 messages at 1440/390 pixels and 100–344 mounted rows. Exact visibility,
keyboard stop, retained history, a quote after a newer visit and unrelated typing
are verified with trusted mouse input. The existing rendered history, keyboard
and first-unread regressions pass under the race detector. Shipping type/hash
checks pass. The synthetic history timing still exceeds the target; this is
bounded rendering-cost evidence, not complete latency qualification.

The native CI workflow now explicitly installs the pinned browser and executes
eight rendered mutation/recovery/navigation parents on each selected OS. Normal
native runs previously skipped these opt-in tests. This adds execution to the
gate; it does not convert prior skips into passes or substitute Chromium for a
native shell or physical phone.

The first expanded native run exposes harness portability gaps: six Windows
parents cannot execute the Unix Tailwind wrapper; a macOS history-preparation
click races the timeline's automatic prepend/removal of the Earlier control.
The harness now invokes Tailwind's pinned JavaScript entry through Node and
prepares retained pages by scrolling before the measured trusted navigation.
Seven affected rendered parents pass together locally with unchanged functional
assertions. The native source suite also finds a missing retained-editor import
in the topic-copy harness; it now evaluates the real retained-text hook and topic
key function. Its original scope/copy assertions pass under the race detector.
These are test repairs, not product improvements. A macOS reaction measurement
still reaches its original 100 ms wait bound; additional diagnostics retain the
browser timing/state on failure. The latency assertion and bound are unchanged.

Five native Windows client partitions pass. Partition 2 fails while setting up
verified group context, before its outgoing-history-control assertions. The
exact parent then passes three isolated native Windows repetitions in
[run 40402846764](https://github.com/misunders2d/agentnet/actions/runs/40402846764).
Only that failed full partition is rerunning; the unexplained initial setup
failure remains recorded, not silently treated as a clean first pass.

## Current evidence and work

Checklist editor: root. Contributors link evidence and request state changes.
Private timings, screenshots and profiles stay in ignored/local evidence;
this document records the test contract and coverage gaps.

| ID | Required outcome | Owner | State | Evidence or next check |
| --- | --- | --- | --- | --- |
| C1 | Complete source and rendered control/state inventory | inventory owner | source candidates collected; reconciliation open | 293 files scanned; 6,317 provisional control candidates and 314 unresolved input bindings; no rendered or executed coverage inferred |
| C2 | Every inventory entry maps to an executed case and timing result | harness owner | validator reviewed; execution open | Offline validator rejects incomplete or synthetic evidence; its unit fixtures establish validator behavior only |
| P1 | Dismissal mutation, refresh and visible result are timed separately | root | real rendered baseline and controlled candidate exercised; full timing matrix open | Owned mixed fixture and trusted browser input separate local display, request, commit and refresh. The candidate clears the card while the request is held before the real backend; normal/10x, sync and comparator qualification remain open |
| P2 | Shared slow refresh cause is reproduced and profiled | root | investigating | Installed v0.9.5 compact overview measured 25.645s and later 3.398s; selected read-only store methods completed in 0.844s, so those queries alone do not explain the delay |
| P3 | Measured bottlenecks fixed and normal/10x cases pass | root | scoped candidate; broader qualification open | Per-build participation reuse has adversarial review and focused signed-membership/unread regressions; no general responsiveness fix or 10x pass is claimed |
| P4 | Local dismissal changes UI before asynchronous processing | root | implemented; focused checks pass; release qualification open | Telegram local mutation/reconciliation flow reused. Real blocked-request card/count removal, usable menu/navigation, Review pending state and failure restoration exercised; stale/new-attempt/lost-response/workspace tests pass. Source candidate is not an installed repair |
| P5 | Composer sizing avoids repeated forced layout while typing | root | implemented; rendered regression and affected checks pass; stress journey still fails | Reused Telegram's separate natural-height measurement and batched conditional writes. Old skin writes height 32 times for ten single-line characters; new desktop/narrow regression permits at most two. Four affected race test parents pass; the complete 6x renderer journey still hits its unchanged watchdog |
| P6 | Typing does not rebuild unrelated topic/chat lists | root | implemented; focused checks pass; stress remains failed | TopicBar subscribes only to selected topic/new-topic state and skips workspace chat-list construction for human DMs. Actual-component regression and six rendered chat/layout combinations pass; owned normal real-backend journey passes. The 6x renderer run fails during startup before typing, so no stress improvement is claimed |
| P7 | Repeated message times avoid per-row locale formatter setup | root | implemented; output/cost regressions pass; stress remains failed | Reuses a fixed clock formatter within one synchronous batch, then clears it before later work. Actual model and Chromium parity checks preserve locale, invalid dates, DST and changing device zones; five focused race parents pass. Full 6x journey still reaches its unchanged watchdog |
| P8 | Edit/delete, navigation and topic changes show local intent before I/O | root | source candidate; focused rendered checks pass | Actual Store/Timeline at 1440 and 390 pixels, 100 and 1,000 messages; writes held and refused, drafts/retry/discard, newer linked edits, unrelated typing/navigation and topic rename/done/reopen exercised. Uncached navigation selects its loading destination synchronously. Complete control/platform coverage remains open |
| P9 | A later reaction removal survives returning to its chat | root | source candidate; rendered regression passes | Reproduced the existing revisit failure on unchanged `edec2bab`; keep the exact actor/target/emoji write lane when returning, so removal queues behind an unfinished addition. DM/group/thread and stale-response/workspace cases pass |
| P10 | Timeline day/run comparisons avoid repeated timestamp parsing | root | source candidate; focused regressions pass | CPU profile identified repeated `sameDay` conversions; one conversion per message now supplies adjacent run/day comparisons. Four device zones, DST, midnight, invalid input, unread boundaries and 10,000-message conversion count checked |
| P11 | Large invite directories avoid repeated presence and mention scans | root | source candidate; source parity/cost checks pass | Profiled 1,000-person dialog openings improve from 333/256 ms to 154/120 ms in one diagnostic pair; remaining latency and comparative qualification stay open |
| P12 | Reminder actions change local attention immediately and preserve newer generations | root | native storage, browser/IndexedDB and rendered checks pass; full skin/platform qualification open | Set/Move/Done/Cancel, duplicate input, held/refused writes, newer same-due state, retained retry, stale-dialog closure and read-only confirmation recovery exercised. Optional conditional writes use existing generations; no schema or authority changes |
| U1 | Update version checks do not depend on chat projection | root | verified source only | Existing lightweight endpoint; real blocked-chat regression, native three-OS/race tests and live patched check at 1.089ms; installed app still v0.9.5 |
| X1 | Confusing history/review screen explains state and next action accurately | UX reviewer/root | investigating | Owner screenshot mixes saved history, unknown original agent and contradictory continuation/approval instructions |
| H1 | Pi/OMP idle notices do not repeat remaining items as new | root | candidate tested; installed activation open | Passive activity uses one native status slot. Pi/OMP focused regressions and isolated real Pi rendering pass; prompt acknowledgement remains tied to native persistence. Existing sessions not reloaded |
| B1 | Slack/Telegram baseline measured under comparable conditions | root | unexecuted | No parity claim or benchmark numbers yet |
| R1 | Every required interaction passes on final candidate | root/verifier | blocked | No v0.9.7 tag, draft or publication is authorized past this gate |

The previous final-pin skin run passed functional compatibility: 306 browser/static
checks and 37 applicable runtime groups, with five explicitly inapplicable entries.
That run does not establish exhaustive interaction timing or acceptable performance.

The existing latency runner also cannot establish this gate: its functional pass
allows feedback below 2,000ms, its 200ms smoothness result is informational, and
some journeys use fixed-latency synthetic responses. Cold page startup is outside
those action timers. These results remain functional evidence only. Required
performance evidence must identify the actual backend, trusted input, rendered
control, presented result, exact operation and target, with separate confirmed
commit and remote receipt where applicable. Raw source candidates and matching
test names remain unexecuted until their rendered cases are reconciled and run.

One real-backend Comic/browser dismissal journey now verifies the exact request
resolves once, its original thread stays visible and its attention card clears.
The acknowledgement handler took 0.805ms; SQL observed the committed state by
0.991ms. Conservative screenshot-completion upper bounds were 409ms for feedback
and 886ms for the cleared card. This is one normal/idle functional diagnostic,
without repetitions, 10x history, synchronization load or comparator evidence.
The initial dialog and final Review captures include transitions and do not prove
those screens had visually settled. None of these results qualifies the release.

The local-dismissal candidate reuses the inspected flow in Telegram Web A
[`f67a408`, `toggleReaction`](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/global/actions/api/reactions.ts#L234-L272):
apply the local visual change before awaiting the operation, then reconcile or
restore on failure. AgentNet adapts that behavioral pattern through the existing
Store and resolve operation. A transient presentation removes exact active
attention and decision counts; original message data remains authoritative.
Pending status remains visible in the history and Review. Newer authoritative
state wins; a lost acknowledgement triggers fresh reads without replaying work.

Eight controlled Store behavior cases pass, including duplicate input, delayed
reads, byte-identical overview rows from a newer attempt, rollback, committed
write followed by response loss, and workspace departure. Five affected race
test parents pass; the existing wide/narrow history/Review browser regression
also passes. The directly affected link-store test needed its two pre-existing
schedule-module imports copied into its temporary bundle; assertions remain.
Real Go/SQLite browser checks hold the resolve request before forwarding it:
the exact card leaves immediately, another menu and Review navigation remain
usable, and Review displays pending instead of AllClear. A controlled refusal
restores the current card and badge with an explicit error. Screenshots of the
major chat/menu/Review/pending/recovery states were inspected. These establish
the scoped behavior, not comparative latency, Android performance, exhaustive
control coverage, cross-device convergence or release readiness.

Composer sizing follows the inspected Telegram Web A
[`MessageInput.tsx`, same pinned revision](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/components/middle/composer/MessageInput.tsx#L276-L350):
measure a separate natural-height copy, batch geometry reads, and write only a
changed height. AgentNet uses the existing mention mirror with one unconstrained
inner element; its textarea and input handlers remain the same. One stable
observer schedules one animation frame, and departure cancels it. The focused
rendered regression covers growth, shrink, trailing newline, capped scrolling,
composition events, attachment space, reduced available height and same-chat
read-only/reopen at desktop and narrow widths. Screenshots were inspected.
Resize and multi-agent Composer race checks pass; panel and schedule checks pass
after two bounded harness repairs: allowing only the exact read-only topic-location
lookup already observed on clean HEAD, and providing a temporary screenshot
directory required by the schedule runner. Other write guards remain intact.

The unchanged 20-second stress-driver watchdog still expires in both 6x CPU
renderer runs. Sampled resize work drops from approximately 1,320ms to 42ms in
that one before/after pair, while opening still has an approximately 1,138ms long
task. This is renderer simulation with owned data, not a physical-phone result
or a qualified latency distribution. Full-history payloads, full timeline
preprocessing, topic layout and unrelated list rebuilding remain open.

The narrower TopicBar candidate removes its whole-draft subscription and skips
the unused workspace chat-list construction for human DMs. Its actual-component
check verifies stable selected values when only draft text changes and zero DM
chat-list calls, retaining the existing compact/layout/state checks. Six mocked
desktop/phone combinations across direct chats, groups and agents pass under
the race detector. The harness needed an exact read-only topic-location lookup
instead of incorrectly emitting a content change, plus Cancel-before-Confirm
and precise confirmation wording matching the already recorded owner contract.
The complete request ledger and mutation/result/scope assertions remain.

One owned normal Go/SQLite/Chromium journey passes in 18.07s. Chat opening took
1.548s, the typing phase 342ms, first/repeated Actions 127/69ms, Review navigation
91ms and cached reopening 89ms. These are single driver intervals, not latency
distributions or first physical-frame measurements. Screenshots of chat, menu,
Review, confirmation and the cleared item were inspected. The same candidate's
6x renderer run fails at the initial Chats click's unchanged five-second limit,
before the modern-chat phases; its startup profile includes 772/760ms long
tasks. Neither a stress pass nor a causal before/after speedup is established.

Clock formatting follows Telegram Web A's inspected
[`formatter reuse`, same pinned revision](https://github.com/Ajaxy/telegram-tt/blob/f67a40879a7d78c2683ec5a296cf55bc22417d56/src/util/localization/dateFormat.ts#L364-L387).
The startup profile identified repeated `timeOf`/`toLocaleTimeString` work.
AgentNet shares one fixed hour/minute formatter between message and reminder
clocks within a synchronous batch, then discards it in a microtask so later work
uses the device's current default zone. Invalid dates retain their previous
output. There is no formatted-result cache or timezone listener.

The actual-model regression fails on the old source with 1,016 per-row locale
calls in its batch. The candidate uses that path only for four invalid inputs
and creates one formatter for the valid rows. Matching output and later-batch
recreation pass across five changing zones and DST boundaries, in default,
Ukrainian and Arabic Node locales. Chromium checks also pass all 15 locale/zone
combinations, including live timezone overrides. Five affected race parents
pass, including rendered reminder and saved-history controls. The complete
6x renderer journey still fails its original 20-second watchdog after seven
captures. Its exact owned request was committed once, but final UI freshness
verification did not complete; that run is a failure, not a performance pass.

The real 48-topic regression exposed a separate binding defect. All 48 topics
remain stored: 40 ordinary human-device topics and eight legacy agent-history
topics. An explicitly opened legacy question shows All topics (0), despite
eight exact-binding overview rows. Its bar incorrectly uses the active-agent
directory projection. The paged list also lacks a way to distinguish those two
bindings when host/key/empty agent ID coincide. This failure remains open;
changing the test's 48 expectation to 40 would hide it. Current-agent directory
filtering and original stored identities must remain intact when repairing it.

## Reported failures that remain open

This register preserves the owner's reports, including cases that have a source
candidate. A passing unrelated test never closes an entry. No next release or
installed repair is claimed by this register.

| ID | Failure | Current evidence and status | Required closing test |
| --- | --- | --- | --- |
| F01 | Interactions leave the user waiting without a clear result | Owner reports delays above 10 seconds across the app; installed compact overview measured 25.645s and 3.398s. Unresolved. | Every inventoried control: trusted input to visible feedback and confirmed result, under the complete required workload/platform/skin matrix and measured comparators. |
| F02 | Mark as handled takes many seconds to disappear | Local-first source candidate clears exact active attention before the request reaches the backend, retains pending status and restores current state on failure. Controlled real-backend browser and focused regressions pass; installed-device and full timing qualification remain open. | Exact task disappears promptly on the initiating and linked devices; preserve unrelated attention and show refusals/failures truthfully. Include normal/10x and concurrent sync. |
| F03 | About cannot update the app | Source fix verified; installed v0.9.5 remains unchanged. Its version check waits for the slow chat overview. | Actual installed old-to-new update, including app, command and running background process; no dependence on slow chat reads and no interruption of admitted work. |
| F04 | Saved agent history, unknown agent and contradictory Reply instructions are confusing | Copy candidate and combined wide/narrow regression exist; not installed. The wider history/composer workflow remains unqualified. | Same history thread with a waiting request: clear available actions, exact Reply routing, retained draft/files and no misleading permission text. |
| F05 | A read-only preflight becomes a human-action blocker | Exact inspected request says "Do not start or modify anything yet." Its report is needs_human, attempt 2, without a native permission prompt. A related answer reports the worker is outside Herdr. The worker's proposal prompt also incorrectly groups environment failures with native permissions. Original phone-to-worker routing remains unverified. | Trace the original phone instruction through delegation and the selected harness. Ordinary clarification/environment reports return to the requester; only a real required authorization becomes a permission decision. |
| F06 | Steering from the phone does not reliably reach the running harness | Owner report unresolved. Source contains native Pi steering and Codex turn/steer calls; their existence is not end-to-end proof. | Phone correction reaches the exact active native session/turn once, is acknowledged by the native steering API and visibly changes the ongoing work. Test completion races, queued follow-ups, stale attempts and lost connections. |
| F07 | Pi repeatedly appends large notification blocks | Candidate replaces passive transcript snapshots/toasts with a keyed native status. Old code fails the regression; five focused test parents pass, including Pi/OMP shrinking/reordered lists and full prompt persistence. Isolated real Pi renders the status through three idle wakes with zero passive transcript entries or acknowledgements. Existing sessions not activated; native OMP rendering unexecuted. | Load and verify the corrected integration in connected harnesses. Pi and OMP: changing activity updates one native status; the next prompt still receives complete current unread/decision metadata with native-persistence acknowledgement. Verify exact selected receiver delivery separately. |
| F08 | Read or completed activity appears again in harness notices | Owner screenshots include repeated answered/running records. State labels alone do not prove effective read status; exact shared-read mismatch remains to be checked. | Read on phone/browser/desktop clears the corresponding hook notice across linked devices; preserve deliberate unread generations, newer unseen items and unresolved decisions. |
| F09 | Passing tests were treated as proof of acceptable performance | Existing runners can pass below 2,000ms feedback, ignore 200ms smoothness failures and use synthetic responses. Reviewed validator guards now pass; complete rendered performance execution is absent. | Incomplete, skipped, synthetic, stale, slow or mismatched evidence fails qualification; every required real interaction has inspectable timing and outcome evidence. |
| F10 | Android loading and first Actions interaction remain slow or invisible | Earlier roughly 13-second phone load and API35 first-menu stall remain unqualified. No current physical-phone tests. | Current candidate on supported Android targets: cold/warm opening, first/repeated Actions, refresh and navigation, with trusted input and displayed-result timings. |
| F11 | Screenshots can capture unfinished transitions while assertions pass | Owned diagnostic's initial dialog and final Review captures show transitional content. Original captures retained. | Verify the intended screen has visibly settled; include transitions in completion timing instead of disabling them for measurement. |
| F12 | Connected harnesses can keep old copied hooks without a clear refresh notice | Runtime source and loaded-session revision are distinct; automatic revision/refresh notice remains unimplemented. | Update hook files, detect an older loaded integration, give the correct native refresh action and verify new-session activation without closing the user's sessions. |
| F13 | Agent/device labels disagree in the request card | Combined history regression still shows an existing Laptop/Zenbook label mismatch. Not repaired by the copy candidate. | Original signed/admitted identity and host labels agree across header, card and action target; never infer a historical agent from today's default. |
