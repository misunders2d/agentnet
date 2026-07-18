# Build Versus Reuse Inventory

Status: local implementation decision record; not a production certification  
Snapshot date: 2026-07-12

## Authority and scope

This inventory implements the source-of-truth rule “reuse mechanisms; retain policy ownership.” It is subordinate to these exact source snapshots:

| Source | SHA-256 |
|---|---|
| `specification.md` | `d55c90e71721e7e4f9001a531b65531077c9786adffb7b361bb2500690583042` |
| `requirements.md` | `e45d2d8fc6afcee9d1c150cfc9ceea5c9b77f07f0f076673ce6c7929614cc3e8` |
| `final-verification.md` | `2d102682dde4578b647c76bd18015c217729a50254c9df73d3f671b42ca45751` |

The extension owns corporate identity, authorization, intent, directional assignment, receipt meaning, audit meaning, isolation, delivery/effect state, and canonical schemas. A reused component may implement a mechanism behind a narrow interface; it may not become a second authority or rewrite signed history.

Decision words in this document are deliberately narrow:

- **Accept — baseline:** selected for the runnable local implementation. This does not imply a must-not-ship gate passed.
- **Accept — target:** selected architecture target, but the required local or production topology is not yet available.
- **Defer:** not enabled. Missing availability, trigger evidence, policy, or bake-off evidence is recorded; no custom substitute is approved merely because the candidate is absent.
- **Reject as authority/core:** the mechanism may still be used through an adapter, but it may not define corporate truth.
- **Build/own:** novel corporate semantics or thin integration code that cannot safely be delegated. This is not permission to reimplement mature cryptography, transport, identity, room synchronization, storage, or workflow machinery.

## Selected runnable baseline

Use **CPython 3.13** for the first runnable extension and the isolated native A2A gateway.

Reasons:

1. The concept explicitly prefers a stable Python or Go A2A gateway after cross-SDK testing and treats JavaScript v1 beta as insufficient to be the sole production dependency.
2. The workspace has CPython `3.13.13` and the official `a2a-sdk==1.1.0` installed in `.venv`.
3. The installed SDK exposes client and server APIs, REST/HTTP+JSON and JSON-RPC route builders, Agent Card resolution, request handlers, task stores, streaming/event queues, and typed protocol models.
4. Python gives a small, self-hosted single-process baseline using standard-library SQLite, authenticated local IPC, a pinned maintained ASGI server, and explicit adapters while infrastructure candidates are baked off.
5. It avoids coupling the corporate core to any Claude, Codex, Pi, or Antigravity conversation model.

Baseline limitations are explicit:

- The local SQLite profile may claim only `created_local`, `submitted`, or an explicitly documented pilot state such as `accepted_local`. It may not claim production `accepted_durable`.
- The official A2A SDK is accepted for implementation, but Gate 4 remains open until the pinned TCK, negative-security suite, binding tests, callbacks, cross-SDK tests, and credential-origin tests pass.
- The installed A2A package contains legacy v0.3 compatibility modules. They must not be mounted on the protected v1 endpoint. Missing/empty version must be rejected rather than silently routed to legacy behavior.
- The current environment does not include all optional SDK features. Uvicorn and the Psycopg client are pinned and installed, and a dedicated local PostgreSQL 18.4 process supplies migration/runtime evidence. gRPC, a PostgreSQL HA topology, telemetry, FastAPI, and the production signing profile remain unavailable or unevaluated.
- Owner decisions PD-001 through PD-011 remain policy inputs. Safe defaults may drive reversible tests but cannot be presented as approved corporate policy.

## Actual local inventory

### Python and A2A

