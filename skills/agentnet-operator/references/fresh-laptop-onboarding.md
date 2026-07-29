# Fresh-laptop onboarding contract

Use this reference when a human needs to enroll a newly installed laptop agent into AgentNet. This is a reusable product workflow, not a device-specific workaround.

## Starting state

Assume the laptop has only:

- a supported operating system;
- a newly installed generic agent or harness;
- a human who can paste one instruction packet and respond to local browser or installer prompts.

Do not assume AgentNet, A2A, Pi extensions, Node.js, `uv`, a secret manager, private-repository access, credentials, keys, configuration files, or technical operator knowledge.

## Human channel boundary

An authorized administrator or ordinary enrolled server agent gives the laptop human one self-contained **public bootstrap packet** through an authenticated human channel. The human pastes it once into the new agent.

The unconnected laptop has no agent inbox. No ordinary server agent or A2A peer can message it directly. A2A is not a bootstrap dependency, enrollment transport, identity proof, or fallback private-artifact channel.

The packet contains public instructions only. It never contains private keys, secret values, join state, OAuth callback data, approval capabilities, signed approval receipts, bearer credentials, cookies, identity profiles, or private host paths.

The exact waiting AgentNet process retains private Core flow state. Core derives a transaction-specific Approval possession secret, sends only its SHA-256 binding when creating the Approval request, and retrieves with that purpose-separated secret after passkey approval through signed Core↔Approval broker. Browser and human receive no receipt, approval code, possession secret, broker capability, or private URL. No extra person, host, relay, second report channel, key, receipt, file, URL, continuation, challenge, or identity-state transfer is required.

If AgentNet is already installed, the receiving agent loads `agentnet-operator` and follows this contract. If it is absent, the single packet remains self-contained through prerequisites and public npm installation; after installation the receiving agent reloads the bundled skill when supported, then continues the same packet. The packet cannot assume the skill exists before installation.

## Human roles

### Laptop human

- pastes the single packet once;
- approves official prerequisite installers only when a prerequisite is missing;
- completes system-browser Google sign-in and consent;
- waits while AgentNet automatically receives the passkey-approved result;
- sends only the bounded completion status allowed by the packet.

### Human approver

- may be the same owner as the laptop human and administrator;
- uses an owner-controlled browser/passkey that the enrolling harness cannot read or automate;
- reviews the exact human, domain, harness, candidate-key thumbprint, purpose, transaction digest, and expiry;
- completes WebAuthn user verification;
- sees a clear confirmation that AgentNet will continue automatically;
- never receives or sends a signed receipt, approval code, possession secret, approval capability, private URL, key, or file.

### Administrator

- grants no authority during enrollment;
- after identity-only enrollment, obtains the exact principal and two harness bindings only through authenticated Core state;
- may initiate C0 authority only when the installed release ships the fixed, one-use, WebAuthn-approved `BootstrapGrantPlan` that atomically commits five communication and five exact-revoke entitlements plus the deny-only guard;
- never assembles the pilot with generic entitlement issuance, three independent grants, the legacy founder ceremony, or the beneficiary identity file/private key.

## Shared-skill roles

### Sender, current manager, or ordinary server agent

1. Resolve and verify the intended human, laptop/harness name, domain, Core, approval service, OIDC values, exact release, approver, test recipient, package integrity, and C0 scope from approved server/package metadata. Do not ask the human for hostnames, callbacks, hashes, identifiers, or configuration values.
2. Verify the release's public package metadata, OS/CPU/Node/`uv` support, integrity, and actual CLI surface.
3. Verify the full selected flow exists in that release. Identity-only onboarding and C0 messaging are separate gates.
4. Resolve every sender placeholder in the canonical example from approved public metadata.
5. Send the resolved example unchanged as one packet. Do not split, shorten, paraphrase, or add follow-up commands.
6. Never claim delivery to the new agent until the human confirms the one paste occurred.

### Fresh-laptop agent

1. Treat the pasted packet as complete starting context; assume no hidden skill, file, credential, or network membership.
2. Explain each unavoidable human action plainly and perform every safe automated check itself.
3. Verify each phase before continuing.
4. Keep all generated private state owner-only and local.
5. Stop on any version, integrity, origin, identity, approval, expiry, authority, or capability mismatch. Report only the bounded public blocker.
6. Do not claim onboarding success until the selected identity-only or C0 acceptance criteria are proven.

