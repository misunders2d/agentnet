# Owner decision inputs

Status: **PD-001 and the ordinary-onboarding portion of PD-002 were recorded
from the accountable owner's explicit authenticated instruction on 2026-07-19.**
This is the governing repository decision for the bounded ordinary profile, but
it is not an independently signed O-tier artifact and does not pass an owner or
production gate. Remaining decisions and the optional high-assurance/production
evidence tier are not approved.

| Decision | Reversible implementation default | Launch blocker requiring accountable owner |
|---|---|---|
| PD-001 | opaque domain principal = OIDC issuer+subject; verified email alias/history; principal/harness IDs remain inside authenticated Core/Manager authority operations and are omitted from public human reports | migration/appeal semantics outside this bounded default |
| PD-002 | owner-controlled WebAuthn UV plus exact-transaction, purpose-separated possession-bound automatic Approval delivery retained by the waiting process; browser/human receives no code or capability; legacy 128-bit claim-code delivery remains compatibility-only; confirmation independent of the enrolling harness; colocated server profile allowed with `independent_boundary_proven=false` | separately administered approval host and stronger recovery/production evidence for the optional high-assurance tier |
| PD-003 | harness/device/session only attenuate; posture disabled | exact posture/appeal policy |
| PD-004 | one independent approver ordinary; two high-impact/break-glass | risk classes, approver sets, TTL/use, break-glass |
| PD-005 | retain inert accepted history only under lawful retention; conservative compromise quarantine | event matrix, erasure/hold, adjudicator |
| PD-006 | one sequencer, separate recovery threshold, from-join, transfer/tombstone | governance, guests/history, deletion/legal hold |
| PD-007 | C1 default; C2 isolated; C3 maintained MLS only, visible key holders | C3 launch/provider/retention/residency/legal decision |
| PD-008 | home proof ordinary; fresh host-local reproof high-risk | per-partner/resource/class/action assurance |
| PD-009 | kill next decision; no issuance during outage; short-token low-risk continuity | token TTL, signal SLO, outage/audit ceiling |
| PD-010 | Linux x86_64/arm64, one region, RPO=0 only for proven `accepted_durable` | RPO/RTO/capacity/residency/topology/admin/key/witness/retention |
| PD-011 | routine silence; content-free count; four redacted exceptional classes | channels, quiet hours, redaction/escalation |

## Recorded ordinary-onboarding decisions — 2026-07-19

The accountable owner approved these bounded defaults after reviewing the
usability and threat-model tradeoff:

- **PD-001:** OIDC issuer plus subject is the canonical principal; verified
  email remains an alias. Principal and harness identifiers may flow only
  inside authenticated Core/Manager operations needed for exact authority
  issuance and are omitted from public human reports by default.
- **PD-002:** the default self-hosted profile uses an owner-controlled WebAuthn
  authenticator to approve the exact enrollment transaction independently of
  the enrolling harness. Core, PostgreSQL, and approval may share the existing
  server under distinct OS identities. This profile reports
  `independent_boundary_proven=false`; separately administered approval is an
  optional high-assurance tier.
- **Updated 2026-07-28:** ordinary enrollment uses purpose-separated,
  possession-bound automatic Approval delivery. Exact waiting process retains
  Core continuation/begin state. Core derives a per-transaction OIDC Approval
  possession secret or generates a distinct high-entropy bootstrap secret,
  sends only that secret's SHA-256 hash, and retrieves the receipt through
  signed broker after WebAuthn UV. Browser and human receive no claim code,
  receipt, continuation, broker secret, or private URL. No extra person, host,
  Slack/A2A relay, copy/paste, or reporting channel is required. Legacy 128-bit
  claim-code delivery remains compatibility-only and is not ordinary onboarding.
- Recovery requires fresh OIDC plus WebAuthn and creates a new binding. It does
  not resurrect or copy a lost harness key.
- One owner may act as enrollee, WebAuthn approver, and messaging administrator
  in ordinary onboarding. OIDC authentication, passkey approval, candidate-key
  possession, and signed authority issuance remain distinct cryptographic acts;
  no role acquires authority merely because the human is the same.
- Ordinary setup uses one frozen consolidated approval. Separate confirmation
  remains required for a materially changed scope or a new destructive,
  restart, privilege-expanding, or high-risk action.
- AgentNet requires secure runtime injection but no named secret manager.
  Infisical and equivalent products are optional mechanisms, never onboarding
  prerequisites.
- For the isolated same-principal C0 pilot only, the owner approved one
  purpose-specific WebAuthn transaction with a one-hour ceiling. It may prepare
  exactly five communication entitlements plus five entitlement-specific revoke
  powers for the exact owner/fresh harness pair. The dedicated verifier must
  immediately revoke the five communication powers after all seven facts commit;
  identity-set drift permanently invalidates the guard. This bounded pilot
  instruction is not general elevation, relationship, messaging-administration,
  task, file, room, federation, A2A, server-agent, or wildcard authority.
- **Updated 2026-08-09:** the owner approved automatic reconciliation only for
  the exact ordinary-onboarding placeholder Approval owner after Core has
  enrolled the canonical OIDC issuer-plus-subject principal. Recovery must
  derive the target from enrolled Core evidence and Approval's pinned OIDC
  binding, accept only the bounded known source shapes, replace current signer
  authority without dual trust, preserve immutable historical receipts, and
  fail closed on ambiguity or drift. This is not a general principal merge,
  alias migration, appeal, account recovery, cross-domain migration, or
  production policy decision. The authenticated coding-session record is not
  independent signed O-tier evidence and does not close PD-001.


This decision does not approve production certification, the optional
independent-administration tier, high-impact/break-glass elevation, company
content, federation, C3, or unrelated PD/ORG decisions. It also does not convert
the repository candidate into O-tier policy evidence, authorize a live
ceremony/deployment, or close PD-004/005/009.

## ORG-006 relationship lifecycle policy — unapproved

The bilateral implementation is a safe mechanism, not an owner decision.
Proposals have zero authority; normal activation verifies exact current
subordinate human/guest-owner consent; policy exceptions and administrative
revocation require distinct exact signed actions; renewal, expiry, subject
exit, and races are revision-fenced. Immutable first-release storage schema v1
contains the bilateral authority model; current unreleased Core migrations 2
through 4 add protected payload-release receipts, guided OIDC enrollment
continuation, and the bounded C0 bootstrap-plan contract without altering or
retrofitting relationship authority. Unsupported pre-release or pre-N/N-1
stores are rejected and no unilateral edge is converted into consent.

An accountable owner must still record:

- who may receive proposal, policy-exception, and administrative-revoke
  entitlements, with any independent threshold and separation of duties;
- the allowed circumstances and scope/TTL for an exception or security/legal
  override, and how that authority is established;
- voluntary versus mandatory relationship rules, subordinate exit handling,
  notice, review, appeal, emergency suspension, and conflict escalation;
- relationship/exception/audit retention and privacy/legal handling; and
- the production approval service, operational owners, drills, and evidence.

Until that record exists, no route, entitlement, local test, exception record,
or administrative-revoke result may be described as approved policy or
production evidence. The implementation remains fail-closed when the exact
entitlement, signature, current revisions/epochs, or schema evidence is absent.

Immediate owner blockers are all eleven recorded PD decisions plus ORG-006,
especially the qualifying independent enrollment boundary, production
topology/durability,
C3/model-provider policy if C3 is desired, and partner assurance/revocation if
federation is desired. Work not dependent on those decisions continues under
the safe defaults without being described as launched.