| Item | Observed version/state | Evidence and implication |
|---|---|---|
| Workspace Python | CPython `3.13.13` in `.venv` | Selected runtime. The environment was created by `uv 0.11.28`. |
| Official A2A SDK | `a2a-sdk==1.1.0`, Apache-2.0 | Installed from the official `a2aproject/a2a-python` distribution. `pip check` reports no broken requirements. |
| A2A API and focused contract lane | Pass locally | Imports succeeded for `ClientFactory`, `A2ACardResolver`, `AgentExecutor`, `RequestContext`, `DefaultRequestHandler`, `create_rest_routes`, `create_jsonrpc_routes`, and `InMemoryTaskStore`; `56` checked-in A2A mapping, gateway, security, grant, callback, persistence, and runnable REST/JSON-RPC tests pass. This is local contract evidence, not a green official TCK, cross-SDK, public-peer, or production result. |
| Core A2A dependencies | `pydantic 2.13.4`, `protobuf 6.33.6`, `httpx 0.28.1`, `httpx-sse 0.4.3`, `google-api-core 2.31.0`, `google-auth 2.55.2`, `json-rpc 1.15.0` | Present and internally consistent. Pin hashes in the release lock before distribution. |
| HTTP route dependencies | `starlette 1.3.1`, `sse-starlette 3.4.5`, `uvicorn 0.51.0` | Present and locked. Uvicorn is the maintained runnable ASGI baseline; its presence does not satisfy target-host, isolation, HA, ingress, or production deployment gates. |
| Cryptography package | `cryptography 49.0.0` | Reuse for standard primitives and exact profiles only. Never implement new cryptographic primitives. |
| Other protocol/runtime packages | `psycopg[binary] 3.3.4`, `PyJWT 2.13.0` transitively, `pytest 9.1.1`, `hypothesis 6.156.6` | The PostgreSQL client, JOSE dependency, and test tooling are installed and locked. A dedicated local PostgreSQL 18.4 process is exercised by five tests; this implies no HA runner, production signing profile, KMS/root ceremony, or external conformance evidence. |
| Missing A2A optionals | no local `fastapi`, `sqlalchemy`, `asyncpg`, `grpcio`, or OpenTelemetry SDK | REST/JSON-RPC serving and client-side PostgreSQL integration are unblocked; gRPC, telemetry, and production topology remain deferred until selected and tested. |
| A2A TCK | retained non-green alpha2 HTTP+JSON report | The hash-bound report classifies every selected outcome without waiver. Exact current counts belong in the retained G04 evidence, not this narrative inventory. The original runner checkout is not retained as a production certification input; Gate 4 remains failed until a current required official run is green and cross-SDK/public-peer evidence exists. |

### Other runtimes, caches, and host mechanisms

