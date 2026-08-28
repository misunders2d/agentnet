# Public Package Status

Snapshot: 2026-07-29

This additive status note reconciles public package availability with AgentNet's
published `0.1.31` first-message onboarding release and unreleased `0.1.32`
release-blocker candidate. It does not replace requirements, gate ledgers, or
accountable-owner evidence.

## Current public package

Reads of the public npm registry and immutable Git tag returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.31`
- published source commit: `dc10f86c9ac15ffc29c4dd5dd37e6c6d5bf15382`
- annotated tag object: `edcca20208d8d8551206dbb2ac5e14d8f4ce086b`

Package availability does not authorize deployment and does not establish
production readiness.

## Frozen release-input clarification

The retained `0.1.28` wheel and sdist under
`evidence/local/2026-07-27-v0.1.28/artifacts/` are immutable release evidence.
Their packaged release inputs retain their pre-publication snapshot. Those
frozen bytes are not rewritten after publication. `scripts/verify_release.py`
checks each candidate against its own release snapshot.

The Hub installed and verified exact public `0.1.28`. A real owner Google OIDC
attempt then exposed an application defect before callback claim: the Approval
handler required exactly two total query parameters and rejected valid unique
provider metadata. The transaction remained pending, callback unclaimed, and
owner bindings/passkeys remained zero. Candidate `0.1.29` repairs owner,
enrollment, and recovery callback handling with global duplicate-name rejection,
strict disjoint success/error projections, ignored unique unknown extensions,
and exact state-bound terminal provider-error handling without token exchange.
Its focused callback suite reports `92 passed`; source and both recursively
packed generations each report `1443 passed, 16 expected skips`; release,
package, reviewer, and reproducible-build gates pass. The exposed callback URL
is not reusable and must not be retried. Fresh-laptop
enrollment resumes only after reviewed public `0.1.29` installation and a new
OIDC transaction; native cross-host message/ACK remains pending.

Exact public `0.1.29` then passed installed verification on the Hub, but that verifier wrote `.hypothesis`, `.pytest_cache`, and Python bytecode caches into the immutable package tree. A read-only Hub probe counted 416 custody violations among 1,069 descendants; no setup or host mutation occurred. Published `0.1.30` runs verification from a bounded disposable copy, rejects caller pytest arguments, and adds recursive complete-tree digest plus no-residue checks.

Published `0.1.31` addresses the first-message blockers known before its live server ceremony: browser-only fixed remote activation with exact approved-owner OIDC policy; purpose-separated automatic Approval receipt delivery; hermetic supported `0.1.28/0.1.30` setup migration/recovery; status-scoped guided-join polling through fresh Approval-challenge expiry; Core-authenticated argument-bound terminal replacement with same-key reuse; enabled-unit reconciliation; and destructive package-only reset under a permanent coordination lock. Latest local full-suite evidence is `1625 passed, 16 expected skips`, with two installed-harness pin failures and pre-refresh release-manifest drift kept non-green. A packed focused recovery/conformance lane reports `72 passed`, one Pi skill, zero loader diagnostics, and no residue. Official source and two recursive packed generations each report `1549 passed, 16 expected skips` with complete package-tree digest/no-residue checks green. Narrow Claude Opus 5 closure reports no blockers and PASS for skill architecture, code security, and Constitution. The first exact untagged Hub artifact installed byte-identically into an inert root-owned prefix and then failed closed before setup because its embedded release bindings were stale. The later live server ceremony completed after operator recovery but exposed MEL-216 (owner-passkey registration ordering) and MEL-217 (false final setup health/identity status); both remain unresolved in `0.1.31` and are assigned to the next release. Replacement internally bound verification, fresh-laptop enrollment, native message/ACK, completion marker, and five-power revocation evidence remain pending. Package availability enables that proof; it does not certify production readiness or close either defect.

Unreleased `0.1.32` is one coordinated repair for the ceremony blockers: signed public-path Approval broker readiness using explicit host trust with certificate/key-log environment denied before setup; authoritative setup reconciliation; OIDC-begin exact replay after response loss/concurrency; finite current-credential renewal; clean current-package setup-attempt custody; and a package-owned isolated C0 responder under a five-unit systemd lifecycle. Terminal responder state is owner-only, and same-digest setup repairs a marker-before-config-cleanup interruption without resurrecting the responder. One exact setup-owned transition accepts the realized public 0.1.31 two-unit marker and a retained non-lab ordinary-server credential expired no more than 24 hours, preserves principal/harness/key and all existing authority/message state, advances its epoch once, records durable replay custody, and completes the five-unit no-start apply without a service action or authority grant. It is release-bound and cannot chain on the same harness. Prior frozen evidence predates this addition; replacement source/package/reviewer/PostgreSQL and external runtime proof are pending. Exact CI policy pairs the Node.js 22.19.0 minimum floor with npm 10.9.3 and retains Node.js 24.18.0 LTS plus npm 12.0.1 for three-OS/release paths and Node.js 24.18.0/26.5.0 compatibility lanes. The deployed Hub compatibility target is separately verified as Node.js 22.23.2 with npm 11.18.0; Node.js 23 and 25 are EOL and unsupported. Same-commit ordinary-server E2E passed at `81315a6`; corrected Node-floor policy is committed at `a26af8f`, while its same-commit cross-platform result remains unverified here. The two installed-harness G01 failures remain non-green and unwaived. Mutation-authorized PostgreSQL, corrected same-commit cross-platform CI evidence, and external fresh-host/C0 evidence remain absent; no replacement-bearing 0.1.32 artifact is published, deployed, or certified.

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
