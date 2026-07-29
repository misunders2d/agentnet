# AgentNet — Requirements Reference Checklist

Status: The exact 85-ID baseline is preserved; clearly labeled post-audit implementation/evidence annotations were added on 2026-07-13. Final design verification of the sealed baseline completed on 2026-07-12.
Purpose: Preserve the user's intent, guide research and debate, and verify that the final architecture covers every requirement.
Scope: A separate, agent-agnostic extension enabling secure communication and collaboration among heterogeneous agent harnesses and external A2A agents.

Final architecture: [specification.md](specification.md)
Verification meaning: the design covers every requirement; implementation, prototype, policy, conformance, and release gates remain explicitly open where listed.

## How to use this document

- `HARD`: mandatory outcome. A proposed architecture fails if it does not satisfy it.
- `PREF`: desired behavior that should be retained unless a clearly better solution is justified.
- `HYP`: current implementation hypothesis; the brainstorm must challenge and compare alternatives.
- `OPEN`: unresolved question or constraint that the brainstorm must address.
- Each requirement has a stable ID for final coverage and acceptance review.
- User requirements must remain distinguishable from recommendations produced during brainstorming.

## Success definition

The brainstorm must not merely restate this document. It must:

- discover missing requirements, constraints, threats, and edge cases;
- research relevant protocols and implementation options;
- debate and criticize assumptions, including mechanisms suggested by the user;
- compare viable architectures and their operational/security tradeoffs;
- produce a defensible recommended design;
- map every recommendation back to these requirement IDs; and
- finish with a verification pass showing satisfied, changed, rejected, and unresolved items.

## 1. Product boundary and interoperability

- [ ] **ARC-001 — HARD — Separate extension.** The capability must be delivered as a separate extension rather than being inseparably embedded in one agent harness.
  - Acceptance: The core communication system can be installed, configured, upgraded, and removed independently of any one supported harness. Corporate communication, identity, authorization, mailbox, file, and audit functions are self-hostable on company-controlled infrastructure and require no third-party managed cloud service.
- [ ] **ARC-002 — HARD — Agent agnostic.** The core design must not depend on the proprietary internals or conversation model of a single harness.
  - Acceptance: Common identity, messaging, authorization, file, and room semantics exist above harness-specific adapters. The implementation reuses suitable standards and open-source components behind owned interfaces instead of recreating proven discovery, transport, identity, room, cryptographic, storage, or workflow mechanisms.
- [ ] **ARC-003 — HARD — Frontier harness support.** The solution must support at least Claude, Codex, Pi, and Antigravity.
  - Acceptance: The final design specifies a concrete adapter/integration path and capability matrix for all four.
- [ ] **ARC-004 — HARD — Native A2A interoperability.** Agents must support the native A2A protocol sufficiently to communicate with official A2A-compatible agents on the web.
  - Acceptance: The design identifies supported A2A roles, discovery, authentication, task/message/artifact mapping, protocol versions, and compatibility tests.
- [ ] **ARC-005 — HARD — A2A is not the only internal transport.** The corporate network may use communication mechanisms better suited to trusted internal collaboration.
  - Acceptance: Internal architecture is selected through a component bake-off while preserving a standards-compliant A2A boundary. Candidate reuse includes AGNTCY OASF/Directory/SLIM, Matrix room/sync/client concepts, SPIFFE/SPIRE for server workloads, audited MLS, Cedar, PostgreSQL, existing artifact stores, and Temporal-style workflow machinery where justified. Reused components may implement mechanisms but never redefine corporate identity, authorization, intent, receipt, audit, isolation, or effect semantics.
- [ ] **ARC-006 — HARD — External trust isolation.** A public A2A peer must not automatically receive corporate-network identity, membership, authority, or data access.
  - Acceptance: The final design defines an explicit policy and trust boundary between public A2A interoperability and corporate membership.

## 2. Identity, enrollment, and anti-impersonation

- [ ] **ID-001 — HARD — Verified human principal.** Every enrolled agent/harness must be linked to a clearly identified real human, initially expected to use a verified user email.
  - Acceptance: Enrollment produces a durable binding between a human principal and an agent/harness identity.
