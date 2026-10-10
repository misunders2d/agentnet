# Mobile startup recurrence after v0.8.16

**Superseded:** completed in [V0_8_17.md](V0_8_17.md).

**Stopped for owner-requested Claude handoff.** Authoritative continuation:
[HANDOFF_CLAUDE_MOBILE.md](../HANDOFF_CLAUDE_MOBILE.md). Source changes are partial
and unverified, with a known runtime shadowing bug. No Codex implementation,
test or release process remains active.

Owner reports on October 10 that the updated phone still needs more than 20
seconds before first messages appear and remains extremely slow. Previous
source regressions passed; the physical result did not meet the requirement.
Do not equate the prior fixes or green tests with resolution of this report.
Owner clarifies that the chats are blank during the wait. Many archive notices
and Amazon_team agent sandbox Waiting also remain unchanged since yesterday.
MEL-546, MEL-588 and MEL-580 have fresh recurrence comments; no closure.

Requirement: reopening an enrolled phone with saved conversations must show
useful saved messages promptly while network recovery continues independently.
Existing encryption, identity/admission checks, delivery facts and original IDs
remain intact. Preserve live clients, history, grants and active agent sessions.
No resets, relinking, blanket suppression or task replay.

Root edits this checklist and owns integration/Linear. GPT-6.1-sol helpers
independently inspect startup, data/render work and native-app alternatives.
First establish a measured failing path and realistic backlog; no speculative
rewrite, extra services, polling, or full test matrix for each hypothesis.

| ID | Target outcome | Acceptance check | Owner | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| T1 | Attribute first-message delay | Measure storage, UI bootstrap, first overview/chat and recovery separately on realistic persisted history | Root + startup helper | agreed | Phone recurrence; exact blocking stage unverified |
| T2 | Fix demonstrated startup bottleneck | Same representative fixture fails before/passes after; cached chat works during slow/offline recovery | Assigned implementation after diagnosis | agreed | Pending |
| T3 | Preserve correctness and responsiveness | Focused authority, restart, delivery, ordering and UI checks; independent review | Receive/data helper + root | agreed | Pending |
| T4 | Assess native mobile against actual needs | Compare current code and official platform/messenger designs; separate foreground bottlenecks from background lifecycle limits | Architecture helper | agreed | Pending |
| T5 | Explain and recover retained sync notices / Waiting | Correlate exact stored admission/result facts; retain blocked invalid records and genuinely pending jobs | Root | agreed | Phone recurrence; current phone records unavailable |

## Measured failures before edits

- Actual device bootstrap + loader + Comic + real IndexedDB with 500 generated
  cached messages: data load 20.8 ms, first DM projection complete at 829 ms,
  but overview does not return within 15 seconds when the relay request is held.
  `overview -> notifyView -> notifyInfo -> features -> /v1/version` makes optional
  notification capability gate the saved Chats list (30-second network bound).
  Owner's screenshot shows exactly the mounted Comic skeleton list. This is a
  matching reproduction, not a remote profile of the actual phone.
- Generated real IndexedDB projection: 1,500 inbox + 300 outbox / 10 DMs takes
  3.862 seconds. 5,000 inbox + 1,000 outbox / 30 DMs takes 43.388 seconds on
  unthrottled desktop Chrome with no network dependency. The larger overview
  performs 393 full inbox reads, 573 full outbox reads and 7,971 person-table reads.
  Prior tiny/simple fixtures did not represent this scaling.
- Topics helper owns notification-view dependency and actual-startup regression.
  Models helper owns explicit per-operation read reuse for display projections;
  action/admission paths continue reading current authority. No persistent cache
  or mutable engine-wide read mode. Helpers coordinate disjoint engine methods.

Actual phone timing and convergence remain required evidence; synthetic or
desktop browser measurements must be labeled accurately. Retain unchanged
qualification and batch affected checks after the candidate is stable.
