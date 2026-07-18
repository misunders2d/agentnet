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

## Canonical public onboarding prompt template

This template is the source of truth for the single fresh-laptop onboarding prompt. The authorized human copies and pastes the resolved prompt exactly once; the receiving agent and AgentNet server handle the remaining workflow. Do not split it into follow-up prompts, command fragments, or manual private-artifact exchanges. Do not handcraft, shorten, paraphrase, or reconstruct it from the checklist. Before issuance, the sender must replace and verify every sender-resolved placeholder listed below from approved public metadata. Any unresolved required placeholder blocks issuance. Runtime markers written as `[[..._FROM_LOCAL_INBOX]]` are populated only from local AgentNet output during the test and are not issuance placeholders. The template is public: it must never acquire private keys, secret values, tokens, capability/private URLs, callback codes, claim codes, receipts, cookies, identity profiles, private host paths, or private payloads.

Required placeholders:

`<AUTHORIZED_HUMAN>`, `<BLANK_LAPTOP_DISPLAY_NAME>`, `<HARNESS_KIND>`, `<AGENTNET_DOMAIN>`, `<CORE_HTTPS_ORIGIN>`, `<APPROVAL_HTTPS_ORIGIN>`, `<OIDC_ISSUER>`, `<OIDC_CALLBACK>`, `<NPM_PACKAGE>`, `<AGENTNET_VERSION>`, `<NPM_INTEGRITY>`, `<NODE_MIN_VERSION>`, `<UV_MIN_VERSION>`, `<TEST_RECIPIENT>`, `<C0_TEST_MESSAGE>`, `<RETENTION_ABORT_POLICY>`, `<HUMAN_REPORT_CHANNEL>`.

The sender must validate that `<CORE_HTTPS_ORIGIN>`, `<APPROVAL_HTTPS_ORIGIN>`, `<OIDC_ISSUER>`, and `<OIDC_CALLBACK>` are canonical HTTPS values; `<NPM_INTEGRITY>` is the exact public registry SRI for the pinned package/version; and `<C0_TEST_MESSAGE>` is an approved 1–128 character plain-ASCII sentence containing no quote, backslash, control character, newline, variable marker, or shell metacharacter.