- [ ] **ID-002 — HARD — Out-of-band human confirmation.** A real user controlling the claimed identity must approve enrollment through an independently authenticated channel such as Slack, Telegram, email, or an owner-controlled WebAuthn authenticator.
  - Acceptance: Merely possessing the agent connection or submitting an email cannot complete enrollment. Independence means the confirmation is authenticated independently of the enrolling harness and cannot be read or completed by that harness; it does not require a separate physical approval host. The default self-hosted profile may colocate Core, PostgreSQL, and approval under distinct OS identities while the human approves on an owner-controlled authenticator, and must report `independent_boundary_proven=false`. Separately administered approval hosting is an optional high-assurance tier, not an ordinary-onboarding prerequisite.
- [ ] **ID-003 — HARD — No self-asserted identity trust.** No caller may gain identity or authority by placing an email, username, agent ID, or role claim in a payload.
  - Acceptance: Altering claimed identity fields without valid cryptographic/authenticated proof never changes the verified caller.
- [ ] **ID-004 — HARD — Cryptographic binding.** Post-enrollment identity must be bound to cryptographic credentials or an equally strong authenticated mechanism.
  - Acceptance: Impersonation attempts using copied public identifiers fail closed.
- [ ] **ID-005 — HYP — Signing-key enrollment.** The current implementation exchanges signing keys during onboarding.
  - Acceptance: The brainstorm compares this with viable alternatives and either validates it or recommends a migration path.
- [ ] **ID-006 — HARD — Human and harness identities are distinct.** One human may enroll multiple Claude, Codex, Pi, Antigravity, or other harnesses; each harness/agent instance must remain separately identifiable.
  - Acceptance: A verified caller identity resolves both the responsible human principal and the exact enrolled harness/instance.
- [ ] **ID-007 — HARD — Harness-level revocability.** An individual harness identity can be revoked without necessarily revoking the owning human or that human's other harnesses.
  - Acceptance: A revoked harness can no longer authenticate even while sibling harnesses continue to operate.
- [ ] **ID-008 — HARD — Domain-scoped identity.** Federated identity must be scoped to a corporate trust domain rather than treating a raw email string as globally sufficient.
  - Acceptance: The same human or harness can participate in multiple networks without authority leaking between them.
- [ ] **ID-009 — OPEN — Credential lifecycle.** Define secure issuance, storage, rotation, expiry, recovery, device loss, compromise response, and human offboarding.
  - Acceptance: The final design contains explicit lifecycle and emergency-revocation procedures.

## 3. Verified caller and authorization

- [ ] **AUTH-001 — HARD — Verified caller on every operation.** Every message, request, room action, task, and file/artifact operation must carry an unambiguous verified caller identity.
  - Acceptance: No protected operation can execute without resolving its authenticated human principal and originating harness.
- [ ] **AUTH-002 — HARD — Identity bound to proof.** Caller identity must derive from authenticated transport, verified message signatures, or equivalent proof—not an untrusted payload claim.
  - Acceptance: Authorization code consumes verified security context rather than caller-supplied prose or fields.
- [ ] **AUTH-003 — HARD — User-level permissions.** Permissions attach to the verified user-email principal, not independently to each harness.
  - Acceptance: The user's authorized role is consistently available across their enrolled harnesses, subject to harness revocation and network scope.
- [ ] **AUTH-004 — HARD — Harness attribution retained.** Even though permissions are user-scoped, all actions must identify the originating harness for audit, revocation, and incident response.
- [ ] **AUTH-005 — HARD — Authority cannot escape the user boundary.** An agent cannot delegate its user's privileges to another human principal or subordinate agent owned by another user.
  - Acceptance: Delegation or task assignment never transfers the caller's data permissions.
- [ ] **AUTH-006 — HARD — Hub-side role gating.** Hubs with sensitive data must authorize each request against the verified human principal and relevant network context before accessing or releasing data.
- [ ] **AUTH-007 — HARD — Fail closed.** Missing, invalid, stale, revoked, ambiguous, or insufficient identity/authority must prevent protected access and disclosure.
- [ ] **AUTH-008 — HARD — Human-approved temporary elevation.** A manager user's laptop principal may receive temporary elevated privileges only after explicit confirmation by the human owner of an authorized admin agent.
- [ ] **AUTH-009 — HARD — Constrained elevation.** Temporary elevation must be scoped, time-limited, revocable, non-self-issued, and auditable.
- [ ] **AUTH-010 — OPEN — Approval policy.** Define which administrator humans may approve which elevations, whether multiple approvals are ever required, and what emergency override/revocation rules apply.

