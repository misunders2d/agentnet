# Threat Model and Must-Not-Ship Test Plan

**Status:** implementation and verification plan; this document is not gate evidence  
**Authoritative baseline:** `specification.md`, `requirements.md`, and `final-verification.md` in this repository’s docs directory  
**Current handoff hashes:** concept `d55c90e71721e7e4f9001a531b65531077c9786adffb7b361bb2500690583042`; requirements `e45d2d8fc6afcee9d1c150cfc9ceea5c9b77f07f0f076673ce6c7929614cc3e8`

## 1. Purpose and proof rule

This plan converts the nineteen must-not-ship gates into concrete unit, contract, property/model, fuzz, integration, privileged-host, interoperability, chaos, red-team, supply-chain, and operational tests.

The core proof rule is strict:

- implementation coverage is not verification;
- a mock or local simulation cannot satisfy a real-harness, independent-device, cross-domain, physical-failure-domain, independent-administration, or owner-approval requirement;
- a skipped, deselected, quarantined, or expected-failing test is not passing evidence;
- a lower evidence tier cannot satisfy a gate that requires a higher tier;
- any unauthorized effect or exfiltration, active-session injection, positive-authority expansion, false durability/completion claim, missing required audit intent, public-gateway containment failure, or silent security downgrade blocks release regardless of aggregate pass rate.

`docs/GATE_EVIDENCE.md` is the release ledger. It starts with every gate unverified or owner-blocked. Nothing in this plan marks a gate passed.

## 2. Evidence tiers

| Tier | Name | What it can prove | What it cannot prove |
|---|---|---|---|
| `H` | Hermetic | Pure functions, schema contracts, deterministic state machines, property invariants, parser/signature vectors, and fuzz findings in an isolated test process. | OS/process isolation, real harness behavior, physical durability, external interoperability, independent administration, or human policy. |
| `L` | Local integration | Multi-process/container behavior on one development host, local PostgreSQL/object-store interactions, process killpoints, retries, and mocked dependency failures. | Independent failure domains, target-host assurance, public peers, independently controlled devices/accounts, or production RPO/RTO. |
| `P` | Privileged target-host | Exact target OS/kernel/architecture behavior, peer credentials, UID/process boundaries, namespaces, seccomp/LSM policy, ptrace/proc restrictions, key custody, installer lifecycle, and real harness escape probes. | Cross-domain independence, public interoperability, separate physical failure domains, or owner/legal approval. |
| `E` | External/lab | Real version-pinned harnesses, external SDKs/peers, multi-node failure domains, real IdP/WebAuthn/device ceremonies, partner domains, supported model configurations, and production-like failure/recovery drills. | Accountable owner policy or independent governance approval unless explicitly supplied. |
| `O` | Owner/independent authority | Signed PD-001 through PD-011 decisions, privacy/legal/retention choices, independently administered KMS/audit/update roots, and accepted operational consequences. | Technical correctness by itself; technical gates still require their own evidence. |

Every evidence record must include the source commit, dependency and harness versions, test command, selected tests, environment fingerprint, seed/corpus, start/end time, result, logs/artifacts, and signer or responsible operator. Evidence expires when a relevant model, harness, prompt, parser, policy, dependency, launch profile, OS/kernel, or security configuration changes.

## 3. Assets and adversaries

### 3.1 Protected assets

- human, guest, harness, session, workload, domain, credential, and key identities;
- positive human authority, task grants, approvals, relationship scopes, and revocation epochs;
- exact message, task, room, receipt, artifact, audit, and effect state;
- local inbox/outbox bytes, database rows, object versions, scanner attestations, and backup keys;
- active user conversation isolation and human attention;
- issuer, receipt, approval, federation, scanner, audit, update, and MLS signing purposes;
- model-provider credentials, corporate bearer credentials, download capabilities, and decryption keys;
- privacy-sensitive content, routing metadata, relationship graphs, presence, search indexes, and audit trails;
- safety capacity for cancellation, revocation, quarantine, and incident response.

### 3.2 Adversaries and failure actors

