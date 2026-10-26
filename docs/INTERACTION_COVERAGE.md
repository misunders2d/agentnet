# Next-release interaction coverage and responsiveness

Owner requirement, October 26, 2026: the next release must meet or exceed
Slack/Telegram performance. Every interaction a user can click must be tested.
Publication is blocked until this is demonstrated. The updater-only candidate
`3f9797f641a9707f3f945d35d6de6d5d810165de` is a baseline, not a qualified release.

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

## Current evidence and work

Checklist editor: root. Contributors link evidence and request state changes.
Private timings, screenshots and profiles stay in ignored/local evidence;
this document records the test contract and coverage gaps.

| ID | Required outcome | Owner | State | Evidence or next check |
| --- | --- | --- | --- | --- |
| C1 | Complete source and rendered control/state inventory | inventory owner | source candidates collected; reconciliation open | 293 files scanned; 6,317 provisional control candidates and 314 unresolved input bindings; no rendered or executed coverage inferred |
| C2 | Every inventory entry maps to an executed case and timing result | harness owner | validator reviewed; execution open | Offline validator rejects incomplete or synthetic evidence; its unit fixtures establish validator behavior only |
| P1 | Dismissal mutation, refresh and visible result are timed separately | root | backend stages measured; rendered result open | Owned mixed fixture: exact Resolve committed in 1.070ms; invalidated overview took 936.180ms; no destructive live test performed |
| P2 | Shared slow refresh cause is reproduced and profiled | root | investigating | Installed v0.9.5 compact overview measured 25.645s and later 3.398s; selected read-only store methods completed in 0.844s, so those queries alone do not explain the delay |
| P3 | Measured bottlenecks fixed and normal/10x cases pass | root | scoped candidate; broader qualification open | Per-build participation reuse has adversarial review and focused signed-membership/unread regressions; no general responsiveness fix or 10x pass is claimed |
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

## Reported failures that remain open

This register preserves the owner's reports, including cases that have a source
candidate. A passing unrelated test never closes an entry. No next release or
installed repair is claimed by this register.

| ID | Failure | Current evidence and status | Required closing test |
| --- | --- | --- | --- |
| F01 | Interactions leave the user waiting without a clear result | Owner reports delays above 10 seconds across the app; installed compact overview measured 25.645s and 3.398s. Unresolved. | Every inventoried control: trusted input to visible feedback and confirmed result, under the complete required workload/platform/skin matrix and measured comparators. |
| F02 | Mark as handled takes many seconds to disappear | Owner report unresolved on installed devices. One owned normal/idle case committed locally by 0.991ms but its confirmed capture completed by 886ms. | Exact task disappears promptly on the initiating and linked devices; preserve unrelated attention and show refusals/failures truthfully. Include normal/10x and concurrent sync. |
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