| Item | Observed version/state | Decision relevance |
|---|---|---|
| Node.js / npm | Node `26.4.0`, npm `11.18.0` | Useful for MCP binding and cross-runtime contract tests; not the selected core/A2A runtime. Node provides `node:test`, `node:sqlite`, WebCrypto/Ed25519, and native TypeScript type stripping. |
| psutil | project-pinned `psutil==7.2.2` | Accepted as the maintained cross-platform PID, parent, creation-time, account, and executable-path probe. AgentNet retains repeated creation-time and executable-digest fencing; psutil data cannot grant authority by itself. Linux still hashes `/proc/<pid>/exe`; macOS/Windows path hashing remains a qualification boundary. |
| pywin32 | conditional project pin `pywin32==312; sys_platform == 'win32'` | Accepted only for Windows token SID, protected DACL, named-pipe client PID, and Job Object mechanisms. AgentNet owns claims, replay, capability, and lifecycle semantics. Missing pywin32 blocks the Windows-specific capability requiring it. |
| MCP TypeScript SDK | `@modelcontextprotocol/sdk==1.29.0` tarball cached and materialized in an npx dependency tree | Candidate for optional Claude/Codex/Antigravity local binding. It is not installed as a project dependency and is not the corporate network. |
| MCP Python SDK | project-pinned and installed `mcp==1.28.1` for spec `2025-11-25` | Selected optional local-binding SDK. It remains an adapter mechanism, not caller identity, corporate transport, or authority; Gate 5 real-harness/direct-IPC parity evidence remains open. |
| Rust | `rustc/cargo 1.95.0`; cached `rmcp 0.12.0` crate | Available for isolated sidecars or cross-language tests, but no Rust core is justified now. |
| SQLite | host CLI `3.53.3`; Python 3.13 runtime SQLite `3.50.4` | Reusable local/pilot store. WAL, fsync, crash, and corruption boundaries must be measured against the Python-linked version actually used. |
| PostgreSQL | `psycopg[binary] 3.3.4` plus dedicated local PostgreSQL `18.4`; no HA runner | Local real-server cases exercise clean-start schema-v1 migration/runtime and selected cross-instance mailbox/reconnect/quota/breaker behavior alongside hermetic contracts. Exact current counts belong in retained test evidence. This is single-process local evidence only; HA/failover/PITR/restore and independent failure domains remain unproven. |
| Artifact mechanism | `rclone 1.74.4`; local, memory, S3-compatible/MinIO, WebDAV, SFTP and other self-hostable backends supported | Strong bake-off candidate behind `ArtifactStore`; not yet accepted for production custody or immutability. |
| Object-store server | no MinIO binary and no container runtime | S3-compatible production tests are deferred until a pinned self-hosted server and runner are provisioned. |
| Host isolation | `bubblewrap 0.11.2`, `systemd 261`, `unshare 2.42.2` | Candidate mechanisms for clean-worker launch. Presence is not proof of containment; exact harness/version escape tests remain mandatory. |
| Containers/orchestration | no Docker, Podman, nerdctl, Kubernetes, or kind | Multi-service bake-offs cannot rely on Compose or Kubernetes in the current environment. |
| File scanner | no ClamAV executable | Files remain quarantine-only until a pinned scanner/transformer candidate passes Gate 13. |
| Crypto host libraries | OpenSSL `3.6.3`, libsodium `1.0.22` | Useful audited primitives; neither is MLS, SPIFFE, approval, or corporate identity. |
| Package-cache writes | external cache roots are sandbox-read-only in this session; some `uv`/`cargo` discovery commands failed with `EROFS` when acquiring locks | Provision dependencies into the workspace or an approved writable cache and capture hashes. Do not make builds depend on mutable global caches. |

## Subsystem decisions

