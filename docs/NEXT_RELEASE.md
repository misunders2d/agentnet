# AgentNet v0.8.10 — in progress

Fresh Linear triage selected two bounded follow-ups: explicit published-release
availability in About (MEL-551/MEL-493), and the remaining phone dismissal footer
with stale Bring back (MEL-542). Reuse existing release discovery and exact-agent
rejoin guards. No new dependencies, background polling, permission changes or
installed-client updates. Focused reproductions first; one integrated release
qualification after the fixes stabilize. These changes are not yet shipped.

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