- a malicious or compromised harness, adapter, plugin, hook, skill, workspace, or same-UID desktop process;
- a malicious enrolled peer, administrator, subordinate, sponsor, guest, partner domain, public A2A peer, or gateway operator;
- hostile signed content, prompt injection, tool output, web output, file bytes, metadata, or model output;
- a confused deputy, replaying caller, copied capability holder, signer-oracle client, or cross-domain credential user;
- a compromised core, PDP, issuer/KMS, approval service, database/object administrator, scanner, effect worker, audit exporter, or update channel;
- process crashes, torn writes, response loss, network partitions, clock rollback, stale caches, key rotation races, disk pressure, failover races, split brain, and restore errors;
- accidental operator misconfiguration, unsupported mixed versions, policy drift, dependency compromise, and silent feature downgrade;
- denial-of-service, fanout loops, reconnect storms, receipt loops, storage floods, model/tool cost loops, and resource starvation.

Authentication never makes content safe, and a separate conversation is not a sandbox. The test oracle must assume every remotely influenced byte is hostile.

## 4. Global invariants and semantic promotion barriers

The following are cross-cutting property assertions, not merely examples:

1. Payload identity, email, role, harness ID, domain, route, or tenant fields never become authenticated actor identity.
2. Positive authority originates only from the verified human principal. Harness, device, session, subagent, relationship, task, capability, and posture can only preserve or reduce it.
3. A management edge grants only its explicit meta-actions; it never grants protected data access.
4. `accepted_queued` proves durable task custody only; it never proves semantic-processing, read, disclosure, or effect authorization.
5. `recipient_committed` proves exact durable recipient custody only; it never proves presentation, human reading, model processing, or completion.
6. A signed A2A Agent Card proves only authenticity of canonical discovery metadata under a locally trusted current key; it never grants corporate identity, membership, authority, or rollover.
7. A home-domain federation assertion can create only a host-local guest candidate. Host-local identity, credential, and current policy remain authoritative.
8. Authenticated content remains tainted. It cannot become system/developer instructions, policy, approval, signer preimage, security principal, or executable job.
9. Model output remains a proposal until deterministic task-grant, source, sink, action, resource, budget, and current-revocation checks pass.
10. A scanner attestation proves only its digest-bound scan fact; it cannot release an artifact.
11. Presence and advertised capability are hints, never authorization, identity, delivery, or readiness.
12. A receipt proves only the fact owned by its signer. Transport acceptance never implies recipient commit or an external effect.
13. Sensitive bytes, capabilities, or keys are never released before the corresponding transactional audit intent commits.
14. Revocation, deny, quarantine, and unknown state never degrade to stale allow, automatic retry, sibling reroute, or invented success.
15. No identifier, signature, receipt, authorization rule, or mailbox interface may require a special service identity; ordinary server-agent custodian/mesh implementations must preserve the same authority boundaries.

## 5. Trust-boundary map

