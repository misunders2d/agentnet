# Fresh-laptop onboarding contract

Use this reference when a human needs to enroll any newly installed laptop agent into AgentNet. This is a generic product workflow, not a one-off device procedure.

## Starting state

Assume the laptop has only:

- a supported operating system;
- a newly installed generic agent or harness;
- a human manager who can paste one instruction packet and respond to browser prompts.

Do not assume AgentNet, A2A, Pi extensions, Node.js, `uv`, Infisical, private-repository access, organization credentials, existing keys, configuration files, or technical operator knowledge.

## Human channel boundary

Sergey, an authorized administrator, or an ordinary enrolled server agent may give the human manager one self-contained **public bootstrap packet** through an authenticated human channel such as an established Slack conversation. The human pastes the complete packet into the new agent.

The unconnected laptop has no agent inbox. Hub cannot message it directly. A2A is not a bootstrap dependency, enrollment transport, or identity proof.

The packet contains public instructions only. It never contains private keys, secret values, join state, OAuth callback codes or challenges, identity profiles, approval capabilities or private URLs, signed approval receipts, bearer tokens, cookies, or reusable credentials.

If AgentNet is already installed, the receiving agent loads `agentnet-operator` and follows this contract. If AgentNet is not installed, the pasted packet must remain self-contained through prerequisite checks and npm installation; after installation, the receiving agent reloads or invokes the bundled skill when its harness supports skills, then continues the same packet. The packet cannot assume the skill exists before package installation.

## Shared-skill roles

### Sender, Hub, or current manager

1. Identify the intended human, target harness kind/name, AgentNet domain, public server origin, and exact release.
2. Verify the release's published installation source, supported OS, Node.js and `uv` requirements, integrity/provenance, and actual CLI surface.
3. Verify every enrollment phase below exists in that release, including product-owned secure transfer of private artifacts.
4. Generate one complete copy/paste packet with exact commands, where they run, expected safe output, failure stops, and how the human reports completion.
5. Send only that public packet through the authenticated human channel.
6. Never claim delivery to the new agent until the human confirms they pasted it.

### Fresh-laptop agent

1. Treat the pasted packet as the complete starting context; do not assume hidden skills, files, credentials, or prior network membership.
2. Explain each human action plainly and run only commands authorized by the packet.
3. Verify outputs at every phase before continuing.
4. Keep all generated private state owner-only and local.
5. Stop on any version, integrity, origin, identity, approval, expiry, or capability mismatch. Report the exact public error to the human without exposing private state.
6. Complete first-message verification before claiming onboarding succeeded.

## Required bootstrap packet

A valid packet must include all phases below. Missing phases block issuance of the packet.

### 1. Scope and safety

- intended human and target laptop/harness display name;
- exact AgentNet domain and canonical HTTPS server origin;
- statement that installation creates no identity, authority, binding, or network membership;
- public/private data boundary and explicit stop conditions;
- confirmation that existing communication systems remain unchanged until separately approved.

### 2. Prerequisites

- supported OS and architecture;
- exact minimum Node.js and `uv` versions for the selected release;
- official public sources for prerequisites;
- commands to check installed versions;
- expected version output and remediation when absent or unsupported.

### 3. Public package installation

- exact public package name and immutable version;
- public registry/source and integrity/provenance checks;
- exact command and where to run it;
- `agentnet --version` and `agentnet --help` verification;
- explicit failure if the package, version, or supported commands differ.

### 4. Guided join start

- exact supported onboarding/join command;
- locally generated owner-only candidate key and state;
- expected public status without printing private key or state;
- exact server/domain/harness confirmation;
- no raw JSON editing or unexplained file paths for a nontechnical operator.

### 5. System-browser Google OIDC

- browser opens on the new laptop; embedded webviews are forbidden;
- human verifies the Google account and consent destination;
- Authorization Code + PKCE callback is captured automatically into owner-only state;
- no callback code, challenge, token, or browser capability is copied into chat.

### 6. Independent approval

- exact canonical enrollment transaction reaches the configured approval service through a product-owned, authenticated, bounded channel;
- approver sees human, domain, harness, candidate-key thumbprint, purpose, transaction digest, and expiry;
- WebAuthn user verification is explicit;
- signed approval receipt reaches only the matching core/candidate operation through a product-owned, possession-bound, encrypted or direct-consumption path;
- no human copies or transports the receipt.

If the installed release lacks that product-owned secure receipt handoff, this phase is the exact blocker: report **blocked: product component not yet shipped** and do not begin enrollment.

### 7. Join completion

- candidate proves possession of its locally retained key;
- challenge and approval are consumed atomically and once;
- owner-only identity profile is written locally;
- expected domain, principal, harness, credential, key thumbprint, audience, status, and assurance are displayed safely;
- enrollment grants no implicit entitlement.

### 8. Connection and first-message verification

- verify the enrolled identity can reach the exact AgentNet server;
- send one harmless C0 AgentNet-native test message to an explicitly authorized test recipient;
- verify signed submission, durable custody state, recipient acknowledgement, reply, and exact-message retrieval as distinct facts;
- do not call transport success a business effect;
- preserve existing A2A and do not use real company data during the isolated test.

### 9. Recovery and reporting

- safe handling of browser cancellation, network loss, expired challenge/receipt, duplicate attempt, restart, and response loss;
- cleanup or resumable state without duplicate identity creation;
- exact public status the fresh agent returns to its human manager;
- exact status the human sends back to the sender/Hub;
- no private artifact in support messages.

## Installed-release gate

Never infer capability from documentation written for a newer version. Check the installed release's actual CLI/help and tested endpoints before issuing a live packet.

A release passes this gate only when it provides a complete nontechnical flow from blank laptop through first verified message without hidden knowledge or manual private-artifact transfer.

AgentNet `0.1.8` fails this gate:

- `join begin` writes candidate state and prints an authorization URL;
- the Google callback returns challenge JSON;
- `join complete` requires local challenge and approval files;
- no product-owned possession-bound secure approval-receipt handoff exists.

For `0.1.8`, report **blocked: product component not yet shipped** and stop before `join begin`. Do not substitute Slack, A2A, chat, prompts, logs, repositories, copy/paste, USB, QR, or custom glue. Never claim a one-click onboarding link unless the installed release actually ships and verifies it.
