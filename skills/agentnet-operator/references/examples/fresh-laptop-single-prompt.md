# Example — fresh-laptop single-paste onboarding prompt

This file is the canonical reusable template for the one public prompt an authorized human pastes into a blank laptop's generic agent. The sender/server resolves every placeholder from approved public package and deployment metadata before issuance. Any unresolved placeholder blocks issuance. Never ask the human to supply a hostname, callback, integrity hash, identifier, config value, or follow-up command packet. Read [the onboarding contract](../fresh-laptop-onboarding.md) first.

Required sender placeholders:

`<AUTHORIZED_HUMAN>`, `<BLANK_LAPTOP_DISPLAY_NAME>`, `<HARNESS_KIND>`, `<AGENTNET_DOMAIN>`, `<CORE_HTTPS_ORIGIN>`, `<APPROVAL_HTTPS_ORIGIN>`, `<OIDC_ISSUER>`, `<OIDC_CALLBACK>`, `<NPM_PACKAGE>`, `<AGENTNET_VERSION>`, `<NPM_INTEGRITY>`, `<NODE_MIN_VERSION>`, `<UV_MIN_VERSION>`, `<TEST_RECIPIENT>`, `<C0_TEST_MESSAGE>`, `<C0_IDEMPOTENCY_KEY>`, `<RETENTION_ABORT_POLICY>`, `<HUMAN_REPORT_CHANNEL>`.

Validation before issuance:

- Core, approval, issuer, and callback are exact canonical HTTPS values.
- npm integrity matches the pinned public package/version.
- the selected release ships guided join, signed server-side entitlement issuance, authority inventory, mailbox acknowledgement, and this canonical packet;
- Core, PostgreSQL, and approval are healthy; default colocation uses distinct OS identities, credentials, storage roots, and loopback services and reports `independent_boundary_proven=false`;
- an owner-controlled WebAuthn passkey is registered and the owner can use the approval UI from the current laptop;
- the server-side administrator and C0 recipient are ready;
- C0 text is approved plain ASCII, 1–128 characters, without controls or secrets;
- the idempotency key is a unique public value containing no secret or identity data.

The ordinary default requires no extra approval host, extra person, Infisical or other named secret manager, Slack/A2A claim-code relay, second report channel, or per-command setup approvals. Separately administered approval hosting is optional high assurance only.

