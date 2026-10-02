# Messenger release v0.6.0 — 2026-10-02

Published stable [v0.6.0](https://github.com/misunders2d/agentnet/releases/tag/v0.6.0) from
`49d0f545da8d33dc13e8c4775048b9ee78afa297`; all seven uploaded asset
digests verified.
[CI 36982671958](https://github.com/misunders2d/agentnet/actions/runs/36982671958): native
Linux/macOS/Windows, container and non-client race checks passed. The client
race aggregate timed out at 1800s without an assertion failure or race
diagnostic; the following Chromium stage was skipped. CI is not all green.
Production: relay HTTPS and both managed client services report v0.6.0.
Stopped-state backups were retained; client doctor checks exited 0 and identity,
config, grants, peer pins and history were preserved. The observed client upgrades
migrated schema 12 (v0.2.1) and schema 22 (v0.5.0) to schema 35.
Authenticated page and served-app digest checks passed; full production
navigation remains unqualified by the read-only smoke.

This release contract supersedes older “later”, device-keeper, and isolated-skin
proposals where they conflict. Earlier release evidence remains historical.
The initial candidate CI had macOS fixture failures; corrected final CI is
recorded separately above.

Released scope: human/group conversations, named member and
outside-host participations, selected receivers, linked files/history,
controls/typing and browser-local interfaces have scoped qualification.
Historical pre-release validation was **composite coverage, not one green command**:
436 client tests passed, 6 opt-in tests skipped; the original whole command timed
out. Migration snapshot/rollback checks pass on synthetic databases. Actual
same-source process replacement does not establish historical schema upgrade
or physical-platform safety. Source/CLI pointers are in the
[handoff](HANDOFF.md) and [opt-in examples](../README.md#opt-in-cli-selection-in-this-candidate).

Teams snapshot review/invitations and selected history/files have scoped
native 390px/browser 1280px qualification (MEL-502: In Review). Stale proposals
require explicit renewed consent; later team changes grant no group authority.
Unverified limits accepted for the owner-authorized production rollout: actual
Linux visible notification/click routing; physical Windows/macOS/Android checks;
live non-production Drive permissions. Native CI does not establish those
physical/live outcomes.
Pi/OMP idle, busy/tool, file and restart delivery and Codex busy queue/clean-close
backup have scoped actual evidence. OMP 18.4.8 optional clean-close backup has scoped actual evidence with a
deterministic synthetic managed executable; original session/file preserved,
backup once after daemon restart. Claude selected-session idle continuation has
actual product evidence: one exact physical reply ACK and a native turn boundary,
not business-tool effects. Scoped dispatch-busy continuation is also accepted:
a second signed input waits durably during native prompt dispatch, then two
ordered native turns/physical ACKs occur, default0/no replay, without a terminal
nudge. In-flight sampling/tool timing remains untested coverage. Claude detached
SessionEnd does not authorize backup. No blanket harness completion is claimed.
The owner authorized production publication and rollout with those limitations
disclosed. Publication and individual production upgrades are established only
by the verified records above, not inferred from scoped tests.

The release must satisfy all open requirements in the Agent Net Revived Linear
project, plus the owner's recorded messenger and storage requirements. There
is no smaller cosmetic release substituted for this goal. Codex coordinates;
GPT-6.1-Sol subagents implement; AGY independently reviews stabilized
candidates. Codex and AGY are the two persistent agents. One writer owns each
shared source region. This ownership follows the owner's October 1 goal.

## Mandatory reuse and verification gate — owner correction

This gate covers all code, modules and user flows, including existing code
and the current unreleased candidate. It is not limited to group membership
or security-sensitive modules. The October 1 goal resumes implementation
using completed comparisons and valid evidence; do not reopen settled reuse
decisions. Preserve work already written and any separately unresolved
approval gates.

For each component, identify existing implementations through primary
upstream sources, inspect their relevant behavior and evidence, and record
what AgentNet actually reuses. Reuse existing implementations. Custom code
or logic requires a documented missing capability or incompatibility with
existing implementations. Preference or convenience is insufficient.
Code already written, dependency count,
or passing AgentNet's own tests does not justify reinventing a capability.
Unsearched or uncertain areas remain unaccepted.

Record the requirement, current code, upstream candidates, actual reuse,
any precise missing capability, compatibility/migration checks and independent
review disposition. Distinguish conformance tests, interoperability, audits
and deployment evidence; none alone proves all of the others. Verify complete
flows as well as individual modules. Resolve unclosed comparisons before
accepting the affected component; do not claim release readiness before the
full gate is satisfied.

Reuse checkpoint: browser IndexedDB plumbing now uses pinned `idb`; browser
and native SSE framing use `eventsource-parser` and `go-sse`; process locks
use `gofrs/flock`. Owner checks and independent focused review support these
bounded replacements. They do not establish whole-release acceptance or
physical-platform verification. Provider SDK integration, application
authority rules and remaining end-to-end journeys retain their own gates.

## Product and authority

- A workspace contains independently enrolled people and services. A person
  has linked devices. A team is a manageable many-to-many member collection,
  with self-service joining and leaving; selecting a team expands to named
  people for an explicit conversation invitation. Team membership grants no
  history, conversation membership, or execution permission.
- Conversations have explicit human audiences and separately invited agent
  participations. Each request identifies one agent and exact execution host.
  Ordinary discussion, mentions, reactions, revisions and status records do
  not start work. The composer keeps audience, addressed participant, reply
  and workspace visible and bound to its draft.
- Agent identity is a stable host-signed ID; a harness name is a local program
  choice, not identity. Multiple agents on one host must be independently
  addressable and dismissible. A running job keeps its stamped executor.
  Switching the next agent preserves the device's default responder and
  passes only allowed AgentNet context, never another harness's session files.
- Recipient/execution host, named executor, reply receiver and default
  background responder are separate choices. Explicit receivers bind before
  enqueue; missing or uncertain bindings never silently fall back. Human
  replies stay for the person; managed continuation requires original local
  instructions/mode, independently of remote task grants. Native registration
  is not liveness, input acceptance is not completed effects, and a normal
  shutdown backup requires explicit prior authorization and verified adapter
  shutdown. Claude detached SessionEnd alone cannot authorize that backup.
- Participation lasts through the first report and subsequent follow-ups
  until explicitly dismissed. An external host receives selected history and
  its participation's requests/outputs, not the whole human room. Host
  acceptance and existing exact-key task grants remain authoritative.
- Human intervention, stop and hand-back are explicit actions. Editing a
  question/task never changes its admitted execution input or reruns it;
  deletion never silently cancels it. Ordinary deleted text and revisions
  are removed from application storage; immutable request input needed for
  execution audit is retained and disclosed. Saved or previously read copies
  cannot be recalled.

## Groups and workspaces

Groups use signed conversation roots and person administrators, including
their authorized linked devices. An authenticated relay compare-and-swap
journal orders encrypted membership state; clients independently validate
admin signatures and roster authority. The relay sees the group identifier,
sequence and admin/writer relation. Group titles, ordinary members and history
grants remain encrypted. Concurrent valid admin writes retry against the
current head rather than freezing a group for normal multi-device use.

Here, journal means versioned membership records stored with SQLite's
existing transaction and conditional-update mechanisms. It is not a new
database journal, consensus service or group-key protocol. Existing age
encryption remains per recipient. Reusing SQLite establishes the storage
mechanism; consent, authority, withdrawal and crash-recovery behavior still
require their own acceptance evidence under the reuse gate above.

A newly admitted reader verifies the root-linked public administrator chain
and the complete current signed state and admission consents. This does not
claim independent validation of every historical hidden membership change.
Existing readers also validate consecutive transitions against their pinned
state. Authority transfer reuses original signed records, including their
opaque ciphertext, in bounded pages; it does not disclose old plaintext
membership snapshots or supply old decryption keys. Partial proof transfer
never makes membership usable. No new signature format is required.

The journal namespace also binds the original root creator's fingerprint.
Its first writer must prove that key; later admin transfers retain that
qualifier. Clients derive it from the verified root, never an untrusted push
hint. Another enrolled key cannot occupy the expected creator's journal by
claiming the same conversation ID. The conversation ID itself remains the
signed root's hash.

An ordinary member can leave through a signed withdrawal bound to that exact
admission. It cannot advance the admin journal. Verified withdrawals exclude
future delivery/execution and are folded into later admin state; old queued
state cannot silently readmit someone. Rejoining needs fresh consent. Leaving
does not wipe a person's keys or already fetched history. Revocation takes
effect when verified state arrives; no instantaneous offline revocation or
new group-ratchet guarantee is claimed. A late joiner receives only selected
earlier messages and their allowed files.

Existing direct-device threads retain their IDs, reply links and trust pins.
Do not flatten them into an unsigned synthetic root or add keyless authority.
Message actions and files must work in those daily-use conversations too.

Workspace IDs are persistent random names independent of TLS certificates.
Each enrollment uses its own store, keys, grants and sessions. A selector binds
immutable workspace hosts for sends, downloads, notifications and pending
operations. Switching preserves each workspace's draft and view. An offline
workspace retains readable local state; connecting checks pinned endpoint and
realm continuity. A copied ID or matching display name cannot merge stores.
This is application isolation, not protection from trusted code under the
same OS/browser account. Federation remains the separate MEL-436 design
deliverable; a working switcher is not a relay mesh.

## Attention, files and interfaces

Custody and delivery describe transport only. Agent work status is asserted
by the exact executing host, with time and stale/unknown states. Human typing
is a separate encrypted, ephemeral, bounded signal: no persistent messages,
jobs, polling or keystroke content. Relay routing/timing remains observable.

Bot clarification belongs in the original chat. Count-only remote reports are
snapshots, not local decisions or live queues. Actionable operator reports
require a locally granted exact operator key on the executing host; notice
content never creates that grant. A decision binds the request, key, state,
attempt and report, and must survive retry without duplicate execution/reply.
Notification clicks open the exact persisted item without accepting it.

Senders and recipients can open/download files when an actual retained copy
exists. Missing, expired, quota-limited or sibling-held copies have honest
states. Storage settings explain holder, encryption, retention, quota and
recovery. Existing manual relay cleanup is not described as automatic expiry;
no destructive retention default or plaintext permanent sender cache is
introduced implicitly. Losing all decryption keys is not fixed by retaining
more ciphertext.

Google Drive is optional and off by default. Settings → File storage options
lets the workspace admin configure a new or existing Google Cloud project,
using agent-guided gcloud commands where supported and the actual Google
consent/client setup flow. Public project/client settings are separate from
each person's local Google consent and credentials. Conversation spaces use
real create/connect/list/upload/access operations; a pasted link alone does
not satisfy MEL-490. Files uploaded to Drive leave AgentNet encryption, with
explicit confirmation. Enabling the provider grants no agent access and does
not silently change Google sharing on conversation join/leave. Tests against
synthetic APIs are not live Google verification.

Interfaces are client-owned packages. They share semantic messenger
capabilities while allowing different layouts. Required flows must work or
be explicitly unsupported, never silently disappear. Host-owned escape stays
reachable. Browser import/retain/select/remove must work without changing the
relay or another client. These packages currently execute trusted client
code; shadow DOM is styling isolation, not a hostile-code sandbox. Digest
consent, conformance evidence and advisory review must not be sold as proof
of harmlessness. Comic emotions and actual job states remain distinct;
missing expression metadata/assets render neutrally without holding replies.

Person display-name changes reuse signed roster updates and retain the person
ID, devices, keys, history and grants. A label or random ID does not verify a
real-world owner. Trust UI must distinguish pinned key/roster continuity from
first-contact reliance on the relay directory; it must not label a self-claimed
name as independently verified. Display-name changes do not rename routing
addresses.

## Acceptance and evidence

| Requirements | Required outcome |
| --- | --- |
| MEL-429/433/491/492/494/498 | Human/group navigation, linked devices and self-device discovery; clear audience/recipient; usable profile/settings at desktop and 390px |
| MEL-496/428/425 | Explicit responder onboarding, per-interaction agent choice, allowed context, follow-ups, dismissal and human intervention |
| MEL-497/426/435 | Usable headless clarification/operator decisions, honest progress, quiet reports and exact notification routing |
| MEL-489; storage requirements | Picker/paste/drop, preview/remove/send/open/save, exact bytes and direction, linked/offline availability and understandable storage lifecycle |
| MEL-476/477/478 | Authenticated convergent edits/deletes/reactions and ephemeral typing, both device threads and conversations, both runtimes |
| MEL-502 | Create/manage/self-join/leave teams, verified snapshot invitation into groups, no inherited authority |
| MEL-475 | Essential interface capabilities, browser-local packages, reliable escape, digest-bound conformance evidence |
| MEL-495/436 | Working workspace switching/isolation and separately documented seven-question federation exploration |
| MEL-490 | Optional real Drive space/provider setup and separate member/agent access controls |
| MEL-434 | Reviewed expression/asset/privacy/compatibility design and representative desktop/mobile/static/reduced-motion prototype; no fabricated resource claims |
| MEL-422/427/493 | Identity versus labels, explicit diagnostics/role, non-mutating inspection and accurate update/version wording |
| Install/release gates | Real installed-window proof on claimed platforms; migration/offline compatibility; full vet/race and integrated user journeys |

Each issue's full description and owner comments must map to implementation,
tests, rendered journeys and reviewer evidence before acceptance. Focused
passes establish only their tested scope. The final combined test run must
use a stable candidate. Physical OS/PWA checks and live Google checks remain
explicitly unverified until performed. Do not close tickets or publish/deploy
while required outcomes are missing. Private test artifacts remain outside
the repository and are not release assets.

Upgrade recovery must prove that an empty or interrupted backup cannot count
as a completed pre-migration snapshot. Existing valid snapshots must remain
intact, and a failed snapshot or migration must preserve the source data and
schema. Verify this with synthetic databases before accepting upgrade safety.

Explicit exclusion added by the owner: `agentnet tui`, both standalone and
possible Herdr integration, is a separate post-release debate and does not
block this release. See [DECISIONS §8](DECISIONS.md#8-terminal-messenger-request-post-release-sep-30).
