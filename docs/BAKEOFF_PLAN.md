# Component Bake-Off Plan

Status: implementation decision record plus remaining external bake-off plan  
Snapshot date: 2026-07-13

## Objective

Select maintained mechanisms that fit the canonical corporate interfaces without weakening identity, authority, intent, isolation, durability, receipt, audit, offline, room, artifact, or effect semantics. The bake-off compares each candidate with the simplest compliant baseline; it does not compare marketing feature counts.

Phases order work but do not waive requirements. A component can be accepted for a local prototype at a lower evidence tier while production use remains blocked at a higher tier.

## Non-negotiable decision gates

A candidate is acceptable only if it:

1. is pinned by immutable release/commit and artifact digest;
2. has an acceptable license, maintenance history, provenance, dependency graph/SBOM, vulnerability response, and reproducible acquisition record;
3. is self-hostable with no mandatory managed-cloud control plane or data egress;
4. maps through a canonical interface without changing corporate actor, authority, intent, task direction, state, receipt, audit, artifact, or effect meaning;
5. supports tenant/domain isolation and non-enumerating behavior where applicable;
6. fails closed on missing, stale, revoked, ambiguous, partitioned, or inconsistent security state;
7. has bounded duplicate, retry, offline, replay, revocation, cache, partition, crash, upgrade, rollback, backup, and restore behavior;
8. does not inject message content or work into an active user conversation;
9. preserves replacement without migrating corporate identity or rewriting immutable/signed history; and
10. passes all relevant must-not-ship gates, not merely its upstream conformance suite.

Immediate rejection conditions are: positive authority expansion, payload-derived identity, hidden bearer/token forwarding, silent A2A downgrade, false durable/recipient/effect claims, blind retry after possible effect, unaudited protected bytes/key/capability release, uncontained hostile semantic content, transitive federation trust, hidden MLS members, silent managed-cloud dependency, or a semantic workaround that makes the corporate model untrue.

If a candidate is unavailable, unpinned, or blocked by missing infrastructure, classify it **deferred**. Do not classify absence as a failed bake-off and do not approve a greenfield replacement.

## Evidence tiers

| Tier | Evidence | What it can support | What it cannot support |
|---|---|---|---|
| `E0 — inventory` | Exact version/digest, license, source, dependency/SBOM snapshot, local availability, API surface | Candidate admission to testing | Functional acceptance or security claims |
| `E1 — smoke` | Reproducible import/start/stop, minimal round trip, malformed-input failure, no unexpected egress | Runnable local smoke-profile selection | Interoperability, durability, isolation, or production claims |
| `E2 — contract` | Canonical-interface suite, schema mapping, authority/identity negatives, deterministic state/property tests | Component semantic fit | Real crash/partition/scale or hostile-environment claims |
| `E3 — fault` | Kill/restart, duplicate, offline, partition, revocation, cache loss, resource pressure, upgrade/rollback, backup/restore | Single-node/pilot operational decision with honest state names | HA/RPO=0, cross-SDK, platform, or adversarial certification |
| `E4 — conformance/adversarial` | Upstream TCK, cross-implementation matrix, SSRF/fuzz/escape/abuse tests, exact crypto vectors, independent red-team corpus | External interoperability or security-gate evidence | Production topology and owner-policy approval |
| `E5 — production topology` | Declared failure/admin/key domains, HA/fencing, synchronous durability, PITR, witness/backlog, kill-switch SLO, capacity and restore drills | Production component adoption for the tested profile | Untested OS, region, harness, partner, E2EE class, or owner decision |

Local unit tests are never enough to promote a requirement that explicitly needs E4/E5 evidence.

## Evidence package format

Store each run under:

```text
tests/evidence/components/<component>/<immutable-pin>/<run-id>/
  manifest.json
  acquisition.json
  license.txt
  sbom.json
  environment.json
  commands.txt
  stdout.log
  stderr.log
  results.json
  canonical-mapping.md
  failures.md
  rollback-and-replacement.md
```