## Required bootstrap packet

A valid packet selects exactly `identity_only` or `c0_pilot` and covers every phase required by that mode. Missing phases block issuance.

### 1. Scope and safety

- exact selected mode: `identity_only` or release-gated `c0_pilot`;
- intended human and laptop/harness display name;
- exact AgentNet domain, canonical HTTPS Core, approval, issuer, and callback origins;
- installation-is-code-only and enrollment-is-identity-only statements;
- public/private boundaries and exact stop conditions;
- existing A2A and communications remain unchanged.

### 2. Prerequisites

- supported OS and architecture;
- exact minimum Node.js and `uv` versions;
- official public prerequisite sources;
- local version checks and safe remediation.

### 3. Public package installation

- exact public package and immutable version;
- registry integrity plus OS/CPU/Node checks;
- pinned install command;
- version/help verification and stop conditions.

### 4. System-browser Google OIDC and guided join

- exact `agentnet join guided` command;
- owner-only candidate key/state;
- system-browser Authorization Code + PKCE;
- Core-brokered WebAuthn human approval independent of the enrolling harness;
- possession-bound automatic receipt delivery to the exact waiting process;
- resumable/idempotent completion ending at `enrolled_identity_only` without a TTY prompt.

### 5. WebAuthn human approval

- the owner reviews the exact transaction on an owner-controlled browser/passkey outside the enrolling harness;
- explicit WebAuthn user verification is required;
- the default server may host approval under a distinct OS identity and must report `independent_boundary_proven=false`;
- the browser displays only automatic-delivery status and no receipt or approval code;
- receipt, possession secret, capability, URL, key, identity, and continuation state never move between machines or enter a human channel.

### 6. Separate bounded C0 authority

Mode `identity_only` ends after guided enrollment and reports `first_message_blocked_explicit_authority_required`; it requests no C0 approval and runs no C0 command. Enrollment never grants messaging authority. C0 may continue only when the installed release exposes the approved fixed `BootstrapGrantPlan` profile and all of its runtime guards. Core—not the browser, prompt, or caller—resolves the same principal's exact owner/fresh harness pair, credentials, epochs, five communication entitlements, five entitlement-specific revoke powers, C0 payloads, event lineage, mailbox ownership, expiry, and one-use limits. One WebAuthn-approved transaction commits all ten entitlement rows plus matching plan/guard records or none.

Generic `agentnet admin entitlement issue`, principal-ID grant issuance, three-grant assembly, the legacy founder ceremony, wildcards, and partial repair are forbidden fallbacks for this pilot. If the installed release lacks the complete bounded-plan path, report `first_message_blocked_explicit_authority_required` and stop identity-only.

For a release that passes that gate, the owner first validates and runs only
`agentnet supervisor-run --config agentnet-supervisor.json
--c0-pilot-responder`. The fresh harness uses only
`agentnet bootstrap-plan begin|status|complete` with its local identity/state,
then `agentnet c0-pilot start|status|complete` with its local identity. The owner
reviews the fixed five-communication/five-exact-revoke WebAuthn summary and the
waiting process automatically retrieves the result with its private begin state.
No caller selects a plan, peer, direction, payload, event,
acknowledgement, digest, receipt, entitlement, or use count.

`waiting_owner` and `waiting_fresh` are resumable sanitized stages. `expired`
and `invalidated` are terminal; added active same-principal harness/credential
state permanently invalidates the guard and later removal cannot revive it.
Only `COMPLETED_C0_ROUND_TRIP` proves all seven issuer-owned facts plus exact
five-communication-power cleanup. Transport ACK, prose, status, or a stored fact
row alone never proves completion.

The recorded ordinary PD-001/PD-002 defaults permit automatic possession-bound delivery only through the signed broker to the exact waiting process. Principal/harness identifiers remain inside authenticated Core operations; the human relays none.

### 7. Fixed C0 round-trip verification

