---
name: agentnet-operator
description: Safely install, initialize, enroll, verify, configure, operate, reset, recover, and troubleshoot AgentNet. Use for AgentNet, the `agentnet` CLI, `@misunders2d/agentnet`, human-mediated fresh-laptop bootstrap, expired laptop credential reauthorization, enrollment or authority, Pi bindings, server-agent deployment, or destructive package-owned server reset/reinstall.
license: Apache-2.0
compatibility: Bundled AgentNet npm/Pi package on Linux, macOS, and Windows local profiles; production deployment remains Linux-first. Follow the installed release's exact Node.js, uv, and Python requirements.
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
8. **Expired laptop credential** — package-owned same-binding reauthorization with the retained owner-private identity and key.
9. **Server reset or recovery** — destructive package-owned cleanup, retained external prerequisites, interrupted setup/upgrade, and clean reinstall.
10. **Troubleshooting** — distinguish package, config, identity, storage, binding, and evidence failures.

## Enforce the exact product contract

The authoritative scope is the stable requirement set in [`../../docs/requirements.md`](../../docs/requirements.md), interpreted by [`../../docs/specification.md`](../../docs/specification.md) and the current evidence ledgers. Do not invent a smaller communication product, an extra privileged Hub product, or capabilities outside those requirements.

“Install and use” means AgentNet must ship or explicitly provision the maintained components, adapters, manifests, and preflight checks needed by its supported profile. The operator may supply hosts, secrets, policy decisions, trust roots, and required human ceremonies; the operator must not have to write integration code, build an approval application, design receipt formats, or manually assemble undocumented infrastructure.

Until a required product component is shipped and its applicable gate passes, report **blocked: product component not yet shipped**. Never offer local synthetic C0, same-boundary approval, or communication-only mode as proof of full AgentNet, artifact support, FILE/G13, or production/ship readiness. Communication-only is eligible only when the user explicitly selects the shipped restricted ordinary-server profile to test messaging/task custody while artifact release remains disabled fail-closed.

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

- **Sender/current manager/ordinary server agent:** give the human one self-contained, public, copyable bootstrap packet through an authenticated human channel. Do not try to contact the unconnected laptop as an agent.
- **Fresh-laptop agent:** when the human pastes that packet, guide them step by step from prerequisites and exact public package installation through the maximum phase the installed release safely supports. Enrollment always ends identity-only. A first AgentNet-native message is eligible only when that exact installed release ships and has passed the complete bounded-plan/responder/verifier gate. Explain where each command runs, expected safe output, and exact stop conditions.

Before producing or following a live packet, read [the fresh-laptop onboarding contract](references/fresh-laptop-onboarding.md) and its [canonical single-paste example](references/examples/fresh-laptop-single-prompt.md). Resolve and verify every required placeholder in the example from approved public metadata, then issue the resulting packet unchanged rather than splitting, handcrafting, shortening, or paraphrasing it. Any unresolved required placeholder blocks issuance. `SKILL.md` contains routing only; the canonical prompt belongs exclusively in the example file. Verify the installed release actually ships every selected phase. The packet must cover the exact public installation source/version, OS/CPU/Node/`uv` prerequisites, install verification, guided join, system-browser Google OIDC, WebAuthn human approval, identity-only completion, expected outputs, and safe recovery. Include C0 authority and verification only after the installed release proves the complete fixed `BootstrapGrantPlan`, deterministic responder, and seven-fact verifier. Until then, the canonical packet stops identity-only; never substitute generic entitlement issuance. Sender or ordinary server agent resolves package integrity, origins, callbacks, administrator/recipient metadata, and runtime identifiers; do not interrogate the human for them.

Keep public instructions separate from local/private material. Private keys, join state, callback codes or challenges, identity profiles, approval capabilities, signed approval receipts, tokens, and secret values never belong in Slack, A2A, chat, prompts, logs, repositories, or the copied bootstrap packet.

### Release-gated bounded C0 phase

When the installed release exposes `bootstrap-plan begin|status|complete`,
`c0-pilot start|status|complete`, and dedicated `c0-pilot responder`, read the
C0 sections of the onboarding reference and canonical example before proceeding.
Ordinary-server setup—not a generic supervisor flag—must already own and run the
responder under the exact managed owner identity.
The fresh harness may then request the fixed plan, the owner WebAuthn-approves
its purpose-specific summary, and the exact waiting process retrieves the result
automatically through the signed broker using its private begin state.

