# AgentNet v0.8.3 — October 7, 2026

[Download v0.8.3](https://github.com/misunders2d/agentnet/releases/tag/v0.8.3).
This patch fixes whole-app updates and running requests incorrectly asking
for your OK.

## Changes

- **Only real decisions need your OK.** Direct agent/device requests already
  running or stopping appear as Working without increasing approval counts.
  Their request and Stop control remain reachable. v0.8.2 fixed conversation
  requests but missed this separate direct-device list.
- **Update the app and command together.** About and the new `agentnet update`
  use the registered desktop app's updater, including official terminal copies
  elsewhere on PATH and the private command used by connected tools. A closed
  app opens automatically. CLI-only installations keep their standalone updater.
- **Repair a command left behind.** An already-current app still checks and
  repairs an outdated official command. Custom or changed files stay protected;
  an error names the exact file that needs attention.
- **Show the real result.** Completion requires the restarted app's version and
  matching command copies. Pending, partial and failed updates remain explicit.
  An unavailable app never silently causes a CLI-only update. Check/status
  commands do not install or launch anything.

Active agent jobs and separately managed daemons retain their existing update
protection. The patch updates AgentNet's own components; it does not update
Claude, Codex, Pi, other devices or the relay.

## Upgrading from v0.8.1 or v0.8.2

Use **Settings → About → Update AgentNet** once. The new app automatically
adopts an unchanged official standalone CLI; a separate Replace command step
is unnecessary. Older standalone executables still contain their old CLI-only
updater until replaced. After installing this patch, both supported update
entry points use the global updater.

Keep the existing identity and data directory. Custom command replacements
remain an explicit owner choice. No reset or re-enrollment is required.

## Qualification

[Source CI](https://github.com/misunders2d/agentnet/actions/runs/37592322850)
and [release package CI](https://github.com/misunders2d/agentnet/actions/runs/37592386488)
passed at `5aec119649807befe711135c97805a89f1aa3b90`. All 12 asset digests and
11 package checksums matched; the downloaded Linux binary reports v0.8.3 and
that exact clean source revision.

An isolated Linux package test replaces a real AppImage, restarts its desktop
shell/backend and verifies private, canonical and PATH command versions and
bytes plus recorded completion. Separate enrolled-backend journeys cover both
update entry points and same-version repair using genuine older CLI files.
Direct-device OKs native regressions and real-browser Comic/Classic/Zoom
fixtures pass on desktop and phone sizes, including exact navigation and Stop.

Interactive Windows/macOS upgrades and a graphical About-button click remain
unverified. Installers are unsigned. No existing installation was upgraded
during qualification.

See [the handoff](HANDOFF.md) for evidence and remaining work, and
[the changelog](../CHANGELOG.md) for release history. Projects and skin redesigns
remain separate work.
