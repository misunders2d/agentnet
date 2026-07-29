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

- verified workforce OIDC identity using exact issuer plus server-staged approved subject or normalized verified-email alias;
- exact harness key and proof of possession;
- WebAuthn user verification/OOB approval authenticated independently of the enrolling harness;
- current credential, domain, policy, and revocation state;
- explicit human-scoped authority and harness attribution.

No payload field, prompt text, claimed email, role name, A2A message, Slack message, or model output grants identity or authority.

The approval ceremony is not enrollment-only. The configured approver set must cover the exact required purposes for enrollment, entitlement bootstrap, elevation, credential recovery, harness revocation, and relationship acceptance. Missing purpose coverage blocks configuration; do not collapse these ceremonies into a generic approval.

The default self-hosted profile may colocate Core, PostgreSQL, and approval on
the existing server under distinct OS identities, credentials, storage roots,
and loopback services. The owner-controlled WebAuthn authenticator remains
outside the enrolling harness. This profile reports
`independent_boundary_proven=false`. Separately administered approval hosting
is an optional high-assurance tier, not an ordinary-onboarding prerequisite.
Normal onboarding must not introduce an extra host, person, named secret
manager, or per-command approval loop.

## PostgreSQL and durability

The always-on profile requires PostgreSQL and verifies schema plus writable-primary durability settings. PostgreSQL 18.4 is retained local evidence and a research reference—not a permanent product dependency. A different supported version needs its own compatibility evidence and must not inherit the 18.4 evidence claim.

A single PostgreSQL process can support local custody testing but does not prove HA, failure-domain separation, PITR, restore, capacity, or production RPO/RTO.

## Secrets

AgentNet requires credentials to be injected securely at runtime. It does not require Infisical. Operators may choose an approved secret manager or private secret-file mechanism. Never serialize injected DSNs, passwords, private keys, or reusable approval material into configuration output or chat.

## Core versus supervisor configuration

Use the core config with core/server commands. The ordinary always-on Linux
profile must follow [ordinary-server-setup.md](ordinary-server-setup.md) only:
dedicated service identities, the resolved absolute root-owned launcher, exact
`/var/lib/agentnet` custody, offline activation, and the approved setup rerun.
Generic per-user `.agentnet` paths and bare PATH-selected launchers are not valid
for that profile.

Activation must run while the service is offline. It holds the exact runtime
lease under a distinct activation owner, runs no migrations, checks the current
credential and private key against PostgreSQL, and changes only enrollment
labels. It never creates entitlement, capability, route, or service authority.

### Public remote activation

Fixed Core `GET /activate` is intentionally unauthenticated and world-reachable for normal browser entry. It accepts no transaction selector or private value. It may redirect only when exactly one unexpired `remote_browser` OIDC transaction exists, is independently rate-limited with its internal activation route, and returns no transaction, state, continuation, receipt, possession, or authority material. OIDC callback must match exact server-staged owner identity. Wrong account returns `activation_wrong_account`, stages nothing, and leaves transaction pending/retryable.

Zero, multiple, expired, rejected, local-browser, malformed, or conflicted state returns `remote_activation_unavailable`. Server Manager resumes exact nonterminal `join guided --browser remote` state; owner retries only fixed `/activate`. Core's 60-poll budget applies only before OIDC callback; callback/Approval polling remains rate-controlled and ends at the fresh challenge expiry. After Core proves the exact continuation `expired` or `failed`, Manager may rerun the exact guided command with `--replace-terminal-state`; the flag must refuse absent, completed, malformed, argument-drifted, or nonterminal state and reuse the same candidate key. Never delete state or send private authorization/callback/approval URL or browser value as recovery.

### Destructive server reset

`server-agent reset` is server-manager-only package recovery. It requires exact explicit destructive approval and both confirmation flags listed in [safe commands](safe-commands.md). It must acquire persistent root-only setup lock before inventory, preserve lock across deletion, reject unknown custody, retain every external prerequisite and service identity, and never appear in browser/fresh-laptop instructions or masquerade as secret rotation.

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

An in-scope downward assignment may enter `accepted_queued` custody, but generic mailbox, conversation, relay, supervisor-reconciliation, and worker-input reads always withhold task payload bytes. Custody alone is never executable.

Protected source builds use one exact recipient-owned supervisor sequence:

1. `authorize` consumes one current event-scoped `task.process` TaskGrant use;
2. the redacted item is durably queued and exact local custody is acknowledged;
3. `payload-release` fresh-checks actor, grant dimensions/revocation/expiry, policy/credential/domain epochs, deadlines/retention, active conflict-free intent, immutable payload/envelope, and provenance;
4. one disclosure receipt and audit record commit before plaintext returns;
5. exact retry rechecks current state and reuses that receipt without another use;
6. result upload requires the committed release receipt.

Do not suggest an `include_payload` flag, caller-selected idempotency key, generic-read bypass, retroactive release after result, or a second grant consumption. Payload release authorizes only exact payload access and semantic processing. It keeps tool and effect authority false; network, budget, credentials, artifacts, protected outputs, and business effects require separately modeled authority. If an installed release predates schema migration 2 or lacks this route, report **blocked: installed AgentNet release lacks protected TaskGrant payload release** rather than weakening the boundary.

## A2A boundary

Existing A2A traffic may coordinate rollout, but A2A receipts do not prove AgentNet identity, enrollment, delivery, processing, or effect completion. Public A2A peers remain external and low trust until explicitly admitted and authorized.

## Production claims

Package installation never proves production certification. Check the installed release's [`../../../docs/GATE_EVIDENCE.md`](../../../docs/GATE_EVIDENCE.md), [`../../../REQUIREMENTS_STATUS.md`](../../../REQUIREMENTS_STATUS.md), and retained evidence. Do not claim production readiness without the applicable external and owner gates, including identity authority, key custody, PostgreSQL recovery topology, artifact safety, exact harness isolation, audit roots, updates/provenance, and owner policy decisions.

When evidence is missing, say **blocked**. Never downgrade the requirement to make a command succeed.