| Subsystem | Candidate(s) and pin | Current decision | Corporate boundary retained | Failed/deferred reason and next gate |
|---|---|---|---|---|
| Canonical identity, event, task, room, grant, relationship, receipt, audit, revocation, presence, error, and artifact schemas | Domain-owned models | **Build/own** | All actor unions, state names, digests, epochs, authority chains, and fact ownership | Novel policy semantics. Reused types may be translated at an edge but cannot leak into signed canonical history. Schema/property tests are required. |
| Supervisor and harness lifecycle | Thin Python supervisor plus harness-specific launch adapters | **Build/own integration** | Exact verified human plus harness binding; zero-secret adapters; separate background workers; foreground silence | No general component can decide harness identity or foreground behavior. Reuse OS IPC, SQLite, service management, and sandbox mechanisms rather than replacing them. |
| Native A2A boundary | A2A release `v1.0.1`, wire `1.0`; Python SDK `1.1.0` | **Accept — baseline** | External actor stays `external-human-unverified`; gateway mapping, standing grants, authorization, receipts, artifacts, and effects remain corporate | Local mapping/security/callback/persistence tests pass, including OR-within-alternatives and AND-within-a-selected-scheme handling. The official alpha2 result is still non-green at 46/12/177; cross-SDK, public-peer, certificate, enabled streaming/push/artifact, and production callback evidence remain. |
| A2A as internal fabric | Same SDK/protocol | **Reject as core** | Internal identity, mailbox, room, file, receipt, audit, and authority remain independent | The standard does not supply the complete corporate trust/state model. Use only through `A2AProtocolPort`. |
| Capability descriptions | AGNTCY OASF, exact release not yet acquired | **Defer** | Imported descriptions are tainted routing metadata, never identity or authority | No local package/binary/source pin. Acquire immutable release, license, SBOM/provenance, then test schema mapping and malicious descriptions before any custom catalog format is approved. |
| Agent discovery | AGNTCY Directory, exact release not yet acquired | **Defer** | Corporate directory stays authorization-filtered, non-enumerating, and current-epoch authoritative | No local component. Must prove tenant isolation, enumeration resistance, stale/revoked key behavior, self-hosting, and offline recovery. |
| Secure transport | AGNTCY SLIM versus HTTPS plus transactional PostgreSQL outbox | **Defer component; accept thin baseline comparator** | Transport never owns actor identity, durable acceptance, mailbox custody, receipt meaning, authorization, or effect truth | SLIM is not available locally. Do not custom-build a transport beyond the simple standards-based comparator until duplicate/offline/revocation/failure bake-off is recorded. |
| Rooms and synchronization | Matrix components versus the canonical single-owner room state/outbox baseline | **Reject Matrix as corporate authority; defer component adoption** | Corporate room owner epoch, governance, membership, history, retention, artifact policy, and effect state remain authoritative | No Matrix SDK/server is local. Concept rejects Matrix as the canonical core due multi-writer/privacy/operations cost, but isolated sync/client components still require a bake-off. No production custom room-sync implementation is approved before that test. |
| Sealed-room encryption | Maintained audited MLS implementation; exact candidate not yet acquired | **Defer; C3 disabled** | Application room authorization, membership, provider disclosure, history, recovery, retention, legal hold, and metadata policy stay explicit | No MLS implementation is local. OpenSSL/libsodium are not substitutes. Never implement MLS primitives. Enable C3 only after PD-007 and full membership/removal/recovery/compromise/file-key tests. |
| Server workload identity | SPIFFE/SPIRE; exact release not yet acquired | **Defer to real fleet trigger** | Human identity and harness ownership stay separate; laptops are not forced into SPIRE | No SPIRE binaries are local and no managed workload fleet has justified the operational cost. Baseline workload credentials remain narrow and explicit; they cannot impersonate humans. |
| Human authentication and independent approval | Existing OIDC authorization-code/PKCE/JWT profile over maintained Python TLS/crypto primitives; Duo Labs `webauthn==3.0.0` (BSD-3-Clause; pinned wheel/sdist hashes) behind an AgentNet-owned ceremony service | **Accept OIDC transport and WebAuthn mechanism at local H tier; independent deployment and real-device activation remain external/owner gated** | Opaque domain principal mapping, exact transaction, purpose, approver authority, receipt/audit meaning, independent boundary, recovery, consumption, endpoint-origin policy, and connection-address pins remain corporate | OIDC uses the validated numeric snapshot while TLS/SNI/Host retain the configured hostname. The separate approval process owns strict config, encrypted SQLite custody, one-time fragment capabilities, UV-required origin/RP/challenge verification, exact transaction display, stable signed-receipt retry, expiry, audit, and credential revocation. No WebAuthn cryptography was reimplemented. Real IdP/TLS, passkey/authenticator, independently administered host/device, recovery drill, PD-001/002/004, and L/E/O evidence remain unresolved; same-boundary approval is not qualifying evidence. |
| Authorization evaluation | Cedar as the single candidate; OpenFGA/SpiceDB only after measured Cedar failure | **Accept — target; runtime deferred** | Human-only positive authority, deny-only harness eligibility, one revision, task grant, source/sink intent, and fail-closed PEP remain corporate | No Cedar runtime is local. Until acquired, only a deny-all/test oracle may stand in; it is not a production policy engine. Dual positive engines are rejected. |
| Durable transactional state | PostgreSQL `18.4` with `psycopg[binary] 3.3.4`; SQLite local comparator | **Accept PostgreSQL — target; accept SQLite — pilot only** | State names follow proven commit/RPO boundaries; event+recipient+idempotency+audit intent+outbox share one transaction | The dedicated local PostgreSQL process proves adapter/migration and selected cross-instance mechanics, not HA. SQLite can support a runnable local profile but must not emit `accepted_durable`. Production requires synchronous durability, fencing, PITR, restore, pressure, and independent-failure-domain evidence. |
| Artifact bytes | Hardened local filesystem comparator; installed `rclone 1.74.4`; no self-hosted object-store candidate selected | **Accept filesystem for local evidence; production backend blocked on G09 external evidence** | PostgreSQL manifest, hidden reservation, quarantine/release, authorization, provenance, retention, and audit stay authoritative | No object-store server/container exists. Rclone must prove exact version addressing, conditional immutability, restore, non-disclosure, and failure behavior. |
| File safety | No maintained scanner/transformer candidate selected | **Quarantine-only; external adoption gate blocked** | Scanner can attest to exact digest but cannot release, authorize, or sign corporate effects | No local scanner. Missing/stale evidence must hold files; never treat upload or signature as safe content. |
| Local tool binding | MCP spec `2025-11-25`; project-pinned Python SDK `1.28.1`; cached TypeScript SDK `1.29.0` only as a comparison candidate; OS Unix/named-pipe primitives through psutil/pywin32 | **Accept pinned Python mechanism for the optional local adapter; production gate deferred** | Caller identity comes from authenticated local session/capability, never MCP arguments; no corporate/A2A bearer pass-through; Pi remains direct IPC | Real-host package/local contracts run on Linux, macOS, and Windows, but Gate 5 still requires installed-harness semantic round trips plus privileged hostile same-account/PID/path trials. MCP as the network is rejected. |
| Clean-worker launch | `bubblewrap 0.11.2`, systemd `261`, kernel namespaces/seccomp/LSM where available | **Defer semantic lane until tested; accept as first bake-off candidates** | Supervisor owns sanitized HOME/workspace, fixed tools, broker capability, task grant, budgets, and no-foreground rule | Tools exist, but exact harness escape, credential, DNS/network, process, proc/ptrace, IPC, and broker-path tests have not run. Failed harnesses remain deterministic-only. |
| Cryptographic primitives and envelopes | `cryptography 49.0.0`, platform OpenSSL; exact-byte JWS and DSSE/in-toto initial interoperability profiles | **Reuse required; profile gate open** | Exact purpose, preimage, audience, nonce/jti, key epoch, signer role, and receipt meaning remain corporate | Cross-language vectors, canonical bytes, rotation, compromise, cache loss, and backup/recovery are not yet proven. No arbitrary-byte signing API is allowed. |
| Long-running effect recovery | Explicit transactional state machine baseline; Temporal-style engine only on trigger | **Defer Temporal; accept explicit baseline** | Workflow success cannot fabricate delivery, authorization, external effect, cancellation, or completion evidence | No Temporal SDK/server/CLI is local and current communication states do not justify its operational surface. Reconsider only when effect orchestration complexity and benchmarks demonstrate need. |
| Audit export/witness | Transactional audit intents plus independently administered append-only/WORM sink; no external product selected | **Intent contract built; external sink gate blocked** | Core commits intent before sensitive disclosure; witness detects gaps/forks but cannot prove omitted real-world events | No sink/witness candidate is local and PD-010 topology is unresolved. Protected release stops at the approved backlog ceiling. |
| Future peer custody | `MailboxCustodian` and transport-neutral envelopes; implementation deferred | **Build seams now; defer distributed authority** | IDs, signatures, epochs, receipts, authorization, and recipient state do not name a special service instance | Opportunistic relay and quorum authority require separate partition/revocation/custody gates. No availability shortcut may weaken ordinary enrolled-agent authority. |

