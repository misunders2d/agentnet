# Public Package Status

Snapshot: 2026-07-31

This additive status note reconciles public package availability with AgentNet's
published `0.1.33` setup-migration release and corrective `0.1.34`
identity-preserving recovery candidate. It does not replace requirements, gate
ledgers, or accountable-owner evidence.

## Current public package

Reads of the public npm registry and immutable Git tag returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.33`
- published source commit: `466c96d50e8aeeb060be92e8d01c024af9fd35c6`

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

Published `0.1.32` is one coordinated repair for the ceremony blockers: signed public-path Approval broker readiness using explicit host trust with certificate/key-log environment denied before setup; authoritative setup reconciliation; OIDC-begin exact replay after response loss/concurrency; finite current-credential renewal; clean current-package setup-attempt custody; and a package-owned isolated C0 responder under a five-unit systemd lifecycle. Terminal responder state is owner-only, and same-digest setup repairs a marker-before-config-cleanup interruption without resurrecting the responder. The first-C0 path rejects reuse of `0.1.31` state. Retained affected and source-regression lanes report `599 passed, 7 expected dedicated-PostgreSQL skips` and `1639 passed, 16 expected skips`; source and two recursive packed npm generations each report `1595 passed, 16 expected skips`. Final TLS security and skill-architecture reviews converge, Constitution review passes, and the later Node-lifecycle skill audit converges. Exact CI policy pairs the Node.js 22.19.0 minimum floor with npm 10.9.3 and retains Node.js 24.18.0 LTS plus npm 12.0.1 for three-OS/release paths and Node.js 24.18.0/26.5.0 compatibility lanes. The deployed Hub compatibility target is separately verified as Node.js 22.23.2 with npm 11.18.0; Node.js 23 and 25 are EOL and unsupported.

At `aefaafbe0e24d3106e7fb2a60dd36e1520ea6395`, main-push cross-platform run `30553084697` and ordinary-server run `30553084681` passed, and tag cross-platform run `30553729116` passed. Tag ordinary-server run `30553729024` remains BOUNDED NON-GREEN: runner image `20260726.254.1` rejected executable lineage under `/usr/local` before AgentNet setup mutation; cleanup passed. Trusted run `30553729045` completed `npm stage publish` before cancellation, creating one non-public stage whose exact identifier is retained out of band. That stage was not the later public release. At the time of this bounded snapshot, npm still reported `0.1.31` as latest.

At `4ae4dca320456100ada77b51f60af44042b645ce`, ordinary-server run `30557431270` remains BOUNDED NON-GREEN: the new fixture precondition proved that the npm-installed launcher remained outside root custody after `sudo npm install --global`, consistent with npm honoring `SUDO_UID`/`SUDO_GID`; the run stopped before AgentNet setup and cleanup passed. The corrected fixture rejects package-root symlinks, transfers only the exact resolved installation prefix to root custody without dereferencing links, leaves package modes unchanged, and then proves full Node/uv/launcher/package-tree custody. A future green run proves that explicit deployment step; it does not claim that `sudo npm install -g` alone creates safe root custody. At that time fresh same-commit workflows, replacement tag, and replacement trusted stage remained pending. The two installed-harness G01 failures remain non-green and unwaived. Mutation-authorized PostgreSQL and external fresh-host/C0 evidence remain absent.

At `1146ba324b70d85f3814b9179474966434e4cc64`, cross-platform run `30558716514` passed, while ordinary-server run `30558716146` remains BOUNDED NON-GREEN: after the exact prefix was transferred to root custody, the installed package launcher was directly measured as `uid=0 mode=777`; AgentNet rejected it before setup and cleanup passed. The archive entry remains normalized to `0755`; npm's post-extraction bin-link step derives the installed executable mode from the install process umask. The correction pins `umask 022` inside the privileged install and every release packed-install path, asserts the packed launcher is `0755`, and asserts the installed systemd launcher remains `0755` without a chmod repair. Fresh same-commit workflows, replacement tag, and replacement trusted stage remain pending.

At `d1bf8c647a812d782d5607fa8cb49ed503699d4c`, cross-platform run `30560778746` passed, while ordinary-server run `30560778776` remains BOUNDED NON-GREEN: the privileged shell umask alone did not constrain npm's separate extraction configuration, and the exact root-owned launcher again measured `mode=777`; setup did not run and cleanup passed. Direct npm 12 source inspection and hostile-umask reproduction show that the server installation must combine process `umask 022`, npm `--umask=0022`, and `--bin-links=false`. The server profile creates no ambient global command and invokes the verified absolute Node/launcher pair directly; the normal laptop install retains npm bin links. The checker now asserts the archive launcher mode, installed launcher mode, and safe modes across the full installed package tree. Fresh same-commit workflows, replacement tag, and replacement trusted stage remain pending.

At `477c9ef54256dd771a2908ab57c98e93ae603e8a`, cross-platform run `30562170819` and ordinary-server run `30562170931` remain BOUNDED NON-GREEN. The ordinary-server fixture proved the launcher file was corrected, then rejected the first npm-created package-root ancestor at `uid=0 mode=777`; setup did not run and cleanup passed. The correction normalizes only the four exact npm-created install-topology ancestors to `0755`, leaves archived descendants unchanged, and retains full lineage/tree checks. The packed unit lane also exposed non-hermetic tests that mocked Node/uv/package resolution but inherited GitHub runner host-tool custody; those tests now mock the host-tool resolver inside the existing fixture, while the dedicated resolver test still proves fixed-path selection and custody-check invocation. The real E2E does not shadow host tools: it directly measures the exact fixed-path `systemctl` and `useradd` lineage before setup. Fresh same-commit workflows, replacement tag, and replacement trusted stage remain pending.

The corrected `0.1.32` source was subsequently published from commit
`77f5240c3df5c0328fbfbc154b623b79992b2a11`. One exact digest-bound Hub apply
then failed closed at `setup_marker_conflict`: the live `0.1.31` marker records
the communication-only two-unit profile, while `0.1.32` accepts no supported
in-place transition from that profile. The refusal occurred before managed
reconciliation, daemon reload, service activation, database migration,
credential, identity, responder, or authority mutation. Active Core and
Approval remain `0.1.31`; Core is live but not ready, and enrollment/C0 remain
blocked.

Published `0.1.33` admitted the exact `0.1.31` two-unit and `0.1.32` five-unit
predecessor profiles. On the Hub it committed marker v3 revision 2 and the five
unit bytes while preserving PostgreSQL, identity, configuration, and credential
state, then failed closed during quiescence. `agentnet-approval.service` exited
with status 143 and systemd retained `ActiveState=failed`; 0.1.33 required
`inactive`, lacked Approval's successful-SIGTERM declaration, and did not clear
the failed latch. A same-digest repeat made no further mutation and cannot
recover this state. Services remain offline, PostgreSQL remains schema 4/5, the
preserved credential is expired, authority is false, and no enrollment or C0
message occurred.

Corrective candidate `0.1.34` preserves the strict inactive postcondition. It
adds Approval's successful-SIGTERM contract, clears only the failed latch for
the five exact managed units after bounded stop/disable, and admits the exact
0.1.33 five-unit marker only. A committed 0.1.33 forward journal is superseded only
after exact marker/config/unit revalidation and before a separately journaled
0.1.33→0.1.34 edge. Earlier sources use the separately released 0.1.33
migration first. No reset,
manual marker/database edit, identity replacement, or authority grant is added.
For the Hub's additional expired-credential blocker, 0.1.34 adds an exact
pre-C0 communication-only recovery: root-only same-key possession proof plus a
fresh owner WebAuthn Approval retires the old expired row unchanged and creates
one finite next-epoch credential. It performs no authority grant or restart and
refuses A2A, relay, or retained C0-terminal topologies. Local recovery tests
pass; complete release verification, same-commit CI, publication, Hub recovery,
the live owner ceremony, fresh enrollment, and the first native message remain
pending.

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
