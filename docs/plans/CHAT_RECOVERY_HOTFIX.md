# October 23 urgent chat recovery hotfix

Owner: Sergey. Implementation: existing Claude in Herdr tab 2 (`w6:p1M`).
Coordination, evidence, independent verification and release: Codex (`w6:p1`).
This records the active user request; it does not create a native goal.

Ship a focused patch release restoring normal chat, exact handling, followups and
steering. Batch the known defects before costly integrated verification. Preserve
native permissions, signed identity, concrete selected agents, history grants,
existing data and paused Play work. No silent retries of failed/completed work.

| ID | Target outcome | Acceptance check | Owner | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| T1 | Mark handled clears the exact request and its reports across linked devices | Actual UI producer → queued decision → host admission → status push; double handle, offline/restart, stale attempt and wrong identity checks | Claude implementation; Codex verification | verified | Actual signed sender convergence race PASS47.2s; exact handling/refusal UI passes3 widths; no rerun; prior live relief remains separately recorded. |
| T2 | Native technical errors are truthful system failures; genuine permissions remain actionable | Bridge RPC/start/resume/failed/empty/EOF cases; exact reason retained, no fabricated upgrade hint or agent attribution; native allow/deny/input/cancel preserved | Claude; Codex | verified | All5 actual installed harness tool/result journeys pass. Native bridge failure/input/permission matrices preserve truthful error and genuine needs-human; historical trigger remains unknown. |
| T3 | Reply, tagged followup and active steering reach the exact selected request/session once | Active/completion race, attachments, repeated rounds, ordinary Reply without explicit tag, terminal followup, unsupported steer, replay, wrong target/PID/topic/key | Claude; Codex | verified | Canonical plain Reply6 cases/12 sends passes1280/360/320; gallery ordinary Reply/draft/selection cases pass. Desktop3OS and Android affected Compose gates pass. |
| T4 | Agents tag the intended person when asking and use readable Markdown | Actual direct/group prompt and worker output paths for every supported harness; canonical mention chip and attention scope; no contradictory plain-text instruction | Claude; Codex | verified | Actual rendered canonical requester mention chip and Markdown elements pass; shared posted-body/prompt checks cover5 harnesses. No separate mention push claim. |
| T5 | Agent reactions attach to the exact human/agent message | Real supported reaction operation produces reaction state/chip, not a chat bubble saying reaction; duplicate/denied/wrong-message checks | Claude; Codex | verified | Same-host human/agent requester reactions, no-bubble and cancellation CAS focused races pass; real attached mark is distinct from text acknowledgement. |
| T6 | Focused patch is independently checked, published and delivered | Stable batch, affected tests for every harness, required integrated release/mixed-version/gallery gates, Claude/Codex checkpoint, checksummed artifacts and rollout checks | Codex coordination; Claude source fixes | verified | App v0.9.5 and matching gallery v2026.10.23.3 are public Latest; all payloads/parser files verify. Relay, Zenbook, laptop and running Contabo client updated. Phone installation unverified. See V0_9_5_RELEASE.md. |
| T7 | Already-read messages are not injected into Pi as fresh work | Reproduce reported notice against effective read state, session cursor and installed extension; verify actual model-context/queued delivery boundaries as well as Go filters; preserve genuine pending decisions, fresh unread and exact selected replies | Claude implementation; Codex evidence/verification | verified | v0.9.5 exact historical address/fingerprint matching closes the removed-phone edge. All five harness guards, 41-parent races, native3OS, mixed-version and installed laptop hook/current app exact-ID parity pass; stale copies absent, no read-marker changes. |

Current final qualification and release state: [v0.9.4 release record](V0_9_4_RELEASE.md).
The dated investigation checkpoints below retain their original failures and open-state history.

## Coverage matrix required before release

Enumerate the supported harnesses from current source, including Codex, Claude,
Pi and Antigravity (agy). For each record executed checks and genuine unsupported
operations separately. A Codex pass does not qualify another harness. Cover normal
answer/Markdown/tag, genuine native approval/input, technical failure, handling,
active steering or its documented continuation fallback, terminal followup,
attachments, duplicate/offline replay, and reactions. Use existing adapters and
fake native protocol fixtures first; separately record actual installed-native
execution. Never report an unavailable harness or opt-in skip as a pass.