## Canonical interface seams

These are the current owned seam names. Implementations may be Python `Protocol`/ABC types, but component-specific types must be translated at the boundary.

| Seam | Minimal responsibility | Forbidden authority leakage | Candidate adapters |
|---|---|---|---|
| `ActorContextResolver` | Resolve authenticated transport/IPC proof into the explicit actor union and exact credential/domain epochs | Payload email, role, harness, task, tenant, or Agent Card claims cannot become identity | OIDC/DPoP, workload mTLS, local peer credentials, A2A low-trust context |
| `PolicyDecisionPoint` | Evaluate one principal/action/resource/typed-context tuple at one coherent revision and return allow/deny plus determining evidence | No component may add positive authority, accept stale allow, or make prose policy | Cedar; one measured replacement only |
| `HarnessBinding` | Bind an enrolled installation and one dedicated background worker to supervisor-issued capabilities | No long-lived corporate credential in a harness; no foreground-session fallback | Claude, Codex, Pi, Antigravity adapters |
| `CleanWorkerLauncher` | Create the exact sanitized, network-restricted, model-broker-only worker profile and attest its version | A separate chat alone is not isolation; no ambient project, shell, secret, socket, or vendor credential | bubblewrap/systemd/OS-specific sandbox adapters |
| `LocalBinding` | Fixed-schema local API/tool round trip with authenticated session context | Tool arguments and MCP tokens cannot establish corporate caller identity | MCP for supported harnesses; direct IPC for Pi |
| `A2AProtocolPort` | Resolve cards, negotiate advertised bindings, send/stream/get/cancel tasks, and map every result variant | A2A IDs, roles, states, cards, and auth never become corporate identity, grant, effect, or completion truth | Official Python A2A SDK; cross-SDK test clients |
| `TransportPort` | Move exact envelopes and wake recipients; report attempts and transport-owned facts | No `accepted_durable`, recipient commit, human read, or effect completion claims | HTTPS/outbox comparator; AGNTCY SLIM candidate |
| `DirectoryPort` | Authorization-filtered lookup of opaque actors, services, rooms, endpoints, and bounded-freshness hints | No global enumeration; self-advertised security facts cannot grant trust | Corporate DB; AGNTCY Directory adapter |
| `CapabilityCatalogPort` | Import/export typed capability descriptions with provenance and expiry | Descriptions are tainted metadata and never authority | Domain schema; AGNTCY OASF adapter |
| `TransactionalStore` | Atomically commit canonical state, idempotency/digest, recipients, audit intent, receipt reservation, and outbox at a declared boundary | The storage driver cannot rename states or weaken RPO semantics | SQLite pilot; PostgreSQL production adapter |
| `MailboxCustodian` | Submit, cursor-fetch, persist, acknowledge, reconcile, expire, and expose per-recipient custody evidence | A wake hint or relay ACK cannot become recipient custody | Always-on server-agent mailbox; future direct/relay/distributed custodians |
| `RoomSyncPort` | Materialize canonical room control/events for clients and transport membership changes | Matrix power levels/event DAG or any vendor membership cannot become corporate governance | Thin baseline; isolated Matrix component candidate |
| `SealedRoomCrypto` | Apply an audited MLS group lifecycle and return cryptographic evidence bound to application epochs | Crypto membership cannot create room authorization; no hidden inspection/recovery member | Maintained MLS implementation only |
| `WorkloadIdentityProvider` | Issue/validate narrowly scoped service identity and rotation/revocation facts | Workload identity cannot assert a human or inherit human permissions | Local mTLS baseline; SPIFFE/SPIRE adapter |
| `ArtifactStore` | Put/read/verify/delete immutable private object versions by opaque key and return backend-owned durability evidence | Object URI/existence/digest cannot authorize access, release, dedup disclosure, or manifest promotion | Local filesystem; rclone-backed/self-hosted object store |
| `ArtifactScanner` | Produce digest-bound scan/derivation attestations from isolated parsing | Scanner cannot release bytes, grant access, or rewrite provenance | Pinned scanner/transformer candidates |
| `EvidenceSigner` | Sign only fixed-schema, purpose-specific canonical preimages for the signer’s owned fact | No arbitrary-byte signing, cross-purpose key reuse, or signer-owned claims about downstream actors | cryptography/KMS/HSM adapters |
| `AuditSink` | Append ordered intents/checkpoints and expose gap/fork/backlog evidence | A witness cannot approve, authorize, or claim the core emitted every real event | Filesystem test sink; independent WORM/witness target |
| `WorkflowEngine` | Schedule/recover typed effect jobs and surface its own execution facts | Workflow success cannot synthesize delivery, current authorization, external commit, or compensation truth | Explicit DB state machine; Temporal-style adapter on trigger |
| `Clock` / `ReplayStore` | Supply bounded authoritative time and durable nonce/jti/sequence checks | No indefinite skew extension or fail-open cache loss | Transactional baseline; replicated production implementation |

