# Public Package Status

Snapshot: 2026-08-06

This additive status note reconciles public package availability with AgentNet's
published `0.1.45` communication and collaboration release. It does not replace
requirements, gate ledgers, or accountable-owner evidence.

## Current public package

Reads of the public npm registry and immutable Git tag returned:

- package: `@misunders2d/agentnet`
- latest published version: `0.1.45`
- published source commit: `e8a49671481767078551f677599f51af051c3d5a`
- immutable tag: `v0.1.45`
- registry shasum: `06f4775ecf63097068e1f3583fe84a2c66c64096`
- provenance: SLSA statement signed by the trusted GitHub Actions publisher and
  approved by the accountable npm owner

`0.1.45` is a **fresh-install-only** release. In-place upgrade from `0.1.44` is
not a supported path: the packaged upgrade and rollback lane is preserved as
non-green, so operators must install `0.1.45` on a clean host and re-enroll the
server and each harness. Publication is gated on the ordinary-server
clean-install lane, which is the exact path operators follow.

Package availability does not authorize deployment and does not establish
production readiness. No must-not-ship gate is promoted by publication.

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

Published `0.1.34` added the identity-preserving failed-unit reconciliation and
was independently verified against its public npm integrity, registry
attestation, and immutable tag before one exact digest-bound Hub apply. That
apply passed frozen preflight and then failed before marker or PostgreSQL
advance. It preserved the marker, five unit files, PostgreSQL, identity, key,
credential, and operator inputs, but removed the retained committed
0.1.31-to-0.1.33 journal before the separately approved next-edge journal was
durably written. The Hub therefore has marker `0.1.33` revision 2 and no forward
journal. Services remain offline, public endpoints return 502, authority is
false, and no enrollment or C0 message occurred. The delayed read-only Hub
diagnostic itself timed out at its orchestration watchdog without durable
package-stage evidence; it is not treated as a diagnosis or a waiver of the
deterministic journal-loss reproduction.

Published `0.1.35` preserves the strict inactive postcondition. It
adds Approval's successful-SIGTERM contract, clears only the failed latch for
the five exact managed units after bounded stop/disable, and admits the exact
0.1.33 five-unit marker only. When a committed predecessor journal exists, it
is superseded only after exact marker/config/unit revalidation and remains
durable until the separate 0.1.33-to-0.1.35 journal atomically replaces it; an
injected next-edge write failure now proves the old bytes remain exact. The
Hub's already-absent old journal does not authorize reconstruction or manual
editing; the package creates the new edge journal from the exact validated
0.1.33 marker/config/unit state. Earlier sources use the separately released
0.1.33 migration first. No reset,
manual marker/database edit, identity replacement, or authority grant is added.
For the Hub's additional expired-credential blocker, 0.1.35 adds an exact
pre-C0 communication-only recovery: root-only same-key possession proof plus a
fresh owner WebAuthn Approval retires the old expired row unchanged and creates
one finite next-epoch credential. It performs no authority grant or restart and
refuses A2A, relay, or retained C0-terminal topologies. The injected recovery
regression passes (`103 passed`); the full source lane, direct verifier, and
both independently installed npm-tarball generations each pass with `1621
passed, 16 expected skips`; the frozen wheel and sdist are byte-identical across
two builds. The final local npm tarball has shasum
`c70bdfdb5b8184c55f46c18c9cda16754d130a4f` and integrity
`sha512-Eo+o7cP0PtsQfGVALJ0nqDB2XP7qWsUcrc3eLtsMmKEZF8aQKR8ZVagr1TSjZL0GPrFoBlHB6L3Ea0H2+R83jQ==`;
those are local package facts, not registry publication evidence. On commit
`c463de92c82e3ff2915df7b6af2dcf32a07988cb`, cross-platform/package run
`30693899336`, clean ordinary-server setup run `30693899319`, and real
0.1.31-to-0.1.33-failed-to-0.1.35 upgrade recovery run `30693899330` all
passed. The evidence-bound source is
`2dfd8edea213ac65e8d4eec215879af4fc53f259`, and exact public `0.1.35` was
independently verified before one approved Hub apply. That apply committed the
marker and PostgreSQL migration, then exposed an unsatisfiable setup check:
canonical `VerifiedActor` identity profiles forbid and never serialize
`actor.key_id`, while setup required that nonexistent field to equal the
private-key thumbprint. Five units remained inactive, authority stayed false,
and no enrollment or C0 message occurred.

Immutable tagged candidate `0.1.36` rejects duplicate/non-finite JSON members,
strictly parses the canonical actor, verifies its domain/harness/credential
labels, retains exact profile shape and private P-256 key custody/readability
checks, and removes only the impossible duplicate `actor.key_id` test. Active
database credential-to-key binding remains proven by `server-agent activate`;
setup does not manufacture a second self-asserted binding. It admitted only the
exact released `0.1.33` five-unit marker migration; `0.1.34`, `0.1.35`, and
direct legacy sources remained rejected. Same-commit main-push cross-platform,
clean-setup, and upgrade workflows passed. The immutable tag upgrade rerun then
failed in the released `0.1.31` seed setup's post-start runtime sampling:
`Type=simple` briefly exposed systemd's pre-exec shell as `MainPID` before Node
replaced it. Cleanup passed, setup authority stayed false, and npm staging never
ran. The tag remains immutable and non-public; no test waiver or tag rewrite is
permitted.

