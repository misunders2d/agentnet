# AGENTS.md — AgentNet Engineering Constitution

This file is the mandatory starting point for every coding, review, research,
or documentation agent working in this repository. It explains what AgentNet
is, which outcomes are non-negotiable, where the authoritative decisions live,
how the implementation is organized, what remains unproven, and how changes
must be validated.

If a task conflicts with this file or with the hard requirements, stop and
surface the conflict. Do not silently reinterpret the product to make a local
implementation easier.

## 1. Project in one paragraph

**AgentNet** is a separate, self-hosted, agent-agnostic communication extension
for Claude, Codex, Pi, Antigravity, future harnesses, and standards-compliant
A2A agents. It creates a secure corporate communication network in which
verified agents can exchange messages and files, collaborate in rooms, assign
and accept tasks, operate while users are offline, and participate in tightly
scoped cross-company federation. Every protected operation is attributed to a
cryptographically verified human principal and exact enrolled harness. The
same extension runs on intermittently connected laptops and on always-on server
agents. There is no separate privileged Hub product or Hub identity class.

## 2. What AgentNet is — and is not

AgentNet is:

- one independently installable extension with harness-specific adapters;
- a corporate identity, authorization, messaging, task, room, file, delivery,
  federation, and audit layer;
- self-hosted by default on company-controlled infrastructure;
- usable by ordinary laptop agents and ordinary always-on server agents;
- natively interoperable with external A2A agents through an isolated gateway;
- designed for durable offline operation and future peer-assisted/hubless modes;
- strict about verified caller identity, current authority, explicit intent,
  honest receipts, and fail-closed behavior.

AgentNet is not:

- a patch embedded into Claude, Codex, Pi, Antigravity, or agent-deck;
- a separate “Hub agent” or privileged central-agent product;
- an MCP network, even though MCP is supported as a local harness binding;
- an A2A-only internal architecture;
- a system where prompt text, role names, email strings, or payload fields grant
  identity or authority;
- a system where a manager relationship automatically grants access to the
  subordinate owner's data or permissions;
- dependent on AWS, S3, Azure, GCP, SaaS identity, or a managed broker;
- production-certified merely because local tests pass.

## 3. Authority and document precedence

Read these documents before making a material change:

1. [Hard requirements](docs/requirements.md) — the authoritative 85-ID product
   baseline. `HARD` requirements are mandatory outcomes; `PREF`, `HYP`, and
   `OPEN` labels retain their meanings from that document.
2. [Consolidated specification](docs/specification.md) — the researched design,
   architecture, state machines, decisions, rejected options, rollout, and
   requirement-by-requirement mapping.
3. [Final specification verification](docs/final-verification.md) — the
   adversarial audit of the agreed design.
4. [Requirements status](REQUIREMENTS_STATUS.md) — implementation and evidence
   status for every stable requirement ID.
5. [Implementation architecture](docs/ARCHITECTURE.md) and
   [schemas/interfaces](docs/SCHEMAS_INTERFACES.md) — current code boundaries.
6. [Must-not-ship evidence](docs/GATE_EVIDENCE.md) — release gate truth.
7. [Owner decisions](docs/OWNER_DECISIONS.md) — PD-001 through PD-011 and the
   safe defaults that apply until accountable owners sign decisions.
8. [Threat model and test plan](docs/THREAT_MODEL_TEST_PLAN.md) — adversaries,
   required evidence tiers, and stop-ship test design.
9. [Build-versus-reuse record](docs/BUILD_VS_REUSE.md) — component adoption
   discipline and semantic ownership.

Precedence for implementation work:

1. explicit current owner instruction;
2. hard requirements in `docs/requirements.md`;
3. normative decisions and invariants in `docs/specification.md`;
4. this engineering constitution;
5. implementation and lower-level documentation.

If lower-level documentation or code contradicts a higher level, treat it as a
bug. Do not “fix” the requirements to match accidental code behavior.

## 4. Stable requirement families

Every material PR or agent handoff must list the affected stable IDs. The 85
requirements are grouped as follows:

| Family | Scope | Non-negotiable outcome |
|---|---|---|
| `ARC-001..006` | product boundary and interoperability | separate, agent-agnostic, four-harness extension with isolated native A2A interoperability |
| `ID-001..009` | identity and enrollment | verified human plus exact harness, OOB confirmation, cryptographic binding, lifecycle and revocation |
| `AUTH-001..010` | verified caller and authorization | proof-derived caller context, human-level positive authority, fail-closed policy, bounded elevation |
| `ORG-001..006` | administration and subordination | many-to-many directed relationships without implicit data-authority transfer |
| `COM-001..011` | communication and collaboration | direct, server-agent, group, room, meeting, task, thread, and receipt semantics |
| `FILE-001..006` | files and artifacts | identity-bound, durable, integrity-protected, quarantined, scanned, policy-released sharing |
| `AVL-001..008` | availability and delivery | offline-normal durable custody, honest states, failover, reconciliation, future mesh path |
| `UX-001..006` | user experience | separate background sessions, no active-flow interruption, passive content-free indication |
| `FED-001..009` | federation and contractors | bilateral least privilege, no transitive trust, exact domain context, rapid revocation |
| `SEC-001..007` | security, privacy, accountability | explicit threat model, crypto, audit, minimization, freshness, containment, trusted updates |
| `OPS-001..007` | operations and adoption | replaceable logical roles, discovery/versioning, telemetry, quotas, portability, reuse bake-off |

Do not use family-level wording as a substitute for reading the individual
acceptance criteria in [the requirements](docs/requirements.md).

## 5. Core identity model

An AgentNet identity is not an email string and is not a harness name. Protected
operations resolve a typed verified actor that preserves the relevant layers:

- corporate trust domain;
- durable human principal, initially bound through workforce identity and a
  verified email alias;
- exact enrolled harness/agent instance;
- current credential and credential epoch;
- device/session/process eligibility where applicable;
- optional workload identity with explicit parent authority;
- host-local federated guest identity when operating across domains.

The human principal is the source of positive permissions. Harness, credential,
device, session, workload, posture, and capability facts can narrow or deny
authority; they do not independently create positive authority.

One human may enroll several harnesses. Each harness is separately attributable
and revocable without revoking the human or sibling harnesses. The same human or
harness may participate in multiple corporate networks, but each operation is
evaluated inside one explicit trust domain. Raw email equality never merges
authority across domains.

## 6. Enrollment and credential lifecycle

Enrollment must combine all of the following:

1. a canonical corporate human identity, normally OIDC issuer plus subject with
   verified email as an alias rather than the sole primary key;
2. an exact enrollment transcript bound to domain, human, harness, credential,
   purpose, expiry, and requested capabilities;
3. proof of possession for the new harness credential;
4. independent human confirmation over an approved OOB boundary such as
   WebAuthn, Slack, Telegram, or email, where the enrolling harness cannot
   approve itself merely by possessing the same channel credentials;
5. atomic issuance and binding with replay protection and auditable evidence.

Credential lifecycle work must cover issuance, secure storage, rotation,
expiry, recovery, device loss, suspected compromise, emergency revocation,
offboarding, and sibling-harness behavior. Missing or ambiguous lifecycle state
fails closed. Recovery creates an explicit new binding; it must not silently
resurrect lost authority.

### Default onboarding topology and human experience

For ordinary self-hosted onboarding, "independent confirmation" means human
authentication independent of the enrolling harness; it does not mandate
another physical approval computer. The supported default may colocate Core,
PostgreSQL, and approval on the existing server under distinct OS identities,
while the owner approves the exact transaction with a WebAuthn authenticator on
an owner-controlled device. This profile must honestly report
`independent_boundary_proven=false`. A separately administered approval host is
an optional high-assurance profile, not a prerequisite for ordinary onboarding.

Normal onboarding must not require a named secret manager, an extra host or
person, per-command infrastructure approvals, or technical values from the
human that AgentNet can resolve itself. The sender/server resolves package
integrity, origins, callbacks, identifiers, and config metadata. Routine setup
uses one frozen, reviewable deployment approval. A fresh approval is required
only when scope materially changes or a new destructive, restart, privilege-
expanding, or high-risk action appears. One owner may perform the enrollee,
approver, and administrator human roles; cryptographic actors and authority
steps remain distinct.

## 7. Verified caller and authorization rules

Every protected message, task, room operation, file action, data request,
administration change, elevation, and effect must consume a verified security
context derived from authenticated transport, a valid purpose-bound signature,
or an equivalent proof. Caller-supplied JSON, prompt text, headers not covered by
proof, display names, email addresses, and role labels are untrusted input.