| Boundary | Untrusted side and attack focus | Required invariant | Primary gates/tests |
|---|---|---|---|
| `B01` Active user session ↔ background worker | Routine content, focus/input theft, approval prompts, terminal escapes | No routine content/context/turn/focus/input mutation; explicit human-open is the sole content bridge. | Gates 1–3; `tests/integration/test_foreground_no_interference.py` |
| `B02` Harness adapter ↔ supervisor IPC | Claimed identity, sibling process, copied capability, PID reuse, socket replacement | Actor comes from authenticated peer/session binding; adapter has no long-lived corporate secret. | Gates 5 and 7; `tests/host/test_local_ipc_attribution.py` |
| `B03` Supervisor store/key custody ↔ same-user processes | Queue theft, ptrace/proc dump, key extraction, rollback | Advertised assurance must match tested OS boundary; otherwise protected operations are disabled. | Gates 6, 7, 19 |
| `B04` Clean worker ↔ host and inherited workspace | Project instructions, hooks, plugins, filesystem, process, secret, IPC, DNS/network escape | Exact clean launch manifest and deny-by-default sandbox. | Gate 3; `tests/host/test_worker_escape.py` |
| `B05` Clean worker ↔ model-egress broker | Credential theft, generic proxy use, origin/budget smuggling | Worker-bound short capability, allowlisted model/origin, fixed framed inference, no vendor credential in worker. | Gates 3 and 14 |
| `B06` Supervisor ↔ corporate edge/core | Payload identity, token replay, wrong audience/domain, stale epoch | Exact human/guest plus harness or scoped workload actor from DPoP/mTLS context. | Gates 6–8 and 19 |
| `B07` OIDC/WebAuthn/OOB ↔ enrollment authority | Mix-up, substitution, agent automation, same-device false independence | Exact transaction binding, phishing-resistant fresh authentication, one-time independent approval. | Gates 6, 17, 19 |
| `B08` Core PEP ↔ PDP/entity snapshot | Missing/stale/incoherent revisions, diagnostics, positive harness authority | One coherent revision; missing or inconsistent state denies; human is sole positive source. | Gate 8 |
| `B09` Core transaction ↔ PostgreSQL/outbox | Response loss, partial commit, stale read, split-brain writer | Accepted state, idempotency, recipient rows, audit intent, and outbox share one authoritative commit. | Gates 9, 10, 16 |
| `B10` PostgreSQL manifest ↔ artifact bytes | Orphan, object swap, wrong version, cross-class dedup leak | No artifact acceptance/access without verified immutable object version plus authoritative manifest. | Gates 9 and 13 |
| `B11` Quarantine ↔ scanner/transformer ↔ release policy | Parser escape, stale/substituted attestation, scanner self-release | Scanner is isolated and signs exact digest/profile facts; current independent policy releases. | Gate 13 |
| `B12` Core ↔ effect worker/reconciler | Model proposal as job, duplicate effect, unknown commit, fabricated completion | Typed authorized reservation; `effect_unknown` reconciles and never blind-retries. | Gates 8, 10, 14 |
| `B13` Public A2A gateway ↔ corporate core | Card trust promotion, tenant spoofing, SSRF, callback replay, credential leakage | External-low-trust actor and exact standing grant; no broad core/KMS/object credential. | Gate 4 |
| `B14` Federation gateway/home assertion ↔ host guest authority | Transitive trust, foreign role/group import, sponsor abuse, stale home revoke | Pairwise host-local identity/key/token/grant; next-decision host kill; no onward authority. | Gates 11, 16, 17 |
| `B15` Directory/capability metadata ↔ protected routing | Enumeration, forged security capability, stale endpoint/key | Authorization-filtered non-enumerating discovery; registry epochs override hints. | Gate 18 |
| `B16` Room/application authority ↔ MLS membership/key epoch | Owner loss, concurrent transfer, hidden inspection member, stale removal | One active owner; separate epochs; current room authorization precedes crypto membership. | Gates 11–13 and 17 |
| `B17` Core audit intent ↔ exporter/WORM/witness | Omission, gap, fork, silent backlog, compromised operator | Pre-release committed intent, bounded publication policy, independent checkpoint reconciliation. | Gates 13, 16, 19 |
| `B18` Build/update roots ↔ installed extension | Dependency substitution, rollback/freeze, compromised adapter, uninstall residue | Signed threshold update metadata, provenance/SBOM, canary, disable, credential cleanup. | Gate 15 |
| `B19` Current mailbox custodian ↔ future relay/mesh | Relay authority promotion, stale policy/revocation, false custody | Transport-neutral encrypted custody only; no content/data authority or central-instance identity. | Gates 9, 11, 16, 18 |

Every boundary check must bind or reject the exact actor, harness/workload, domain, audience, purpose, resource, payload digest, policy/grant/key epoch, expiry, nonce/jti/sequence, and idempotency scope. Contradictory or missing values fail closed.

## 6. Planned test layout and execution contract

The following paths are planned. Their presence in this document is not evidence that the files exist or pass.

```text
tests/
  unit/          pure schema, authorization, crypto-boundary, and projection tests
  contract/      binding-neutral API and component-contract tests
  property/      stateful/generative invariants
  model/         room/delivery model checking adapters
  fuzz/          parser, protocol, frame, metadata, and semantic conversion fuzzers
  integration/   local multi-process and storage integration
  host/          privileged target-host and exact-harness isolation probes
  security/      deterministic negative-security suites and corpora
  chaos/         killpoint, partition, pressure, failover, and restore suites
  interop/       A2A, harness, federation, and MLS interoperability
  redteam/       adaptive model/content campaigns
  supply_chain/  build, package, update, rollback, and cleanup tests
  operations/    topology, SLO, kill-switch, backup, and compromise drills
  bakeoff/       reusable-component contract and replacement tests
  vectors/       cross-language canonicalization and cryptographic vectors
```

Planned Python-style test paths are intentionally runnable under a conventional command such as `pytest <path>` once implemented. If the implementation language changes, equivalent native test runners may replace the runner, but these logical filenames and gate mappings should remain stable in the evidence manifest.

## 7. Nineteen gate plans

### Gate 1 — Foreground isolation

**Requirements:** ARC-003, UX-001, UX-002, UX-004, UX-006  
**Required evidence:** `H`, `L`, and exact-version `E`

