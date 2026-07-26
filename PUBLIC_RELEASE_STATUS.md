# Public Package Status

Snapshot: 2026-07-26

This additive status note reconciles public package availability with AgentNet's
frozen `0.1.26` release evidence. It does not replace requirements, gate
ledgers, or accountable-owner evidence.

## Current public package

Reads of the public npm registry and immutable Git tag returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.26`
- published package `gitHead`: `a7da3aa945c0b2f25fdb06803b80529f89bf8242`
- annotated tag `v0.1.26` peels to the same commit

Package availability does not authorize deployment and does not establish
production readiness.

## Frozen release-input clarification

The retained `0.1.26` wheel and sdist under
`evidence/local/2026-07-24-v0.1.26/artifacts/` are immutable release evidence.
Their packaged release inputs retain pre-publication wording such as "latest
published package: 0.1.25", "unreleased 0.1.26", and publication pending. Those
frozen bytes are not rewritten after publication. `scripts/verify_release.py`
intentionally checks them against their release snapshot.

Correcting that wording inside packaged release inputs requires a new version
candidate, fresh artifacts, normal release verification, and separate
publication by Sergey. This note records the post-publication registry and tag
facts without changing or creating another artifact under version `0.1.26`.

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