Authorization must:

- bind method, path/target, body digest, audience, purpose, nonce/freshness, and
  current credential/policy epochs as applicable;
- reject replay, signature substitution, stale policy, revoked credentials,
  ambiguous principals, unsupported algorithms, and unknown critical fields;
- evaluate one coherent policy revision;
- record an audit/decision intent before sensitive bytes, previews, key release,
  downloads, exports, or protected effects become observable;
- distinguish authorization to submit a proposal, accept custody, process work,
  access data, and perform a business effect;
- never infer internal business completion from a transport response or A2A
  task state.

## 8. Administration, subordinates, and task direction

Administration is a directed many-to-many graph, not necessarily a tree. An
agent may have several administrators and may administer several subordinates.
The authenticated relationship edge, revision, scope, expiry, and revocation
state—not titles or prose—determine available meta-actions.

Directional assignment behavior is mandatory:

- administrator → subordinate: when an active relationship includes
  `may_assign` and the task is within its approved scope, the subordinate may
  automatically accept the task into durable custody as `accepted_queued`;
- subordinate → administrator: requires explicit human confirmation unless a
  separate active directed relationship grants the sender `may_assign` over the
  recipient;
- peer → peer: same rule; it remains `pending_human` unless separately granted;
- cross-domain or out-of-scope assignment: never auto-accepted;
- conflicting administrators: resolve through explicit relationship/policy
  rules and durable states, never last-prompt-wins behavior.

Automatic task custody is not execution authority. The subordinate still needs
its owner's current permissions, an eligible harness, exact task intent/grant,
data/resource authorization, budget, and non-revoked state. Management never
transfers the administrator's data permissions to another human.

Temporary elevation is human-approved, narrowly scoped, expiring, revocable,
use-limited where appropriate, non-self-issued, and audited. High-impact and
break-glass classes may require multiple independent approvers under PD-004.

## 9. Communication and collaboration semantics

AgentNet must support:

- agent-to-agent and user-to-user direct conversations;
- laptop-agent to server-agent and server-agent to laptop-agent traffic;
- server-agent to server-agent relay and federation;
- one-to-many, many-to-one, and many-to-many communication;
- persistent rooms, temporary meetings, brainstorming spaces, threads, replies,
  mentions, structured tasks, handoffs, cancellation, and acknowledgements;
- first-class files and typed artifacts in all authorized topologies;
- verified human and originating-harness attribution on every contribution.

Requests that require an answer use a durable `ResponseObligation`, not an
inbox classifier or prose convention. The obligation binds the exact request,
one responsible harness, optional deadline, and optional self-contained JSON
response schema. Permissions and read visibility remain human-principal scoped,
so an active sibling harness may inspect the principal's obligations; progress
and terminal response ownership remain exact-harness scoped. Revoked harnesses
must fail every read and mutation. The shared supervisor—not any Pi-, Claude-,
Codex-, or Antigravity-specific adapter—automatically reconciles obligation
state and durably retains only encrypted content-free attention counters.

Rooms require explicit ownership/sequencing, membership epochs, invitation and
removal rules, from-join/history policy, guest constraints, moderation,
transfer, tombstone recovery, archival, retention, and deletion behavior.
Encryption does not replace room authority or membership policy.

## 10. Availability, custody, and delivery states

Offline is a normal availability condition—not revocation, identity failure, or
permanent system failure. Messages, tasks, and files must survive recipient
offline periods and reconcile after reconnection.

Transport is at least once with durable idempotency and replay prevention.
There is no promise of global ordering or magical exactly-once business
execution. The system must preserve distinct facts and terminal/nonterminal
states, including as applicable:

- proposed/submitted;
- accepted into durable or local custody;
- queued per recipient;
- delivered;
- acknowledged/processed;
- rejected;
- canceled;
- expired;
- effect reserved/committed/failed;
- `effect_unknown` pending reconciliation.

Each receipt states only what its authenticated issuer can prove. A relay receipt
cannot fabricate recipient processing; a recipient acknowledgement cannot prove
an external business effect; a workflow success cannot fabricate connector
evidence. Expiry, retention deletion, legal hold, effect deadline, and grant TTL
are separate concepts.

