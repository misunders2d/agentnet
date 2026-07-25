# Public Package Status

Snapshot: 2026-07-24

This additive status note reconciles public package availability with AgentNet's
frozen `0.1.25` release evidence. It does not replace requirements, gate
ledgers, or accountable-owner evidence.

## Current public package

A read of the public npm registry on 2026-07-24 returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.25`
- published package `gitHead`: `eed319a76e4fbd0495f4d1bc1862f4c5891444ff`

Package availability does not authorize deployment and does not establish
production readiness.

## Frozen release-input clarification

The retained `0.1.25` wheel and sdist under
`evidence/local/2026-07-24-v0.1.25/artifacts/` are immutable release evidence.
Their packaged release inputs include older pre-publication wording such as
"latest published package: 0.1.24" and "prepared 0.1.25". Those frozen bytes are
not rewritten after publication. `scripts/verify_release.py` intentionally
checks them against their release snapshot.

Correcting that wording inside packaged release inputs requires a new version
candidate, fresh artifacts, normal release verification, and separate
publication by Sergey. This note records the post-publication registry fact
without creating a second artifact under version `0.1.25`.

## Release and gate posture

`RELEASE_MANIFEST.json` remains controlling for release eligibility:

- status: `BLOCKED`
- `production_ready=false`
- `ship_eligible=false`
- no must-not-ship gate is `PASSED`
- G04 remains `FAILED`
- G10 remains `PARTIAL`

No deployment, setup, enrollment, restart, authority grant, or gate promotion
is implied by this note.
