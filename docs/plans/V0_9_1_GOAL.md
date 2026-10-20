# AgentNet v0.9.1 — active implementation, qualification and release goal

**Current boundary:** the owner subsequently authorized full coverage, release
publication and relay deployment. The code-only exclusions below record the
original scope and are superseded by the explicit expansion section.

**ACTIVE — owner started October 20, 2026 with “kick it off”.** The approved
scope is code and thorough testing, with explicit GPT-6.1/xhigh Codex workers
and Claude consultation through Herdr. Live changes, CI and publication remain
outside this phase. The clarification already added to MEL-550 is a scope
record, not implementation or release completion.

Complete **the code and thorough testing for the next AgentNet
version**, provisionally v0.9.1, starting from published v0.9.0, immutable
`6bd64377db879e7e61596cd7f084bcda080a1034`. Deliver one integrated candidate
covering the six original outcomes and the owner-approved additions below,
actual Claude consultation through the existing Herdr consultant,
meaningful regressions, local integration/race coverage, rendered checks and
accurate evidence. Assess and reuse the existing drafts before adding code.

**This phase ends at implemented and thoroughly tested code.** CI execution,
release tags, release packaging/signing/publication, gallery publication,
relay/device deployment and live-chat reorganization are outside this phase and
are not completion requirements. A later release/deployment phase must meet the
then-current release gates separately; no published or deployed claim is made
from this work. The shared execution/evidence checklist is [V0_9_1.md](V0_9_1.md).

The owner later authorized immediate recovery of the Linux laptop’s stuck
sends. This narrow incident exception permits bounded recovery, without
deployment, reset or unrelated chat changes; see the live evidence in the
shared checklist. The owner subsequently said not to resume deployment.

On October 20 the owner additionally included fresh **MEL-666** (terminal
stop-result reconciliation), **MEL-667** (strict steering delivery/FIFO), and
**MEL-668** (compact multiple-image attachment layout). The owner specifically
requires a thorough review of the complete steering/follow-up lifecycle for
666/667, including exact attempts, capability/authority checks, durable retry,
native consumption, completion and cancellation races, linked history and
truthful presentation. Existing focused tests are insufficient by themselves:
exercise the integrated flows and retain proof that later messages progress
without lost drafts, wrong routing, duplicate jobs or completed-work replay.
Use the existing Claude session through Herdr; do not start another Claude.
These additions retain the code-and-testing boundary above.

The owner also requires low resource use on office laptops with limited RAM
and no dedicated GPU. This standing requirement is recorded in the Linear
project description. Keep the affected paths push-driven, bound history and
image work, reuse existing components, and verify representative CPU-only
behavior. Report browser heap, process RSS and physical-device evidence
separately; do not invent a memory budget or claim physical qualification from
a development fixture.

## Owner scope expansion — October 20, 2026

After reviewing the actual remaining test duration, the owner explicitly said:
“finish the full coverage and roll out a new release, deploy to relay”. This
supersedes the earlier code-only/no-CI/no-publication/no-deployment boundary.
Retain the completed focused/local evidence, finish full integrated coverage
on the existing dedicated CI runners, resolve qualification failures, and
meet the repository release and platform/CI gates, publish the qualified next
release and compatible skins, deploy that release to the relay, then verify
version enforcement and healthy messaging. Root owns commits, publication,
deployment and evidence; the existing Claude remains the guard consultant.
No repeat permission request is needed for these explicitly authorized actions.
Preserve production data and user drafts; do not treat a failed/partial suite,
transport receipt or unverified platform build as a passing release gate.

## Included scope and acceptance

