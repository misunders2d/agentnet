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

The unconnected laptop has no agent inbox. Hub cannot message it directly. A2A is not a bootstrap dependency, enrollment transport, identity proof, or fallback private-artifact channel.

The packet contains public instructions only. It never contains private keys, secret values, join state, OAuth callback data, approval capabilities, signed approval receipts, bearer credentials, cookies, identity profiles, or private host paths.

A short-lived claim code is a distinct enrollment factor, not public packet content. Under the recorded ordinary profile, the owner reads the 128-bit code in the approval UI on the current laptop and types it directly into AgentNet's masked prompt on the fresh laptop. It expires after five minutes and permits at most five failed attempts. No extra person, host, Slack/A2A relay, second report channel, key, receipt, file, URL, continuation, challenge, or identity-state transfer is required. If an organization selects a different human channel, that channel must remain authenticated and unreadable by the enrolling harness.

If AgentNet is already installed, the receiving agent loads `agentnet-operator` and follows this contract. If it is absent, the single packet remains self-contained through prerequisites and public npm installation; after installation the receiving agent reloads the bundled skill when supported, then continues the same packet. The packet cannot assume the skill exists before installation.

## Human roles

### Laptop human

- pastes the single packet once;
- approves official prerequisite installers only when a prerequisite is missing;
- completes system-browser Google sign-in and consent;
- types the short-lived claim code into the masked local prompt;
- sends only the bounded completion status allowed by the packet.

### Human approver

- may be the same owner as the laptop human and administrator;
- uses an owner-controlled browser/passkey that the enrolling harness cannot read or automate;
- reviews the exact human, domain, harness, candidate-key thumbprint, purpose, transaction digest, and expiry;
- completes WebAuthn user verification;
- reads the short-lived code in the approval UI and types it directly into the fresh laptop's masked prompt;
- never sends the signed receipt, approval capability, private URL, key, or file.

### Administrator

- grants no authority during enrollment;
- after identity-only enrollment, obtains the new principal and harness identifiers only through an owner-approved identity-reporting path;
- issues exact current entitlements through the signed, audited administration path;
- does not request or load the new laptop's identity file or private key.

## Shared-skill roles

### Sender, Hub, or current manager

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

A valid packet covers every selected phase below. Missing phases block issuance.

### 1. Scope and safety

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
- masked claim-code input;
- resumable/idempotent completion ending at `enrolled_identity_only`.

### 5. WebAuthn human approval

- the owner reviews the exact transaction on an owner-controlled browser/passkey outside the enrolling harness;
- explicit WebAuthn user verification is required;
- the default server may host approval under a distinct OS identity and must report `independent_boundary_proven=false`;
- the owner transfers only the short-lived code directly to the fresh laptop's masked prompt;
- receipt, capability, URL, key, identity, and continuation state never move between machines or enter a human channel.

### 6. Separate messaging authority

C0 messaging is allowed only when the installed release exposes a safe administrator path that accepts a public principal identifier without loading the beneficiary's private identity state. Required entitlements are exactly:

- `message.send` on `direct`;
- `mailbox.read` on the new laptop's own harness ID;
- `mailbox.acknowledge` on the new laptop's own harness ID.

`recipient authority` is not an AgentNet entitlement. Enrollment never grants any of these.

The recorded ordinary PD-001/PD-002 defaults keep principal/harness identifiers inside authenticated Core/Manager authority operations and permit direct owner transfer of the claim code between approval UI and masked prompt. The human does not relay identifiers or approve three separate grant commands. An authorized server-side administrator may issue the three exact grants under the frozen onboarding plan; inventory must still prove them before messaging.

### 7. Connection and first-message verification

- agent writes the approved JSON payload through its file-writing capability, not a platform-specific shell-quoting trick;
- exact signed submission;
- durable custody;
- recipient acknowledgement;
- reply;
- exact retrieval;
- acknowledgement of the retrieved envelope;
- each fact remains distinct and existing A2A remains unchanged.

### 8. Recovery and reporting

- browser cancellation, network loss, timeout, expired code, duplicate paste, restart, and response loss resume from owner-only state without a second identity;
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
- keeps key, continuation, challenge, callback, and receipt state owner-only;
- accepts only the short-lived claim code through a masked prompt;
- resumes safely after timeout or response loss;
- returns `enrolled_identity_only`, `authority_granted: false`, and `first_message_blocked_explicit_authority_required`.

A release passes the C0 gate only when it additionally lets an authorized administrator issue entitlements by public principal ID without possessing the beneficiary identity file/private key, and the real Core/approval/recipient infrastructure is proven.

AgentNet `0.1.8` fails this gate:

- `join begin` prints an authorization URL and writes pending state;
- `join complete` requires local challenge and approval files;
- no product-owned possession-bound approval-receipt handoff exists.

For `0.1.8`, report **blocked: product component not yet shipped** and stop before `join begin`. Never substitute Slack, A2A, chat, prompt, log, repository, copy/paste, USB, QR, or custom glue.

## Pilot acceptance

Identity-only pilot success requires one paste, a fresh laptop unable to read or automate the approval authenticator, real workforce OIDC, real WebAuthn UV, no leaked private state, safe idempotent rerun, and no implicit authority. Default server colocation retains `independent_boundary_proven=false`; separate approval hosting is optional high assurance.

Full C0 success additionally requires exact principal-ID-based entitlement issuance, safe inventory confirmation, C0 submission, custody, recipient acknowledgement, reply, retrieval, and envelope acknowledgement as distinct facts. A mock/local run or transport ACK is not this proof.