## 11. Files and artifacts

Files are not ordinary message bytes. The protected lifecycle is:

1. pre-upload authorization and byte/quota reservation;
2. immutable encrypted quarantine;
3. exact digest, manifest, owner, recipient/scope, version, policy, and lineage
   binding;
4. content safety, malware, dangerous-file, secret, and executable policy;
5. scanner attestation and freshness checks;
6. policy-gated release;
7. current authorization on every access/download;
8. single-use or otherwise bounded download capability;
9. explicit retention, deletion, legal-hold, restore, and audit behavior.

Do not disclose sensitive previews, plaintext, metadata-derived secrets, object
existence, deduplication facts, or key material before the relevant identity,
authorization, audit, safety, and privacy gates succeed.

## 12. Non-interrupting background operation

Agent-to-agent communication and delegated work execute in sessions/lifecycles
separate from the user's active harness conversations. Routine network activity
must never:

- inject a foreground conversation turn;
- steal focus or keyboard input;
- consume or corrupt the active conversation context;
- require routine approval prompts;
- expose message content in passive UI.

When a harness supports it, show only a minimal, passive, non-modal,
content-free activity/count indication. If it does not, remain silent rather
than violating the active-flow requirement. Only explicitly classified events
such as exact human approvals, confirmed security incidents, expiring high-risk
elevation, or terminal unrecoverable failure may enter an attention path, and
those paths remain redacted and policy-controlled.

## 13. Ordinary server agents and future hubless operation

The reliable initial profile assumes at least one always-on logical corporate
service topology for authority, durable mailboxes, offline delivery, artifact
custody, reconciliation, and gateways. In implementation and product language,
this is an ordinary enrolled **server agent** running AgentNet with explicit
capabilities. It is not a distinct Hub product, identity type, or universal
superuser.

Logical roles—identity/enrollment, policy, mailbox, room/task authority,
artifact custody, effects/data access, A2A/federation gateways, and audit—must
have separable interfaces and credentials even if a local profile composes them
in one process. Production may distribute and replicate these roles.

The future peer-assisted/hubless profile may use whichever explicitly eligible
agents are online for encrypted custody and delivery. It must preserve the same
identity, authorization epochs, revocation, globally unique IDs, idempotency,
receipts, audit, privacy, relay limits, partition semantics, and honest outcome
model. Availability never justifies weaker authority.

## 14. A2A, MCP, IPC, and internal transport

### Native A2A

A2A is the standards-compliant external interoperability boundary. Use the
pinned official SDK and preserve Agent Card discovery, authentication,
task/message/artifact mapping, callbacks/streaming where supported, versioning,
and conformance evidence. Public A2A actors remain external and low trust until
an explicit corporate/federated admission flow succeeds.

For A2A `securityRequirements`, alternatives in the array are OR alternatives;
every named scheme and scope inside the chosen requirement object is AND. Select
one locally permitted alternative, satisfy all of it, and fail closed on empty,
unsupported, malformed, downgraded, or ambiguous combinations.

A2A task state does not grant corporate identity, internal data authority, or
business-effect completion.

### MCP and local IPC

MCP is an optional local binding for harness tools. Pi may use direct private
Unix IPC. Neither is the corporate network transport or authority source.
Arguments must never establish caller identity. Local bindings derive the actor
from the enrolled current credential and measured parent/child context, use
owner-only capability roots and sockets, fence generation/TTL/epoch/revocation,
and reject replay or uncertain reconnect outcomes.

Every supported local binding must expose the same canonical send, inbox,
conversation, and response-obligation operations. A harness-specific adapter
may change only framing and presentation, never omit lifecycle operations or
change their authorization semantics.

### Internal network mechanisms

Internal mechanisms may use HTTPS, PostgreSQL outbox/mailboxes, selected
AGNTCY/SLIM components, Matrix client/room concepts, Cedar, SPIFFE/SPIRE,
maintained MLS, an artifact store, or Temporal-style workflow components when a
bake-off proves they fit. Mechanism reuse never delegates AgentNet policy or
semantic ownership to the component.

## 15. Federation and contractors

Federation is bilateral and host-controlled. An internal agent may receive a
scoped grant into another company, and an external contractor may receive a
temporary minimal grant into this network. In both directions:

- the host issues and controls host-local identity and authorization;
- every operation names the active trust domain;
- access is resource/action/room/data-class/time scoped;
- no trust or permission is transitive;
- home-domain proof may support ordinary assurance, while high-risk access can
  require fresh host-local proof;
- either host or home revocation can terminate effective access;
- audit ownership and incident contacts are explicit;
- network participation never exposes the guest's home network or another host.

Federation and C3 sealed-room features remain disabled until their owner and
external evidence gates pass.

## 16. Security and privacy model

Assume compromised or malicious harnesses, users, server agents, external A2A
peers, contractors, files, model outputs, relays, dependencies, and network
paths. Contain compromise through least privilege, narrow capabilities,
separate credentials, replay/freshness controls, quotas, rate limits,
backpressure, immutable evidence, revocation, kill switches, and safe recovery.

Data classes and key-holder visibility must be explicit. Transport encryption,
at-rest encryption, MLS/E2EE, and sandboxing solve different problems. If an LLM
worker receives C3 plaintext, its model provider receives that plaintext unless
an approved local model or approved provider arrangement says otherwise. Never
claim “sealed from the provider” merely because the relay/server agent cannot
decrypt.

Only the supervisor/model-egress broker may hold upstream model credentials.
Workers receive task/model/budget-bound access, not generic proxies, reusable
secrets, or unrestricted connectors. Missing clean-worker or broker evidence
means deterministic-only processing or explicit human-open viewing.

Audit must be tamper-evident and privacy-minimized. Logs and telemetry must not
leak protected content, secrets, raw identities beyond need, or cross-tenant
existence. Audit availability limits sensitive release: if the required audit
intent/checkpoint cannot be recorded, protected disclosure fails closed.

## 17. Build versus reuse

Do not reinvent mature subsystems by default. Before custom-building discovery,
transport, workload identity, policy evaluation, MLS, object storage, workflow,
or observability mechanisms:

1. evaluate maintained candidates at pinned versions;
2. review license, maintenance, supply chain, and operational burden;
3. map their data and authority semantics to AgentNet;
4. test offline recovery, duplicate delivery, revocation, expiry, tenant
   isolation, non-enumerating discovery, and failure behavior;
5. document semantic gaps and exit/migration strategy;
6. adopt only when the component passes AgentNet's security gates without
   semantic workarounds.

Reuse mechanism; retain policy ownership. Matrix may display a room but does not
become corporate authority. A2A may transport a task but does not prove an
effect. MLS may encrypt content but does not prove authorization, scanning, or
metadata privacy. A workflow engine may schedule retries but does not own
receipts or fabricate success.

## 18. Repository and implementation map

Public product name: **AgentNet**. Repository root: `agentnet/`.

The Python import namespace and public command are both `agentnet`. AgentNet is
the first-release product name; there is no retained predecessor namespace or
CLI alias.

Key paths:

| Path | Responsibility |
|---|---|
| `src/agentnet/core/` | ordinary-extension composition root and capability model |
| `identity/` and `approval/` | domains, actors, enrollment, OIDC/OOB, credentials, rotation, recovery, revocation |
| `authorization/` | decisions, human authority, grants, elevation, Cedar seam, bootstrap evidence |
| `organization/` | directed relationships, assignments, and task custody |
| `messaging/`, `delivery/`, `mailbox/` | immutable events, state machine, receipts, durable per-recipient custody |
| `rooms/` | membership, governance, meetings, ownership, MLS seam |
| `artifacts/` | reservation, quarantine, scanning, release, download, lifecycle |
| `effects/` | effect reservation, execution evidence, unknown-outcome reconciliation |
| `federation/` | bilateral trust and host-local guest lifecycle |
| `gateways/` | isolated public A2A client/server boundary |
| `bindings/` | canonical MCP tools and measured direct IPC composition |
| `supervisor/` | background queues, workers, daemon, model-egress broker, harness lifecycle |
| `relay/` | ordinary server-agent relay/custody behavior |
| `storage/` | SQLite local profile, PostgreSQL production path, migrations, recovery |
| `security/` | signatures, proof binding, replay, freshness, distribution/update controls |
| `operations/` | config, secure defaults, quotas, outage, telemetry, versioning/migration |
| `protocol/` and `schemas/v1/` | canonical models, negotiation, mappings, exported strict schemas |
| `interfaces/`, `components/`, `mesh/` | replaceable mechanisms, bake-off registry, disabled future mesh seams |
| `tests/` | hermetic, integration, property, race, recovery, privileged, and explicit external gates |
| `deploy/` | self-hosted deployment assets; provider-specific services remain optional |
| `evidence/` | retained reproducible evidence; never upgrade status without proof |