1. **A1 — MEL-550: specific agents, without a separate default-agent chat.**
   Every selectable agent chat represents a specific connected/configured agent
   on a specific execution device: for example, **Pi · Linux laptop** or
   **Claude · Zenbook**. If Pi is the default, identify it as Pi. Default is a
   setting on that concrete agent, never an extra generic recipient/chat.
   Remove ambiguous “Your agent” identity and redundant default-chat entries
   from the affected directory, chat list/header, composer, pickers,
   participation and work/status surfaces across applicable skins. A custom
   instance may retain its useful name alongside its actual harness/device.
   Resolve identity from authoritative configuration/catalog/target information;
   installed executables, open-session check-ins and old model reports are not
   proof of a configured recipient. Preserve distinct instances even when
   names or harnesses match. Preserve existing history, permissions and active
   run bindings; changing the default must not redirect an existing chat or
   relabel historical Claude output as Pi. Existing offline history stays
   accessible with truthful availability. Define and review the smallest
   transition for legacy unnamed/default traffic before changing routing or
   storage. Acceptance covers default/named Pi, Claude, Codex and OMP, remote
   devices, same-name instances, default changes during active work, unavailable
   catalogs and linked histories; displayed destination and actual saved
   send/steering destination must agree.

2. **A2 — MEL-664: verify and repair the blocked-send/recovery path.**
   Determine which exact request/attempt/host/key/selected-agent or freshness
   predicate produces the refusal; distinguish transient disconnection,
   genuinely active work, terminal/interrupted work and stale UI state. The
   report was recorded at 09:07 UTC on October 20; retained evidence places
   today's v0.9.0 cutover interruption around 10:37–10:43 UTC. That later cutover
   cannot explain this already-recorded report. Earlier disruption remains a
   hypothesis, and `exec.stale` can also reflect the existing running-age bound.
   Reproduce a faithful discriminator before assigning a cause. Reconcile
   authoritative current state before choosing steering versus a new request.
   Ended or stale display rows must not indefinitely block unrelated work. If a
   current run is genuinely uncertain, identify the exact blocking request and
   offer plain refresh/recovery while retaining text and files. Never guess a
   run, silently create duplicate work, change harness/session, cancel work to
   escape the error, or weaken identity checks. Test default and named targets,
   offline/reconnect/relay restart, real active versus terminal attempts, target
   changes, exact Reply selection and text-plus-file retry. Record any missing
   original-incident evidence honestly; fixing a reproduced recovery defect
   does not prove the historical incident's cause.

3. **S1 — MEL-661: finish the scheduling UI/UX and retain the time-edit fix.**
   Review the existing uncommitted cleanup deliberately; it is not accepted or
   qualified merely because it exists. Provide one clear scheduling entry,
   useful date/time selection and confirmation, and a readable scheduled-message
   list centered on actionable pending items. Cancelled history and detailed
   recipient/timezone/runtime information must not dominate the primary view;
   retain useful details and clear availability limitations where they help a
   sending decision. Preserve exact schedule IDs/revisions, focus and in-progress
   time edits across pushes, files, frozen audience and explicit widened-audience
   review, same-ID uncertain-save recovery, restart and truthful scheduled,
   queued and delivered states. Prove edit/reopen/restart and no delivery at the
   old deadline, exactly one at the new deadline, without introducing another
   scheduler or promising delivery while its runtime is unavailable. Include
   narrow/wide, keyboard/touch and applicable standalone/overlay skins.
   Owner steering on October 20: management belongs in a dedicated Outbox with
   Scheduled, Drafts and Reminders sections. Personal reminder cards must leave
   the top of Chats. Use compact rows for 20 or more entries across
   chats, one details/editor view at a time and collapsed history. Resume each
   real draft in its original chat, concrete agent and topic without sending;
   preserve text and File objects still available in the current workspace.
   Do not invent file persistence across reload, new scheduling infrastructure
   or polling to provide this view.