Planned tests:

- `tests/unit/test_foreground_routing.py`
- `tests/property/test_foreground_isolation_machine.py`
- `tests/integration/test_foreground_no_interference.py`
- `tests/fuzz/test_indicator_payload_fuzz.py`

Test routine route construction, explicit-human-open capabilities, integer-only indicators, transcript/input/focus/turn/approval sentinels, reconnect/compaction, concurrent user activity, and hostile ANSI/control/Unicode payloads. Arbitrary routine traffic must leave the complete foreground trace unchanged. Real Claude, Codex, Pi, and Antigravity evidence is mandatory; a fake harness cannot close the gate.

### Gate 2 — Worker recovery

**Requirements:** AVL-003, AVL-005, AVL-006, UX-001, SEC-006  
**Required evidence:** `H`, `L`, and real-harness `E`

Planned tests:

- `tests/property/test_worker_recovery_machine.py`
- `tests/integration/test_worker_killpoints.py`
- `tests/integration/test_compaction_recovery.py`

Inject death before and after every local/core commit, receipt, cursor advance, processing lease, model result, and compaction edge. Recovery must reconstruct accepted state only from supervisor/core durable facts, preserve exact bytes, avoid cross-session state, and execute at most one reserved effect. Corrupt/truncate local queue pages and replay duplicate recovery records. Exact harness resume/compaction remains external evidence.

### Gate 3 — Harness/worker isolation and model egress

**Requirements:** ARC-003, SEC-001, SEC-006, SEC-007, OPS-005  
**Required evidence:** `H`, `P`, and exact-version `E`

Planned tests:

- `tests/unit/test_clean_worker_manifest.py`
- `tests/host/test_worker_escape.py`
- `tests/integration/test_model_egress_broker.py`
- `tests/fuzz/test_broker_protocol_fuzz.py`

Verify clean HOME/environment/workspace/session, no inherited hooks/plugins/skills/MCP/shell/profile, fixed supervisor binding, and denial of filesystem, process, ptrace/proc, IPC, browser/keychain, DNS, and arbitrary network access. Seed secret canaries. Verify worker-bound broker capability, exact model/origin/grant/budget, credential stripping, and no CONNECT/generic-proxy/redirect smuggling. A failing harness must be tested in deterministic-only mode with every semantic/effect path denied.

### Gate 4 — Native A2A

**Requirements:** ARC-004, ARC-006, OPS-002, OPS-003, SEC-005  
**Required evidence:** `H`, `L`, and external `E`

Planned tests:

- `tests/unit/test_a2a_mapping.py`
- `tests/interop/test_a2a_tck.py`
- `tests/interop/test_a2a_cross_sdk.py`
- `tests/security/test_a2a_gateway_attacks.py`
- `tests/fuzz/test_a2a_protocol_fuzz.py`

Cover Task versus direct Message, every StreamResponse variant, unspecified role/state, server-owned IDs, artifact direction, input/auth-required, per-agent Card/route, tenant mismatch, standing-grant revoke, push duplicates/gaps, direct-Message stream loss, callback replay, credential-origin confinement, key/cache rotation, enumeration, and SSRF/DNS/redirect attacks. `securityRequirements` alternatives are OR; all schemes and scopes inside the selected alternative are AND. Binding literals must match the official specification exactly. The pinned TCK must have zero MUST failures, and cross-SDK/public-peer evidence is external.

### Gate 5 — MCP and local API

**Requirements:** ARC-002, ARC-005, AUTH-001, AUTH-002, AUTH-004  
**Required evidence:** `H`, `L`, and real-binding `E`

Planned tests:

- `tests/contract/test_local_binding_parity.py`
- `tests/integration/test_mcp_token_confinement.py`
- `tests/integration/test_pi_direct_ipc.py`
- `tests/fuzz/test_local_api_framing_fuzz.py`

Run one typed operation corpus through supervisor API, MCP bindings, and Pi direct IPC and compare canonical results. Spoofed identity arguments must not affect authenticated context. Corporate and A2A bearer tokens must never enter MCP. Fuzz JSON-RPC/framing, nested arguments, duplicate IDs, cancellation, partial writes, and reconnect.

### Gate 6 — Identity and enrollment

**Requirements:** ID-001 through ID-009, AUTH-001, AUTH-002, AUTH-007, SEC-002, SEC-005, SEC-006  
**Required evidence:** `H`, `L`, real ceremony `E`, and policy `O`

