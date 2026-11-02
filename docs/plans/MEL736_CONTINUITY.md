# MEL-736: one person after recovery (Option A)

Owner rule: “Same person with same email is accepted as same previous
identity.” Fresh Google verification plus admin Remove/re-allow is sufficient;
colleagues do not accept keys individually. This is the recorded messaging
trust exception in DECISIONS.md. Offline status is never removal evidence.

Phase 1's request/approval protocol remains parked, pushed and unmerged on
`t3/mel736-continuity` (`fa134267`). This implementation introduces no Hub
endpoint, table, signed successor record, dependency or group replacement
protocol. Existing Remove revokes the old devices; re-allow and Google
sign-in establish the current person.

## Derived identity

Each workspace derives messaging equivalence from its verified pinned person
chains, equal normalized Google email and exact Hub revocations. Before email
settlement, fetch the old chain including its pinned head and verify every
following step, then check every current device against the authenticated
Hub directory. A local config/KV completeness checkpoint binds these checks
to that exact head. It is a cache of authenticated evidence, not a recovery
attestation or permission. Persist revocations first and the checkpoint last;
a newly discovered head invalidates the previous checkpoint. Interrupted or
failed checks remain split and retry through existing member/push work.

Exactly one non-left pinned/self person must remain for an email. Live,
unknown, conflicted or ambiguous candidates cannot establish equivalence.
Multiple removed generations resolve directly to the current terminal.
Startup reconstructs the projection from saved evidence; it needs no alias
table. Original keys, roster hashes, signatures and person IDs remain intact.

## Messaging and groups

Only recovered-person DM families collapse. Ordinary distinct DMs stay distinct.
The oldest root (creation time, then ID) names the view; either old or new link
opens that view. Union stored timelines by message ID, retaining the original
rows, receipts, read generations, reminder references and replies. New human
messages use an existing valid current-person DM root, or create one; no old
root gains a forged member. Cross-root human replies require the same logical
DM family. Native context/grant APIs retain exact conversation scope. Controls
resolve back to the selected message's original root.

Current group membership, devices, admission epochs and ordinary incoming
human authors use the same equivalence. Historical signature verification
keeps original signed members/admissions. Filter withdrawals/removals before
deduplication; an explicit restored current slot wins over its older duplicate.
Reuse the existing encrypted proof/current-context carriers and durable outbox
batch markers to reach the recovered device. No group administrator signs a
replacement slot. Original group governance signatures and native execution
permissions remain exact; this change does not create new admin signatures,
transfer task grants, replay jobs or recover ciphertext whose keys were lost.

A deterministic local-only inbox row records “<Name> signed in again” per
transition and affected conversation. Existing conversation storage holds it;
it is already read, never sent as signed history, never a task and never a
native permission. A later recovery creates a new notice while retaining the
previous notice. The new key starts with its own verification state.

## Counterpart evidence reused

- Signal Desktop `5dad34ad5ea1a5b6229412e5c8ac0a696ff7532d`,
  `ts/SignalProtocolStore.preload.ts`, `isTrustedForSending` and `saveIdentity`:
  conversation identity separate from replaceable keys, durable key-change
  notice, verification reset. Signal's peer trust policy is not claimed as
  identical; the owner's Hub-approved exception governs AgentNet.
- Telegram Desktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`,
  `Telegram/SourceFiles/api/api_authorizations.cpp`,
  `MTPDupdateNewAuthorization`: account/session separation. A peer-visible
  sign-in notice is AgentNet's explicit adaptation, not claimed Telegram parity.
- Slack's authoritative [SSO account association documentation](https://slack.com/help/articles/220766827-Connect-your-SSO-account-with-Slack)
  preserves history associated with the existing account. Internal merge and
  peer notice behavior are unverified because Slack is closed source.

Current verification evidence and remaining release gates belong in HANDOFF.md
and INTERACTION_COVERAGE.md. This branch is for review, not deployment.