## Build approval rules

A custom implementation of a mature subsystem is approved only when all of the following are checked in:

1. at least one maintained candidate was pinned by immutable version/digest;
2. license, provenance, SBOM/dependency, self-hosting, egress, maintenance, upgrade, rollback, and operational ownership were assessed;
3. canonical-interface contract tests were run against the candidate and the simplest owned baseline;
4. failure, partition, offline, duplicate, revocation, enumeration, and recovery tests relevant to that subsystem were run;
5. the failure is semantic or operational, not merely “not installed today”;
6. a custom scope smaller than the failed candidate is documented;
7. replacement and history-migration boundaries remain intact; and
8. the decision does not bypass any of the 19 must-not-ship gates or PD-001 through PD-011.

Until those conditions hold, unimplemented candidate-backed capabilities fail closed or remain queued. They are not silently replaced with an in-memory, transcript-only, bearer-token, foreground, or managed-cloud shortcut.

## Current deferred/blocker register

| Record | Classification | Consequence |
|---|---|---|
| No local AGNTCY OASF/Directory/SLIM artifacts | Availability defer, not a quality rejection | Preserve catalog/directory/transport seams; acquire pinned releases before custom equivalents. |
| No Matrix component | Availability defer plus concept-level rejection as canonical core | Canonical room schemas may proceed; production room sync/component decision cannot. |
| No maintained MLS implementation | Security blocker for C3 only | C3 remains disabled; C1/C2 work continues. |
| No SPIRE | Trigger/availability defer | Narrow workload credentials may be evaluated in a laboratory profile; no SPIFFE claim and no laptop identity substitution. |
| No Cedar runtime | Authorization component defer | Implement PEP contracts and deny-all/test oracle only; no production allow path. |
| No PostgreSQL HA/failure-domain/PITR topology | Production durability blocker | The client and dedicated local PostgreSQL 18.4 process pass selected tests; SQLite remains pilot-only, and neither local store may overclaim production `accepted_durable`. |
| No object-store server/container runtime | Production artifact durability blocker | Synthetic filesystem/rclone-local bake-off only; required artifacts stay held outside that scope. |
| No scanner | File-release blocker | Uploads remain quarantined; no semantic/model exposure or released download. |
| Official TCK non-green; no cross-SDK/public peers | A2A certification blocker | The retained alpha2 report classifies every outcome without waiver; consult G04 for exact current counts. Local SDK integration may run, but ARC-004 remains unverified external. |
| No Temporal | Non-blocking trigger defer | Use explicit transactional effect state machine; reassess only on measured complexity. |
| PD-001 through PD-011 unresolved | Owner blocker for affected launch claims | Use documented safe defaults for reversible tests only; Gate 17 remains open. |

Every status change must cite immutable evidence in `evidence/` and update `REQUIREMENTS_STATUS.md`. Absence alone never becomes a “failed bake-off.”