Planned tests:

- `tests/unit/test_enrollment_transcript.py`
- `tests/security/test_oidc_webauthn_dpop_attacks.py`
- `tests/integration/test_credential_lifecycle.py`
- `tests/fuzz/test_jose_enrollment_fuzz.py`

Test exact transaction hashing, OIDC issuer/audience/state/nonce/PKCE, WebAuthn origin/RP/user verification, proof of possession, DPoP method/URI/audience/domain/jti, key epochs, refresh rotation, recovery, device loss, and offline revocation. Attack mix-up, code/OOB substitution, reused challenges, copied identifiers, stolen tokens, sibling/cross-domain replay, response loss, and harness automation of approval. Mocks test logic only; real workforce IdP, authenticator, independent channel, platform custody, and owner-approved ceremony are required.

### Gate 7 — Local IPC and signing oracle

**Requirements:** ID-003, ID-004, ID-006, ID-007, AUTH-001, AUTH-002, AUTH-004, AUTH-007  
**Required evidence:** `H` and target-host `P`; exact harness evidence may require `E`

Planned tests:

- `tests/unit/test_signer_purpose_schema.py`
- `tests/host/test_local_ipc_attribution.py`
- `tests/security/test_signing_oracle_attacks.py`
- `tests/fuzz/test_ipc_frame_fuzz.py`

Reject arbitrary-byte signing, unknown critical fields, wrong purpose/audience/domain/session, stale capabilities, and raw caller identities. Attack wrong and sibling processes, same UID, capability copying, PID reuse, socket replacement/symlink, inherited file descriptors, ptrace/proc dump, replay, restart, and flood. If exact same-UID attribution is not proven, verify that the compromise-domain fallback allows only draft, explicit human viewing, and deterministic non-business control while every protected operation denies.

### Gate 8 — Authorization and intent

**Requirements:** AUTH-003 through AUTH-010, ORG-001 through ORG-006, COM-007  
**Required evidence:** `H`, `L`, and owner policy `O`

Planned tests:

- `tests/unit/test_authorization_fail_closed.py`
- `tests/property/test_authority_lattice.py`
- `tests/integration/test_grant_reservation_atomicity.py`
- `tests/integration/test_directional_assignment.py`
- `tests/property/test_relationship_lifecycle.py`
- `tests/fuzz/test_policy_input_fuzz.py`

Generate arbitrary humans, harnesses, grants, relationships, revisions, resources, and revocations. Only human entitlement may add authority; deny/revoke wins. Missing, stale, diagnostic, schema-invalid, or incoherent state denies. Verify one-use grant/reservation atomicity and next-decision revocation. Admin-to-subordinate matching assignments reach `accepted_queued`; expired, revoked, scope-mismatched, upward, and lateral assignments remain non-executable absent a separate exact directed edge. Test concurrent revoke/renew, multiple administrators, subject exit, fencing, incompatible commands, and legal/security override.

### Gate 9 — Durability

**Requirements:** FILE-003, FILE-004, AVL-003, AVL-005, AVL-006, AVL-007, OPS-001  
**Required evidence:** `H`, `L`, and multi-node/failure-domain `E`

Planned tests:

- `tests/property/test_durability_invariants.py`
- `tests/chaos/test_artifact_commit_killpoints.py`
- `tests/chaos/test_postgres_failover_restore.py`
- `tests/integration/test_offline_reconnect.py`
- `tests/integration/test_attachment_durability.py`

Fault every reservation, object write/ack/verification, promotion, event/audit/outbox commit, response, dispatch, and receipt boundary. `accepted_durable` must imply exact recoverable bytes and, for required files, the verified immutable object version plus manifest. Test same-key/different-digest conflict, torn writes, disk full, concurrent retry, reservation expiry, orphan inventory, required/optional attachment behavior, fanout, pressure, 1-hour/7-day/30-day offline, reconnect storms, failover/fencing, PITR, and full restore. One-host containers cannot certify RPO=0 or independent failure domains.

### Gate 10 — Delivery, effects, and expiry

**Requirements:** COM-011, AVL-005, AVL-006, SEC-003, SEC-005  
**Required evidence:** `H`, `L`, and connector-specific `E` for real effects

Planned tests:

- `tests/property/test_delivery_effect_state_machine.py`
- `tests/integration/test_effect_unknown_reconciliation.py`
- `tests/integration/test_expiry_cancel_commit_races.py`
- `tests/fuzz/test_receipt_ordering_fuzz.py`

