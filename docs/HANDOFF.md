# AgentNet Revival — Fresh Agent & Contributor Handoff Guide

Keep notable user-facing changes in [CHANGELOG.md](../CHANGELOG.md) under
Unreleased as they are implemented. Move shipped entries into a dated version
section at release; keep detailed verification and remaining work here.

## Current published release — v0.8.14, October 9, 2026 (UTC)

[Download v0.8.14](https://github.com/misunders2d/agentnet/releases/tag/v0.8.14).
Published at **19:34:43 UTC** from
`760e945a28c38461c74d84c7d4c9610464573dea`. Product code is unchanged from
`cde2cba6`; final test/workflow corrections were qualified at `36260adb`.
All five final [packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37980178645)
passed. All 12 asset digests and all 11 SHA256SUMS entries matched. Standalone
and bundled Linux commands report the exact clean tagged revision.

The [release checklist](plans/V0_8_14.md) records composed source qualification:
retained native Linux/macOS and Windows package results, all 965 Linux client
race assignments, other-package race/browser/desktop/container checks, exact
Windows controls/governance, and disjoint Windows client
[A–G](https://github.com/misunders2d/agentnet/actions/runs/37978551047)
(**655.004 s**) and [H–Z](https://github.com/misunders2d/agentnet/actions/runs/37978560159)
(**424.799 s**). The Windows list has 945 testing.T declarations, excluding
TestMain (387 + 558); it is separate from the Linux list. Initial full runs were
not all green. No deadline increased. Four shell-stub history scenarios execute
on Linux/macOS and skip Windows; a green Windows job does not mean they ran.

The existing Contabo relay was verified at **19:35:17 UTC**, with container
start **19:35:15.674 UTC**, on image `agentnet-revived:v0.8.14-760e945a`.
The verified stopped-state backup is
`/opt/agentnet-revived/backups/pre-v0.8.14-dx443TBQ`. Normal HTTPS checks from
server and laptop passed with the original realm and data volume. The neighboring
service is unchanged; zero OOM events or restarts. Deployment changed only the
existing relay, with no installed-client mutations by Codex.

**Urgent post-update repair — focused checks passed; integrated gates pending.** After
publication, the owner updated Zenbook himself and reported
“Could not connect 127.0.0.1: Connection refused.” Read-only inspection confirms the app/launchers use the permanent application
data path, not Downloads. Both running backends subsequently reported the exact
clean v0.8.14 tag; update activation is running and authenticated local Overview
returns200/v0.8.14. The shell can retain a failed page during attached-daemon
restart: Go does not wait for UI readiness after lock reacquisition, and Rust
suppresses a same-URL page event. Both source gaps now have failing-before and
passing-after regressions; six Go tests also pass under race. Root reviewed
the changes. Full Tauri compilation and physical recovery remain unverified;
tray Quit/reopen was advised, but recovery is not user-confirmed. This takes
priority over v0.8.15 feature work. A separate
new report says the phone on v0.8.13 shows “Sending” for 15 minutes; its exact
cause is unverified. v0.8.14 fixes confirmed sync/queue blockers, but no exact
fix or physical convergence is claimed for this report. Preserve live sessions,
identity, data and native permissions; no reset, relink or task replay.

Physical phone convergence and actual post-fix ThinkPad CPU remain unverified.
The shutdown abort has no attributed fix. The owner explicitly made MEL-590's
historical Mac installation report nonblocking because the app is already
installed. Absent Developer ID signing/notarization is a possible barrier, not
an established cause or fix. Installers remain unsigned.

**Active next release — v0.8.15:** based on the released tag with the urgent
post-update repair first. Follow [V0_8_15.md](plans/V0_8_15.md) for
MEL-591/592/571/583/574/575, verified directly in Linear. Zenbook owns overnight
implementation/integration; ThinkPad is offline and no peer response is required.
MEL-527 remains v0.9. All six features have focused native/browser/rendered
evidence and independent review. Cross-chat history sharing covers accepted
destination guests, exact selected encrypted files, stale audience/source review
and same-operation retry. The first combined qualification found stale fixtures
and a real cancellation race. The corrective batch and retained passing evidence
are recorded at the top of the release checklist; it is not published yet. A
measured worker-query planning regression is corrected and reviewed. The final
named callback race passes with unchanged bounds; earlier deadline failures,
including on pre-feature source, remain recorded. A further signed regression
found that a completion status arriving before its request could be permanently
rejected. Native and browser fixes pass focused checks, including retained native
invalid records and browser proof-pending records. Exact executor pins are
rechecked through delayed recovery and atomic admission. The final focused race
check passes 29.082 s and vet is clean; the next combined qualification is ready.

**October 9 evening correction:** MEL-588 rejected-history flood and MEL-580
answered requests remaining pending are mandatory v0.8.15 fixes. The active
checklist's opening checkpoint records exact source causes, failed-before tests,
focused passes, independent review and remaining verification. Repeated signed
DM lifecycle history was incorrectly attributed to its forwarder; DM agent
outputs also retained host-only reply IDs. Narrow native/browser regressions
now pass. A failed browser lookup also interrupted later push messages; exact
encrypted carriers now wait durably without receipts while later messages can
proceed. Actual IndexedDB tests cover restart, changed keys and stream shutdown;
group catch-up and file-recovery checks pass. No physical phone convergence or
release is claimed.
After v0.8.15 the owner requests a dedicated mobile responsiveness/sync phase.

## Previous release — v0.8.13, October 9, 2026 (UTC)

[Download v0.8.13](https://github.com/misunders2d/agentnet/releases/tag/v0.8.13).
Published at **15:23:08 UTC** from
`ee420e63087bdc224d07bdd2f163de13d27c5207`, a documentation-only release record
above the qualified application and corrected tests. All five final
[packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37949926805)
passed, including native shell tests on all three systems and Linux AppImage
replacement/restart. All 12 downloaded asset sizes/GitHub digests and all 11
SHA256SUMS entries matched; the published assets retain those exact digests.
Standalone and bundled Linux commands report the exact clean tagged revision.

The existing Contabo relay upgraded at **15:24:52 UTC**, after a verified
stopped-state backup. Its running binary and normal HTTPS from both server and
laptop report v0.8.13 on the original realm and data volume. Zero restarts/OOM
events; the neighboring service retains its original start time. At publication,
GitHub latest was v0.8.13. No installed client, live client grant or desktop session changed.

### v0.8.13 scope and qualification

Branch `release/v0.8.13`, starting at `7e0ea2ef`, contains the performance-first
bug fixes tracked in [the release checklist](plans/V0_8_13.md). The owner
deferred ongoing topic-only access and proposal editing
(MEL-582/584) to the following release; topic merging remains backlog.

The owner explicitly confirmed **Use native permissions unchanged** (MEL-504),
superseding earlier question-specific restrictions. Presets, room transport,
background CLI routes and product guidance now retain each owner's native
setup. Existing request acceptance, grants, exact receiver bindings, signed
membership, encryption and output checks remain enforced; native requests for
approval are reported as needing attention, never automatically granted.

Implemented repairs include indexed history selection, bounded browser recovery,
long-timeline typing, reciprocal exact agent questions, one visible multi-target
send, compact stopped cards, terminal Pending projection, old Main topic controls,
human guest invitations in Comic OKs and safe Markdown attachment previews.
Focused tests and comparative synthetic measurements are recorded in the
checklist. Combined source qualification is complete: the full run at `7269ac32`
([37946430562](https://github.com/misunders2d/agentnet/actions/runs/37946430562))
and focused correction run at `706df9cf`
([37948969712](https://github.com/misunders2d/agentnet/actions/runs/37948969712))
cover native Linux/macOS/Windows, all client and other-package race tests,
real IndexedDB, all desktop builds/shell checks, Linux AppImage replacement and
restart, and the container journey. The first full run was not clean: obsolete
fixtures, macOS path normalization, a cleanup-observation race and unlinked-owner
prompt wording required the bounded corrections documented in the checklist.
All affected cases pass on all three systems, with local focused race and vet;
unchanged passing evidence is retained. No full suite or timeout was increased.
Final tag-stamped packaging, downloaded assets and publication passed as above.
The redundant main-push full run was cancelled after confirming the release
record changed only documentation; final tag packaging completed normally.

No installed clients, live client databases, grants or desktop sessions were
changed. Physical phone convergence and ThinkPad's reported sustained busy
renderer CPU remain unverified; the synthetic native profile did not reproduce
that load. Deployment changed the existing relay only.

**Historical v0.8.13 owner restart boundary:** after publishing v0.8.13 and
verifying the relay, save this handoff and pause for the owner's update/restart. Do not start v0.8.14
until explicitly resumed. Its immediate issues and today's bug scope are recorded
in [the checklist](plans/V0_8_13.md#owner-restart-boundary-and-v0814-scope), including
all own-device direct chats (MEL-569), the separate invitation-sync queue blocker,
physical catch-up/quarantine, exact unresolved needs-human topics, own-agent
consent, bounded inbox output and remote-agent labels. Topic-only ongoing access
and editable proposals remain next-release features. No old task is replayed.

## Previous release — v0.8.12, October 9, 2026 (UTC)

### Published v0.8.12

[Download v0.8.12](https://github.com/misunders2d/agentnet/releases/tag/v0.8.12).
Published at **12:04:00 UTC** from
`ba49514927d082bb0b7de46b9ceca1f934c364e4`, a documentation-only release record
above the qualified application and corrected tests. All five final
[packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37926597371)
passed, including native shell tests on all three systems and actual Linux
AppImage replacement/restart with durable completion at v0.8.12. All 12 downloaded
asset sizes/GitHub digests and all 11 SHA256SUMS entries matched. Standalone and
bundled Linux commands both report the exact clean tagged revision.

The existing Contabo relay upgraded at **12:06:03 UTC**, after a verified
stopped-state backup. Its running binary and normal HTTPS from both server and
laptop report v0.8.12 on the original realm and volume; `teams2` is advertised.
Zero restarts/OOM events; the neighboring service is unchanged. GitHub latest
is v0.8.12. No installed client, live client grant or desktop session was changed.

Update source computers through the existing app/CLI controls, then refresh
linked phones. The inspected source laptop was still running v0.8.10 during
qualification; refreshing its phone does not update that producer. Actual
physical-phone history convergence remains unverified.

### v0.8.12 scope and qualification

The owner authorized implementation, integration, qualification, publication and
upgrade of the existing relay. Branch `release/v0.8.12` integrates ThinkPad's
MEL-502 collective tags (`4fc19719`, applied as `fec2b046`) and the bounded fixes
in the [release checklist](plans/V0_8_12.md). The direct Linear sweep records
[all 79 open issue dispositions](plans/V0_8_12_ISSUES.md), including unresolved
physical journeys and explicitly deferred features. Earlier notification,
compact approval and concurrency branches are already in v0.8.11; they were not
applied again.

Implemented: exact current-chat collective recipients with independent retries;
Comic own-human task permission through existing verified-person grants; exact
pending-request navigation; prominent available-version heading and Update;
clearer default/local agent labels and group invitation wording. Independent
native device trust stays separate and now displays inactive after a pinned-key
replacement. No authority, native sandbox or automatic replay policy changed.

For MEL-580, read-only retained metadata showed an unmatched task: later final
replies belonged to other requests, and ordinary reply messages did not complete
it. The new list exposes exact contributing requests and their existing actions.
It does not claim to repair an unproven stale state or silently close old work.
Shared vectors and a reopened synthetic history store verify late exact replies
clear the derived pending list without execution-state mutations.

Focused native, browser, authority/race, desktop/narrow rendered checks and
repository-wide vet pass; detailed evidence is in the checklist. Root inspected
the affected screenshots and reviewed the merged source.

The full integrated run at `ea1b51e1962fb1ecf6a93ad5fa6c96026275f5e9`
([37923678631](https://github.com/misunders2d/agentnet/actions/runs/37923678631))
completed with three test-fixture failures: Classic and Zoom standalone checks
omitted the new shared tag helper's source path, and an existing own-device
removal test issued a second independent removal before its device had received
the first verified roster change. The latter correctly hit the stale-roster
refusal; it reproduced in 6 of 20 focused race runs. The test now waits for that
exact roster, preserving the refusal and pending-task demotion assertions, and
passes all 20 repetitions (51.066s). Production code did not change.

Corrections at `bfa6f896fd54fc93a9aded3945d57fc0b4c30973` passed the affected
native tests on Linux, macOS and Windows, with repository-wide vet, in
[37926269863](https://github.com/misunders2d/agentnet/actions/runs/37926269863).
All unaffected results from the full run remain valid: native packages and real
process journeys, all other client race tests, other package race tests, desktop
builds/native shell tests and the container journey. The corrected standalone
packages pass race locally (2.559s/2.604s); the actual Chromium IndexedDB gate,
skipped after the original fixture failure, also passes under race (1.829s).
No timeout was raised and no full suite was repeated. The release-record commit
changes documentation only from this corrected candidate. Final tag-stamped
packaging and downloaded-asset checks passed as recorded above. The redundant
main-push full run was cancelled after verifying that the release-record commit
changed documentation only; the required tag packaging completed normally.

## Previous release — v0.8.11, October 9, 2026 (UTC)

### Published v0.8.11

[Download v0.8.11](https://github.com/misunders2d/agentnet/releases/tag/v0.8.11).
Published at **10:50:40 UTC** from
`0209d7f1790b85ba817d3e3842206c82105e5e31`. This release-record commit changes
only documentation from the final qualified application source `3c880b65`.
All five [release packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37919244927)
passed, including native shell checks and the final Linux AppImage replacement,
restart and stable-install journey. All 12 downloaded asset sizes/digests and all
11 SHA256SUMS entries matched; standalone and bundled Linux CLI report the exact
clean released revision.

The existing Contabo relay upgraded at **10:51:31 UTC**, after a verified
stopped-state backup. Its running binary and normal HTTPS from both server and
laptop report v0.8.11; the original realm and data volume remain unchanged.
Zero restarts/OOM events; the neighboring service retains its original start
time. No installed desktop client or live client data was changed. GitHub's latest
release is v0.8.11. Update source laptops through their existing app/CLI controls
and refresh linked phones; source devices must be online to provide retained
history. Actual physical-phone convergence is still unverified and must not be
claimed from producer preparation or automated tests.

### v0.8.11 scope and verification

The owner requires the remaining mobile history and Held back failures in this
release, together with the integrated unread and reaction fixes. All verified
own human devices represent one person: histories and read state must converge,
including offline and reordered arrivals. Recent history should become useful
first, with older pages continuing in the background. Reuse the existing signed,
encrypted delivery, durable outbox, exact logical identities and push upkeep.
Telegram's [descending history pages](https://core.telegram.org/method/messages.getHistory)
and [saved update/gap recovery](https://core.telegram.org/api/updates) are behavior
references, not a change to AgentNet's trust or storage model.

Integrated changes: incoming-only native topic unread projection; ordinary
held-person-turn resolution parity and truthful dismissal failures; frequent
reaction choices and stable human-count chip ordering. Proof-pending notices
use the existing Archive action while retaining ciphertext and automatic recovery;
a changed safety reason resurfaces. Long raw-link compaction already shipped in
v0.8.9; existing rendered and parser regressions remain valid.

Outgoing invitations have an inert encrypted own-human status mirror, separate
from executable local intents, with monotonic versions, exact current-human/key
checks and explicit `own2` compatibility. Native focused race passed 8.695 s;
17 browser checks passed in Node and real IndexedDB. Independent review found a
post-capability-lookup authority race; its before failure and after regression
close that gap. Mirrored pending invitations render in all three skins at both
widths; the integrated fixture passed 7.425 s.

Native and browser catch-up now discover missing jobs, send recent pages first,
resume older pages and independently consume late accepted arrivals. Durable
exact-copy ledgers and bounded deferred references preserve retries without a
polling loop. Missing context in one chat does not block another chat. Original
snapshot cursors remain intact. Native focused race passed 104.473 s; browser
vectors passed 661 checks each in Node and real IndexedDB before the final
historical-witness merge. A real linked-browser journey also reproduced a stream
cancellation hang in v0.8.10; the existing reconnect path now resumes it.

Historical agent traffic whose host left and rejoined uses a retained original
signed membership witness. It is carried only as encrypted inert history, never
installed as current membership, grants or runnable work. Original signed
commits/rosters, exact admissions, present own-human authority, pins and file
hashes remain mandatory. Missing original ciphertext remains deferred. Focused
native race passed 54.003 s and browser race 15.156 s; independent merged-source
review found no remaining authority issue. A verified witness can satisfy its
exact predecessor dependency without synthesizing an inbox or live event row.

An offline private-copy preflight caught source projection loss of topic/human
metadata and folded edits. Own sync now reuses a complete immutable source reader;
selected excerpts retain their existing signed hashes. Original attachment
recovery has matching own-human/admission/pin gates; its focused race passed
16.599 s. Missing reply ancestry does not block a separately verified reply;
exact control references and signed event dependencies still wait.

The same preflight exposed an unused task key belonging to a removed phone.
Historical validation now uses its exact original signed author roster/admission,
without restoring any current device membership or execution right. The natural
regression failed before (3.118 s), passed after (4.899 s), and the combined
native focused race passed 82.244 s. Browser counterpart passed 710 signed Node
checks and 710 real IndexedDB checks (60.635 s); independent review found no
live-authority expansion. All three final interface builds/integrity checks pass;
progress stays unfinished for deferred work and distinguishes queued history
from delivered history.

Historical lifecycle records transported through a since-removed own human phone
now reuse the verified original human roster and exact counted signed dismissal.
Captured-consent mismatches retry only inert history against its original witness;
live admission remains unchanged. The two natural native regressions failed
before and passed under race in 36.299 s; vet passed. Browser verification passed
731 signed checks in Node (21.457 s) and actual IndexedDB (43.061 s). Independent
source review found no live authority expansion.

The final fresh offline private-copy preflight passes in 6.333 s: the original
blocked message carries its verified witness and bounded catch-up completes
without deferred records. This proves producer preparation, not phone delivery.
All HTTP attempts were intercepted and refused before network access; no live
data or process was changed. Both private database/key copies and the temporary
diagnostic test have been removed. Detailed aggregate evidence remains local.

The owner's newly reported topic-name divergence was a separate local-only
preference path in both native and browser clients. Own-human title/reset sync
now uses exact topic scope, durable causal revisions and the existing encrypted
own-device transport. Migration preserves saved titles; explicit edits outrank
old migration seeds. Concurrent edits converge deterministically, resets survive
reordering, and absent history can receive an inert preference. Private topic
labels remain private to the person. Native focused race passed 15.833 s,
protocol/envelope race and affected vet passed; browser 41 checks passed in Node
and real IndexedDB, plus 21 native/browser wire cases. Independent review closed
a migration ordering edge and found no remaining authority issue. Comic title
wording and existing desktop/phone rename/reset checks passed 37.461 s.

Before freezing the candidate, the owner reported preview routing and selected
topic unread inconsistencies on MEL-494/MEL-545. Chat summaries now carry the
exact preview reference, selecting a stable logical ID only when unambiguous.
All skins resolve the actual topic before acknowledging its visible messages;
delayed history retains that target until explicit navigation supersedes it.
Topic lists refresh on read changes, failed acknowledgements remain retryable,
and unseen sibling topics stay unread. Both failures reproduced before the fix.
All six skin/width preview cases and all six unread cases pass, including cold
startup, delayed history, stale navigation, logical-ID collisions and read-only
remote changes. Native API/alias checks passed 0.547 s; independent source review,
all three canonical builds and bundle integrity checks passed.

MEL-580's Waiting report has no verified false-pending
reproduction: exact request/reply correlation remains authoritative; unrelated
later answers cannot close another request. No live state was changed. ThinkPad
separately owns MEL-502 collective tags and latency investigation; avoid duplicate
writers. The full/native qualification and final corrected-path results are recorded
below. Clients update through their own controls; actual phone convergence
remains a separate physical check.

The first full integrated qualification at `634691e7`
([run 37914687984](https://github.com/misunders2d/agentnet/actions/runs/37914687984))
completed with failures. Desktop builds/restart checks, the container journey,
three client race shards and all unaffected package tests passed. Corrections
were batched before requalification: updated fixtures for intentional OKs actions,
capability inheritance and immutable history; terminal status for removed history
recipients; exact signed own-edit recovery after original readers withdraw;
browser control indexing and original-author projection; and bounded file work
during bulk catch-up. The Windows governance journey now waits for the direct
incoming file it intends to verify, since a preceding history copy may be replaced.
No timeout was raised. Passing unaffected evidence was retained; all affected
paths then passed on the final corrected candidate before tagging.

The corrected application source at `3c880b65183d051dbeb25bc41855eaeab1156377`
passed all affected native paths on Linux, macOS and Windows in
[run 37918602575](https://github.com/misunders2d/agentnet/actions/runs/37918602575).
Final focused race checks cover history/file fairness, removal and frozen-person
boundaries; final browser checks passed 755 signed Node vectors and 756 actual
IndexedDB vectors. Repository-wide vet passed locally and on all three runners.
The release-record commit changes documentation only; application source remains
identical to that qualified revision. Final packaging passed its native shell
and Linux AppImage replacement/restart gates before publication and relay rollout.

### Original v0.8.12 planning scope — completed above

Implementation started on `release/v0.8.12` after fetching/pulling main.
The shared [release checklist](plans/V0_8_12.md) records owners, acceptance and
evidence. Root owns integration and publication; ThinkPad has no active writer.

The October 9 post-release Herdr handoff and direct Linear reads confirm these
original requirements. This historical planning snapshot is superseded by the
published implementation and qualification above:

- [MEL-580](https://linear.app/mellanni/issue/MEL-580): the owner still sees the
  agent topic marked WAITING after v0.8.11. This issue was explicitly unresolved
  in v0.8.11; `topicOpen`/`deriveTopic` pending calculations were unchanged.
  Identify the exact contributing requests and their reply/status aliases.
  If stale, repair the source transition and recovery of existing affected state;
  if genuinely pending, expose the exact request with the appropriate existing
  action. Verify pre-upgrade history, multiple requests, restart and native/browser
  parity. An unrelated later answer must not resolve another request, and diagnosis
  must not silently rerun, cancel or close work.
- [MEL-551](https://linear.app/mellanni/issue/MEL-551): make an available update
  prominent, with its target version as the heading and the existing Update
  AgentNet action adjacent. Hide or subordinate old update-success text when a
  newer version exists. Reuse discovery and installation, preserve explicit-click
  installation and truthful current/checking/error/in-progress states, and verify
  Comic desktop and narrow layouts. No updater lifecycle redesign or polling.
- [MEL-525](https://linear.app/mellanni/issue/MEL-525): own-human task grants are
  required immediately after v0.8.11. Reuse the existing verified-person grants,
  native permission boundaries and revocations; no live grant changes during work.
- [MEL-502](https://linear.app/mellanni/issue/MEL-502): collective tags are prepared
  on `feat/mel-502-collective-tags` at
  `4fc197192c46bdb9d34ca4f76df099f2dda23104`; integrate and qualify the existing
  work rather than duplicating it.

MEL-526's Comic-only bundling, with optional Classic/Zoom moved to
`agentnet-skins`, remains future work rather than a new gate for the published
release. Shared topic names across all participants remain a recommendation,
not approved scope.

### Published v0.8.10

[Download v0.8.10](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10).
Published at **21:54:58 UTC** from
`7afac5bda699a33f60db38dfb188b1a6d83ef66d`. Existing relay upgraded at
**21:56:11 UTC** after a verified stopped-state backup. Its running binary and
normal HTTPS from relay and laptop report v0.8.10 with the original realm and
volume. Zero restarts/OOM events; the neighboring service is unchanged. No
installed desktop client was changed. GitHub's latest release resolves to
v0.8.10; the relay's older recommendation remains unchanged because the available
identity lacks admin rights. The explicit release check does not need those rights.

The owner authorized continuing after v0.8.9 rollout. Fresh direct Linear triage
covered 107 project issues, including 79 open records. Two bounded fixes are
implemented: MEL-551/MEL-493, an explicit published-release check in About using
the existing resolver and authenticated native controls; and MEL-542, the
remaining phone dismissal footer that offers stale Bring back after an exact
agent rejoins. All three skins use the existing authenticated computer controls
and fixed HTTPS release resolver. Check is explicit and read-only, including
unsupported installations; its loading, available/current/ahead and error states
cannot leak across a closed view or workspace switch. The phone action reuses
exact agent identity and fresh group state; delayed results cannot open an
invitation in another conversation. No new dependency, background polling,
installed-client changes or permission changes.

Focused API/bridge race passed 2.721 s, including authentication, timeout/cancel,
version comparison, failure and no-mutation checks. Linux native shell tests
passed all 18 tests. Final integrated Chromium checks passed 27.149 s: every
skin at 1280/390, 11 explicit checks each and no POSTs or background polling,
older hosts/browser fences, plus 12 phone rejoin/navigation cases. All three
locked skin builds and Comic type checking pass; review found no remaining
blocker. Local affected-package race passed: command 241.096 s, UI 533.332 s,
Classic/Zoom 3.275/3.325 s, static browser code 9.742 s.

[Affected native qualification](https://github.com/misunders2d/agentnet/actions/runs/37847585973)
passed all three Linux/macOS/Windows jobs: full `cmd/agentnet` and
`internal/ui/...` tests without a name filter, plus repository-wide vet.
Its revision `d1173e90` differs from the released source only in HANDOFF text.
Client, protocol, Hub, browser engine/storage, dependencies, scripts and workflows
are byte-unchanged from v0.8.9; retain their recorded qualification rather than
rerunning the costly client shards. This is composed evidence, not a new full
suite claim.

All five [release packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37848549685)
passed. All 12 downloaded asset sizes/digests and 11 SHA256SUMS entries match;
the Linux CLI and bundled CLI report the exact clean released revision. The
final AppImage passed actual replacement, Tauri/sidecar restart, command parity
and durable completion. A second identical tag/SHA packaging run was cancelled
before completion to avoid redundant builds and draft-release races.

Installers remain unsigned. Physical phone history/rejoin, notifications,
clipboard and interactive Windows/macOS installation are not established by
these checks. MEL-558 remains open for actual Amazon_team catch-up after the
v0.8.9 recovery; no further history protocol or replay was added in v0.8.10.

MEL-574 automatic Antigravity questions remains blocked on proof of its native
no-edit policy; MEL-575 model visibility needs compatible authoritative private
reporting. MEL-569 unified direct-agent history and MEL-579 Drive embedding
search remain explicitly future scope. Do not duplicate shipped fixes merely
because their physical-device verification remains open. ThinkPad is offline;
coordinate through Herdr only if it returns, never AgentNet or Orca.

## Previous release — v0.8.9, October 8, 2026 (UTC)

[Download v0.8.9](https://github.com/misunders2d/agentnet/releases/tag/v0.8.9). Published at **21:03:42 UTC**
from `bd793b64e07e303458604f3dec01067884dbb3d6`. Existing relay upgraded at **21:06:24 UTC**.
No installed desktop client was changed.

- Recover retained linked group-history copies that older clients rejected after
  a clean assistant dismissal. Browser and native recovery are restricted to
  authenticated current own-human devices and reuse normal admission. Historical
  requests never run; signatures, exact keys, grants and cursors stay intact.
- Exact cross-device causal callbacks can return to a busy local ancestor while
  retaining ordering, cancellation, authority and update fences.
- Linux AppImages install a verified stable per-user copy on first user-initiated
  opening. Launchers and updates use it; custom launchers and conflicts remain
  protected. Removing the download no longer removes that installed copy.
- Long raw links have compact labels in all skins, with exact targets, code,
  descriptive labels and native copy/inspection retained. Setup removes only an
  already-selected sole-agent choice. Needs-human guidance explains native
  permission/environment repair without changing permissions or claiming to fix
  the unverified Hyprland IPC cause.

History regression evidence includes the actual v0.8.6 browser rejecting a copy,
v0.8.8 leaving it missing, and recovery in the same store. Signed vectors passed
610 checks each in Node and Chromium IndexedDB; old-version upgrade passed 611.
Native same-store upgrade race passed 49.552 s; paged recovery, current authority,
restart and rollback passed 4.995 s. Natural callback regressions fail before
and pass after; adjacent race checks retain exact authority, ended-ancestor,
update/cancel and stale-consent coverage. Every skin passed desktop/phone-width
long-link and setup rendering. These are synthetic checks: the actual phone's
Amazon_team catch-up remains unverified and MEL-558 stays open. Source devices
must be updated and online to supply history missing from the relay/phone.

Qualification is composed evidence, **not an all-green full source run**.
[Full source CI](https://github.com/misunders2d/agentnet/actions/runs/37839979797)
at `74018a7c` passed 9/14 required jobs: all desktop builds, the container journey
and five client race shards. The five failed jobs contained two test-fixture
defects only: Classic/Zoom used the wrong source path for the new shared link
module, and a review test mistook an interim empty snapshot for final settlement.
All other packages in the failed jobs passed. Neither correction changes
production code. The latter reproduced 8/20 race failures before and passed
20/20 after (37.190 s); standalone skin races passed 2.327/2.370 s and real
Chromium storage passed 1.988 s.

[Focused native qualification](https://github.com/misunders2d/agentnet/actions/runs/37842386390)
passed all three jobs on Linux/macOS/Windows; the existing shell-stub review test
skips Windows. Its revision `a945711b` differs from the final release source only
in HANDOFF documentation. All five [release packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37842995917)
passed. All 12 downloaded asset sizes/digests and all 11 SHA256SUMS entries match;
downloaded Linux CLI and bundled CLI report the exact clean release revision.
The final AppImage passed actual replacement, Tauri/sidecar restart and durable
completion, including the stable-install path.

Relay rollout used a verified stopped-state backup, retained the original volume
and realm, and reports v0.8.9 through normal HTTPS from relay and laptop. Zero
restarts/OOM events; the neighboring service retains its previous start time.
The server's recommendation still names v0.8.5: the available identity is a member,
so the signed admin update was refused. No rights or database controls were
bypassed. Update independently checks GitHub; v0.8.10 addresses discovery before
installation. Physical notifications/clipboard and interactive Windows/macOS
installation remain unqualified. Installers are unsigned.

Release efficiency: keep passing unchanged coverage after a fixture correction.
Do not tag a `[skip ci]` documentation commit: it also suppresses tag packaging.
The initial unpublished v0.8.9 tag produced no run/assets and was corrected under
an exact lease to its identically qualified code parent before packaging.
For relay staging, download the published binary directly on the server and
verify the already-known checksum; the laptop SCP uplink was very slow.

## Previous release — v0.8.8, October 8, 2026

[Download v0.8.8](https://github.com/misunders2d/agentnet/releases/tag/v0.8.8).
Published October 8 at 19:43:30 UTC from immutable release source `8336346d546a11fddba74f06964a3fff6c95ee1c`.

- Updates verify the registered app file before shutdown and bind replacement to
  its checksum. A missing or changed file leaves the app open; later loss gives
  truthful recovery guidance. If an older registered AppImage is already absent,
  reinstall the official app once. This fix does not permanently relocate it.
- Reply on a needs-human request opens an explicit answer dialog, bound to the
  exact author key, request, host and attempt. Local owner, verified own human
  devices and existing report-bound operators use the existing decision paths.
  Stable answer IDs make retries inert. The same original task/question starts
  fresh context with saved clarification; ordinary quotes never restart work.
  This is not model-effect checkpointing or a permission change: verify prior
  completed effects, and native sandbox/IPC failures may still need recovery.
  Explicit `cont1` requires an updated host; changed keys fail closed.
- One newly tagged multi-agent send shows repeated human text once while every
  target keeps its own request/LID, files, status, reply, Stop and retry. Signed
  exact-author grouping survives linked history/reload; contradictory metadata
  and older untagged messages remain ungrouped. Explicit `sg1` is optional per
  copy: older peers receive otherwise compatible requests without it. Already
  sealed grouped copies wait on capability rollback. Each target's files remain
  visible. Distinct local executors run concurrently; the same executor remains
  ordered, with existing exact-parent, cancellation and update-idle fences.
- Codex group questions can use the official app-server dynamic room tool to ask
  an exact current group agent and receive its correlated reply. Read-only
  sandbox, never-approve policy, own inherited configuration and authority remain.
  Unsupported app-server capability gives needs-human, not widened permissions.
- Stale No thanks persists offline without membership changes. Existing declined
  consent queues only to the original unchanged pinned inviter key; missing keys
  mean honest local dismissal. Joining still requires fresh verified consent.
  Bring back disappears when the exact agent is already active or awaiting rejoin.
- Review notifications route to the installed app's exact request/workspace or
  grouped review list; without the app they reuse the coding-agent opener.
  Opening does not accept or retry the request. Approved suggested tasks use compact cards with linked
  proposals and the exact approved text retained in a disclosure in all skins.

Qualification is composed evidence, **not an all-14-green source CI claim**.
Production source is unchanged since `486d977`; final qualification commit
`8336346d546a11fddba74f06964a3fff6c95ee1c` changes three test fixtures and
adds focused inputs to the existing workflow. Full source run
[37827641115](https://github.com/misunders2d/agentnet/actions/runs/37827641115)
at `990f49faf3ae77c528c3cec90693d678dec9dbb7` completed 12/14 required jobs.
Its two failures were test-fixture issues: mutating a running daemon's HTTP
transport, and Windows owner-ACL/handshake/pipe/launcher assumptions. The
completed unrelated coverage remains valid; affected checks were corrected.

Focused native full cmd/agentnet across Linux/macOS/Windows:
[37832387974](https://github.com/misunders2d/agentnet/actions/runs/37832387974)
passed all three native jobs. Focused selected-client native coverage:
[37832385095](https://github.com/misunders2d/agentnet/actions/runs/37832385095)
passed all three native jobs. Vet passed. Focused updater race passed 1.018 s;
review-click race five runs passed 17.253 s; picture race ten runs passed
24.919 s. Earlier focused continuation/grouping/history/decline tests,
all three locked builds and 1280/390-width embedded rendered checks passed.
Installed Codex 0.161.0 isolated room smoke passed once, 9.465 s, using a
synthetic exact signed target/reply and inherited model/configuration.

Packaging [37832858055](https://github.com/misunders2d/agentnet/actions/runs/37832858055): all five jobs passed; downloaded asset size/digest checks
all 12 GitHub sizes/digests and all 11 SHA256SUMS entries matched; exact clean downloaded CLI/AppImage revision
both report the exact clean release revision; Linux replacement/restart passed in release CI.
Relay upgraded October 8 at 19:47:55 UTC after a verified stopped-state backup. Its running binary reports the exact clean release revision, public HTTPS reports v0.8.8 with the original realm, and the original volume is mounted. Zero restarts/OOM events; the neighboring service retains its prior start time. Retain original data,
configuration and realm; no installed desktop client or live trust/history state
was changed by release qualification. Installers remain unsigned.

The relay recommendation still names v0.8.5: the available enrolled identity
has member rights and the signed admin update was refused with 403. No admin
rights were added or database controls bypassed. The Update button checks
GitHub independently and can install v0.8.8. The rollout script’s Python HTTPS
probe received 403; normal HTTPS requests from both relay and laptop verified
v0.8.8 and the original realm. This was a probe failure, not an unverified
service transition.

Physical notification clicks, actual phone catch-up and interactive Windows/macOS
installation remain unqualified. Cyclic cross-device callbacks to a busy ancestor
executor remain unqualified; ordinary overlap does not establish cycle recovery.
MEL-558 stays open for physical regular person/group catch-up; direct-agent
history MEL-569 remains separate deferred scope. Native clipboard/photo behavior
requires actual platform interaction before closure.

## Historical v0.8.9 triage authorization

Owner asks to review all issues/bugs added October 8 plus important older bugs,
report chosen scope, implement and release. ThinkPad is offline overnight;
continue independently, with relay-only deployment and existing security gates.
Triage exact MEL-497 native permission/environment failure, permanent AppImage
installation and busy-ancestor cycles. MEL-573 is a bounded raw-link display fix;
MEL-566 requires evidence-led stale-Join recovery, never weakened consent.
MEL-545/555/552 begin with verification of shipped paths. MEL-558 persisted-store
history recovery and MEL-546 backlog/pre-custody evidence need targeted repros.
MEL-530 mixed-version and MEL-563 latency must distinguish existing fixes from
physical/runtime evidence. MEL-579 Drive embedding search is explicitly future
work; indexing is not authorized by ticket creation. Avoid broad UI redesign. Today’s MEL-575 model visibility stays read-only
and owner-scoped; MEL-574 Antigravity needs native permission-contract proof
before advertised support. First-open permanent AppImage installation is
authorized product behavior; development must not migrate live clients.

## Previous release — v0.8.7, October 8, 2026

[Download v0.8.7](https://github.com/misunders2d/agentnet/releases/tag/v0.8.7).
The released source addresses MEL-558, MEL-539 and the owner's
new next-release requirement on MEL-540. Root integrates and owns release;
ThinkPad Codex supplied read-only production evidence through Herdr. Coordinate
with that peer through Herdr only, never AgentNet. No installed client, identity,
trust grant, history cursor or quarantine content was reset or changed.

- **History (MEL-558):** ThinkPad's actual v0.8.6 sidecar had existing running
  jobs for the phone and other linked devices at the empty cursor. Its recurring
  error was `human: author or reader participation ended or is held`. A synthetic
  original-member/own-assistant dismissal reproduced that exact failure. The
  existing history path now retains accepted inert requests, replies, status and
  selected excerpts after a clean assistant dismissal. Forwarded dismissal
  evidence verifies the original signed author. Held/conflicting proof, wrong
  keys, visitor history access, changed admission epochs and live sending still
  fail closed. No new relay protocol or history reset. The relay stores encrypted
  copies; the source device must authorize and prepare old history with its keys.
- **Held back (MEL-539):** appended SQLite columns and equivalent browser fields
  store only allowlisted diagnostic codes, never arbitrary errors or content.
  Existing records lacking detail say so explicitly. Archive notice hides only
  a local `invalid` notice while retaining its envelope, reason, receipt and
  deduplication. Other reasons cannot be archived through this action. There is
  no acceptance, trust, replay or execution side effect. The five old ThinkPad
  invalid records do not contain enough persisted detail to assert their cause.
- **Multiple selected agents (MEL-540):** intact exact mention spans identify
  distinct participation IDs. Existing single-target request paths create one
  stable request ID per agent and retain each independent retry/result, including
  two same-named agents on one host and separately staged files. Failed targets
  do not resend successful ones. Zoom's visible write dialog reuses its existing
  picker. Received mention text never initiates fan-out.

Focused native history and adjacent checks passed, including the exact before-
failed dismissal case, asking-assistant replies, signed forwarded dismissal,
restart/reorder, excerpts, file bytes, deduplication and no extra execution.
Browser parity, warm recovery, human lifecycle and history files passed. The
actual Chromium IndexedDB history journey and final native/archive/package
checks passed together (client 4.135 s, UI 11.677 s, static 0.956 s).
All three locked skin builds and Comic TypeScript checking passed. The rendered
multi-agent and Held back flows passed in all three skins at 1280/390 widths;
source candidate/mention regressions passed. Local Go vet and the full required
race qualification passed: client A–F 401.343 s, G 600.925 s, H–P 490.582 s,
Q–Z 414.183 s, and non-client packages up to 445.811 s.
All 14 final [source CI jobs](https://github.com/misunders2d/agentnet/actions/runs/37795624300) passed. The first Linux fixture
ordering failure passed on isolated retry. A causal fixture reproduced a
sender capturing its audience before learning acceptance; the post-release
test-only follow-up waits for that sender before sending. Its source passed
three focused runs and is identical to the applied fix. No runtime change or
longer timeout was needed; this was not a clean first CI attempt. Immutable release source:
`129a38ec5de8fc8f988b9972a0a143e6dc27dbb8`.
[Release packaging](https://github.com/misunders2d/agentnet/actions/runs/37800186287):
all five jobs passed; all 12 downloaded asset sizes/digests and all 11 SHA256SUMS entries matched. The Linux CLI and AppImage-bundled CLI report the exact clean release revision. The final AppImage passed the release CI replacement/restart check.
Published/latest: 15:35 UTC.
Relay rollout: upgraded the existing relay at 15:42 UTC after a verified stopped-state backup; its running binary and public HTTPS report v0.8.7, with the original data volume, configuration and realm retained. Post-checks found zero restarts/OOM events and the neighboring service unchanged. Installed desktop clients were not changed.
Update both laptops with **Settings → About → Update AgentNet** or
`agentnet update v0.8.7`, then reload the phone app. The existing history jobs
resume through the usual encrypted path; do not reset or relink devices.
Physical phone catch-up is not yet verified and MEL-558 remains open for it.

**Separate later work:** MEL-435 notification click repair is on branch commit
`ae1cea5762e2087646a53267408e058ceb795f32` on
`fix/mel-435-notification-open`, peer-reported tested; it is not
shipped in v0.8.7 and its physical click qualification is not independently
verified here. Keep tokens out of notification commands and retain normal
app authentication.

## Previous release — v0.8.6, October 8, 2026

[Download v0.8.6](https://github.com/misunders2d/agentnet/releases/tag/v0.8.6).
Published as the latest release at 13:03 UTC from
`0a0e7aeadef15b315feb694706b6e0cafb6f8ecc`. All 14 required
[source CI jobs](https://github.com/misunders2d/agentnet/actions/runs/37777205734)
and all five [release jobs](https://github.com/misunders2d/agentnet/actions/runs/37779934720)
passed. Source CI ran native tests and desktop builds on Linux, macOS and
Windows, the race shards and the container journey. The optional Windows
task probe was skipped. All 12 downloaded asset sizes/digests and all 11
`SHA256SUMS` entries matched. The downloaded Linux command and the AppImage's
bundled command report v0.8.6 and that exact clean source revision.

The final AppImage passed the release workflow's extract-and-run
replacement/restart smoke check. A separate isolated check of the downloaded
native shell dispatched the exact release link once, without rejected commands,
and kept the app page usable. That fixture enables synthetic popup clicks;
it does not certify a physical click or interactive Windows/macOS installation.
Physical linked phones and live model execution remain unverified for this
release. Installers are unsigned.

The existing Contabo relay was upgraded at 13:10 UTC after a verified
stopped-state backup. Its running binary reports the released clean revision;
public HTTPS reports v0.8.6 and the unchanged realm. The existing data volume
and configuration were retained. The container is running without restarts or
OOM events, and the unrelated service remained running. No installed desktop
or phone client was upgraded by this rollout.

Desktop clients can install v0.8.6 with **Settings → About → Update AgentNet**:
the button checks GitHub independently of the relay's recommendation. That
advisory notice still names v0.8.5; the available laptop and server identities
have member rights, and the signed recommendation update was refused with 403.
An existing enrolled admin device can update it using the command in
[the installation guide](revival/INSTALL.md#updating-and-downgrading). This
does not block the published release or the Update button. No new admin rights
were granted.

Source CI exposed two test-fixture issues before tagging: unrelated background
POSTs polluted a global receiver counter, and a raw SQLite test connection
lacked the neighboring fixtures' busy wait. Both were corrected using existing
patterns, with focused loaded/race checks; production code did not change after
the initial implementation commit. The final integrated source passed every
required CI job without raising test timeouts. Release tags remain immutable.

## v0.8.6 implementation and local qualification — October 8

The owner authorized the urgent/important bug fixes below after direct Linear
review. The implementation starts from `334bcbf`. No installed client identity,
permission grant or user session was changed. Final publication and deployment
evidence is recorded above.

- **Queued requests (MEL-546):** browser ciphertext uploads now share the existing
  30-second HTTP deadline. A signed local deletion stops remaining question/task
  copies, including private reply-routing wrappers. The final direct/relay
  handover rechecks the durable deletion. One appended SQLite migration records
  handover attempts and stopped sends; older rows stay unknown, never assumed
  unsent. Offline controls retain their recipient keys and required capabilities
  and recheck them before delivery. Late proven receipts, per-copy delivery and
  existing execution state remain authoritative.
- **Linked history (MEL-558):** a withdrawn original control recipient no longer
  stalls an entire native snapshot. Another exact signed original copy can be
  used for a current reader; missing/conflicting evidence still fails closed.
  Browser snapshots reuse the existing signed journal/context carriers and repair
  missing context for completed jobs without resetting message cursors. Empty
  groups, changed own-device keys and transaction-time roster changes are covered.
- **Approval details (MEL-539):** Classic/Zoom use the exact conversation/request
  explanation from the existing review projection when message-level detail is
  absent. Classic also selects the request's actual topic before focusing it.
- **Browser setup (MEL-534/MEL-533):** all three skins use the existing installer
  table and one-use device link. Merely opening setup creates no link; enrollment
  still requires the original device's approval. No new protocol or service.
- **Native release links (MEL-551):** the published Linux shell reproduced an
  opener-plugin ACL denial after its default JavaScript interceptor prevented
  navigation. The supported plugin option disables that interceptor, retaining
  the existing Rust origin/scheme checks and external opener, without granting
  a general webview opener permission. The rebuilt Linux shell passes the same
  fixture: exact URL dispatched once, no rejected command, app page retained.
  Its test-only WebKit setting allows the synthetic popup click; this is not
  evidence of a physical click or Windows/macOS behavior.

Focused evidence: the new native history regression fails on the original
source with `recipient admission withdrawn` and passes on the candidate, including
verified ciphertext, restart/deduplication and no execution. Browser history
engine/warm-recovery and real IndexedDB/history-file checks pass. Existing native
guest unread, exact browser read-sync and receipt convergence checks pass
(MEL-545/MEL-561). All three UI packages build, including Comic's TypeScript check.
Full approval and browser setup checks pass in all three skins at desktop and
phone widths. All 54 stopped/uncertain/delivered/working/answered rendering cases
pass; Zoom now reuses the existing signed execution line for device threads.
Notification mute, profile picture/crop/paste, keyboard viewport and all 17
native Rust tests pass.

`go vet ./...` and the repository-required race checks pass with
`-race -count=1 -timeout 600s`. All non-client packages passed, including the
complete UI package rerun (377.664 seconds). The four client shards ran quietly
and sequentially:

| Client shard | Seconds | Passing cases, including subtests |
| --- | ---: | ---: |
| `^Test[A-F]` | 405.972 | 316 |
| `^TestG` | 532.685 | 168 |
| `^Test[H-P]` | 489.853 | 251 |
| `^Test[Q-Z]` | 412.507 | 350 |

The initial concurrent A–F run reached its fixed deadline; its quiet rerun
passed without raising the limit. Full verification also caught a browser
receipt-cursor regression: Hub restore replay must be allowed to lower its
cursor. The correction retains terminal delivery protection and passes the full
UI rerun. An existing receiver-retention test expected a deleted, never-sent
request to be released after later approval. Its updated assertion preserves the
immutable setup already received by the selected host, while proving that the
deleted original never leaves the sender; the focused test and full Q–Z rerun
pass. No production change was needed for that test correction.

The default race runs skip optional browser/harness journeys; affected browser
flows were exercised separately with Chromium as described above. Twelve client
tests remain skipped because they need an opted-in native/live harness, a frozen
synthetic binary, or an installed Pi package. Physical linked phones,
Windows/macOS interactions and live model execution are not certified by these
fixtures. Final source CI, release artifacts and the authorized existing-relay
rollout passed as recorded above. Future fixes should use cheap focused checks
first, then qualify one stable integrated candidate with the costly suites.

## Original laptop handoff — October 8, 2026

The v0.8.6 release and implementation sections above supersede the next-work
status here. Use those sections for shipped behavior and verification limits.
Older release sections and `DECISIONS.md` contain historical
snapshots, not unfinished release steps.

- **Checkout:** inspect the destination working tree, then fast-forward `main`
  from `misunders2d/agentnet` without discarding local changes. Read `AGENTS.md`
  before implementation. Git transfers source and these documents; this chat,
  private laptop journal, ignored fixtures and tool credentials do not transfer.
- **Release finished:** v0.8.5 is published and deployed to the relay. Its exact
  source, CI and qualification are recorded below. All previous workers and
  test fixtures are drained; do not resume old worker IDs or rebuild/redeploy
  v0.8.5 as an unfinished task. Released tags stay immutable.
- **Owner update confirmed afterward:** the owner reported the app upgraded;
  read-only checks confirmed the terminal and app-private commands both report
  v0.8.5 at the released revision, with identical hashes and complete update
  status. An earlier repeated request returned a busy-job 409; its exact job
  was not identified. It did not require another update after completion.
- **Next bug priorities:** use the existing
  [MEL-546](https://linear.app/mellanni/issue/MEL-546) for mobile agent requests
  stuck on Sending, including deleted queued requests, and
  [MEL-558](https://linear.app/mellanni/issue/MEL-558) for missing linked-device
  group history. Read their latest Linear updates before editing; search for
  existing issues before creating any. Linear access must be connected on the
  destination independently; ask the owner if needed.
- **Diagnosis boundary:** the reported phone question had no relay custody
  record, while a separate deletion control arrived. The client queue or
  pre-custody send path remains unresolved; this is not proof of a slow remote
  agent. Existing history-copy work also remains incomplete on physical devices.
  Trace existing queue/copy transitions before adding a new sync mechanism. Do
  not reset/relink identities, replay requests or weaken admission checks to
  make the symptoms disappear. Message IDs and private evidence stay in Linear
  and the local journal, not this public repository.
- **Owner's workflow:** the coordinator supervises GPT-6.1-sol implementation
  helpers. Prefer existing implementations, minimal fixes and focused evidence;
  avoid overengineering. Use synthetic identities in previews. Keep native test
  windows on the laptop display without stealing focus, after verifying that
  machine's display layout; preserve the owner's open apps and desktop session.
- **Scope:** v0.8.5 fixed updating only. Mobile sending/history and the reported
  What's new link remain open. Projects are a v0.9 discussion; do not implement
  deferred projects/team tags or design polish as part of these bug diagnoses.
  The previous coordinator is paused; work resumes under the owner's new task.

## Historical release — v0.8.5, October 7, 2026

[Download v0.8.5](https://github.com/misunders2d/agentnet/releases/tag/v0.8.5).
Updater-only hotfix: retain host search paths for AppImage restart, detect
immediate child exits, resume verified app/CLI reconciliation after reopening,
and restore native controls/session loading after the URL token is removed.
The handoff keeps the Strict HttpOnly session cookie and uses a fixed,
hash-authorized same-origin navigation. No data/schema migration.

Published October 7, 2026 at 19:26 UTC from
`591690c55fb1c29b206c6774433af9f3c69739d6`. [Source CI](https://github.com/misunders2d/agentnet/actions/runs/37671163931)
and [installer builds](https://github.com/misunders2d/agentnet/actions/runs/37671250480) passed. All 12 asset digests and sizes,
and all 11 `SHA256SUMS` entries, matched. The downloaded Linux command reports
v0.8.5 and that exact clean revision. The relay was deployed at 19:27 UTC after
a verified stopped-state backup, retaining its data volume and realm. Its
public version and client recommendation both report v0.8.5.

The final downloaded AppImage passed an isolated native FUSE journey:
Comic Settings → About → Update, automatic restart into a private next-version
fixture, a usable About page without reload, and matching app/command copies.
The private next version is only a test artifact, not a published release.
Identity stayed unchanged, the session token left the address/history, and a
controlled link fragment survived. Separate genuine v0.8.3 UI and v0.8.4 CLI
journeys verified the one-time manual reopen and independent-daemon recovery.
Busy-daemon behavior has protocol regression coverage; an interactive busy
update and interactive Windows/macOS updates remain unverified. Installers are
unsigned. No existing desktop or phone installation was changed during testing.
Mobile sending/history issues remain open; this release is updater-only.

**Upgrading a v0.8.3/v0.8.4 AppImage:** its old updater still needs one
manual reopen after installing v0.8.5. Open AgentNet from its launcher if it
closes without returning. If v0.8.4's Update button is unavailable, run
`agentnet update v0.8.5` for the registered desktop installation, then reopen
once. Updates started by v0.8.5 include the restart fix.

Release work is complete; workers and fixtures are drained. Remaining phone
Sending/history reports are tracked on MEL-546/MEL-558. Future product/design
work stays deferred; resume only when the owner asks.

## Historical release — v0.8.4, October 7, 2026

[Download v0.8.4](https://github.com/misunders2d/agentnet/releases/tag/v0.8.4).
Released source: `ac084454e767b5f1f8096533b64c0c57b2d822b3`.
Published October 7, 2026 at 15:54 UTC. The [source CI](https://github.com/misunders2d/agentnet/actions/runs/37642753786) and [release workflow](https://github.com/misunders2d/agentnet/actions/runs/37645700324)
passed on `ac084454e767b5f1f8096533b64c0c57b2d822b3`. All 12 asset digests and
sizes matched GitHub, and all 11 package entries matched `SHA256SUMS`. The
downloaded Linux command reports v0.8.4 and that exact clean source revision.
Both isolated Linux update journeys passed with the final downloaded AppImage.
The relay was upgraded after a verified stopped-state backup, preserving its
existing volume and realm. Its public version and client recommendation both
report v0.8.4. No existing desktop or phone installation was updated.

The implementation record below describes focused qualification; final release
CI/package/relay facts belong above. Tests and cross-compilation do not certify
interactive Windows/macOS installation or a physical phone journey. The local
Pi stall repair was a separate custom extension cleanup, not a shipped AgentNet
performance change. No reset, re-enrollment or permission migration is required.

## v0.8.4 implementation and focused qualification — October 7

The owner expanded the v0.8.4 patch to the core chat, agent setup,
notification and desktop usability fixes below. GPT-6.1-sol helpers own source
and focused qualification; root owns review, integration and release gates.
Published v0.8.3 remains immutable. No installed AgentNet client identity, history or
configuration was changed. Temporary native responder validation used existing
OMP authentication and settings, without opening or resuming a user session.

- **One person chat and flat Topics (MEL-494):** Comic, Classic and Zoom show
  Main plus All topics. Other signed conversation roots and all native topics
  are siblings, including empty/deleted-only flows. Messages, drafts, send and
  action routes keep their exact source root/topic and audience; guest and agent
  access is never merged. Main is a validated local preference for one existing
  signed member root, retained when older history arrives; different devices
  may initially choose differently when available history differs. No new Hub
  authority or rewritten history. Production-source Chromium fixtures cover all
  three skins at desktop/phone widths, reload, late metadata, exact routing,
  drafts and native bulk actions. Final embedded-bundle integration checks
  passed; browser fixtures do not prove a physical phone journey.
- **Chat notifications (MEL-564/MEL-560):** ordinary admitted chats default
  unmuted under the device's global opt-in. A person-chat mute derives from
  existing muted signed member-DM roots, covers Main and Topics, and also covers
  later synced roots for that verified person. Unmute clears those roots while
  preserving other chats. Cleared histories retain their mute anchors; a full
  reset naturally removes preferences. Groups and guest-only roots retain their
  own scopes. Existing configured quiet DMs migrate conservatively because old
  data cannot distinguish explicit deny provenance; missing old DM sender grants
  never blanket-mute groups. Unknown requests/invites gain no ordinary-chat
  notification eligibility. Native and browser regressions cover group members
  without a DM, signed attention hints, migration, future-root inheritance and
  scoped unmute. Final rendered settings checks and physical Windows/macOS/phone
  notification display remain unverified here.
- **Unified agent setup (MEL-506/MEL-550/MEL-504):** one setup flow uses the
  existing harness catalog and named-agent configuration for installed supported
  Claude, Codex, Pi and OMP. Native hook registration is Details, not the gate
  for creating a runnable named agent; Windows hook unavailability does not
  remove a catalog-found agent. Executable/folder readiness does not certify
  provider sign-in. Codex/Claude readiness checks exact AgentNet hook identities
  and coverage rather than whole-file JSON formatting; malformed, missing,
  duplicate, matcher-scoped or stale executable/home handlers remain rejected.
  A reproduced formatting-only false NEEDS SETUP fails before and passes after
  the fix. Existing byte snapshots still protect review/apply changes. Unified
  setup source/browser fixtures and TypeScript passed; final bundles are separate.
- **OMP responder:** the existing worker/catalog now supports fresh OMP
  question and accepted-task jobs with normal own settings, skills, extensions
  and MCP tools. Tasks add no permission bypass. Questions use native
  `always-ask` plus a temporary, deep-merged overlay denying built-in editing
  and shell tools; other explicit grants and denies remain authoritative. A
  setup-only, bounded native capability check rejects unsupported launchers
  before storing an explicit default/named-agent choice; discovery, refresh,
  claim and each request add no probe. The selected launcher's own behavior
  still applies; AgentNet has no install fallback. Installed 18.4.8/18.7.0 help
  and an actual 18.7.0 isolated effective-config check qualify the contract.
  Real 18.7.0 fresh jobs returned a question response and the exact benign
  accepted-task ACK with own existing auth; the synthetic working folder stayed
  unchanged. Stronger version
  lookup qualification exposed a missing TypeBox API; the lookup schema was
  repaired to native Zod and its factory registration verified through
  metadata-only RPC, with no model prompt. The executable API-shaped regression
  runs the exact fixed lookup and rejects arbitrary operations. A post-fix live
  model lookup was not rerun; see [M4](revival/M4.md) for exact flags and limits.
  Existing selected-native-session adapters are unchanged. Antigravity remains
  an optional future compatibility candidate, not advertised responder support.
- **Reminders (MEL-528):** separate Date and Time fields in Comic preserve
  incomplete native edits. Real Chromium tests type hours/minutes, dismiss and
  reopen, create and move reminders, and check exact submitted timestamps in
  New York/Kyiv across DST at desktop and phone widths. A phone-sized native
  fixture does not add reminder support to browser-only devices.
- **Browser sends/reconnect (MEL-546):** HTTP headers and body share a
  30-second deadline; caller and engine-close cancellation remain effective.
  A reproduced stalled onConnect no longer blocks reconnect indefinitely, and
  the original durable queued request resumes without creating a new ID.
  The physical phone's observed Sending stall is not yet attributed or proven
  recovered. Read-only relay checks found no crash/restart; phone was offline.
- **Existing history copying (MEL-558):** outgoing group edits, deletions and
  reactions retain their signed timestamp instead of later local persistence
  time. Regression reproduces the exact admission failure before the fix and
  passes afterward, including restart/resume and tamper rejection. Signature
  checks and history cursors stay authoritative. Deployed catch-up after an
  upgrade remains unverified.
- **Mixed-reader group sends (MEL-530):** a following participation made
  ordinary group turns preflight every reader's capabilities, so one older
  member blocked everyone, even when a person mention named somebody else.
  Compatibility now holds each original encrypted copy in the existing outbox;
  current readers receive while the incompatible copy waits for its exact
  device's signed capability recovery. No mention, audience, consent or signed
  admission semantics are removed. Native regression reproduced the refusal
  before the repair and passes with missing group-guest/group-reader support,
  compatible delivery once, daemon restart, unchanged envelope/context and
  updated delivery once. Browser source lifecycle tests cover the same cases
  and retain ended-scope, changed-key and upload handoff fences. Existing
  detail fields preserve the exact device and missing feature. Production
  rendered Details passed all six Comic/Classic/Zoom desktop/phone cases, with
  escaped, readable device/feature text; the owner's physical mixed-version
  journey remains unverified.
- **Delivery and agent work (MEL-531/MEL-563/MEL-542/MEL-566):** existing
  receipt push projection and signed sent-time paths passed original-path tests,
  including all-audience convergence without refresh or polling. Existing group
  participation lets another authorized member ask the present agent without
  reinviting it; the regression preserves selected context and task acceptance.
  Worker diagnostics now record stored/approval age, process setup/start and
  first output, with no content/tool arguments logged. Missing-executable tests
  retain terminal failure and prove no automatic rerun. No new model timeout,
  telemetry service or background job system. Native Windows CI passed the
  final release source.
  Startup diagnostics do not establish faster answers. The separate local Pi
  incident was traced to a custom Google extension leaving its OAuth listener
  alive after a completed answer. A local extension cleanup was verified with
  a fresh harmless answer and normal process exit in 7.471 seconds; it is not
  a shipped AgentNet performance fix or a worker timeout workaround.
- **Own-agent retry and held-record labels:** outgoing own-agent requests now
  expose their authoritative local execution state through the existing Exec
  view, independently of custody/delivery. Cancelled, failed and interrupted
  jobs offer explicit retry without claiming a new permission decision;
  interrupted requests retain their resolve action. Stand-in harness tests
  prove own questions and tasks start automatically without grants, run once,
  and actual Stop leaves an explicit retry without automatically rerunning.
  Legacy `invalid` quarantine reasons cover both verification failures and
  rejected operations, so their labels make no blanket failed-signature or
  verified-identity claim. Quarantine state, hidden contents and admission
  checks remain unchanged; no old records were rewritten or replayed.
  Thirty embedded production-skin cases passed for stopped, failed,
  interrupted, awaiting and running states across all three skins at desktop
  and phone widths, including nonempty interrupted details, retained Resolve,
  neutral held labels and absence of runtime errors. Settled captures were
  checked for readability; all three generated packages reproduce exactly.
- **Zoom send and incomplete participation display:** Zoom's send dialog
  referenced undefined `d.id`; it now checks its captured `t.id` against the
  current route. Send and stale-route regressions passed at both widths,
  preserving typed drafts when the selected route changes. All-skin source
  contracts passed. For reopened MEL-566, `PartPending` records without a valid
  counted invitation no longer inflate Invited or invent Someone's agent/
  Waiting for OK cards. Comic shows neutral collapsed incomplete context,
  without fabricated identity, permissions or actions; real invited Cancel
  stays unchanged. Backend/model race passed (1.085 s); actual embedded
  desktop/phone rendering and the real-invitation Cancel path passed. UI vet
  and root's full Go vet passed.
- **Attachments and skin import (MEL-568/MEL-570):** plain-text previews are
  escaped and bounded to 256 KiB; HTML/SVG are excluded from preview. Actual
  isolated Linux Tauri downloads saved exact text/HTML/SVG names and bytes;
  foreign/unmarked blob navigation was refused and the app page did not execute
  or navigate to downloaded content. The official folder picker returned an
  actual selected directory, which the existing package validation/import path
  accepted. Desktop/phone source-browser preview tests passed. Physical native
  clipboard behavior remains unverified; existing clipboard guards/unit tests
  and actionable OKs regressions are retained (MEL-539/MEL-519/MEL-555).
- **Updater (MEL-551), focused qualification:** stable replacement cwd and immediate
  child-failure regressions pass. Actual isolated Linux Tauri plus the real Go
  backend exercised the independently managed daemon control bridge, old-style
  fetch, and refusal of unrelated actions/CLI operations. A real whole-package
  handoff from the published v0.8.3 helper/AppImage/CLI passed in isolation,
  including inherited AppImage paths and a removed old working directory.
  Independently managed v0.8.3 daemon cutover also passed: a second accepted
  job completed before replacement; the same daemon PID, page and token survived,
  both jobs answered exactly once, and canonical/private/PATH CLI bytes matched
  the candidate package. Preparation uses the existing 30-second bound: a busy
  old daemon can refuse preparation without changing installed bytes or killing
  its job; retry after that work completes. The published v0.8.3 UI button/network
  download journey and the owner's original v0.8.2 cause remain unproven. The final
  downloaded packages and native-platform CI passed the release gates above.

- **Agent addressing and nested questions:** prompts name configured agents
  alongside exact participation IDs. The existing scheduler can claim an exact
  selected same-host child caused by the running parent in the same group,
  allowing that child to execute while the parent waits; this does not grant
  unrelated queued work new authority. Pi and OMP question extensions expose
  the existing room ask/wait path for questions. Claude and Codex retain their
  own question restrictions; no blanket question-delegation support is claimed.
  All skins show the exact selected recipient and distinguish an existing
  approval proposal using its authoritative metadata. Final embedded addressing,
  proposal and terminal-not-run rendering passed six cases
  across all three skins at desktop/phone widths; body and exact PID were retained,
  with no sends. Native question-tool/name races passed in 3.654 seconds.
  Canonical locked builds, bundle integrity and all three digest checks passed;
  integrated CI passed on the released source.
- **Own sent-message reminders:** the existing personal-device reminder path
  accepts locally stored inbox/outbox message IDs, preserving its time bounds,
  deletion checks, scheduler and schema. Outgoing copies retain their exact
  local IDs; the existing reply-ending rule matches logical aliases only within
  the same conversation. The new own-sent regression failed before the fix.
  Focused reminder races passed in 17.339 seconds; final actual direct/DM and
  synthetic fanout-copy/reopen checks passed in 4.238 seconds. Menu guards retain
  native reminder capability checks; browser-only support is not added. Final
  embedded own/incoming create, reload retention and Move/reopen rendering
  passed six cases across all three skins at desktop/phone widths. Rendering
  used fictional API persistence; real native persistence is qualified by the
  separate client tests above. Group reminder notifications open the chat;
  exact-message focus for that notification is not claimed.

Focused core races and actual source-browser fixtures pass within those scopes.
Root's `go vet ./...` passed on the integration candidate.
Two macOS CI failures were repaired as fixture synchronization assumptions:
count only the exact new group publication rather than independent queued
history deliveries, and wait for a signed DM root to arrive before inspecting
its timeline. Security and message assertions were retained. Final combined
source CI, embedded skins, native packages and checksums were pending at this
focused-qualification stage; final published evidence is recorded above.
These checks do not establish every installed device’s recovery. Generated local
screenshots and verification logs stay private and must not be published.

The owner deferred unified direct-agent history to **MEL-569**, the Appearance
skin-repository link to existing **MEL-526**, and Projects to existing
**MEL-527** discussion. **[MEL-571](https://linear.app/mellanni/issue/MEL-571/future-assign-any-selected-message-to-my-agent)**
records a future Assign to my agent action for any existing accessible message,
reusing task/context facilities instead of retyping another person's request.
It is discussion/backlog only, outside this patch. Preview versus immediate
assignment, chosen agent/device/default, context/attachments and result audience
remain owner decisions; no selected-message assignment behavior was implemented.
Reusable team/person tags (**MEL-502**) and collapsed participant presentation
(**MEL-498**) remain deferred; neither is part of this release.

## Historical release — October 7 v0.8.3

[v0.8.3](https://github.com/misunders2d/agentnet/releases/tag/v0.8.3) was published at 08:39 UTC from
`5aec119649807befe711135c97805a89f1aa3b90`. The final combined candidate includes
the global updater and the owner's required direct-device OKs correction.
The earlier updater-only draft was replaced before publication; it was never
published. This tag is now a published release and must not be moved.

### Whole-app updates

Both About and the v0.8.3 `agentnet update` command use the registered desktop
app updater. Verified official terminal copies, including an earlier PATH
entry, and the private connected-tool command update together. A closed app
opens for an update. An already-current app repairs older official commands.
Pending/partial/failed results remain explicit until the restarted app and all
required executable bytes match. Check/status remain read-only. Custom files,
symlinks, active jobs and independently managed daemons keep their protections.
From v0.8.1/v0.8.2, use About once: an older standalone command still contains
its old updater until replaced. CLI-only and relay installations stay separate.

### Direct-device OKs

v0.8.2 corrected conversation `needs_you` items but missed the separate device
`review` list. Native overview now projects trusted local running/stopping
state into the existing `agent_running` reason. All bundled skins omit those
items from approval counts while keeping a Working entry, exact navigation and
Stop. No new permissions, DTO fields or message-text state inference.

### Qualification

[Source CI 37592322850](https://github.com/misunders2d/agentnet/actions/runs/37592322850)
and [release CI 37592386488](https://github.com/misunders2d/agentnet/actions/runs/37592386488)
passed: native Linux/Windows/macOS, race shards, browser storage, container and
desktop builds. All 12 release asset digests and 11 manifest checksums matched.
The downloaded Linux CLI reports v0.8.3 and the exact clean release revision.

Linux package qualification replaces a disposable published v0.8.2 AppImage
with the real candidate package, restarts its Tauri shell/backend, and verifies
matching private/canonical/PATH commands plus a durable complete result.
Separate temporary enrolled-backend journeys verify About and CLI entry points
and same-version repair using actual published-checksum-verified old commands.
These checks found and fixed official-CLI recognition, private-command ordering
and inherited PATH adoption defects before publication.

Native OKs races pass (3.231s), including real signed ingress over an isolated
relay and exact public Stop; worker lifecycle states are staged locally.
Real-browser Comic/Classic/Zoom fixtures pass at desktop and phone widths;
the wrapper passes under race (9.460s). Waiting decisions still count, running
requests do not, and their exact request and Stop stay reachable. Root visually
checked private captures. These tests do not claim a live model execution.

Interactive Windows/macOS upgrades and a graphical About-button click remain
unverified. Installers remain unsigned. Qualification changed no existing
installation. After publication, the owner separately authorized the relay
upgrade recorded below.

### Relay deployment — October 7, after publication

The owner explicitly requested the published release on the Contabo relay.
The official v0.8.3 binary matched its release checksum and was verified in a
new image before cutover. Hub was stopped for a consistent private backup;
the archive was checked before switching only the Compose image. Previous
v0.8.2 image and configuration remain available for rollback. The existing
volume, realm, TLS/sign-in configuration and networking were preserved.
Running container and public HTTPS version endpoint report v0.8.3 at the
release revision, with zero restarts and no OOM. Backup/rollback paths are in
the private project handoff. No desktop or phone was updated.

### Follow-up ownership

MEL-551 and MEL-557 track the shipped global updater. MEL-539 records the
shipped direct-device fix; its older broader card-presentation requirements
remain open. Next authorized work is Classic/Zoom design review and polish
with other agents (MEL-567), including the earlier Zoom connections graph.
Projects/Topics stays the existing MEL-527 design discussion, without a new
entity or protocol implementation. Missing direct-agent @ picker is recorded
in existing MEL-544; agent-file download/preview is MEL-568. Preserve those
scopes and check for an existing issue before creating one.

## Previous release — October 6 v0.8.2

### Relay deployment — October 7

The owner explicitly requested deployment. The existing Contabo relay was
upgraded from v0.8.1 to the official v0.8.2 Linux binary at release revision
`ac9dab82ddcc31bb510b0644a9edf1a561c015cf`. The binary checksum matched the
published manifest; its version was checked inside the new image before
cutover. A stopped-state Hub backup and the old image were retained. Compose
keeps the original data volume, configuration and realm. After restart, the
public HTTPS version endpoint and the running container both report v0.8.2;
the container is running with zero restarts and no OOM. Backup/rollback paths
are in the private project handoff. No desktop or phone was updated by this
deployment.

### Post-v0.8.2 CLI adoption finding — corrected in v0.8.3

The owner updated the Linux AppImage to v0.8.2 through the app. The bundled
CLI at `~/.config/agentnet/bin/agentnet` became v0.8.2/schema 50, while the
terminal copy at `~/.local/bin/agentnet` remained v0.8.1/schema 48. Its hash
matches the official v0.8.1 release, but no `app-command.json` ownership record
exists. `appOwnsCommand` only read a retained `-ldflags` version; the actual
release stores its tag in Go's `Main.Version` without that flag. This wrongly
classifies the official CLI as custom. The public installer regression fails
before the fix (1.780s) and all focused AppCommand race tests pass after it
(1.182s), including rejection of a modified release with the same tag.

The correction uses module-version metadata as a checksum-manifest selector;
only a matching published hash authorizes replacement. It does not execute an
unknown CLI or weaken custom/symlink protection. It is not in the immutable
v0.8.2 tag. Current users can choose **Settings → Your agent → Replace command…**
once to adopt the bundled CLI. The owner subsequently ran the standalone
updater: both copies now report v0.8.2/schema 50, but that manual action did
not establish app ownership. No assistant desktop update was performed.

### Publication and qualification

[v0.8.2](https://github.com/misunders2d/agentnet/releases/tag/v0.8.2) was published
at 17:58 UTC from `ac9dab82ddcc31bb510b0644a9edf1a561c015cf`. It adds group human guests, explicit permanent-member invites,
retract/refresh controls, corrected OKs/navigation, current edited previews,
person notification controls, worker wake, quieter internal records and
whole-app CLI updates. The owner approved encrypted exact read-state sharing
among current verified own human devices. Private Project organization is a
separate [design discussion](DECISIONS.md#10-private-projects-and-selected-guest-context-discussion-oct-6),
not part of this bug-fix release.

Three explicit reader capabilities preserve mixed-version behavior: `hgg1`
for complete group human guests, `gic1` for nonce-bound invitation refresh and
cancellation, and `rd1` for read-state synchronization. `rm1` implies none of
these. All live sessions on a recipient must support the new record before
handoff; otherwise the unchanged encrypted copy waits. Existing rm1-implied
tokens were omitted from advertisements to preserve the 16-token limit.

Read-state records contain bounded batches of exact `(conversation, original
author fingerprint, logical message ID)` references. They carry no text,
notifications, visible chat entry or job. Current self-person human keys and
roster chains are checked at admission and delivery. Durable local marks and
the existing outbox reconcile reconnects, restarts and newly linked devices;
marks arriving before their messages are retained. No timestamp watermark or
new polling loop is introduced. Removed devices, pending keys and conflicted
persons cannot share read state.

App-managed `agentnet update` uses the authenticated local About updater.
It refuses active work before staging, prevents new claims during handoff,
and preserves custom commands. A separately managed daemon sharing the home
must be stopped when idle first. Standalone CLI and relay updates remain
separate operations. v0.8.1 users use About once to reach this command behavior.

Focused native/browser regressions and rendered Comic/Classic/Zoom checks
pass for the implemented paths. [Candidate CI](https://github.com/misunders2d/agentnet/actions/runs/37498756845)
passed all native Linux/Windows/macOS tests, six client race shards, other
race and real browser-storage tests, the container journey and desktop builds
at `2082b4f34dd237bff8295b527afd647cb9f84124`. Only release documentation
follows that tested code. A Windows outbox-flush timeout passed on an unchanged
commit rerun; no test deadline was raised. The
[release workflow](https://github.com/misunders2d/agentnet/actions/runs/37506258479)
passed. All 12 assets matched GitHub digests and all 11 packages matched
`SHA256SUMS`; the downloaded Linux CLI reported v0.8.2, protocol 1, schema 50.
The public release is marked latest. A duplicate full CI run on the final
documentation-only commit was cancelled; the code is identical to the qualified
candidate. No live installations were changed during qualification. Later
owner and relay updates are recorded above; physical Windows/macOS updates
remain unverified. The opt-in
`TestLiveOwnAgentTopicClosure` passed against installed Codex in 81.541s:
two ordinary answers kept one topic active, then an explicit close marked it
done. It used synthetic participants, temporary AgentNet homes and a local
TLS relay; no open user sessions or existing installations were touched.

## Previous release — October 6 v0.8.1

[v0.8.1](https://github.com/misunders2d/agentnet/releases/tag/v0.8.1) is published
from `daddab38d229f695936a6d975702668b381f3135`, based on v0.8.0 (`d99dcc85`). It fixes
MEL-550–557 plus MEL-558 linked-device history, one person chat with separate
conversations, solo-group message controls and visible pending invitations.
Comic also restores direct agent-chat entry points, retains named agents on
follow-up and refreshes tool-connection status when the user returns. A local
self-addressed question is accepted only with an exact matching signed local
outbox envelope and current self key; retries never reopen an existing job.
Read [the published release notes](https://github.com/misunders2d/agentnet/releases/tag/v0.8.1)
for scope and limits; [NEXT_RELEASE.md](NEXT_RELEASE.md) tracks v0.8.2.
The owner requires every original item before rollout; none is deferred.

Conversation replication carries an unchanged signed DM root in a quiet v2
`root-sync` replica (`crs1`, explicitly advertised). Existing outbox records
provide durable reconciliation across reconnects and roster changes. It adds
no schema step, polling, history reset, visible message or execution request.
Both endpoints must remain current human devices of the same verified person;
root signatures and bound roster chains are checked. Old readers wait for an
update. Person-chat grouping never merges signed roots or their audiences.

Browser regressions cover all three skins at desktop/phone sizes. Final
[release-candidate CI](https://github.com/misunders2d/agentnet/actions/runs/37461588724)
passed native Linux/Windows/macOS tests, Linux race shards, the container journey
and all three desktop builds. The
[release workflow](https://github.com/misunders2d/agentnet/actions/runs/37466526667)
passed; all 12 assets were checked against GitHub digests and the 11 packaged
files against `SHA256SUMS`. A Windows transfer-test timeout passed on the same
commit's rerun; no test deadline was raised.

The existing relay and one existing Linux AppImage installation were upgraded
to this exact release with identity preserved and private backups retained.
The owner confirmed the v0.8.1 About page and update button in the running app.
This is not qualification of every UI feature: native photo/clipboard behavior,
interactive Windows/macOS installation, physical-phone history convergence,
and a future in-app update still need live verification. An earlier isolated
Linux test window stayed blank and was closed; that failed test was not proof
of a working native UI.

### Operating and updating an existing installation

- Read [README setup](../README.md#get-started) and the
  [installation guide](revival/INSTALL.md#updating-and-downgrading). v0.8.0
  desktop users install v0.8.1's matching desktop package once; later updates
  use **Settings → About → Update AgentNet**. The standalone CLI updater
  cannot replace the app.
- Inspect the actual process, executable path, AgentNet home and service
  ownership first. The app can attach to an older manually managed daemon;
  replacing the AppImage alone does not replace that separate daemon. Follow
  the guide's existing-installation steps rather than starting another daemon.
- Keep keys, data, permissions, service choices and user configuration.
  Preserve a rollback copy; never wipe or re-enroll to make an upgrade work.
  Do not interrupt active jobs or update additional devices without authority.
  Verify the running UI and command versions separately after reopening.
- Whole-app updates refresh the bundled command and managed AgentNet hook
  copies. They do not update the user's agent runtimes or unrelated settings.
  Unrecognized/custom CLI copies require the owner's explicit replacement
  choice. New terminal/agent sessions may be needed to see PATH changes.
- Relay upgrades are separate from desktop upgrades. Back up while stopped,
  retain the existing volume and secrets, replace the release binary/image,
  restart, then verify `/v1/version` and unchanged realm identity. An admin's
  `release set` only recommends a client version; it installs nothing.

### Open follow-ups after v0.8.1

- MEL-523 is required for **v0.8.2**: an ordinary agent answer can still close
  an ongoing topic. The earlier status-based closure rule was removed, but
  the agent prompt still encourages `topic: done` when it infers completion.
  Separate answering one request from ending the topic; preserve intentional
  closure and manual Done/Reopen. Verify a real multi-turn own-agent chat.
- MEL-494: conversation counts cover all separate roots for a person, while
  topic counts cover only the selected conversation. Empty roots remain
  reachable. New Chat and sidebar entry points need the same preference for
  an existing populated conversation and clearer scope labels; never merge
  or delete signed histories to simplify these counts.
- MEL-539: running work can be shown in OKs without an approval action; the
  arrow can retain the wrong topic. Standing question approval affects future
  eligible questions; an already held question still needs its own acceptance.
  Do not automatically re-run held, failed or interrupted work.
- MEL-559: edited message text can remain stale in the sidebar preview.
- MEL-561: chats read on one linked device can remain unread on another.
- MEL-562: internal review-status JSON can appear in agent messages/topic titles.
- MEL-564: notification settings repeat people once per conversation and mix
  person-level allow grants with conversation mutes. Proposed correction is a
  person-level control with explicit overrides, preserving existing grants.
- Agent startup latency remains separate work: observed queue waits of 41–47
  seconds need worker-wake and process-start measurement. Do not label the
  whole delay a model cold start without timings.
- MEL-563: a Codex launcher resolving back to itself caused a stuck greeting;
  the affected host's launcher was repaired separately with owner approval.
  For a stuck job, inspect the actual launcher/process and job state before
  blaming the relay or cancelling/resending anything.
- The owner reported **What's new** doing nothing on v0.8.0. Use the direct
  release URL as a workaround; v0.8.1 behavior is not yet verified.

## Previous release — October 5 implementation record

The October 5 candidate includes the app, Google sign-in, topics, person
approvals, group agents, sending, catch-up, unread and readable needs-you
fixes combined at `c049c6c7`, plus the later profile picture, Comic parity,
Windows installation and group security fixes below.
Read the [release notes](NEXT_RELEASE.md) for
what people see and the [installation guide](revival/INSTALL.md) for setup.
This entry records the candidate, not a published release or a production
upgrade. No release number is assigned here.

People install one AgentNet app per computer and open its icon. It brings
its own `agentnet` command; command-line setup is for agents, server
operators and advanced use. Phones use the browser/home-screen app. Comic,
Classic and Zoom are all standalone interface packages in this candidate.
Windows installation adds the bundled command to the current user's PATH;
new terminal/agent sessions pick it up. Its login startup entry quotes the
whole executable path, including account/install paths containing spaces.
Existing enabled entries are repaired; a saved disabled choice stays disabled.

The current behavior is:

- Google-enabled workspaces admit allowed Google accounts. A later device
  asks an existing human device of that person for an OK. Old identities
  are not converted; joining afresh is the recorded choice for this rollout.
- Every chat has topics. Done and Reopen are shared in people chats and
  groups; later unseen messages reopen a topic. Bulk actions offer a
  confirmation and Undo. Archive stays local. Delete for me in people
  chats reaches the person's linked devices; an agent-chat deletion stays
  on the deleting device.
- New accepted group agents are members that anyone in the group can ask.
  Adding the same agent again shares selected history instead of making
  another entry. Agents can ask each other and use permitted replies in
  that group. Their owners' approvals still decide execution; joining the
  group is not a permission to run tasks. An outside-owner agent requires
  a current admin with the exact current group admission; an old admin
  record gives no authority. Valid signed removals are retained in membership
  context and retried to current devices, including late joiners, without
  waiting for another group message. The receive path records only readers
  proven by the message's signed recipients, not everyone currently in the
  group; being a current member cannot authorize sharing old messages.
- Person approvals cover current and future verified devices. Only a
  person's own human devices can enroll more devices. Removed devices,
  changed keys and conflicting identities do not retain automatic access.
  Grants select the exact verified person ID (or an explicit advanced device
  address); a display name, even a unique one, never selects a grant target.
- Person profile pictures use the existing signed roster. All three skins
  offer choose/crop/preview/save/replace/remove and Use as my picture from a
  chat image. The Hub serves public PNGs by content hash; checked native and
  browser caches support offline display. Pictures are at most 256 pixels
  square and 64 KB. Removing clears the profile reference, not uploaded blobs
  or old signed references; pictures confer no identity or permission. Custom
  agent pictures are not included.
- Comic has native service/bot controls, verified team management and
  reviewed team-based invitations, plus Google Drive setup/project-space/file
  saving controls. Existing provider consent and sharing rules apply; later
  team changes grant no chat access. Reply receiver/session selection,
  continuation status and configured session-close backups remain in Classic
  and Zoom. See [the exact Comic gap](COMIC_PARITY_GAPS.md); this is not full
  parity.
- Send clears the draft you sent immediately and leaves one progress
  bubble. Another draft stays usable while delivery continues. Failures
  keep Retry and preserve newer writing. Phone catch-up processes messages,
  files and receipts with fewer delays. Reading visible messages clears
  their unread badge.
- The people responsible for deciding can read the full needs-you
  explanation in the chat and on the OKs card. Running and stalled work
  stay visible; Ask again retries, and Mark as handled clears without a
  reply or work. Invitations show the workspace name.
- MEL-521's app Do it/no-toggle entry is deliberately left as a placeholder
  in the [release notes](NEXT_RELEASE.md), for Claude to fill after merge.

Evidence is scoped. Component reports record native/browser agreement,
focused tests with race checks, and local rendered checks across Comic,
Classic and Zoom. P16 records a passing whole UI-package race run; that is
not a passing whole-repository run. P6 integration/security reports record
focused race checks and full vet. The skin and page types were regenerated
through the earlier `c049c6c7` snapshot. Later P22/P27/P29 picture checks
were completed across Comic, Classic and Zoom at desktop and phone widths;
Claude reports all six rendered scenarios passing with screenshots checked.
P20 removal and P26 reader-proof reports record focused native/browser race
checks; their browser checks use in-memory fixtures. P24 adds Comic controls,
with P30 correcting its rendered fixtures; those reports alone do not
establish a rendered or real Google Drive pass. No final candidate CI,
publication, installed-device verification or production rollout is
established by these records.

Remaining gates and limits:

- Installer completion, install/open/tray/startup behavior and download
  verification belong to the release integrator. Windows and Mac desktop
  installer runs are unverified; Mac remains explicitly untested. Builds
  are unsigned.
- Google tests used a local stand-in. Real Google accounts, desktop sign-in
  callbacks, physical phone catch-up and live group-agent replay remain
  unverified by the reviewed reports.
- MEL-525 retains the larger person identity work; approvals across devices
  are the delivered step. MEL-542 retains group-agent follow-up and live
  acceptance. Older limited guest invitations keep their earlier scope.
  New outside-owner group agents require a current admin's invitation;
  if that inviter leaves or loses admin rights, a current admin must add
  the agent again. Earlier private history is not widened automatically.
  The receive-path reader fix does not rewrite previously stored reader rows
  or grants. A late joiner already holding a stale acceptance may retain it
  if the non-inviter admin who removed the agent loses that role before the
  end arrives. A still-authorized person with an active local view can issue
  a fresh removal; an already dismissed local participation cannot simply
  be removed again. Do not claim unconditional old-state repair.
- Browser recovery after a refused, expired or removed membership still
  lacks the desktop app's Start again flow. The app's separate failed-link
  state was not included in that reset repair.

The release integrator owns remaining qualification and publication. The
older release and deployment records below are historical evidence, not
proof of this candidate's state. Their older rollout notes do not override
the current behavior above.

> **Last recorded published release — v0.7.0 (historical):** [published](https://github.com/misunders2d/agentnet/releases/tag/v0.7.0)
> from `ad52546310776e04697217e1004539093880a7b2` on 2026-10-04. All six
> binaries and SHA256SUMS were downloaded back and match the build. CI run
> 37223612752 passed native Linux, macOS and Windows, the seven parallel
> Linux race shards and the container journey. Scope: Comic, the new default
> skin, as a standalone package loaded through the same path as any skin
> (contract additions in docs/UI_SKINS.md); topics with a short bar, All
> topics, done and 7-day archive (docs/plans/TOPICS.md); job lifecycle fixes;
> messenger fields; the rm1 reader; Comic motion; the old built-in interface
> removed (Classic and Zoom follow as standalone packages from branch
> `skins/modules`, not yet merged). Production: the relay runs image
> `agentnet-revived:v0.7.0-ad52546` (stopped-state backup
> `/opt/agentnet-revived/backups/v0.7.0-20261004T183531Z`), public HTTPS
> reports v0.7.0; hub/bezos's client and admin/laptop updated with the
> official updater after stopped-state home backups, both `doctor` checks
> exit 0; the Hub recommends v0.7.0. A read-only browser check of the
> laptop page at 1440 light and 390 dark loaded Comic in its shadow root
> with its fonts and no page errors. Not updated yet: zenbook (owner tests
> it next). Not claimed: physical phone, Safari, macOS/Windows desktop runs
> beyond CI.

> **Previous release — v0.6.2 (historical):** [published](https://github.com/misunders2d/agentnet/releases/tag/v0.6.2)
> from `5342aa3a1207a95f04c888360b7937b72d8e627d`. Relay and both managed
> clients run this clean revision; both client doctor checks passed. All seven
> uploaded digests and the changed hosted UI assets match. Fixes: searchable
> people/assistant mentions with exact person references in signed text,
> compact mobile layout, and a 24-choice reaction grid. No backend/schema or
> wire-envelope change. Focused tests, UI vet, native/browser rendered checks,
> and a live Chats/Settings smoke at 1440/390 passed. Keyboard testing was a
> viewport simulation; owner phone acceptance remains pending. Full emoji
> search/categories/recents and responder attachment inspection remain outside
> this correction. Prior release evidence follows unchanged.

> **Previous release — v0.6.1 (historical):** published stable
> [v0.6.1](https://github.com/misunders2d/agentnet/releases/tag/v0.6.1) from
> `2eb1d6eac8304ff7f11c7af4dddb853aaa4cd18c`; all seven uploaded asset digests
> verified. Production: the relay and both managed client services run that
> exact clean revision; client doctor checks exited 0 with identities,
> responders and grants preserved; stopped-state backups were retained; the Hub
> recommends v0.6.1.
>
> Scope: replies stay in the conversation where they were asked, and a CLI
> request returns to the assistant session that made it (automatic for Pi/OMP
> sessions with AgentNet's hook and for Claude Code and Codex sessions
> registered through AgentNet's setup, also while a temporary person is present;
> harness permissions still apply); people `@`-address assistants accepted into
> a conversation; owner-approved temporary participation of another person in
> the same DM, after which the original members continue privately; choosing a
> different reply receiver stays an explicit advanced option. Claude/Codex
> origin return selects the session from the harness's own session environment
> and registration files, not from the harness process a sandbox hides;
> delivery keeps every native check, and the sandbox's own file/network rules
> still decide whether the command can run. Verified end to end once with Codex
> 0.160.0 in workspace-write mode with only the test's AgentNet home writable
> and loopback network, and once with Claude Code 2.1.287 on Linux (one model
> turn; other Claude builds and a Claude sandbox not exercised). Also: a
> temporary person may address an added assistant only under its owner's
> permissions (joining grants no question or task permission); questions to
> named and conversation assistants run with the owner's own question setup (no
> editing tools; tools the owner already allows keep their effects); assistants
> react as themselves, and with a temporary person present a reaction reaches
> the request's captured audience; deleting a conversation erases exactly the
> messages the deleting device holds, on all of that person's linked devices
> (each applies it once it reads deletions; running work and unsent copies are
> kept until they finish or are handed over; group membership is kept), while
> deleting a legacy device thread erases that exact thread on this device only;
> assistant setup and readable workspace names in the device's messenger; a
> quiet update notice; the daemon finds launchers installed beside `agentnet`;
> marked progress updates do not end a wait. The `hgp1` (temporary people),
> `agr1` (assistant reactions) and `clr1` (deletions) capabilities are
> advertised.
>
> Validation is composite, not one green full-suite command: `go vet ./...` was
> clean; the single local race run failed, because the client package exceeded
> its 600s per-package limit (no test had failed before the timeout) and three
> browser-test fixtures failed. Coverage was completed by running the
> interrupted and unrun client tests in two disjoint batches under CI's 3600s
> allowance (only opt-in tests skipped) and by narrow reruns of the three
> corrected browser tests. Initial
> [CI 37117890426](https://github.com/misunders2d/agentnet/actions/runs/37117890426)
> passed Linux native and container checks; its macOS fixture readiness was then
> fixed, and Windows explicitly skips three tests whose shell stand-ins it
> cannot run. Final
> [CI 37119578563](https://github.com/misunders2d/agentnet/actions/runs/37119578563)
> was still running when this was written and is not claimed green. A bounded
> read-only production smoke passed: the exact served assets, and the Chats and
> Settings views at 1440 and 390 pixels. No live conversation or task and no
> full navigation is claimed.

> **Previous release — v0.6.0 (historical):** read [NEXT_RELEASE.md](NEXT_RELEASE.md)
> and the current [CLI guidance](../README.md#guides-for-agents-and-maintainers).
> Published stable [v0.6.0](https://github.com/misunders2d/agentnet/releases/tag/v0.6.0) from
> `49d0f545da8d33dc13e8c4775048b9ee78afa297`; all seven uploaded asset
> digests verified.
> [CI 36982671958](https://github.com/misunders2d/agentnet/actions/runs/36982671958): native
> Linux/macOS/Windows, container and non-client race checks passed. The client
> race aggregate timed out at 1800s without an assertion failure or race
> diagnostic; the following Chromium stage was skipped. CI is not all green.
> Production: relay HTTPS and both managed client services report v0.6.0.
> Stopped-state backups were retained; client doctor checks exited 0 and identity,
> config, grants, peer pins and history were preserved. The observed client upgrades
> migrated schema 12 (v0.2.1) and schema 22 (v0.5.0) to schema 35.
> Authenticated page and served-app digest checks passed; full production
> navigation remains unqualified by the read-only smoke.
>
> Groups, named member/outside-host participations, selected reply receivers,
> authenticated controls/typing and linked history/files have scoped native,
> browser and rendered qualification. Historical pre-release accepted
> composite regression coverage: 436 client tests passed, 6 opt-in tests skipped;
> the original whole command timed out; that attempt is not a single-command
> full-suite pass. Synthetic migration/snapshot rollback checks also passed;
> the earlier same-source updater handoff did not prove historical migration.
> The production upgrades above establish only their observed upgrade paths.
> Teams snapshot review/invitations and selected history/files are qualified at
> native 390px/browser 1280px (MEL-502: In Review). Stale proposals require
> explicit renewed consent; later team changes grant no group authority. Actual
> Linux visible notification/click, physical Windows/macOS/Android and live non-prod
> Drive checks remain unverified limitations accepted for owner-authorized
> production rollout; native CI does not establish those physical/live outcomes.
> Initial candidate CI had macOS fixture failures; corrected final CI is recorded
> separately above. Earlier releases below remain historical evidence.

> **Target Audience:** Any incoming coding agent (Claude, Codex, Antigravity, Pi) or human engineer starting fresh in this repository without access to prior chat transcripts.
>
> **Historical published preview — 2026-09-30:** [v0.5.0](https://github.com/misunders2d/agentnet/releases/tag/v0.5.0) is published from `d30c087`. [Final CI 36698046881](https://github.com/misunders2d/agentnet/actions/runs/36698046881) passed native Linux, macOS and Windows, Linux race and real IndexedDB checks, and the container journey. Seven uploaded asset digests match the final archive build. Stable/latest remains v0.2.1; other clients install this preview explicitly with `agentnet update v0.5.0`. No schema or wire-version change from v0.4.0.
>
> **Historical rollout observation — 2026-09-30:** the operator's relay and Linux laptop run v0.5.0. The relay image is `agentnet-revived:v0.5.0-d30c087`, using the unchanged external volume `agentnet-revived-v040-09c4da6`; public HTTPS reports v0.5.0. The laptop's official updater verified the release and switched its running daemon at the same UI address. Notebook is installed in the laptop's local skins directory only. No custom skin was installed on the relay; its catalog lists the default UI only. Deployment configuration owns the relay hostname, never product defaults. Other clients, including the remote responder installations, are not claimed updated by this rollout.
>
> **Historical v0.5.0 live UI evidence:** the sole Chrome tab passed laptop Chats/People/Activity/profile and existing Zenbook-thread checks at 1280 and 390 pixels, with no overflow or page errors. The laptop profile lists laptop/pixel/zenbook. Locally installed Notebook mounted independently, read the same 13-message thread and switched back to the default UI. No send/decision/link/notification-setting POST was made during this smoke. Hosted landing/assets/version checks passed; the relay catalog contains only default. Hosted loader/app bytes match release source. Screenshots remain private. Physical-phone installation, standalone app launch and push were not tested; narrow settings tabs still scroll, and grouped device conversations still require an extra navigation step.
>
> **Historical v0.5.0 batch scope and remaining work:** compact Chats/People/Activity/profile navigation, scalable lists, settings/appearance, independently loaded trusted UI packages, question clarification guidance, labeled earlier task context and linked-device setup guidance shipped. CI also exposed a pending-link subscription race: `bfa4d84` reads persisted refusal/activation/expiry before waiting; deterministic regressions cover all three. The later Windows reminder failure was a fixture deadline racing cancellation; test-only `d30c087` cancels first, then evaluates past the deadline explicitly. MEL-475 owns the remaining skin contract/conformance/sharing work; skins must ultimately install per client without relay cooperation. Desktop local installation exists; browser-local package installation does not yet exist (the hosted-browser catalog currently comes from its relay). LLM review is advisory, not a security guarantee. MEL-498 tracks further human UX refinements; MEL-497 retains task clarification/remote review gaps; MEL-499 covers task context and MEL-500 the linking race.
>
> **Messenger preview — 2026-09-29:** [v0.4.0](https://github.com/misunders2d/agentnet/releases/tag/v0.4.0) is published as a prerelease from `09c4da6`. [Final CI 36609670816](https://github.com/misunders2d/agentnet/actions/runs/36609670816) passed native Linux, macOS and Windows, Linux race and real IndexedDB checks, and the container journey. The preceding Windows failures were corrected in test-only commits `b0b8e7f` (close an opened file) and `09c4da6` (realistic heartbeat timing); their failed run remains recorded. All seven uploaded release asset digests match the reproducible build. Stable/latest remains v0.2.1; install this preview with `agentnet update v0.4.0`.
>
> **Prior live rollout — 2026-09-29:** the operator's relay at `https://agentnet.bezosapp.uk` and Linux laptop ran v0.4.0. This hostname is deployment configuration, not a product default. The Hub was restored into a fresh volume from a stopped v0.3.0 backup (six enrollments, 121 messages, two attachments); the original volume remains intact. Public HTTPS and the unchanged legacy pinned endpoint reported v0.4.0. Hosted JavaScript bytes matched the release source; the real hosted landing page passed desktop and 390px Chrome checks. The laptop's key, connection/responder settings, peer pins, approvals and direct-device message IDs were unchanged. Its new person was Sergey, then on admin/laptop. A read-only real laptop page check showed one self in Classic and Zoom, laptop under Your devices, Add a device available, no downgrade prompt and no 390px overflow; no message or link was created. Physical Android linking/install/files/background push/click remains unqualified. Real Windows banner clicks and physical suspend/resume remain unverified; macOS is banner-only.
>
> **Identity and UI cutover:** v0.4.0 implements one person across linked devices, existing-DM history/files, grouped conversations, visible devices and browser-to-device messaging. Old preview person/DM rows stay unused; direct-device conversations remain. Before relinking an old preview browser, its owner must close all AgentNet tabs/app windows and clear that relay origin's site data. Then use the laptop page's **Your devices → Add a device**, choose an unused device name (the operator's old `admin/phone` address remains taken), and approve the request on the laptop within ten minutes. No agent cleared the owner's phone. Browser data and notification permission are lost by this explicit reset. The local page is opened through `agentnet ui`; the hosted page holds its own linked device key. [DECISIONS §2](DECISIONS.md#2-human-identity--multi-device-linking-mel-433-synthesis) records the model and limits.
>
> **Dated baseline — 2026-09-27:** [Client release v0.2.1](https://github.com/misunders2d/agentnet/releases/tag/v0.2.1) (Binary: `611b633`, prior evidence docs: `2395d11`, CI: `36341139910`), compatible with deployed Hub relay `v0.2.0`. Recheck current state before operations.
>
> **Linear Tracking:** Project [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580) (Tickets `MEL-409` through `MEL-435`; this repository stands completely alone if Linear is inaccessible).

---

## 1. Quick Orientation & Cold-Start Entry

### 1.1 What is AgentNet?
AgentNet is a minimal, self-hosted Go MVP communicator for coding agents. It enables agents on separate laptops, workstations, or servers to send messages, exchange large encrypted files, ask questions answered by the coworker's local agent setup, and delegate tasks—with no inbox polling, strict human authorization gates, and end-to-end encrypted payloads.

Routing metadata (sender, recipient, message ID, timestamps, attachment sizes and digests) is visible to the Hub for delivery; message text and file attachments remain encrypted ciphertext.

The client and Hub share **one Go program**, `agentnet`. People use the installed AgentNet desktop app, which includes that program and opens its messenger. The command line remains for agents, servers and advanced use.

### 1.2 Two Codebases: Main (Go) vs Legacy (Python)
- **`main` (Go Revival, v0.2.x)**: The active Go codebase. Standard library first, modernc SQLite, filippo.io/age encryption, official A2A SDK. This is the **only** active development branch.
- **`legacy/agentnet` (Python, v0.1.x)**: The historical prototype. It is preserved for archival reference only. **Never mix, edit, or import from the legacy Python codebase.**

### 1.3 Recommended Reading Path for Fresh Agents
To reach full productive context without reading lost chat sessions, follow this exact sequence:
1. **[`AGENTS.md`](../AGENTS.md)**: Product rules that all code must maintain (non-negotiable safety and architecture invariants).
2. **[`docs/HANDOFF.md`](HANDOFF.md)** (this document): Operational state, contracts, verification matrix, and current handoff boundaries.
3. **[`docs/DECISIONS.md`](DECISIONS.md)**: Architectural decisions ledger, debate resolutions (human identity, UI skeleton, comic avatars, notification clicks), and ticket mapping.
4. **[`README.md`](../README.md)**: Public user guide, quickstart, command summary, and tested limits.
5. **[`docs/revival/INSTALL.md`](revival/INSTALL.md)**: Setup, daemon login services, Hub operations, backup/restore, and updates.
6. **Milestone Design & Limit Notes**:
   - **[`docs/revival/M1.md`](revival/M1.md)**: Cryptographic identity (Ed25519/age), enrollment, Hub protocol, signed requests, push stream.
   - **[`docs/revival/M2.md`](revival/M2.md)**: Resumable chunked encrypted file attachments, streaming age, quota accounting.
   - **[`docs/revival/M3.md`](revival/M3.md)**: Ephemeral session presence (`#SESSION`), direct peer HTTPS delivery, Hub fallback.
   - **[`docs/revival/M4.md`](revival/M4.md)**: Multi-harness auto-answers, skills-on question mode, human task review states, headless review notices, conversation threading, loopback A2A gateway.

---

## 2. Core Invariants & Product Contracts

Every change to AgentNet must uphold these fundamental invariants:

1. **Identity is a Key, Membership is an Invite**:
   - Agents enroll with an Ed25519 signing key and an age (X25519) encryption key generated locally, stored in `<home>/identity.json`.
   - Address format is `person/agent` (e.g. `alice/laptop`).
   - Admins invite members (`agentnet admin invite <label>`); invite codes carry the Hub URL, label, secret, and certificate pin.
   - Public keys are pinned on first contact (TOFU). Changed peer keys block sending and hold incoming messages until explicitly trusted (`agentnet trust <address>`).

2. **End-to-End Encryption & Blind Hub**:
   - Each agent instance has a unique Ed25519 identity key and an X25519 age encryption key stored in `<home>/identity.json` (`0600` permissions; Windows protected owner-only ACLs via `internal/secfile`).
   - Private keys are generated locally and are never copied between devices for device linking, exported across the network, or backed up on the Hub. Protected local backups of the device's home directory are authorized and allowed.
   - Messages and attachments are encrypted directly to the recipient's public age key.
   - The Hub relay stores only ciphertext, routing metadata (sender, recipient, message ID, timestamp), and attachment sizes/digests. The Hub cannot decrypt content.

3. **Receipts Say Only What is Proven**:
   - `custody`: Hub stored the ciphertext.
   - `delivered`: Recipient verified, decrypted, and stored the message.
   - `quarantined`: Recipient received the envelope but signature or key verification failed.
   - `expired`: Directed session ended before delivery was acknowledged.
   - **Sender Delivery State Lag**: Sender delivery state can lag. Use `agentnet status --wait 30s ID` to refresh with a bounded receipt wait; there is no `agentnet wait` subcommand. Connect/ping retries queued sends and sends stored recipient receipts; it does not refresh every sender outbox state. Transport success is **never** conflated with task completion.

4. **No Polling**:
   - The client daemon maintains a single persistent HTTP/2 push stream to the Hub.
   - The only periodic network traffic is a signed ping acknowledgement every 90 seconds.
   - Inbox checks, file updates, and review notices wake reactively via daemon stream events, local socket kicks, or job completions.

5. **Questions vs Tasks vs Follow-ups**:
   - **Direct-device default question (`agentnet ask`)**: Automatically answered **only** if the sender is approved by verified person or exact device key and a responder is active. Person approval covers the current verified roster and later linked devices, not a matching name. Runs with the recipient's **own setup** (skills, plugins, MCP servers, permissions), with harness-specific fencing:
     - Claude Code: `--permission-mode dontAsk --disallowedTools Edit,Write,NotebookEdit`.
     - Codex CLI: `--sandbox read-only -c approval_policy="never"`.
     - Pi: `--exclude-tools bash,edit,write,powershell`; recipient extension tools remain available with their configured effects. Pi has no read-only shell or unattended approval gate. The new preset has isolated SDK tool-selection evidence, not live-model qualification.
     - Tools and Bash commands already permitted by user settings keep their native effects (not a blanket sandbox). If the model cannot answer without forbidden tools, it responds `AGENTNET: NEEDS-HUMAN`.
   - **Task (`agentnet task`)**: Stored as `awaiting` for the person and run only after `agentnet accept <id>` (or declined with `agentnet decline <id>`), unless the recipient has approved that verified person or exact device key for tasks (`agentnet approve --tasks PERSON_OR_ADDRESS` or `agentnet accept --always <id>`). Person grants cover current and later linked devices through the verified roster; removed devices lose access, and changed keys or frozen persons block automatic execution. Grants are local only, never created by received names or messages, and never rerun failed or interrupted work; either way the responder runs with the recipient's normal permissions. An ordinary conversation question/task addressed to the person does not execute. A request addressed to an accepted agent participation can execute under its exact current host/member/epoch and task grants. An invite of the host person's own agent from the host device, or from an own device trusted there with `agentnet person approve --native` (never a browser), is accepted without a click (owner decision D3, `docs/plans/ROOM_V1.md` §5), so its task keys (trusted own keys only) may then give it tasks; that trust is local only, set by nothing received. Background workers use their own sessions; only an explicitly selected, verified native receiver adapter may deliver continuation input to a user session.
   - **Legacy follow-up (`--follow-up <text>`)**: One local summary of the first correlated reply; sends nothing back. This is separate from explicit selected-receiver continuation.
   - **Selected reply receiver**: `--reply-receiver human|AGENT_ID|session:HANDLE` binds before enqueue. Managed receivers need original `--continue TEXT --continue-mode question|task`; exact native sessions use verified adapter registration. `--reply-binding ID` reuses frozen authority/context. Optional `--on-close-agent AGENT_ID` authorizes a managed backup at send time, only on a supported verified normal shutdown. Unknown/changed bindings never fall back to the default. Registration is not liveness; accepted input is not completed effects. Receiver-local authority is separate from remote task grants.

6. **Interactive Session Awareness & Hooks Contract**:
   - Inspect `agentnet hooks show claude|codex|pi|omp`; install only by explicit local choice. Codex requires native hook trust. Windows hooks and Antigravity remain unsupported.
   - Generic arrival attention keeps per-harness/session cursors and bounded metadata; it does not choose a receiver or grant authority. Full history remains an explicit `agentnet conversation ID` read.
   - Explicit selected native receivers reuse verified adapters and durable correlation/receipt checks. Pi/OMP actual idle, busy/tool, file and restart delivery and Codex busy queue/clean-close backup have scoped evidence. OMP 18.4.8 clean-close backup also has scoped actual evidence: original session/file preserved and the selected managed backup ran once after daemon restart, using a deterministic synthetic executable. Claude selected-session idle continuation has actual product evidence: one exact physical reply ACK and a native turn boundary. Dispatch-busy continuation is also qualified: a second signed input waits durably during native prompt dispatch, followed by two ordered native turns and exact physical ACKs, default0/no replay, without a terminal nudge. In-flight sampling/tool timing and business-tool effects remain untested. Inactive or uncertain input stays bound/pending without arbitrary terminal injection or reassignment.
   - Claude's opt-in Linux channel assets are shown/installed with `hooks show|install claude --channel`; installation prints an MCP fragment, never enables channels, installs Node, or changes MCP configuration. Native development-channel admission and consent remain explicit. SessionEnd marks detached, not a verified normal shutdown for backup; input acceptance never proves model completion.
   - Historical hook versions and qualification are retained in [M4](revival/M4.md) and the release ledger below. Source contracts: [hook dispatcher](../cmd/agentnet/hook.go), [selection flags](../cmd/agentnet/receivers.go), [native registration](../internal/client/replysessions.go).

7. **Persistent Background Sessions & Context Retention (MEL-425)**:
   - Claude/Codex store `session_ref` on an inbox job and `session_head` per harness/session ID. Resume requires the first linked ancestor to be the latest job in that native session, to have ended cleanly, and to match peer, mode, canonical directory and preset. Branching from an older job, a changed preset/directory/mode, or interrupted/failed/cancelled/needs_human state starts fresh. Failed resume is not retried automatically. There is no context-exhaustion recovery mechanism in AgentNet. Native context lives in the harness store with harness-controlled retention; AgentNet history is separate. Pi stays one-shot. See M4 for full rules.

8. **Process & State Isolation**:
   - Responders run headless in their own process group (`Setpgid: true` on Unix).
   - Output buffers: standard command stdout is capped at 64 KiB and stderr at 4 KiB. For streaming Codex CLI executions (`codex exec --json`), individual JSON lines are bounded to 4 MiB (`codexLineMax = 4 << 20` in `internal/client/codexstream.go`), with bounded fallback to `-o`.
   - Process cancellation: On Windows, cancellation terminates the worker process (`cmd.Process.Kill()`), but child process cleanup is unverified. Pi argument length limits on Windows remain an open uncertainty (`MEL-414`).
   - Database operations use SQLite in WAL mode with append-only schema migrations.
   - All state, keys, databases, and downloads are restricted to owner-only permissions (POSIX `0700` home, `0600` files; Windows protected owner-only ACLs via `internal/secfile`).

---

## 3. Historical Shipped State & Verification Matrix

### 3.1 Stable Release Baseline (preview status above)
- **Client Version:** `v0.2.1`
- **Git Commits:** Code: `611b633`, Documentation baseline: `2395d11`
- **Hub Version:** Compatible with `v0.2.0` and `v0.2.1`.
- **Binary Artifacts:** Cross-compiled into `dist/` via `scripts/build.sh` (emits six binaries only; release manifest `SHA256SUMS` is prepared separately during release packaging):
  - `agentnet-linux-amd64`, `agentnet-linux-arm64`
  - `agentnet-darwin-amd64`, `agentnet-darwin-arm64`
  - `agentnet-windows-amd64.exe`, `agentnet-windows-arm64.exe`

### 3.2 Automated CI Matrix (GitHub Actions)
All native source qualification jobs passed in GitHub Actions ([Run 36341139910](https://github.com/misunders2d/agentnet/actions/runs/36341139910)):
- **Linux (`ubuntu-latest`)**: Unit tests, race detector (`-race`), integration journeys (`itest`).
- **macOS (`macos-latest`)**: Native compilation, unit and integration test suite.
- **Windows (`windows-latest`)**: Native unit tests, owner-only ACL verification (`TestOwnerOnlyACL`).
- **Container**: Hub image build, bootstrap, backup/restore, offline catchup (`scripts/hub-container-test.sh`).

### 3.3 Live Verification Ledger
The following historical milestone capabilities were tested on live machines; they do not qualify the current source candidate:
- **Claude Code 2.1.283 (Linux)**:
  - Native background session resumption verified live at `c7b5d1a` / `0a73a04`.
  - Skills-on question mode verified live (`8ce73cc` / `8ed75da`): synthetic skill read delivered secret; file creation request answered `AGENTNET: NEEDS-HUMAN`; Write tool removed by flags.
  - Accepted tasks verified live in earlier milestone runs.
- **Codex CLI 0.157.1 (Linux)**:
  - Native background session resumption and streaming JSON event capture (`turn.completed`) verified live at `0a73a04`.
  - Skills-on question mode verified live (`8ce73cc` / `8ed75da`): read-only shell sandbox active; synthetic skill read delivered secret; `touch` request answered `AGENTNET: NEEDS-HUMAN`.
  - Standalone questions, follow-up summaries, and accepted tasks verified live in earlier milestone runs (`d6785bd`, `9575b2a`).
- **Pi (Linux, 2026-09-27 18:55 UTC, binary `611b633` / `v0.2.1`)**:
  - Diagnostic question lookup answered live from read-only records.
  - Explicitly accepted task sent exactly one Telegram notification via canonical skill/tool setup (`done` status; provider message ID independently verified in delivery ledger by supervisor with zero duplicates).
  - Headless environment credential timeout resolved via dedicated AgentNet PATH wrapper invoking host Infisical runner (host environment configuration only; zero daemon secrets or product dependencies; exact cause of the initial 5-minute timeout remains unproven due to absent transcript).
- **Desktop Notifications & Headless Review Notices**:
  - Linux desktop verified live with real `notify-send` (notification ID replacement, silence hint, duplicate suppression across daemon restarts, visual banner confirmed by owner).
  - Headless review notices verified live at `v0.2.1`: awaiting task on headless host never executed; single review notice recorded as `needs_human`/`notified=1` at laptop; owner visually confirmed desktop banner; synthetic item declined and resolved.
- **Zenbook Host Update Verification**:
  - Update notice reached relay custody only; recipient delivery and upgrade were not confirmed. Full unattended follow-up and returned-file roundtrip remain unproven on that host.

### 3.4 Honest Qualification Limits (What is NOT Claimed)
- **Runtime Tool Refusal**: Models answered `NEEDS-HUMAN` per system prompt; active refusal by the sandbox/harness was not exercised.
- **Allowed Bash/MCP Effects**: Tools and commands already auto-approved in user settings keep their effects; AgentNet does not provide a hypervisor sandbox.
- **Historical Pi background qualification**: These milestone runs proved one diagnostic question and accepted task, not persistent background sessions. Current explicit Pi native-session receiver evidence is separate from this old background-worker limit.
- **macOS & Windows Desktop UI**: macOS `osascript` builds and passes test suites, but has NO live desktop API execution evidence. Windows `Shell_NotifyIconW` passes native API execution in CI, but real physical desktop balloon display has not been verified live.
- **Platform Deployments**: Hub platform TLS tested with local proxy; not deployed to Railway or cloud PaaS.
- **Security boundary:** local history is plaintext in owner-only storage, not an encrypted local database. The Hub sees routing metadata and is trusted at first contact; compare fingerprints independently. There is no forward secrecy or retroactive removal of delivered data. Responder plaintext reaches its chosen model provider. See M1 for the full limits.

---

## 4. Codebase Architecture & File Map

```
agentnet/
├── cmd/agentnet/               # CLI Entrypoint & Subcommands
│   ├── main.go                 # Command routing, flags, global error handling
│   ├── help.go                 # Structured help topics (agentnet help <topic>)
│   ├── invite.go               # Join, admin invite, bootstrap invite parsing
│   ├── hook.go                 # Hook runner and installer for Claude Code & Codex
│   ├── hubcmd.go               # Hub serve, storage, cleanup, backup, restore
│   └── uicmd.go                # agentnet ui: the daemon-hosted messenger page (daemon --ui) and --demo
├── internal/
│   ├── protocol/               # Wire types, addresses, invite format, request signing
│   ├── envelope/               # Encrypted envelope format, age encryption, manifests
│   ├── identity/               # Ed25519 signing keys, age keys (identity.json)
│   ├── secfile/                # Owner-only file creation (0600/0700, Windows ACLs)
│   ├── lockfile/               # Process locking (prevent dual daemon/CLI corruption)
│   ├── sqlitedb/               # SQLite connection, WAL mode, schema migrations
│   ├── notify/                 # Native desktop notifications (Linux, macOS, Windows)
│   │   ├── notify_linux.go     # notify-send implementation with ID replacement
│   │   ├── notify_darwin.go    # osascript implementation
│   │   └── notify_windows.go   # Shell_NotifyIconW implementation
│   ├── hub/                    # Hub service, push streams, presence, blob storage
│   ├── client/                 # Agent client logic
│   │   ├── client.go           # High-level client API (send, ask, task, download)
│   │   ├── daemon.go           # Long-lived push connection, stream listener, ping ACK
│   │   ├── store.go            # Local SQLite schema (inbox, outbox, config, attention)
│   │   ├── worker.go           # Job runner for auto-questions and accepted tasks
│   │   ├── respond.go          # Manual transitions: accept, decline, resolve, takeOver
│   │   ├── responder.go        # Responder selection, detection on PATH, configuration
│   │   ├── attention.go        # Hook attention manager (per-session inbox cursors)
│   │   ├── session.go          # Session state tracking (session_head), native resume
│   │   ├── codexstream.go      # Streaming JSON event parser for Codex (4 MiB line cap)
│   │   ├── reviewnotice.go     # Headless review notice generator and forwarder
│   │   ├── conversation.go     # Threading and reply graph reconstructor
│   │   ├── direct.go           # Direct peer HTTPS server and client transfers
│   │   └── files.go            # Spooling, chunked uploads, verified atomic downloads
│   ├── a2abind/                # Loopback A2A protocol gateway (a2aproject/a2a-go/v2)
│   └── ui/                     # Embedded web UI for demo/inspection
│       ├── ui.go               # Web server setup, loopback binding, token auth
│       ├── server.go           # HTTP handlers, cookie exchange, event streaming (SSE)
│       ├── fixture.go          # Mock data fixtures for UI demo mode
│       └── static/             # Embedded HTML/CSS/JS assets
├── itest/                      # Out-of-process integration tests with real binary
├── docs/                       # Tracked documentation
│   ├── HANDOFF.md              # This handoff and operational guide
│   ├── DECISIONS.md            # Architecture decision records, ticket map, roadmap
│   └── revival/                # Milestone design notes and install guide
├── scripts/
│   ├── build.sh                # Cross-platform build script into dist/
│   └── hub-container-test.sh   # Container integration test script
└── compose.yaml                # Docker Compose file for self-hosted Hub
```

---

## 5. Standard Operating Procedures & Playbooks

### 5.1 Local Verification (Hermetic, No Model Calls)
For hermetic source verification without model calls or live deployments (tests use local network fixtures; dependency downloads may require network access):
```bash
# 1. Format and code analysis
go vet ./...

# 2. Run unit and integration tests with race detector
go test -race -count=1 -timeout 600s ./...

# Optional focused alternative: out-of-process CLI journeys (already included above)
go test -count=1 -timeout 300s ./itest/...

# Packaging only when required; compilation is not native runtime evidence
scripts/build.sh
```

### 5.2 Upgrading Clients
1. **Resolve Environment & Check Idle State**:
   - Resolve actual home, binary and owning service; inspect inbox job states and process tree privately.
   - Review-only listing (`agentnet inbox --review`) cannot prove idle.
   - Defer while `running`, `cancel_requested`, `accepted`, or claimable pending jobs exist.
2. **Stop Service & Recheck**:
   - Stop exact service (e.g. systemd user unit, launchd service, or Scheduled Task/daemon PID) and recheck before replacing anything.
   - *Never use blanket `pkill` commands.*
3. **Unique Owner-Only Backup**:
   - Create one unique owner-only backup directory; copy the stopped whole home, old binary, and relevant external hook configs (`~/.claude/settings.json`, `~/.codex/hooks.json`) there; keep its path for rollback.
4. **Verify Checksum & Install New Binary**:
   - Explicitly verify the new binary checksum against the release distribution manifest (`SHA256SUMS`).
   - Replace binary on `PATH` (e.g. `install -m 0755 agentnet-linux-amd64 ~/.local/bin/agentnet`).
5. **Restart & Verify Continuity**:
   - Start daemon service, run `agentnet doctor` to verify connectivity and automated schema migration (`agent.db.vN.bak` created before migration).
   - Compare identity fingerprint, responder/settings, approvals/peers and message IDs before/after without exposing private values.
6. **Rollback Procedure**:
   - If an issue occurs, stop the service first, then roll back **both binary and home directory/database together** (restore stopped home and database from the backup AND reinstall previous binary). Rolling back restores key and history consistency with the older binary, at the cost of any messages or attachments received after the backup was made.

### 5.3 Maintenance & Cleanup Commands
- **Client Local Cleanup**:
  ```bash
  agentnet cleanup [--saved]
  ```
  With the daemon stopped, removes encrypted copies of abandoned sends and direct uploads never attached to a message. `--saved` also removes directly received ciphertext of files already saved.
- **Hub Cleanup**:
  ```bash
  agentnet hub cleanup --data DIR [--delivered-older-than 720h] [--unattached-older-than 24h]
  ```
  With Hub stopped, removes delivered attachment ciphertext and old unattached reservations. Undelivered files in custody are never deleted.
- **Hub Consistent Backup & Restore**:
  ```bash
  agentnet hub backup --data DIR --out hub-backup.tgz
  agentnet hub restore --from hub-backup.tgz --data NEWDIR
  ```
- **Admin Release Recommendations (Equality-Only Nudges)**:
  ```bash
  agentnet admin release set --url https://... [--note TEXT] VERSION
  ```
  Broadcasts advice to members. Compared strictly for string equality (no semver ordering, no automatic installation).

### 5.4 Private Recovery Discovery (No Secrets In Logs)
If a local database or key file is damaged:
- Check for automatic pre-migration backups: `<home>/*.vN.bak`.
- Keys are located in `<home>/identity.json` (holds Ed25519 and age keys). Permissions must be owner-only (`0600`). On Windows, verify protected owner-only ACLs via `icacls`.
- To test Hub connectivity without sending messages: `agentnet doctor`.
- **Never paste raw keys, invite codes, or auth tokens into public issues or transcripts.**

### 5.5 Headless Troubleshooting & Operational Continuation
- A relay and an enrolled server responder are different processes/homes. The relay does not answer questions by itself. On the receiving installation check `doctor`, `responder show`, sender approval and `inbox --review`. A delivered question may be held; approval does not itself run an older held item. Tasks require explicit local acceptance (`accept ID`) unless the local user granted that sender's exact key (`approvals` lists grants and whether they still hold).
- Use `conversation ID` for durable two-way history without changing read state. Ordinary `inbox` marks listed messages read; `inbox --review` does not. Read state never proves a model or person understood the message.
- The daemon's service environment can differ from an interactive shell. Resolve the actual harness on its PATH and its canonical credential bootstrap. In the live Pi task, an AgentNet-only wrapper reused the host's existing secret runner for the child; the daemon received no injected Telegram secret. This was host configuration, not a mandatory Infisical dependency. Preserve the host's non-poller setting and AgentNet background-hook isolation.
- A timeout does not prove an external effect failed. Before retrying an effectful task, check its provider/ledger evidence and any human confirmation needed to rule out duplicates. The successful Telegram acceptance was independently verified once; it did not prove the person read it.
- `review-to ADDRESS` forwards count-only attention to a chosen installation; both ends need the feature. It neither grants remote acceptance nor forwards request content. Optional notifications for ordinary messages are separate and built in local commits (desktop alerts from the daemon, browser push; see the notifications item below).
- On an existing host, discover the actual service, home, responder directory, private stopped backups and rollback record locally with authorized access. Never assume another machine has the operator's paths or credentials. The repository deliberately contains no private deployment inventory or recovery secrets.

---

## 6. Historical Roadmap & Recorded Boundaries

The entries below preserve earlier releases, decisions and their then-open
limits. They are not the current implementation inventory or worker roster.
Use [NEXT_RELEASE.md](NEXT_RELEASE.md) for current acceptance gates; current
source supports groups and explicit selected receivers as described above.

### 6.1 Immediate Focus Areas
1. **Pilot Deployment & Real-World Use**:
   - Begin small-scale operator deployment using released `v0.2.1` binaries.
   - Monitor real-world desktop notifications and review notices across Linux desktop environments.
2. **Community Verification (Call for macOS & Windows Testers)**:
   - Verify native desktop balloon/banner display on physical macOS and Windows machines (see current verification limits in [`README.md`](../README.md#guides-for-agents-and-maintainers)).
3. **Linear Tracking**:
   - Track progress across tickets `MEL-409` through `MEL-435` in project [Agent Net Revived](https://linear.app/mellanni/project/agent-net-revived-c2f1212b3580).

### 6.2 Explicitly Held / Deferred Work
- **Messenger continuation contract (2026-09-28)**: Before scoping chat work, read
  [DECISIONS §3.2](DECISIONS.md#32-persistent-conversations--temporary-participation-2026-09-28).
  Conversations outlive model sessions; participant/history access is explicit.
  An agent's first report does **not** dismiss it: it remains available for
  follow-ups, with no model calls merely to wait, until explicitly dismissed.
  This records design direction and acceptance examples, not implementation approval.
- **Messenger architecture (reviewed design, partly built: S-A, the People directory, Pi attention, the
  human-DM core and its page candidate, browser-wire infrastructure; the rest is proposal)**: read
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) before any messenger code.
  It covers the real Provider/backend contract, relay-served phone/browser devices
  and the requirement that the relay stays one portable unit (binary/container,
  one data volume, one port). Owner choices listed there (§15) remain open.
- **Increments, not the messenger (owner correction, 2026-09-28)**: the device-inbox page (S-A,
  directory included) and the N7 browser transport (S-W, v1) do not complete the messenger contract of
  [DECISIONS §3.2 and §2](DECISIONS.md). Still required: choose a person (e.g. Vitalii), hold a human DM,
  explicitly invite an agent with the context you allow, clear human/agent participants and authorship,
  and follow-ups until an explicit dismissal. A message kind is not a participant's identity or an agent
  invitation; an address is one installation's key, not a person; nothing is merged by address. Zoom
  must show each human with visible links to their own agents (ownership, not presence or invitation;
  from the reviewed identity/agent relationship, never labels or addresses; no device-as-human shortcut).
  See [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Human-DM core (corrections accepted)**: `1d97945` (core Claude) adds, from the CLI (`agentnet person create`,
  `agentnet dm`), explicit single-device persons, signed DM roots, envelope v2, capability records, admission
  dedupe, and holds every conversation question/task (nothing runs). Root review defects R1, R2, C1, C2 and the
  frozen-person deferred send are fixed (`9f5bea1`, `5da7fa1`, `7247759`); root accepted `7247759` with focused
  race tests, closing the core correction gate. Agent participation and execution, Zoom's links and the browser
  device were built since (items below), device linking too (MEL-433 item below); not built: groups.
- **Human DMs on the page (`4a0659c`, accepted after Agy's review)**: person setup by hand, people listed by their claimed name until checked,
  separate persistent DMs, a message-only DM composer with receipts, held questions/tasks (nothing runs), frozen
  DMs; apart from device history. `9c1d4b4` (candidate, review pending) adds people search and DMs in Comic and
  Zoom. Agent invitation, Zoom's person–agent links, browser DMs and device linking were built since (items
  below).
- **Release gate (owner request, 2026-09-29)**: a whole-team pre-release review of a stable candidate SHA
  (inventory, per-area and cross-area review, one or two challenge rounds, fix and recheck per confirmed defect,
  Codex synthesis, only genuine product decisions to Sergey). Rows R1–R12 are listed in
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status); the working checklist is root's temporary
  `/tmp/agentnet-release-review-20260929.md`. Nothing is released until each outcome is verified or explicitly
  excluded by Sergey; the inventory is not approval.
- **Notifications required for this release (owner, 2026-09-29)**: "it's messenger! it's supposed to notify
  people. optionally, of course." Optional notifications for ordinary messages on desktop and mobile, also with
  the app or page closed; opt-in only (no permission request on page load; denied or off leaves a fully usable
  messenger); no polling, no plaintext to the relay or a push service, no model calls; mutes and click to the
  conversation; platform limits stated plainly. This supersedes the S-P deferral (release row R13). Contract
  [`docs/revival/NOTIFY.md`](revival/NOTIFY.md); built in local commits (Hub and browser device, daemon desktop
  alerts and page controls; evidence and what is not shown are in the next item and in
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status)).
- **Agents in DMs (2026-09-29, local commits)**: core records and execution (`1c35ad4`, `fc87c3f`, `85af6a8`, accepted
  for UI use; assembled/platform gates open); daemon pages invite, decide (host only), ask, dismiss and act on requests
  (`78b172f`, `fa9ccc0`), with people linked to their agents in Classic/Comic/Zoom (`3c61430`); the browser device
  invites, asks and dismisses the other person's agent and never runs one (`061faaf`). See
  [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status). Notifications ([`docs/revival/NOTIFY.md`](revival/NOTIFY.md)):
  Hub side `a83aa4d` and the browser device's side (`6adc18d`, `63f1ac9`) built locally; one real desktop Chrome 154
  (Linux) implementer-observed pass through Google's push service showed opt-in (permission answered by the test),
  presented suppression, closed-page alerts, click to the exact DM, per-DM mute and off (loopback, synthetic; no
  provider 2xx log). Desktop daemon alerts: core `a85b84c`/`99b2559`, page controls `cd54168`, one Linux pass (native
  banner, suppression, mute, off; click route opened directly; a click reaching an already open tab is taken too,
  `81792ef`). Windows clicks: core/root `b08e65b`, `ab440c7` (their evidence). Not shown: deployed HTTPS,
  phone/iPhone, other browsers, macOS clicks.
- **Files and pictures (MEL-489, local commits)**: core `e6a3048`, `960ceda`, `423e937`; pages `8e1c64b`, `6ddb450`,
  `fdca26f`; browser gate `2a86600`. DMs on both pages and device conversations (v1) on the daemon's page: choose,
  paste or drop, pending name/size/remove, explicit Send (file-only allowed), per-conversation drafts in all three
  views, limits said first; received pictures (PNG/JPEG/GIF/WebP by their bytes) shown in place, anything else only
  saved under its safe name; agents are told files exist, never given them. Evidence: Go/browser interop both ways
  (tampered blob and lying manifest refused, hostile names), provider/page tests, one real Chrome journey on loopback
  (both pages, offline, reload, back online, phone width, second tab). Not shown: a phone's picker, a deployed relay;
  unsent draft files live only in the open page.
- **Remind me later (S-R, local commits)**: core `ac517b5`, daemon page `0474bda` (set/move/done/cancel on received
  messages, overdue first in the list and Zoom); one Linux pass: banner at the chosen minute, due shown live, a reply
  ended it. Not on the browser device.
- **Browser device (2026-09-29, local commits, review in progress)**: `hub serve --web` serves the page at the Hub
  origin (`7a97aaf`), `admin invite --link` prints browser invite links (`3a588c3`); the human-only browser engine,
  plain join page and installable manifest are `6061c11`, `9ecd3c8`, `2d05b2c`, `e971c16`, `1cab993`. The app icon
  is the owner's chosen ant on a light tile (`ae7f7ea`: favicon, header mark and the installed app's icons, both pages). Verified: Node journeys against a real
  test Hub, the opt-in real IndexedDB test (`AGENTNET_CHROME=google-chrome-stable go test ./internal/ui/static`), a
  real Chrome 154 journey on loopback including install and standalone launch. Not verified: deployed HTTPS relay,
  phone, Firefox, Safari. See [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Owner decisions for two-person DMs (2026-09-29)**: either person may dismiss an invited agent; selected earlier
  DM messages may be shared with an agent, the choice visible to both people, with no separate approval from the
  other person; the host's acceptance stays explicit. See [MESSENGER_ARCHITECTURE.md §9.2 and §15](MESSENGER_ARCHITECTURE.md).
- **Next release scope (recorded; v1 contacts/conversations navigation, search of known agents and
  conversations, separated review reports and the People directory are implemented, the rest not)**: S-W relay-served HTTPS browser/phone,
  a People directory with event-driven presence (online = daemon connected, shown only while this daemon
  is connected to the relay; listing grants no trust; choosing a member opens a draft, never sends), Pi native attention (built for Pi 0.87.1:
  `2479a7e`, `4b1c7a2`, `587a1d5`; evidence and limits in [M4](revival/M4.md)), and contacts and conversations (reviewed
  direction, corrected by the owner: one row per exact address holding separate conversations,
  never merged; grouped remote notices; no remote approval), and one search for agents and
  conversations (kinds shown, opens exactly the chosen item, grants no trust; people are not searchable yet). See
  [MESSENGER_ARCHITECTURE.md §16](MESSENGER_ARCHITECTURE.md#16-a5-rollout). Chrome install: the relay page's
  manifest is built (`1cab993`; the ant icon since `ae7f7ea`) and was installed and launched in Chrome 154 on loopback, not
  on a deployed relay; S-R reminders unchanged. One-command `agentnet update` (latest stable,
  same home, daemon and page switch safely): in source, not released; Windows switches only a daemon
  started by the scheduled task `\agentnet` (native test passed in Actions run 36458378377; a standalone
  daemon is not stopped, an explicit limit)
  ([§19.2b](MESSENGER_ARCHITECTURE.md#19-updates-and-versions)).
- **Human Identity & Multi-Device Linking (`MEL-433`, local commits, review pending)**: one person on several devices,
  a clean cut to person v2 (re-enrollment, no v1 salvage). Core (core Claude): roster chains, own-device links (one QR,
  approved on an existing device; no admin invite per device), fan-out to every device, history snapshot and
  forwarding, files of history on request (`886afe1`, `80f9717`, `ef125db`, `4ea4f92`, `efeb9aa`, `dbb640f`); CLI join
  with a link (`c2d34a1`, root). Pages (frontend Claude): setup as a person or a service, "You on N devices" and each
  person's devices behind one closed disclosure, add/approve/remove devices with a QR, where each message came from,
  history progress, history files (`8201b6c`, `0f1026d`, `cd4702f`, `3a0af06`, `9616b1e`, `63ad48e`, `fc897bc`,
  `d94b84c`); the browser device speaks person v2 and links either way, copies and keeps chats and files
  (`a94b3d2`, `c050417`, `77dd232`, `63ad48e`) and writes to devices again (v1, `f5be650`; owner's Android report).
  Evidence: Go/browser tests against real Hubs and daemons (browser as new and as approving device, history,
  stale-roster forwarding, files after the Hub dropped them, device questions with a stub responder); one sole-tab
  Chrome journey at phone width on loopback (link, CLI approval, history and its files, Zoom device chat, DM after).
  Not shown: a phone, a deployed relay, the browser approving a real second browser; recovery of a lost last device
  is not built. See [`docs/DECISIONS.md`](DECISIONS.md) and [MESSENGER_ARCHITECTURE.md](MESSENGER_ARCHITECTURE.md) (status).
- **Comic Avatars & Visual Expressions (`MEL-434`)**: Avatar facial emotion requirement agreed (sender emits emotion with turn; cached predefined reaction set; no extra per-message model call); asset generation, emotion vocabulary, art direction, and implementation deferred.
- **Notification Click Focus (`MEL-435`)**: a click opens its conversation: the browser device's service worker, the daemon's Linux alert (`#conv=`, also in an open tab since `81792ef`), Windows (`b08e65b`, `ab440c7`); macOS alerts have no click.
- **Owner requests for later (2026-09-29; backlog, not this release)**:
  - custom visual plugins, [MEL-475](https://linear.app/mellanni/issue/MEL-475/secure-custom-messenger-ui-plugins):
    each user bundles presentations like Classic/Comic/Zoom; isolated, least permission, no secret access or silent
    export, core keeps all authority; architecture, API and package format undecided;
  - editing sent messages, [MEL-476](https://linear.app/mellanni/issue/MEL-476/edit-sent-messages): visible edited
    state, authenticated sender revisions synced to offline devices, never a silent rewrite or rerun of accepted/run
    work; edit windows, history visibility, attachments and conflicts undecided.

  See
  [MESSENGER_ARCHITECTURE.md §20](MESSENGER_ARCHITECTURE.md#20-owner-requests-for-later-backlog-not-this-release).
- **Live Daemon Web UI Integration (`MEL-429`, slice S-A)**: `agentnet daemon --ui 127.0.0.1:0` serves the
  messenger page over this home's real inbox from the daemon that owns the home; `agentnet ui` prints its
  address (token kept in the owner-only `HOME/ui-url`, never logged). Classic, Comic and Zoom presentations
  share one Provider and the same decision dialogs. Threads are derived v1 reply links;
  every action is an existing `Agent` operation with its CLI gates; changes are pushed (store writes and
  the local kick socket), never polled. It is a page on this computer only: no relay hosting, phone or
  browser devices, groups, or push (S-W, S-C, S-P and later); human DMs (v2), files (MEL-489), the browser
  device and notifications were built since (items above).
  Tests: `internal/ui`, `internal/client/uiview_test.go`, `itest/ui_test.go` (two synthetic homes, real binary).
- **Group Messaging & Federation**: Deferred from initial slice.
- **Automated Task Execution**: Tasks run only after `agentnet accept ID` or under a local, per-key task grant (`approve --tasks`, `accept --always`; see docs/revival/M4.md). No other path may run a task: nothing received, no name match and no policy engine grants it.

### 6.3 Team Roles & Next Slice Design Scope
- **Human Owner & Caller**: Sergey (final authority on product scope, security rules, emotion requirements, and release approvals).
- **Supervisor**: Codex (`p1`) (architecture, Linear project coordination, verification reviews).
- **Critic & Systems**: Agy (`p4`) (code reviews, documentation audits, system verification, hermetic testing).
- **Stopped Workers**: Core Claude and UI Claude were exited at the owner's request. Pane identifiers above describe this session only; discover current agents before delegation. Do not relaunch them without a new assignment.
- **Next Slice Design Scope**:
  - **MEL-433**: Human identity, linked devices, and shared conversations for messenger rollout.
  - **MEL-434**: Comic-book conversations, expressive avatars with required facial emotions (`happy`, `sad`, `curious`, etc.) distinct from operational status, and cached predefined reaction sets.
  - **MEL-435**: Deep-link notification click focus to exact conversation without task side effects.
  - **MEL-429**: Live daemon integration for lightweight messenger UI.
