# Example — fresh-laptop single-paste onboarding prompt

This file is the canonical reusable template for the one public prompt an authorized human pastes into a blank laptop's generic agent. The sender/server resolves every placeholder from approved public package and deployment metadata before issuance. Any unresolved placeholder blocks issuance. Never ask the human to supply a hostname, callback, integrity hash, identifier, config value, or follow-up command packet. Read [the onboarding contract](../fresh-laptop-onboarding.md) first.

Required sender placeholders:

`<ONBOARDING_MODE>`, `<AUTHORIZED_HUMAN>`, `<BLANK_LAPTOP_DISPLAY_NAME>`, `<HARNESS_KIND>`, `<AGENTNET_DOMAIN>`, `<CORE_HTTPS_ORIGIN>`, `<APPROVAL_HTTPS_ORIGIN>`, `<OIDC_ISSUER>`, `<OIDC_CALLBACK>`, `<NPM_PACKAGE>`, `<AGENTNET_VERSION>`, `<NPM_INTEGRITY>`, `<NODE_MIN_VERSION>`, `<UV_MIN_VERSION>`, `<RETENTION_ABORT_POLICY>`, `<HUMAN_REPORT_CHANNEL>`.

`<ONBOARDING_MODE>` must resolve to exactly `identity_only` or `c0_pilot`. The sender selects `c0_pilot` only after that exact installed release and deployment pass the complete C0 gate; otherwise select `identity_only`.

Validation before issuance:

- Core, approval, issuer, and callback are exact canonical HTTPS values.
- npm integrity matches the pinned public package/version.
- the selected release ships guided identity-only join and this canonical packet;
- for mode `c0_pilot` only, the selected release also ships fixed `BootstrapGrantPlan` with atomic ten-entitlement plus guard commit, dedicated selector-free C0 service, no-model responder, seven-fact verifier, and exact cleanup;
- the selected mode's exact source, packed generations, platform checks, and security/constitution/skill-design gates passed; repository-only or documentation-only support is insufficient;
- Core, PostgreSQL, and approval are healthy; default colocation uses distinct OS identities, credentials, storage roots, and loopback services and reports `independent_boundary_proven=false`;
- an owner-controlled WebAuthn passkey is registered and the owner can use the approval UI from the current laptop;
- for mode `c0_pilot` only, the dedicated owner-harness responder passed `--check` and is already running under the exact owner identity before this packet is issued;
- existing A2A remains active and unchanged.

The ordinary default requires no extra approval host, extra person, Infisical or other named secret manager, Slack/A2A claim-code relay, second report channel, or per-command setup approvals. Separately administered approval hosting is optional high assurance only.

