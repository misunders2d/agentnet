# v0.8.33 — released October 17, 2026

[Public release](https://github.com/misunders2d/agentnet/releases/tag/v0.8.33),
[matching gallery](https://github.com/misunders2d/agentnet-skins/releases/tag/v2026.10.17.1)
and the backed-up existing-Hub rollout are verified. See the current
[handoff](HANDOFF.md) and [release record](plans/V0_8_33.md) for qualification,
owner dispositions and exact preservation evidence.

MEL-648/652 retain unverified physical/native badge and notification acceptance;
MEL-646/641/587 retain the measured-performance, missing-incident and unresolved
WebKit discriminators. Provider-dependent Thinking acceptance and unavailable
physical devices are recorded explicitly. No v0.8.34 implementation goal is activated.
MEL-656 is the deferred cloudflared cleanup; no unrelated infrastructure was changed.

The following sections are historical planning records, not current release status.

# v0.8.32 — historical approved implementation and release

Current scope and qualification: [V0_8_32.md](plans/V0_8_32.md); owner goal:
[V0_8_32_GOAL.md](plans/V0_8_32_GOAL.md). Starting release is published
v0.8.31. Selected work is exact Android notification landing, measured long-chat
responsiveness, natural direct-agent steering, deliberate unread, and bounded
coordination/WebKit investigations, plus MEL-647 all-status topic search added
by Sergey during implementation. Implementation and focused checks are in
progress; no v0.8.32 publication or completed release qualification is claimed.

Root orchestrates and challenges worker code. GPT-6.1-sol xhigh workers use
Herdr tab 3 and native automatic review; Claude independently reviews design,
significant code and the integrated candidate in tab 2. Stabilize focused
corrections before expensive gates. Projects and unrelated backlog remain out
of scope; affected ThinkPad and physical-phone acceptance remain explicit.

Non-blocking follow-up requested October 16: [MEL-648](https://linear.app/mellanni/issue/MEL-648/show-an-unread-notification-dot-on-the-app-icon-across-supported),
an unread dot/badge on native app icons across supported systems. Initial local
inspection finds a static desktop tray icon and no badge updater in the Rust
shell. Reuse native platform facilities when implemented; verify actual icon
appearance/clearing per OS. This feature must not delay v32 and is not claimed
implemented or qualified.

Non-blocking follow-up updated October16: [MEL-615](https://linear.app/mellanni/issue/MEL-615/preview-and-play-video-attachments-inline-in-chats), inline video previews/playback like Slack or Telegram. This updates the existing video request; earlier OS-only playback notes are historical. Keep external opening/download fallback, encrypted file handling and responsive chat. No v32 milestone or implementation commitment.

## v0.8.31 — historical approved scope

Current scope and evidence: [V0_8_31.md](plans/V0_8_31.md); full owner goal:
[V0_8_31_GOAL.md](plans/V0_8_31_GOAL.md). v0.8.30 is published and deployed.
Guest-owned agents, remaining chat/action latency, resilient social controls,
guest participation/reinvitation, native live activity, stale request state,
full-window skins, Windows sounds, narrow invitation decisions, intentional
quotes and unread correctness are selected. WebKit recurrence remains urgent
investigation with the existing owner-authorized carry-forward if unresolved.
Projects stays reserved for v0.9.0.

Root orchestrates, independently challenges and verifies; GPT-6.1-sol xhigh
workers implement in Herdr tab 3 and Claude consults in tab 2. Batch focused
fixes before stable integrated qualification. ThinkPad is offline; its actual
latency/crash acceptance is not inferred from local fixtures. Publication is
authorized after required gates and artifact verification.

The records below describe earlier releases and their evidence.

---

# v0.8.20 fixes and remaining verification

## October 11 group invitations, compact tags and passive diagnostics

The fixes below shipped in [v0.8.20](https://github.com/misunders2d/agentnet/releases/tag/v0.8.20) and are integrated into main.
[The release record](plans/V0_8_20.md) contains final qualification, publication
and existing-relay deployment evidence. No live grants or original tasks were
manually changed. Physical-phone latency and broad history recovery remain open.

**MEL-530:** Android's selected-topic own-agent invite failed because an
unrelated member lacked topic support. Native and browser invitations now
require that capability on the exact selected host. Every reader copy retains
its existing per-device topic/capability, key and admission gate. The browser's
group participation sender also retains unsupported reader copies instead of
letting them veto the selected host. Proof carriers use the existing tolerant
copy preparation; no new routing or authority model is introduced. Real host
incompatibility, changed pins, invalid scope/grants and revoked hosts still fail.

Native repro fails before the fix (0.524s); four focused races pass (61.264s).
Browser signed lifecycle and peer-tolerance races pass (17.013s; 573 and 54
checks). Regressions verify unrelated older/suspended readers, strict selected
host checks and delivery of the same sealed copies after signed capability
refresh. Independent cross-reviews are clear. Relevant topic-host hunks reuse
Claude's unmerged `next/audience-prechecks`; other changes in that branch remain
unmerged. This is not a claim that every human-invitation precheck was changed.

**MEL-502:** `@everyone` and named team selections stay one visible tag each.
The signed message text stores the captured exact references using the existing
mention-link mechanism; no wire/schema/permission change. Reloads and failed
target retries keep that snapshot, and agent targets deduplicate independently
of the display label. Removing a tag clears its hidden fallback targets;
stopping asks removes agent references without removing the human tag. Comic,
Classic, Zoom and legacy parsing preserve compact display. Independent review,
the locked Comic typecheck and bounded-parser/1,000-recipient checks pass.

**MEL-588:** a passive missing-context record appeared as “Chat sync needs
attention” below “Nothing needs you”. The rendered test fails on the previous
bundle (1.640s). Comic now shows invalid/proof-pending diagnostics only under
Settings → About → Technical details. Key-change and other identity warnings
remain in OKs. Reading either page performs no archive or trust mutation;
explicit archive retains its exact snapshot, failure and retry behavior. This
relocation does not establish recovery of the screenshot's missing context.

Integrated small UI races pass (28.893s), including actual Chromium at 390 and
1280 px: passive notice location/archive failure/retry/reload, existing held
turn actions across all three skins, device Details and remote Needs-you card.
The remote-card fixture still logs its known unstubbed read-only 404 routes;
its asserted card host, reason and exact action bindings pass. Combined Comic,
Classic and Zoom assets were rebuilt. Passive-detail checks across all skins
pass (6.65s), as do grouped flood/archive and topic-invite checks (13.384s).

The actual 1,000-member rendered case then exposed a Classic/Zoom draft-restore
bug: reload retained exact mentions but lacked the legacy single-agent field,
so restoring “no agent” stripped the captured agent refs. Both now reconstruct
the exact selected participation from intact saved mentions before restoring
the composer. A removed participant stays an unavailable exact target, never
a replacement selected by name. Independent follow-up review is clear. The
ordinary multi-agent fixture also needed an exact text selection instead of
visual-line Home/Delete after owner-qualified labels wrapped on phones.
The next rendered run also exposed Zoom's durable preview showing the raw
collective link and a pre-existing exact-recipient ambiguity when deleting one
of two identical agent labels. Preview formatting now handles collective links;
mention editing uses the caret to identify the actual edited range. Independent
review required conservative handling of unknown/IME edits too: ambiguous
references must become plain text rather than retain a guessed recipient.
Final focused collective/ordinary multi-agent races pass (45.867s): actual
1,000-member groups in Comic, Classic and Zoom at 390/1280 px; compact draft
and durable history display; named-team edits followed by full reload; exact
frozen references, overlapping picks, attachments, failed-target-only retry,
deletion of equal labels and inert received mentions. Pure source-function
checks cover unknown/IME ambiguity, unchanged edits and unavailable restored
targets. All three final builds/typechecks, embedded asset checks (1.160s),
`go vet ./...` and whitespace checks pass. Independent review cleared the final
corrections. Earlier unaffected evidence is retained rather than rerunning
complete suites. Full integrated release/platform qualification and the
installed physical-phone journey remain before any published-release claim.

## MEL-595 remote Needs you card and MEL-558 history source repair

After v0.8.19, the owner reported a default-agent question to Bezos whose
Needs you card named Zenbook and hid the explanation already present in the
authenticated owner report. Fix branch: `fix/mel-595-remote-needs-human`.
Reuse the provider's execution/continuation host and the newest matching
report item (request ID, requester key, attempt, kind and state). No new
transport, permissions, job retry or live-data change is involved. This fixes
the card; it does not repair the native agent's reported memory-access denial.
The model regression fails before the fix (Bezos becomes Zenbook). The updated
Comic build/typecheck and focused model/rendered race tests pass (3.950 s), with
actual Chromium at 390 and 1280 px and unchanged exact Reply/Resolve bindings.
Independent read-only review is clear. This change is not yet released.

**Owner follow-up, MEL-558:** verify and repair the remaining source-side
deferred/blocked history records; do not purge messages or relax validation.
A private read-only snapshot is being used for diagnosis. The current app is
v0.8.19; Android phone 3, Bezos and Linux laptop are connected, while the old
Android phone 2 browser identity is offline. Its 212 custody copies are not
evidence that the current APK is disconnected. Deferred counts also include
backpressure and in-transit originals, so “waiting for context” overstates the
known cause. Move internal history counts/progress under device Details; show
no invented approval or user action. Do not revoke the old browser identity
without the owner's specific instruction.

Device presentation is implemented and reviewed: Comic build/typecheck, combined
card/device rendered race tests (7.359 s), final device-only rendered race
(4.470 s), and all fourteen Comic asset checksums pass. Actual Chromium checks
390/1280 px, collapsed/expanded diagnostics, progress, address/key, Remove and
no overflow. This is not a claim that pending history has been repaired.

The private audit found 101 conversation-history deferred references: 3 already
original on their target, 28 blocked directly by eight carrier records for two
signed historical group dismissals, 45 blocked through those dependencies, and
25 source-valid but still uncovered. The old browser phone's background window
is full; the other windows are empty. All originals remain present. The 23
quarantined phone-browser copies and six laptop copies have exact equivalent
source/hash copies delivered to the current APK, which does not prove recovery
on the original targets. No queues, receipts, grants or devices were changed.
Repair the source policy only with preserved signed event, historical membership
and admission proof, then test recovery through the normal bounded queue.
The two events have valid original-author carriers already retained. The repair
uses those verified equivalents instead of expanding historical forwarding
authority: compare the entire signed event including its signature, keep every
original row, and clear redundant deferred bookkeeping only with exact-recipient
durable coverage in the same transaction. Browser and native source paths must
agree, and unmatched/forged events must remain held.

**Implemented and independently reviewed, not released:** Go and browser use a
bounded page-local fallback only for the existing rejected dismissal-transport
case. Pure event payload, exact signature, current own-device authority, source
admission, canonical history authority and durable recipient coverage are
rechecked atomically. No schema, wire format, receipt or live-permission change.
Original rows stay intact. Conflicts, unmatched events and extra payload remain
deferred. Source changes during verification roll back the page.

Verification: the new native regression fails on the original source with
“exact signed dismissal duplicate stayed deferred” (1.634 s). The corrected
regression plus three adjacent historical-witness/restart cases pass under race
(123.068 s). Browser race checks pass (18.135 s): 22 signed memory checks,
22 actual IndexedDB checks including a second-connection mutation/rollback and
restart, and 776 adjacent signed compatibility checks. `go vet ./...` and
whitespace checks pass. The final Comic build/typecheck and fourteen asset hashes
pass; the Details wording now says archive items were accepted for background
import, without claiming each message was imported.

One normal bounded history page on isolated writable copies of the read-only
snapshot resolves Phone 3's 25 deferred refs, Linux's 7 and Bezos's 25 (to zero),
queuing 17, 4 and 19 needed history carriers respectively. The old browser phone
retains its 44 refs because its window is full at 228; no pacing or receipt was
changed. All attempted HTTP lookups were denied, no replies fabricated, and no
live store was written. This establishes source/queue recovery, not delivery or
rendering on the real devices; the artificial capability lookup failure used
the existing legacy transport fallback. Writable diagnostic copies were removed.

Remaining: publish only after the integrated release gates, then verify actual
device delivery/import. The old offline browser backlog and the 29 quarantined
copies on their original targets have not been purged or declared recovered.
MEL-558 stays open; removing an old device remains an explicit owner choice.

## AgentNet v0.8.19 — published; physical-phone verification remains

[v0.8.19](https://github.com/misunders2d/agentnet/releases/tag/v0.8.19) published
October 11 at **08:33:39 UTC**; existing relay verified **08:37:03 UTC**.
[The full record](plans/V0_8_19.md) covers qualified source, all thirteen assets,
Android in-place data preservation, native update check and MEL-594's corrected
history direction, including existing saved copies. Root main is integrated.

Next: owner phone verification of the published APK, prompt fresh replies and
history convergence. Keep MEL-546/558 open. Investigate the public loader.js
four-hour cache header recorded in MEL-551; served bytes currently match the
release, and no CDN configuration was changed during deployment. Do not
reset/relink/purge or replay tasks. Existing old previews install the APK over
their app once; clients otherwise use their own update controls. No new feature
batch is assigned here.

## v0.8.18 — published; physical-phone verification remains

[v0.8.18](https://github.com/misunders2d/agentnet/releases/tag/v0.8.18) published
October 10 at **21:29:14 UTC**, tag `35ce9190`, qualified source `484db3e7`.
All source gates and fresh package/hash/version checks are complete. The existing
Contabo relay was verified at **21:29:46 UTC**, preserving its realm, volume,
port and neighboring service. [HANDOFF.md](HANDOFF.md) records deployment and
[V0_8_18.md](plans/V0_8_18.md) records full qualification and honest limits.

The separately maintained Android preview includes the final shared core and
passes in-place emulator upgrade/data preservation, core races, native unit,
lint and packaging checks. Artifact and source references are in HANDOFF.
Neither this preview nor synthetic relay tests establish physical-phone timing.

Next: verify first useful messages, foreground/background receive, immediate
fresh replies during retained history catch-up and resource usage on the real
phone. MEL-546/558 stay open until confirmed. Preserve identities and existing
messages: no reset, relink, purge or task replay. Clients update themselves;
only the existing relay was deployed by Codex. No extra feature batch is started.

# Previous release — v0.8.16

[v0.8.16](https://github.com/misunders2d/agentnet/releases/tag/v0.8.16) published
October 10 at **00:37:52 UTC**, tag
`45e38feea7b7e7ae9454247b2fdda28b362ceea6`, qualified application source
`14d214dd384dc453de0113ee122de6aa22cce592`. All five packaging jobs, native shell
checks, actual Linux AppImage replacement/restart and final asset checks passed.
The existing relay was verified at **00:39:14 UTC** with unchanged realm, volume
and neighboring service. Server and laptop verified all five exact browser
assets. Clients and live grants were not changed by Codex.

[The release checklist](plans/V0_8_16.md) records the complete composed evidence,
including both full UI race partitions, focused corrections, ten native
repetitions per platform, actual IndexedDB and rendered Comic checks. It retains
initial failures and their diagnosed fixes. Historical native/core tests remain
valid for unchanged source; opt-in/platform skips are not claimed executed.

The owner's physical phone remains the next verification target: first useful
messages after reopening, prompt fresh replies during history recovery, original
send-state convergence, and resource use. Update computers through their app/CLI
controls, then reload linked phones. Keep a source online for older history and
files. Preserve identities/history, native permissions and live sessions; no
reset, relink, resend or task replay. Keep MEL-546/558 open until real phone
results establish the outcome. No new feature batch is assigned here.

## Previous release — v0.8.14, October 9, 2026 (UTC)

[Published v0.8.14](https://github.com/misunders2d/agentnet/releases/tag/v0.8.14)
at **19:34:43 UTC** from `760e945a28c38461c74d84c7d4c9610464573dea`.
All five [packaging jobs](https://github.com/misunders2d/agentnet/actions/runs/37980178645)
passed; all 12 asset digests and 11 SHA256SUMS entries matched. Standalone and
bundled Linux commands report the exact clean tag. The existing relay was
verified at **19:35:17 UTC** on `agentnet-revived:v0.8.14-760e945a`, after a
verified stopped-state backup, with normal HTTPS from server and laptop,
unchanged realm/volume/neighbor, and zero OOM events or restarts. Deployment
changed no installed client.

**Historical post-update report, owned by root Codex.** The
owner then updated Zenbook himself and reported “Could not connect 127.0.0.1:
Connection refused.” v0.8.15 repairs local-page readiness and
same-address shell reload; physical recovery is not user-confirmed. The phone
v0.8.13 report of “Sending” for 15 minutes also has no exact diagnosis;
v0.8.14 fixes confirmed blockers but is not claimed to fix that exact report.
The implementation and six-feature batch shipped in v0.8.15 above.

Implemented areas include direct-agent history across personal devices, durable
history receipts and progress, grouped/archiveable Held back notices, bounded
CLI inbox output, visible inherited permissions, exact remote request resolution,
topic-only invitations and editable proposals. ThinkPad's MEL-589 shared-context
authorship and other prepared fixes are integrated. Repeated native executable
hashing without pending input and four own-sync quarantine retry loops are
corrected with focused regressions.

Source qualification combines retained passing native/race/browser/desktop/
container evidence with corrected affected checks on product code `cde2cba6`,
verification candidate `36260adb`. Windows client
[A–G](https://github.com/misunders2d/agentnet/actions/runs/37978551047)
passed 655.004 s and [H–Z](https://github.com/misunders2d/agentnet/actions/runs/37978560159)
passed 424.799 s, covering 945 platform-specific test declarations. Linux client
race coverage contains 965 assignments. Initial full runs were not all green;
four shell-stub history scenarios skip Windows. No deadline increased. Exact
composed evidence is in [V0_8_14.md](plans/V0_8_14.md).

Actual post-fix ThinkPad CPU and physical phone convergence remain unverified.
The reported shutdown abort has no attributed fix. The owner made the historical
Mac installation report explicitly nonblocking; no exact cause or fix is claimed.
Installers remain unsigned.

Clients use their existing app/CLI update controls; linked browsers reload after
the relay rollout. New direct-agent history and remote resolution require
compatible own devices; edited or sibling-device proposal confirmations also
require an updated agent host. Topic-only invitations require compatible
original-member devices and the invited host, so an older participant can leave
that option unavailable. Topic-scope and proposal-edit controls are in Comic.
An additional topic invitation does not narrow existing whole-chat access.
File references synchronize, but downloads need a linked source retaining the
bytes. Preserve live sessions, native permissions and exact grants; no reset,
relink or task replay.

Previous [v0.8.13](https://github.com/misunders2d/agentnet/releases/tag/v0.8.13)
was published at 15:23:08 UTC on October 9; its existing-relay checks passed at
15:24:52 UTC. Historical evidence remains in [V0_8_13.md](plans/V0_8_13.md).

---

# AgentNet v0.8.10 — October 8, 2026 (UTC)

[Published v0.8.10](https://github.com/misunders2d/agentnet/releases/tag/v0.8.10)
at 21:54:58 UTC from `7afac5bda699a33f60db38dfb188b1a6d83ef66d`.

- **Check for updates** in every desktop skin uses the published stable release independently of the server recommendation. It shows the installed app version and available/current/ahead/error states, with no installation, work pause or background polling.
- The phone's stale **Bring back** footer now respects exact active/pending agent membership. It rechecks before opening an invitation and discards delayed results after switching chats.

Full affected command/UI packages and repository-wide vet passed on
Linux/macOS/Windows, alongside local race and all-skin desktop/phone browser
checks. Unchanged core code retains v0.8.9 qualification. All five packaging jobs
passed, all asset digests/checksums matched, and the final AppImage passed actual
replacement and restart. See [HANDOFF.md](HANDOFF.md) for exact evidence.

Update through **Settings → About → Update AgentNet** or
`agentnet update v0.8.10`, then reload browser clients. Installers are unsigned;
physical phone catch-up, notifications/clipboard and interactive Windows/macOS
installation remain separate checks. No installed desktop client was changed.
The existing relay was upgraded at 21:56:11 UTC after a verified backup, retaining
its original realm and volume; public HTTPS and post-deployment health passed.

---

# AgentNet v0.8.9 — October 8, 2026 (UTC)

- Recover retained linked group-history copies that older clients rejected after an agent left. Current signatures, membership, exact keys and human-device checks remain; historical requests never run.
- Support exact causal requests that call back through remote agents to an already busy ancestor, preserving ordering, cancellation and update fences.
- Install Linux AppImages into the per-user application directory on first opening. Updates and launchers use that stable copy, so Downloads can be cleaned up. Conflicting installs and custom launchers stay preserved.
- Shorten long raw link labels in all skins, keeping full opening/copy targets, descriptive labels, code and native mobile link inspection.
- Remove the single-agent setup choice when the exact agent is already selected. Needs-human messages explain that native permission/environment problems need repair before retry; replies do not change permissions.

Update laptops through **Settings → About → Update AgentNet** or the CLI once
published. Reload the mobile app after the relay rollout to load recovery.
No reset or relinking is required by this fix. Physical Amazon_team catch-up
remains unverified; the same-store old-client rejection and upgrade recovery
have passed browser and native regression tests.

MEL-574 Antigravity support and MEL-575 reported model visibility remain open
with qualification/compatibility limits recorded in Linear. MEL-579 Drive
embedding search remains explicitly future scope. Installers remain unsigned;
interactive Windows/macOS and physical notification/clipboard checks remain
separate from automated qualification. Native permission/Hyprland IPC root
cause is not claimed fixed by guidance.

Published at 21:03:42 UTC from `bd793b64e07e303458604f3dec01067884dbb3d6`; existing relay upgraded at 21:06:24 UTC with a verified backup and unchanged realm/volume. Composed source qualification and all five packaging jobs passed; asset checksums/revisions match. See [HANDOFF.md](HANDOFF.md) for exact CI evidence and physical-device limits.

---

# AgentNet v0.8.8 — October 8, 2026

AgentNet v0.8.8 fixes recent request, group and desktop issues:

- Updates check the app file before shutdown; a missing or changed file keeps the app open with recovery guidance.
- Reply to an agent waiting for clarification continues the exact request, including from your linked phone. Repeated sends are idempotent; an ordinary quoted message stays ordinary chat.
- Multi-agent sends show the human message once, with separate per-agent files, status, replies and retries. Different local agents can run concurrently. Older messages without grouping metadata retain their original entries.
- Codex group questions can ask another exact group agent through the native tool transport while retaining the question sandbox and current permissions.
- Notification clicks open AgentNet at the request or review list. Without the app, they use the existing coding-agent opener.
- Approved suggested tasks use compact cards with the exact approved text available.
- Stale invitations can be declined offline, and old agent cards stop offering Bring back when that agent is already active or awaiting rejoin.

Signatures, end-to-end encryption, membership and local permission checks remain enforced. Unsupported Codex app-server versions return needs-human; answering a clarification does not change sandbox permissions or repair environment restrictions. Older peers still receive compatible ordinary requests; explicit clarification continuation requires an updated host.

If an older installation’s registered AppImage has already been removed, reinstall the official app once. The new preflight applies after this version is installed.

Update with **Settings → About → Update AgentNet** or `agentnet update v0.8.8`, then reload linked browser clients. Installers are unsigned; verify downloads against `SHA256SUMS`.

The unchanged production code was exercised by the full source run, including native Linux/macOS, all race shards, desktop builds and the container journey. Two jobs exposed test-fixture defects; those were corrected and qualified with focused race runs plus native Linux/macOS/Windows CLI and affected-client checks. The original full run remains recorded as 12/14 passed, not an all-green run. Physical notification clicks and linked-phone catch-up remain separate checks. Cyclic callbacks to an already busy ancestor agent are not yet qualified.

Verification and remaining physical-device checks: [handoff](https://github.com/misunders2d/agentnet/blob/main/docs/HANDOFF.md).

Published 19:43:30 UTC from `8336346d546a11fddba74f06964a3fff6c95ee1c`. All five [release jobs](https://github.com/misunders2d/agentnet/actions/runs/37832858055) passed; all 12 asset sizes/digests and 11 checksum entries matched. Downloaded Linux CLI and bundled CLI report the exact clean revision. Existing relay upgraded at 19:47:55 UTC with verified stopped-state backup, original data/realm and healthy post-checks. No installed desktop client was updated. Its recommendation remains v0.8.5 because the available identity has member rights; Update checks GitHub independently.

---

# Historical AgentNet v0.8.7 — October 8, 2026

- Linked-device group history retains accepted inert requests, replies, status
  and selected excerpts after a clean assistant dismissal. Exact original
  signatures, keys, membership and admission checks remain; live authority is
  unchanged. No history reset or new protocol is required.
- Held back notices show persisted allowlisted explanations and recovery advice.
  Archive notice hides only a local invalid notice; its envelope remains blocked
  and nothing is accepted, trusted, replayed or executed. Old records cannot
  reveal diagnostic detail that was never stored.
- Explicitly selected agents receive independent requests, results and retries,
  including same-named agents on one host. Failed targets do not resend successful
  requests; received mention text never starts work.

Use **Settings → About → Update** or download
[v0.8.7](https://github.com/misunders2d/agentnet/releases/tag/v0.8.7), verifying
`SHA256SUMS`, or run `agentnet update v0.8.7`. Update the laptops holding
the missing history and reload the phone app after the relay update. Identity
and history are retained; no reset or relinking is needed.
Older v0.8.3/v0.8.4 AppImages may need one manual reopen after replacement.

Local required vet/race checks and all three locked skin builds passed. All
three skins passed 1280/390-width multi-agent and Held back regressions;
focused native/browser history regressions and real IndexedDB recovery passed.
All 14 [source CI jobs](https://github.com/misunders2d/agentnet/actions/runs/37795624300) passed on immutable source `129a38ec5de8fc8f988b9972a0a143e6dc27dbb8`.
[Release packaging](https://github.com/misunders2d/agentnet/actions/runs/37800186287):
all five jobs passed; all 12 downloaded asset sizes/digests and all 11 SHA256SUMS entries matched. The Linux CLI and AppImage-bundled CLI report the exact clean release revision. The final AppImage passed the release CI replacement/restart check.
Published: 15:35 UTC. Relay: upgraded the existing relay at 15:42 UTC after a verified stopped-state backup; its running binary and public HTTPS report v0.8.7, with the original data volume, configuration and realm retained. Post-checks found zero restarts/OOM events and the neighboring service unchanged. Installed desktop clients were not changed.
Physical mobile catch-up remains unverified and MEL-558 remains open.
MEL-435's separate peer-tested notification-click branch is not shipped here.
Installers are unsigned; interactive Windows/macOS installation remains
unverified. Full evidence is in [the handoff](HANDOFF.md).

---

# Historical AgentNet v0.8.6 — October 8, 2026

Sending, linked history, setup and approval fixes for AgentNet.

- Browser uploads use the existing bounded request timeout so stalled uploads no longer hold queued sends indefinitely.
- Deleting a queued question or task stops its remaining local copies, including after restart and later receiver approval. Delivery already attempted stays explicitly uncertain; proven delivery and existing agent work are preserved.
- Linked-device group history continues when an original control recipient has left. Browser history also supplies the existing signed group context, including empty groups and recovery of completed copies missing that context.
- Every skin retains the full approval explanation. Classic opens the request's actual topic; Zoom displays signed agent progress in direct device threads.
- Browser agent setup offers the existing computer installer and one-use device-link flow, with approval on the original device before enrollment.
- Native What's new links use the existing external navigation handler through the supported opener-plugin setting.

Existing unread/read synchronization, group admission, notification mute,
profile picture/paste and keyboard behavior have regression coverage. These
changes reuse existing protocols and setup flows; no new service or production
dependency was added.

## Upgrade

Use **Settings → About → Update** in the desktop app, or download the matching
installer from [v0.8.6](https://github.com/misunders2d/agentnet/releases/tag/v0.8.6).
Verify downloads against `SHA256SUMS`. Installers are unsigned.

AppImages upgrading from v0.8.3/v0.8.4 may still need one manual reopen because
their old updater cannot repair itself before replacement. If the old Update
button is unavailable, run `agentnet update v0.8.6` for the registered desktop
installation, then reopen once. Updates started by v0.8.5 or later include the restart fix.
Identity and history are retained; no reset or relinking is needed.

## Qualification

Local Go vet, all required race shards, all three skin builds and focused
desktop/phone-width browser checks passed. The new history regression fails on
the original source and passes on this version. All 17 native shell tests pass;
an isolated Linux native fixture reproduces the old external-link ACL failure
and verifies the corrected dispatch.

The released source `0a0e7aeadef15b315feb694706b6e0cafb6f8ecc` passed all
14 required [source CI jobs](https://github.com/misunders2d/agentnet/actions/runs/37777205734)
and all five [release jobs](https://github.com/misunders2d/agentnet/actions/runs/37779934720).
All 12 downloaded asset sizes/digests and all 11 checksum entries matched.
The final AppImage passed CI replacement/restart checks and a separate isolated
external-link check of the downloaded native shell. The existing relay now
reports v0.8.6, retaining its data volume and realm after a verified backup.

Optional harness tests require their own prerequisites and opt-in. Physical
linked phones, live model execution and interactive Windows/macOS installation
remain unverified. Source CI, final downloadable artifact checks and rollout
evidence are recorded in [the handoff](HANDOFF.md).

See [the changelog](../CHANGELOG.md) for earlier releases.