## 4. Organizational relationships

- [ ] **ORG-001 — HARD — Multiple administrators.** An agent may have one or more administrators above it.
- [ ] **ORG-002 — HARD — Multiple subordinates and downward assignment.** An agent may administer one or more subordinate agents. When a verified active administration relationship explicitly includes `may_assign` and an assignment is within its preapproved scope, the subordinate agent automatically accepts the task into its durable task mailbox without requiring a new human confirmation.
  - Acceptance: Automatic acceptance proves only that the task is durably queued. An omitted deadline is replaced before commit by an exact whole-second server-derived deadline capped by the complete assignment-scope duration and strictly before relationship expiry. Generic mailbox, conversation, and supervisor reads disclose only immutable metadata, digests, and a custody reference for task/task-linked-control events, including legacy records without the marker. Protected payload release is a separate recipient-owned operation: the exact transport-authenticated harness must first consume one current `task.process` TaskGrant authorization, durably acknowledge the bound local queue custody, retain an active conflict-free execution intent and current policy/credential/domain epochs, and commit one immutable disclosure receipt plus audit record before bytes become observable. Response-loss retries reauthorize current state and return that exact receipt without consuming another grant use. Payload visibility grants no inherited administrator authority and explicitly grants no tool or effect authority. Ordinary non-task messages retain their normal authorized payload behavior.
- [ ] **ORG-003 — HARD — Many-to-many hierarchy.** Administration is not restricted to a tree; the model must support a graph without ambiguous security decisions.
- [ ] **ORG-004 — HARD — Management does not imply data authority.** Manager/subordinate relationships must not silently transfer user-scoped permissions.
- [ ] **ORG-005 — HARD — Directional assignment and conflict resolution.** Downward assignment from an authorized administrator to its subordinate may be accepted automatically within the relationship scope. Upward or lateral assignment from a subordinate/manager agent to an administrator or peer must require explicit human confirmation unless a separate active relationship grants that sender `may_assign` authority over that recipient. Define behavior when multiple administrators issue conflicting instructions, revoke access, change room membership, or compete for the same subordinate.
  - Acceptance: Claimed titles and message prose never determine direction. The system evaluates the authenticated sender, recipient, active bilateral-consent evidence, relationship and lifecycle revisions, assignment scope, expiry, revocation, policy/domain epoch, and current endpoint-owner/credential bindings; unauthorized upward/lateral requests remain non-executable proposals in `pending_human` state.
  - Implemented conflict behavior: Every accepted task carries a complete canonical typed resource/operation/access/exclusivity intent. Incompatible open intents atomically place all exact members in `conflict_pending`, independent of arrival order. Only the subordinate endpoint's current human/guest positive-authority owner may submit a strict, revision/epoch-fenced partition of the complete member set into release and reject sets. A released member returns only to queued custody; adjudication never grants payload access, semantic processing, tools, or effects.
