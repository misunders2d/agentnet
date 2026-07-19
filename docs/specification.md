# AgentNet — Consolidated Specification

**Status:** Research-backed architecture with local implementation; production and accountable-owner gates remain open  
**Date:** 2026-07-13  
**Scope:** A separate, agent-agnostic extension for Claude, Codex, Pi, Antigravity, future harnesses, and standards-compliant A2A agents  
**Requirements baseline:** [requirements.md](requirements.md)  

## 1. Outcome

Build a **domain-owned corporate agent communication network** with:

- a signed **Device Communications Supervisor** installed independently on every laptop or server;
- thin, credential-free adapters for each agent harness;
- one dedicated background communication worker per enrolled harness, isolated from active user conversations;
- a transactional corporate core backed by highly available PostgreSQL and a provider-neutral immutable artifact store;
- an explicit verified actor type on every event: business/protected submissions use a verified human or host guest plus exact harness; workload control facts and unadmitted A2A facts use restricted, visibly different authority chains;
- user-level positive permissions, harness-level deny-only eligibility and attribution, and no authority transfer across human boundaries;
- durable store-and-forward mailboxes, explicit receipts, at-least-once transport, idempotent processing, and first-class unknown-effect handling;
- direct messages, tasks, threads, rooms, meetings, files, manager/subordinate relationships, groups, and server-agent-to-server-agent delivery;
- an isolated native A2A gateway for public web-agent interoperability;
- bilateral, host-controlled contractor federation;
- managed server-service-visible content by default, with a separately approved MLS end-to-end-encrypted profile for sealed rooms;
- deterministic security and protocol processing outside all LLMs, with isolated effect workers, human approval, quotas, and model-wake budgets.

This is **not an MCP network**. MCP is useful as one local harness binding where a harness natively supports it. The shared extension API is the supervisor’s authenticated local API. A2A is the external interoperability boundary. The internal corporate fabric uses purpose-built identity, authorization, messaging, room, file, receipt, and audit semantics.

The recommended first production profile is **single-domain, single-region,
Linux-first, managed-service-visible, mailbox-first, broker-free, and self-hosted
on company-controlled infrastructure**. The same ordinary AgentNet extension
runs on laptops and always-on server agents; there is no separate Hub product,
privileged Hub identity, or second package. Production may deploy redundant
instances of the server-agent roles and storage rather than trusting one
physical machine. JetStream, Kafka, SPIFFE/SPIRE, MLS, SCITT, multilateral
federation, and multi-region active-active are triggered upgrades, not launch
dependencies.

No external managed infrastructure is mandatory. “Object storage” means a provider-neutral artifact interface, not AWS S3: a pilot may use a hardened filesystem-backed content-addressed store, and production may use a self-hosted replicated object store. AWS S3, Cloudflare R2, Azure Blob, or Google Cloud Storage are optional owner-selected adapters. Corporate identity may use an existing or self-hosted OIDC provider. External connectivity is necessary only for capabilities the owner deliberately enables, such as public A2A peers, partner domains, or remote model providers.

Implementation is **reuse-first, not greenfield-first**. The project owns its corporate authority model and integration contracts, but should adopt maintained, audited implementations for discovery, transport, workload identity, room synchronization, cryptography, policy evaluation, storage, and workflow recovery whenever they pass the same gates. The rule is: **reuse mechanisms; retain policy ownership**. No component is adopted merely because its vocabulary resembles the design, and no custom replacement is written before a documented bake-off shows that existing choices cannot satisfy the required semantics safely and operably.

## 2. Evidence and decision language

The brainstorm used three independent research passes, three cross-critiques, a merged dispute register, fresh re-research of every material dispute, three convergence memos, and an adversarial audit-and-correction cycle. Final audit artifacts and their sealed verdicts are recorded only after the corrected bytes are re-reviewed.

- **Verified:** established by a current primary specification, official product documentation, or dated local read-only probe.
- **External-evidence-gated:** the local design or implementation exists, but required target-environment tests, benchmarks, or independent evidence remain open.
- **Owner policy:** research establishes the tradeoff, but an accountable human owner must approve the value or behavior.
- **Design-covered:** the architecture answers a requirement; it does not mean code exists or that release acceptance has passed.

Authoritative research artifacts:

- [Codex first pass](brainstorm-codex-pass1.md), [critique](brainstorm-codex-critique.md), [resolution](brainstorm-codex-resolution.md), [convergence](brainstorm-codex-convergence.md)
- [Claude first pass](brainstorm-claude-pass1.md), [critique](brainstorm-claude-critique.md), [resolution](brainstorm-claude-resolution.md), [convergence](brainstorm-claude-convergence.md)
- [Pi first pass](brainstorm-pi-pass1.md), [critique](brainstorm-pi-critique.md), [resolution](brainstorm-pi-resolution.md), [convergence](brainstorm-pi-convergence.md)
- [Merged dispute register](brainstorm-dispute-register.md)

## 3. Non-negotiable invariants

1. **No claimed identity is trusted.** Email, user ID, harness ID, role, manager, capability, and domain fields in a payload are untrusted until bound to authenticated security context.
2. **Every business submission resolves both identities:** the responsible verified human or host-local guest and the exact originating harness. Service-owned receipts and deterministic control facts use a scoped workload identity and may assert only facts that workload owns; any service data access or effect references an authorized parent human event or exact task grant. Unknown public A2A actors are labeled external-human-unverified and are never displayed as corporate participants.
3. **Positive authority belongs to the human principal.** A harness, session, subagent, delegation, task, or management edge can only preserve or reduce that ceiling.
4. **Management is not data authority.** Administering, supervising, sponsoring, moderating, or assigning another agent does not inherit that other human’s permissions.
5. **No authority crosses the human boundary.** Delegating work never delegates data access.
6. **Protected access fails closed** on missing, ambiguous, stale, revoked, inconsistent, or insufficient identity, policy, grant, room, guest, or credential state.
7. **Routine agent communication never changes an active user conversation.** It cannot inject context, create turns, steal focus, prompt for permission, or take over input.
8. **The foreground indicator is content-free and optional.** If a harness lacks a safe passive surface, the correct fallback is no indicator.
9. **Protocol state never lives only in an LLM transcript.** Worker death, compaction, restart, or context loss cannot lose accepted communication.
10. **Authentication does not make content trustworthy.** A signed peer can still send prompt injection, malware, false claims, or costly loops.
11. **A receipt proves only what its signer owns.** Missing evidence is unknown, not success.
12. **No false exactly-once claim.** Transport is at least once; idempotency and reconciliation provide safe effects where possible.
13. **No false durable claim.** The state name must match the tested durability boundary.
14. **External A2A identity is not corporate identity.** Public peers remain low trust until separately admitted through corporate onboarding.
15. **Federation is bilateral and non-transitive.** Trust from A to B cannot create trust from B to C.
16. **Human consent is independent of the requesting harness.** Peer or model content may request approval but can never satisfy it. Independence is an authentication property, not a mandatory extra-host topology.
17. **A separate conversation is not a sandbox.** A semantic worker may process hostile content only inside the enforceable clean-worker launch profile; otherwise that harness is restricted to deterministic protocol handling.

## 4. Architecture

~~~text
Human/workforce authority                   Independent human approval
OIDC (issuer, subject) + email alias         WebAuthn + exact transaction
             |                                          |
             +------------- Enrollment Authority -------+
                                      |
Device trust zone                    |                 Corporate trust domain
+----------------------------------+  |  +--------------------------------------+
| Device Communications Supervisor|--+->| Edge/API policy enforcement point     |
| - encrypted local inbox/outbox   | DPoP| - verified human+harness resolution |
| - key/credential broker          |     | - replay, schema, SSRF, quota gates  |
| - deterministic dispatcher      |     +------------------+-------------------+
| - cursor/dedup/replay state      |                        |
| - budgets and worker control     |     +------------------v-------------------+
|       |                          |     | Transactional Corporate Core         |
| zero-secret harness adapters     |     | identity / policy / relationships    |
|       |                          |     | mailbox / rooms / tasks / receipts   |
| dedicated comms workers          |     | audit intents / transactional outbox |
+----------------------------------+     +----+--------------+--------------+----+
                                            |              |              |
                                      +-----v------+ +-----v------+ +-----v-----+
                                      | Artifact   | | Effect /   | | Audit     |
                                      | service    | | active     | | exporter  |
                                      +-----+------+ | agent      | +-----+-----+
                                            |        +------------+       |
                                      +-----v------+                  independent
                                      | Scanner /  |                  WORM/witness
                                      | transformer|
                                      +------------+

External zone: Public A2A gateway --- typed low-trust API ---> Corporate Core
Partner zone:  Federation gateway --- host-local guest API --> Corporate Core
~~~

### 4.1 Device Communications Supervisor

One independently installable, signed supervisor runs per machine. It owns:

- an encrypted crash-safe local inbox/outbox, preferably SQLite with WAL and explicit fsync tests;
- one domain-local binding for every enrolled harness installation;
- secure credential use and rotation;
- authenticated local IPC and adapter capability tokens;
- durable cursors, replay state, deduplication, reconnect, and backoff;
- deterministic receipt, presence, retry, expiry, cancellation, and capability handling;
- one dedicated background communication worker per harness;
- per-peer, room, recipient, and causal-chain budgets;
- content-free activity counts for safe harness UI surfaces;
- explicit inbox/read commands initiated by the human.

The supervisor is a lifecycle and credential-containment boundary. A process owned by the same desktop user is not assumed to be a strong adversarial boundary. Prefer supervisor-inherited anonymous capabilities, distinct OS identities, LSM labels, containers, or measured/code-bound launch evidence. Every event records binding_assurance and compromise_domain_id. If hostile same-user tests cannot prove exact adapter binding, that harness may only draft locally, show content after an explicit human open, and perform deterministic non-business protocol control. It may not originate any protected business message, task, room/file action, read, download, key release, export, disclosure, or effect in Phase 1. GA support requires a tested inherited anonymous capability plus process/ptrace/proc-dump restrictions and an OS/LSM/measurement boundary. Human approval cannot reconstruct an unprovable originating harness.

### 4.2 Harness adapters

Adapters are thin shims. They contain no long-lived corporate token or private key, do not decide authorization, and do not own protocol truth. The supervisor binds harness identity from authenticated IPC and the launched worker context, not from tool arguments or model prose.

### 4.2.1 Clean-worker launch profile

A dedicated thread protects foreground UX but does not itself isolate hostile content. Before any harness may process the semantic lane, the supervisor must create a version-specific clean worker with:

- a new empty non-user workspace and sanitized HOME, environment, current directory, and session store;
- no inherited project AGENTS.md, CLAUDE.md, hooks, plugins, skills, MCP configuration, startup scripts, shell profile, or workspace code; exactly one supervisor-provided fixed-schema binding is permitted;
- no shell, arbitrary filesystem, desktop keychain/secret store, browser session, vendor credential, local socket enumeration, or unrestricted process access;
- no arbitrary network or DNS; only the authenticated supervisor sidecar and explicitly allowlisted model endpoint;
- a distinct OS identity or container plus LSM/seccomp or platform equivalent when the harness cannot enforce a model-only tool profile;
- read/write/secret/IPC/process/DNS/network escape tests pinned to the exact harness version.

The worker receives only tainted message inputs, allowed reference bytes, an attenuated task grant, and fixed-schema supervisor tools. Pi may process hostile semantic content only after an OS sandbox or genuinely model-only profile passes. Any harness that cannot meet this profile remains useful for deterministic protocol handling and explicit human-opened viewing, but cannot autonomously process untrusted semantic content or perform effects.

The clean worker receives no vendor credential or user auth state. A supervisor-owned model-egress broker alone holds upstream authentication. It accepts only framed inference requests bound to one worker, exact task grant, allowed model, token/spend budget, and clean profile; exposes no generic proxy, connector, or tool; strips upstream credentials; returns only model output; and logs/revokes a short-lived worker capability. Each adapter must prove its exact broker/auth path. If a harness cannot target the broker without reading user auth/session state, it is deterministic-only.

### 4.3 Transactional Corporate Core

The core is initially a modular application with one authoritative PostgreSQL transaction boundary for:

- human, harness, workload, guest, credential, and domain identities;
- management and room relationships;
- Cedar entity snapshots, policy versions, and decisions;
- temporary grants and approval consumption;
- message, task, room, recipient, and delivery states;
- idempotency keys and payload-digest conflicts;
- receipt references and effect reservations;
- artifact metadata and provenance references;
- transactional outbox rows;
- transactionally complete audit intents for mutations mediated by the core.

Logical trust roles stay separate even when modules share a deployment. In the default self-hosted profile, Core, PostgreSQL, and approval may share the existing server under distinct OS identities, credentials, storage, and loopback services; the human WebAuthn authenticator remains outside the enrolling harness. Scanner, effect worker, public gateway, federation gateway, authority keys, and audit witness retain their explicit role boundaries. A separately administered approval host is an optional high-assurance profile, not an ordinary-onboarding prerequisite.

### 4.4 Object, scanner, effect, and audit services

- **Artifact service:** immutable versions, encrypted quarantine/released namespaces, digest addressing, lifecycle, quota, retention, and optional legal hold.
- **Scanner/transformer:** isolated workload with bounded parsers, decompression, CPU, memory, egress, and no release authority. It signs digest-bound scan and derivation attestations.
- **Effect/active-agent worker:** receives only typed, currently authorized jobs. It cannot grant access, approve itself, rewrite receipts, or access issuer/audit keys.
- **Audit exporter:** publishes core-mediated transactional audit intents to an independently administered append-only or WORM sink with signed checkpoints and witnesses.

Audit assurance is bounded. A witness detects later alteration, sequence gaps, stalls, or inconsistent checkpoints; it cannot prove that a compromised core, PDP, issuer/KMS, approval service, database administrator, or object-store administrator submitted every real event. These are catastrophic trust roots. Unmediated protected database/object mutations are prohibited; independent IdP, KMS, database, object, network, approval, and gateway logs are reconciled. Use purpose-specific origin signatures where authenticity must survive a compromised accepting server role.

Before any sensitive disclosure, preview, C2 decrypt, export, download-capability issuance, or decryption-key release, the core transactionally commits a digest-linked access-decision/audit intent containing current policy, entity, grant, actor, harness/workload, credential, and data-class epochs. If the audit intent or bounded publication policy cannot accept it, the read/disclosure is held. No sensitive bytes, capability, or key are released first.

After core compromise, quarantine the affected epoch, stop protected effects and new authority, preserve and compare independent logs/checkpoints, rotate/rebind authority keys, rebuild from verified backups plus external evidence, and require explicit adjudication before releasing uncertain events.

### 4.5 Gateways

- **Public A2A gateway:** separate network/process identity, low-trust queue, restricted egress, and no broad database, tool, KMS, or object-store authority.
- **Federation gateway:** separate partner-facing identity and policy boundary; disabled until contractor and revocation gates pass.

## 5. Harness capability and adapter plan

The research baseline was locally reinspected on 2026-07-13 at Claude Code 2.1.207, Codex CLI 0.144.3, Pi 0.80.6, and Antigravity CLI 1.1.1. These exact observations are dated evidence, not permanent product contracts. Every version change reruns the adapter suite.

| Harness | Dedicated background path | Passive foreground behavior | Important gate/fallback |
|---|---|---|---|
| **Claude** | Supervisor drives a dedicated Agent SDK or headless claude worker. An organization-approved Channel over stdio may feed only a dedicated communication session. | Content-free statusline count. | Custom Channels are preview/allowlist constrained. Do not require a dangerous development flag in production; use headless worker until approved. Never expose remote permission relay. |
| **Codex** | Supervisor starts codex app-server over stdio/private Unix socket, creates or resumes a dedicated thread, and starts semantic turns only there. | No verified in-TUI passive badge; show nothing by default. | Independently deny or route worker approval requests. Do not depend on experimental non-loopback or process APIs. |
| **Pi** | Supervisor starts pi --mode rpc with a separate session directory and strict JSONL framing. | Credential-free extension may show content-free setStatus/widget/footer. | Pi intentionally has no native MCP client and no sandbox. Use direct authenticated IPC. Never send routine steer, followUp, nextTurn, or user-message content into the foreground. |
| **Antigravity** | Supervisor serializes agy --conversation with a dedicated conversation and noninteractive prompts. | Built-in background-task indicator only if reproduced safely; otherwise nothing. | Persistent push, resume, cost, permission routing, and status mapping remain external-evidence-gated. AX gRPC is a later adapter candidate, not A2A. |

### 5.1 UX decision

Routine automatic “digest on the next user turn” is **rejected as the default**. Although all four harnesses expose mechanisms that can add context to a user-initiated turn, doing so still changes the active model context and creates a prompt-injection and attention leak.

The compliant model is:

1. agent communication is received and processed in a dedicated background worker;
2. the foreground may display only a passive, non-modal count or activity state;
3. where no safe indicator exists, display nothing;
4. content enters a foreground session only after the user explicitly opens or reads the inbox;
5. exceptional approval/security/failure notifications use the separately approved PD-011 policy and contain no message content by default.

For Codex at the researched version, this means **explicit-pull only**: no compliant passive in-TUI indicator was verified, so UX-003 remains unmet on that harness while UX-001/002/004 stay protected. A non-blocking probe may test a supervisor-owned terminal title or tmux status segment with content-free count, provided escape-sequence/TUI behavior cannot corrupt or distract the session. Codex cloud tasks are a verified future offload option, but they are not the durable mailbox, identity, receipt, or foreground-notification path.

## 6. Protocol roles

### 6.1 Native A2A

A2A is the standards-compliant public interoperability boundary, not the internal corporate network.

This is the explicit ARC-004 interpretation: each enrolled agent can communicate with native A2A peers **through the domain gateway acting on that agent’s behalf**. The four harnesses do not each embed an independent A2A stack. Centralizing the native client/server implementation preserves agent-level interoperability while enforcing ARC-005/006 consistently. If the owner instead requires direct per-harness wire participation, that is a materially different deployment and security requirement.

- Pin the A2A v1.0.1 release and normative proto; the wire version is 1.0.
- Missing or empty version maps to legacy 0.3 and is rejected on the protected v1 endpoint. No silent downgrade.
- If legacy support becomes necessary, isolate hostname/path, credentials, audience, queues, policy, and telemetry.
- Support tested Message, Task, Artifact, streaming/subscription, push, discovery, and authentication mappings.
- Every unknown peer begins as external-low-trust.
- An Agent Card is self-asserted discovery metadata. A valid signature under a locally trusted, current registry key authenticates the canonical card bytes only; it grants no membership, human identity, authority, tool access, or rollover. Card signatures are optional in native A2A.
- Protected partners require an administrator-curated registry of endpoint, key thumbprints, algorithms, owner, audience, status, rollover authority, review date, and incident contact.
- Card-provided key URLs are never automatic trust bootstrap.
- Foreign IDs remain in an A2A namespace and never become corporate principal IDs.
- Cards, callbacks, push URLs, and file URLs receive SSRF-safe fetch controls, redirect/DNS/IP revalidation, allowlists, and size/time limits.
- Run the pinned A2A TCK plus negative security, downgrade, key-rotation, cache, tenant-spoofing, task-enumeration, cross-SDK, webhook, and artifact tests. TCK success alone is not a security certification.

Recommended gateway SDK order is stable Go or Python after cross-SDK tests, then Java where appropriate. JavaScript v1 and .NET v1 surfaces were not mature enough at the research cutoff to be the only production dependency.

Phase-1 binding profile: the gateway client and server implement and advertise HTTP+JSON and JSON-RPC over HTTPS with A2A wire version 1.0; HTTP+JSON is preferred when both peers advertise it. gRPC is optional until its SDK/TCK path graduates. A missing binding intersection, wrong media type, unsupported version, or unadvertised binding fails explicitly—never by downgrade or heuristic transcoding. Each exported logical agent has its own standards-compliant Agent Card/profile and supportedInterfaces under an authorization-filtered gateway route; optional tenant routing is never identity or authority.

#### Corporate-to-A2A profile

| Concern | Normative gateway behavior |
|---|---|
| Logical-agent addressability | Each exported logical agent has an authorization-filtered opaque route and standards-compliant Agent Card/supportedInterfaces under the domain gateway; it never exposes the corporate directory. Publishing the route creates a standing, scoped, revocable exact task grant: source class external-low-trust, allowlisted actions/resources/sinks, budget, expiry, and revocation handle. No grant means no exported route. |
| Client role | An outbound authorized internal event creates an A2A client operation under a gateway credential scoped to the remote origin/resource. Corporate tokens and human credentials never cross the gateway. |
| Server role | An inbound A2A request terminates at the low-trust gateway actor. It does not run as the target human or harness and cannot access internal tools/data until a separately typed, currently authorized internal task is created. |
| Message mapping | Internal conversational content maps to an A2A Message with namespaced IDs and typed/text/file Parts. Inbound Message content is inert/tainted semantic input; metadata and extensions never grant authority. |
| SendMessage result | SendMessage may return a server-owned Task **or a direct Message**. A Task maps to the internal task projection. A direct Message maps to one remote-response fact without synthesizing a Task, Artifact, completion, durable recoverability, or GetTask handle. |
| Task states | submitted, working, input-required, auth-required, completed, failed, canceled, and rejected map to separate internal facts. TASK_STATE_UNSPECIFIED maps to remote_state_unknown plus quarantine/no effect or completion. input-required/auth-required never trigger automatic approval or credential disclosure. |
| Artifact mapping | An A2A Artifact is treated only as task output. Uploaded/request files arrive as Message Parts and enter quarantine. Every byte, digest, size, media type, and object version is verified before use. |
| Context and IDs | contextId, taskId, messageId, extension IDs, and correlation fields are opaque, origin-namespaced context only. They are never corporate principal, room, grant, or idempotency authority. |
| Roles | user and agent roles map to explicit external actor directions. ROLE_UNSPECIFIED is rejected/quarantined and cannot create work, authority, or participant identity. |
| Push/streaming | Authenticated push or streaming is a wake hint. Map every StreamResponse variant separately: Task, direct Message, TaskStatusUpdateEvent, and TaskArtifactUpdateEvent. Task-bearing gaps reconcile through GetTask; a lost direct-Message stream is remote_response_unknown/incomplete and is never called GetTask-recoverable. Callbacks are deduplicated and verify origin/audience/replay. |
| Authentication | Exact card securityRequirements select a locally allowed scheme. OAuth uses exact resource/audience and credential-origin confinement; mTLS uses the curated peer registry. Credentials are acquired out of band and stripped before internal forwarding. |
| Unknown peer | Actor is external-human-unverified plus verified network/provider facts, not a corporate human. Corporate participant display and positive authority require a separate host admission ceremony. |

Gate 4 includes HTTP+JSON/JSON-RPC interface advertisement and preference, binding/media/version mismatch, per-agent Card/route and tenant mismatch, standing-grant revocation, Task versus direct-Message results, all StreamResponse variants, unspecified role/state, server-owned Task IDs, input/auth-required behavior, artifact direction, Task push duplication/gap recovery, direct-Message stream loss, credential-origin confinement, and cross-SDK tests in addition to the TCK.

### 6.2 MCP

MCP is an optional local tool/context binding:

- depend on stable MCP 2025-11-25 until a later revision is final and implemented;
- Claude, Codex, and Antigravity may bind explicit local tools through MCP;
- Pi uses direct authenticated extension IPC;
- MCP's optional transport authorization and experimental Tasks are insufficient for this corporate human-plus-harness identity, room, federation, consent, and durable peer-mailbox model; MCP remains a local tool/resource/context binding;
- caller identity comes from the authenticated local adapter/session and network token context, never model-supplied tool arguments;
- corporate or A2A bearer tokens never pass through an MCP server;
- vendor extensions such as Claude Channels remain vendor-specific adapters.

### 6.3 Internal protocol

The internal protocol is a versioned, typed corporate API over TLS plus DPoP or workload mTLS. It uses:

- immutable submission and event objects;
- separate domain-owned receipts;
- typed room, task, relationship, grant, presence, artifact, and policy objects;
- cursor-based mailbox reconciliation;
- explicit schema negotiation and declared capability downgrades;
- opaque internal IDs scoped to a trust domain;
- authenticated local IPC between harness shim and supervisor.

### 6.4 Authenticated internal discovery

The core owns an authorization-filtered directory for internal agents/harnesses, hubs/services, rooms, trust domains, approved endpoints, adapter/profile versions, and capabilities.

Every record has a stable opaque ID, actor/resource type, owner domain, profile and schema version, status/key/endpoint epoch, validity/freshness, accountable owner, current key and endpoint references, capability authority, and review date. List/get is non-enumerating: unauthorized callers receive a non-disclosing result rather than a global employee, agent, room, or service list.

Enrollment and trust status, crypto/profile support, endpoint origin, scanner/effect-worker class, and other security facts come only from the authoritative registry plus active conformance probes. Self-advertised capabilities are bounded-freshness routing hints that may affect batching or presentation but never grant identity, permission, assurance, file release, or tool scope. Current revocation/key/endpoint epochs override all caches.

### 6.5 Version negotiation, rolling upgrades, and configuration portability

Every connection performs an authenticated profile handshake carrying protocol ranges, schema/profile IDs and hashes, critical extensions, feature set, adapter version, and status epoch. Select the highest mutually allowed profile. Reject unknown major versions, unknown critical fields, and any downgrade of identity, authorization, durability, audit, isolation, or crypto floor. Unsupported event types remain queued or receive an explicit typed rejection; intermediaries never strip unknown signed fields.

The core supports a declared N/N-1 read/write window and an expand → migrate/backfill → verify → contract database sequence. Rollback is allowed only within the declared compatibility window and never rolls back revocation/security state. Deprecation, cache invalidation, mixed-version receipt behavior, and partially capable adapters are versioned policy.

AgentNet's immutable first release starts at storage schema version 1 with one
complete checksum-bound authority migration. Current unreleased source adds
contiguous migration 2 only for the protected task-payload disclosure receipt.
Fresh SQLite initializes v2 atomically; an existing SQLite store upgrades only
from an exact metadata/catalog/checksum/object-verified v1 state and commits the
v1→v2 migration atomically or not at all. PostgreSQL applies the same contiguous
migration catalog. Altered, prototype, noncontiguous, future, or unsupported
older state fails closed.

No pre-release or differently named database is an authority source, and no
conversion infers bilateral consent from a unilateral edge. Operator moving
exploratory data exports only reviewed non-authority content, initializes a
fresh current store, and obtains fresh exact consent. Rollback may restore only
an exact signed, verified compatible backup and cannot downgrade metadata,
synthesize, or reactivate relationship authority.

Configuration uses a portable versioned schema split into:

- redacted non-secret domain, endpoint, policy-profile, quota, and UI configuration that may be exported/imported;
- device/domain secrets and private keys that are never exported and must be rebound through enrollment;
- migration validation that fails on unknown security settings or unsupported profiles.

Pin exact A2A/MCP specifications, SDK releases, TCK commit/report, internal profile hashes, and adapter versions in the release manifest. Mixed-version, downgrade, partial-adapter, config-migration, and rollback tests are required.

### 6.6 Reuse-first component strategy

The corporate supervisor and governance integration remain the novel product boundary. Established components are candidates behind narrow, replaceable interfaces:

| Need | Reuse candidate | Mandatory corporate boundary |
|---|---|---|
| External agent interoperability | A2A SDKs and TCK | Gateway mapping cannot promote public identity or equate A2A task state with internal effect evidence. |
| Capability descriptions | AGNTCY OASF | Imported descriptions are untrusted discovery metadata, never authority. |
| Agent discovery | AGNTCY Directory | Results pass authorization-filtered, non-enumerating corporate discovery and current key/revocation checks. |
| Secure agent transport | AGNTCY SLIM | Evaluate as a transport only; corporate actor binding, mailbox custody, receipt ownership, offline semantics, and authorization remain ours. |
| Reference integration | CoffeeAGNTCY and official component SDKs | Use for laboratory evaluation and interoperability evidence, not as production trust proof. |
| Human rooms, device sync, and UI | Matrix clients/bridges or isolated room components | Matrix power levels, federation, event DAG, and delivery state do not become corporate authority or business-effect truth. |
| Sealed-room cryptography | Maintained audited MLS implementation, including one already used by a selected transport | Corporate membership, provider disclosure, recovery, retention, and metadata policy remain explicit. Never implement cryptographic primitives locally. |
| Server workload identity | SPIFFE/SPIRE where a real managed workload fleet justifies it | Human identity and harness ownership remain separate; laptops are not forced into SPIRE. |
| Human authentication | OIDC and WebAuthn implementations | Corporate principal mapping, independent approval ceremony, recovery, and transaction binding remain ours. |
| Authorization evaluation | Cedar initially; OpenFGA/SpiceDB only as a measured replacement | Exactly one positive authority and one coherent revision model. |
| Durable transactional state | PostgreSQL | Corporate state names and durability claims follow tested commit boundaries. |
| Artifact bytes | Existing self-hosted filesystem/object-store implementations behind `ArtifactStore` | PostgreSQL manifest, quarantine, provenance, scan, authorization, retention, and release state remain authoritative. |
| Local tools | MCP where the harness supports it | MCP arguments or tokens never establish corporate caller identity. |
| Long-running effect recovery | Temporal-style engine if complexity/benchmarks justify it | Workflow success cannot fabricate delivery, authorization, or external effect evidence. |

Every candidate must be pinned and evaluated for maintenance, license, supply chain, self-hosting, data egress, API stability, resource cost, upgrade/rollback, failure behavior, identity model, tenant isolation, enumeration, offline recovery, duplicate delivery, revocation timing, and operational ownership. A component is accepted only when it maps cleanly into the canonical schemas and must-not-ship gates. Adapters preserve the ability to replace it without migrating corporate identity or rewriting signed history.

## 7. Identity, onboarding, and credentials

### 7.1 Identity model

- **principal_id:** immutable opaque ID local to a corporate trust domain, mapped to OIDC issuer and subject.
- **verified email:** required visible identity/contact alias with history; not globally sufficient authorization.
- **harness_id:** exact enrolled installation/profile in one domain; independently revocable.
- **credential_id and key epoch:** rotatable proof material for that harness/domain.
- **session/subagent_id:** attributable child identity that can only attenuate its parent ceiling.
- **workload_id:** server service or active-agent identity, separate from a human.
- **guest_id:** host-local contractor identity, separate for every host network.

This refines the initial “permissions by user email” requirement without abandoning it: the human remains visibly and durably tied to the verified corporate email, but authority uses a non-reassignable internal principal keyed by domain and IdP subject. Raw email reuse, rename, merger, or reassignment cannot silently transfer authority.

Actor types are explicit:

| Actor | May originate | Required authority chain | Participant display |
|---|---|---|---|
| Verified human + harness | Business messages, tasks, room/file actions, task grants | Current human entitlement ∩ harness eligibility ∩ task/resource policy | Host/room-scoped verified human label, domain, harness; email only to authorized directory/audit viewers |
| Host-local guest + harness | Host-scoped business actions | Current host guest grant ∩ harness eligibility ∩ host policy | Pairwise host label, home domain where policy permits, harness |
| Workload/service | Receipts and deterministic control facts it owns | Workload credential and exact role; data/effect work also references parent human event or task grant | Service role and domain; never fabricated human |
| External A2A peer | Low-trust protocol submissions | Verified network/provider facts only until admission | external-human-unverified; not a corporate participant |

### 7.2 Enrollment ceremony

1. The signed supervisor connects to the exact TLS enrollment origin and receives a short-lived nonce plus exact transaction transcript.
2. It generates a distinct per-domain, per-harness P-256 candidate key in the strongest available platform store and proves possession over the nonce and transcript.
3. The human authenticates in the system browser using OIDC Authorization Code + PKCE, state, and nonce. Embedded webviews are forbidden.
4. The server validates issuer, audience, subject, workforce/contractor status, assurance, nonce, expiry, and proof of possession.
5. A pre-bound approval service shows the exact typed transaction: domain, human, beneficiary harness/device, key thumbprint, requested class, nonce, and expiry. The confirmation uses an owner-controlled WebAuthn authenticator that the enrolling harness cannot read or automate. The default self-hosted profile may colocate the approval service with Core under a distinct OS identity, credential, storage root, and loopback service; it reports `independent_boundary_proven=false`. A separately administered host/device/security boundary is the optional high-assurance profile. Slack, Telegram, email, or another authenticated channel may carry a human prompt only when the enrolling harness cannot read, approve, or automate that channel; no such channel is required when the same owner directly uses the approval UI and fresh laptop.
6. The human actively transfers a 128-bit one-time code that is valid for at most five minutes and approves that exact transaction after fresh phishing-resistant authentication. At most five failed code attempts are accepted. The approval service binds random challenge, canonical transaction hash, beneficiary, approver, domain, and expiry; verifies RP ID, origin, user verification, and credential status; consumes the challenge once; and signs an immutable approval receipt. This proves ceremony and transaction binding, not human comprehension or a trusted display on a compromised enrolling host. A notification click, agent message, or auto-readable bot response alone is insufficient.
7. The enrollment service atomically binds principal, harness, key, assurance tier, domain, validity, approval evidence, and audit intent.
8. It issues opaque, stateful, short-lived DPoP-bound access credentials plus a rotating refresh family. Bearer downgrade is rejected.