Model every actor-owned fact and per-recipient branch. No actor may assert another actor's fact; partial delivery is computed; completion/cancellation/global success requires owning evidence. `effect_unknown` must reconcile and never blind-retry. Race response loss, cancellation, reservation, commit, expiry, late receipts, contradictory receipts, and clock failure. Retention is independent of delivery/effect expiry, and exact retry cannot reopen an expired branch.

### Gate 11 — Revocation

**Requirements:** ID-007, ID-009, AUTH-007, FILE-002, FED-009, SEC-006  
**Required evidence:** `H`, `L`, cross-domain `E`, and continuity policy `O`

Planned tests:

- `tests/property/test_revocation_matrix.py`
- `tests/integration/test_revocation_next_decision.py`
- `tests/integration/test_no_sibling_reroute.py`
- `tests/interop/test_federation_revocation_slo.py`

Exercise acceptance, queue, presentation, read, download, key release, processing, reservation, effect, room membership, and streams for human, harness, sibling, recipient, room, guest, key epoch, compromise window, domain quarantine, stale policy, and MLS epoch. Revocation is monotonic at the committed decision epoch, cannot reroute through a sibling, and must apply during offline/cache/stream races. Partner signal SLO and host kill require real cross-domain evidence and PD-009.

### Gate 12 — Room authority

**Requirements:** COM-008, COM-010, SEC-002, SEC-006  
**Required evidence:** `H`, `L`, external MLS/domain `E` where enabled, and governance `O`

Planned tests:

- `tests/model/test_room_authority_model.py`
- `tests/property/test_room_membership_history.py`
- `tests/integration/test_room_transfer.py`
- `tests/interop/test_mls_lifecycle.py`

Model-check one owner, monotonic control sequence, frozen immutability, transfer CAS, competing transfers, cutoff ownership, crash at every transfer phase, permanent-loss tombstone, and fork labeling. Test membership/history visibility, removal, guests, legal hold, owner/recovery threshold, and independent application/owner/MLS epochs. A two-domain transfer must reconcile every event, recipient, artifact, effect, cancellation, capability, key, and audit row before commit. C3 requires the selected maintained MLS implementation and real multi-device evidence.

### Gate 13 — Files and audit

**Requirements:** FILE-001 through FILE-006, SEC-002, SEC-003, SEC-004, SEC-006  
**Required evidence:** `H`, `L`, production backend/scanner `E`, and legal/audit `O`

Planned tests:

- `tests/security/test_hostile_file_corpus.py`
- `tests/unit/test_artifact_attestations.py`
- `tests/security/test_dedup_non_disclosure.py`
- `tests/chaos/test_audit_release_order.py`
- `tests/integration/test_audit_witness.py`
- `tests/integration/test_artifact_restore.py`

Use malware fixtures, polyglots, MIME/extension mismatch, archives, symlink/path traversal, decompression/parser bombs, macros, links, and secret canaries. Test digest/version/profile-bound attestations, substitution, staleness, broken lineage, object swap, and E2EE membership. Run statistical cross-domain/tenant/class timing, quota, and duplicate probes. Kill between authorize, audit-intent commit, and capability/key/bytes release; sensitive material must never release first. Test omission, gaps, forks, checkpoints, backlog ceiling, outage, deletion, legal hold, and restore.

### Gate 14 — Signed-peer abuse and intent

**Requirements:** AUTH-005, AUTH-006, AUTH-007, SEC-001, SEC-004, SEC-006, OPS-005  
**Required evidence:** `H`, `L`, and exact-model/config `E`

Planned tests:

- `tests/redteam/test_adaptive_signed_peer_abuse.py`
- `tests/security/test_source_sink_oracle.py`
- `tests/property/test_budget_safety_capacity.py`
- `tests/fuzz/test_semantic_to_typed_fuzz.py`

Put deterministic source/sink oracles around every protected read, credential use, disclosure, and effect. Use independently generated adaptive prompt-injection/exfiltration trials with seeded canaries and data-class/effect/sink coverage. Run at least 1,000 trials per exact model, harness, prompt, tool, parser, policy, and launch profile and report seeds, coverage, and confidence bounds. Fuzz semantic-to-typed conversion and hostile tool/file/web/Card output. Loop, fanout, reconnect, and cost floods must remain inside budgets while cancellation/revocation/security capacity remains available. The campaign is empirical evidence, never universal proof.