- [ ] **ORG-006 — OPEN — Relationship lifecycle.** Define who may create, approve, modify, expire, and remove management relationships.
  - Implemented safe mechanism (not owner approval): `POST /v1/relationships` creates a zero-authority, expiring proposal only. Normal activation requires a fresh, one-use, exact-transaction approval whose configured independent verifier derives the current subordinate owner kind and ID (`human` principal or host-local `guest`). A separately signed, recorded, bounded domain-policy exception can be consumed through a distinct activation path, but its existence does not approve who may use it. Either basis also requires one exact local activation intent, inserted pending before evidence consumption and completed only with the relationship compare-and-swap in the same transaction; later authority fails closed if that intent is missing, pending, noncanonical, stale, or inconsistent. This intent is durable local provenance, not an independently administered audit witness.
  - Implemented lifecycle fences: renewal uses a new relationship ID, the next coherent relationship revision, a predecessor snapshot, and fresh activation evidence; expiry, activation, supersession, subject exit, endpoint revocation, policy-exception signer authority drift, and separately entitled administrative revocation compare exact lifecycle and authority revisions so a stale/racing operation conflicts. Injected rollback and activation-versus-revocation race tests prove there is one versioned winner, no consumed proof without activation, and no authority resurrection. An active edge grants only exact scoped custody meta-actions, never data, semantic, tool, or effect authority.
  - Implemented clean-start boundary: immutable first-release storage schema version 1 contains the bilateral governance, consent, exception, lineage, activation-intent, and conflict relations directly. It contains no unilateral relationship table, quarantine conversion, legacy alias, or prototype-version upgrade. Current unreleased Core schema v5 adds the protected payload-release receipt in migration 2, guided OIDC enrollment continuation in migration 3, the bounded C0 bootstrap-plan contract in migration 4, and OIDC-begin/finite-credential lifecycle custody in migration 5; none retrofits relationship consent. The current N/N-1 SQLite path accepts only an exact catalog/checksum-verified v4 database for one-transaction upgrade to v5; PostgreSQL uses the same contiguous migration catalog. Missing, future, tampered, prototype, noncontiguous, or unsupported older state fails closed before authority use. A pre-release prototype database is never treated as consent. Rollback is restore to an absent target from a separately signed, verified compatible backup, never metadata downgrade or authority-row transplantation.
  - Still open/accountable-owner blocked: approve the eligible proposers, exception and administrative-override authorities, thresholds, circumstances, mandatory-versus-voluntary relationship rules, notices, review, appeal, retention, and production evidence. No current document treats these as approved.

## 5. Communication topology and collaboration modes

- [ ] **COM-001 — HARD — Agent-to-agent communication.** Direct communication between enrolled agents is supported.
- [ ] **COM-002 — HARD — Agent-to-hub communication.** Local/intermittent agents can securely communicate with server-hosted hub agents.
- [ ] **COM-003 — HARD — Hub-to-agent communication.** Hubs can address and deliver communication to enrolled agents.
- [ ] **COM-004 — HARD — Hub-to-hub communication.** Server agents/hubs can securely communicate and relay across hubs.
- [ ] **COM-005 — HARD — Group topologies.** One-to-many, many-to-one, and many-to-many communication are supported.
- [ ] **COM-006 — HARD — Direct conversations.** Corporate-style direct conversations between authorized participants are supported.
- [ ] **COM-007 — HARD — Manager communication.** Agents can communicate with their managers and subordinates while preserving caller identity and authorization boundaries. Communication remains bidirectional, but automatic task acceptance is directional under ORG-002/005: scoped administrator-to-subordinate assignments may auto-queue, while peer-to-peer and subordinate-to-administrator assignments require explicit human confirmation unless separately authorized.
- [ ] **COM-008 — HARD — Rooms and meetings.** Authorized agents can gather in persistent or temporary brainstorming rooms, meeting rooms, and other group collaboration spaces.
- [ ] **COM-009 — HARD — Participant clarity.** Each contribution in every direct or group context clearly identifies the verified human principal and originating harness.
- [ ] **COM-010 — OPEN — Room governance.** Define room creation, invitations, membership, moderation, history visibility, late-join behavior, external guests, archiving, and deletion.
- [ ] **COM-011 — OPEN — Conversation semantics.** Define threads, replies, mentions, tasks, handoffs, structured requests, cancellation, and completion acknowledgements.

## 6. File and artifact sharing

- [ ] **FILE-001 — HARD — First-class sharing.** Files and structured artifacts can be exchanged in every required direct, hub, and group topology.
- [ ] **FILE-002 — HARD — Identity and authorization.** Upload, relay, access, modification, and download are bound to verified caller identity and role policy.
- [ ] **FILE-003 — HARD — Offline survival.** Authorized files/artifacts survive recipient offline periods and become available after reconnection.
- [ ] **FILE-004 — HARD — Integrity.** Recipients can verify artifact integrity and provenance.
- [ ] **FILE-005 — OPEN — Storage and retention.** Define storage location, encryption, quotas, size limits, retention, deletion, versioning, deduplication, and legal/audit requirements.
- [ ] **FILE-006 — OPEN — Content safety.** Define malware scanning, dangerous-file handling, secret detection, sandboxing, and executable-content policy.