`manifest.json` records source URL, release/commit, artifact digests, configuration hash, test code commit, OS/kernel/architecture, runtime, dependency lock hash, start/end time, and operator. `results.json` records every assertion, repetition count, seed, latency/resource statistics, and pass/fail/blocked status. Logs must redact secrets and message content.

An evidence package is immutable after review. A new component version, configuration, harness, model, prompt, tool, parser, policy, or sandbox profile gets a new run directory.

## Current reproducible inventory probes

These commands were run successfully on the snapshot date. Rerun them and capture output in an evidence package before promoting any ledger status.

```sh
.venv/bin/python --version
.venv/bin/python -m pip show a2a-sdk
.venv/bin/python -m pip show mcp psycopg uvicorn pyjwt pytest hypothesis
.venv/bin/python -m pip check
.venv/bin/python -c "from a2a.client import A2ACardResolver, ClientFactory; from a2a.server.agent_execution import AgentExecutor, RequestContext; from a2a.server.request_handlers import DefaultRequestHandler; from a2a.server.routes import create_jsonrpc_routes, create_rest_routes; from a2a.server.tasks import InMemoryTaskStore; print('a2a-sdk-1.1.0-client-server-rest-jsonrpc-ok')"
.venv/bin/python -m pytest -q tests/a2a
node --version
npm --version
npm cache ls @modelcontextprotocol/sdk
rclone version
sqlite3 --version
bwrap --version
systemd-run --version
```

Observed baseline: Python `3.13.13`; `a2a-sdk 1.1.0`; `mcp 1.28.1`; `psycopg`/`psycopg-binary 3.3.4`; `uvicorn 0.51.0`; transitive `PyJWT 2.13.0`; `pytest 9.1.1`; `hypothesis 6.156.6`; clean `pip check`; A2A client/server REST+JSON-RPC import smoke and `56` checked-in A2A tests passed; Node `26.4.0`; npm `11.18.0`; TypeScript MCP SDK `1.29.0` present in npm cache only; rclone `1.74.4`; host SQLite `3.53.3`; bubblewrap `0.11.2`; systemd `261`.

The Python-linked SQLite version is separate from the CLI and must be captured with:

```sh
.venv/bin/python -c "import sqlite3; print(sqlite3.sqlite_version)"
```

Current result is `3.50.4`.

## Standard run sequence

Every candidate follows this order:

1. **Acquire:** download or vendor only the pinned release/commit; record hashes and signatures. No `latest`, floating branch, or unrecorded container tag.
2. **Inventory:** capture E0 evidence and all optional features actually installed.
3. **Contain:** run with no secrets, a fresh data directory, explicit bind addresses, denied default egress, quotas, and a unique service identity.
4. **Adapt:** translate at the canonical seam; never pass component-native identities, ACLs, task states, receipts, or object URIs into domain authority.
5. **Contract:** run E2 schema, state, identity, authority, and failure-mapping tests against the candidate and comparator.
6. **Fault:** run the component-specific E3 matrix at every state edge, including response-loss-after-commit.
7. **Adversarial/conformance:** run applicable E4 suites and independent negative tests.
8. **Operate:** benchmark resource cost, backlog, upgrades, rollback, backup, restore, revocation SLO, and incident disable path.
9. **Decide:** record accept/reject/defer, exact allowed profile, known gaps, owner, expiry/review date, and replacement plan.
10. **Map:** update `REQUIREMENTS_STATUS.md` only after immutable evidence exists.

## Candidate bake-offs

### 1. Official A2A SDK and TCK

**Pin:** A2A release `v1.0.1`, wire `1.0`; Python SDK `1.1.0`; official TCK tag `1.0.0.alpha2` with lock SHA-256 `7650dafd015617312c49f87bfafaa17e96f6af5d7d73540ce0ef4a2f46dfe404`.  
**Current level:** E2 local contract suite (`56` tests) passes. The retained official HTTP+JSON TCK run selected 235 tests and recorded 46 passed, 12 failed, and 177 reviewed skips; it is non-green. Cross-SDK, public-peer, and production evidence remain absent.  
**Comparator:** no custom A2A implementation is approved; cross-check Python behavior with pinned official Go `v2.3.1` and Java `v1.1.0.Final` clients/servers when acquired.