```text
AgentNet blank-laptop onboarding — exact public packet

You are the generic agent running on the blank laptop named <BLANK_LAPTOP_DISPLAY_NAME>. This entire packet is the one prompt the human will paste. Guide <AUTHORIZED_HUMAN> through it to completion without requesting a second instruction packet. Do not add, omit, reorder, paraphrase, or guess steps or values. Assume this laptop has no AgentNet, A2A, extensions, private repository access, credentials, keys, Node.js, uv, or prior network membership until verified locally.

Approved public facts:
- AgentNet domain: <AGENTNET_DOMAIN>
- Core: <CORE_HTTPS_ORIGIN>
- Independent approval service: <APPROVAL_HTTPS_ORIGIN>
- OIDC issuer: <OIDC_ISSUER>
- Exact OIDC callback: <OIDC_CALLBACK>
- Harness: <HARNESS_KIND>
- Public package: <NPM_PACKAGE>@<AGENTNET_VERSION>
- Expected npm integrity: <NPM_INTEGRITY>
- Test recipient: <TEST_RECIPIENT>

Safety rules:
- This is an isolated nonproduction C0 pilot using no company or personal data.
- Installation creates code only. Enrollment creates identity only. Neither grants messaging, recipient, read, tool, data, A2A, or business authority.
- Preserve existing A2A and every existing communication system unchanged.
- Use only the system browser for OIDC and WebAuthn. Do not use an embedded webview.
- Never place a private key, token, capability/private URL, callback code, claim code, approval receipt, cookie, identity profile, private host path, or private payload in chat, Slack, A2A, prompts, logs, screenshots, repositories, USB, QR, or support reports.
- Human actions are limited to this one paste, system-browser sign-in/consent, explicit passkey user verification, and direct entry into a masked local terminal prompt when the product requires it. The human must never transport private artifacts between systems.
- Stop immediately on any unresolved placeholder, version/integrity mismatch, unexpected origin/account/domain/callback, non-HTTPS endpoint, private-artifact request, or command mismatch. Report only the short public blocker to <HUMAN_REPORT_CHANNEL>; do not ask for a replacement prompt or ad-hoc commands.

Phase 1 — prerequisites
1. Identify the laptop OS and architecture. Continue only if the published package metadata supports them.
2. In the laptop's normal terminal, run:

   node --version
   npm --version
   uv --version

3. Node.js must be at least <NODE_MIN_VERSION>. uv must be at least <UV_MIN_VERSION>. If missing or older, ask <AUTHORIZED_HUMAN> to install from the official public sites only:
   - https://nodejs.org/
   - https://docs.astral.sh/uv/getting-started/installation/
   Then rerun the checks. Stop if requirements still fail.

Phase 2 — public package verification and installation
1. Run:

   npm view "<NPM_PACKAGE>@<AGENTNET_VERSION>" name version dist.integrity engines.node os cpu --json

2. Continue only if name/version equal `<NPM_PACKAGE>@<AGENTNET_VERSION>`, `dist.integrity` exactly equals `<NPM_INTEGRITY>`, and OS/CPU/Node requirements match this laptop.
3. Install only the pinned public release:

   npm install -g "<NPM_PACKAGE>@<AGENTNET_VERSION>"

   If this returns a permissions error, stop. Use the official Node.js installer or an already approved user-local npm prefix, then rerun the exact command. Do not improvise with `sudo`, Administrator mode, profile edits, or a different package source.

4. Run:

   agentnet --version
   agentnet --help
   agentnet join guided --help
   agentnet authority inventory --help
   agentnet message send --help
   agentnet message inbox --help
   agentnet message acknowledge --help

5. Continue only if AgentNet reports `<AGENTNET_VERSION>`; `join guided` supports `--server`, `--domain`, `--harness`, `--name`, `--state`, and `--identity`; and the authority/message commands expose the exact flags used below. Stop if guided join requires manual challenge or approval-receipt files.

Phase 3 — product-guided identity enrollment
1. Run exactly:

   agentnet join guided --server "<CORE_HTTPS_ORIGIN>" --domain "<AGENTNET_DOMAIN>" --harness "<HARNESS_KIND>" --name "<BLANK_LAPTOP_DISPLAY_NAME>" --state ".agentnet/guided-join.json" --identity ".agentnet/identity.json"

2. AgentNet must create owner-only local state, open the system browser without printing its private authorization URL, and poll only `<CORE_HTTPS_ORIGIN>`.
3. In the browser, <AUTHORIZED_HUMAN> must confirm the expected Google account, issuer `<OIDC_ISSUER>`, callback `<OIDC_CALLBACK>`, Core origin `<CORE_HTTPS_ORIGIN>`, and domain `<AGENTNET_DOMAIN>`. Stop on any mismatch.
4. Independent approval must use `<APPROVAL_HTTPS_ORIGIN>`. The human approver must see the exact human, domain, harness, candidate-key thumbprint, purpose, transaction digest, and expiry and must complete explicit WebAuthn user verification.
5. The signed approval receipt must travel only through the product-owned Core/approval path. No human or agent may copy, see, paste, upload, or relay it.
6. The default guided polling window is five minutes. If it times out, pending owner-only state is retained; rerun the exact same command to resume rather than starting a second identity.
7. If the terminal requests a one-time approval code, <AUTHORIZED_HUMAN> may type it only into the terminal's masked prompt. Never relay it through this conversation or any channel. Stop if direct private terminal input is unavailable.
8. On success, verify only the safe normal output shown by AgentNet. It must report:
   - status `enrolled_identity_only`
   - domain `<AGENTNET_DOMAIN>`
   - `authority_granted: false`
   - `first_message_status: first_message_blocked_explicit_authority_required`
   Do not paste raw output or local state into chat.

Phase 4 — separate messaging authority
1. Stop after identity enrollment until a current authorized administrator separately grants this exact identity:
   - `message.send` limited to the approved C0 test;
   - recipient authority for `<TEST_RECIPIENT>`;
   - read/inbox authority needed to retrieve the test reply.
2. After the administrator confirms the grant through the approved control path, run:

   agentnet authority inventory --identity ".agentnet/identity.json"

3. Continue only if the safe inventory shows the exact current grants above. Enrollment alone is never sufficient. Otherwise report `first_message_blocked_explicit_authority_required` and stop.

Phase 5 — one harmless C0 message
1. Create one local JSON object containing only the approved plain-ASCII sentence `<C0_TEST_MESSAGE>`:

   node -e "require('fs').writeFileSync('agentnet-c0-test-message.json', JSON.stringify({text: process.argv[1]}) + '\n')" "<C0_TEST_MESSAGE>"

   The resulting file must contain exactly one JSON object with one `text` field. Do not include company, personal, credential, or production data.
2. Send it using the enrolled identity:

   agentnet message send --identity ".agentnet/identity.json" --recipient "<TEST_RECIPIENT>" --classification C0 --payload "agentnet-c0-test-message.json"

3. Treat signed submission, durable custody, recipient acknowledgement, reply, and exact retrieval as separate facts. Transport success alone is not completion.
4. Read the first mailbox page only through AgentNet:

   agentnet message inbox --identity ".agentnet/identity.json" --after 0 --limit 50

   If another bounded read is needed, use the greatest `cursor` returned by the previous local result as the next `--after` value. Do not restart from zero or invent a cursor.
5. When the exact reply event is durably stored locally, replace both runtime markers below only with the exact values returned by that local inbox result, then acknowledge it:

   agentnet message acknowledge "[[EVENT_ID_FROM_LOCAL_INBOX]]" --envelope-digest "[[ENVELOPE_DIGEST_FROM_LOCAL_INBOX]]" --identity ".agentnet/identity.json"

6. Never copy event payloads, receipt material, identity data, or local paths into chat. If the reply is absent, report a public waiting status rather than inventing success.

Phase 6 — recovery, abort, and public report
- Apply this approved local-state rule: <RETENTION_ABORT_POLICY>
- Browser cancellation, timeout, network loss, duplicate attempt, restart, or response loss must resume through the same `agentnet join guided` command or stop without creating a second identity.
- Never recover by moving private artifacts between devices or channels.
- Return only this public report to <HUMAN_REPORT_CHANNEL>:

  AgentNet blank-laptop test
  human: <AUTHORIZED_HUMAN>
  laptop: <BLANK_LAPTOP_DISPLAY_NAME>
  domain: <AGENTNET_DOMAIN>
  package: <NPM_PACKAGE>@<AGENTNET_VERSION>
  identity: enrolled_identity_only | blocked | aborted
  messaging: waiting_for_explicit_authority | completed_c0_round_trip | blocked | aborted
  public_blocker: none | <short public reason>

Do not include secrets, private artifacts, private paths, screenshots, or raw command output in that report.
```