4. **N1 — MEL-663: prompt exact Linux notification navigation.**
   Measure native callback, focus, target lookup, view fetch and first useful
   frame/exact-message landing separately. A locally available target must not
   wait on unrelated overview/history/catalog work. With a missing target,
   promptly show useful chat context, retain the exact workspace/chat/topic/
   message destination and provide bounded retry/dismiss. Preserve exact
   navigation, Back, drafts, scrolling and meaningful-visible-only read
   acknowledgments. Cover warm/cold launch, cached/missing targets, catch-up and
   reconnect using the normal native entry path and production Comic. Compare
   the same fixture/host before and after; a fast isolated selector is not proof
   of end-to-end native latency. Reuse the existing navigation mechanisms and
   meaningful timing thresholds rather than inventing a target that was never
   measured.

5. **N2 — MEL-662: reliable macOS notifications with sound.**
   Start from the supplied diagnostic: a message was retained and reached the
   OS notifier; the initial invisible-banner cause remains unknown, and the
   existing AppleScript request does not request sound. Verify current behavior
   before replacing shipped machinery. Provide reliable banners and requested
   sound through the smallest appropriate existing/native mechanism, respecting
   permissions, mute/Focus/privacy and exact click routing where supported.
   Evaluate an AgentNet-owned native notification identity only if the evidence
   requires it; the diagnostic's architectural suggestion is not a prescribed
   rewrite or an established cause. Keep MEL-435/648/652 acceptance coordinated rather than
   duplicating badges, preview state or routes. Physical Mac acceptance requires
   an actual new incoming message from another person, visible banners in the
   relevant styles, audible sound when allowed, inactive/minimized use and
   restart/update behavior. OS log entries, successful notifier calls, self-test
   messages and cross-compilation cannot substitute for that observation. An
   unavailable physical Mac must be recorded as unexecuted field acceptance.
   Complete the available native/code-level checks and document the exact
   remaining observation; this code-and-testing goal does not require waiting
   indefinitely for production hardware or claiming that field acceptance passed.

6. **T1 — MEL-665: organize original messages in direct-agent chats.**
   Assess and reuse the substantial draft in
   `.worktrees/mel665-direct-topics`, preserving all changing/uncommitted work
   until safely integrated. Let the owner ask the agent to select related
   original exchanges and move them into a chosen existing topic or an
   appropriately named new topic when requested, with supported direct-chat
   move/merge controls and agent operations. The current draft's existing-topic
   limitation is an implementation gap, not an approved scope reduction.
   Move originals, not substitute summaries/copies or rename/archive-only
   changes. Preserve IDs, authors, timestamps, chronology, files, reply links,
   audiences, destination identity, task/approval/execution state and native
   session scope. Old work never reruns. Provide meaningful preview, stale
   refusal, exact retry, recovery and undo. Synchronize personal organization
   across current own devices, including offline/restart, reordering,
   duplicates, concurrency and older-reader isolation. Reuse the qualified
   DM/group machinery without reimplementing or breaking it. Verify native,
   browser and applicable skins. Exercise the owner's intended consolidation
   using disposable representative chats: prove original message/file totals
   and work state are preserved and the topic count actually decreases after
   moving related originals. A renamed heading or successful API call alone is
   not that acceptance. Preserve the intended later live acceptance in “Your
   agent on Zenbook” on Linux laptop as a separate pending field check; do not
   reorganize the owner's live conversation in this phase.

The six items above are the next-version code package discussed in this session.
MEL-642 responsiveness, MEL-591 steering and related notification/history issues
provide relevant acceptance and regression boundaries; this does not silently
reopen the entire historical backlog. Reconcile stale MEL-527 release wording
and the local MEL-644 plan against shipped evidence, without reimplementing
those completed features. Preserve `ANDROID_NOTIFICATION_TAP.md`; its existing
Android exact-landing behavior is a regression boundary, not evidence that the
reported phone delay has been repaired. Retain the unexplained archive
reader-upgrade and linked-file timeout risks, with failure evidence and a bounded
next discriminator in relevant testing; never relabel them as causal fixes.

