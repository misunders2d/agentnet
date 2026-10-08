# AgentNet v0.8.6 — October 8, 2026

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
installation, then reopen once. Updates started by v0.8.5 include the restart fix.
Identity and history are retained; no reset or relinking is needed.

## Qualification

Local Go vet, all required race shards, all three skin builds and focused
desktop/phone-width browser checks passed. The new history regression fails on
the original source and passes on this version. All 17 native shell tests pass;
an isolated Linux native fixture reproduces the old external-link ACL failure
and verifies the corrected dispatch.

Optional harness tests require their own prerequisites and opt-in. Physical
linked phones, live model execution and interactive Windows/macOS installation
remain unverified. Source CI, final downloadable artifact checks and rollout
evidence are recorded in [the handoff](HANDOFF.md).

See [the changelog](../CHANGELOG.md) for earlier releases.