## Installed-release gate

Never infer capability from documentation written for a newer version. Check the installed release's actual CLI/help and tested endpoints before issuing a live packet.

A release passes this gate only when it provides a complete nontechnical flow from blank laptop through first verified message without hidden knowledge or manual private-artifact transfer.

AgentNet `0.1.8` fails this gate:

- `join begin` writes candidate state and prints an authorization URL;
- the Google callback returns challenge JSON;
- `join complete` requires local challenge and approval files;
- no product-owned possession-bound secure approval-receipt handoff exists.

For `0.1.8`, report **blocked: product component not yet shipped** and stop before `join begin`. Do not substitute Slack, A2A, chat, prompts, logs, repositories, copy/paste, USB, QR, or custom glue. Never claim a one-click onboarding link unless the installed release actually ships and verifies it.

A later release may pass the private-artifact handoff gate only when its actual
CLI exposes and tests `agentnet join guided`. Required invocation shape:

```bash
agentnet join guided \
  --server https://agentnet.example \
  --domain corp.example \
  --harness pi \
  --name 'Fresh laptop' \
  --state .agentnet/guided-join.json \
  --identity .agentnet/identity.json
```

The command must open the system browser without printing the private
authorization URL, keep candidate key/continuation/challenge state owner-only,
poll only Core, and prompt only for the 128-bit short-lived claim code after
fresh WebAuthn UV. Normal output must contain no callback code, continuation,
capability URL, claim code, receipt, private key, or token. Exact rerun must
resume or return the same completed identity. The result
`enrolled_identity_only` still blocks first messaging until a separate current
administrator grants exact `message.send` and recipient/read authority.
