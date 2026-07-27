# Public Package Status

Snapshot: 2026-07-27

This additive status note reconciles public package availability with AgentNet's
frozen `0.1.27` release evidence. It does not replace requirements, gate
ledgers, or accountable-owner evidence.

## Current public package

Reads of the public npm registry and immutable Git tag returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.27`
- published package `gitHead`: `4641503ac6ee398db44f2c3fffe4c639b7c60561`
- annotated tag `v0.1.27` peels to the same commit

Package availability does not authorize deployment and does not establish
production readiness.

## Frozen release-input clarification

The retained `0.1.27` wheel and sdist under
`evidence/local/2026-07-26-v0.1.27/artifacts/` are immutable release evidence.
Their packaged release inputs retain their pre-publication snapshot. Those
frozen bytes are not rewritten after publication. `scripts/verify_release.py`
checks each candidate against its own release snapshot.

Root-installed verification exposed three `0.1.27` portability failures before
any server mutation or network creation. Candidate `0.1.28` repairs those exact
failures. Its source and two clean recursively packed npm generations each
report `1418 passed, 16 expected platform/dedicated-PostgreSQL skips`; those
lanes exclude installed-live-inference, subprocess-lifecycle, and bake-off
evidence, while two installed-harness pin failures remain non-green and are not
rerun or waived. Package, release-verifier, launcher, recursive-package, and
byte-identical wheel/sdist checks pass. A new tag and separate publication by
Sergey remain required. This note records public `0.1.27` without changing its
immutable package evidence; exact public `0.1.28` root-installed Hub evidence,
network creation, fresh-laptop enrollment, and native cross-host message/ACK are
still pending.

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
