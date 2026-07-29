# AgentNet — Final Specification Verification

Date: 2026-07-13

## Sealed baseline and dated implementation handoff

- Concept: `specification.md`
- Sealed-audit SHA-256: `6f253942e8590e2524697ee41232f58a9c18b0f4cfca8fb15a327963554557a3`
- 2026-07-13 implementation-handoff SHA-256: `73c8415622f3b328159e67ce84dfebe79bdee5df15f9923f4e2b9768eb8d1a8d`
- Requirements baseline: `requirements.md`
- 2026-07-13 requirements-handoff SHA-256: `7f61f1ad1038997279a767864c444e1f79105bc5e3768bc31b1e7ffa1e72f8e3`

These hashes identify the dated sealed/handoff snapshots; they are not hashes
of the current uncommitted worktree or a current independent verification.

The concept remained byte-for-byte frozen throughout the sealed audits. Current
files retain the exact 85 stable requirement IDs but are no longer sealed bytes.
After those audits, the owner supplied normative assignment, self-hosting, and
reuse clarifications. Later implementation passes added explicitly labeled
status annotations for bilateral relationship consent, immutable governance
baseline schema v1 plus current contiguous Core migrations 2–5, separate
Approval schema v4, completed local ORG-005 adjudication, recipient-owned
protected task payload release, the ORG-006 owner blocker, and current S0–S9
zero-state C0 plan, activation, proof, persistent invalidation, cleanup,
release-candidate packaging, and evidence/skill mechanics. Their
cited local checks are implementation evidence,
not a new independent sealed concept audit, release certificate, or production
claim.

## Consensus result

- Codex sealed audit: `brainstorm-codex-sealed-audit.md` — `VERDICT: PASS`
- Pi sealed audit: `brainstorm-pi-sealed-audit.md` — `VERDICT: PASS`
- Claude product/harness audit and corrected re-audit: `brainstorm-claude-final-audit.md` and `brainstorm-claude-final-reaudit.md` — `VERDICT: PASS`; its later C3 model-provider, A2A route-grant, and RFC-label findings were incorporated before the frozen version.

The sealed baseline closed the blocking and high findings raised during the independent research, cross-critique, dispute re-research, convergence, and adversarial audit rounds. That verdict applies to the sealed baseline, not automatically to later implementation annotations. The current implementation handoff is mechanically checked and locally tested separately; neither statement is a production certification.

## Post-audit owner clarification

The current handoff adds an explicit implementation rule under ORG-002, ORG-005, and COM-007:

- a current directed `may_assign` edge plus matching `assignment_scope` allows automatic durable `accepted_queued` custody from administrator to subordinate;
- reverse or lateral assignments are non-executable `pending_human` proposals unless another directed edge grants the exact authority;
- automatic acceptance never transfers the administrator's permissions or bypasses recipient-side authorization for semantic processing, protected reads, or effects; and
- release gate 8 must test both allowed and denied directions, revocation, expiry, and scope mismatch.

This was an additive owner-policy clarification after the sealed audits, not a new independent sealed audit. The identifier/count verification below was rerun on the current handoff; it does not seal the added content.

The owner subsequently clarified infrastructure direction as well: the product must be self-hostable on company-controlled infrastructure with no mandatory AWS or other managed-cloud dependency; “object storage” is a provider-neutral artifact interface; one continuously available ordinary extension server-agent is the initial reliable profile; and direct, opportunistic-relay, and fully distributed operation remain explicit future profiles. There is no separate Hub product. ARC-001, AVL-004/007, OPS-001/006, §9.7, §14, and their verification rows now carry this requirement. The 85-ID set remains unchanged.

