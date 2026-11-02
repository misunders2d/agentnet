# MEL-736: lost-every-device continuity

Owner approved November 2, 2026: “Same person with same email is accepted as
same previous identity.” Fresh Google proof plus independent admin approval
establishes messaging continuity. No colleague acceptance is required (Option B).
This is the recorded exception to changed-key blocking; native execution grants
never transfer.

## Phase 1: protocol, Hub and CLI

A versioned `agentnet-person-successor-v1` canonical JSON record binds realm,
explicit old person and exact head, enrolled new person and genesis, normalized
email, creation time, nonce and current new human signer. Its signature is
consent, not independent proof of admin authority. Only a committed record
obtained through the authenticated Hub establishes continuity.

Fresh Google credentials must have been issued within five minutes. The request
must initially be created within fifteen minutes; exact admitted retries remain
valid with fresh Google proof after that window. Tokens remain in memory. The old email
comes from the retained signed roster, including after Remove deleted its Google
binding. Archived Google subject equality is not required. No email-only merge.

One appended SQL step stores pending, denied and committed records. A partial
unique index prevents two pending/committed successors for an old identity.
Denied requests remain audited; a new explicit consent can retry. Exact retries
return their existing record. Targets already replaced are refused, preventing
cycles. SQLite's existing immediate transaction and single writer serialize
concurrent approvals.

Approval revalidates heads and consent, revokes old roster keys, pending and
detached old devices, settles custody, expires old invitations, swaps the Google
binding, preserves allowed email and commits actor/status together. Successor
associations are excluded from the old email's detached-device revocation.
No old signed history or local task permissions are rewritten. Admin self
approval is refused. Offline host fallback holds the existing maintenance lock
and records `hub-operator`. Existing members-stream discovery generation wakes
clients; authenticated fetch by old/new ID supports restart catch-up.

Commands:

- `agentnet recover --from OLD-PERSON-ID` from an enrolled new human device;
- `agentnet admin google recovery list|approve ID|deny ID`;
- `agentnet hub recovery --data DIR approve ID` with the Hub stopped.

Phase 2 must consume committed records in Go, verify old/new chains and current
revocations, cache proofs durably, resolve DM presentation/routing without
rewriting signed roots, reset verification and add one durable notice. Browser
parity and controls follow separately. Phase 1 alone does not merge chat rows.
Groups remain under existing membership signatures; no slot-replacement protocol.

## Counterparts reused

- Signal Desktop `5dad34ad5ea1a5b6229412e5c8ac0a696ff7532d`,
  `ts/SignalProtocolStore.preload.ts`, `isTrustedForSending`/`saveIdentity`:
  separate conversation and replaceable key identity, durable key-change event,
  verification reset. Automatic Hub-approved trust is AgentNet's owner-approved
  adaptation, not a claim of identical Signal trust policy.
- Telegram Desktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`,
  `Telegram/SourceFiles/api/api_authorizations.cpp`, `updateNewAuthorization`:
  account/session separation. No cloud-history recovery claim.
- Slack [SSO association](https://slack.com/help/articles/220766827-Connect-your-SSO-account-with-Slack):
  preserve account association and history. Closed-source merge mechanics and
  peer-visible notices remain unverified.

These are the supplied design's inspected references, not new live comparisons.