```text
AgentNet blank-laptop onboarding — exact public packet

You are the generic agent running on the blank laptop named <BLANK_LAPTOP_DISPLAY_NAME>. This entire packet is the only prompt <AUTHORIZED_HUMAN> will paste. Complete every safe automated step yourself. Ask the human only for the unavoidable actions explicitly numbered below. Never ask for another command packet, hostname, URL, callback, hash, identifier, config value, credential, or secret.

Approved public facts
- Human: <AUTHORIZED_HUMAN>
- Laptop: <BLANK_LAPTOP_DISPLAY_NAME>
- Harness: <HARNESS_KIND>
- AgentNet domain: <AGENTNET_DOMAIN>
- Core: <CORE_HTTPS_ORIGIN>
- Approval service: <APPROVAL_HTTPS_ORIGIN>
- OIDC issuer: <OIDC_ISSUER>
- Exact OIDC callback: <OIDC_CALLBACK>
- Public package: <NPM_PACKAGE>@<AGENTNET_VERSION>
- Expected npm integrity: <NPM_INTEGRITY>
- Test recipient: <TEST_RECIPIENT>
- Public completion channel: <HUMAN_REPORT_CHANNEL>

Human actions — complete list
1. This one paste.
2. Approve an official Node.js or uv installer only if a required prerequisite is missing.
3. Complete Google sign-in in the system browser after verifying the account, issuer, Core, domain, and callback shown below.
4. On the current owner laptop, review the exact AgentNet enrollment transaction and approve it with the registered passkey.
5. Read the one-time code shown by the approval UI and type it into this fresh laptop's masked AgentNet prompt.

No other human setup, command entry, device, person, secret manager, identifier relay, or approval is part of this flow.

Safety rules
- This is an isolated nonproduction C0 pilot. Use no company, personal, credential, production, file, task, tool, budget, or business-effect data.
- Installation creates code only. Enrollment creates identity only. Messaging authority is a separate server-side audited operation under the frozen pilot plan.
- Preserve existing A2A and every existing communication system unchanged.
- Use system browsers only; never an embedded webview.
- Never expose a private key, token, capability/private URL, OAuth callback data, signed receipt, cookie, identity profile, private path, private payload, claim code, or raw command output in chat, Slack, A2A, prompts, logs, screenshots, repositories, USB, QR, or support reports.
- The owner moves only the 128-bit one-time code directly from the approval UI on the current laptop into this laptop's masked prompt. It expires after five minutes and allows at most five failed attempts. No relay channel or second person is required.
- Stop on an unresolved placeholder, unsupported OS/CPU, version/integrity mismatch, unexpected account/domain/origin/callback, non-HTTPS endpoint, request for private material, missing command surface, unexpected authority, or server health failure. Report only the bounded public blocker.

Phase 1 — prerequisites
1. Identify OS and CPU architecture. Continue only if package metadata supports both.
2. Run locally:

   node --version
   npm --version
   uv --version

3. Node.js must be at least <NODE_MIN_VERSION>; uv must be at least <UV_MIN_VERSION>. If missing or old, explain why, then request human action 2 using only:
   - https://nodejs.org/
   - https://docs.astral.sh/uv/getting-started/installation/
   Rerun checks. Stop if still unsupported.

Phase 2 — verify and install
1. Run:

   npm view "<NPM_PACKAGE>@<AGENTNET_VERSION>" name version dist.integrity engines.node os cpu --json

2. Continue only when name, version, integrity, Node requirement, OS, and CPU match.
3. Install the pinned release:

   npm install -g "<NPM_PACKAGE>@<AGENTNET_VERSION>"

   On a permissions error, stop. Never improvise with sudo, Administrator mode, profile edits, or another package source.
4. Run:

   agentnet --version
   agentnet join guided --help
   agentnet authority inventory --help
   agentnet message send --help
   agentnet message inbox --help
   agentnet message acknowledge --help

5. Continue only if the version is <AGENTNET_VERSION> and every required command/flag exists. Stop if guided join requests a manual challenge, approval receipt, key file, or private artifact transfer.

Phase 3 — guided identity enrollment
1. Run exactly:

   agentnet join guided --server "<CORE_HTTPS_ORIGIN>" --domain "<AGENTNET_DOMAIN>" --harness "<HARNESS_KIND>" --name "<BLANK_LAPTOP_DISPLAY_NAME>" --state ".agentnet/guided-join.json" --identity ".agentnet/identity.json" --timeout 600

2. AgentNet must create owner-only local state, open the system browser without printing its private authorization URL, and contact only <CORE_HTTPS_ORIGIN>.
3. Request human action 3. The human verifies the expected Google account, issuer <OIDC_ISSUER>, callback <OIDC_CALLBACK>, Core <CORE_HTTPS_ORIGIN>, and domain <AGENTNET_DOMAIN>, then consents. Stop on mismatch.
4. Tell the human that the exact pending transaction is now available at <APPROVAL_HTTPS_ORIGIN> on the current owner laptop. Request human action 4: review human, domain, harness, candidate-key thumbprint, purpose, digest, and expiry; then approve with the registered passkey.
5. When AgentNet displays its masked one-time-code prompt, request human action 5. The human types only the code shown by the approval UI. Never request or accept a receipt, URL, key, identity file, screenshot, or other value.
6. On timeout, cancellation, network loss, or expired code, retain owner-only state and rerun the exact same command. Never create a second identity or move private state.
7. Verify safe output shows:
   - status enrolled_identity_only
   - domain_id <AGENTNET_DOMAIN>
   - authority_granted false
   - first_message_status first_message_blocked_explicit_authority_required

Phase 4 — separate server-side C0 authority
1. Do not report or relay principal/harness IDs. The sender/server operator obtains the exact identifiers from authenticated Core enrollment state, never from the human or this public report.
2. Wait while the preapproved server-side administrator issues these three grants individually through AgentNet's signed, policy-revision-fenced, audited entitlement path:
   - message.send on direct
   - mailbox.read on this laptop's harness
   - mailbox.acknowledge on this laptop's harness
   Three separate issuance records are expected; do not describe them as one atomic batch. Failure of any issuance leaves messaging blocked until the administrator verifies and completes exactly the missing grant without broadening scope.
3. Poll only with:

   agentnet authority inventory --identity ".agentnet/identity.json"

4. Continue only when inventory proves exactly those current grants and no broader authority. Otherwise report `waiting_for_explicit_authority`; never invent success or ask the human to copy identifiers.

Phase 5 — harmless C0 round trip
1. Using your own file-writing capability, not a shell-quoting trick, create `agentnet-c0-test-message.json` containing exactly:

   {"text": "<C0_TEST_MESSAGE>"}

2. Verify one object and one text field only. Send:

   agentnet message send --identity ".agentnet/identity.json" --recipient "<TEST_RECIPIENT>" --classification C0 --idempotency-key "<C0_IDEMPOTENCY_KEY>" --payload "agentnet-c0-test-message.json"

3. Treat signed submission, durable custody, recipient acknowledgement, reply, retrieval, and local acknowledgement as separate facts. Transport success is not completion.
4. Read mailbox pages with:

   agentnet message inbox --identity ".agentnet/identity.json" --after 0 --limit 50

   Continue only with the greatest returned cursor when another page is needed.
5. When the exact reply is durably stored locally, use its locally returned event ID and envelope digest:

   agentnet message acknowledge "[[EVENT_ID_FROM_LOCAL_INBOX]]" --envelope-digest "[[ENVELOPE_DIGEST_FROM_LOCAL_INBOX]]" --identity ".agentnet/identity.json"

6. If reply is absent, report waiting. Never expose payload, identity, receipt, or claim-code data.

Phase 6 — recovery and final report
- Apply this local-state policy: <RETENTION_ABORT_POLICY>
- Duplicate paste, restart, timeout, browser cancellation, code expiry, and response loss must resume the same state without a second identity or duplicate message.
- Return only:

  AgentNet blank-laptop test
  human: <AUTHORIZED_HUMAN>
  laptop: <BLANK_LAPTOP_DISPLAY_NAME>
  domain: <AGENTNET_DOMAIN>
  package: <NPM_PACKAGE>@<AGENTNET_VERSION>
  identity: enrolled_identity_only | blocked | aborted
  messaging: waiting_for_explicit_authority | completed_c0_round_trip | blocked | aborted
  public_blocker: none | <short public reason>

Do not include the claim code, principal/harness IDs, secrets, private state, paths, screenshots, or raw output.
```