The agent connection, a submitted email, copied public ID, chat text, or possession of another harness’s token cannot complete enrollment.

The default colocated profile satisfies ID-002 through human authentication independent of the enrolling harness, but does not claim independent administration and must retain `independent_boundary_proven=false`. It may run bounded ordinary onboarding and C0 tests under the recorded owner policy. Production-independent, high-impact, or stronger assurance claims require the optional separately administered profile and its external evidence. Synthetic flows without real OIDC, candidate-key proof, WebAuthn UV, or exact claim-code binding remain noncompliant and cannot be promoted.

### 7.3 Credential lifecycle

- Generate distinct keys per domain and harness; never clone sibling harness identity.
- Prefer hardware-backed non-exportable custody where proven; label software/OS-user-protected custody honestly.
- DPoP sender-constrains access tokens; it does not itself sign message bodies or authorize effects.
- Rotate refresh families, access tokens, and key epochs; retain alias/key history for audit.
- Device loss or key loss creates a new binding after reauthentication and recovery; it does not restore the old private key.
- Harness revocation invalidates that harness and key epoch while siblings remain enrolled.
- Human offboarding revokes positive authority and all harnesses, guests, grants, memberships, and pending protected effects according to the event-specific matrix.
- Compromise creates a quarantine window, rotates credentials, blocks protected work, and triggers incident review.
- Offline submissions remain encrypted local drafts until accepted after current authentication and authorization.

Same-domain ordinary managed messages need DPoP authentication plus a server-signed durable acceptance receipt. A separate durable origin signature is required for federation, sealed E2EE, protected offline commands, untrusted relays, approval/control evidence not already signed by the approval service, or explicit legal/evidence classes.

When compromise time is uncertain, compromised_from is the earliest credible compromise or last-confirmed-good boundary selected by incident response. Uncertainty widens rather than narrows the quarantine window. Record source, confidence, adjudicator, affected credential/key epochs, and signed release/reject decisions.

### 7.4 Freshness and cryptographic key profile

AgentNet publishes a versioned profile for enrollment, access-token DPoP, origin objects, callbacks, federation, approvals, receipts, and local IPC. Numeric values remain accountable-owner inputs under PD-009/010, but boundedness is normative.

- **Algorithms and canonical input:** explicit allowed algorithms/key sizes and exact typed preimages; unknown critical fields or algorithms fail closed.
- **Proof age and clock:** maximum past age, bounded future skew, monotonic/server-corrected clock behavior, clock-health alarm, and no indefinite skew widening.
- **Nonce/jti:** high-entropy server nonce where required, one-time use, minimum jti entropy, deterministic duplicate denial, replicated/persistent cache for the entire acceptance/retry window.
- **Sequences and idempotency:** actor/operation/recipient scope, accepted windows, gap behavior, and dedup retention longer than every retry/replay window.
- **Key epochs:** activation, overlap, rotation, compromise, offline use, historical verification, and deterministic unknown/stale-key behavior.
- **Failure:** clock rollback, cache loss/partition, nonce-service outage, or state uncertainty denies protected operations while safe drafting and deterministic diagnostics remain.

Key hierarchy:

- TLS 1.3 preferred and TLS 1.2 minimum only with an approved cipher/certificate profile; exact hostname/origin and mTLS identity validation;
- per-device local queue keys protected by the advertised platform custody tier;
- per-object/message DEKs scoped at least to domain, tenant, and confidentiality class, wrapped by separately administered KEKs;
- explicit decrypt ACLs for mailbox, C2 enclave, scanner, effect worker, backup/restore, and legal-hold roles;
- distinct issuer, receipt, approval, federation, audit, scanner, update, and MLS key purposes;
- rotation/overlap/revocation/compromise schedules plus historical public-key retention;
- encrypted backup-key recovery, restore tests, and honest crypto-erasure limits for replicas, backups, downloaded, and federated copies.

KMS outage never falls back to a raw application key. Downgrade, substitution, stale key, cross-class decrypt, offline rotation, backup restore, and compromise rebuild are release tests.

## 8. Authorization, administration, and delegation

### 8.1 One authorization authority

Use one Cedar-based policy decision service over one transactional PostgreSQL entity snapshot, subject to correctness and scale benchmarks.

- Request tuple: verified principal, action, resource, and typed context.
- Positive entitlements attach only to the human principal.
- Harness, device, session, credential, assurance, classification, and capability are deny-only eligibility constraints.
- Missing state, schema failure, policy diagnostics, stale revision, or authority outage denies protected operations.
- Every decision records reason, determining policy, policy/schema/entity revision, principal, harness, session, credential/grant epoch, domain, and resource.
- Do not run Cedar and OpenFGA as two independent positive authorities. If Cedar fails measured graph/list/causal requirements, replace it with one OpenFGA or SpiceDB authority and preserve a single revision model.

### 8.2 Organizational graph

Represent administers, supervises, sponsor_of, assigned_work, moderates, owns, and member_of as explicit typed, time-bounded relations. The graph supports multiple administrators and multiple subordinates without becoming a permission tree. An administration edge is authority-bearing only while its bilateral governance transaction is `active`; a `proposed`, `expired`, `revoked`, `rejected`, or `superseded` record has no relationship authority.

Relations grant only explicit meta-actions such as assign, observe allowed status, cancel under policy, invite, moderate, or approve. They never reveal another human’s protected data automatically.

**Implemented bilateral activation:** the exact owner of the proposed
administrator harness (human principal or host-local guest), acting through
that harness and holding `organization.relationship.propose`, may create a
proposal. The canonical transaction binds both endpoint harnesses, both current
owner kinds and IDs, both credential epochs, exact terms and relationship
revision, proposal/relationship expiries, policy and domain-revocation epochs,
and the predecessor snapshot. Proposal creation is idempotent only for the same
bytes and has zero authority.

Normal activation requires a fresh one-use receipt verified by the configured
`IndependentApprovalVerifier` for the exact purpose
`organization.relationship.accept`. The verifier-derived approver kind and ID
must equal the **current** subordinate harness owner (`human` or `guest`). The
activation caller must also be a current exact participant. Caller JSON cannot
assert an actor, a `verified` result, or policy-decision evidence. Every bound
owner, credential, policy, domain, predecessor, digest, revision, and expiry is
rechecked in the activation transaction.

Either activation basis also requires one exact local activation audit intent.
Before the owner receipt or recorded exception is consumed, the same database
transaction inserts a pending intent whose digest binds the canonical
relationship transaction and digest, relationship and lifecycle revisions,
`proposed` to `active` transition, activation basis and transport-derived
activation actor, exact receipt or exception evidence, activation time, and
custody-only authority effect. The edge compare-and-swap and evidence
consumption occur in that transaction, and the intent is completed at the
exact activation time before commit. A persisted active edge supplies no
assignment authority if this completed intent is missing, pending, malformed,
noncanonical, stale, or inconsistent with the edge or evidence.

This activation intent is durable **local provenance**, not an independent
external witness. A fully coherent compromise of the application database can
forge or erase the edge and local intent together. Production audit claims
therefore still require the separately administered export/checkpoint/witness
and reconciliation evidence described in §4.4 and gates 13, 16, and 19.

The authenticated HTTP flow is:

| Route | Strict request | Result |
|---|---|---|
| `POST /v1/relationships` | relationship plus proposal expiry | `201` zero-authority `proposal` |
| `POST /v1/relationships/{relationship_id}/accept` | strict approval receipt plus expected transaction digest, relationship revision, and lifecycle revision | active edge only after exact current subordinate-owner consent |
| `POST /v1/relationships/{relationship_id}/policy-exceptions` | exact exception plus signed authority command | records bounded evidence; does not activate |
| `POST /v1/relationships/{relationship_id}/policy-exceptions/activate` | exception ID plus expected digest/revisions | one-time exact exception consumption and activation |
| `GET /v1/relationships/{relationship_id}` | participant read, or separately entitled administrative read | non-enumerating read |
| `POST /v1/relationships/{relationship_id}/revoke` | exact signed command at current lifecycle revision | endpoint exit/revoke or separately entitled admin-revoke mechanism |

All relationship bodies forbid unknown fields. Actors come only from the
authenticated transport, and successful/handled-error relationship responses
are `no-store`.

**Normative assignment direction:** every active `administers`/`supervises` edge may carry an explicit `may_assign` action and an `assignment_scope` covering permitted task types, resources, data classes, tools, budgets, deadlines, concurrency, and expiry. A task from the authenticated administrator endpoint to the subordinate endpoint is automatically accepted into the subordinate's durable task mailbox only when it matches that current edge and scope. The acceptance fact is `accepted_queued`: it proves custody of the task, not permission to read protected data or perform an effect.

Automatic custody always has an exact deadline. An explicit request deadline
must be timezone-aware and is preserved exactly. When the request omits it,
the accepting service derives a whole-second deadline from the normalized
immutable event creation time, capped by the exact assignment scope's complete
`max_duration` and strictly before relationship expiry. That server-derived
value is written back into the canonical assignment request and immutable event
as both `effect_deadline` and the no-later-than `delivery_expires_at` before the
custody digest and row commit. A duplicate therefore cannot acquire a later
window from retry time.

Custody is also a nondisclosure boundary. Generic mailbox reads, conversation
thread reads, explicit-open/supervisor reconciliation, and downstream ordinary
background delivery never return the plaintext payload of a task assignment
or task-linked control event. They return only immutable event metadata,
digests, and a custody reference with `payload_access=task_grant_required`.
This denial uses the typed task/control shape as a fail-closed fallback, so a
record with no marker is still withheld and deleting or changing the
marker cannot promote it to ordinary content. Protected release exists only on
the recipient-owned supervisor execution path. Authorization consumes one
exact current `task.process` TaskGrant use bound to the event, action, resource,
mailbox source, receipt sink, classification, principal, and harness. The
recipient then durably records exact local queue custody before the release
transaction rechecks the immutable cursor/envelope/payload, current policy,
credential and domain epochs, grant dimensions/revocation/expiry, delivery,
effect and retention boundaries, active execution intent, and unresolved
conflicts. It validates plaintext and provenance inside the transaction,
commits one disclosure receipt and audit record, and returns bytes only after
commit. Exact retries repeat current-state checks and reuse the receipt without
another grant use. Generic reads remain permanently redacted, and release
explicitly grants neither tool nor effect authority. Ordinary non-task messages
retain their normal authorized mailbox and conversation payload behavior.

The reverse and lateral directions are fail-closed. A subordinate/manager agent assigning a task to one of its administrators, or one manager agent assigning a task to a peer manager, creates a non-executable proposal in `pending_human` and requires explicit confirmation by the recipient's human owner. It may bypass that confirmation only when a separate current relationship independently grants the sender `may_assign` over that recipient for the exact task scope. An agent can therefore be an administrator relative to some agents and a subordinate relative to others; authorization is evaluated for the exact directed edge, never from a global title or payload claim.

Conflict defaults:

- explicit deny, revocation, quarantine, and resource/security policy win;
- task ownership uses leases and fencing generations;
- duplicate or concurrent non-idempotent commands require idempotency/effect reservation;
- incompatible non-commutative instructions enter conflict_pending for an authorized human or governance threshold;
- arrival order, prose seniority, model confidence, or manager title never resolves authority;
- relationship creation, modification, expiry, and deletion require explicit meta-permission and immutable audit.

The code supplies exact fail-closed lifecycle mechanics, but the accountable
owner has **not** recorded the ORG-006 policy. In particular, implementing the
policy-exception and `organization.relationship.admin_revoke` mechanisms does
not approve who may hold those entitlements, when they may be used, what
threshold applies, or any mandatory-relationship, notice, review, or appeal
rule. Job title or prose never supplies that authority.

| Relationship transition | Implemented fail-closed mechanism | Policy status |
|---|---|---|
| Propose | Exact current administrator-endpoint owner plus exact propose entitlement; record is expiring and has zero authority | Implemented |
| Normal activate | Independent verifier derives the exact current subordinate human/guest owner from a fresh purpose/transaction-bound receipt; exact revisions and epochs are rechecked; an exact local activation intent is inserted pending, the receipt and edge are consumed/activated, and that intent is completed in one transaction | Implemented local mechanism; independent witness evidence remains external |
| Policy-exception activate | A separately signed exception is first recorded against the exact digest/revisions/epochs and bounded by proposal expiry, then atomically consumed once by a current participant or its exact recorded signer harness; the same exact local activation-intent rule applies | Mechanism implemented; eligible authorities/circumstances/threshold unapproved under ORG-006 and independent witness evidence remains external |
| Modify/renew | New relationship ID, next coherent pair revision, predecessor snapshot, and fresh activation evidence; no in-place authority edit | Implemented |
| Subject exit / endpoint revoke | Exact signed `organization.relationship.revoke` command at the current lifecycle revision; on commit it advances the pair lineage epoch and revokes every then-open row; either endpoint is bound by transport and command proof | Mechanism implemented; mandatory-relation/notice/appeal policy unapproved |
| Administrative revoke | Nonparticipant requires separately entitled and signed `organization.relationship.admin_revoke` | Mechanism implemented; override policy unapproved |
| Expire | Proposal and active-edge expiry are persisted/audited; renewal never backdates authority | Implemented |
| Race | Exact lifecycle compare-and-swap permits one committed activation, revoke, expiry, or supersession. If revoke commits first, its lineage epoch fences every bound proposal; if activation/renewal commits first, the older revoke command is stale and conflicts. | Implemented |

Renewal activation atomically supersedes a still-current predecessor. If a
revocation commits first, the renewal's lineage epoch and predecessor snapshot
are stale and activation fails. If renewal commits first, a command prepared
against the predecessor's prior lifecycle revision is stale and fails; a fresh
exact command is required to revoke the new active edge. At most one exact
directed pair is active.

Immutable storage schema v1 contains these governance transactions and
exception receipts as first-release authority objects. Current unreleased
source adds contiguous schema migration 2 for the protected payload-release
receipt only. Exact catalog/checksum verification precedes the one-transaction
SQLite v1→v2 upgrade and PostgreSQL migration application. No unsupported,
tampered, prototype, or unilateral record is converted into consent.

The implemented conflict lifecycle binds every automatically accepted task to
a strict typed resource/operation/access/exclusivity intent. Incompatible live
intents for one subordinate enter deterministic per-resource conflicts, and
all affected events move to `conflict_pending` atomically. The exact current
human/guest positive-authority owner of the subordinate endpoint may partition
every current member into pairwise-compatible release and reject sets at the
exact conflict, policy, domain, and credential revisions. Arrival order, prose,
and manager title do not choose a winner. Compare-and-swap fences competing
decisions; rejection propagates across overlapping conflicts; and a released
event returns to the queue only when no pending conflict membership remains.
No release grants data, semantic-processing, tool, or effect authority.

### 8.3 Task delegation

A delegated task carries the delegator’s verified identity, requested outcome, data references, allowed actions, budget, deadline, cancellation semantics, and reporting path. The recipient executes only with its own human principal’s authority or a separately approved host grant. The delegator’s data permissions do not travel with the task.

Assignment processing has two distinct security decisions. First, the relationship policy decides whether the recipient mailbox may automatically record `accepted_queued` or must record a non-executable `pending_human` proposal. Second, before semantic processing, any protected read, or any effect, the PEP performs the full recipient-side authorization and intent checks below. Automatic downward acceptance never means automatic privilege inheritance or unrestricted execution.

The second decision is implemented as a separate recipient-owned supervisor
flow, never as a generic read flag. `authorize` consumes one exact current
`task.process` TaskGrant use; durable local custody binds the queue; protected
payload release then rechecks current actor, grant, policy, credential, domain,
intent, conflict, digest, provenance, and lifetime state and commits its audit
and disclosure receipt before returning plaintext. A result upload requires
that exact committed release receipt, so local custody alone cannot fabricate
semantic processing and an old `result_uploaded` row cannot retroactively
obtain payload bytes. Response-loss retries reuse the release receipt without
another grant use. Current TaskGrant dimensions establish payload access and
semantic-processing authority only; the release response keeps tool and effect
authority false. Separate grants/reservations remain mandatory for any tool,
network, budget, protected sink, or business effect.

Autonomous semantic processing also requires an **exact task grant** issued through a locally authenticated human action or preapproved deterministic workflow. It binds allowed input sources, output sinks and recipients, actions, resources, data classes, tools, network origins, budget, expiry, and whether further confirmation is required. The PEP intersects human entitlement ∩ harness eligibility ∩ exact task grant ∩ resource/data-class policy ∩ current revocation state.

Semantic-derived values remain durably tainted. Model output cannot directly create an executable job. Every external transmission, sensitive read/export, destructive mutation, credential use, or owner-defined protected sink requires independent confirmation unless that exact sink and source class were preauthorized by the task grant. This closes the gap between “the human may do X” and “the human intended this invocation to do X.”

