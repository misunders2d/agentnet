# AgentNet

A runnable, self-hosted ordinary-agent communication extension. The same
Python package and API serve a laptop agent and an always-on server agent; the
server profile changes durable storage, enrollment, and capability gates, not
the product role. There is no separate Hub service or privileged Hub identity,
and this is not a patch to Claude, Codex, Pi, Antigravity, agent-deck, or any
other harness.

The current build provides working local semantics and strict fail-closed seams
for all architecture areas. It does **not** claim production certification.
SQLite acceptance is named `accepted_local`, synthetic identities are visibly
non-production, and federation/C3/peer-mesh/semantic-worker/protected-effect
features default off. The ordinary `always_on_server_agent` profile fails
closed until PostgreSQL, enrollment, keys, capabilities, and enabled-feature
evidence are present.

## Runnable today

Prerequisites: Python 3.13 and `uv`.

```bash
UV_CACHE_DIR=/tmp/uv-cache uv sync --extra test
uv run agentnet demo --data-dir /tmp/agentnet-demo
uv run agentnet a2a-demo
uv run agentnet harness-probe --data-dir /tmp/agentnet-harness-probes
uv run agentnet harness-probe --harness pi --data-dir /tmp/agentnet-harness-probes
uv run agentnet harness-demo --data-dir /tmp/agentnet-harness-demo
uv run agentnet init --config agentnet-config.json --data-dir .agentnet --domain local.example
uv run agentnet status --config agentnet-config.json
uv run agentnet serve --config agentnet-config.json --host 127.0.0.1 --port 8080
uv run agentnet verify
```

The production Compose file accepts no mutable runtime image tags. Operators
must supply verified repository and digest pairs through
`AGENTNET_SERVER_AGENT_IMAGE_REPOSITORY`/`AGENTNET_SERVER_AGENT_IMAGE_DIGEST`,
`AGENTNET_POSTGRES_IMAGE_REPOSITORY`/`AGENTNET_POSTGRES_IMAGE_DIGEST`, and
`AGENTNET_NGINX_IMAGE_REPOSITORY`/`AGENTNET_NGINX_IMAGE_DIGEST`. Build the AgentNet image with
an owner-verified Python base digest, for example
`--build-arg AGENTNET_PYTHON_BASE_DIGEST=<64 lowercase hex characters>`, publish it
to the operator-controlled registry, and use the resulting registry manifest
digest. The build also requires an owner-verified immutable uv image digest via
`--build-arg AGENTNET_UV_IMAGE_DIGEST=<64 lowercase hex characters>`; the Dockerfile
installs the locked Hatchling backend first and then installs the project from
the committed `uv.lock` with `uv sync --frozen --no-build-isolation`. A local
image ID is not a registry manifest digest and must not be substituted.

The demo creates two deterministic-only synthetic identities, stores explicitly
marked C0 synthetic bytes through a non-networked test lane, and reconciles the
offline recipient mailbox. It prints an explicit warning and only claims
`accepted_local`; the lane cannot carry C1/C2/C3 data, tasks, grants, rooms, or
effects and is not exposed over HTTP or MCP.

By default, `harness-probe` verifies all four exact Claude, Codex, Pi, and
Antigravity binary pins for the G01 gate. `--harness pi` (or another single
harness) is a diagnostic-only probe: it reports only that executable and never
claims four-harness readiness. `harness-demo` then starts each one in a distinct private
background lifecycle, exercises durable local custody, content-free passive
status, explicit human open, and bounded shutdown. It sends no semantic content
to any model and makes no inference or external-conformance claim.

Credentialed semantic evidence is deliberately separate and fail-closed:

```bash
AGENTNET_RUN_LIVE_HARNESS_INFERENCE=1 \
AGENTNET_LIVE_CLEAN_EVIDENCE_DIR=/owner/evidence \
AGENTNET_LIVE_CLEAN_EVIDENCE_KEY_ID=owner-key-1 \
AGENTNET_LIVE_CLEAN_EVIDENCE_PUBLIC_KEY=/owner/evidence/public.pem \
AGENTNET_LIVE_SANDBOX_EGRESS_WRAPPER=/owner/bin/evidenced-broker-wrapper \
uv run agentnet harness-live-gate --harness all --data-dir /tmp/agentnet-harness-live
```