## Evidence and incident inventory

Private evidence: `.agentnet/oct23-chat-urgent/` in the parent checkout. Do not
commit private messages, logs, screenshots, keys or database snapshots.

- Group/self-chat `fdc892…`; Zenbook Codex participation `13c6fac…`.
- Failed request IDs `884b2d…`, `7d7cbb…`, `ac67fe…`: fabricated transport reason,
  needs_human, attempt 1. Normal CLI force-handle succeeded for those three only;
  all now resolved, attempt 1, no rerun or deletion. Sender convergence unverified.
- Screenshot technical ID `f3dc5e…` must be mapped, not assumed equal to a host ID.
- No received decision rows under the three known host IDs/LIDs. Missing-daemon-wake
  hypothesis was refuted: storedControlSend calls notifyDaemon. Investigate actual
  sender outbox and exact UI ref/host mapping before changing delivery code.
- Followup investigation found four decisions under the screenshot's local physical
  ID `f3dc5e…`, all refused `no such request here`, all refusal statuses delivered
  to the laptop. `remoteRequestFor` validates an ID-or-LID row then returns the
  caller's physical ID unchanged; `Live.DM` passes `m.ID`. Browser uses LID first.
  This explains handling and explicit continuation failure for that screenshot.
  The manual host resolutions' nine status copies also reached delivered state.
- Ordinary Codex questions lacked @tag; tagged answers then hit the native failure.
- Same-host Codex succeeded on other requests. Compare session/cwd/resume and actual
  native errors. Sandbox mise auto-install failure is not daemon-cause evidence.
- Pi version request used Bezos `37e02711…`; weather/Markdown used Linux laptop
  `970a8e6e…`. Do not infer an automatic target switch from screenshots alone.
- Pi completed the Markdown sample successfully; inspect that exchange read-only
  as the comparison case, including its incorrectly posted reaction bubble.
- User requires all supported harnesses, approval edge cases and steering/followups.
- Existing v0.9.3 first-menu/timing/physical-device limitations remain separately
  recorded; they do not qualify this hotfix or justify unrelated scope expansion.

## Edge cases agreed with Claude

1. Group request handled on phone clears exact host item and all own projections.
2. Offline handle reconnects once; replay returns recorded outcome.
3. Double/two-device handle is idempotent.
4. Stale-attempt CAS refuses with current state, no mutation.
5. Active steering stays on the same turn.
6. Completion-racing steering becomes exactly one continuation.
7. Attachments preserve bytes, selected target, cwd and context.
8. Completed-answer followup resumes the exact authorized session once.
9. Failed/resolved followup never resurrects old attention or reruns old work.
10. Unsupported native steer falls back once without losing input.
11. Duplicate/offline-replayed envelopes never execute twice.
12. Selected-session reply reaches only its concrete target.
13. Plain Reply-button answer to a clarification continues once without a tag.

Claude consultation: parent `.agentnet/herdr/oct23-chat-consult-1.md`.
Codex owns this checklist; Claude owns implementation and focused regressions.
New owner instruction assigning Claude source writes supersedes the transport
skill's generic parent-only source ownership rule. No descendants/new panes.

## Implementation checkpoints

- Claude accepted the editor role after Sergey's direct confirmation in tab 2.
- Shared-ID normalization, authenticated handled-refusal projection and its UI
  message are implemented. Group decision and existing related focused checks
  were run by Claude; the complete rendered/browser acknowledgement gate remains
  open pending the stable batch.
- Reaction-only parsing and genuine attached-reaction settlement are implemented.
  Independent review caught a cancellation race in settlement. Claude added a
  running-state CAS and an after-delivery Stop test that fails with the old
  settlement and passes with the correction. Integrated verification stays open.
- Shared Markdown instructions and exact group requester mention syntax are in
  progress. Mandatory output behavior, native error classification, Reply/steering
  and the five-harness matrix are still open; no full hotfix qualification claimed.
- Matching gallery worktree: parent `.agentnet/oct23-chat-gallery`, branch
  `fix/v094-chat`, based on published-gallery docs commit `d4d8943`. Claude owns
  necessary standalone source/test corrections; Codex owns pin/build/release
  metadata and the integrated gate. Dirty gallery main remains untouched.
