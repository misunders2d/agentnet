# AgentNet v0.8.3 — candidate, not released

A focused patch for updating AgentNet as a whole on a computer. The published
release remains [v0.8.2](https://github.com/misunders2d/agentnet/releases/tag/v0.8.2).

## Changes

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

## Upgrading from v0.8.2

Use **Settings → About → Update AgentNet** once this patch is published. The
new app automatically adopts an unchanged official standalone CLI; a separate
Replace command step is unnecessary. Existing v0.8.2 standalone executables
still contain their old CLI-only updater until replaced. After installing this
patch, both supported update entry points use the global updater.

Keep the existing identity and data directory. Custom command replacements
remain an explicit owner choice. No reset or re-enrollment is required.

## Qualification

Focused update regressions, command-package race tests and vet pass. An
isolated compiled backend with a temporary enrolled device and actual official
old CLI files verifies startup adoption and same-version repair through About
and the managed command. It checks all resulting versions and executable bytes.
It does not exercise a graphical package restart or physical Windows/macOS
installation. Final native CI and package-upgrade qualification are pending;
this document is not a release announcement.

See [the handoff](HANDOFF.md) for exact commits, evidence and remaining gates,
and [the changelog](../CHANGELOG.md) for published release history. Projects and
other new product features are outside this patch.