Claude additionally requires `AGENTNET_LIVE_CLAUDE_BROKER_KEY` and
`AGENTNET_LIVE_CLAUDE_BROKER_URL`; Codex requires the corresponding
`AGENTNET_LIVE_CODEX_*` values. Pi and Antigravity require explicit owner-only
private auth directories and broker origins through
`AGENTNET_LIVE_PI_PRIVATE_AUTH_DIR`/`AGENTNET_LIVE_PI_BROKER_URL` and
`AGENTNET_LIVE_ANTIGRAVITY_PRIVATE_AUTH_DIR`/`AGENTNET_LIVE_ANTIGRAVITY_BROKER_URL`.
Credentials are injected only into their bound private worker and are never
accepted as command-line arguments or printed. Missing evidence, binary, or
credential fails the requested gate; it is never reported as a skip.

The npm/Pi package bundles a documentation-only `agentnet-operator` skill under
`skills/agentnet-operator/`. It routes install, local-conformance, server-agent,
identity, supervisor, Pi-binding, and troubleshooting requests to safe examples
and fail-closed references. Loading the skill does not initialize or activate
AgentNet and never grants identity or authority.

The real-network install-and-use contract is exactly the stable requirement set:
no reduced communication product, synthetic C0 substitute, or extra privileged
Hub product. AgentNet must ship or explicitly provision the maintained
mechanisms, adapters, manifests, and deterministic preflight checks required by
the selected supported profile. Operators supply approved hosts, secret values,
owner policy decisions, trust roots, and required human ceremonies; they do not
write missing approval services, scanners, storage adapters, receipt logic, or
vendor glue. A missing product component is a named blocker, not an operator
integration assignment or justification to weaken identity, authority,
durability, artifact, task, room, federation, or non-interruption semantics.

Local harness bindings are an explicit ordinary-extension feature. Package
installation alone does not activate them. `agentnet supervisor-run` expects a
separate owner-only `agentnet-supervisor.json`; do not pass the core
`agentnet.json`. Validate it before launch:

```bash
agentnet supervisor-run --config agentnet-supervisor.json --check
```

Set `local_bindings_required` to `true` in that supervisor config. The measured
child receives its capability only after launch; loading the Pi extension in an
ordinary foreground Pi process therefore remains unavailable by design.

Enable `local_bindings` in the core configuration, include the `local_binding`
capability limit, and provide an owner-only capability-root file plus a private
Unix-socket path. Production
environment loading uses `AGENTNET_LOCAL_IPC_CAPABILITY_ROOT_FILE` and
`AGENTNET_LOCAL_IPC_SOCKET_PATH` (with optional TTL/frame limits). The extension
derives the actor from the enrolled current credential on every call. It issues
a Pi capability only after the child is running and measured, so the opaque
value must be delivered over the supervisor's private post-launch channel; it
is never a command-line, MCP, A2A, or caller-supplied bearer.

## Implemented kernel

- exact verified actor union: human+harness, host guest+harness, workload, or
  `external_human_unverified` A2A;
- P-256 purpose-bound proofs, body/path/audience binding, freshness, and
  persistent replay rejection;
- exact enrollment transcript, PoP, atomic binding, lab-only approval verifier,
  and per-harness revocation without sibling revocation;
- human-only positive authorization, deny-only harness/device/session
  eligibility, one coherent revision, exact task grants, and audited decisions;
- directed `may_assign`: in-scope administrator-to-subordinate custody becomes
  `accepted_queued`; reverse/lateral/out-of-scope remains `pending_human`;
- encrypted local supervisor queues, authenticated live watch plus cursor
  fallback, separate worker lifecycle, explicit-open inbox, automatic durable
  obligation-counter reconciliation, content-free status, and no foreground
  message API;