- Linear incident record: MEL-497, comment `3a7590b5-e602-41ae-8009-333eb75194a5`.

## Independent focused native checkpoint

Codex executed `TestLiveNativeHarnessGroupTools` with
`AGENTNET_LIVE_NATIVE_ACTION=read` separately for all five installed harnesses.
All five passed; zero skips. Each actually read a fresh unpredictable temporary
file token with its native tool and returned the signed, attributed result through
a disposable Hub/group. Real native configuration/permissions were retained.
Outer process times: Codex 20.8s, Claude 20.5s, Pi 17.6s, OMP 55.4s, agy 52.1s.
The 296-file native production manifest is identical before/after. Private raw
Go JSON and summaries are in parent `oct23-chat-urgent/evidence/actual-native/`.

This qualifies that native tool/result path; it does not substitute for the
specific approval/failure, Reply/steering, rendered mention/Markdown/reaction or
release gates. The separate real `TestCodexRoomActualQuestionSmoke` also passed
in 19.73s: native room tool, synthetic signed peer reply and signed final answer.
No closed user request was restarted. The historical failure trigger remains
unproven; this is positive execution evidence, not a reconstructed historical cause.

Review then identified another T5 gap: the reported praise and Pi share the same
host, but existing reaction eligibility rejects local requests. The implementer
has been asked to reproduce/fix that exact same-host path and remove the silent
emoji-bubble fallback. That reopens affected reaction checks; ordinary native
read/result evidence above remains separately recorded. The complete batch is
not yet qualified or published.

## Latest bounded review

The 577-parent related client selection ended FAIL (661.020s), with four named
exact-body expectations missing the new requester mention. Claude corrected those
four expectations and reports the focused selection passing; retain the original
failed run and collect exact focused logs. Do not restart all 577 for that change.

The new same-host human reaction test now uses actual AskAgent. Independent review
found the agent-to-agent reaction-only test still explicitly accepted a text
`reaction:` bubble; that contradicts the owner's requirement and is returned to
Claude for a real reaction plus a readable peer result, preserving the peer wait.
The local single-device/no-remote-copy limitation remains honest not-delivered,
not a fabricated emoji message.

Pi laptop evidence is read-only and private: exact extension-bound binaries both
report clean v0.9.3, excluding a stale path. Six notices recorded today have
effective unread rows; the older persisted notice contains five already-read
rows. Historical custom metadata remains native model context. The causal
context-delivery regression and correction remain open with Claude.

Codex independently ran four current-source UI parents: own-group Composer
8.48s, linked-direct Composer 2.83s, browser request continuation 0.38s and
TypeScript DTO consistency. All passed, zero skips; package 11.712s, outer
35.860s. These exercise actual Composer code with real signed DTOs and the
browser Engine controls; they are not rendered bundled-asset proof. Raw JSON
and command are in parent evidence/root-ui-focused.

Canonical Comic build passed in11.856s with all captured source hashes unchanged.
Codex then used a private copy of the existing rendered Q1 fixture, replacing
manual tag entry with plain text and selecting actual Reply in both orders.
Group and DM, two rounds each at1280/360/320, all six cases/twelve sends passed
in24.832s. Each exact followup reference/attempt0 was asserted, attachment
retention stayed checked, and the first body contained no manually entered tag.
No unrelated ask was sent. Screenshots and exact private fixture transformation
are in evidence/plain-reply-rendered. The inherited fixture summary says tagged/
four skins; this run was Comic only with plain Reply, as its provenance records.

Canonical resolution controls independently passed1280/360/320 in5.879s using
a private existing-fixture copy: exact capability check, failed-response retry
retains send ID, queued does not clear work, new host refusal visibly renders,
and confirmed resolved status removes controls. Synthetic status injection is
recorded as such; actual signed sender convergence was requested in the Go
regression before claiming end-to-end completion.

Claude's reaction/mention focused race selection passed30 parents in456.272s
with no race reports. The peer reaction correction remains affected/open.
Linear checkpoint comment15e0ed86-6e6a-4151-b1db-e055d0fe63cb records T7 and
current evidence without private chats or raw native logs.