### 8.4 Temporary elevation

An elevation request stores requester, beneficiary human, eligible harness, domain, exact actions/resources, reason, expiry, use budget, risk class, and required approvers.

- beneficiary cannot approve;
- manager relation alone is insufficient;
- approval uses fresh authentication and exact transaction hash;
- grant creation, approval threshold, nonce consumption, and use counters are transactional;
- every protected effect reauthorizes;
- ordinary scoped elevation defaults to one independent approver;
- high-impact and break-glass defaults to two approvers;
- break-glass is short, nonrenewable, immediately notified, and retrospectively reviewed.

## 9. Communication and collaboration model

### 9.1 Supported topologies

The same canonical objects support:

- agent-to-agent;
- agent-to-always-on server agent (the baseline's agent-to-hub topology);
- always-on server agent to agent (the baseline's hub-to-agent topology);
- server-agent-to-server-agent delivery (the baseline's hub-to-hub topology);
- one-to-many, many-to-one, and many-to-many;
- direct conversations;
- manager/subordinate communication;
- persistent and temporary rooms;
- brainstorming and meeting rooms;
- threads, replies, mentions, tasks, handoffs, cancellation, and completion;
- file and structured-artifact sharing in every topology.

Every corporate contribution resolves and displays a host/room-scoped verified human label or pairwise guest pseudonym, trust domain, harness, and—where useful—session identity. The verified email mapping is visible only to authorized directory/audit viewers or when an explicit policy/legal purpose requires participant disclosure. Workload facts display the service role/domain; external A2A facts display external-human-unverified rather than fabricating a corporate human.

### 9.2 Event object

An immutable event includes:

- schema and protocol version;
- trust domain and stable origin actor;
- explicit actor union: verified human+exact harness, host guest+exact harness, scoped workload with parent event/task grant, or external-human-unverified A2A actor;
- event type and classification;
- conversation, room, thread, task, recipient, fanout, and causal-parent references;
- idempotency key and exact payload/artifact digests;
- creation plus separate delivery_expires_at, effect_deadline, retention_delete_at, legal-hold state, and policy/room/credential revision references;
- an optional extension-owned `payload_access=task_grant_required` marker for
  task custody; readers also classify typed task assignments and task-linked
  controls so a missing marker never permits generic disclosure;
- optional purpose-specific origin evidence;
- no mutable server, relay, or recipient claim inside the origin signature.

A workload-origin local event must bind exactly the actor's declared local
parent event and no other causal parent. The mailbox resolves the immutable
parent ledger digest in the same transaction, records derived provenance, and
preserves source classification, sink ceilings, taint, and current policy.
Missing/wrong parents, self-reference, replay-byte drift, classification
reduction, sink widening, or policy drift abort the submission without an event
or provenance row. Cross-domain relay packets retain their signed remote-event
and audit binding; the receiving domain does not mislabel a remote event as a
local ledger parent.

### 9.3 Separate authority objects

Keep these separate and digest-linked:

1. optional harness-origin submission;
2. authenticated transport context retained locally, never forwarded as bearer authority;
3. durable acceptance and authorization receipt;
4. relay or cross-domain handoff receipt;
5. remote-domain acceptance, rejection, or delay receipt;
6. recipient durable-commit receipt;
7. presentation or model-processing observation;
8. effect reservation and terminal or unknown receipt;
9. revocation, quarantine, compensation, and adjudication receipt.

Each signer asserts only facts it owns. A missing downstream receipt means unknown, not failed or complete.

### 9.4 Delivery states

There is no single mutable global truth. Canonical projection is derived from actor-owned facts:

| State/fact | Owning actor and durable evidence | Meaning and allowed transition |
|---|---|---|
| created_local | Origin supervisor; encrypted local record | Draft only; may submit, edit into a new digest, expire, or delete. |
| submitted | Origin supervisor plus request/idempotency proof | Sent but no corporate acceptance known; retry exact bytes only. |
| rejected_before_accept | Accepting domain; signed rejection | Terminal for this submission digest; it cannot proceed to queue. A changed request needs a new idempotency key. |
| authorization_hold | Accepting domain; typed hold reason/revision | No acceptance/effect; may resume only after current reauthorization or expire/reject. |
| accepted_durable | Accepting domain; committed event/recipient/audit evidence at declared RPO boundary | Source domain owns exact accepted bytes/state; may queue, dispatch, cancel before effect, expire under policy. |
| queued / retry_scheduled | Current mailbox/dispatcher; attempt/backoff record | Waiting for eligible recipient/relay; no delivery claim. |
| dispatch_attempted | Dispatcher/relay; attempt and exact digest | Attempt occurred; may yield remote accepted/rejected/delayed or retry. |
| remote_accepted / rejected / delayed | Remote domain; separately signed receipt | Remote domain’s claim only; never implies recipient commit or effect. |
| recipient_committed | Recipient supervisor/domain; durable inbox bytes+dedup evidence | Baseline delivery acknowledgment: recipient custody exists; no presentation, model, or effect claim. |
| presented | Recipient presentation layer; observation receipt | Content was made available on an explicit view surface; not proof a human read it. |
| processing | Recipient worker; renewable fenced processing lease | Worker is active; lease expiry returns to eligible retry without claiming effect. |
| effect_prepared | Effect authority; transactional reservation and current grant/policy revision | Exact effect reserved but not proven committed; execute once or reconcile. |
| completed | Effect owner; terminal effect/output evidence | Required effect/output committed. Only this owner may assert it. |
| failed_retryable / failed_terminal | Failing owner; typed error and attempt evidence | Retry only when policy/idempotency allows; terminal failure ends this operation. |
| expired | Accepting authority/mailbox per recipient; committed delivery_expires_at/effect_deadline, policy/grant revision, and authoritative-clock evidence | Terminal no-future-delivery/effect for that branch. Exact retry returns the expired fact. A late receipt cannot reopen it. Expiry cannot overwrite a committed effect; possible commit becomes effect_unknown/too_late until reconciled. Retention is independent. |
| cancel_requested | Authorized requester/control authority; cancel receipt | Request only; races with effect reservation/commit. |
| canceled | Effect/control owner; proof no irreversible effect committed | Terminal for requested effect. |
| too_late | Effect owner; proof effect committed before cancellation | Cancellation failed; reconciliation/compensation may follow. |
| effect_unknown | Effect owner/dispatcher; timeout after possible commit | Nonterminal uncertainty; query/reconcile, then adjudicate or authorize compensation. Never blind retry. |
| reconciling / compensated / adjudication_required | Reconciler, compensating effect owner, or authorized human | Separately evidenced follow-up; original history is never rewritten. |
| quarantined / dead_lettered / conflict_pending | Security, queue, or governance authority | Held from normal processing with reason, review owner, expiry/retention, and release/reject path. |

Each recipient/fanout branch has its own projection. partially_delivered is computed from those facts and is never an actor-written terminal state. Cross-domain/fanout projection preserves independent expiry. Contradictory or late signed receipts trigger quarantine/reconciliation and incident review. Dedup/replay evidence is retained longer than every retry, receipt, and replay window.

### 9.5 Delivery guarantees

- At-least-once transport; no global ordering.
- Stable idempotency scope binds origin actor, operation, recipient/fanout, and payload digest.
- Same idempotency key with a different digest is a security conflict.
- Conversation or room authority allocates local sequence; causal parents expose gaps.
- Emit accepted_durable only after the declared PostgreSQL durability boundary.
- Recipient persists exact bytes and dedup state before recipient_committed.
- Recipient commit is not model processing or effect completion.
- A timed-out non-idempotent effect becomes effect_unknown. Query and reconcile before human adjudication or a separately authorized compensating action. Never blind retry.
- During its authorized retention and custody lifetime, the core retains every
  accepted event's exact bytes and exposes the authorized projection even if it
  is informational, no-reply, errored, progress-only, canceled, or
  non-actionable. Generic mailbox/conversation/supervisor projections never
  release task-assignment or task-linked-control payload bytes: they expose
  only immutable metadata/digests and the custody reference, including for
  records without the marker. Protected bytes are available only through the
  separately signed, recipient-owned supervisor authorization → local-custody →
  payload-release flow described in §8.3; generic projections remain
  nondisclosing. Ordinary non-task content remains fetchable under its normal authorization and
  retention policy. Classification suppresses model wake or notification,
  never durable visibility. After lawful expiry/deletion, preserve only the
  minimized access-controlled tombstone, receipt chain, deletion authority,
  and deletion evidence required by policy; never retain recoverable plaintext
  merely to satisfy a delivery gate. Downloaded or federated copies cannot be
  guaranteed erased.

### 9.6 Offline and presence

The local supervisor stores encrypted drafts and committed inbox content. On reconnect it:

1. refreshes credential and current revocation/room/policy epochs;
2. uploads drafts idempotently;
3. reconciles by durable cursor;
4. persists recipient bytes before acknowledgment;
5. wakes a model only for authorized semantic work.

Requests that require an answer carry an opt-in durable `ResponseObligation`
bound to the exact accepted request and one responsible recipient harness. Only
a typed response repeating the request event ID and digest can close it; prose
cannot. If the request supplies a response schema, its identifier and bounded
self-contained JSON Schema are both part of the signed payload and the stored
schema digest is validated before terminal closure. The authenticated common
supervisor reconciles obligations at startup, reconnect, live wake, and the
bounded cursor fallback and stores only encrypted content-free attention
counters locally. Active sibling harnesses share principal-level visibility,
but cannot claim the specifically responsible harness's progress or response;
revoked harnesses fail closed.

WSS, SSE, push, and broker notifications are wake hints. Cursor reconciliation is authoritative. Offline is a normal availability state, not identity failure.

Presence is a signed/authorized expiring lease with live, recent, stale, and unknown states. Presence never proves readiness, authorization, or receipt.

### 9.7 Always-on and future peer-assisted profiles

The same ordinary AgentNet extension may run continuously on one or more server
agents to compose domain authority, durable recipient mailboxes, offline
store-and-forward, reconciliation, and audit roles. This is not a separate Hub
product or identity. A local deployment may run one explicitly weaker server
agent, while a production durability claim requires redundant stateless role
instances, authoritative PostgreSQL replication, replicated artifact storage,
fencing, backups, and restore drills on company-controlled infrastructure.

The architecture must preserve two later profiles without changing corporate event formats:

1. **Opportunistic mesh:** online enrolled devices prefer direct delivery and explicitly eligible devices may temporarily store and forward recipient-encrypted envelopes or encrypted artifact chunks. Relays gain no content or data authority; eligibility, quotas, retention, custody receipts, and revocation are policy-controlled. The sender retains its durable outbox until the required custody evidence exists. If all eligible nodes are offline, delivery safely pauses.
2. **Fully distributed domain:** approved company nodes replicate authority, revocation, mailbox, room-sequencing, and audit state under an owner-approved quorum and partition model. This profile is disabled until policy freshness, revocation under partition, split-brain fencing, membership changes, recovery, equivocation, and durable-replica thresholds pass dedicated gates.

The first production profile therefore uses globally unique actor/event/message/artifact IDs, independently verifiable signatures where required, transport-neutral envelopes, versioned policy and revocation epochs, immutable content digests, per-recipient state, and an abstract mailbox-custodian interface. No identifier, signature, receipt, or authorization rule may require a particular AgentNet server-agent instance. These seams permit a future mesh without weakening the initial consistent security model.

## 10. Rooms and meetings

Every room records:

- immutable room ID;
- owner domain and owner epoch;
- independent governance policy and recovery threshold;
- control sequence and membership epochs;
- creator, moderators, members, guests, invitations, and sponsorship;
- persistent/temporary mode and expiry;
- classification, managed-service-visibility/E2EE mode, history mode, retention, legal hold, archive/delete policy;
- artifact policy and storage domain;
- fanout, tool, token, spend, and causal-loop budgets.

Phase 1 uses one authoritative owner domain/control sequencer. Infrastructure failover within that domain does not change owner. Membership and cryptographic-control changes fail closed during a control partition.

Transfer protocol:

ACTIVE(A,n) → TRANSFER_PROPOSED(B, cutoff, snapshot_digest) → FROZEN → TARGET_ACCEPTED → TRANSFER_COMMITTED(B,n+1).

Transfer requires the predeclared governance threshold plus destination acceptance over immutable state and history digests. Before commit, A remains authoritative and frozen; after commit, B wins and A only forwards a signed redirect. Competing transfers use compare-and-swap. If permanent owner loss lacks a precommitted recovery threshold, tombstone the room and create an explicitly linked successor rather than inventing authority.

The transfer manifest binds cutoff control/event sequences, state/history roots, per-recipient rows, pending effects and cancellations, quarantined/released artifact manifests and object versions, retention/legal-hold declarations, guest roster, audit custody, application/MLS/file-key epochs, destination keys, and destination acceptance. Before cutoff, A completes or explicitly hands off each owned row; after cutoff, B owns new control and delivery. No pre-cutoff effect is silently replayed. B verifies copied objects and evidence before commit; old download capabilities are revoked; guests are re-admitted under B’s host policy; operational, MLS, and file keys rotate; A’s post-cutoff retention/deletion duties remain in the signed manifest. Reconciliation must close every row before TRANSFER_COMMITTED.

Default history is from_join for internal managed rooms and no prior history for guests or sealed rooms. Removed members receive no future content, undelivered history, file keys, or downloads. Already delivered plaintext cannot be recalled.

Single-owner sequencing leaves a real censorship risk. Signed acceptance/relay gaps expose it; participants can leave or fork. Matrix-style multi-writer state resolution remains an alternative only if partition writes become a HARD requirement and the owner accepts the complexity and replicated-history/privacy cost.

Placing, modifying, or lifting legal hold requires a dedicated legal/governance meta-permission, exact scope, authority basis, independent audit, and conflict rule. Room owners and administrators do not receive that power implicitly.

## 11. Files, artifacts, provenance, and content safety

### 11.1 Storage and access

- Immutable content-addressed versions in a versioned object store.
- Separate encrypted quarantine and released namespaces.
- Deduplication, refcounts, encryption, retention, and quota effects are scoped at least to (trust domain, tenant, confidentiality class, retention/legal-hold class), or disabled for sealed/high-risk data. Cross-domain, cross-tenant, or cross-class bytes are never coalesced merely because they match.
- Object storage addresses ciphertext. Plaintext digests stay encrypted inside authorized manifests or use a domain/class-keyed equality identifier. First and duplicate uploads return indistinguishable non-disclosing responses and reveal no prior owner, path, class, artifact ID, quota difference, or existence through timing.
- Current authorization on upload, release, relay, transform, preview, download, and key release.
- Short-lived, single-use, audience-bound download capabilities.
- Offline files survive with the message/room event and exact manifest.
- Retention, deletion, archive, WORM/legal hold, quotas, size, and residency are explicit policy.

### 11.2 Evidence objects

Separate:

1. upload/acceptance statement;
2. immutable manifest containing digest, size, media type, classification, and origin reference;
3. derivation statement linking exact input and output digests plus transformer identity/version;
4. scan attestation linking digest, scanner/version/rules/time/result;
5. release-policy decision;
6. access/download receipt;
7. audit checkpoint.

Artifact promotion accepts an optional strict derivation with complete parent
provenance references and one or more canonical transformation steps. The
service resolves each parent version and digest from the authoritative ledger
inside the promotion transaction, binds the output digest and current policy,
requires every executor to equal the authenticated harness, and persists the
derived artifact as tainted, unreviewed, and scan-pending. Exact replay must
match the server-resolved parents and transformations. Human-origin HTTP
registration is restricted to the exact authenticated human harness; composed
services, not callers, create server origins.

DSSE/in-toto-style statements remain the initial interoperability profile. A
smaller exact-byte signed manifest/attestation profile remains viable if
interoperability or operations fail. Provenance proves lineage, not safety.

### 11.3 Safety pipeline

- quarantine before parsing or model exposure;
- validate extension, declared type, detected type, size, count, nesting, compression ratio, and parser limits;
- malware, secret, DLP, polyglot, archive, decompression-bomb, link, macro, and executable-content controls;
- isolated preview/transform with no ambient credentials and restricted egress;
- stale or missing scanner evidence denies release for protected classes;
- public A2A files are untrusted regardless of Agent Card or sender signature;
- sealed E2EE rooms cannot claim server scanning unless the scanner is a visible key-holding member.

### 11.4 Cross-store artifact acceptance

PostgreSQL and object storage do not share an atomic transaction. Required artifacts therefore use this staged state machine:

0. **upload_reserved:** one PostgreSQL transaction insert-or-returns a private reservation keyed by trust domain, verified actor, and idempotency key. It binds exact request/payload digest, tenant/class/retention/legal-hold scope, expected size/type, required-vs-optional role, expiry, unpredictable private quarantine object key/version, encryption context, task grant, and quota owner. Same key/different digest conflicts before upload.
1. **uploading:** conditionally stream bytes to the reservation’s immutable private quarantine object/version; no event acceptance and no reusable public identifier;
2. **quarantine_object_committed:** object store durably acknowledges the exact reserved version under the declared storage RPO;
3. **object_verified:** re-read/verify ciphertext/plaintext digest as policy permits, size, media constraints, reserved object version, encryption scope, and durability;
4. **manifest_committed/promoted:** one PostgreSQL transaction promotes the reservation and records manifest, artifact state, event/room reference, audit intent, idempotency state, quota charge, and outbox row;
5. **scanning / held / released / rejected:** scanner and release-policy evidence update by separately owned immutable facts; access remains denied until released;
6. **deleted/tombstoned:** lawful policy destroys allowed objects/keys and retains minimized evidence without claiming recall of downloaded/federated copies.

Artifact/event durable acceptance is emitted only after both the immutable object version and authoritative manifest/reference transaction are proven. A required attachment keeps the surrounding message in artifact_hold or rejects it before acceptance; an optional attachment may allow the message only with an explicit attachment_incomplete fact visible to every recipient.

Failure rules:

- reservation committed but object absent → resumable private upload until reservation expiry; no artifact/event acceptance;
- object committed but promotion transaction failed → reservation still authenticates actor, task, scope, object version, and idempotency; retry returns the same hidden reservation, conditionally verifies existing bytes, and promotes once. Inventory reconciliation and policy-safe GC run only after reservation/retry/incident/legal-hold windows;
- metadata references a missing/wrong object or version → artifact_integrity_hold, deny all access/effects, alert, restore/reconcile exact evidence, never substitute bytes;
- object store outage → artifact acceptance incomplete;
- scanner outage/stale attestation → quarantine remains;
- promotion committed but response lost → retry exact idempotency key and converge on the existing manifest/event without disclosing prior ownership.

Fault tests cover reservation-before-object, object-before-promotion, promotion-before-response, reservation expiry, concurrent retry, every object/DB/audit/receipt crash permutation, orphan inventory, missing-version recovery, restore of object+manifest+attestation chain, dedup equality/timing/quota probes, retention, key deletion, and legal hold.

## 12. Federation and contractors

Phase 1 prepares schemas but keeps federation disabled. The first federation profile is bilateral and host-controlled:

1. two domains exchange signed federation metadata out of band;
2. an administrator allowlists exact domain IDs, endpoints, keys, assurance profile, algorithms, data classes, revocation/incident contacts, expiry, and non-transitivity;
3. a host sponsor creates a single-use invitation bound to the intended home domain, principal assurance, pairwise human subject, harness key, resources/actions, expiry, and sponsor;
4. the home domain authenticates its human and harness and returns a per-host pairwise subject/pseudonymous stable identity assertion, not host authority. Cross-host linkability requires a separate approved purpose.
5. the guest proves a fresh pairwise per-host harness key;
6. the host creates a local guest identity, optionally performs host-local WebAuthn/reproof for high-risk access, and issues host-local DPoP credentials plus minimal grants;
7. internal services accept only the host identity, credential, and current policy;
8. a host kill switch applies at the next local decision or stream check. A home revocation signal applies only after verified receipt/reconciliation; exposure is bounded by host token expiry and the PD-009 signal SLO;
9. sponsor loss blocks renewal and new protected work until explicit reassignment;
10. normal exit revokes grants, rooms, keys, downloads, and protected queued effects while retaining minimized audit evidence.

Foreign tokens, groups, roles, manager edges, workload IDs, email strings, and gateway certificates never become positive host permissions. Trust A→B never creates B→C. The same person or harness receives distinct host-local identities and keys in every network.

Guests receive no sponsor_of, invitation, federation-administration, guest-creation, or onward-trust meta-permission by default. Any exception is a separately approved, narrowly scoped, expiring, auditable host grant and never reuses home authority. A compromised sponsor can mint invitations only within its explicit sponsor scope; quotas, independent high-risk approval, anomaly detection, immediate sponsor kill, and invitation audit bound the blast radius.

SCIM and CAEP are lifecycle/revocation signals, not sole authorization or guaranteed-immediate truth. Each domain owns its receipts, audit, and incident response. High-risk guests require fresh host-local proof; low-risk continuity during a partner outage ends at an owner-approved short token expiry.

## 13. Security, privacy, and abuse containment

### 13.1 Content classes

| Class | Managed service visibility and functions | Default |
|---|---|---|
| **C0 public/A2A** | Gateway-readable, no internal-secret assumption | Transport security only |
| **C1 internal managed** | Least-privilege AgentNet service roles may inspect for search, moderation, DLP, scanning, tools, backup | First production-profile default, owner-approved |
| **C2 restricted managed** | Plaintext only inside isolated content/scanner/tool enclave; no broad indexing | Sensitive operational default |
| **C3 sealed E2EE** | MLS members only; relay sees ciphertext/metadata; no server plaintext search, scan, moderation, tool execution, or recovery | Explicit opt-in after policy and required external evidence |
| **H legal-hold overlay** | WORM/eDiscovery over the selected class | C1/C2 by default; C3 only with disclosed inspection/recovery member and legal approval |

For C3, room authorization commits before MLS membership; application room, owner, and MLS epochs remain separate. Any scanner, tool, archive, recovery, or inspection service with keys is a visible room member. No hidden escrow. If C3 is required at launch, the managed Phase-1 profile cannot ship until MLS identity, KeyPackage, multi-device, removal, recovery, history, file-key, compromise, and legal-hold tests pass.

A semantic LLM worker that is a C3 member decrypts plaintext and transmits it to its model provider. The room is therefore sealed from nonmember AgentNet service roles but **not** from that provider. PD-007 must explicitly choose whether each C3 class permits named LLM providers with approved retention/training/residency terms, requires an approved local/self-hosted model, or is human-read-only/deterministic-delivery with no semantic worker.

Metadata minimization is separate from content encryption:

| Class | Mandatory routing metadata | Authorized readers and indexes | Retention/disclosure/federation |
|---|---|---|---|
| C0 | Gateway peer/route, task/message IDs, size/timing, security scheme | Gateway/security/abuse services; public capability index only | Short operational retention plus incident policy; no corporate directory membership |
| C1 | Opaque actors, room/conversation/recipient, state/receipt, classification, size/timing, policy epochs | Authorized mailbox/search/moderation/audit roles; content indexes only by declared purpose | Room/domain retention; participant display uses scoped verified label; federation exposes only required pairwise fields |
| C2 | Minimum opaque route, state, receipt, classification, size/timing | Isolated enclave and narrowly scoped audit/security roles; no broad content or relationship index | Shortest operational/legal period; exports and joins require exact purpose |
| C3 | Ciphertext route, room/MLS epochs, recipient delivery facts, size/timing required for transport | Relay sees only routing/ciphertext metadata; member clients see decrypted content; no managed-service content index | Minimize/pad where feasible; partner disclosure specified by room policy; key/member history follows MLS/legal policy |
| H | Hold ID, authority basis, scope, immutable custody/deletion facts | Dedicated legal/governance and independent audit roles | Overrides deletion only within exact scope and jurisdiction; lifting requires recorded authority |

Presence, relationship graphs, receipt chains, search terms, model/tool cost, and cross-domain correlation are independently authorized resources, not free metadata. Email mappings remain private to authorized directory/audit viewers. Every index names purpose, reader, fields, retention, deletion, and export policy; unknown use is denied.

### 13.2 Four inbound lanes

1. **Protocol-control lane:** receipts, retries, dedup, expiry, presence, cursors, and capability probes are strict deterministic schemas and trigger zero model calls.
2. **Typed-operation lane:** allowlisted action/resource/arguments; current policy enforcement; deterministic or restricted effect worker.
3. **Semantic lane:** peer prose, cards, files, tool output, and web output enter only a clean-profile background worker under an exact task grant. Inputs and derived values remain tainted; the worker has no ambient credentials or unrestricted sinks.
4. **Human-consent lane:** enrollment, elevation, sensitive export, destructive/high-risk effects use an independent exact-transaction approval service.

Raw remote content never becomes a system/developer instruction, tool schema, policy, approval label, signer preimage, or security principal. Model output is a proposal, not authority, human intent, or proof of effect. A semantic proposal becomes a typed job only after source/sink validation and intersection with the exact task grant. Sensitive reads, disclosures, external transmissions, destructive changes, and credential use require a preauthorized exact sink or independent confirmation.

### 13.3 Quotas and loop control

Budgets cover bytes, recipients, fanout, causal depth, turns, model calls, tokens, tools, CPU, memory, network, storage, duration, retries, and spend. Child tasks and subagents receive attenuated sub-budgets. Use fair queues and per-peer/domain/room/recipient circuit breakers. Reserve capacity for cancellation, revocation, quarantine, and security signals. Receipt loops and presence updates never wake a model.

### 13.4 Replay, signing, and local IPC

- Exact audience, resource, domain, nonce, timestamp, sequence, jti, and replay cache checks.
- Purpose-specific, fixed-schema signing only; the broker is never an arbitrary-byte signing oracle.
- Authenticated private local IPC with platform peer credentials, adapter capability, session binding, socket ownership, and replay protection.
- Same-user compromise remains residual risk; server policy and independent human approval protect high-risk effects.
- Signed packages, pinned dependencies, SBOM, provenance, threshold update metadata, canary, rollback, and emergency adapter disable.

### 13.5 Threat coverage

The design defines candidate controls, subject to §19 gates, for impersonation, email reassignment, token/key theft, copied harness IDs, compromised harnesses, malicious peers, prompt injection, malware, compromised hubs, malicious insiders including a compromised sponsor minting rogue invitations, replay, stale authority, confused deputy, privilege escalation, signer-oracle abuse, cross-domain leakage, transitive trust, room-owner loss, split brain, false receipts, unknown effects, denial of service, reconnect storms, cost loops, supply-chain compromise, update rollback, audit omission/fork, and data-retention conflicts.

## 14. Availability and operations

### 14.1 Canonical substrate

Phase 1 uses supported PostgreSQL plus a provider-neutral versioned artifact store. Both are self-hosted by default; the core depends on its own narrow `ArtifactStore` contract rather than AWS credentials or S3-specific identity semantics:

1. one transaction records accepted event, recipient states, idempotency/digest, relevant authority revisions, receipt/audit intent, and outbox row;
2. accepted_durable is emitted only after the declared local WAL and synchronous replica or managed-service durability boundary;
3. unavailable durability quorum means the client retains a submission and receives no false durable acceptance;
4. dispatch failure creates visible lag but cannot lose accepted rows;
5. lost response after commit converges on retry;
6. failover requires fencing; split-brain authority is forbidden;
7. WAL archive, PITR, object backup, full restore drills, and receipt/audit reconstruction are release gates.

The local signed-backup mechanism binds manifest, trust record, seal, and
archive to the exact source fingerprint, domain, storage schema, key epoch, and
bytes. Publication uses descriptor-relative staging, atomic rename, directory
durability, and inode/digest verification. If cleanup cannot prove ownership of
the installed pathname, AgentNet moves the product-visible name to a random
owner-only `.agentnet-quarantine-*` file and retains the exact bytes for
authenticated operator inspection and out-of-band removal. It never unlinks a
pathname that a competing same-UID process could have replaced. Post-commit
close or durability uncertainty is an unknown publication outcome, not a
successful rollback claim.

JetStream may later carry wake/fanout behind the transactional outbox if benchmarks show dispatch saturation. It never becomes a second source of truth. Kafka/Pulsar are later log/analytics choices at demonstrated scale.

### 14.2 Deployment profile

- Linux x86_64 and arm64 first for production-topology qualification, after exact distro, service lifecycle, IPC, storage, and update tests.
- Current owner instruction (2026-07-17) requires the installable package and local conformance mechanisms on Linux, macOS, and Windows now rather than deferring portable implementation. Real-host CI may establish only its named package, state, SQLite, IPC, and lifecycle contracts; privileged hostile-host, signed update/install/uninstall, semantic harness, and production topology gates remain separate.
- Two stateless core replicas and HA PostgreSQL for a production durable claim.
- Company-controlled deployment by default; a filesystem-backed artifact store is acceptable only for an explicitly weaker pilot, while production requires independently tested replication, backup, immutability, encryption, and restore behavior from the selected self-hosted backend.
- One region initially; residency and multi-region require explicit owner/legal decision.
- Separate credentials/processes for scanner, effect worker, public/federation gateways, approval, issuer/KMS, and audit witness.
- Privacy-aware metrics: queue age/depth, state counts, acceptance/delivery latency, database replication/outbox lag, authorization denials, revocation latency, scanner/audit lag, receipt gaps, model wakes/tokens/spend, quota trips, and adapter health—without plaintext or secrets.
- Unsupported OS, harness, or version fails installation or downgrades only a nonsecurity feature. It never silently weakens identity, authorization, durability, or isolation.

PD-010 must select a concrete minimum physical and administrative topology. Safe small-organization baseline:

- core replicas, dispatcher, and read-only facades may share a hardened compute host only under distinct OS/service identities and credentials, but replicas on one host do not count as HA;
- synchronous PostgreSQL primary/standby and object backup span declared failure domains;
- scanner, effect worker, public/federation gateways, and approval service use separate sandboxed processes/OS identities with no authority-key access; high-risk profiles may require separate hosts;
- issuer/KMS authority and independent audit witness are in different administrative accounts or under independent threshold administrators from the application/database operators;
- witness and audit export have redundant destinations or an approved bounded degraded backlog; they are not an accidental single point of silent failure;
- every co-location, failure domain, administrator boundary, key custodian, backlog ceiling, and stop rule is recorded.

### 14.3 Always-on server-agent role composition

An always-on AgentNet installation composes the same ordinary extension roles
used on a laptop. It is not a separate Hub product, privileged identity, or
universal authority. A deployment may compose:

- mailbox/relay;
- transactional room/task authority;
- identity and policy enforcement;
- artifact metadata/service;
- active server agent or effect worker;
- public A2A gateway;
- federation gateway;
- audit exporter.

These are separable roles with distinct credentials. A server agent with
sensitive data is an effect/data service behind the same verified-caller and
policy enforcement boundary; always-on placement creates no authority.

### 14.4 Normative failure and recovery contract

| Failed or uncertain role | Allowed behavior | Required state/receipt and recovery | Forbidden fallback |
|---|---|---|---|
| IdP or independent approval | Existing short sessions and already-active relationship edges only to their recorded status/expiry and while every stored/current binding remains valid; safe drafting | enrollment/recovery/elevation and new relationship consent/renewal unavailable; health receipt; recover with fresh ceremony | no cached identity, cached/caller-asserted consent, or agent-mediated approval |
| Relationship governance schema missing/altered/uncertain | No relationship authority evaluation or automatic custody | block startup/use; restore the exact clean-start v1 objects and migration record/checksum, then reconcile | no unsupported-store import, unilateral consent inference, schema downgrade, or object repair by guesswork |
| Issuer/KMS/authority signer | Deterministic reads already authorized where policy permits | hold new token, grant, authority receipt, key release; rotate/reconcile after recovery | no raw local/application key |
| PDP/status/revocation | Local drafting and non-sensitive diagnostics | protected acceptance/effect authorization_hold; current snapshot required on recovery | no stale allow or prose policy |
| Internal directory/capability registry | Existing exact non-security presentation hints only within bounded freshness | hold new protected discovery, route/key/endpoint selection, export, or capability-dependent work; reconcile current epochs | no global enumeration, stale trust, or self-advertised security fact |
| PostgreSQL durability boundary | Client retains local submission | no accepted_durable; visible unavailable/retry; fenced restore/failover | no in-memory/broker acceptance |
| Object store | Message without required artifact may remain held | artifact acceptance incomplete or artifact_integrity_hold; reconcile exact version | no fabricated URI, substitute bytes, or metadata-only availability |
| Scanner/transformer | Quarantine storage only | scan_pending/held; rerun exact scanner/profile after recovery | no stale/missing attestation release |
| Audit publisher/witness | Core commits audit_intent while bounded backlog remains | visible degraded_audit, sequence/checkpoint reconciliation; owner-approved ceiling/SLO | no silent unlimited backlog |
| Audit ceiling exceeded | Explicitly classified low-risk non-sensitive traffic only if policy permits | stop enrollment, elevation, federation changes, sensitive reads/preview/export/download/key release, and high-impact/irreversible effects until witnessed | no protected disclosure, high-risk admission, or effect |
| Budget/quota service | Deterministic queue, cancel, revoke, security control | semantic/model/tool/effect work budget_hold; reconcile counters | no unmetered model/effect work |
| Model-egress broker | Deterministic protocol control and explicit human viewing | queue semantic work as model_egress_hold; rebind short worker capability after recovery | no worker vendor credential, user auth state, or generic proxy |
| Effect worker/tool | Queue and diagnostics | pending, failed, or effect_unknown based on evidence; reconcile/adjudicate | no hallucinated completion or blind retry |
| A2A/federation gateway | Internal corporate operation continues | isolate external edge, revoke gateway credential, quarantine epoch, reconcile peers | no gateway-to-core broad credential |
| Supervisor/worker | Durable protocol control and local queue restore | restart from supervisor/core cursor; re-establish clean launch/task grant | no active-user-session fallback |
| Room owner/control | Existing policy-approved local reads/drafts | freeze control mutations; recover threshold or tombstone | no invented successor |
| Update metadata/channel | Run last trusted non-expired compatible build only if policy permits | visible update_degraded; recover trusted root/metadata | no unsigned, expired-security, rollback, or freeze bypass |

Every row has an owner-approved backlog ceiling, timeout/SLO, safety-lane behavior, recovery evidence, reconciliation procedure, and alert policy. Fault injection verifies that uncertainty moves to the named hold/degraded state rather than fail-open.

## 15. Phased roadmap

### Local conformance and release kernel

Freeze semantics before production claims:

- complete a reuse-versus-build inventory for each subsystem and run the component bake-off in §6.6 before approving custom implementation;
- evaluate AGNTCY OASF and Directory, evaluate SLIM delivery/offline/duplicate behavior, test the selected MLS implementation's membership removal and compromise recovery, and compare their operational burden with the thin corporate API and PostgreSQL-outbox baseline;
- map every reused result into actor-owned receipts, exact revocation/expiry, artifact holds, and `effect_unknown`; reject any candidate that needs a semantic workaround or silently weakens a gate;
- versioned event, identity, room, task, artifact, receipt, grant, relationship, revocation, presence, audit, and error schemas;
- supervisor and four dedicated-worker adapter implementations with exact external-evidence gates;
- non-production transactional core, object store, scanner, approval stub, and isolated A2A gateway;
- exact A2A v1 and stable MCP profiles;
- identity, enrollment, credential, authorization, and data-class profiles;
- cross-language crypto vectors;
- declarative state-machine and property tests;
- hostile-content, abuse, local IPC, and signing-oracle corpora;
- PostgreSQL durability/load/failover benchmark;
- every release re-pins the exact supported PostgreSQL release used by the benchmark; PostgreSQL 18 was the research reference, not an implicit permanent dependency;
- owner decisions PD-001 through PD-011.

Exit: no production-secure, federated, E2EE, or accepted_durable claim without its named gate.

### Limited owner deployment

A reversible pilot may validate novelty before production HA:

- one owner and three to five harnesses;
- explicitly named accepted_local durability, never accepted_durable;
- Pi and Claude adapters first if they match the actual fleet, then Codex and Antigravity;
- direct messages, tasks, explicit inbox, and passive counts; file tests use synthetic/non-sensitive bytes until cross-store, scanner, retention, and deletion gates pass;
- no contractors, E2EE, or protected public A2A data access;
- enrollment uses the final independent-device/security-boundary ceremony; any noncompliant lab identity is synthetic-only and cannot be promoted;
- semantic processing uses the full clean-worker plus model-egress-broker profile; otherwise the pilot is deterministic control plus explicit human-opened viewing only;
- no claim that ARC-003, ARC-004, AVL-007, or production security is satisfied.

This reconciles the useful early-pilot proposal with the operational objection: a single-node pilot is allowed only when its weaker state and scope are explicit.

### Phase 1 — Single-domain production

- all four adapters pass isolation/recovery tests;
- Linux-first production supervisor topology qualification; the shared package and local conformance adapters remain available on Linux, macOS, and Windows without promoting production support;
- HA PostgreSQL and a self-hosted, provider-neutral immutable artifact store;
- C1/C2 direct communication, tasks, files, rooms, offline delivery, receipts, policy, elevation, audit, scanner, and effect worker;
- isolated native A2A gateway passes conformance/security tests and is enabled for Corporate GA;
- no federation or C3 MLS yet.

The A2A gateway is a Phase-1 Corporate-GA critical path because ARC-004 is HARD. Engineering may stage it as Phase 1B after the internal Phase-1A core, but GA cannot claim ARC-004 until the full A2A conformance and security gate passes. This front-loads a substantial external-security workload and must be reflected in schedule and staffing.

### Phase 2 — Bilateral federation pilot

- one partner;
- host-local guest identities and keys;
- sponsor lifecycle, assurance mapping, host kill, CAEP/SCIM reconciliation;
- cross-domain receipts, revocation SLO, incident and partner-outage drills;
- no multilateral or transitive trust.

### Phase 3 — Measured scale and governance

Only when trigger evidence exists:

- JetStream wake/fanout behind outbox;
- specialized OpenFGA/SpiceDB authority replacing Cedar if benchmarked need is real;
- SPIFFE/SPIRE for a real server workload fleet;
- workflow engine for complex long-running effects;
- regional ownership/sharding;
- richer room governance and partner operations.

### Phase 4 — Selective E2EE and transparency

- C3 MLS rooms after explicit privacy/legal policy and full cryptographic lifecycle tests;
- private SCITT or equivalent cross-domain non-equivocation only if partners or compliance require it;
- multilateral identity federation only after bilateral operations no longer scale.

## 16. Alternatives and trigger conditions

| Alternative | Decision now | Why not now | Trigger to reconsider |
|---|---|---|---|
| **Build all infrastructure from scratch** | Reject | Reimplementing mature transport, crypto, identity, room, storage, and workflow machinery increases security, schedule, maintenance, and interoperability risk | Custom-build only the corporate supervisor/governance integration and components whose bake-off proves no suitable implementation passes the gates |
| **AGNTCY components** | Evaluate individually first | OASF, Directory, SLIM, Identity, and reference integrations may save major work, but maturity and their authority/offline/revocation semantics require proof | Adopt each component behind a replaceable interface when §6.6 bake-off passes; never adopt the ecosystem wholesale as the corporate trust model |
| **MCP as the network** | Reject | Not durable peer messaging, identity, federation, or universal across harnesses; Pi lacks it by design | Never as the corporate fabric; retain as local binding |
| **A2A as the internal fabric** | Reject | External task/message interoperability lacks corporate room, identity, authority, mailbox, file, receipt, and governance semantics | Use only as isolated boundary; reevaluate if the standard later covers the full trust model |
| **Mandatory managed cloud services** | Reject | Creates avoidable external trust, availability, residency, cost, and lock-in dependencies | Permit only as explicit owner-selected adapters when a concrete requirement justifies them |
| **Opportunistic peer-assisted mesh** | Future profile; design seams now | Message relay is tractable, but authority freshness, revocation, sequencing, partitions, custody thresholds, abuse, and recovery need a separate proof program | Enable direct/relay mode after encrypted custody and partition tests; enable distributed authority only after quorum/revocation gates pass |
| **PostgreSQL transactional mailbox** | Recommend, benchmark-gated | Best fit for multi-object atomic authority; operational and failure profile still needs proof | Replace only if declared RPO/RTO/load tests fail |
| **JetStream canonical store** | Reject | Split truth and default durability semantics conflict with transactional authority | None without redesign |
| **JetStream outbox-backed wake/fanout** | Defer | Adds operations before measured dispatch need | PostgreSQL dispatch/fanout saturates while canonical acceptance remains healthy |
| **Kafka/Pulsar** | Defer | Excess operational surface for initial scale; does not solve arbitrary-effect exactly once | Sustained analytics/event/geo-log scale |
| **Matrix core** | Reject, retain concepts | Multi-writer state resolution, replicated history, privacy, and operator complexity | Partition writes or open Matrix interop becomes HARD |
| **XMPP core** | Reject, retain ACK/MUC concepts | Requires a fixed XEP/security overlay and duplicates identity/authorization | Contractual XMPP interoperability |
| **Cedar** | Recommend candidate | Clear default-deny and forbid semantics; graph/list performance not yet proven | Benchmark failure causes replacement by one OpenFGA/SpiceDB authority |
| **Cedar + OpenFGA dual engine** | Reject | Split positive authority and revision races | Only after a formally proven intersection architecture; not default |
| **Universal origin signature** | Reject | Unnecessary complexity for same-domain managed traffic and mixed field ownership | Require per evidence/threat class |
| **Exact-byte JWS** | Initial interoperability profile | Simple detached authority objects; cross-language and performance still need proof | COSE or DSSE envelope if vectors/size/latency fail |
| **DSSE/in-toto file statements** | Initial interoperability profile | Strong provenance ecosystem but operational overhead | Smaller exact-byte manifest/attestation if interoperability fails |
| **Universal managed-service plaintext** | Reject | Excess insider/privacy blast radius | C1/C2 only by policy |
| **Universal MLS/E2EE** | Reject | Eliminates scanning, search, tools, recovery, and legal functions; complex lifecycle | C3 only after explicit class and tests |
| **SCITT everywhere** | Defer | Unneeded launch complexity | Cross-domain inclusion/non-equivocation becomes contractual |
| **SPIFFE/SPIRE on laptops** | Reject | Workload identity does not prove human; endpoint operations mismatch | Managed fleet already runs it, while human binding remains separate |
| **Multilateral/OpenID Federation** | Defer | Trust governance, metadata, and revocation complexity | Bilateral partner operations become unmanageable |
| **Active-active multi-region** | Reject initially | Authority, ordering, fencing, data residency, and recovery complexity | Approved RTO/residency requires it and sharded tests pass |
| **Microservice-first deployment** | Reject initially | Operational and trust-surface cost exceeds initial scale | Independent scaling or administration justifies extraction while trust roles remain explicit |
| **Temporal/workflow engine** | Optional internal detail | Communication state machine is simpler in the transactional core | Effect orchestration complexity outgrows explicit state machine |
| **Automatic foreground digest** | Reject as compliant behavior | Alters active context and expands prompt-injection/attention surface | Content may enter only through explicit open/read action; an owner-approved experiment cannot satisfy UX-001/002 without an approved requirement change |
| **All-OS launch** | Reject | Unverified IPC, key custody, service/update lifecycle | Each OS graduates through full native test matrix |

## 17. Dispute resolution register

| DR | Final decision | Evidence class | Remaining gate or owner |
|---|---|---|---|
| **DR-001** | Dedicated autonomous worker plus optional content-free indicator; automatic foreground digest disabled. Claude advocated Lane-2 pilot digests; Codex/Pi security analysis won because even next-turn context changes the active prompt surface. | Surfaces verified; external-evidence-gated | Four-harness zero-interference/recovery tests |
| **DR-002** | A2A v1.0.1 package, wire 1.0, no downgrade, isolated gateway, curated trust registry | Verified | TCK plus security and cross-SDK suite |
| **DR-003** | Supervisor API is common; MCP is optional local tool binding and absent by design in Pi | Verified | Adapter conformance and spec pin |
| **DR-004** | PostgreSQL canonical mailbox/control plus versioned object store, staged cross-store artifact acceptance, and outbox; optional wake broker later | External benchmark/evidence gated | DB/object crash permutations, load, failover, restore, PD-010 |
| **DR-005** | Domain/OIDC principal, exact OOB approval, per-domain/per-harness DPoP key, opaque tokens | External-evidence-gated | OS custody, enrollment, recovery, revocation; PD-001/002/009/010 |
| **DR-006** | Human-only positive entitlement; harness/device deny-only eligibility | Owner-policy default | PD-003 and AUTH-003 wording confirmation |
| **DR-007** | One Cedar candidate over one transactional authority; single-engine fallback | External benchmark/evidence gated | Correctness, graph/list, scale; PD-004 |
| **DR-008** | Event-specific acceptance/delivery/read/effect authorization; history continuity and compromise quarantine | Owner-policy default | PD-005 plus state tests |
| **DR-009** | One room sequencer, separate governance threshold, fenced transfer and tombstone | Owner-policy default, model-test-gated | PD-006 plus model/fault tests |
| **DR-010** | Bilateral allowlist, pairwise human+harness identity, host-local guest/key/token, sponsor and host kill. Earlier fixed four-hour/24-hour grace suggestions were rejected as unsafe universal defaults; continuity ends at the owner-approved short host-token/SLO boundary. | Owner-policy plus external evidence | PD-008/009 and federation incident tests |
| **DR-011** | C0–C3 content classes; managed Phase 1; explicit MLS/inspection semantics for C3 | Owner-policy default | PD-007 and MLS suite if enabled |
| **DR-012** | Domain-owned receipt facts, per-recipient state, first-class effect uncertainty and adjudication | External-evidence-gated | State/property/fault tests; PD-005/009 |
| **DR-013** | Separate origin/acceptance/relay/commit/effect objects; purpose-specific exact-byte signing | External-evidence-gated | Cross-language, key, performance, federation/E2EE tests |
| **DR-014** | Immutable artifact versions; separate acceptance/derivation/scan/release/audit evidence; SCITT optional | External-evidence-gated | Scanner, provenance, audit crash/omission, retention |
| **DR-015** | Authenticated content remains hostile; clean-worker launch, exact task grants, source/sink taint controls, four lanes, budgets, typed signer, truthful IPC assurance, and supply-chain bounds | External benchmark/evidence gated | Adaptive intent/exfiltration red team, sandbox escape, fuzz, cost/loop/IPC soak |
| **DR-016** | Linux-first modular core with explicit process, physical failure-domain, and administrative/key/audit boundaries | Owner-policy plus external evidence | PD-010/011 and all Phase-1 gates |

## 18. Owner policy decisions

Every item requires recorded confirmation. The local implementation may test the safe default; it cannot silently convert the default into organizational policy.

| PD | Recommended safe default | Viable alternative and consequence | Required confirmation |
|---|---|---|---|
| **PD-001 Canonical human** | Opaque domain principal mapped to OIDC issuer/subject; verified email alias/history | Email as authority is simpler but risks reassignment, issuer/domain collision, and migration ambiguity | Identity owner; approves refinement of ID-001/AUTH-003 |
| **PD-002 Enrollment/recovery** | Owner-controlled WebAuthn UV plus an exact-transaction 128-bit one-time code, five-minute expiry, and five-attempt bound; confirmation is independent of the enrolling harness; default server colocation uses distinct OS identities and reports `independent_boundary_proven=false` | Separately administered approval hosting is the optional high-assurance tier; extra people, hosts, and relay channels are not ordinary-onboarding prerequisites | Recorded for the ordinary self-hosted profile on 2026-07-19; high-assurance administration/recovery evidence remains open |
| **PD-003 Harness attenuation** | Hard binding, revoke, proof, capability, freshness constraints now; posture/classification only after approval | No posture enlarges weak-device blast radius; broad opaque posture creates inconsistent denial | Explicit AUTH-003 clarification and appeal rules |
| **PD-004 Elevation** | One independent approver ordinary; two high-impact/break-glass; beneficiary cannot approve; short nonrenewable grants | One everywhere raises insider risk; two everywhere adds delay/fatigue | Risk classes, approvers, TTL/use, break-glass |
| **PD-005 Post-revocation** | Preserve accepted inert history only within authorized retention after ordinary offboarding; recheck custody, downloads, grants, and effects; quarantine conservative compromise windows | Purge-all enables censorship; acceptance-forever conflicts with legal erasure/minimization and preserves stale attacker effects | Event-class matrix plus legal erasure/hold compatibility and compromised_from adjudication |
| **PD-006 Rooms/history** | Creator/host domain owns one sequencer; separate recovery threshold; from-join history; explicit transfer; tombstone on unrecoverable loss | Multi-writer improves partition writes with major complexity; neutral owner adds trust/residency dependency | Governance, history, guests, retention, deletion/legal hold |
| **PD-007 Visibility/E2EE** | C1 managed default, C2 isolated services, C3 MLS only for sealed rooms; all inspection/recovery members visible; C3 LLM-provider exposure explicitly classified | Universal plaintext enlarges insider risk; universal E2EE removes managed service functions; C3 may permit approved provider, require local model, or be human-read-only | Privacy, security, legal owners; C3-at-launch, model-provider retention/training/residency, and LLM-member decision |
| **PD-008 Partner assurance** | Profiled home proof for ordinary access; fresh host-local reproof/admin for high risk | Home-only inherits partner failures; reproof-all adds friction/privacy burden | Per partner, resource, class, and action |
| **PD-009 Revocation continuity** | Host kill at next decision; no issuance during outage; privileged hold; bounded low-risk continuity to short-token expiry | Immediate deny couples availability; longer grace enlarges compromise window | Token TTL, signal SLO, outage ceiling, audit backlog |
| **PD-010 Operations** | Linux x86_64/arm64 first; RPO=0 for accepted_durable; one region; measured capacity/RTO and concrete host/failure-domain/admin/key/witness topology | All-OS/multi-region launch broadens unverified surface; weaker durability needs weaker state name | OS, scale, RPO/RTO, residency, quotas, staffing, co-location, witness redundancy/backlog; consolidates message/file/audit retention and legal-hold ownership with privacy/legal co-approval |
| **PD-011 Attention** | Routine traffic never notifies; content-free count only; notify exact approvals, confirmed incidents, expiring high-risk elevation, terminal unrecoverable failure with redacted metadata and no message content by default | More alerts interrupt/leak; no exceptional alerts delays response | Event classes, channels, quiet hours, redaction, escalation |

**ORG-006 remains a separate accountable-owner blocker.** The implemented
baseline fixes cryptographic/storage mechanics only. Before production use, the
owner must record eligible proposers, exception signers and administrative
revokers; thresholds and allowed circumstances; voluntary versus mandatory
relationship handling; notice, review, appeal and retention; and how legal or
incident authority is established. No implemented route or local test is that
approval.

## 19. Must-not-ship gates

Release is blocked until relevant gates pass:

1. **Foreground isolation:** zero routine agent content, context, turns, focus/input changes, permission prompts, or automatic digest enters an active user session.
2. **Worker recovery:** kill, restart, and compact every communication worker at every state edge; accepted state rebuilds from supervisor/core with no cross-session contamination or duplicate effect.
3. **Harness/worker isolation and model egress:** Claude needs approved Channel or clean headless fallback with no remote permission relay; Codex approval routing remains independent; Pi RPC survives queued load with no cross-talk and processes no hostile semantic content without OS sandbox/model-only profile; Antigravity passes resume, cost, approval, status, clean-HOME, secret, filesystem, process, IPC, DNS, and network escape tests. Every semantic adapter proves a supervisor model-egress broker path with no worker vendor credential or generic proxy. A harness that fails is deterministic-only.
4. **A2A:** zero TCK MUST failures; reviewed SHOULD failures; HTTP+JSON/JSON-RPC advertisement/preference and binding/media/version mismatch; per-agent Card/route and tenant mismatch; standing-grant revoke; Task versus direct-Message result; all StreamResponse variants; unspecified role/state; server-owned IDs; input/auth-required; Task push-gap/duplicate reconciliation; direct-Message stream loss; credential-origin confinement; downgrade, trust, rotation, cache, revoke, enumeration, callback, SSRF, artifact, and cross-SDK tests pass.
5. **MCP/local API:** identical explicit tool round trip through every supported binding; Pi passes without MCP; no identity from arguments or token passthrough.
6. **Identity/enrollment:** OIDC mix-up/state/nonce/PKCE, OOB substitution, key proof, stolen-token, DPoP replay/audience/domain, rotation, recovery, offline revoke, and credential leakage tests pass for every advertised OS tier. No enrolling/enrolled harness can read or auto-complete the independent approval channel; same-machine Slack/Telegram fails unless a tested boundary proves independence.
7. **Local IPC/signing oracle:** wrong, sibling, same-user, PID-reuse, socket-replacement, copied-capability, arbitrary-sign, replay, flood, and broker-restart attempts yield zero unauthorized proof, signature, or token at the claimed assurance tier. If exact same-UID adapter attribution cannot be proven, the one-compromise-domain fallback allows only local draft, explicit human view, and deterministic non-business control—zero protected business message, task, room/file action, read, disclosure, export, download, key release, or effect.
8. **Authorization and intent:** schema/diagnostic/missing state denies; human entitlement is the only positive source; self/duplicate approval fails; one-use grant creates one reservation; committed revocation affects the next protected decision; every decision uses one coherent revision. Semantic-derived effects/disclosures also require an exact task grant and source/sink policy. Relationship proposals have zero authority; exact subordinate-owner consent, policy-exception consumption, a completed exact local activation intent for either basis, renewal, concurrent revoke/renew, subject exit, expiry, clean-start-v1 verification, and unsupported-store rejection pass lifecycle/race tests. The local intent is provenance, not an independent witness. ORG-006 owner approval—not hidden code—is additionally required before exception/admin-override/mandatory-relationship/notice/appeal policy can ship. Directional assignment tests prove that an in-scope administrator-to-subordinate task reaches `accepted_queued`, while subordinate-to-administrator and peer-to-peer tasks remain `pending_human` with zero execution unless a separate active `may_assign` edge authorizes the exact direction and scope. Typed incompatible intents enter atomic `conflict_pending`; only the exact current subordinate owner can decide a revision-bound complete partition, with stale/racing decisions fenced and overlapping rejects propagated before release. Omitted deadlines must be server-derived at whole-second precision, capped by the full scope duration and strictly before relationship expiry, and bound into request/event custody bytes. Generic mailbox, conversation, and supervisor paths must withhold task/task-linked-control payloads even when a marker is absent; no protected TaskGrant release route exists in the current build. Acceptance or conflict release must never bypass recipient-side data or effect authorization.
9. **Durability:** crash, hard reset, upload reservation/object/promotion permutations, concurrent retry/reservation expiry, missing-version recovery, required/optional attachment behavior, fanout, 1-hour/7-day/30-day offline, reconnect storm, failover, fencing, PITR, restore, and resource-pressure tests produce no accepted loss or available artifact without verified object+manifest under the declared model.
10. **Delivery/effects/expiry:** actor-owned per-recipient state-machine/property tests prove no completion/cancellation/global-success without owning evidence; effect_unknown is never blind-retried; idempotency/receipt contradictions are detected. Delivery expiry uses authoritative clock/policy evidence, cannot reopen on retry/late receipt, and races safely with commit/cancel/effect; retention remains independent. Exact content stays fetchable only during authorized retention.
11. **Revocation:** full acceptance/delivery/read/effect matrix passes for human, harness, recipient, room, guest, key-compromise window, domain quarantine, stale policy, and E2EE epoch; no sibling reroute.
12. **Room authority:** model-check one active owner, frozen immutability, transfer race/crash, key compromise, permanent loss, fork labeling, history, and MLS/application epoch independence.
13. **Files/audit:** hostile-file, decompression/parser, scanner substitution/staleness, lineage, object swap, E2EE linkage, authorization-at-download, cross-domain/tenant/class equality, timing/quota probing, non-disclosing duplicates, audit-intent crash at authorize→intent→capability/key/bytes release, bounded backlog, omission/gap/fork/witness, deletion/legal-hold, and restore tests pass.
14. **Signed-peer abuse and intent:** zero unauthorized **or unintended** protected effect, sensitive read, disclosure, or exfiltration. At least 1,000 adaptive trials per supported model/config is a floor, not proof: require independent generation, deterministic source/sink oracles, seeded secret canaries, data-class/effect coverage, statistical reporting, and full rerun after any model, harness, prompt, tool, parser, policy, or launch-profile change. Loop, fanout, reconnect, and cost floods stay within budgets and preserve safety capacity.
15. **Supply chain:** signed install/update/uninstall, threshold-root rotation, rollback/freeze rejection, SBOM/provenance, canary, compromised-adapter disable, and credential cleanup pass for every supported OS/architecture.
16. **Operations/failure and component adoption:** demonstrated backup restore; every trust role passes the §14.4 degraded/recovery contract and approved backlog/stop rules; minimum physical/admin topology is deployed; kill switches meet SLO under load; unsupported combinations fail or remain queued without security downgrade. Every adopted third-party component has a pinned version, license/provenance/SBOM review, self-hosted deployment, upgrade/rollback and data-egress assessment, failure/revocation/offline/duplicate tests, canonical-schema mapping, replacement plan, and a recorded bake-off against the simpler baseline. No greenfield subsystem is approved without recording why maintained candidates failed the relevant gates.
17. **Policy:** no feature relying on PD-001 through PD-011 launches until the accountable owner records the chosen default or alternative and accepts its consequence. ORG-006 independently blocks production claims for relationship exception/override, mandatory-relationship, notice, review, and appeal policy.
18. **Discovery/version/config:** authorization-filtered non-enumerating directory, stale/forged capability and key/endpoint rotation tests; authenticated profile handshake; N/N-1 rolling upgrade, critical-field, downgrade, queued-unsupported-event, rollback, and redacted config migration/rebinding tests pass.
19. **Freshness/crypto/audit roots:** exact vectors and boundary tests pass for proof age/skew/nonce/jti/cache persistence/key epochs; transport/DEK/KEK/decrypt ACL/backup/rotation profiles pass; catastrophic audit trust roots, independent log reconciliation, witness outage/backlog, and compromise rebuild are demonstrated.

Any unauthorized effect or exfiltration, false durable/completion claim, active-session injection, positive authority expansion, missing required audit intent, uncontained public gateway, or silent security downgrade blocks release regardless of average benchmark results.

## 20. Requirement-by-requirement verification

Status meanings:

- **Covered:** the selected design directly addresses the requirement.
- **Changed hypothesis:** the user’s proposed mechanism was compared and refined.
- **Owner decision:** the architecture supplies a safe default, but human approval is still required.
- **External evidence gate:** local design or implementation exists, but required target-environment or independent evidence remains open.

No row below claims implementation completion.

### 20.1 Product and interoperability

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **ARC-001** Separate extension | Covered | Supervisor, adapters, and core install independently of any harness (§4). Verify install, upgrade, removal, and credential cleanup. |
| **ARC-002** Agent agnostic and reuse-first | Covered | Common corporate semantics sit above harness and reusable-component adapters; §6.6 requires narrow replaceable interfaces and forbids component-defined authority. Adapter and bake-off gates remain. |
| **ARC-003** Four frontier harnesses | External evidence gate | Concrete Claude, Codex, Pi, Antigravity paths in §5. All four must pass isolation/recovery/version tests before GA. |
| **ARC-004** Native A2A | External evidence gate; interpretation explicit | Each agent interoperates via the domain’s native client/server gateway; per-harness wire stacks are intentionally not required. Discovery, auth, Message/Task/Artifact mapping, versioning, TCK and negative tests are in §6.1 and gate 4. |
| **ARC-005** Internal transport independent of A2A | Covered; bake-off pending | Corporate typed API and transactional semantics remain authoritative while AGNTCY SLIM and other maintained transports are evaluated rather than recreated (§6.3, §6.6, §14). |
| **ARC-006** External trust isolation | Covered, gate pending | All public peers start external-low-trust; curated registry never grants membership by itself (§6.1). Gateway containment tests remain. |

### 20.2 Identity and enrollment

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **ID-001** Verified human principal | Covered with owner clarification | Domain/OIDC principal plus verified email alias/history (§7.1). PD-001 approves refinement from raw email authority. |
| **ID-002** Independently authenticated human confirmation | Design covered; external evidence gate | Exact transcript, WebAuthn UV outside the enrolling harness, and active 128-bit code bound to that transaction (§7.2). Shared-host default must preserve distinct OS identity/storage/credentials and report `independent_boundary_proven=false`; separate administration remains optional high-assurance evidence. |
| **ID-003** No self-asserted identity | Covered | Payload claims ignored; principal and harness derive from authenticated context (§3, §7). Negative spoof tests remain. |
| **ID-004** Cryptographic binding | External evidence gate | Per-domain/per-harness key, proof of possession, DPoP credentials (§7.2–7.3). Platform custody/replay tests remain. |
| **ID-005** Signing-key enrollment hypothesis | Changed hypothesis | Retain PoP keys, but replace bare key exchange with OIDC, exact OOB approval, lifecycle, DPoP, and purpose-specific origin signatures. |
| **ID-006** Human/harness distinction | Covered | principal_id and harness_id are distinct and both mandatory (§7.1). |
| **ID-007** Harness-level revocation | Covered, gate pending | Credential/harness epochs revoke one installation without siblings (§7.3). Revocation tests remain. |
| **ID-008** Domain-scoped identity | Covered | IDs and keys are per trust domain; federation creates host-local guest IDs (§7.1, §12). |
| **ID-009** Credential lifecycle | Covered, external evidence gate | Issuance, storage tiers, rotation, expiry, loss, recovery, compromise, offboarding in §7.3; OS and incident drills remain. |

### 20.3 Verified caller and authorization

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **AUTH-001** Verified caller on every operation | Design covered; actor union explicit | Business/protected requests resolve human or host guest + exact harness. Service receipts resolve a scoped workload and, for any data/effect, an authorized parent human event/task grant. External A2A remains unverified-low-trust (§3, §7.1). |
| **AUTH-002** Identity bound to proof | Covered, gate pending | TLS/DPoP, workload mTLS, local IPC, purpose-specific signatures; never payload prose (§6–8). |
| **AUTH-003** User-level permissions | Covered with owner clarification | Positive entitlement only at human; harness constraints deny-only (§8.1). PD-003 approves precise attenuation semantics. |
| **AUTH-004** Harness attribution | Covered | Harness/session/credential epoch recorded on events, decisions, and audit (§7–9). |
| **AUTH-005** No authority escape | Covered | Task and management relations cannot carry positive data permissions (§8.2–8.3). Property tests remain. |
| **AUTH-006** Hub-side gating | Covered by same-extension server role | Edge PEP and current policy check precede sensitive existence disclosure or effect; no separate Hub identity exists (§4, §8). |
| **AUTH-007** Fail closed | Covered, gate pending | Missing/stale/diagnostic/inconsistent state denies (§8.1). Error and outage corpus remains. |
| **AUTH-008** Human-approved elevation | Covered | Independent exact-transaction approval service (§8.4). |
| **AUTH-009** Constrained elevation | Covered, gate pending | Scoped, expiring, non-self-approved, use-counted, revocable grant object (§8.4). Atomicity tests remain. |
| **AUTH-010** Approval policy | Owner decision | Safe thresholds and alternatives in PD-004 (§18). |

### 20.4 Organizational relationships

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **ORG-001** Multiple administrators | Implemented locally | Bilaterally active, exact directed pairs form a many-to-many graph (§8.2); production policy/evidence gates remain. |
| **ORG-002** Multiple subordinates and downward assignment | Implemented locally | Current scoped `may_assign` edges permit automatic `accepted_queued` custody with an exact explicit or server-derived, whole-second, scope/relationship-bounded deadline. Generic reads disclose only a custody reference and no protected TaskGrant release route exists, so custody grants no execution authority (§8.2–8.3). |
| **ORG-003** Graph, not tree | Implemented locally | Relations are explicit, typed, versioned, consent-bound, and pair/lifecycle-fenced (§8.2). |
| **ORG-004** Management not data authority | Implemented locally | Exact completed local activation intent and assignment checks limit the edge to custody meta-actions. Task/task-linked-control payloads are withheld from generic mailbox, conversation, and supervisor reads, including records with no marker; no protected-data, semantic, tool, or effect authority is granted (§8.2). |
| **ORG-005** Directional assignment and conflict resolution | Implemented locally | Downward scoped assignments may auto-queue; upward/lateral assignments remain `pending_human` unless a separate active directed edge exists. Exact relationship revision, owner/credential/policy epochs, expiry, and revoke/renew fencing are implemented. Typed incompatible intents enter atomic, arrival-order-independent `conflict_pending`; exact current subordinate-owner adjudication binds all members and revisions, permits only compatible releases, fences races/replay/drift, propagates overlapping rejects, and queues only fully unblocked custody. It grants no data, semantic, tool, or effect authority (§8.2 and gate 8). Production owner and external evidence gates remain. |
| **ORG-006** Relationship lifecycle | Mechanics implemented; accountable-owner blocked | Zero-authority proposal, exact current subordinate human/guest-owner consent, separately recorded/consumed exception mechanism, and a completed exact local activation intent for either basis, plus renewal, expiry, subject exit/admin-revoke mechanism, race fencing, clean-start schema-v1 verification, and unsupported-store rejection are implemented. The activation intent is local provenance, not an independent witness. Eligible roles, exception/override circumstances/thresholds, mandatory relations, notice/review/appeal, retention, and production evidence remain unapproved (§8.2, §18, gate 17). |

### 20.5 Communication and collaboration

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **COM-001** Agent-to-agent | Covered | Canonical direct events and mailboxes (§9.1). |
| **COM-002** Agent-to-hub | Covered by same-extension server role | Supervisor to the ordinary AgentNet core/effect role on an always-on server agent (§4, §9). |
| **COM-003** Hub-to-agent | Covered by same-extension server role | Durable recipient mailbox and cursor delivery from an ordinary always-on AgentNet installation (§9.5–9.6). |
| **COM-004** Hub-to-hub | Covered as server-agent relay; federation gate pending | Domain-owned relay receipts and federation/public gateways between ordinary AgentNet installations (§9.3, §12). |
| **COM-005** Group topologies | Covered | Per-recipient fanout state supports all cardinalities (§9.1, §9.4). |
| **COM-006** Direct conversations | Covered | Conversation/thread objects and durable events (§9). |
| **COM-007** Manager communication | Covered | Direct communication is bidirectional, while automatic task acceptance follows authenticated directed management edges and scope (§8.2–8.3, §9.1). |
| **COM-008** Rooms and meetings | Covered, external evidence gate | Persistent/temporary room model, membership, governance, history, artifacts (§10). |
| **COM-009** Participant clarity | Covered with minimization | Corporate contributions show scoped verified human/guest label, domain, harness, optional session; service facts show workload role; public A2A shows external-human-unverified. Email mapping is restricted (§7.1, §9.1–9.2). |
| **COM-010** Room governance | Owner decision plus model gate | Creation, membership, moderation, history, guests, archive/delete, transfer in §10 and PD-006. |
| **COM-011** Conversation semantics | Covered | Threads, replies, mentions, tasks, handoffs, cancellation, receipts, completion/unknown states (§9). |

### 20.6 Files and artifacts

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **FILE-001** First-class sharing | Covered | Artifact references work in every event topology (§9.1, §11). |
| **FILE-002** Identity and authorization | Covered | Current authorization at upload, release, relay, preview, download, and key release (§11.1). |
| **FILE-003** Offline survival | Design covered; durability gate | Staged object-version→verification→manifest/event transaction, holds/orphans, restore, and durable mailbox (§11.4, §14). |
| **FILE-004** Integrity/provenance | External evidence gate | Immutable digests, exact origin/derivation/scan evidence, and the initial DSSE/in-toto interoperability profile (§11.2). |
| **FILE-005** Storage/retention | Owner decision | Versioning, encryption, quotas, dedup, lifecycle, WORM/legal hold modeled. PD-010 consolidates message/file/audit retention, with PD-006/007 plus privacy/legal co-approval. |
| **FILE-006** Content safety | External evidence and owner gate | Quarantine, scanner sandbox, DLP/malware/polyglot/archive/executable rules (§11.3). |

### 20.7 Availability and durable delivery

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **AVL-001** Mixed availability | Covered | Local supervisors plus always-on corporate core (§4, §9.6). |
| **AVL-002** Offline is normal | Covered | Offline presence is distinct from identity/revocation (§9.6). |
| **AVL-003** Durable store-and-forward | External evidence gate | Local encrypted queue, PostgreSQL mailbox, object store, cursor reconciliation (§9.6, §14). |
| **AVL-004** Baseline logical Hub and future hubless profile | Covered by one ordinary extension; future gates pending | The same AgentNet package composes continuously available server-agent roles with redundant production deployment; there is no separate Hub product or identity. Section 9.7 preserves direct, opportunistic-relay, and fully distributed upgrade paths without central-instance identifiers or authority (§9.7, §14). |
| **AVL-005** No silent loss | Covered, state tests pending | Explicit acceptance, queue, delivery, commit, expiry, rejection, failure, unknown, DLQ states (§9.4). |
| **AVL-006** Delivery guarantees | Covered, external evidence gate | At-least-once, idempotency, dedup, causal sequence, replay, cancellation, reconciliation (§9.5). |
| **AVL-007** Hub failure and hubless operation | External evidence/owner gate | HA, synchronous durability boundary, fencing, PITR, and restore protect the first production server-agent profile; future mesh/distributed modes require custody, quorum, partition, sequencing, policy-freshness, and revocation gates. There is no separate Hub product (§9.7, §14, PD-010). |
| **AVL-008** Presence | Covered, external evidence gate | Expiring live/recent/stale/unknown lease, never authority (§9.6). |

### 20.8 Non-interrupting user experience

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **UX-001** Separate sessions | External evidence gate | Dedicated worker per harness (§5). Four-harness interference tests required. |
| **UX-002** Never interrupt flow | External evidence gate | No foreground content, turns, focus, input, or approvals (§5.1, gate 1). |
| **UX-003** Passive indication | Partially covered preference | Content-free count where safe; researched Codex has no verified compliant passive surface and is explicit-pull only. No-indicator fallback protects the HARD UX requirements (§5). |
| **UX-004** Noninteractive indicator | Covered, gate pending | No content or demanded interaction; status-only adapter surfaces (§5.1). |
| **UX-005** Attention policy | Owner decision | Only approvals, confirmed incidents, expiring high-risk elevation, terminal unrecoverable failure by PD-011. |
| **UX-006** Harness fallback | Covered | Headless/dedicated worker plus no indicator; explicit inbox only (§5). |

### 20.9 Federation and contractors

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **FED-001** Multi-company future | Covered | Bilateral profile and separate gateway (§12). |
| **FED-002** Internal user in external network | Covered | External host creates its own guest identity/grants; same non-transitive model (§12). |
| **FED-003** External guest internally | Covered | Sponsor invitation, home assertion, pairwise key, host-local guest/credential (§12). |
| **FED-004** Least privilege | Covered | Exact resources/actions/classes/expiry and host-local grant (§12). |
| **FED-005** No transitive trust | Covered | Explicit invariant; A→B never creates B→C (§12). |
| **FED-006** Domain context | Covered | Distinct IDs, keys, audiences, policy, and receipts per host domain (§7, §12). |
| **FED-007** Federation model | Changed open question | Bilateral host-controlled federation selected; multilateral deferred (§12, §16). |
| **FED-008** External assurance | Owner decision | Profiled home proof for ordinary, host-local reproof for high risk (PD-008). |
| **FED-009** Audit/revocation | External evidence and owner gate | Host kill, home signals, dual-domain receipts, sponsor lifecycle, incident ownership (§12, PD-009). |

### 20.10 Security, privacy, and accountability

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **SEC-001** Threat model | Design covered; adversarial gate | Candidate controls and catastrophic trust roots are explicit (§4.4, §13.5); campaigns remain. |
| **SEC-002** Encryption | Design/owner/external evidence gate | Transport floor, device keys, domain/tenant/class DEK/KEK hierarchy, decrypt ACLs, backup/rotation, C1/C2/C3 profiles (§7.4, §13.1). |
| **SEC-003** Auditability | Design covered; external evidence gate | Core-mediated transactional intents, independent logs/exporter/WORM/witness, assurance limits, backlog/outage/compromise rebuild (§4.4, §14.4). |
| **SEC-004** Privacy/minimization | Design covered; owner gate | C0–C3 content and metadata matrix, scoped participant identity/dedup/indexes, retention/disclosure policy (§11.1, §13.1). |
| **SEC-005** Replay/freshness | Design covered; external evidence gate | Versioned proof-age/skew/nonce/jti/cache/sequence/key-epoch/failure profile (§7.4). |
| **SEC-006** Compromise containment | Design covered; drills pending | Conservative compromise window, one-domain same-UID fallback, dependency failure matrix, quarantine/rekey/rebuild (§4.1, §7.3, §14.4). |
| **SEC-007** Extension trust | External evidence gate | Signed packages, SBOM/provenance, threshold updates, canary/rollback, zero-secret adapters (§13.4). |

### 20.11 Operations and implementation

| Requirement | Status | Design evidence and remaining proof |
|---|---|---|
| **OPS-001** Hub definition and replaceability | Covered by one ordinary extension | An always-on AgentNet installation is a self-hosted composition of mailbox, policy, effect/data, gateway, artifact, and audit roles, not a separate product or identity; transport-neutral event and custodian interfaces preserve future peer-assisted replacement (§4, §9.7, §14.3). |
| **OPS-002** Discovery | Design covered; external evidence gate | Non-enumerating authorization-filtered internal directory plus curated A2A/partner metadata and conformance probes (§6.4, §12). |
| **OPS-003** Versioning | Design covered; conformance gate | Authenticated profile handshake, critical fields, no security downgrade, N/N-1 expand/migrate/contract, rollback/deprecation (§6.5). |
| **OPS-004** Observability | Covered, external evidence gate | Privacy-aware delivery, DB, authorization, scanner, audit, cost, adapter metrics (§14.2). |
| **OPS-005** Resource controls | Covered, soak gate | Multi-dimensional quotas, fair queues, breakers, attenuated sub-budgets, safety capacity (§13.3). |
| **OPS-006** Portability and infrastructure independence | Design/owner/external evidence gate | Versioned redacted config, secret rebinding, independent installer, Linux-first, and self-hosted corporate services; cloud/provider integrations are optional adapters, never mandatory (§6.5, §9.7, §14.2, PD-010). |
| **OPS-007** Test strategy and reuse bake-off | Covered | Nineteen must-not-ship gates plus §6.6 require pinned component evaluation, semantic mapping, failure/revocation/offline/duplicate tests, operational comparison, and documented build-versus-reuse decisions (§6.6, §15, §19). |

### 20.12 Verification result

- **85 of 85 baseline requirements are explicitly addressed by the design.**
- **2 implementation hypotheses changed:** signing-key-only enrollment (ID-005) and a separate single/monolithic Hub product (AVL-004); the implemented shape is one ordinary AgentNet extension on laptops and always-on server agents.
- **1 HARD requirement interpretation is explicit:** ARC-004 is delivered by the extension’s native domain gateway on behalf of each enrolled agent, not by embedding a separate A2A stack in every harness.
- **A second HARD requirement interpretation is explicit:** AUTH-001 uses a typed actor union—business/protected submissions require verified human/host guest + exact harness; workload facts require scoped workload + parent human authority chain; unadmitted A2A remains external-human-unverified.
- **11 owner policy decisions remain:** PD-001 through PD-011.
- **External, benchmark, conformance, owner, and operations gates remain before any production “satisfied” claim.**
- No requirement was rejected as an outcome. Several proposed mechanisms were replaced with safer mechanisms that preserve the intended outcome.

## 21. Primary-source research index

Key sources used by the three research and re-research rounds, retrieved or rechecked 2026-07-12. This index is explanatory, not the release lockfile. Any URL using latest, current, main, or master is a supplemental drift check; every release manifest pins the exact specification release, SDK tag/commit, TCK commit/report, internal profile hash, and retrieval date.

### Protocols and harness integration

- [A2A protocol specification](https://a2a-protocol.org/latest/specification/)
- [A2A v1.0.1 release](https://github.com/a2aproject/A2A/releases/tag/v1.0.1)
- [A2A normative proto](https://github.com/a2aproject/A2A/blob/v1.0.1/specification/a2a.proto)
- [A2A conformance kit](https://github.com/a2aproject/a2a-tck)
- [Inspected A2A TCK commit](https://github.com/a2aproject/a2a-tck/commit/5996b79f9cefa6fc390980e383e358a66fb9e49e)
- [A2A Python SDK v1.1.0](https://github.com/a2aproject/a2a-python/releases/tag/v1.1.0)
- [A2A Go SDK v2.3.1](https://github.com/a2aproject/a2a-go/releases/tag/v2.3.1)
- [A2A Java SDK v1.1.0.Final](https://github.com/a2aproject/a2a-java/releases/tag/v1.1.0.Final)
- [A2A JavaScript v1 beta](https://github.com/a2aproject/a2a-js/releases/tag/v1.0.0-beta.0)
- [MCP stable 2025-11-25 specification](https://modelcontextprotocol.io/specification/2025-11-25/)
- [MCP 2025-11-25 release](https://github.com/modelcontextprotocol/modelcontextprotocol/releases/tag/2025-11-25)
- [MCP security best practices](https://modelcontextprotocol.io/docs/tutorials/security/security_best_practices)
- [Claude Code Channels reference](https://code.claude.com/docs/en/channels-reference)
- [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp)
- [Codex App Server documentation](https://learn.chatgpt.com/docs/app-server)
- [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks)
- [Codex MCP documentation](https://developers.openai.com/codex/mcp)
- [Google AX repository](https://github.com/google/ax)
- [AGNTCY documentation](https://docs.agntcy.org/)
- [AGNTCY project repositories](https://github.com/agntcy)
- [AGNTCY OASF](https://github.com/agntcy/oasf)
- [AGNTCY Agent Directory](https://github.com/agntcy/dir)
- [AGNTCY SLIM](https://github.com/agntcy/slim)
- [CoffeeAGNTCY reference integration](https://github.com/agntcy/coffeeAgntcy)

Pi and Antigravity adapter findings also relied on the exact locally installed documentation and binaries recorded in [Claude’s resolution memo](brainstorm-claude-resolution.md). Antigravity’s agy 1.1.1 surface evidence is single-source to that local probe—the Codex probe did not have Antigravity installed—so the release environment must reproduce it before reliance.

### Identity, authorization, and federation

- [NIST SP 800-63-4](https://pages.nist.gov/800-63-4/)
- [NIST authenticator and OOB requirements](https://pages.nist.gov/800-63-4/sp800-63b/authenticators/#oob)
- [WebAuthn Level 3 Candidate Recommendation snapshot](https://www.w3.org/TR/2026/CR-webauthn-3-20260526/)
- [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html)
- [OAuth DPoP — RFC 9449](https://www.rfc-editor.org/rfc/rfc9449.html)
- [OAuth native apps — RFC 8252](https://www.rfc-editor.org/rfc/rfc8252.html)
- [OAuth security BCP — RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html)
- [Cedar authorization semantics](https://docs.cedarpolicy.com/auth/authorization.html)
- [Cedar meta-permissions](https://docs.cedarpolicy.com/bestpractices/bp-meta-permissions.html)
- [OpenFGA access control](https://openfga.dev/docs/getting-started/setup-openfga/access-control)
- [SpiceDB consistency](https://authzed.com/docs/spicedb/concepts/consistency)
- [OpenID CAEP](https://openid.net/specs/openid-caep-1_0-final.html)
- [SCIM protocol — RFC 7644](https://www.rfc-editor.org/rfc/rfc7644.html)
- [OpenID Federation 1.0](https://openid.net/specs/openid-federation-1_0.html)

### Delivery, rooms, E2EE, files, and operations

- [PostgreSQL synchronous replication](https://www.postgresql.org/docs/current/warm-standby.html#SYNCHRONOUS-REPLICATION)
- [PostgreSQL continuous archiving and PITR](https://www.postgresql.org/docs/current/continuous-archiving.html)
- [NATS JetStream concepts](https://docs.nats.io/nats-concepts/jetstream)
- [Matrix room specification](https://spec.matrix.org/latest/rooms/)
- [XMPP multi-user chat](https://xmpp.org/extensions/xep-0045.html)
- [XMPP stream management](https://xmpp.org/extensions/xep-0198.html)
- [MLS — RFC 9420](https://www.rfc-editor.org/rfc/rfc9420.html)
- [MLS architecture — RFC 9750](https://www.rfc-editor.org/rfc/rfc9750.html)
- [HTTP message signatures — RFC 9421](https://www.rfc-editor.org/rfc/rfc9421.html)
- [JWS — RFC 7515](https://www.rfc-editor.org/rfc/rfc7515.html)
- [DSSE protocol](https://github.com/secure-systems-lab/dsse/blob/master/protocol.md)
- [in-toto attestation statement](https://github.com/in-toto/attestation/blob/main/spec/v1/statement.md)
- [in-toto attestation v1.2.0 release](https://github.com/in-toto/attestation/releases/tag/v1.2.0)
- [SLSA provenance](https://slsa.dev/spec/v1.2/provenance)
- [OWASP file upload guidance](https://cheatsheetseries.owasp.org/cheatsheets/File_Upload_Cheat_Sheet.html)
- [OWASP prompt-injection risk](https://genai.owasp.org/llmrisk/llm01-prompt-injection/)
- [OpenAI prompt-injection defenses](https://openai.com/index/designing-agents-to-resist-prompt-injection/)
- [TUF specification](https://github.com/theupdateframework/specification/blob/master/tuf-spec.md)
- [COSE Receipts used by SCITT — RFC 9942](https://www.rfc-editor.org/rfc/rfc9942.html)
- [SCITT architecture — RFC 9943](https://www.rfc-editor.org/rfc/rfc9943.html)
- [NIST incident response SP 800-61r3](https://csrc.nist.gov/pubs/sp/800/61/r3/final)

## 22. Final recommendation

Continue the **local conformance and release kernel** while closing every
locally implementable accepted finding. Production enablement remains separate:
it requires the named external, target-platform, accountable-owner, benchmark,
and operations evidence without reopening the one-extension product boundary.

The product should be described as:

> A secure, agent-agnostic corporate communication extension that gives each machine a durable communications supervisor, gives every harness a separately identified background worker, gives each corporate domain a transactional mailbox and policy authority, and exposes native A2A only through an isolated low-trust gateway.

The accountable owners must approve or change PD-001 through PD-011. Release
review must preserve the already implemented supervisor/adapter, identity,
authorization, mailbox, governance, provenance, artifact, and A2A boundaries
while requiring their still-open target-environment evidence before enabling
federation, E2EE, protected semantic execution, or production durability claims.
