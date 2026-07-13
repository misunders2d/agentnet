# AgentNet — Final Specification Verification

Date: 2026-07-13

## Sealed baseline and current implementation handoff

- Concept: `specification.md`
- Sealed-audit SHA-256: `6f253942e8590e2524697ee41232f58a9c18b0f4cfca8fb15a327963554557a3`
- Current implementation-handoff SHA-256: `b44b3e1467dbc8752fd77d4605231d31b42d400e3938cb7117946ca01a859c30`
- Requirements baseline: `requirements.md`
- Current requirements SHA-256: `00ec66f0b84f2c55566d777736776393a6092aec3afaa426981af6f106a5ebc4`

The concept remained byte-for-byte frozen throughout the sealed audits. The current files retain the exact 85 stable requirement IDs but are no longer the sealed bytes. After those audits, the owner supplied normative assignment, self-hosting, and reuse clarifications, and the implementation pass added explicitly labeled evidence/status annotations for bilateral relationship consent, clean first-release schema v1, completed local ORG-005 adjudication, and the ORG-006 owner blocker. These additions have local executable review evidence; they have not received a new independent sealed concept audit.

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

On 2026-07-13, implementation/evidence annotations were added for the bilateral administrator-governance lifecycle: proposal has zero authority; normal activation requires exact current subordinate human/guest-owner approval through the independent verifier; a separately signed and recorded exception has a distinct one-use path; both bases require one exact atomically completed local activation intent; renewal, expiry, subject exit, revocation races, rollback injection, clean schema-v1 creation, and rejection of nonempty pre-release databases fail closed. Directional custody binds an explicit or server-derived scope/relationship-bounded deadline, and generic readers permanently withhold task/task-linked-control payloads, including missing-marker rows. Incompatible typed intents from multiple authorized administrators now enter atomic, arrival-order-independent holds; exact current subordinate-owner adjudication, overlapping staged release, cross-conflict terminal propagation, replay/revision fencing, and real-PostgreSQL races are executable. Release remains custody-only. No protected TaskGrant payload-release route is claimed. These annotations report implemented local mechanics and provenance, not an independent witness or approved policy. ORG-005 is locally implemented; ORG-006 remains owner-blocked because accountable policy and production evidence do not exist.

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