Published `0.1.37` changed only that protected release gate and exact
candidate migration edge. The upgrade E2E still performs one real released
`0.1.31 --apply --start`; exact success evidence passes directly, while only
exit 1 plus the exact `service_runtime` refusal and all three false safety flags
may enter a bounded, non-mutating convergence probe. That probe imports the
single root-owned released `0.1.31` private runtime and invokes its own exact
Approval/Core systemd-runtime and loopback/public-health validators. It never
restarts or reruns setup; malformed evidence, any other blocker, stable wrong
runtime, health mismatch, module-provenance mismatch, or timeout fails closed.
All command stderr is separately retained and included in synthetic-secret leak
scanning. The release admits only exact `0.1.33` five-unit marker migration to
`0.1.37`; `0.1.34`, `0.1.35`, `0.1.36`, and direct legacy sources are rejected.
Its focused, source, recursive packed-package, direct-verifier, byte-identical
archive, same-commit CI, immutable tag, and trusted npm-stage gates passed. Exact
public Hub setup then committed the five-unit marker with Core and Approval
healthy but failed closed because the public route converged after the ordinary
30-attempt probe window; authority remained false and auxiliary units disabled.

Published `0.1.38` changes only post-restart setup convergence. Public
Approval/Core health and public Core readiness use the existing finite 90-attempt
startup bound instead of the ordinary 30-attempt probe bound. Exact health/readiness
identity, TLS, redirect, and fail-closed behavior are unchanged. Its same-commit CI,
immutable tag, trusted npm stage, registry signature/provenance, and clean public
installation checks passed. The remote Hub peer reported from a bounded read-only
fresh-install preflight that the package's default `Python-urllib/*` request
identity received HTTP 403 on all three public routes. No 0.1.38 Hub installation
occurred.

Corrective candidate `0.1.39` changes public health request identity: it sends an
explicit GET with `User-Agent: AgentNet/0.1.39` and `Accept: application/json`.
The remote Hub peer reported from a bounded read-only comparison that the default
urllib identity returned 403 on 3/3 routes while the explicit product identity
passed edge classification and reached the expected offline-origin 502 class.
This is peer-reported corroboration, not retained reproducible release proof.
Proxy disabling, redirect rejection, system TLS and hostname verification, the
two-second per-attempt timeout, response bound, exact JSON identity/readiness, and
finite retry behavior remain unchanged.

The candidate also repairs a local-conformance-only composition gap: intentional
`deterministic_only` lab harnesses can now traverse the existing narrow signed C0
authorization, recipient resolution, and exact custody-acknowledgement path without
being promoted to `active`. The production policy engine still rejects those
harnesses. A fresh npm-tarball installation has run separate signed client and
Core processes over loopback, with the recipient client absent until after Core
restart. It proved `accepted_local`, proof-derived actor attribution, idempotent
request/receipt convergence, `recipient_committed`, typed response-obligation
completion across restarts, fresh refusal after a lab-only credential fixture,
and temporary-state cleanup. This is local synthetic evidence only—not the bounded
C0 pilot, approved revocation, OIDC/WebAuthn enrollment, ordinary server-agent
operation, PostgreSQL durability, or a production/ship claim.

This candidate is for fresh clean-state setup only and adds no release-marker
migration edge. Same-commit cross-platform, clean-setup, exact released-marker
rejection, and packaged-local-communication CI, immutable tag, trusted npm stage,
public bytes, and fresh Hub setup remain pending separately approved actions. No
Hub deployment, reset, database, enrollment, authority, native C0, federation,
production, or gate mutation is implied.

Candidate `0.1.43` consolidates the unpublished corrective sequence and the
narrow first-C0 evidence. A disposable ordinary server and separately stored
laptop harness completed real Google workforce OIDC plus owner WebAuthn UV,
remained identity-only before one fixed approved plan, then completed one native
request/reply/ACK round trip and exact revocation of all five temporary
communication entitlements. The retained historical deployment used source
`d8884b6c03a0dd38baab03386982aae8ad11dd58`; it did not include the later
credential-open correction.

The candidate accepts strict remote-browser bootstrap evidence without weakening
local-browser evidence, preserves the systemd responder's P-256 private key as
bytes, permits only its package-owned credential file, and opens that file with
`O_NONBLOCK` before regular-file custody validation. A dedicated regression
demonstrably failed before the nonblocking correction and the responder file
then passed 23 tests. Local focused and broad-source gates passed. Fresh npm
tarballs from both recursive generations repeated the broad source lane, and
the retained-content generation completed the real installed-byte local
communication journey with an empty workspace. The candidate evidence manifest
records final release-manifest/direct-verifier and retained byte-identical
archive outcomes. Same-commit CI, immutable tag, trusted npm stage,
staged-package remote deployment, and publication remain required external
actions; none is inferred from the historical `0.1.42` run.

Package `0.1.43` was subsequently published from that immutable source commit
with npm trusted-publisher provenance, a registry signature, and SLSA
attestation. Publication did not authorize deployment or promote a gate.

Candidate `0.1.44` adds a loopback-only private administration dashboard, one
fresh-approval same-principal communication-scope activation, and a
parent-owned Manager gateway that gives sandbox children only short-lived
measured local capabilities. The dashboard revalidates the exact current
harness credential and authority per request, keeps credentials out of URLs,
binds every mutation to a single-use method/path/body token, and emits only
content-free passive update state. Manager operations cross the canonical
signed AgentNet HTTP boundary; prompt text and child arguments cannot establish
identity or authority.

Local affected and broad source lanes report 322 passed with seven expected
dedicated-PostgreSQL skips and 1754 passed with 16 expected
platform/dedicated-PostgreSQL skips, respectively. Authenticated browser smoke
and a Lighthouse accessibility score of 100 cover the dashboard path.
Recursive installed-package checks, same-commit CI, staged remote deployment,
production IdP/WebAuthn, privileged process-boundary, and owner evidence remain
required. No requirement or must-not-ship gate is promoted.

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