- transactional per-recipient mailbox, at-least-once/idempotent submission,
  actor-owned receipts, expiry, cancellation, and `effect_unknown` controls;
- staged artifact reservation, immutable encrypted quarantine, exact manifest,
  scanner attestation, policy-gated release, and single-use download capability;
- rooms, from-join membership, temporary meetings, explicit frozen ownership
  transfer, and tombstone fallback;
- bilateral host-local guest schemas, non-transitive trust, sponsor/host revoke;
- official A2A Python SDK 1.1.0 routes and strict mapping/security helpers;
- one canonical local-tool composition service for MCP and Pi direct Unix IPC,
  with server-derived actors, current credential-epoch fencing, measured
  per-child capabilities, persistent replay rejection, and no caller
  bearer/identity arguments; the ordinary supervisor launches owner-only,
  parent-measured MCP endpoints and directly delivers sealed Pi capabilities;
  the exact local tool set includes direct send/inbox, conversation
  create/action/thread, and response-obligation inbox/list/get/progress/cancel/
  reconcile operations;
- provider-neutral interfaces for PostgreSQL, artifact storage, Cedar,
  SPIFFE/SPIRE, maintained MLS, workflow engines, and future mailbox relays;
- audit hash chain/checkpoints, quotas, privacy classes, redacted attention,
  non-enumerating directory, version negotiation, and generated JSON Schemas.

## Relationship governance workflow

The HTTP examples below show body shapes only. Every call still requires the
ordinary extension's authenticated request proof; a body field such as
`actor`, `verified`, or `policy_decision_id` is rejected and can never replace
the transport-derived actor.

1. The current owner of the proposed administrator endpoint, holding the exact
   `organization.relationship.propose` entitlement, submits:

   ```text
   POST /v1/relationships
   {
     "relationship": { ... exact Relationship ... },
     "proposal_expires_at": "... timezone-aware timestamp ..."
   }
   ```

   The `201` response key is `proposal`. Its lifecycle state is `proposed`, its
   activation basis is null, and it has no assignment or other authority.

2. Normal activation uses a fresh receipt from the independently configured
   verifier:

   ```text
   POST /v1/relationships/{relationship_id}/accept
   {
     "approval": { ... strict signed independent-approval receipt ... },
     "expected_transaction_digest": "... 64 lowercase hex ...",
     "expected_relationship_revision": 1,
     "expected_lifecycle_revision": 1
   }
   ```

   The verifier, not the caller, establishes the approver identity and owner
   kind. They must exactly equal the current human principal or host-local guest
   owner of the subordinate harness. The purpose must be
   `organization.relationship.accept`; the receipt is transaction-bound,
   fresh, one-use, and signed by a configured trusted key. Activation also
   rechecks both endpoint owners and credential epochs, domain/policy epochs,
   proposal expiry, relationship expiry, and predecessor revision.

   Before consuming the verified receipt, the same transaction inserts a
   pending `organization.relationship.activate` audit intent that binds the
   exact transaction/digest/revisions, transition, activation actor and basis,
   receipt evidence, activation time, and custody-only authority effect. The
   receipt is consumed, the edge is compare-and-swapped active, and the exact
   intent is completed at that same activation time before commit. Persisted
   authority checks reject a missing, pending, malformed, or inconsistent
   intent.

3. The separately implemented exception mechanism is two-step:

   ```text
   POST /v1/relationships/{relationship_id}/policy-exceptions
   { "exception": { ... }, "command": { ... signed authority command ... } }

   POST /v1/relationships/{relationship_id}/policy-exceptions/activate
   {
     "policy_exception_id": "...",
     "expected_transaction_digest": "...",
     "expected_relationship_revision": 1,
     "expected_lifecycle_revision": 1
   }
   ```

   Recording binds the exception to the exact transaction digest, relationship
   and lifecycle revisions, policy/domain and endpoint credential epochs, and
   an expiry no later than the proposal expiry. Activation consumes it once.
   The activation caller must be a current exact relationship participant or
   the exact recorded signer harness. Merely recording an exception creates no
   edge. Exception activation uses the same pending-to-completed exact local
   activation intent, with the recorded exception digest/reference in place of
   receipt evidence.