Call only the fixed commands; never use generic message, inbox, ACK, authority
inventory, or entitlement mutation commands for this pilot. The C0 commands
accept no peer, plan, payload, event, receipt, digest, entitlement, or use-count
selectors. `waiting_owner` and `waiting_fresh` are resumable stages;
`invalidated` and `expired` are terminal. Report success only for
`COMPLETED_C0_ROUND_TRIP`, after the service verifies seven facts and atomically
revokes exactly five communication powers. Never expose protected evidence or
promote `accepted_local` to production durability.

If the installed release lacks a product-owned secure handoff for any private enrollment artifact—especially the signed approval receipt—report **blocked: product component not yet shipped** and stop before `join begin`. Do not invent Slack/A2A transfer, copy/paste, USB/QR choreography, custom glue, or a one-click link. AgentNet `0.1.8` has `join begin`/`join complete` but no supported possession-bound approval-receipt handoff, so fresh cross-host enrollment is blocked on that release.

For a later installed release whose actual `agentnet join --help` exposes
`join guided`, use only that product flow for a nontechnical fresh laptop. It
opens the system browser without printing the authorization URL, keeps
continuation/challenge/key/possession state owner-only, and automatically receives
the passkey-approved result without a TTY prompt or browser value transfer. For a
headless server, its local AgentNet manager uses `join guided --browser remote`,
then tells the owner only to open the fixed public Core `/activate` page. Owner
signs in and passkey-approves entirely in normal browser. Never reveal or transfer
a private authorization URL, claim code, receipt, continuation, or broker secret.
Resume nonterminal state with exact command. Only after Core proves `expired` or
`failed`, use `--replace-terminal-state`; never delete state or replace
absent/completed/nonterminal/drifted state, and preserve candidate key. Success
means `enrolled_identity_only`, not messaging readiness. Stop at
`first_message_blocked_explicit_authority_required` unless the installed
release's complete bounded C0 gate is verified and the canonical packet selects
that phase. Generic `authorization.entitlement.issue`, the legacy founder
ceremony, or three independent grants are not substitutes; never turn enrollment
into implicit or partially assembled authority.

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

### Expired laptop credential

When Core or the CLI gives the exact expired-laptop reauthorization instruction,
read the canonical [safe commands](references/safe-commands.md) entry and use
only its package-owned laptop workflow. Never substitute `join guided`,
enrollment, generic recovery, active renewal, key rotation, or the root-only
managed-server workflow. The safe-command reference owns all flags, paths,
states, retry rules, preserved fields, and rejection conditions. Laptop
reauthorization remains owner-local; managed-server reauthorization remains a
separate root/server-manager operation. Any state that does not match the exact
retained expired binding fails closed.

### Real server-agent network

Read [product-owned ordinary Linux server setup](references/ordinary-server-setup.md) and [safe commands](references/safe-commands.md) before any host change. Those references own exact request schemas, commands, runtime pins, PostgreSQL peer contract, digest/marker versions, secret syntax, systemd profile, activation sequence, recovery, and reset command surface. Do not duplicate or improvise them here.

Target server's coding agent owns local execution. Remote Managers may provide immutable public package instructions and inspect sanitized evidence only; they must not shell into host or send bespoke user/directory/unit/systemctl choreography. Use only resolved absolute root-owned AgentNet launcher and fixed `server-agent setup` flow: read-only plan first, then one frozen exact-digest apply. Never replace it with manual identities, directories, units, markers, network creation, Approval provisioning, or service assembly.

Select artifact mode before request creation. Communication-only requires request-v2 `artifact_mode=disabled`, no scanner input, and only `offline_custody`; artifact state/bindings stay unavailable. Setup registers no identity and grants no authority. Missing prerequisite reports one named **blocked** state; never substitute root access, payload identity, chat/A2A/Slack claims, synthetic actors, or model output.

After `waiting_owner_oidc_or_passkey`, follow only reference's browser-only owner registration, server-local `join guided --browser remote`, fixed public Core `/activate`, offline activation, and exact setup rerun. Human uses no SSH, `sudo`, server terminal/path, private authorization URL, claim code, receipt, continuation, or broker secret. Success requires `operational`, `identity_enrolled=true`, verified public Core readiness, and `authority_granted=false`.

