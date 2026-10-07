# AgentNet v0.8.5 — October 7, 2026

[Download v0.8.5](https://github.com/misunders2d/agentnet/releases/tag/v0.8.5).

Updater hotfix for the AgentNet desktop app and its installed command.

- Linux AppImage updates keep the system search path needed to start the replacement. Immediate restart failures are reported.
- After reopening, AgentNet resumes verification of the app and installed command copies. An independent daemon waits for active work and confirms its new version before completion.
- The Update button recognizes the app's authenticated session after its URL token is removed.
- Restarted apps open an authenticated main page without requiring a reload. Conversation and invitation links retain their destination.

### Upgrading an older AppImage

The updater already inside v0.8.3/v0.8.4 cannot repair its own restart before this release is installed. If the app closes during this first upgrade, open AgentNet once from its launcher. Updates started by v0.8.5 include the restart fix.

If v0.8.4's Update button is unavailable, run `agentnet update v0.8.5` for the registered desktop installation, then reopen AgentNet once after it closes. This updates the registered app and its managed command copies; it does not reset identity or history.

This release is limited to updating. Mobile sending/history issues remain open separately.

Download the installer for your platform and verify it against `SHA256SUMS`. Installers are unsigned. Native automated CI and package builds do not establish interactive Windows/macOS update behavior.

## Qualification

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

See [the handoff](HANDOFF.md) for remaining work and [the changelog](../CHANGELOG.md) for earlier releases.