Checkpoint2 collected: three Go production files changed after the ordinary
installed-native checks (assistantreaction, agentjob, worker). The same-host and
peer reaction checks now pass. Peer reactions attach and return a readable
system acknowledgement instead of a literal reaction trailer; human reactions
post no text bubble. Other checkpoint1 native adapter files are unchanged.
The five repaired related-parent race selection passed88.4s, preserving direct
DM untagged output and strict identity/attempt/no-duplicate assertions.

T7 must also cover an idle notice queued after the last assistant message, then
the chat being read before the next prompt. Keeping every notice after the last
assistant would retain that stale notice. Exact receiver inputs and ordinary
native history remain outside the passive-metadata filter. Core freeze is
requested after T7 and the T1 signed sender-convergence assertion, so integrated
qualification can overlap only the separate gallery implementation.

Canonical Markdown/requester rendering independently passed six Comic cases
(DM+group1280/360/320) in21.210s: own canonical person chip is highlighted,
bold/checklist/code render as elements, raw mention markup is absent, and
plain Reply retains exact ancestry. Private synthetic fixture transformation,
entry hash and screenshots are in evidence/mention-markdown-rendered.

T7 first-draft context filter has a retained RED: historical notice drops, but
[assistant, idle notice, new user prompt, exact receiver] retains the stale idle
notice. Input history is unchanged and exact receiver preserved. Evidence was
returned to Claude; no T7 verification claim yet.

Required mixed-version gate passed: TestMixedVersion against newest published
v0.9.3, AGENTNET_REQUIRE_COMPAT=1, parent67.000s/outer69.242s, no skip.
All captured production Go and the Pi extension bytes were unchanged during
the run; evidence/mixed-v093 retains JSON and source hashes. Go vet ./...
also passed14.440s with production Go unchanged.

The refined actual Pi context handler independently passes both retained private
cases: historical replay and idle-notice/read-before-prompt. Exact receiver
input and native history are preserved. This is narrow request-context proof;
the implementer is still completing permanent lifecycle/ack tests.

Actual Pi1.4.0 provider-request capture now confirms T7's causal boundary:
Claude ran the installed native binary with disposable configuration/session and
a loopback stand-in provider. Codex independently inspected both first requests.
Base extension sent the historical OLD NOTICE marker; fixed extension omitted
it; both retained the current unread notice. One provider request per final run.
Capture hashes and reduced facts: evidence/pi-native-context-capture-summary.json.
The initial private stub incorrectly returned news on every Stop and looped;
that failed diagnostic is retained and was corrected to real Stop semantics.
No installed configuration, actual user conversation or live model provider was
modified or used by this capture. Laptop's installed latest Pi binary is also
1.4.0. OMP shares the extension; actual OMP context capture is not claimed here.

Independent native-failure matrix passes: nine adapter parents/69 hierarchical
pass records,0skips,2.519s; worker native-exit21 modes across Claude/Pi/OMP,
26 hierarchical pass records,0skips,4.824s. These are owned protocol/process
fixtures, distinct from the earlier five installed-native positive runs.

## Final source and qualification checkpoint — October23

This checkpoint supersedes the earlier open implementation notes above.
Claude's bounded handoff is frozen (HERDR_TASK_COMPLETE_a2e4916b). Core
production is b192a6f6; subsequent changes correct only test expectations and
contract documentation. The three standalone gallery Reply gestures now retain
text/files typed before Reply, clear New topic deliberately, and send once.
Their full matching build/gallery gate remains required.

Signed T1 sender convergence passed race47.2s, preserving unrelated pending
work and attempt counts. T2 protocol/worker fixtures distinguish native requests
for input from technical errors across supported adapters. Canonical T3/T4
rendering passed DM/group at1280/360/320, with exact Reply ancestry, requester
chip and Markdown elements. T5 same-host human and peer attached reactions pass;
peer waits receive a readable result, not a reaction text bubble. Native positive
tool/result checks executed all five installed harnesses, zero skips.