### Gate 15 — Supply chain

**Requirements:** ARC-001, SEC-007, OPS-006  
**Required evidence:** `H`, target-platform `P/E`, and independently administered roots `O`

Planned tests:

- `tests/supply_chain/test_reproducible_artifacts.py`
- `tests/supply_chain/test_update_metadata.py`
- `tests/supply_chain/test_install_uninstall_cleanup.py`
- `tests/fuzz/test_package_metadata_fuzz.py`

Verify reproducible artifacts, signatures, provenance, SBOM, dependency pins, threshold metadata, and root rotation. Attack rollback, freeze, expiry, compromised-adapter disable, canary failure, partial update, reinstall, and uninstall credential residue. Fuzz metadata, package manifests, archive extraction, path handling, and version comparison. Every supported OS/architecture needs its actual installer/update channel and separately controlled signing roots.

### Gate 16 — Operations, failure, and component adoption

**Requirements:** ARC-002, ARC-005, AVL-007, OPS-001, OPS-004, OPS-005, OPS-006, OPS-007  
**Required evidence:** `L`, production-like `E`, and topology/adoption `O`

Planned tests:

- `tests/chaos/test_dependency_failure_contract.py`
- `tests/operations/test_backup_restore.py`
- `tests/operations/test_kill_switch_slo.py`
- `tests/bakeoff/test_component_contract.py`
- `tests/bakeoff/test_component_replacement.py`

Automate every dependency row in concept §14.4 and assert the exact hold/degraded state; forbid alternate credentials, stale allows, false durability, and silent backlog. Exercise backup/restore, kill switches under load, ceilings, unsupported combinations, and recovery reconciliation. Run every reuse candidate through one canonical contract covering identity mapping, revocation, offline/duplicate behavior, failure, egress, upgrade/rollback, schema mapping, and replacement. Attach pinned version, license, provenance, SBOM, self-hosting, resource, and operational evidence. Physical/admin topology and operator procedures cannot be proven locally.

### Gate 17 — Owner policy

**Requirements:** every feature depending on PD-001 through PD-011  
**Required evidence:** `O`; no technical substitute exists

Planned tests:

- `tests/unit/test_policy_decision_records.py`
- `tests/integration/test_feature_policy_gate.py`
- `tests/security/test_unsigned_policy_rejection.py`

Use machine-readable owner records containing decision, consequence, scope, owner, version, effective time, and signature. Map each feature to required PD records. Startup/readiness must reject enabling a dependent feature when its record is absent, expired, unsigned, or mismatched. Safe defaults permit development only; they never satisfy this gate.

### Gate 18 — Discovery, version, and configuration

**Requirements:** OPS-002, OPS-003, OPS-006  
**Required evidence:** `H`, `L`, and real mixed-version `E`

Planned tests:

- `tests/unit/test_directory_non_enumeration.py`
- `tests/integration/test_profile_handshake.py`
- `tests/integration/test_rolling_upgrade.py`
- `tests/integration/test_config_migration.py`
- `tests/fuzz/test_handshake_config_fuzz.py`

Test authorization-filtered non-enumerating list/get, forged/stale capability denial, bounded hint freshness, and key/endpoint epoch rotation. Cover N/N-1, unknown major/critical fields, no security downgrade, unsupported event queue/rejection, and preservation of signed unknown fields. Run expand/migrate/verify/contract and rollback only inside the compatibility window; revocation state never rolls back. Config export redacts secrets, import requires rebinding, and unknown security settings deny.

### Gate 19 — Freshness, cryptography, and audit roots

**Requirements:** ID-004, ID-009, AUTH-002, AUTH-007, SEC-002, SEC-003, SEC-005, SEC-006  
**Required evidence:** `H`, `L`, external infrastructure `E`, and independent roots `O`

Planned tests:

- `tests/vectors/test_cross_language_crypto_vectors.py`
- `tests/unit/test_freshness_boundaries.py`
- `tests/chaos/test_replay_cache_partition.py`
- `tests/integration/test_key_rotation_backup_restore.py`
- `tests/operations/test_compromise_rebuild.py`

