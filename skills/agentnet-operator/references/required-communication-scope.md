# AgentNet required communication scope

Use this reference to decide whether an installation or readiness claim delivers the product described by AgentNet. The authoritative acceptance criteria remain `docs/requirements.md`; `docs/specification.md` defines the corresponding invariants and state machines. Do not replace those sources with this summary.

## Product boundary

- **ARC-001..006:** one separately installable, agent-agnostic extension supports Claude, Codex, Pi, Antigravity, and isolated standards-compliant A2A interoperability.
- An always-on node is an ordinary enrolled server agent with explicit capabilities. There is no separate privileged Hub product or Hub identity class.
- Public A2A identity, task state, message content, Agent Cards, or receipts do not create corporate identity, authority, or business-effect proof.

## Communication and collaboration

A supported AgentNet product must preserve the mechanisms and semantics for:

- **COM-001:** direct agent-to-agent and user-to-user communication;
- **COM-002..004:** laptop-to-server-agent, server-agent-to-laptop, and server-agent-to-server-agent traffic;
- **COM-005:** one-to-many, many-to-one, and many-to-many topologies;
- **COM-006:** durable direct conversations;
- **COM-007:** manager/subordinate communication with directional, scoped assignment rules;
- **COM-008:** persistent group collaboration and rooms;
- **COM-009:** verified human principal and originating-harness attribution on every contribution;
- **COM-010:** temporary meetings and brainstorming spaces;
- **COM-011:** threads, replies, mentions, structured tasks, handoffs, cancellation, acknowledgements, and durable response obligations.

Do not call a deployment complete merely because two agents exchanged one direct message. That round trip is useful evidence for one path only.

## Tasks and organization

- **ORG-001..006:** directed many-to-many administration does not transfer the administrator's data permissions.
- In-scope downward assignment may enter `accepted_queued` custody; custody is not payload access, execution authority, or business-effect completion.
- Upward, lateral, cross-domain, or out-of-scope assignment requires the exact separately authorized relationship or human confirmation.
- Queued custody is never executable delegated work. Protected source builds release payload only after exact recipient-owned `task.process` authorization, durable local custody, current intent/conflict/epoch/lifetime checks, and audit/receipt commit. Generic reads remain redacted; tool/effect authority remains false and requires separate authority.

## Files and artifacts

- **FILE-001..006:** files and typed artifacts remain first-class in every authorized topology.
- Required lifecycle stages include pre-upload authorization/reservation, encrypted immutable quarantine, digest/manifest/lineage binding, safety scanning, policy-gated release, current authorization on every access, bounded download capability, retention, deletion, legal hold, restore, and audit.
- Missing scanner, production artifact backend, key custody, or release evidence keeps files quarantined; it does not justify dropping file support from the product scope.

## Availability, delivery, and receipts

- **AVL-001..008:** offline operation is normal. Authorized custody, retries, reconciliation, failover rules, and future peer-assisted seams preserve honest states.
- Transport is at least once with durable idempotency and replay prevention—not magical exactly-once business execution.
- Preserve distinct submitted, custody, queued, delivered, acknowledged/processed, rejected, canceled, expired, effect-committed/failed, and `effect_unknown` facts as applicable.
- A receipt states only what its authenticated issuer can prove.

## Non-interruption

- **UX-001..006:** communication and delegated work run outside the user's foreground harness conversation.
- Routine activity cannot steal focus, inject turns, expose content in passive UI, or consume the active conversation context.
- Passive indication is content-free; only explicitly classified approvals, incidents, high-risk expiry, or terminal failure may enter an attention path.

## Federation and gated capabilities

- **FED-001..009:** federation is bilateral, host-controlled, least-privilege, non-transitive, domain-explicit, rapidly revocable, and independently audited.
- Federation, C3 sealed rooms/MLS, partner access, high-impact effects, and other owner/evidence-gated capabilities remain disabled until their exact gates pass.
- Gated does not mean removed from the product requirements. AgentNet must ship the governed mechanism or an explicit disabled seam and must not falsely activate it.

## Install-and-use rule

AgentNet owns the installer, deployment manifests, adapters, protocol mappings, ceremony service, preflight diagnostics, and integration tests needed for the supported profile. Maintained third-party mechanisms may be pulled by immutable digest behind AgentNet-owned interfaces.

The operator may supply approved hosts, network names, secret values, workforce tenancy, trust roots, owner decisions, and required human approval ceremonies. The operator must not be required to write missing adapters, scanner wrappers, approval applications, receipt formats, storage glue, or undocumented infrastructure orchestration.

When a required component or evidence tier is missing, report the exact blocker. Do not:

- substitute synthetic C0/local conformance for real communication;
- invent a weaker starter product that claims requirement coverage;
- add privileged Hub authority;
- omit required files, rooms, tasks, obligations, offline custody, attribution, or non-interruption semantics;
- enable owner-gated capabilities without approval;
- infer processing or business effects from transport success.
