---
name: agentnet-operator
description: Safely install, initialize, verify, configure, operate, and troubleshoot AgentNet across Pi, npm, local-conformance, supervisor/local-binding, and always-on server-agent workflows. Use for AgentNet-related setup, enrollment, networking, messaging, package validation, Pi binding, server deployment, PostgreSQL configuration, identity prerequisites, or readiness—even if the user does not explicitly name this skill.
license: Apache-2.0
compatibility: Bundled AgentNet npm/Pi package on Linux; follow the installed release's exact Node.js, uv, and Python requirements.
---

# AgentNet Operator

Use the smallest safe workflow matching the request. AgentNet installation is code installation only; it never establishes identity, authority, enrollment, bindings, or a network by itself.

## Classify the request

1. **Install or package** — npm/Pi installation, PATH, `uv`, package verification.
2. **Local conformance** — demos, local initialization, synthetic C0 mechanics, `accepted_local`.
3. **Harness readiness** — exact version probes and diagnostic-only single-harness probes.
4. **Supervisor or Pi binding** — separate supervisor config, measured child, private Unix IPC.
5. **Always-on server agent** — PostgreSQL, HTTPS, secret injection, identity and recovery prerequisites.
6. **Enrollment or authority** — workforce OIDC, proof of possession, independent WebAuthn/OOB approval, explicit grants.
7. **Troubleshooting** — distinguish package, config, identity, storage, binding, and evidence failures.

## Enforce the exact product contract

The authoritative scope is the stable requirement set in `docs/requirements.md`, interpreted by `docs/specification.md` and the current evidence ledgers. Do not invent a smaller communication product, an extra privileged Hub product, or capabilities outside those requirements.

“Install and use” means AgentNet must ship or explicitly provision the maintained components, adapters, manifests, and preflight checks needed by its supported profile. The operator may supply hosts, secrets, policy decisions, trust roots, and required human ceremonies; the operator must not have to write integration code, build an approval application, design receipt formats, or manually assemble undocumented infrastructure.

Until a required product component is shipped and its applicable gate passes, report **blocked: product component not yet shipped**. Never offer local synthetic C0, same-boundary approval, or a reduced communication subset as the product substitute.

## Start with read-only checks

Before changing state, identify the installed version, requested profile, existing configuration, and whether the request is only a demo or a real network. Prefer:

```bash
agentnet --version
agentnet --help
uv --version
agentnet status --config agentnet.json
agentnet harness-probe --harness pi --data-dir /tmp/agentnet-pi-probe
```

A single-harness probe is diagnostic only. It cannot prove complete four-harness or production readiness.

## Choose the correct workflow

### Install or local evaluation

Read [safe commands](references/safe-commands.md). Local demos and `agentnet init` may use synthetic identities and weaker local acceptance only. State that limitation plainly.

### Pi local binding

Read [fail-closed boundaries](references/fail-closed-boundaries.md) before suggesting activation. The core config and supervisor config are different files. Validate the supervisor config with:

```bash
agentnet supervisor-run --config agentnet-supervisor.json --check
```

A normal foreground Pi process does not receive AgentNet tools merely because the package is installed. Activation requires `local_bindings_required=true` in the separate supervisor configuration plus an enrolled, measured supervisor-launched child.

### Real server-agent network

Do not initialize or enroll until all required inputs exist:

- supported PostgreSQL with verified durability settings and recovery plan;
- securely injected database credentials;
- dedicated HTTPS/TLS endpoint and exact service audience;
- workforce OIDC provider;
- independently controlled WebAuthn user-verification approval authority;
- per-harness keys, proof of possession, revocation and recovery procedures;
- explicit capabilities, policies, retention, and evidence appropriate to enabled features.

The approval ceremony service, deployment manifests, and required adapters are AgentNet product components. The installer must deploy/configure them from pinned maintained mechanisms; the operator supplies approved infrastructure, secrets, and human decisions—not custom integration code. Until that installer path exists and passes its gates, do not normalize a manual Keycloak/WebAuthn/PostgreSQL/object-store assembly guide as the supported product experience.

If any prerequisite is missing, report **blocked** and name it. Never replace real identity with payload fields, chat claims, A2A receipts, Slack messages, synthetic actors, or model output.

After exact enrollment, an ordinary always-on process still needs explicit
offline deployment binding:

```bash
agentnet server-agent activate --config agentnet.json --identity .agentnet/server-agent-identity.json
```

This command must acquire the configured runtime lease under a distinct owner,
verify the same PostgreSQL credential and owner-only private key, and change
only the enrolled harness/credential labels. It never grants authority or
restarts the service. A live process, stale/retired credential, key mismatch,
or different prior binding blocks activation.

## Preserve evidence boundaries

Always distinguish:

- installed vs initialized;
- initialized vs enrolled;
- submitted vs accepted into custody;
- accepted custody vs recipient `recipient_committed` acknowledgement;
- `recipient_committed` vs presentation, processing, obligation progress, or effect;
- `accepted_local` vs production durability;
- diagnostic harness detection vs external conformance;
- transport/A2A acknowledgement vs AgentNet-native processing or business effect;
- local synthetic C0 mechanics vs real cross-host communication;
- transport/task custody (`accepted_queued`) vs authorized payload release and execution.

## Troubleshoot without weakening gates

- **Command/package failure:** check `agentnet --version`, `agentnet --help`, `command -v agentnet`, the npm prefix, and the installed package version.
- **Skill/package discovery failure:** run the package check and inspect Pi skill-loader diagnostics; do not copy skill files into unrelated global directories as a workaround.
- **Core config failure:** run `agentnet status --config agentnet.json` and preserve its named blocker.
- **Supervisor/Pi binding failure:** run `agentnet supervisor-run --config agentnet-supervisor.json --check`; never fall back to ambient tools or a foreground process.
- **Identity/enrollment failure:** distinguish OIDC endpoint validation, proof of possession, independent approval, expiry, replay, and revocation. Never substitute synthetic identity or chat approval.
- **Storage/durability failure:** report the actual backend/readiness evidence and state vocabulary; never promote `accepted_local` to durable acceptance.
- **Task execution failure:** distinguish `accepted_queued` custody from the currently missing protected TaskGrant payload-release route.

Do not print secrets, private keys, credential-bearing DSNs, reusable approval material, or private report URLs while troubleshooting.

## Use references

- Read [required communication scope](references/required-communication-scope.md) before judging installation or network readiness; one direct-message round trip is not full requirement coverage.
- Read [safe commands](references/safe-commands.md) for installation, local examples, package checks, supervisor validation, and server preflight.
- Read [fail-closed boundaries](references/fail-closed-boundaries.md) before identity, authority, server, Pi binding, secrets, or production-readiness work.

## Report clearly

Return:

1. **Scope** — what the user is trying to run.
2. **Status** — ready, blocked, or completed.
3. **Commands/actions** — exact and minimal.
4. **Evidence** — what was actually verified.
5. **Limits** — what the evidence does not prove.
6. **Next human decision** — only when truly required.