Trust-boundary models use strict schemas (`extra="forbid"`) and reject unknown
major versions, critical extensions, actor variants, algorithms, and state
transitions rather than downgrading them.

## 19. Profiles and operational claims

### Local conformance profile

- encrypted SQLite/WAL and local filesystem artifacts;
- synthetic or laboratory identity/OOB options visibly labeled as nonproduction;
- result vocabulary such as `accepted_local`, never `accepted_durable`;
- deterministic harness lifecycles unless exact signed clean-worker evidence is
  supplied;
- high-risk federation/C3/mesh/effect capabilities disabled by default.

### Always-on server-agent profile

- same AgentNet package and identity model;
- PostgreSQL and selected self-hosted artifact backend;
- explicit relay/custody/policy/data/federation/A2A capabilities;
- startup fails closed until enrollment, keys, capabilities, storage, policy,
  recovery, and required evidence are present;
- production durability claims require real HA/fencing/PITR/restore evidence,
  not merely a PostgreSQL connection string.

### Future peer-assisted/hubless profile

- disabled/evolutionary unless explicit partition, quorum, sequencing,
  revocation-freshness, encrypted replication, privacy, and recovery rules are
  implemented and tested;
- must preserve the canonical actor/event/receipt/authority model.

## 20. Current evidence and limitations

This repository contains a broad local implementation, but it is **not
production-certified**.

The current ledger contains all 85 requirement IDs. Consult the ledger and
release evidence for exact, run-specific counts rather than copying counts into
this narrative file.

Current locally implemented organizational mechanics include:

- exact bilateral proposal, subordinate-owner consent, renewal, expiry,
  subject exit, signed revocation, and separately authorized override paths;
- typed, arrival-order-independent `conflict_pending` admission and exact
  subordinate-owner adjudication, with revision fencing, overlapping-resource
  propagation, and custody-only release/reject outcomes;
- immutable schema-v1 governance baseline plus the current contiguous Core v4
  SQLite/PostgreSQL catalog and exact v3→v4 N/N-1 path; no older unilateral
  relationship is inferred or retrofitted as consented; and
- causal event and artifact derivation records bound to server-resolved parent
  digests, exact transformations, persistent taint, current policy, and the
  authenticated executor.

Those results do not prove production readiness. Important absent or non-green
evidence includes:

- official A2A TCK gate G04 and cross-SDK/public-peer interoperability remain
  external conformance gates; use the retained G04 report for exact outcomes;
- cross-SDK and public-peer A2A interoperability, certificates, real callbacks,
  streaming/push/artifact matrix;
- credentialed semantic clean-worker evidence for all four harnesses;
- independently administered workforce IdP, WebAuthn/OOB, hardware/OS key
  custody, and recovery/offboarding drills;
- privileged hostile same-UID/PID-reuse/process-boundary trials on target OSes;
- multi-node HA, failure-domain, failover, WAL archive/PITR/restore, load, and
  approved RPO/RTO evidence;
- maintained MLS and two-domain room/federation evidence;
- maintained hostile-file scanner corpus, replicated artifact restore, KMS/WORM
  roots, and independent audit witness;
- adaptive hostile-model and exfiltration trials;
- independent update-signing/root ceremony, published SBOM/provenance, and
  macOS/Windows lifecycle qualification;
- all accountable owner decisions PD-001 through PD-011.

No must-not-ship gate is currently `PASSED`. See
[the gate ledger](docs/GATE_EVIDENCE.md) for the exact status and evidence tier.

## 21. Accountable owner decisions

PD-001 through PD-011 are policy decisions, not coding checkboxes. Safe defaults
exist so implementation can proceed without unsafe enablement, but defaults are
not owner consent. Do not mark these complete without signed accountable-owner
evidence:

- `PD-001`: canonical human principal and alias/migration/appeal semantics;
- `PD-002`: approved independent enrollment/OOB boundaries and recovery rules;
- `PD-003`: harness/device/session attenuation and posture/appeal policy;
- `PD-004`: elevation risk classes, approvers, thresholds, TTL/use, break-glass;
- `PD-005`: compromise, retention, erasure/legal-hold, and adjudication policy;
- `PD-006`: room governance, history, guests, transfer, deletion/legal hold;
- `PD-007`: C1/C2/C3 privacy, MLS, model-provider, residency, retention, legal;
- `PD-008`: partner assurance by resource/data/action/risk class;
- `PD-009`: revocation signal SLO, continuity window, outage/audit ceiling;
- `PD-010`: production topology, RPO/RTO, capacity, residency, keys, witnesses;
- `PD-011`: attention channels, quiet hours, redaction, escalation policy.

## 22. Rules for changing the code

Before implementation:

1. read the relevant authoritative sections, not only nearby code;
2. list affected requirement IDs, trust boundaries, actor types, states, and
   owner/gate dependencies;
3. inspect the current dirty tree and preserve unrelated user changes;
4. state the fail-closed behavior and recovery behavior;
5. decide whether an existing maintained component should be evaluated first.

During implementation:

- preserve verified actor context end to end; do not reconstruct it from prose;
- use typed states and structured grants, never regex/prompt authority;
- make retries idempotent and crash/restart behavior explicit;
- keep protected decisions and durable writes atomic where the model requires;
- separate transport acknowledgement, custody, processing, and effect facts;
- retain compatibility or provide explicit migration and rollback;
- default new high-risk capability off until evidence and owner policy exist;
- keep every function, class, and module focused on one cohesive responsibility and
  one clear abstraction level; do not create or expand god functions or god modules;
- keep composition roots and orchestrators thin: they may sequence collaborators,
  but policy, storage, transport, parsing, and presentation logic stay in their
  owning modules behind explicit typed interfaces;
- when touched code mixes independent responsibilities, extract cohesive units at
  the responsibility boundary; do not use arbitrary file splitting or empty
  indirection as a substitute for modular design;
- avoid broad rewrites that make evidence provenance impossible to review;
- never weaken a negative test or secure default merely to get green output.

Required adversarial tests as applicable:

- spoofed human/harness/domain identity;
- wrong purpose/audience/path/body/key/signature;
- replay, duplicate, stale epoch, clock skew, expiry, and revocation;
- cross-tenant and cross-domain leakage;
- sibling-harness isolation;
- unauthorized upward/lateral assignment and privilege transfer;
- crash between reservation/write/release/effect transitions;
- response loss and unknown outcome reconciliation;
- concurrent approval, grant, relationship, quota, and breaker races;
- unsafe file, stale scanner, secret, archive, executable, and download reuse;
- SSRF/callback abuse, protocol downgrade, malformed critical extensions;
- queue pressure, loops, rate limits, and cleanup of child processes/resources.

## 23. Validation workflow

