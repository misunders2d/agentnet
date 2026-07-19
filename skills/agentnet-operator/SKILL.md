---
name: agentnet-operator
description: Safely install, initialize, enroll, verify, configure, operate, and troubleshoot AgentNet. Use for AgentNet, the `agentnet` CLI, `@misunders2d/agentnet`, human-mediated fresh-laptop bootstrap, enrollment or authority, Pi bindings, or server-agent deployment.
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
6. **Enrollment or authority** — workforce OIDC, proof of possession, WebAuthn/OOB human approval independent of the enrolling harness, explicit grants.
7. **Fresh-laptop bootstrap** — one complete public instruction packet for a human to paste into a newly installed generic agent with no prior AgentNet/A2A knowledge.
8. **Troubleshooting** — distinguish package, config, identity, storage, binding, and evidence failures.

## Enforce the exact product contract

The authoritative scope is the stable requirement set in [`../../docs/requirements.md`](../../docs/requirements.md), interpreted by [`../../docs/specification.md`](../../docs/specification.md) and the current evidence ledgers. Do not invent a smaller communication product, an extra privileged Hub product, or capabilities outside those requirements.

“Install and use” means AgentNet must ship or explicitly provision the maintained components, adapters, manifests, and preflight checks needed by its supported profile. The operator may supply hosts, secrets, policy decisions, trust roots, and required human ceremonies; the operator must not have to write integration code, build an approval application, design receipt formats, or manually assemble undocumented infrastructure.

Until a required product component is shipped and its applicable gate passes, report **blocked: product component not yet shipped**. Never offer local synthetic C0, same-boundary approval, or a reduced communication subset as the product substitute.

## Fresh-laptop onboarding is human-mediated

Treat this as one reusable product flow for any fresh laptop—not a device-specific workaround. A fresh laptop may have only a newly installed generic agent and a human manager. Never assume it already has AgentNet, A2A, Pi extensions, Node.js, `uv`, a secret manager, private-repository access, credentials, or technical operator knowledge.

Default ordinary onboarding uses the existing server for Core, PostgreSQL, and
approval under distinct OS identities; the current owner laptop supplies the
browser/passkey; the fresh laptop receives one complete prompt. Do not require
an extra approval host, extra person, Infisical or another named secret manager,
per-command setup approvals, or technical values from the human that AgentNet
can resolve. Separate approval hosting is an optional high-assurance profile.
Routine setup uses one frozen approval; ask again only for a materially changed
scope or a new destructive, restart, privilege-expanding, or high-risk action.

The same shared skill serves two roles:

- **Sender/Hub/current manager:** give the human one self-contained, public, copyable bootstrap packet through an authenticated human channel. Do not try to contact the unconnected laptop as an agent.
- **Fresh-laptop agent:** when the human pastes that packet, guide them step by step from prerequisites and exact public package installation through enrollment and the first verified AgentNet-native message. Explain where each command runs, expected safe output, and exact stop conditions.

Before producing or following a live packet, read [the fresh-laptop onboarding contract](references/fresh-laptop-onboarding.md) and its [canonical single-paste example](references/examples/fresh-laptop-single-prompt.md). Resolve and verify every required placeholder in the example from approved public metadata, then issue the resulting packet unchanged rather than splitting, handcrafting, shortening, or paraphrasing it. Any unresolved required placeholder blocks issuance. `SKILL.md` contains routing only; the canonical prompt belongs exclusively in the example file. Verify the installed release actually ships every selected phase. The packet must cover the exact public installation source/version, OS/CPU/Node/`uv` prerequisites, install verification, guided join, system-browser Google OIDC, WebAuthn human approval, identity-only completion, separate exact messaging authority when in scope, C0 verification, expected outputs, and safe recovery. Sender/Hub resolves package integrity, origins, callbacks, administrator/recipient metadata, and runtime identifiers; do not interrogate the human for them.

Keep public instructions separate from local/private material. Private keys, join state, callback codes or challenges, identity profiles, approval capabilities, signed approval receipts, tokens, and secret values never belong in Slack, A2A, chat, prompts, logs, repositories, or the copied bootstrap packet.

If the installed release lacks a product-owned secure handoff for any private enrollment artifact—especially the signed approval receipt—report **blocked: product component not yet shipped** and stop before `join begin`. Do not invent Slack/A2A transfer, copy/paste, USB/QR choreography, custom glue, or a one-click link. AgentNet `0.1.8` has `join begin`/`join complete` but no supported possession-bound approval-receipt handoff, so fresh cross-host enrollment is blocked on that release.