Required contract and adversarial cases:

- HTTP+JSON and JSON-RPC advertisement, exact official binding literals, preference, media type, and no-binding-intersection failure;
- absent/empty/legacy version rejection on the protected endpoint with no v0.3 heuristic fallback;
- security requirement alternatives are OR; every scheme and scope inside the selected alternative is AND;
- per-logical-agent opaque route/Card, tenant mismatch, standing-grant issue/revoke, and non-enumeration;
- Message versus server-owned Task result without synthesizing a Task for a direct Message;
- every StreamResponse variant, Task push duplicate/gap recovery, and direct-Message stream loss as `remote_response_unknown` rather than GetTask-recoverable;
- unspecified role/state quarantine, opaque foreign IDs, input-required/auth-required with zero automatic credential or approval disclosure;
- artifact direction, digest/size/version checks, callback audience/origin/replay, DNS/IP/redirect revalidation, and SSRF limits;
- credential-origin confinement and zero corporate/human token forwarding;
- TCK MUST/SHOULD report plus cross-SDK task, message, artifact, streaming, push, cancellation, discovery, and auth tests.

Current commands and durable record:

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/a2a
evidence/gates/G04/2026-07-13-alpha2-http-json/manifest.json
```

The official TCK was acquired and run; its 12 failures and 177 skips remain non-passing. Upstream TCK success alone would not promote Gate 4.

**Accept at E4 only if:** zero TCK MUST failures, reviewed SHOULD failures, all corporate negative cases pass, and no SDK state or credential becomes corporate authority.  
**Current decision:** accept the SDK mechanism for the baseline; G04 remains failed and production interoperability remains externally blocked.

### 2. AGNTCY OASF

**Pin:** unresolved; no local artifact.  
**Seam:** `CapabilityCatalogPort`.  
**Comparator:** minimal domain capability schema containing only owned, versioned, expiring routing descriptions.

Tests must prove malformed, oversized, recursive, stale, forged, cross-tenant, and authority-looking descriptions remain tainted metadata; imported skills/tools never grant membership, trust, data access, task grants, network sinks, or signer input.

Acquisition is itself gated: select a maintained immutable release, record license/SBOM/provenance, and copy the official version/smoke commands into `commands.txt`. Do not invent a CLI command before the release is pinned.

**Accept at E2/E3 only if:** the schema round-trips without semantic authority fields and removal/revocation invalidates routing hints within the declared freshness bound.  
**Current decision:** deferred for absence; no custom replacement approved beyond the comparator schema.

### 3. AGNTCY Directory

**Pin:** unresolved; no local artifact.  
**Seam:** `DirectoryPort`.  
**Comparator:** authorization-filtered PostgreSQL directory query at one current revision.

Tests cover unauthorized list/get indistinguishability, tenant/domain isolation, stale capability and key/endpoint epochs, revoked records, cache partition/loss, endpoint rotation, duplicate IDs, malicious self-registration, pagination/enumeration, backup/restore, and self-hosted outage behavior.

**Accept at E3 only if:** the corporate PEP remains the only disclosure authority and current authoritative epochs override every component cache.  
**Current decision:** deferred for absence.

### 4. AGNTCY SLIM versus the thin transport baseline

**Pin:** unresolved; no local artifact.  
**Seam:** `TransportPort`.  
**Comparator:** HTTPS plus PostgreSQL transactional outbox and cursor reconciliation.

Tests cover offline recipient, duplicates, reordering, reconnect storms, 1-hour/7-day/30-day disconnect, response loss after commit, sender/recipient crash, queue pressure, replay, revocation during retry, partition, route/key rotation, tenant spoofing, upgrade/rollback, and full restore. Transport ACKs must never map to `accepted_durable`, `recipient_committed`, `presented`, or `completed` unless the owning corporate actor supplied the required evidence.

**Accept at E3/E5 only if:** SLIM materially improves measured transport/operations while remaining a replaceable mover of exact envelopes.  
**Current decision:** deferred for absence; use only the thin comparator until evidence exists.

### 5. Matrix components

**Pin:** unresolved; no local SDK/server.  
**Seam:** `RoomSyncPort`.  
**Comparator:** canonical one-owner room control sequence plus transactional event/outbox projection.

Tests cover Matrix power-level and membership mismatch, event-DAG conflict, owner transfer freeze/cutoff, removed-member future/undelivered access, history visibility, guest scope, archive/delete/legal hold, offline sync, duplicated events, federation disabled, cross-room/tenant enumeration, and component outage/restore. Matrix delivery/read state cannot become corporate custody, presentation, human read, or effect evidence.

**Reject as core if:** adoption requires Matrix federation, multi-writer authority, replicated plaintext history, or Matrix ACLs to become corporate policy.  
**Accept a component only if:** it reduces client/sync work behind the seam and preserves the canonical single-owner model.  
**Current decision:** Matrix core rejected by architecture; isolated component adoption deferred for absence. No production greenfield room-sync implementation is approved before this bake-off.

### 6. Maintained MLS implementation

**Pin:** unresolved; no local MLS library.  
**Seam:** `SealedRoomCrypto`.  
**Comparator:** none. Custom MLS is prohibited; C3 remains disabled.

Required E4 cases include external-commit/proposal validation, KeyPackage lifecycle, credential/application identity binding, multi-device add/remove, concurrent proposals, removed-member future access, history policy, epoch separation, compromise recovery, exporter/file keys, backup/recovery, group size/performance, interop vectors, malicious ciphertext, rollback, metadata, visible scanner/tool/recovery members, and PD-007 provider-disclosure policy.

**Accept only at E4/E5 if:** an audited implementation passes the exact application binding and lifecycle profile.  
**Current decision:** deferred and C3 blocked. OpenSSL/libsodium are not substitutes.

### 7. SPIFFE/SPIRE

**Pin:** unresolved; no local SPIRE binaries.  
**Seam:** `WorkloadIdentityProvider`.  
**Comparator:** narrowly scoped local service mTLS credentials with explicit rotation/revocation.

Tests cover workload selector spoofing, SVID rotation/expiry, stale bundle, server/agent outage, node compromise, wrong trust domain/audience, workload migration, revocation latency, least-privilege issuance, and zero mapping from workload identity to human/harness authority.

**Accept at E3/E5 only if:** a real managed server fleet justifies its operational cost. Never require SPIRE on laptops.  
**Current decision:** trigger-deferred.

### 8. Cedar policy engine

**Pin:** unresolved; no local Cedar runtime.  
**Seam:** `PolicyDecisionPoint`.  
**Comparator:** an owned deny-all/test oracle plus canonical decision fixtures; it is not a production allow engine.

Tests cover schema diagnostics, missing entities, forbidden overrides, one human positive source, harness/session/device attenuation, exact task grants, source/sink intent, one-use elevations, coherent policy/entity/grant revision, relationship direction, scope/expiry/revoke, subject lifecycle, non-enumerating list decisions, graph scale, cache loss, policy update/rollback, and outage. Administrator-to-subordinate in-scope assignment must yield only `accepted_queued`; reverse/lateral requests remain `pending_human` unless a separate current directed edge allows them.

**Accept at E3/E5 if:** correctness and graph/list latency meet the declared load while every uncertainty denies. If Cedar fails, compare one OpenFGA or SpiceDB replacement; never run dual positive authorities.  
**Current decision:** target accepted, runtime deferred. Production protected allow paths remain blocked.

### 9. PostgreSQL versus SQLite pilot

**Pin:** PostgreSQL `18.4` for the retained local-service evidence; Psycopg `3.3.4`; Python-linked SQLite `3.50.4`.  
**Seams:** `TransactionalStore`, `MailboxCustodian`, `ReplayStore`.  
**Comparator:** SQLite WAL local profile with the weaker `accepted_local` label.

SQLite E2/E3 tests cover WAL/fsync boundaries, kill -9 and hard reset simulation, idempotency key/digest conflict, event+recipient+audit+outbox atomicity, cursor reconciliation, duplicate delivery, corruption detection, backup/restore, disk full, clock/replay state, and resource pressure.

PostgreSQL E3/E5 tests add synchronous primary/standby commit, connection loss after commit, fencing, split brain, failover, WAL archive/PITR, replica loss, restore of receipts/audit/outbox, schema expand/migrate/verify/contract, N/N-1 mixed versions, capacity, and RPO/RTO measurement.

Current local commands:

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/delivery
AGENTNET_TEST_POSTGRES_URL='postgresql:///agentnet_test_final?host=/tmp/agentnet-pgsocket-20260713-final&port=55432' AGENTNET_TEST_POSTGRES_ALLOW_MUTATION=1 PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/production/test_postgres_runtime.py
```