T7 also passed actual installed OMP18.12.6 provider-request capture: base retains
the old notice; fixed drops it while retaining fresh notice and native history.
Together with actual Pi1.4.0 capture and lifecycle/ack regressions, this verifies
the shared extension correction. The exact frozen extension was installed on
the Linux laptop with preserved constants and binary, backed up outside its
autoload directory. Existing Pi requires /reload; activation is not yet observed.
No native permission or session-history file was changed.

Run39957726105 passes corrected Compose on all three desktop systems and native
Android. Retain initial run39952907831's unaffected passes; only failed/missing
race shards and exact native parent selections are being rerun. The media-guard
fix preserves unsupported-port classification and raw failures; its assertion
now counts the driver's actual320/360 viewport contexts. Independent focused
race passes. No test deadline or product threshold was raised.

## Fresh T7 discrepancy — owner confirmed October23

The owner supplied a new Pi screenshot and confirmed the displayed messages were
already read in AgentNet, with no unread chats since morning. The extension fix
above handles retained native context; it did not cover this separate hook
projection defect. T7 is reopened and publication remains pending.

Read-only checks of the actual laptop app and its hook-bound home establish:

- Same default workspace, person/device address and data home; no stale extension
  or alternate-workspace explanation. The deployed extension hash is unchanged.
- App unread page has three private-device progress/result/answer rows. The hook
  reports913. The old group answer from the screenshot is projected as outgoing
  by ConversationMessages but was counted as an unread incoming copy by the hook.
- The first narrow own-device correction removes244 false entries but leaves669;
  that is not a complete fix. Anonymous metadata breakdown found540 history
  replicas,47 membership/event records and70 resolved reports, plus ordinary
  records including seven already-erased turns. No inbox or read state was changed.

Claude is extending the same two-file correction (attention.go and its existing
read-sync fixture) to reuse existing message visibility rules. Explicit V2 unread
generations, real pending review and exact selected replies must be preserved.
The all-five-harness table covers Codex, Claude, Pi, OMP and agy. Current passing
release evidence remains retained; affected final-source checks are required
before tagging. A later laptop SSH timeout is retained, not reported as a pass.

Gallery gate at corec29 passed306 unit checks with zero skips and every built
package/overlay contract. One real-world Zoom provider readiness check failed
when both navigation layers coexisted. Claude's one-line fixture correction
uses the sole active layer; focused browser proof is collected separately.
Retain every unaffected world cell; no second full-gallery run is justified by
that readiness-only fixture change. Final core pin/equivalence remains pending.

Relay backup preparation passed: native archive contains8,600 verified stored
ciphertexts, with fresh database snapshot and successful v0.9.3 offline restore.
Only the same existing Hub was stopped/restarted for12.599s; exit0. It is healthy
on v0.9.3, schema25. Candidate restore and public artifact verification are still
required before activation. No data restore into the live volume occurred.

## T7 final source freeze and affected verification

Claude's final correction is frozen at attention.go SHA256cd8d1b74… and its
read-sync fixture0b36c695…. It uses existing eligibility, history-origin,
verified own-device direction, retraction and erased-row rules. All four queries
bind the existing notice arguments before review states. No schema, cursor,
inbox or native permission change is introduced.

The new regression has causal RED against both the old source and the first
own-copy-only draft. It preserves real pending review, foreign/unverified
claims, deliberate unread and exact receiver state. All five harness names run
the same checks. The41-parent focused selection passed regular12.8s and race
294s; original REDs remain retained. Root's final full vet passed10.030s and
required mixed-version vs publishedv0.9.3 passed75.49s(parent)/82.352s(outer),
with all production hashes unchanged during both. Native affected reruns remain
required before publication.

Read-only evaluation of the exact final query on the laptop removes the old
screenshot entries and the inert backlog, returning the prior three actual
private-device rows plus a later chat arrival. This is actual-data predicate
proof; a simultaneous live app comparison was unavailable after its ui-url
closed, and is not claimed. No user data was marked read to produce the result.

The sole gallery failure is closed by the frozen one-line readiness fix,
SHA256ad56c8d5…, with deterministic RED/GREEN and a fresh real Zoom browser
provider journey. Retain306 passing unit checks and every other full-gate cell.
Imported backlog without a deliberate unread mark stays quiet by the existing
notification rule; native generic notices remain passive. Unexecuted physical
platform scenarios are not counted as passes.