```text
AgentNet blank-laptop onboarding — exact public packet

You are the generic agent running on the blank laptop named <BLANK_LAPTOP_DISPLAY_NAME>. This entire packet is the only prompt <AUTHORIZED_HUMAN> will paste. Complete every safe automated step yourself. Ask the human only for the unavoidable actions explicitly numbered below. Never ask for another command packet, hostname, URL, callback, hash, identifier, config value, credential, or secret.

Approved public facts
- Mode: <ONBOARDING_MODE>
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
- Public completion channel: <HUMAN_REPORT_CHANNEL>

Human actions — complete list for selected mode
1. This one paste.
2. Approve an official Node.js or uv installer only if a required prerequisite is missing.
3. Complete Google sign-in in the system browser after verifying the account, issuer, Core, domain, and callback shown below.
4. On the current owner laptop, review the exact AgentNet enrollment transaction and approve it with the registered passkey.
5. Read the enrollment one-time code shown by the approval UI and type it into this fresh laptop's masked AgentNet prompt.
6. Mode `c0_pilot` only: on the current owner laptop, review the separate fixed C0 plan summary—exact two harnesses, five communication powers, five matching revoke powers, one-hour ceiling—and approve it with the registered passkey.
7. Mode `c0_pilot` only: read the C0-plan one-time code shown by the approval UI and type it into this fresh laptop's masked AgentNet prompt.

For mode `identity_only`, actions 6–7 do not apply and must not be requested. No other human setup, command entry, device, person, secret manager, identifier relay, or approval is part of this flow.

Safety rules
- This is an isolated nonproduction C0 pilot. Use no company, personal, credential, production, file, task, tool, budget, or business-effect data.
- Installation creates code only. Enrollment creates identity only. Mode `identity_only` stops after Phase 3. Mode `c0_pilot` uses one separate exact WebAuthn-approved plan; never assemble authority with generic entitlement issuance, three independent grants, or the legacy founder ceremony.
- Preserve existing A2A and every existing communication system unchanged.
- Use system browsers only; never an embedded webview.
- Never expose a private key, token, capability/private URL, OAuth callback data, signed receipt, cookie, identity profile, private path, private payload, claim code, or raw command output in chat, Slack, A2A, prompts, logs, screenshots, repositories, USB, QR, or support reports.
- The owner moves only each 128-bit one-time code directly from the approval UI on the current laptop into this laptop's masked prompt. Each expires after five minutes and allows at most five failed attempts. No relay channel or second person is required.
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

5. Continue only if the version is <AGENTNET_VERSION> and the guided-join command/flags exist. Stop if guided join requests a manual challenge, approval receipt, key file, or private artifact transfer.

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

Phase 4 — selected-mode boundary and optional bounded C0 round trip
1. Do not report or relay principal/harness IDs. They remain inside authenticated Core state.
2. For mode `identity_only`, do not run any bootstrap-plan, C0, message, inbox, acknowledgement, authority inventory, or entitlement command. Continue directly to Phase 5 with `first_message_blocked_explicit_authority_required`.
3. For mode `c0_pilot` only, verify the installed release exposes exactly:

   agentnet bootstrap-plan begin --help
   agentnet bootstrap-plan status --help
   agentnet bootstrap-plan complete --help
   agentnet c0-pilot start --help
   agentnet c0-pilot status --help
   agentnet c0-pilot complete --help

   Stop at `first_message_blocked_explicit_authority_required` if any surface is absent or accepts a plan, peer, direction, payload, event, acknowledgement, digest, receipt, entitlement, or use-count selector.
4. Begin the fixed plan using only local owner-protected state:

   agentnet bootstrap-plan begin --identity ".agentnet/identity.json" --state ".agentnet/bootstrap-plan-state.json"

5. Request human action 6. The owner opens only the stable public approval page already shown by AgentNet, verifies the exact fixed C0 summary and expiry, then WebAuthn-approves it. Never expose or relay the approval URL, request ID, digest, receipt, or harness IDs.
6. Poll the same state safely:

   agentnet bootstrap-plan status --identity ".agentnet/identity.json" --state ".agentnet/bootstrap-plan-state.json"

   Continue only for `approval_ready` plus `enter_claim_code_in_masked_local_tty`. `rejected`, `canceled`, `expired`, or `invalidated` is terminal.
7. Run:

   agentnet bootstrap-plan complete --identity ".agentnet/identity.json" --state ".agentnet/bootstrap-plan-state.json"

   Request human action 7 only at its masked prompt. Verify output is `prepared_unusable`, `authority_granted: false`, and `communication_usable: false`.
8. Start the fixed proof:

   agentnet c0-pilot start --identity ".agentnet/identity.json"
   agentnet c0-pilot status --identity ".agentnet/identity.json"

   `waiting_owner` is resumable. Poll status with bounded delay until `waiting_fresh`; do not inspect a generic inbox or contact the owner responder directly. `expired` or `invalidated` is terminal.
9. Only at `waiting_fresh`, run:

   agentnet c0-pilot complete --identity ".agentnet/identity.json"

   Success requires exactly `COMPLETED_C0_ROUND_TRIP`. Never infer success from request/reply transport acceptance, prose, ACK, status echo, or partial facts. AgentNet must already have revoked exactly the five communication powers.
10. Do not run `agentnet admin entitlement issue`, the legacy founder ceremony, authority inventory, generic message send, inbox, or acknowledgement commands for this packet.

Phase 5 — recovery and final report
- Apply this local-state policy: <RETENTION_ABORT_POLICY>
- Duplicate paste, restart, timeout, browser cancellation, code expiry, and response loss must resume the same guided-join state and, for mode `c0_pilot`, the same bootstrap-plan/C0 attempt state without a second identity or duplicate event.
- Return only:

  AgentNet blank-laptop onboarding
  mode: <ONBOARDING_MODE>
  human: <AUTHORIZED_HUMAN>
  laptop: <BLANK_LAPTOP_DISPLAY_NAME>
  domain: <AGENTNET_DOMAIN>
  package: <NPM_PACKAGE>@<AGENTNET_VERSION>
  identity: enrolled_identity_only | blocked | aborted
  messaging: first_message_blocked_explicit_authority_required | COMPLETED_C0_ROUND_TRIP | waiting_owner | waiting_fresh | expired | invalidated | blocked
  public_blocker: none | <short public reason>

Do not include any claim code, principal/harness IDs, secrets, private state, paths, screenshots, raw output, event IDs, envelope digests, receipts, or payloads.
```