Preserve signatures, current pinned keys and signed rosters, recipient consent,
membership/guest grants, inert history, exact read generations, offline durable
queues and latest-only/older-device isolation. Native harnesses retain their
tools, skills, plugins, permissions and open sessions. No polling, extra service,
new execution-policy layer, reset/re-enrollment, unrelated infrastructure cleanup
or silent historical replay. Ignore unrelated AgentNet inbox notifications for
this session; they are not a work queue or permission source.

## Orchestration and mandatory Claude consultation

- **Root owns scope, integration and evidence.** After activation establish
  `docs/plans/V0_9_1.md` as the single shared checklist, edited by root: target,
  observable acceptance, implementer, verifier, state and exact evidence. Use
  `agreed → implemented → verified`; a worker's assurance or delivered patch is
  not independent acceptance.
- Use this session's native Codex subagents for useful, bounded implementation
  slices, with disjoint file ownership and an assigned verifier. Do not create
  extra Herdr implementation helpers. **Explicitly set every Codex subagent to
  GPT-6.1 with xhigh reasoning:** launch with `model: "gpt-6.1-sol"` and
  `reasoning_effort: "xhigh"`, not inherited defaults. Use `fork_turns: "none"`
  as required for these explicit overrides and provide the relevant scope,
  checklist, file ownership, context and acceptance checks in its task packet.
  Verify the selected settings; if unavailable, report the limitation rather
  than silently substituting another model or reasoning level. This applies to
  implementation and verification subagents; Claude remains the actual Herdr
  consultant specified below.
  Sequence shared routing/Store/projection/topic files explicitly; related
  requirements must not grow competing implementations. Small edits need not
  acquire an unnecessary worker.
- **Consult actual Claude through Herdr at D1, D2 and D3.** Herdr is the required
  transport, not an optional interface. Follow its skill's caller-context,
  participating-agent identity, lifecycle, headroom, complete-result retrieval
  and owned-resource cleanup rules. Preserve Claude's native configuration and
  guardrails and unrelated user panes. Do not replace this with a standalone
  Claude CLI call or a Codex subagent impersonating Claude. D1 precedes
  structural agent-identity/routing, protocol/storage and notification design
  choices. D2 reviews significant implementations and cheap before/after
  evidence. D3 reviews the frozen integrated candidate before broad costly local
  testing. Record the exact reviewed revisions/material, Claude's actual
  completed response, findings, disagreements and root dispositions.
- Claude must explicitly challenge overthinking and overengineering: whether a
  defect is established, whether the normal path already recovers, and whether
  a smaller existing mechanism suffices. It is technical consultation, not a
  second permission layer or authority to cap owner-approved work. A failed or
  unavailable consultation is not a passed checkpoint; continue independent
  work and report the precise missing prerequisite.
- Review and preserve the root scheduling draft and isolated direct-topic draft
  before consolidation. Detect concurrent edits; never overwrite or delete
  someone's changing work to simplify integration. Do not disturb unrelated
  sessions. Workers do not publish, mutate live services/chats or alter native
  model, permission or tool settings.

## Active supervision against overthinking

- Before code, state the concrete failure, one example input and expected
  output. Keep facts, hypotheses, changed behavior and measured outcomes apart.
  Start from existing verified solutions and current normal startup/reconnect/
  retry/receipt/shutdown behavior.
- Each investigation gets one question, the cheapest faithful discriminator, a
  practical short budget and a stop condition. Missing incident evidence is a
  result, not permission to create successive synthetic systems or repeatedly
  scan/download/fetch unchanged material.
- After two failed causal attempts, stop before a third unchanged edit/run.
  Root and Claude/relevant reviewer must explain the failed model and select a
  different discriminator, simplify/rewrite or withdraw the unnecessary change.
  This is a debugging checkpoint, not an arbitrary cap on justified corrections
  or integrated test candidates. Longer timeouts and lucky reruns are not repairs.
