# Example — fresh-laptop single-paste onboarding prompt

This file is the canonical reusable template for the one public prompt an authorized human pastes into a blank laptop's generic agent. Resolve every sender placeholder from approved public metadata before issuing it. Any unresolved placeholder blocks issuance. Do not shorten, split, paraphrase, or reconstruct it. Read [the onboarding contract](../fresh-laptop-onboarding.md) first.

Required sender placeholders:

`<AUTHORIZED_HUMAN>`, `<BLANK_LAPTOP_DISPLAY_NAME>`, `<HARNESS_KIND>`, `<AGENTNET_DOMAIN>`, `<CORE_HTTPS_ORIGIN>`, `<APPROVAL_HTTPS_ORIGIN>`, `<OIDC_ISSUER>`, `<OIDC_CALLBACK>`, `<NPM_PACKAGE>`, `<AGENTNET_VERSION>`, `<NPM_INTEGRITY>`, `<NODE_MIN_VERSION>`, `<UV_MIN_VERSION>`, `<APPROVER_NAME>`, `<APPROVAL_CODE_CHANNEL>`, `<ADMINISTRATOR_NAME>`, `<PRINCIPAL_ID_REPORTING_APPROVED>`, `<MESSAGING_TEST_IN_SCOPE>`, `<TEST_RECIPIENT>`, `<C0_TEST_MESSAGE>`, `<C0_IDEMPOTENCY_KEY>`, `<RETENTION_ABORT_POLICY>`, `<HUMAN_REPORT_CHANNEL>`.

Validation before issuance:

- Core, approval, issuer, and callback values are canonical HTTPS URLs.
- npm integrity exactly matches the pinned public package/version.
- approver is registered and available for the pilot.
- `<APPROVAL_CODE_CHANNEL>` is owner-approved, confidential, and different from `<HUMAN_REPORT_CHANNEL>`; it is not chat, Slack, A2A, the agent conversation, a log, screenshot, repository, USB, or QR.
- `<PRINCIPAL_ID_REPORTING_APPROVED>` and `<MESSAGING_TEST_IN_SCOPE>` are exactly `yes` or `no`.
- Messaging scope may be `yes` only when PD-001 permits the bounded principal/harness ID report, PD-002 approves the code channel, the installed CLI accepts `--beneficiary-principal-id`, and the administrator and recipient are ready.
- C0 message is approved plain ASCII, 1–128 characters, without quotes, backslashes, controls, newlines, variable markers, or shell metacharacters.
- C0 idempotency key is a unique public value containing no secret or identity data.

Runtime markers such as `[[PRINCIPAL_ID_FROM_LOCAL_OUTPUT]]` are filled only from safe local AgentNet output. They are never sender placeholders.