4. `GET /v1/relationships/{relationship_id}` is participant-scoped and
   non-enumerating. `POST /v1/relationships/{relationship_id}/revoke` accepts
   only an exact signed command at the current lifecycle revision. An endpoint
   can revoke/exit under `organization.relationship.revoke`; a nonparticipant
   requires the distinct `organization.relationship.admin_revoke` entitlement.
   All relationship responses and handled errors use `Cache-Control: no-store`.

Renewal never edits an active edge. Submit a new relationship ID at the next
coherent relationship revision and obtain fresh consent (or a fresh exact
exception). The proposal includes the predecessor snapshot. Activation
atomically supersedes that predecessor only if its lifecycle is unchanged;
revocation, expiry, or another activation makes the stale operation conflict.
At most one edge for an exact directed pair is active.

An active `may_assign` relationship only permits in-scope administrator-to-
subordinate `accepted_queued` custody. Protected reads, semantic processing,
tools, and effects still require the subordinate owner's current authority,
exact task intent/grant, and every normal policy check.

### Task-custody deadline and payload behavior

Every accepted assignment has an exact timezone-aware deadline. A supplied
deadline is preserved. If omitted, AgentNet derives a whole-second deadline
from the normalized immutable event creation time, capped by the exact
assignment scope's complete `max_duration` and one second before relationship
expiry. Before computing the custody digest and committing the event, it writes
that deadline into the canonical request and event `effect_deadline` and caps
`delivery_expires_at` at the same value. Exact retries reuse the stored bytes;
retry wall time never extends the accepted window.

Task custody is deliberately metadata-only at all generic read surfaces.
Mailbox reconciliation, conversation-thread reads, supervisor explicit-open,
and background delivery return `payload: null`, `payload_available: false`, and
an immutable custody reference for a task assignment or task-linked control.
The check uses the typed event/task fields as well as
`payload_access=task_grant_required`, so records without that marker are
also withheld; a removed or substituted marker fails immutable-envelope
validation rather than revealing bytes. Ordinary non-task messages continue to
return their authorized payloads.

This build has **no protected TaskGrant payload-release route**. A task grant
object therefore cannot make these stored payload bytes visible or executable
through a generic read, supervisor, or worker path. This closes the custody
non-grant boundary at the cost of leaving task execution unavailable. Adding a
release path is separate security work requiring an exact current TaskGrant,
source/sink/data/tool/effect authorization, audit-before-release ordering, and
new adversarial review; an `include_payload` flag is not an acceptable design.

Both activation-intent variants are local database provenance. They do not
prove independent publication or witnessing, and a coherently compromised
database can alter local edge and intent rows together. Production operation
still requires the independent audit exporter/checkpoint/witness and
reconciliation evidence tracked by the release gates.

### Clean-start schema v1 and recovery

AgentNet has no supported predecessor database format. SQLite creates the full
storage schema at version 1; PostgreSQL applies the single contiguous,
checksum-bound `agentnet_first_release_schema` migration. Service startup
verifies exact metadata, migration records/checksums, objects, indexes,
triggers, and constraints and fails closed on any missing, altered, older, or
newer state.

Do not point AgentNet at a pre-release or differently named database, edit its
version metadata, or infer relationship authority from unilateral records. A
transition from exploratory data requires a reviewed export of non-authority
content into a freshly initialized v1 store and fresh exact bilateral consent.
Restore only an exact signed and verified AgentNet v1 backup; rollback cannot
synthesize or reactivate authority.

Backup manifest, trust, seal, and archive publication is fail-closed and bound
to the exact source database, schema, domain, key epoch, and bytes. If rollback
cleanup cannot prove that it still owns the installed pathname, AgentNet
atomically moves the product-visible name to a random owner-only
`.agentnet-quarantine-*` file. It intentionally retains that file for an
authenticated operator to inspect and remove out of band; it does not unlink a
path that another same-UID process may have replaced. A post-commit close or
durability failure is reported as publication-outcome unknown, not successful
rollback.