For `server-agent reset`, treat request as destructive server-manager-only recovery. Read exact reset entry in safe commands and ordinary-server reference. Require explicit approval for both confirmation flags. Reset must retain PostgreSQL, runtimes, proxy/TLS, operator config, and service identities; preserve permanent root-only coordination lock; refuse unknown custody; and never be presented as secret rotation. No browser prompt or fresh-laptop packet may contain reset.

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
- communication-only `artifact_mode=disabled` vs scanner-backed artifact support; disabled mode creates no scanner/artifact key and proves no FILE/G13 outcome;
- artifact promotion into `quarantined` vs independent scanner attestation and policy release;
- released-artifact download vs task-grant payload/data/effect authority.

## Troubleshoot without weakening gates

- **Command/package failure:** check `agentnet --version`, `agentnet --help`, `command -v agentnet`, the npm prefix, and the installed package version.
- **Skill/package discovery failure:** run the package check and inspect Pi skill-loader diagnostics; do not copy skill files into unrelated global directories as a workaround.
- **Core config failure:** run `agentnet status --config agentnet.json` and preserve its named blocker.
- **Supervisor/Pi binding failure:** run `agentnet supervisor-run --config agentnet-supervisor.json --check`; never fall back to ambient tools or a foreground process.
- **Identity/enrollment failure:** distinguish OIDC endpoint validation, proof of possession, approval-service config/catalog/key custody, registration, RP/origin/UV/challenge verification, exact transaction/purpose/digest, receipt expiry/replay, credential revocation, and the selected deployment profile's evidence. Use `agentnet approval status`; never substitute synthetic identity, enrolling-harness control, or chat approval. Do not reject default server colocation merely because it is not the optional high-assurance tier.
- **Expired laptop credential:** use the dedicated workflow above and its canonical safe-command reference; never improvise state repair.
- **Expired managed-server credential:** keep Core stopped and use only the root package-owned `server-agent reauthorize-expired-credential` provenance/journal workflow; never substitute the laptop command or restart automatically.
- **Storage/durability failure:** report the actual backend/readiness evidence and state vocabulary; never promote `accepted_local` to durable acceptance.
- **Artifact failure:** distinguish reservation, exact byte upload, quarantine promotion, scan, release, capability issuance, and single-use download. Never pass bytes/base64 or arbitrary host paths through model-visible tools, print capabilities/private object keys, or treat quarantine as availability.
- **Task execution failure:** distinguish `accepted_queued` custody from exact recipient-owned `task.process` authorization, durable local queue custody, protected payload release, and separately authorized result/effect handling. Generic reads stay redacted. If installed release predates schema migration 2, report a version/component gap; never add an `include_payload` workaround.

Do not print secrets, private keys, credential-bearing DSNs, reusable approval material, or private report URLs while troubleshooting.

## Use references

- Read [product-owned ordinary Linux server setup](references/ordinary-server-setup.md), the unchanged [scanner-backed request-v1 example](references/examples/ordinary-server-setup-request.json), and the restricted [communication-only request-v2 example](references/examples/ordinary-server-communication-only-setup-request.json) before selecting an always-on profile.
- Read [the fresh-laptop onboarding contract](references/fresh-laptop-onboarding.md) and issue only the resolved [single-paste example](references/examples/fresh-laptop-single-prompt.md) for human-copyable bootstrap instructions.
- Read [required communication scope](references/required-communication-scope.md) before judging installation or network readiness; one direct-message round trip is not full requirement coverage.
- Read [safe commands](references/safe-commands.md) for installation, local examples, package checks, supervisor validation, expired laptop credential reauthorization, and server preflight.
- Read [fail-closed boundaries](references/fail-closed-boundaries.md) before identity, authority, server, Pi binding, secrets, or production-readiness work.

## Report clearly

Return:

1. **Scope** — what the user is trying to run.
2. **Status** — ready, blocked, or completed.
3. **Commands/actions** — exact and minimal.
4. **Evidence** — what was actually verified.
5. **Limits** — what the evidence does not prove.
6. **Next human decision** — only when truly required.
