# Messenger fixes after the Play Store pause

Release completion, October22: [v0.9.2 is published](https://github.com/misunders2d/agentnet/releases/tag/v0.9.2)
and the existing relay is healthy on it. The qualified final source, full retained
CI coverage, matching gallery, artifact verification and preserved-data cutover are
recorded in [V0_9_2_RELEASE.md](V0_9_2_RELEASE.md). Play remains paused; no live Drive
connect or move was performed. The scope and failure notes below are historical;
they do not describe the completed release gate. Physical/live acceptance and
unrelated backlog remain separate.

Owner direction, October 21, 2026: save Play Store work for later and continue
the other bugs/features. No release, deployment or live attachment migration.
Root maintains this checklist; existing workers retain bounded file ownership.

October22 release update: the owner superseded the regular-release hold and
authorized app publication and existing-relay deployment, with Play Store later.
Continue in [V0_9_2_RELEASE.md](V0_9_2_RELEASE.md). The seven local timing failures
recorded here also pass actual CI run39577187806 on9133893b, independently
verified by root; they are not proven product defects. That historical CI run
is not current-candidate qualification. Retain this local record and run the
isolated regular candidate through the real CI gates without weakening fixtures.

The Play checkpoint is
`.agentnet/checkpoints/2026-10-21T153550Z-play-store-paused/` (source archive,
tracked patch, per-file hashes, unsigned pre-D2 AAB and evidence). Its source
archive was extracted in memory and every saved file hash verified. Preserve
the two owner-authored Android-notification and mark-unread drafts.

| Target | Observable outcome | Owner / verifier | State | Evidence |
| --- | --- | --- | --- | --- |
| N1 — MEL-677 relay Drive | Admin alone connects one relay-wide account; encrypted future uploads and explicit existing-file moves retain access, crash recovery, verified custody and portable backups | existing backend/UI/adapter workers / root / original Claude guard | focused race/vet passed; independent review accepted | D2 gdrive/Hub race passed in 8.080s/21.879s and affected vet passed. Corrupt resumed-remote failure was an invalid fake Drive numeric size; documented string size fixed with all assertions/deadlines retained. No live consent/migration |
| N2 — today's UI/reminder/identity reports | Reconcile MEL-550, 670–674 against source, acceptance and retained evidence; repair actual remaining gaps instead of repeating accepted work | root / existing bounded workers | archived gap implemented; focused/integrated checks passed | Exact archived snapshot and revision-race negatives pass native/API/browser/Store checks; canonical build and original Claude review passed. Earlier qualified UI/reminder/identity evidence retained; installed v0.9.1 is distinct |
| N3 — earlier bugs/features | Exhaust current Linear inventory, distinguish stale status from missing code, identify next concrete regressions and dependencies | root / existing bounded workers | inventory/audit and joined topic plumbing verified | Two issue pages exhausted; MEL-665 implementation retained, MEL-617 actual native/report/link/Undo plumbing passes. Incident correlation, real-model selection and physical/live acceptance remain explicit limits |
| N4 — integrated verification | Cheap discriminators and fixes first, then affected integrated checks once stable; retain valid unchanged evidence and report explicit skips/physical limits | root / original Claude guard | accounting complete; full race gate FAILED | Final: client1377 pass/25 skip/seven fail,zero missing of1409; UI369 pass/137 skip,zero missing of506; command205/2 and integration36/3 pass/skip. MixedVersion actually PASS76.32s; final vet passed. All seven retained failures also reproduce on committed e26c772c. AssistantReaction and AfterHostReadmission passed isolated candidate races; earlier failures retained. No release gate pass claimed |
| N5 — MEL-678 literal database paths | Writable databases, upgrade snapshots and three local read-only helpers open the exact owner-only path when filenames contain URI characters | root / original Claude guard | required sibling correction complete; affected races passed | RED reproduced truncation/options/decoded-separator failures and read-only alias creation. Shared FileURI fixes all five paths. Full adapter suites passed before export (modernc16/CGo19 parents); final focused writable/read-only races pass on both adapters, zero failures/skips. No schema/lock change or physical-platform claim |
| N6 — MEL-617 organized topic name | The requester sees the name intentionally supplied for the admitted agent's new topic without promoting a private label | root / existing topic worker / original Claude guard | explicit-intent correction implemented; focused/race/render checks passed | Only the new-topic phrase from request.New plus explicit title supplies the fallback. Generic legacy names are rejected. Affected normal/race parents pass, with the existing-destination fixture causality correction below. Final desktop/phone joined render passes8.482s |
| N7 — MEL-679 manual notice privacy | An outgoing manual move with no explicit title never copies the sender's private destination label | existing topic worker / root | producer correction implemented; focused and affected race checks passed | Native and Engine RED reproduced the implicit private lookup. Native two parents/two subcases and Engine97 checks pass; affected client/UI race packages pass19.292s/59.751s. Explicit supplied titles and viewer-local rendering remain unchanged; no protocol/schema/auth change. Evidence: .agentnet/oct21-topic-acceptance/human-private-title-evidence.json |
| N8 — MEL-680 editable reminder date | A custom date stuck on year0012 can be replaced directly; calendar remains available and invalid dates never save | root / original Claude guard | direct-date correction implemented; focused build/race/render checks passed | Owner could not correct the native segmented year. Primary YYYY-MM-DD text field plus native calendar option; no dependency. Canonical build/types and static races pass. Comic rendered32.724s and rebuilt Office light/dark33.403s pass with keyboard recovery, malformed/impossible dates, calendar fallback and all earlier time/DST/no-send/reopen/cancel checks. Original native sequence/physical GTK remains unproven |

The current Linear retrieval exhausted both pages: 195 non-archived project
issues, 105 with open statuses at retrieval. This is tracker state, not a count
of missing implementations. The local source inventory is
`.agentnet/oct21-non-play/linear-inventory.json`; the newly created MEL-677/678
are tracked separately above. Many older tickets deliberately retain live,
physical or incident-correlation acceptance limits.

MEL-674 has a proven remaining archived non-latest thread aggregate gap. Its
bounded correction adds an optional exact-peer/agent archived-count snapshot to
the existing thread response and reuses confirmed-read revision fences. The
original Claude design guard accepted this as the cheapest exact path and cut
the proposed duplicate group total/model change and separate snapshot map.
No extra endpoint or polling. Independent-arrival/revision negatives pass.
Native/browser response/type and Store changes are frozen: client focused
checks passed in 0.302s, UI/API/browser parity in 1.133s; the final Store fixture
has 32 passing groups plus four affected Store harnesses. Metadata is omitted
if the revision changes during assembly: the negative fixture reproduced why
publishing a newer aggregate under the earlier sequence is unsafe.

The final same-conversation Claude code guard accepted N2 and N5 with no
blocker. Its required three sibling read-only corrections are included and
race-tested on both SQLite adapters. It ran no tests itself and inspected only
Engine hunk headers; actual browser parity comes from the executed fixtures.
Optional unobserved transient mixed read records and a pre-existing legacy
blank-host grouping difference prompted no speculative code.

Current source hashes, command/log references and qualification limits are in
`.agentnet/oct21-non-play/candidate-evidence.json`. Full-client inventory and
per-partition runs are under its `qualification/` directory; unfinished runs
are not passing evidence. The original inventory had 1,406 client
parents. Both Play and non-Play changes remain locally preserved; this is not
a published or deployed candidate.

MEL-665's code qualification remains valid; excluded live owner-chat
consolidation is not being executed. MEL-617's useful next evidence is one joined
native stand-in action/report/link/production-UI Undo journey; this cannot prove
a real model's semantic selection. MEL-583's whole-topic cross-chat continuation
is still deferred and is not being silently folded into that test.

The joined deterministic native/Comic journey passes at 1280/390, including
the actual staged-file read, exact native action retry/report link, narrow grant,
real Undo/reload and both participants' exact original identities/work stamps.
Its two original rendered failures were unsupported layout-selector assumptions;
the exact destination rows and backend placement prove navigation without
requiring an optional inline topic chip. Logs and screenshots are retained in
`.agentnet/oct21-topic-acceptance/`. This is plumbing proof, not real-model
semantic selection or live owner-chat acceptance. It exposed N6's actual naming
gap. A further guard exposed a privacy problem in generic legacy notices:
they can contain the host's private local label. A proposed creation inference
from currently held messages was rejected with three RED counterexamples
(earlier destination content exists, moved away, or absent from granted history).
The smallest correct fix publishes a distinct human-readable new-topic phrase
only where explicit New intent is known. Recipients decode only that phrase,
never generic legacy bodies. New existing-target agent moves no longer look up
private local names. Explicit private/shared names retain existing precedence;
no extra query, schema, protocol field, capability or register change. Old
generic history/older hosts retain automatic names. Internal closing quotes
remain valid. N7 closes the same implicit-copy bug in manual producers while
preserving explicitly supplied title behavior.

The final N6 AdmittedRun race initially timed out in the new privacy assertion.
Both readers had received identical moves, but the stopped host had missed the
requester's earlier Undo events: its reused source-topic precondition was stale,
and the requester correctly ignored that compare-and-swap move. The privacy
check now uses a distinct ordinary original explicitly included in the initial
grant; the prior omitted-source, Undo, revision, cutoff and deadline assertions
are unchanged. The exact parent race passes both subcases in40.276s. Diagnostic
RED and final hashes are in `.agentnet/oct21-topic-acceptance/causality-evidence.json`.
No product change was justified by that fixture failure.

## Retained verification chronology

The first full client invocations at the unchanged 600s package bound exhausted
their aggregate budgets. Passing parents are retained; exact unfinished cases
will be continued after collecting the batch. Six original client parent
failures and one Hub stream failure were kept and rerun at unchanged bounds.
All now have passing quieter evidence; the last history catch-up case passed
alone in49.24s after also failing in a sequential selected batch. No product
history patch is justified by that evidence. The broad test
orchestrators were subsequently terminated with signal143 at about19:26:39,
leaving no owned Go test processes. Completed client parents/package results,
partial logs and failures are retained; the cause of termination is unproven.
The attempted launcher-spacing probe failed before sending any signal.
Required UI partitions did not start. Finish the newly discovered naming fix
and focused failure diagnosis before rescheduling remaining qualification.

Later qualification is retained separately: client6 completed all78 parents;
client8 completed60 passes and one setup failure, leaving two started and15
not started at its600s package bound. UI0 completed77 passes and38 explicit
opt-in skips before its600s package bound; UI1 and client9–17 were unstarted
at that checkpoint and are now scheduled as continuations of the original matrix.
Client7 exposed two additional linked-device/history waits. An isolated race
packet reproduced all three current failures in223.191s: RetiredTaskDevice,
GuestDerivedParent and OwnPersonTasksDirectAndGroup. Normal-test passes do
not qualify race coverage. Failure-only ledgers show staged/queued source
history, an earlier queued archive and no export error; one recipient already
has current group context and proofs, while the other lacks group context.
The guest failure occurs during a roster-chain request in setup. A single-parent
profile measured substantial race-instrumented SQLite preparation costs, without
proving a lock cycle. The same-Claude guard recommended one four-core comparison,
with every deadline/assertion unchanged: all three still failed (94.15/58.12/99.89s),
despite591.78 sampled CPU-seconds in252.18s. The guest passed the traced invitation
and failed later in acceptance; the caller budget covers several requests.
Committed HEAD e26c772c, extracted without changing this tree, also fails
RetiredTaskDevice at the identical wait in92.34s. That proves this failure was
not introduced by the current uncommitted changes; the other two remain
unattributed. Baseline/normal logs are excluded from candidate race coverage.
Stop per-case reruns and speculative product changes; continue the unfinished
original matrix in two Go lanes at GOMAXPROCS2, collecting failures together.
No history timer, SQL framework, deadline increase or assertion removal was added.

Play packaging/signing, store/legal compliance, Play-only consent, reviewer
provisioning and native-push architecture remain parked. Shared files can be
changed only for the active messenger fixes, preserving the saved Play work.

Mandatory independent guard remains the same Claude conversation through
Herdr: `c86bd851-a8b5-43cf-b989-27cd6ac81faa`. Extra Claude coding workers may
use Opus 5.5/xhigh as authorized; native workers remain explicitly
GPT-6.1-sol/xhigh. Do not read AgentNet inboxes for this session.

At20:13UTC, the root matrix lane is held after its current Go batch exited
naturally; the second lane is also held for focused isolation. This is an
intentional orchestration hold, with no Go test signaled. The orchestration
script's repeated log scans were corrected before isolation. Command and
integration continuations have complete parent accounting (205/2 and36/3
pass/skip); UI has368 passes,137 explicit skips and one unresolved parent.
Client accounting is1133 passes,21 skips,15 failures and240 unfinished parents.
Every original failed invocation remains retained.

The legacy-asker proposal failure had an invalid fixture assumption: AskAgent
wakes a current daemon which republishes its current capabilities. A forced
republication RED returned the exact logical request ID, proving the capability
representation choice rather than wrong-message selection. The final setup
uses the existing future-dated signed advertisement pattern plus verified
legacy/current observation barriers. All original physical/logical reference,
confirmation and execution-count assertions remain. Normal1.872s and race36.558s
pass; that focused worker inherited the host CPU default rather than the matrix's
GOMAXPROCS2. Evidence is qualification/proposal-legacy-caps-evidence.json.

The remaining UI parent fails alone at GOMAXPROCS2 after its shared45s setup
context expires, while the local durable-save and2s/1s delivery/response checks
pass. No HumanOnly or deadline edit was made; a single four-core isolated
comparison is running. The dismissed linked-history file parent passes its
normal diagnostic in7.284s; this is not race qualification. Its failure-only
producer job/source check changes no admission rule. Three Google failures also
reuse cumulative20s/30s fixture contexts and fail before their named feature
assertions; no Google-provider defect has been established.

Baseline-proven open failures after the isolated packet

These are open defects, not regressions from this batch. They are not passes:
the full gate remains failed until they are fixed or the owner accepts them.
Comparison source: `e26c772c4c258d67f519ab18b691e04f6e0b37ec`, one Go process,
GOMAXPROCS4, unchanged fixture and package bounds.

| Parent | Baseline assertion | Proof log |
| --- | --- | --- |
| `TestGroupHistoryWitnessRetiredTaskDevice` | `groupparticipationhistory_epoch_test.go:453` | `history-phone-baseline-race.jsonl` |
| `TestGoogleReviewIntentRetry` | `google_review_test.go:198` | `remaining-three-baseline-race.jsonl` |
| `TestGoogleAdmissionAndDesktopExchange` | `google_test.go:233` | `remaining-three-baseline-race.jsonl` |
| `TestP6FixLocalAndNestedHarnessUseRealCLI` | `room_p6fix_test.go:590` | `remaining-three-baseline-race.jsonl` |
| `TestOwnPersonTasksDirectAndGroup` | `ownpersontasks_test.go:24`, phone current group (current source:47) | `remaining-unattributed-baseline-race.jsonl` |
| `TestRoomBusyAncestorCallback/named_hop` | `room_callback_test.go:270`, verified reply | `remaining-unattributed-baseline-race.jsonl` |
| `TestGroupHistoryWitnessGuestDerivedParent` | `groupparticipationhistory_epoch_test.go:453`, old inert history (current source:466) | `remaining-unattributed-baseline-race.jsonl`; candidate `guest-derived-separate-race.jsonl` |

Logs are in `.agentnet/oct21-non-play/qualification/`; SHA256:

- `history-phone-baseline-race.jsonl`: `099e80d5415f23d5d12da494ae1515aae884c15e0413c864c6dbcc6be7b346f7`.

- `remaining-three-baseline-race.jsonl`: `ff7dd31da15cc862bc1308dba089fb7f1ead2d7ee97f5ac7797136981c4a2852`.

- `remaining-unattributed-baseline-race.jsonl`: `842ed63e733c5ea459c05fbe3dcac981be794a6640e241a4789967bc846685ef`.

The same Claude guard accepted the final legacy fixture without cuts and the
single-process four-worker plan. After full accounting, the exact baseline packet covered:
`TestAssistantReactionGroupLinkedHistory`,
`TestGroupParticipationHistoryAfterHostReadmission`,
`TestGroupHistoryWitnessGuestDerivedParent`, and
`TestOwnPersonTasksDirectAndGroup`, plus the newly discovered
`TestRoomBusyAncestorCallback`. OwnTasks and the named-hop callback reproduced
the same assertions; both remain open as above. AssistantReaction and
AfterHostReadmission passed on the baseline. GuestDerivedParent failed later
than its candidate setup failure, so that is not yet a same-cause attribution.
The paired candidate packet gave AssistantReaction PASS51.82s,
AfterHostReadmission FAIL75.13s at old inert history, and GuestDerivedParent
FAIL46.58s at its fresh10s AcceptParticipation context. The latter did not yet
match the baseline's later history failure, so both uncertain cases received
the same-Claude bounded diagnostic review.

Final bounded disposition —21:29UTC, October21 (October22 locally): the guard
allowed at most three isolated races using existing observations. Only two were
needed. Enabling the already-existing failure snapshot for AfterHostReadmission
changed no assertion or deadline; that exact parent passed93.04s. Its earlier
failure remains resource-sensitive/unattributed. GuestDerivedParent then ran in
its own Go process and failed108.88s at old inert history acceptance, matching
the baseline assertion. Source history was still staged/queued behind an
uploading archive; the phone had the current context and both proofs but no
history item or archive job. This is not proof of a lost wake or failed release.
No product, SQL, framework or timeout patch was justified. No third run occurred.

Final accounting is1409 client parents (1377 pass/25 skip/seven fail),506 UI
parents (369 pass/137 skip),207 command parents (205 pass/two skip) and39
integration parents (36 pass/three skip), all with zero missing. MixedVersion
actually passed76.32s; final integrated go vet passed after the diagnostic-only
change. The full race gate remains FAILED because the seven baseline-proven
failures above are still open. No release, deployment or automatic test lane is
running. Play stays paused. Current hashes, final command metadata, retained
failures and both split-parent proofs are in candidate-evidence.json and the
qualification directory; paired-comparison-evidence.json records the bounded
attribution. No further unrelated root-cause work is included in this batch.