This section applies only to selected mode `c0_pilot`. The dedicated C0 service
owns both fixed harmless payloads, recipients, event selection, acknowledgement
targets, digests, receipts, lineage, and use counts. The fresh agent uses only
`agentnet c0-pilot start|status|complete`; it writes no message file and invokes
no generic message, inbox, acknowledgement, or entitlement command.

Success requires request custody, owner retrieval, owner exact acknowledgement,
fixed causal reply, reply custody, fresh retrieval, and final exact
acknowledgement as seven distinct issuer-owned facts. Existing A2A remains
unchanged.

### 8. Recovery and reporting

- browser cancellation, network loss, local timeout, duplicate paste, restart, and response loss resume the exact owner-only nonterminal state without a second identity;
- server-confirmed `expired` or `failed` guided state is terminal: never delete it manually; rerun the exact command with `--replace-terminal-state`, which refuses absent/completed/nonterminal state, reuses the same candidate key, and starts a fresh OIDC transaction without creating an identity;
- no private artifact is moved between systems;
- public completion report contains only owner-approved fields;
- principal/harness identifiers remain inside authenticated Core/Manager operations and are omitted from the public completion report.

## Canonical public onboarding prompt example

The source of truth for the single fresh-laptop onboarding prompt is:

[`references/examples/fresh-laptop-single-prompt.md`](examples/fresh-laptop-single-prompt.md)

The sender must read this contract, resolve every required placeholder in that example, and issue the resulting packet unchanged. Any unresolved required placeholder blocks issuance. The example remains public and reusable; runtime identifiers may be taken only from safe local AgentNet output and handled according to the approved reporting policy.

## Installed-release gate

Never infer capability from documentation written for another release. Check the installed release's actual CLI/help and tested endpoints before issuing a live packet.

A release passes the identity-only gate only when `agentnet join guided`:

- opens the system browser without printing its private authorization URL;
- keeps key, continuation, challenge, callback, possession, and receipt state owner-only;
- returns the receipt only to the exact waiting process through the signed broker, without a TTY prompt;
- reports safe output field `approval_delivery: automatic_possession_bound_signed_broker`, which is the operator-checkable proof that this release did not use a claim-code handoff;
- resumes safely after local timeout or response loss, and offers explicit `--replace-terminal-state` recovery only after Core proves `expired` or `failed`;
- returns `enrolled_identity_only`, `authority_granted: false`, and `first_message_blocked_explicit_authority_required`.

A release passes the C0 gate only when it additionally ships the fixed atomic `BootstrapGrantPlan`, purpose-specific WebAuthn summary, deny-only runtime guard, deterministic owner-harness responder, exact seven-fact verifier, and immediate communication-entitlement cleanup. Generic principal-ID entitlement issuance does not pass this gate. Real Core/Approval/PostgreSQL/two-harness infrastructure must also be proven.

AgentNet `0.1.9` is a compatibility-only guided flow. It keeps authorization, continuation, candidate key, and signed receipt out of chat, but its browser ceremony gives the human one short-lived claim code to enter only into the exact waiting local guided process. It ends identity-only and grants no communication authority. It does **not** pass the current automatic signed-broker delivery gate and must not be presented as the current no-transfer flow.

AgentNet `0.1.8` fails this gate:

- `join begin` prints an authorization URL and writes pending state;
- `join complete` requires local challenge and approval files;
- no product-owned possession-bound approval-receipt handoff exists.

For `0.1.8`, report **blocked: product component not yet shipped** and stop before `join begin`. Never substitute Slack, A2A, chat, prompt, log, repository, copy/paste, USB, QR, or custom glue.

## Pilot acceptance

Identity-only pilot success requires one paste, a fresh laptop unable to read or automate the approval authenticator, real workforce OIDC, real WebAuthn UV, no leaked private state, safe idempotent rerun, and no implicit authority. Default server colocation retains `independent_boundary_proven=false`; separate approval hosting is optional high assurance.

Full C0 success additionally requires one committed fixed `BootstrapGrantPlan`, exact guard confirmation, original durable custody, owner retrieval, owner acknowledgement, correlated reply send, reply durable custody, fresh retrieval, and final acknowledgement as the seven approved facts. A mock/local run, generic grant assembly, or transport ACK is not this proof.