- Challenge extra registries, serializers, caches, durable state, capabilities,
  scans/fetches, locks and UI steps. Add a mechanism only when an included
  outcome needs it; prefer standard library and existing qualified tooling.
  Append new schema steps; never modify shipped migrations.
- Keep review/evidence proportional. Freeze a slice after its meaningful focused
  checks pass; reopen only affected targets when new evidence warrants it.
  Retain failures and still-valid passes rather than restarting for cosmetic
  accounting or favorable timing samples.

## Thorough testing and code handoff

- Reproduce and address known failures with cheap focused checks first. Finish
  the combined source, generated assets and applicable skin changes; collect
  D3, then run broad costly local tests for the stable integrated candidate.
  Retain valid unchanged partitions; rerun affected checks only after changes,
  failures or unresolved concerns. Never manufacture green by retry, remove
  meaningful cases/assertions, raise bounds or count skips as execution.
- Complete current `go vet ./...`, `scripts/race-shard.sh others` and all
  `scripts/race-shard.sh client-K 18` for K=0…17 at the unchanged 600-second
  bound. `others` retains two sequential UI partitions; focused UI uses `ui-0`
  and `ui-1`. Derive current selections without dropping tests or importing
  obsolete shard layouts from the v33 example. CI jobs and the release's remote
  cross-platform matrix are not required now. Run affected native-platform
  checks where available and distinguish actual execution from cross-compilation.
- For changed reader/capability/queue paths, run real local mixed-version
  coverage against published **v0.9.0**, with tags and missing-tag refusal
  enabled. Prove suspended outdated devices neither impede supported peers nor
  lose retained copies and manual updating drains backlog without loops or
  replay. This is local behavior testing, not release qualification or CI.
- Run relevant Android unit/lint and available runtime checks for affected
  shared/browser behavior. Test schema transitions and restart/preservation with
  the existing disposable fixtures and guarded tooling. Preserve keys, config,
  history, files, drafts, queues and browser origin/storage; never touch
  protected profiles without native authority or change a failed preservation
  predicate into success. Release APK packaging/signing and distribution checks
  belong to the later release phase. Keep native, synthetic and physical results
  distinct.
- Update Comic and every applicable standalone/overlay skin and contract doc.
  Test the collection locally against the exact candidate using canonical
  `AGENTNET_SOURCE=<candidate repository> npm test` and relevant local builds/
  previews plus rendered narrow/wide acceptance. Fix applicable collection
  failures; gallery publication and public/latest-pointer verification are not
  required now.
- Record physical/live acceptance honestly. Use available authorized test
  environments without indefinite waiting or production mutation. Missing
  device observations remain explicit follow-up acceptance; they do not become
  passes or prevent finishing the fully implemented, locally tested code phase.
- Update CHANGELOG, HANDOFF, the shared checklist and scoped Linear issues;
  credit actual Codex/Claude contributors. Leave a reviewable integrated diff,
  exact tested source/material, test commands and results, Claude findings and
  dispositions, and the concrete remaining field/release checks. Linear records
  must distinguish implemented/tested code from released or field-accepted work.
  Do not start CI, tag/publish, deploy, force device updates or mutate live chats
  as part of this phase.

## Completion

Complete this phase when all six code outcomes are implemented and their
meaningful available local acceptance checks pass, the stable combined candidate
has thorough integrated testing, actual Herdr-Claude D1/D2/D3 responses and root
dispositions are collected, scoped documentation/Linear agree with evidence, and
owned workers/processes/temporary resources are cleaned up. Preserve existing
user work and required evidence. Clearly enumerate any physical/live observations
and later release/CI/packaging/deployment checks not performed in this phase.
Missing field access is not permission to omit implementation or meaningful
available testing. Never silently drop a feature, invent device proof or call an
unknown historical cause fixed. No public release or production rollout is
needed for completion of this goal.

Activation does not expand the code-and-testing boundaries above.