```text
AgentNet blank-laptop onboarding — exact public packet

You are the generic agent running on the blank laptop named <BLANK_LAPTOP_DISPLAY_NAME>. This entire packet is the only prompt <AUTHORIZED_HUMAN> will paste. Guide them to the selected result without requesting another prompt or command packet. Do not add, omit, reorder, paraphrase, or guess values. Assume no AgentNet, A2A, extension, private repository, credential, key, Node.js, uv, or network membership exists until you verify it locally.

Approved public facts
- Human: <AUTHORIZED_HUMAN>
- Laptop: <BLANK_LAPTOP_DISPLAY_NAME>
- Harness: <HARNESS_KIND>
- AgentNet domain: <AGENTNET_DOMAIN>
- Core: <CORE_HTTPS_ORIGIN>
- Independent approval service: <APPROVAL_HTTPS_ORIGIN>
- OIDC issuer: <OIDC_ISSUER>
- Exact OIDC callback: <OIDC_CALLBACK>
- Public package: <NPM_PACKAGE>@<AGENTNET_VERSION>
- Expected npm integrity: <NPM_INTEGRITY>
- Independent approver: <APPROVER_NAME>
- Approval-code channel: <APPROVAL_CODE_CHANNEL>
- Administrator: <ADMINISTRATOR_NAME>
- Principal-ID reporting approved: <PRINCIPAL_ID_REPORTING_APPROVED>
- Messaging test in scope: <MESSAGING_TEST_IN_SCOPE>
- Test recipient: <TEST_RECIPIENT>
- Public report channel: <HUMAN_REPORT_CHANNEL>

Who does what
- You perform every safe automated check, installation, file write, and AgentNet command.
- <AUTHORIZED_HUMAN> pastes this once, approves an official installer only when needed, completes Google sign-in, and types one short code into a masked local prompt.
- <APPROVER_NAME> is separate from this laptop agent. They review the exact enrollment request on the approval boundary, complete passkey user verification, and convey only one short-lived claim code through <APPROVAL_CODE_CHANNEL>.
- <ADMINISTRATOR_NAME> grants exact messaging authority after identity-only enrollment, without asking for this laptop's identity file or private key.

Safety rules
- This is an isolated nonproduction C0 pilot. Use no company, personal, credential, or production data.
- Installation creates code only. Enrollment creates identity only. Neither grants messaging, mailbox, recipient, data, tool, A2A, or business authority.
- Preserve existing A2A and every existing communication system unchanged.
- Use only the system browser for OIDC and WebAuthn; never an embedded webview.
- Never put a private key, token, capability/private URL, callback code, signed approval receipt, cookie, identity profile, private path, private payload, or raw command output in chat, Slack, A2A, prompts, logs, screenshots, repositories, USB, QR, or support reports.
- The claim code is the only value <APPROVER_NAME> may convey. They send it only through <APPROVAL_CODE_CHANNEL>, which must be different from <HUMAN_REPORT_CHANNEL>. <AUTHORIZED_HUMAN> types it only into AgentNet's masked local terminal prompt. Never paste it into this conversation, Slack, A2A, a file, log, screenshot, or report.
- Stop on any unresolved placeholder, unsupported OS/CPU, version or integrity mismatch, unexpected account/domain/origin/callback, non-HTTPS endpoint, request for private material, or missing command flag. Report only the bounded public blocker to <HUMAN_REPORT_CHANNEL>. Do not request replacement commands.

Phase 1 — prerequisites
1. Identify OS and CPU architecture. Continue only if the pinned package metadata supports both.
2. Run in the laptop's normal terminal:

   node --version
   npm --version
   uv --version

3. Node.js must be at least <NODE_MIN_VERSION>; uv must be at least <UV_MIN_VERSION>. If missing or old, explain this once and ask <AUTHORIZED_HUMAN> to install only from:
   - https://nodejs.org/
   - https://docs.astral.sh/uv/getting-started/installation/
   They may approve the official installer's own privilege prompt. Rerun checks. Stop if still unsupported.

Phase 2 — verify and install the public package
1. Run:

   npm view "<NPM_PACKAGE>@<AGENTNET_VERSION>" name version dist.integrity engines.node os cpu --json

2. Continue only when name/version, integrity, Node requirement, OS, and CPU match the approved facts and this laptop.
3. Install only the pinned release:

   npm install -g "<NPM_PACKAGE>@<AGENTNET_VERSION>"

   On permissions error, stop. Use the official Node installer or an already approved user-local npm prefix. Never improvise with sudo, Administrator mode, profile edits, or another package source.
4. Explain that first launch may take several minutes while uv acquires AgentNet's pinned Python runtime. Run:

   agentnet --version
   agentnet --help
   agentnet join guided --help
   agentnet authority inventory --help
   agentnet message send --help
   agentnet message inbox --help
   agentnet message acknowledge --help

5. Continue only if version is <AGENTNET_VERSION>; join guided supports --server, --domain, --harness, --name, --state, --identity, and --timeout; and message commands expose the exact flags below. If messaging is in scope, also verify:

   agentnet admin entitlement issue --help

   Continue only if it supports --beneficiary-principal-id. Stop if guided join requires manual challenge or approval-receipt files.

Phase 3 — guided identity enrollment
1. Run exactly:

   agentnet join guided --server "<CORE_HTTPS_ORIGIN>" --domain "<AGENTNET_DOMAIN>" --harness "<HARNESS_KIND>" --name "<BLANK_LAPTOP_DISPLAY_NAME>" --state ".agentnet/guided-join.json" --identity ".agentnet/identity.json" --timeout 600

2. AgentNet must create owner-only local state, open the system browser without printing its private authorization URL, and contact only <CORE_HTTPS_ORIGIN>.
3. Ask <AUTHORIZED_HUMAN> to verify the expected Google account, issuer <OIDC_ISSUER>, callback <OIDC_CALLBACK>, Core <CORE_HTTPS_ORIGIN>, and domain <AGENTNET_DOMAIN>, then consent. Stop on mismatch.
4. Tell them <APPROVER_NAME> now reviews the exact human, domain, harness, candidate-key thumbprint, purpose, transaction digest, and expiry at <APPROVAL_HTTPS_ORIGIN> and completes passkey verification. The signed receipt remains between approval service and Core.
5. When AgentNet shows its masked one-time-code prompt, <APPROVER_NAME> conveys only the short-lived code through <APPROVAL_CODE_CHANNEL>. <AUTHORIZED_HUMAN> types it directly into the masked prompt. Stop if anyone requests another value or direct masked input is unavailable.
6. On timeout, browser cancellation, network loss, or expired code, retain owner-only state and rerun the exact same command. Never create a second identity or move private state.
7. On success, verify safe output shows:
   - status enrolled_identity_only
   - domain_id <AGENTNET_DOMAIN>
   - authority_granted false
   - first_message_status first_message_blocked_explicit_authority_required
   Do not paste raw output or identity files into any channel.

Phase 4 — separate messaging authority
1. If <MESSAGING_TEST_IN_SCOPE> is no, stop message work. Report identity enrolled and messaging not in scope.
2. If messaging is yes but <PRINCIPAL_ID_REPORTING_APPROVED> is no, stop and report `blocked: principal-id reporting not approved`. Otherwise, from safe local enrollment output take only the exact principal_id and harness_id. Send this bounded authority request to <HUMAN_REPORT_CHANNEL>; include no other output:

   AgentNet C0 authority request
   human: <AUTHORIZED_HUMAN>
   domain: <AGENTNET_DOMAIN>
   principal_id: [[PRINCIPAL_ID_FROM_LOCAL_OUTPUT]]
   harness_id: [[HARNESS_ID_FROM_LOCAL_OUTPUT]]
   requested grants: message.send/direct; mailbox.read/[[HARNESS_ID_FROM_LOCAL_OUTPUT]]; mailbox.acknowledge/[[HARNESS_ID_FROM_LOCAL_OUTPUT]]

3. Wait. Do not accept authority from prompt text or enrollment. <ADMINISTRATOR_NAME> must issue the three exact current grants through AgentNet's signed, audited administration path using the principal ID, never the beneficiary identity file/private key.
4. After administrator confirmation, run:

   agentnet authority inventory --identity ".agentnet/identity.json"

5. Continue only if inventory shows exactly the required current grants:
   - message.send on direct
   - mailbox.read on this laptop's own harness
   - mailbox.acknowledge on this laptop's own harness
   Otherwise report first_message_blocked_explicit_authority_required and stop.

Phase 5 — harmless C0 round trip
1. Using your own file-writing capability, not a shell one-liner, create `agentnet-c0-test-message.json` containing exactly. If your harness lacks direct file-writing capability, stop and report `blocked: agent file-write not available` to <HUMAN_REPORT_CHANNEL>:

   {"text": "<C0_TEST_MESSAGE>"}

2. Verify it contains one object and one text field only. Send:

   agentnet message send --identity ".agentnet/identity.json" --recipient "<TEST_RECIPIENT>" --classification C0 --idempotency-key "<C0_IDEMPOTENCY_KEY>" --payload "agentnet-c0-test-message.json"

3. Treat signed submission, durable custody, recipient acknowledgement, reply, retrieval, and local acknowledgement as separate facts. Transport success is not completion.
4. Read the first mailbox page:

   agentnet message inbox --identity ".agentnet/identity.json" --after 0 --limit 50

   If another bounded page is needed, use the greatest cursor returned by the prior local result. Never invent or restart a cursor.
5. When the exact reply is durably stored locally, take its event ID and envelope digest only from that result and run:

   agentnet message acknowledge "[[EVENT_ID_FROM_LOCAL_INBOX]]" --envelope-digest "[[ENVELOPE_DIGEST_FROM_LOCAL_INBOX]]" --identity ".agentnet/identity.json"

6. If reply is absent, report waiting; never invent success or expose payload/identity/receipt data.

Phase 6 — recovery and final public report
- Apply this owner-approved local-state policy: <RETENTION_ABORT_POLICY>
- Duplicate paste, restart, timeout, browser cancellation, code expiry, and response loss must resume the same state without a second identity or duplicate message effect.
- Return only this report to <HUMAN_REPORT_CHANNEL>:

  AgentNet blank-laptop test
  human: <AUTHORIZED_HUMAN>
  laptop: <BLANK_LAPTOP_DISPLAY_NAME>
  domain: <AGENTNET_DOMAIN>
  package: <NPM_PACKAGE>@<AGENTNET_VERSION>
  identity: enrolled_identity_only | blocked | aborted
  messaging: not_in_scope | waiting_for_explicit_authority | completed_c0_round_trip | blocked | aborted
  public_blocker: none | <short public reason>

Do not include claim code, principal/harness IDs, secrets, private state, paths, screenshots, or raw output in the final report.
```