## 7. Availability and durable delivery

- [ ] **AVL-001 — HARD — Mixed availability.** The architecture supports intermittently connected laptop agents and continuously available server agents.
- [ ] **AVL-002 — HARD — Offline is normal.** Normal offline periods must not be interpreted as identity failure, revocation, or permanent system failure.
- [ ] **AVL-003 — HARD — Durable store-and-forward.** Messages and files addressed to an offline recipient survive and are delivered after that recipient returns online.
- [ ] **AVL-004 — HARD — Baseline logical Hub and future hubless profile.** The initial reliable profile has at least one always-on logical corporate Hub, self-hosted on company-controlled infrastructure, for authority, durable mailboxes, offline delivery, and reconciliation. The architecture must not make that topology permanent: a future opportunistic mesh may rely on whichever explicitly eligible enrolled agents are online, with encrypted peer-assisted mailbox/file replication and direct delivery.
  - Acceptance: The baseline distinguishes one logical Hub from one physical server and supports production redundancy. The future profile preserves signed identity, authorization epochs, revocation, globally unique IDs, idempotency, receipts, audit evidence, and non-transitive relay authority without requiring a permanent central server.
- [ ] **AVL-005 — HARD — No silent loss.** The system must expose a reliable state for accepted, queued, delivered, acknowledged, expired, rejected, and failed communication.
- [ ] **AVL-006 — OPEN — Delivery guarantees.** Define ordering, retries, acknowledgements, idempotency, deduplication, replay prevention, expiration, cancellation, and exactly-once versus at-least-once behavior.
- [ ] **AVL-007 — OPEN — Hub failure and hubless operation.** Define failover, replication, recovery, split-brain behavior, and operation when the recipient's usual Hub is unavailable. Preserve an upgrade path from Hub-backed operation to direct peer delivery, opportunistic eligible relays, and eventually a fully distributed domain with explicit quorum, partition, sequencing, policy-freshness, and revocation rules.
- [ ] **AVL-008 — OPEN — Presence.** Define how online/offline/away/capability status is established without confusing stale presence with current availability.

## 8. Non-interrupting user experience

- [ ] **UX-001 — HARD — Separate communication sessions.** Agent-to-agent communication executes in sessions separate from the user's active conversation sessions.
- [ ] **UX-002 — HARD — Never interrupt active flow.** Background communication must not steal focus, inject conversation turns, take over input, or otherwise interrupt a user talking to one or several local agent instances.
- [ ] **UX-003 — PREF — Minimal passive indication.** Where the harness permits, the user should see a minimal, passive, non-modal indication that background agentic communication is occurring.
- [ ] **UX-004 — HARD — Indicator remains non-interactive by default.** The background indicator must not insert message content or demand attention merely because communication is active.
- [ ] **UX-005 — OPEN — Attention policy.** Define which exceptional events may notify or interrupt the user, such as approval requests, security incidents, expiring elevation, or unrecoverable delivery failure.
- [ ] **UX-006 — OPEN — Harness capability fallback.** Define behavior for harnesses that cannot display a passive activity indicator or cannot host a true background session.

## 9. Federation and contractors

- [ ] **FED-001 — HARD — Multi-company future.** The architecture must anticipate communication and collaboration across corporate trust domains.
- [ ] **FED-002 — HARD — Outbound contractor access.** Internal users/agents may receive scoped permission to use another company's corporate agent network.
- [ ] **FED-003 — HARD — Inbound contractor access.** External contractor agents may be temporarily admitted to the internal network with minimal privileges.
- [ ] **FED-004 — HARD — Least privilege.** Guest access is limited to explicitly authorized networks, resources, rooms, operations, and time periods.
- [ ] **FED-005 — HARD — No transitive trust.** Admission to one corporate network must not confer access to another network or to the guest's home network.
- [ ] **FED-006 — HARD — Domain context per operation.** Every federated operation identifies the trust domain under which identity and authorization are evaluated.
- [ ] **FED-007 — OPEN — Federation model.** Compare bilateral trust, identity federation, invitation/voucher systems, cross-signed credentials, brokered trust, and other viable approaches.
- [ ] **FED-008 — OPEN — External identity assurance.** Define who vouches for a contractor's human and harness identity, how much the host trusts that assertion, and whether local re-verification is required.
- [ ] **FED-009 — OPEN — Cross-domain auditing and revocation.** Define audit ownership, incident coordination, immediate guest revocation, and behavior when the home domain revokes an identity.

