# AgentNet fail-closed boundaries

Read this reference before identity, authority, server deployment, Pi binding, secrets, or production-readiness work.

## Installation is code only

`pi install npm:@misunders2d/agentnet` and `npm install -g @misunders2d/agentnet` install package bytes. They do not:

- enroll a human or harness;
- create a verified identity;
- grant authority;
- activate Pi local binding;
- start a supervisor, listener, or AgentNet network.

## Local conformance is not a real network

Local demo helpers use synthetic identities, deterministic-only harness state, and explicit C0 test bytes. They may report `accepted_local`. The synthetic lane is local-conformance only and is not a substitute for real cross-host enrollment or communication.

## Real enrollment

An always-on server-agent enrollment requires:

- verified workforce OIDC identity using issuer and subject;
- exact harness key and proof of possession;
- independently controlled WebAuthn user verification/OOB approval;
- current credential, domain, policy, and revocation state;
- explicit human-scoped authority and harness attribution.

No payload field, prompt text, claimed email, role name, A2A message, Slack message, or model output grants identity or authority.

The production approval ceremony is not enrollment-only. The configured independent approver set must cover the exact required purposes for enrollment, entitlement bootstrap, elevation, credential recovery, harness revocation, and relationship acceptance. Missing purpose coverage blocks configuration; do not collapse these ceremonies into a generic approval.

## PostgreSQL and durability

The always-on profile requires PostgreSQL and verifies schema plus writable-primary durability settings. PostgreSQL 18.4 is retained local evidence and a research reference—not a permanent product dependency. A different supported version needs its own compatibility evidence and must not inherit the 18.4 evidence claim.

A single PostgreSQL process can support local custody testing but does not prove HA, failure-domain separation, PITR, restore, capacity, or production RPO/RTO.

## Secrets

AgentNet requires credentials to be injected securely at runtime. It does not require Infisical. Operators may choose an approved secret manager or private secret-file mechanism. Never serialize injected DSNs, passwords, private keys, or reusable approval material into configuration output or chat.

## Core versus supervisor configuration

Use the core config with core/server commands. After exact enrollment and
before service start, activate the ordinary server-agent binding explicitly:

```bash
agentnet server-agent activate --config agentnet.json --identity .agentnet/server-agent-identity.json
agentnet status --config agentnet.json
agentnet serve --config agentnet.json --host 127.0.0.1 --port 8080
agentnet bootstrap-server-agent --config agentnet.json
```

Activation must run while the service is offline. It holds the exact runtime
lease under a distinct activation owner, runs no migrations, checks the current
credential and private key against PostgreSQL, and changes only enrollment
labels. It never creates entitlement, capability, route, or service authority.

Use the separate supervisor config with:

```bash
agentnet supervisor-run --config agentnet-supervisor.json --check
```

Do not pass the core `agentnet.json` to `supervisor-run`.

## Pi binding

Pi binding is not ambient. It requires:

- an enrolled current harness and credential;
- enabled core local bindings and an explicit `local_binding` capability limit;
- owner-only capability-root material;
- private Unix-socket path;
- supervisor config with `local_bindings_required=true`;
- exact pinned Pi version and a measured supervisor-launched child.

The supervisor delivers the capability after launch through a private channel. It is never a command-line, MCP, A2A, or caller-supplied bearer.

## Task custody versus execution

An in-scope downward assignment may enter `accepted_queued` custody, but generic mailbox, conversation, supervisor, and worker reads withhold task payload bytes. The current build has no protected TaskGrant payload-release route, so task execution remains unavailable even when a TaskGrant object exists.

Do not suggest an `include_payload` flag or generic-read bypass. A future release route must require the exact current TaskGrant, source/sink/data/tool/effect authorization, audit-before-release ordering, and adversarial tests. Until shipped, report **blocked: protected TaskGrant payload-release component not yet shipped**.

## A2A boundary

Existing A2A traffic may coordinate rollout, but A2A receipts do not prove AgentNet identity, enrollment, delivery, processing, or effect completion. Public A2A peers remain external and low trust until explicitly admitted and authorized.

## Production claims

Package installation never proves production certification. Check the installed release's `docs/GATE_EVIDENCE.md`, `REQUIREMENTS_STATUS.md`, and retained evidence. Do not claim production readiness without the applicable external and owner gates, including identity authority, key custody, PostgreSQL recovery topology, artifact safety, exact harness isolation, audit roots, updates/provenance, and owner policy decisions.

When evidence is missing, say **blocked**. Never downgrade the requirement to make a command succeed.