Use the locked environment from the repository root:

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
UV_CACHE_DIR=/tmp/uv-cache uv run pytest -q
UV_CACHE_DIR=/tmp/uv-cache uv run python scripts/verify_release.py
UV_CACHE_DIR=/tmp/uv-cache uv run agentnet verify
```

`agentnet` is the only public command; no predecessor CLI alias is supported.

PostgreSQL-only tests remain explicitly gated when no mutation-authorized
dedicated test database is supplied. The complete local database baseline
requires the dedicated test server and both:

```bash
AGENTNET_TEST_POSTGRES_URL='postgresql:///agentnet_test_governance_final?host=/tmp/agentnet-pgsocket&port=55432'
AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1
```

Never point mutation-authorized tests at production, shared, or valuable data.
External, owner, privileged-host, live-model, and partner tests remain separate
gates; missing infrastructure is not a passing result.

Run focused tests first, then the broadest relevant suite. Inspect long-running
processes before termination and leave no test server, worker, socket, temporary
credential, or background process behind.

## 24. Evidence and release-status discipline

Evidence tiers are defined in [the gate ledger](docs/GATE_EVIDENCE.md):

- `H`: hermetic unit/contract/property/vector/fault evidence;
- `L`: local multi-process or real local-service integration;
- `P`: privileged exact host/OS/kernel/process-boundary evidence;
- `E`: real harness/peer/IdP/partner/multi-node/model/production-like evidence;
- `O`: signed accountable policy/ceremony/root decision.

Rules:

- a lower tier cannot satisfy a gate requiring a higher tier;
- mock, interface, disabled feature, skip, xfail, deterministic no-inference
  run, reviewed upstream defect, or safe default is not missing-tier evidence;
- update `REQUIREMENTS_STATUS.md` and `docs/GATE_EVIDENCE.md` only from a
  reproducible record containing source revision, exact command, selected tests,
  environment, versions, seeds/corpus, artifacts, operator, and reviewer where
  required;
- never label the release, a gate, or a requirement “complete” beyond the scope
  actually proven;
- preserve non-green official results honestly; classification is not waiver.

## 25. Documentation obligations

When behavior, schema, authority, state, deployment, compatibility, or evidence
changes, update the corresponding authoritative implementation documents in the
same change:

- requirement mapping: `REQUIREMENTS_STATUS.md`;
- architecture or interface: `docs/ARCHITECTURE.md` and
  `docs/SCHEMAS_INTERFACES.md`;
- release evidence: `docs/GATE_EVIDENCE.md` and retained `evidence/` artifacts;
- owner-dependent behavior: `docs/OWNER_DECISIONS.md` without inventing consent;
- product operation: `README.md` and `docs/implementation-guide.md`;
- compatibility/versioning: schema catalog, migrations, and rollout evidence.

Do not edit the meaning of the 85 hard requirements as an incidental code
change. A genuine baseline change requires explicit owner direction and a fresh
coverage/adversarial verification pass.

## 26. Repository security and hygiene

- Never commit real credentials, private keys, access tokens, production
  secrets, enrollment proofs, customer data, private messages, production
  configuration, runtime databases, or private `.agentnet/` state.
- Use unmistakably synthetic domains such as `.example`, `.invalid`, or `.test`.
- Preserve ignores for `.venv/`, caches, build output, local databases, runtime
  state, and generated secrets.
- Treat inbound A2A content, federation metadata, files, callbacks, model output,
  and third-party component data as untrusted.
- Do not log protected plaintext, reusable credentials, raw tokens, private key
  material, or unnecessary personal data.
- Keep dependencies exactly pinned where the release process requires it.
- Do not add mandatory cloud/provider dependencies. Provider adapters must be
  optional and owner-selected.
- Do not claim Apache-2.0 licensing for third-party material without retaining
  its required notices and license obligations.

## 27. Definition of done

A change is done only when all applicable items are true:

- affected requirement IDs and invariants are identified;
- implementation preserves verified identity and fail-closed authority;
- state transitions, retries, expiry, revocation, and crash recovery are explicit;
- positive, negative, race, and recovery tests pass at the appropriate tier;
- compatibility, migration, rollback, and cleanup are covered;
- schemas and documentation agree with runtime behavior;
- evidence status is updated honestly and no higher-tier claim is invented;
- security-sensitive defaults remain off until their gates pass;
- no background process or sensitive temporary state is left behind;
- the release verifier and relevant suites pass in the claimed environment.

Passing unit tests alone is not sufficient.

## 28. Quick decision rules for future agents

When uncertain, apply these rules:

- If caller identity came from the payload, reject the design.
- If authority came from a harness, prompt, title, or relationship prose, reject
  the design.
- If an administrator assignment is downward, current, scoped, and explicitly
  `may_assign`, queue it; otherwise require human confirmation.
- If transport says success but the effect is unproven, use an intermediate or
  `effect_unknown` state and reconcile.
- If a recipient is offline, retain authorized custody and retry; do not call it
  identity failure.
- If a server agent needs a capability, grant it explicitly; do not create a
  privileged Hub identity.
- If A2A metadata claims corporate status, keep it tainted until explicit
  admission and local authorization succeed.
- If MCP arguments name a caller, ignore them for identity and use the verified
  local binding context.
- If encryption hides content from one component but exposes it to a model
  provider or member, describe that visibility honestly.
- If a mature component can be reused, bake it off; if it changes AgentNet
  semantics, reject or wrap it behind an owned interface.
- If required evidence or owner consent is absent, keep the gate blocked.
- If a proposed convenience weakens identity, revocation, audit, privacy,
  offline durability, or non-interruption, the convenience loses.