## 10. Security, privacy, and accountability gaps to resolve

- [ ] **SEC-001 — OPEN — Threat model.** Cover impersonation, credential theft, compromised harnesses, compromised hubs, malicious insiders, replay, confused-deputy attacks, privilege escalation, cross-domain leakage, denial of service, and supply-chain attacks.
- [ ] **SEC-002 — OPEN — Encryption.** Define transport, at-rest, and potential end-to-end encryption requirements, including hub visibility and key management.
- [ ] **SEC-003 — OPEN — Auditability.** Define tamper-evident records for enrollment, identity changes, communication metadata, protected requests, access decisions, temporary elevation, federation, and revocation.
- [ ] **SEC-004 — OPEN — Privacy and minimization.** Define which message content and metadata hubs may inspect, retain, index, or disclose.
- [ ] **SEC-005 — OPEN — Replay and freshness.** Define nonces, timestamps, sequence handling, clock-skew tolerance, and replay windows.
- [ ] **SEC-006 — OPEN — Compromise containment.** Define emergency kill switches, blast-radius limits, credential rotation, quarantine, and safe restoration.
- [ ] **SEC-007 — OPEN — Extension trust.** Define signing, secure updates, dependency/supply-chain controls, sandboxing, and adapter isolation for the extension itself.

## 11. Operations and implementation gaps to resolve

- [ ] **OPS-001 — HARD — Hub definition and replaceability.** Define the initial logical Hub as separable authority, mailbox, policy, artifact, effect/data, gateway, and audit roles. It must be self-hostable, redundantly deployable, and accessed through transport-neutral interfaces so future direct and hubless profiles can replace centralized routing without replacing corporate identity or event semantics.
- [ ] **OPS-002 — OPEN — Discovery.** Define how agents, hubs, capabilities, rooms, trust domains, and A2A endpoints are discovered and authenticated.
- [ ] **OPS-003 — OPEN — Versioning.** Define protocol/schema negotiation, backward compatibility, rolling upgrades, and behavior with partially capable harness adapters.
- [ ] **OPS-004 — OPEN — Observability.** Define health, queue, delivery, latency, error, authorization-denial, and security telemetry without leaking sensitive content.
- [ ] **OPS-005 — OPEN — Resource controls.** Define quotas, rate limits, backpressure, abuse controls, file limits, and protection from runaway agent conversations.
- [ ] **OPS-006 — OPEN — Portability and infrastructure independence.** Define supported operating systems, installation model, configuration portability, secure local credential storage, and self-hosted deployment. No AWS, Azure, Google Cloud, external identity SaaS, managed message broker, or other third-party infrastructure service may be mandatory; provider-specific services are optional adapters only when the owner explicitly selects them.
- [ ] **OPS-007 — OPEN — Test strategy and reuse bake-off.** Define conformance, interoperability, security, failure-recovery, offline-delivery, federation, and harness-specific integration tests. Before custom-building a mature subsystem, evaluate relevant maintained implementations with pinned versions, license/supply-chain review, semantic mapping, failure and revocation tests, operational cost, portability, and an exit strategy. Reuse only when the component passes the existing security gates without semantic workarounds.

## 12. Final brainstorm verification matrix