Publish exact cross-language vectors for canonical preimages, signatures, receipts, callbacks, approvals, and artifact statements. Test age/future-skew boundaries, clock rollback, nonce reuse, jti entropy, persistent replay cache, sequence gaps, and key activation/overlap/revocation. Inject cache loss/partition, KMS outage, stale/substituted keys, cross-class decrypt, ACL denial, offline rotation, and backup-key restore. Drill quarantine, protected-effect stop, independent-log comparison, authority rebind, restore, and adjudication. Hardware custody, separately administered KMS/audit roots, and catastrophic compromise recovery are external/owner evidence.

## 8. Local proof ceiling

A runnable local build can implement and locally test all canonical schemas, state machines, APIs, denial paths, safe fallbacks, deterministic source/sink controls, and simulated failure behavior. It cannot honestly certify:

| Claim | Relevant requirements/gates | Evidence still required |
|---|---|---|
| Four real harnesses and zero active-session interference | ARC-003; UX-001 through UX-006; gates 1–3 and 5 | Exact version-pinned Claude, Codex, Pi, and Antigravity binaries, credentials, UI/session instrumentation, and target-host execution (`P/E`). |
| Native A2A compatibility and public isolation | ARC-004, ARC-006, OPS-002, OPS-003; gate 4 | Pinned TCK, independent SDKs, certificates/callbacks, cross-SDK runs, and public peers (`E`). |
| Real verified human and independent approval | ID-001, ID-002, ID-004, ID-009, AUTH-008, AUTH-009; gates 6, 17, 19 | Workforce IdP, phishing-resistant WebAuthn, separately controlled device/boundary, platform key custody, and owner policy (`E/O`). |
| Same-UID exact harness attribution | ID-006, AUTH-001, AUTH-004; gate 7 | Target OS/LSM/container/measurement evidence (`P/E`), otherwise the documented deterministic-only fallback applies. |
| Production durability and HA | FILE-003, AVL-003, AVL-004, AVL-007, OPS-001; gates 9 and 16 | Separate physical failure domains, synchronous PostgreSQL topology, replicated artifact backend, fencing, PITR, restore, and measured RPO/RTO (`E/O`). |
| Cross-company federation and revocation SLO | FED-001 through FED-009; gates 11, 16, 17 | Independently administered domains, partner identity, sponsor lifecycle, host kill, outage, incident, and revocation drills (`E/O`). |
| Production file safety, retention, and audit independence | FILE-003 through FILE-006, SEC-003, SEC-004; gate 13 | Selected scanner/object/WORM systems, independent witness administration, legal hold/deletion policy, and restore evidence (`E/O`). |
| C3 MLS lifecycle and model-provider disclosure | COM-008, COM-010, SEC-002; gates 11–13 and 17 | Maintained MLS implementation, real multi-device lifecycle, visible inspection members, and PD-007 (`E/O`). |
| Zero observed unintended effect in the required campaign | SEC-001, OPS-005; gate 14 | At least 1,000 adaptive trials for every exact supported model/config and complete reruns after relevant changes (`E`). This remains empirical, not universal proof. |
| Signed distribution and independent update roots | SEC-007, OPS-006; gate 15 | Real package channels on every supported OS/architecture and independent threshold-root administration (`P/E/O`). |
| Production operational topology and component suitability | OPS-001, OPS-004 through OPS-007; gate 16 | Capacity/SLO/restore drills on the selected topology, operator procedures, and reviewed third-party adoption records (`E/O`). |
| Organizational policy | Gate 17 and all PD-dependent requirements | Signed accountable decisions for PD-001 through PD-011 (`O`). No mock, default, or code path can substitute. |
| Catastrophic-root recovery | SEC-002, SEC-003, SEC-006; gate 19 | Independent KMS, approval, audit, database/object, and witness roles plus a real compromise/rebuild exercise (`E/O`). |

## 9. Immediate owner and environment blockers

Unblocked implementation and local negative testing should continue. Release certification remains blocked on:

1. signed owner decisions PD-001 through PD-011;
2. the qualifying independent enrollment/approval boundary and recovery owner;
3. the exact production physical/admin topology, accepted durability name, RPO/RTO, backlog ceilings, and witness arrangement;
4. PD-007 plus maintained MLS and provider policy if C3 is enabled;
5. PD-008/009 and independently administered partner infrastructure if federation is enabled;
6. exact supported harness versions, target OS/architectures, model configurations, public A2A peers/SDKs, and external lab credentials;
7. independently administered KMS/audit/update roots and legal/privacy/retention authority.

These blockers restrict claims and feature enablement; they do not justify weakening a requirement or marking a simulation as release evidence.
