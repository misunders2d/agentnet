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
| P3 | Measured bottlenecks fixed and normal/10x cases pass | root | unexecuted | No general responsiveness fix is claimed |
| U1 | Update version checks do not depend on chat projection | root | verified source only | Existing lightweight endpoint; real blocked-chat regression, native three-OS/race tests and live patched check at 1.089ms; installed app still v0.9.5 |
| X1 | Confusing history/review screen explains state and next action accurately | UX reviewer/root | investigating | Owner screenshot mixes saved history, unknown original agent and contradictory continuation/approval instructions |
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