The owner also confirmed a reuse-first implementation policy after reviewing the latest Pi exchange. The current handoff now requires a build-versus-reuse inventory and component bake-off before custom implementation; explicitly evaluates A2A, AGNTCY OASF/Directory/SLIM, Matrix components, maintained MLS, SPIFFE/SPIRE, Cedar, PostgreSQL, existing artifact stores, MCP, and Temporal-style engines; and preserves the boundary “reuse mechanisms, retain policy ownership.” ARC-002/005 and OPS-007 carry this requirement without changing the 85-ID set.

On 2026-07-13, implementation/evidence annotations were added for the bilateral administrator-governance lifecycle: proposal has zero authority; normal activation requires exact current subordinate human/guest-owner approval through the independent verifier; a separately signed and recorded exception has a distinct one-use path; both bases require one exact atomically completed local activation intent; renewal, expiry, subject exit, revocation races, rollback injection, clean schema-v1 creation, and rejection of nonempty pre-release databases fail closed. Directional custody binds an explicit or server-derived scope/relationship-bounded deadline, and generic readers permanently withhold task/task-linked-control payloads, including missing-marker rows. Incompatible typed intents from multiple authorized administrators now enter atomic, arrival-order-independent holds; exact current subordinate-owner adjudication, overlapping staged release, cross-conflict terminal propagation, replay/revision fencing, and real-PostgreSQL races are executable. At that checkpoint release remained custody-only and no protected TaskGrant payload-release route was claimed. These annotations report implemented local mechanics and provenance, not an independent witness or approved policy. ORG-005 is locally implemented; ORG-006 remains owner-blocked because accountable policy and production evidence do not exist.

On 2026-07-15, a later unreleased implementation annotation added the separate recipient-owned protected release path: one exact current `task.process` grant use at authorization, redacted durable local custody, current intent/conflict/epoch/lifetime and immutable payload/provenance checks, audit plus one disclosure receipt before bytes, response-loss retry without another use, and mandatory release-receipt linkage before result upload. Generic reads remain nondisclosing and tool/effect authority remains false. Migration 1 stays byte-identical; contiguous migration 2 adds only the disclosure-receipt relation, with exact SQLite v1→v2 atomic-upgrade/rollback evidence. This work postdates the sealed audit and does not promote any external or owner gate.

The same unreleased branch now includes the smallest separately runnable
WebAuthn-UV approval component. It reuses pinned Duo Labs `webauthn==3.0.0`
for ceremony verification while AgentNet retains exact purpose, approver,
domain, canonical transaction, signed receipt, audit, expiry, replay, and
credential-lifecycle semantics. Strict owner-only config/key custody, encrypted
exact-catalog SQLite state, fragment-only one-time capabilities, bounded
no-store browser/API routes, exact transaction display, committed expiry/denial
audit, stable one-receipt retry, and credential revocation have local hermetic
evidence. Existing core consumers remain receipt-only. This closes a software
component gap, not the independence gate: no real authenticator, independently
administered host/device/OS/TLS boundary, rotation/recovery drill, or PD-002/004/
005/009 evidence is claimed. ID-002/009 and AUTH-008/010 retain their
owner/external status.

## Mechanical verification

| Check | Result |
|---|---:|
| Baseline requirement IDs | 85 unique |
| Concept §20 mappings | 85 unique, exactly once |
| Missing or extra requirement IDs | 0 |
| Decision records | 16 |
| Owner policy decisions | 11 |
| Must-not-ship gates | 19 |
| Unique local evidence targets | 14, all present |
| External sources in concept | 53 |

## Remaining work by design

The document deliberately leaves implementation evidence open. Before release, owners must resolve PD-001 through PD-011 and the implementation must pass the named isolation, identity, A2A conformance, durability, cryptographic, file-safety, federation, recovery, abuse, and operational gates.

Codex recorded one non-blocking protocol-precision item for implementation: when processing A2A `securityRequirements`, alternatives are OR, while every scheme and scope inside the chosen alternative is AND. Gate 4 must test this and serialize the official binding literals exactly.

FINAL SEALED-BASELINE DESIGN VERIFICATION: PASS