### Conflict adjudication and derived provenance

Assignment requests may include a strict typed resource intent. Incompatible
active intents enter deterministic `conflict_pending` records atomically. The
subordinate's exact current human/guest positive-authority owner can list only
their conflicts and must decide an exact revision-bound partition of every
current member. Released intents must be mutually compatible; rejects propagate
across overlapping conflicts, and an event queues only after all its pending
memberships clear. Concurrent/stale decisions and authority-epoch drift fail
closed. This process releases custody only and grants no data, semantic, tool,
or business-effect authority.

Workload event replies bind exactly one local causal parent; AgentNet resolves
the parent's immutable provenance digest in the same transaction and rejects a
missing parent, replay mismatch, classification reduction, sink widening, or
policy drift. Artifact promotion can include exact parent provenance references
and canonical transformation steps; all parents are server-resolved and every
executor must equal the authenticated harness. Derived records remain tainted,
unreviewed, and scan-pending. Public provenance origin registration accepts
only human input from the exact authenticated human harness; composed services,
not callers, create server origins.

The relationship-governance workflow is implementation evidence only.
**ORG-006 remains
owner-blocked:** the accountable owner has not approved eligible proposers or
proposal-entitlement holders, which roles may receive policy-exception or
admin-override entitlements, when such mechanisms may be used, or
mandatory-relationship, notice, review, retention, and appeal rules. Nothing
here claims those policies or their production operation have passed a gate.

## Security boundary

Protected production features are not enabled by a mock, safe default, skipped
test, or interface stub. The following remain real external/owner gates:

- PD-001 through PD-011 accountable owner decisions and the separate ORG-006
  relationship-governance policy;
- version-pinned Claude/Codex/Pi/Antigravity isolation and recovery;
- independent OIDC/WebAuthn/OOB approval and target-platform key custody;
- A2A TCK, cross-SDK, certificate, callback, and public-peer evidence;
- HA PostgreSQL/object-store failover, fencing, PITR, and restore;
- maintained MLS lifecycle, bilateral partner lab, real scanner/WORM/KMS roots;
- adaptive hostile-model trials and signed installer/update lifecycle.

See [REQUIREMENTS_STATUS.md](REQUIREMENTS_STATUS.md) and
[docs/GATE_EVIDENCE.md](docs/GATE_EVIDENCE.md) for the evidence ledger.

## Repository map

```text
src/agentnet/
  adapters/       credential-free harness capability paths
  approval/       independent approval verifier contracts
  artifacts/      quarantine, manifest, scan, release, download
  authorization/  policy decisions, grants, elevation, Cedar seam
  bindings/       canonical MCP/local tools and measured Pi direct IPC
  core/           one ordinary-extension composition root
  delivery/       actor-owned state machine
  federation/     bilateral host-local guests
  gateways/       isolated public A2A boundary
  identity/       domains, actors, enrollment, credentials, revocation
  mailbox/        per-recipient custody and future custodian seam
  mesh/           disabled future opportunistic/distributed seams
  messaging/      immutable event construction
  organization/   directed relationships and assignments
  protocol/       canonical models, negotiation, A2A mapping, schema catalog
  rooms/          governance, meetings, maintained-MLS seam
  security/       signatures, proof, replay, encryption, update controls
  storage/        SQLite local profile and PostgreSQL readiness gate
  supervisor/     local queue, worker launcher, model-egress broker
schemas/v1/       generated versioned JSON Schemas
tests/            hermetic, integration, adversarial, and explicit external gates
docs/             architecture, bake-offs, threats, milestones, gates
```

## Source-of-truth boundary

The repository-local `docs/specification.md`, `docs/requirements.md`, and
`docs/final-verification.md` are the authoritative implementation handoff.
Their current hashes are recorded in `docs/ARCHITECTURE.md` and enforced by the
release verifier; sealed-audit hashes remain separately labeled as historical
evidence and must not be confused with the current annotated bytes.