The PostgreSQL corpus passes 44/44 with seven real local-service cases and 37 hermetic contracts. This proves neither HA nor PITR; a self-hosted independent-failure-domain runner remains external.

**Accept SQLite only for:** local/synthetic pilot with honest weaker state names.  
**Accept PostgreSQL for `accepted_durable` only at E5:** declared RPO=0 boundary, fencing, PITR, and restore all pass.  
**Current decision:** SQLite and the PostgreSQL 18.4 local mechanism are accepted for their tested profiles; `accepted_durable`, HA, PITR, and production topology remain blocked on G09 external/owner evidence.

### 10. Artifact store and rclone

**Pin:** `rclone 1.74.4`; self-hosted object-store server unresolved.  
**Seam:** `ArtifactStore`.  
**Comparator:** private filesystem-backed content-addressed store for synthetic pilot data.

Run the complete reservation state machine at every crash edge: reservation before object, object before promotion, promotion before response, reservation expiry, concurrent retry, wrong/missing version, orphan reconciliation, restore, required/optional attachment, scanner stale/outage, retention, legal hold, and key deletion. Test cross-domain/tenant/class equality and timing/quota probes; first and duplicate uploads must be indistinguishable. Object backend evidence never promotes a manifest or authorizes a download.

Current local commands:

```sh
rclone version
rclone help backends
.venv/bin/pytest -q -p no:cacheprovider tests/artifacts
```

The filesystem artifact corpus is implemented and included in the 957-test run. S3-compatible tests remain blocked by the absence of a pinned local server and container runner.

**Accept filesystem/rclone-local at E3 only for:** the explicitly weaker synthetic pilot.  
**Accept production backend at E5 only if:** immutable version, replication, encryption, backup, restore, and non-disclosure gates pass.  
**Current decision:** local comparator accepted for local evidence; production backend remains blocked on external G09/G13 evidence.

### 11. MCP local binding

**Pin:** protocol `2025-11-25`; selected Python SDK `mcp 1.28.1`; cached TypeScript SDK `1.29.0` remains comparison-only.  
**Seam:** `LocalBinding`.  
**Comparator:** authenticated direct supervisor IPC, mandatory for Pi.

Tests perform the same explicit tool/resource round trip through Claude, Codex, Antigravity MCP and Pi direct IPC. Mutating email, actor, harness, role, domain, task grant, or token arguments must not change authenticated caller context. Corporate and A2A bearer tokens must never traverse the MCP server. Disconnect, duplicate, cancellation, oversized arguments, protocol downgrade, server restart, and foreground-interference tests apply.

Current local command:

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/bindings tests/security/test_ipc_capability.py
```

The parent-bound direct/MCP/Pi composition and IPC negatives are implemented. Exact semantic interoperability on each installed harness remains external and a cache hit alone is still E0 only.

**Accept at E2/E3 only as:** optional local binding per harness.  
**Reject permanently as:** corporate network, identity authority, durable mailbox, room, consent, federation, or effect engine.  
**Current decision:** accept the pinned Python mechanism for the optional local adapter; G05/G07 semantic and privileged-host evidence remains externally blocked.

### 12. Clean-worker sandbox and model-egress broker

**Pin:** bubblewrap `0.11.2`, systemd `261`, exact harness versions from the release manifest.  
**Seam:** `CleanWorkerLauncher`.  
**Comparator:** deterministic-only worker with no semantic lane.

For each exact Claude, Codex, Pi, and Antigravity version, test an empty non-user workspace, sanitized HOME/environment/CWD/session store, no inherited AGENTS/CLAUDE/hooks/plugins/skills/MCP/shell profiles, fixed-schema supervisor binding only, no shell, no arbitrary filesystem, no desktop/keychain/browser/vendor credential, no socket/process/proc/ptrace access, no DNS or arbitrary network, model endpoint through a non-generic broker only, worker-bound short capability, quotas, restart, compaction, queued load, and zero foreground activity. Include seeded secrets and independent escape payloads.

Current local commands:

```sh
bwrap --version
systemd-run --version
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/adapters
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src .venv/bin/pytest -q -p no:cacheprovider tests/supervisor tests/components/test_bakeoff_evidence.py
```

**Accept a harness semantic lane only at E4:** zero escape and credential findings at the exact version/profile and a proven broker auth path.  
**On failure:** deterministic protocol handling and explicit human-opened viewing only.  
**Current decision:** mechanisms available, semantic lane deferred.

### 13. File scanner/transformer

**Pin:** unresolved; no local scanner.  
**Seam:** `ArtifactScanner`.  
**Comparator:** quarantine with no parse, model exposure, transform, preview, release, or download.

Required cases cover malware, secrets/DLP, extension/type mismatch, polyglots, archives, recursive nesting, decompression bombs, parser crashes, macros, executable links, time/memory/CPU/egress limits, scanner substitution/staleness, digest binding, derivation provenance, concurrent rescan, outage, and restore.

**Accept at E4 only if:** the scanner is isolated, signs exact digest/profile evidence, and has no release authority.  
**Current decision:** deferred; quarantine-only behavior is mandatory.

### 14. Temporal-style workflow engine

**Pin:** unresolved; no Temporal SDK/server/CLI.  
**Seam:** `WorkflowEngine`.  
**Comparator:** explicit transactional effect reservation, fenced lease, `effect_unknown`, reconciliation, adjudication, and compensation state machine.

Trigger the bake-off only when measured effect complexity exceeds the comparator. Tests cover worker crash at every activity edge, duplicate scheduling, timeout after possible commit, cancellation races, stale authorization, revocation, idempotent versus non-idempotent effects, query/reconcile, compensation authorization, history replay/versioning, upgrade/rollback, server outage, backup/restore, and the invariant that workflow completion is not delivery or external effect evidence.

**Accept at E3/E5 only if:** it materially reduces recovery complexity without becoming a second source of truth or fabricating actor-owned facts.  
**Current decision:** deferred by trigger and absence; comparator accepted.

## Cross-cutting threat-model suites

Every adopted component also runs the applicable cross-cutting suites:

| Suite | Mandatory assertions | Gates |
|---|---|---|
| Identity confusion | Claimed email/harness/role/domain/tenant/ID fields never alter authenticated actor; sibling and external actors cannot collide | 4, 6-8, 11, 18-19 |
| Authority attenuation | Human is the only positive authority; harness, session, task, relationship, component, and transport can only preserve/reduce it | 8, 11, 17 |
| Directional assignment | Exact current `may_assign` edge/scope permits downward custody only; reverse/lateral is `pending_human`; execution separately reauthorizes | 8, 10-11 |
| Receipt/state ownership | Each fact is asserted only by its owner; missing evidence is unknown; no false exactly-once/durable/completion/cancellation | 9-10, 13, 16 |
| Hostile content | Signed content remains tainted; zero unintended protected read, disclosure, effect, or exfiltration; model output is proposal only | 3, 7-8, 13-14 |
| Foreground isolation | Zero routine content, context, turns, focus/input changes, permission prompts, or automatic digest in active sessions | 1-3, 5 |
| Replay/freshness | Exact audience/resource/domain, nonce/jti/sequence, bounded skew, durable cache, key epoch, and fail-closed cache loss | 4, 6-7, 10-11, 19 |
| Failure uncertainty | Every outage maps to the named hold/degraded/unknown state; no stale allow, raw key, unmetered model/effect, or silent backlog | 9-11, 13-16, 19 |
| Supply chain | Signed pin, hashes, SBOM/provenance, update root rotation, rollback/freeze rejection, canary, disable and credential cleanup | 15-16, 18 |
| Privacy/non-enumeration | Unauthorized list/get/upload/download/dedup/index/metrics reveal no protected identity, membership, existence, class, or content | 4, 8, 13, 18 |

Gate 14 requires at least 1,000 adaptive trials per supported model/config with independent generation, deterministic source/sink oracles, seeded canaries, statistical results, and a full rerun after any relevant version or configuration change.

## Decision record template

Each completed bake-off ends with:

```text
Component:
Immutable pin and artifact digest:
Evidence tier reached:
Canonical seam:
Allowed deployment/profile:
Requirements and must-not-ship gates covered:
PASS findings:
FAIL findings:
BLOCKED findings:
Semantic workarounds required (must be none for acceptance):
Data egress and managed-service dependency:
Failure/revocation/offline/duplicate result:
Upgrade/rollback/backup/restore result:
Resource/capacity result:
Owner and review expiry:
Decision: accept | reject | defer
Comparator decision:
Replacement/exit plan:
REQUIREMENTS_STATUS.md changes:
```

## Execution order and present blockers

1. **Unblocked now:** official A2A SDK mapping and negative contract tests (`56` checked-in tests currently pass); pinned Uvicorn serving; pinned MCP adapter work; Psycopg client integration; canonical Python interfaces/schemas; SQLite weaker-state comparator; dedicated local PostgreSQL 18.4 adapter/migration tests; local filesystem/rclone contract tests; bubblewrap/systemd clean-launch probes; deterministic state/property tests.
2. **Provisioning needed:** a green current official A2A TCK run and official Go/Java peers; Cedar; a PostgreSQL HA/failure-domain/PITR runner and declared topology; AGNTCY components; Matrix candidate; self-hosted object-store server; scanner; OIDC/WebAuthn implementations.
3. **Owner/policy needed before activation:** PD-001 through PD-011, especially independent approval boundary, positive-authority wording, elevation, room/history, C3/model-provider, revocation continuity, operational topology, and attention policy.
4. **Trigger-deferred:** SPIFFE/SPIRE until a managed workload fleet exists; Temporal until effect complexity justifies it; MLS until C3 is explicitly enabled; future hubless authority until partition/revocation/custody gates are funded.

Current host limitations are concrete: no Docker/Podman/Kubernetes, MinIO, Temporal, Cedar, Matrix, AGNTCY, MLS, SPIRE, or ClamAV deployment; the dedicated `/tmp` PostgreSQL 18.4 process has no HA/failure-domain/PITR topology; the retained official alpha2 report is non-green and no cross-SDK/public-peer lab exists. External package caches and temporary test binaries are not portable production inputs. These are deferred availability facts, not evidence that maintained candidates failed.

No component or custom replacement may be promoted solely to meet a schedule. Any unauthorized effect/exfiltration, false durable/completion claim, foreground injection, positive authority expansion, missing required audit intent, uncontained gateway, or silent security downgrade blocks the component regardless of benchmark averages.