The individual 85-row verification is in [concept §20](specification.md#20-requirement-by-requirement-verification). “Design covered” does not mean implemented or release-ready.

| Requirement | Status | Design reference | Verification evidence | Notes / approved change |
|---|---|---|---|---|
| ARC-* | Design covered; adapter/A2A gates pending | Concept §§4–6, 20.1 | Three convergence memos; DR-001–003 | A2A is the isolated external boundary; MCP is only an optional local binding |
| ID-* | Design covered; policy and platform gates pending | Concept §7, 20.2 | DR-005, PD-001/002/003 | ID-005 changed: keys retained within OIDC + exact OOB + lifecycle, not bare exchange |
| AUTH-* | Design covered; policy/benchmark gates pending | Concept §8, 20.3 | DR-006/007; PD-003/004 | Positive authority remains human-level; harness constraints are deny-only |
| ORG-* | Bilateral lifecycle and ORG-005 conflict adjudication mechanics implemented; ORG-006 owner policy pending | Concept §8.2, 20.4 | Local lifecycle/migration, arrival-order-independent conflict, race, adjudication, and PostgreSQL tests; no owner or production gate evidence | Proposals have zero authority; active relations and released conflicts grant explicit queued custody only, never implicit data, semantic, tool, or effect authority |
| COM-* | Design covered; state/room/federation tests pending | Concept §§9–10, 20.5 | DR-008/009/012 | Direct, hub, group, room, task, thread, cancellation, and receipt semantics defined |
| FILE-* | Design covered; scanner/provenance/policy gates pending | Concept §11, 20.6 | DR-014; PD-005/006/007/010 | Immutable versions and separate lineage/scan/release evidence |
| AVL-* | Design covered; durability/RPO/RTO gates pending | Concept §§9.4–9.6, 14, 20.7 | DR-004/012; PD-009/010 | AVL-004 changed: replicated transactional mailbox, not one monolithic hub queue |
| UX-* | Design covered; four-harness tests and policy pending | Concept §5, 20.8 | DR-001; PD-011 | Routine foreground digest rejected; dedicated workers plus content-free status only |
| FED-* | Design covered; disabled until pilot and policy gates pass | Concept §12, 20.9 | DR-010; PD-008/009 | Bilateral host-local guests, pairwise keys, no transitivity |
| SEC-* | Design covered; red-team/crypto/audit/supply-chain gates pending | Concept §13, 20.10 | DR-011/013/014/015 | Hub visibility and E2EE are explicit data-class policy |
| OPS-* | Design covered; deployment/restore/scale gates pending | Concept §§14–15, 19, 20.11 | DR-016; PD-010/011 | Linux-first modular core; logical trust-role separation |

## Remaining questions for the brainstorm

These did not block the baseline. Their final dispositions are:

1. **Hub roles:** decomposed into mailbox, policy, room/task authority, artifact, active/effect worker, gateways, and audit roles with separate credentials; see concept §14.3.
2. **Retention/privacy/legal:** explicit per room/data class/artifact/audit policy; exact values remain PD-006/007/010 owner decisions.
3. **Human attention:** only exact approvals, confirmed incidents, expiring high-risk elevation, and terminal unrecoverable failure by default; see PD-011.
4. **History/artifacts:** internal managed default from join; guests and sealed rooms no prior history; removal stops future/undelivered access; current authorization on every download; see concept §§10–11.
5. **Delivery:** at-least-once transport, durable idempotency, domain-owned receipts, no global ordering, explicit effect_unknown and reconciliation; partition durability must match the state name; see concept §9.
6. **Approval/recovery:** independent exact-transaction approval, predeclared thresholds, fresh authentication, no self-approval, explicit new binding after key loss, tombstone instead of invented room authority; see concept §§7–10.
7. **Partner assurance:** bilateral profile; home proof for ordinary access, host-local reproof for high risk; host-issued identity/grant and kill switch; see concept §12 and PD-008/009.
8. **Platform/update:** Linux x86_64/arm64 first; signed threshold updates, SBOM/provenance, canary/rollback; macOS/Windows graduate separately; see concept §§13–15 and PD-010.
9. **Relationship governance:** exact bilateral proposal/consent, policy-exception recording, renewal, expiry, subject exit, admin-revoke mechanism, clean-start schema-v1 creation, and typed arrival-order-independent `conflict_pending` adjudication with exact owner release/reject fencing are implemented. ORG-006 remains open until an accountable owner approves eligible roles, exception/override circumstances and thresholds, mandatory-relationship behavior, notice/review/appeal, retention, and production use.