For a later installed release whose actual `agentnet join --help` exposes
`join guided`, use only that product flow for a nontechnical fresh laptop. It
opens the system browser without printing the authorization URL, keeps
continuation/challenge/key state owner-only, and prompts only for the short-lived
human claim code. Success means `enrolled_identity_only`, not messaging
readiness. Stop at `first_message_blocked_explicit_authority_required` until an
authorized administrator issues exact `message.send` plus required
recipient/read entitlements; never turn enrollment into implicit authority.

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
- workforce OIDC provider with exact issuer, callback, endpoint origins, signing algorithms, and token-endpoint authentication method;
- for confidential OIDC, a public `client_secret_env` reference plus a private runtime value; never a secret in config, commands, logs, evidence, or chat;
- owner-controlled WebAuthn user-verification authority that the enrolling harness cannot read or automate;
- per-harness keys, proof of possession, revocation and recovery procedures;
- explicit capabilities, policies, retention, and evidence appropriate to enabled features.

The WebAuthn-UV ceremony service is an AgentNet product component operated through `agentnet approval`; it uses pinned maintained verification and the existing receipt contract. In the default self-hosted profile it may share the existing server with Core/PostgreSQL under a distinct OS identity, credential, storage root, and loopback service that the enrolling harness cannot read or control. The owner approves with a WebAuthn authenticator on the current laptop. This profile reports `independent_boundary_proven=false`; separate administration is optional high assurance. Operators must not be sent through manual extra-host, secret-manager, or per-command setup choreography.

OIDC token-endpoint authentication must be explicit: `none`,
`client_secret_post`, or `client_secret_basic`. Existing public clients default
to `none`. Confidential methods require provider discovery support and a
non-empty runtime secret resolved through `client_secret_env`; method inference
and embedded secret values are forbidden. Google Web applications use the exact
registered HTTPS callback and `client_secret_post` profile documented in the
implementation guide.

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
- transport/task custody (`accepted_queued`) vs authorized payload release and execution;
- artifact promotion into `quarantined` vs independent scanner attestation and policy release;
- released-artifact download vs task-grant payload/data/effect authority.

## Troubleshoot without weakening gates

- **Command/package failure:** check `agentnet --version`, `agentnet --help`, `command -v agentnet`, the npm prefix, and the installed package version.
- **Skill/package discovery failure:** run the package check and inspect Pi skill-loader diagnostics; do not copy skill files into unrelated global directories as a workaround.
- **Core config failure:** run `agentnet status --config agentnet.json` and preserve its named blocker.
- **Supervisor/Pi binding failure:** run `agentnet supervisor-run --config agentnet-supervisor.json --check`; never fall back to ambient tools or a foreground process.
- **Identity/enrollment failure:** distinguish OIDC endpoint validation, proof of possession, approval-service config/catalog/key custody, registration, RP/origin/UV/challenge verification, exact transaction/purpose/digest, receipt expiry/replay, credential revocation, and the selected deployment profile's evidence. Use `agentnet approval status`; never substitute synthetic identity, enrolling-harness control, or chat approval. Do not reject default server colocation merely because it is not the optional high-assurance tier.
- **Storage/durability failure:** report the actual backend/readiness evidence and state vocabulary; never promote `accepted_local` to durable acceptance.
- **Artifact failure:** distinguish reservation, exact byte upload, quarantine promotion, scan, release, capability issuance, and single-use download. Never pass bytes/base64 or arbitrary host paths through model-visible tools, print capabilities/private object keys, or treat quarantine as availability.
- **Task execution failure:** distinguish `accepted_queued` custody from exact recipient-owned `task.process` authorization, durable local queue custody, protected payload release, and separately authorized result/effect handling. Generic reads stay redacted. If installed release predates schema migration 2, report a version/component gap; never add an `include_payload` workaround.

Do not print secrets, private keys, credential-bearing DSNs, reusable approval material, or private report URLs while troubleshooting.

## Use references

- Read [the fresh-laptop onboarding contract](references/fresh-laptop-onboarding.md) and issue only the resolved [single-paste example](references/examples/fresh-laptop-single-prompt.md) for human-copyable bootstrap instructions.
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
