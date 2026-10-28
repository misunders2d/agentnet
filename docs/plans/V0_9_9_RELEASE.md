# v0.9.9 corrective release — MEL-709 / MEL-701

Status: implemented; focused/native review passes; full qualification pending. Root owns this checklist,
integration, publication, deployment and clean handoff. review_patch alone writes
MEL-709's internal/client Go changes; review_history alone writes MEL-701's
disjoint Store and focused UI regressions. Neither edits the other's files.
Participating verifier reviews the stable candidate and exact public payloads.

Owner explicitly requested an immediate new release fixing MEL-709 and including
MEL-701. Existing native permissions, signed/encrypted synchronization, original
identity/topic/attempt, no replay and newer unread generations stay authoritative.
Latest inspected published app is v0.9.8; verify again before choosing the tag.

| ID | Required outcome | Acceptance evidence | Owner | State | Evidence |
|---|---|---|---|---|---|
| T1 | Exact native steering reaches and changes the active admitted run once | Owned native Codex consumption plus focused direct/group and completion/cancellation race regressions | review_patch + verifier | implemented | Native Codex53.46s and focused direct/group checks pass; full qualification pending |
| T2 | Absorbed corrections acquire truthful terminal/result linkage | Success, cancellation, failure, uncertain acceptance and interruption; no blanket answered state or cross-attempt completion | review_patch | implemented | RED cancellation regression; terminal/reaction/manual/attempt cases and corrective review pass |
| T3 | Stuck shipped records have safe supported recovery | Exact legacy fixtures; no replay/manual SQL rewrite; truthful disposition of four identified live records when safe | review_patch + root | implemented | Exact legacy fixtures and conservative recovery pass; four live records inspected read-only, recovery awaits activation |
| T4 | Linked Pending/status/attention converge and stay cleared | Existing signed updates, duplicate/reconnect/restart/stale-status regressions; newer work preserved | review_patch + verifier | implemented | Pending/attention projection and linked reader restart/stale/duplicate checks pass 2.662 s |
| U1 | Confirmed old reads stay cleared; unconfirmed reads report failure and recover | Actual Store rejected/timed-out read, scoped warning/retry and no false clearing; real read/restart/import/read-sync with unseen/newer unread preserved; retained latency/Review evidence | review_history | implemented | Actual Store 12 modes and React/Chromium1280/390 plus canonical build/typecheck pass; original Zenbook 13 recurrence cause remains unverified |
| R1 | Qualified integrated candidate | Required applicable race/native, exact-pin skins, real mixed-version against latest older tag; retained evidence and skips explicit | root + verifier | agreed | Pending |
| R2 | Publish, deploy and leave clean | Immutable tags/checksums/public-byte checks, safe existing Hub rollout, Linear/handoff, clean synced repos/no stray worktrees | root + verifier | agreed | Pending |

## Current evidence

Live read-only metadata confirms all four MEL-709 corrections are still steered,
read and admitted as attempt 1. The older parent is answered with its stored
same-scope result; the other three share a cancelled parent. No live repair ran.
The new exact cancellation regression fails on shipped source (child stays
steered); the first candidate passes success/cancel/failure/interruption cases.
Legacy recovery and races remain under focused qualification.

MEL-701: the later zero was after the owner read everything. The 18:52 UTC batch
contains 14 rows (12 native, 2 imports), now all carrying exact read tombstones;
no V2 attention marks exist in this installation. Retained read-sync copies
start that minute, so earlier durable reads are not established. Isolated
13-row real-Live read/reopen/restart/duplicate checks pass; signed history and
older-reader checks pass separately. This is not a reproduced resurrection.
The actual frontend's swallowed read-save error path is reproduced and corrected.

The actual Store rejected/timed-out read regression now fails: the backend
remains unread, no acknowledgement is recorded and no error appears, including
after Back/visibility retry. This proves a source defect that can leave viewed
messages unread without warning; it does not prove the historical 13 incident's
cause. Post-ack refresh rejection already reports an error, so the correction
must distinguish it from read-write failure and suppress stale-workspace noise.

Native verifier resolved existing Codex 0.165.0 ELF and inherited Sol/xhigh
configuration. No install, account/config change or user session change; the
real native steering test passes 53.46 s with two ordered corrections and signed terminal links.

U1 focused candidate: actual Store reject-open, timeout-after-Back, reopen and
Android visibility retry, stopped/disconnected/stale host, confirmed newer view,
newer unread, wrong author and duplicate-error checks pass. The test fails on
the exact baseline (expected warning 1, actual 0). Actual React/Chromium at 1280
and 390 widths shows failure-after-Back warning in 34/10 ms, retaining the Review
row until retry's fresh view proves read. Reopen clears only confirmed reads;
previous retained navigation measures 205/232 ms and confirmed return 176/36 ms.
The canonical builder supplies generated shared-module staging before tsc;
raw-tree tsc alone lacks that staging and is not authoritative build evidence.

Do not change execution authority or manufacture completion from acceptance.
No user desktop/native session may be closed or restarted. No live read-state
clearing or database repair is authorized merely to make Review empty. Record
verified causal evidence before repairs; prefer source transition plus bounded
recovery over a new public operation.

Final focused checkpoint: vet passes. Current candidate single-case reaction
history race passes 41.19 s; exact released f23 baseline passes 39.59 s with the
same test bytes/command. Prior suite failure remains unattributed. No deadline
was extended, source guard weakened or race warning ignored. The initial full
focused run had an obsolete post-completion steered assertion, corrected to
stronger exact answered/result/no-replay checks. The affected race passes all
changed cases; linked status/Pending assertions are integrated as regressions.
