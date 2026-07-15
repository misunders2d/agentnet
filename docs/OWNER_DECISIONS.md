# Owner decision inputs

Status: **proposed safe defaults, not owner-approved**. This file is not an
approval record and cannot satisfy Gate 17.

| Decision | Reversible implementation default | Launch blocker requiring accountable owner |
|---|---|---|
| PD-001 | opaque domain principal = OIDC issuer+subject; verified email alias/history | identity owner accepts migration/appeal semantics |
| PD-002 | phishing-resistant primary auth plus exact transaction and independent device/boundary | approved channels/boundaries, recovery owners, TTL/entropy/throttling |
| PD-003 | harness/device/session only attenuate; posture disabled | exact posture/appeal policy |
| PD-004 | one independent approver ordinary; two high-impact/break-glass | risk classes, approver sets, TTL/use, break-glass |
| PD-005 | retain inert accepted history only under lawful retention; conservative compromise quarantine | event matrix, erasure/hold, adjudicator |
| PD-006 | one sequencer, separate recovery threshold, from-join, transfer/tombstone | governance, guests/history, deletion/legal hold |
| PD-007 | C1 default; C2 isolated; C3 maintained MLS only, visible key holders | C3 launch/provider/retention/residency/legal decision |
| PD-008 | home proof ordinary; fresh host-local reproof high-risk | per-partner/resource/class/action assurance |
| PD-009 | kill next decision; no issuance during outage; short-token low-risk continuity | token TTL, signal SLO, outage/audit ceiling |
| PD-010 | Linux x86_64/arm64, one region, RPO=0 only for proven `accepted_durable` | RPO/RTO/capacity/residency/topology/admin/key/witness/retention |
| PD-011 | routine silence; content-free count; four redacted exceptional classes | channels, quiet hours, redaction/escalation |

## ORG-006 relationship lifecycle policy — unapproved

The bilateral implementation is a safe mechanism, not an owner decision.
Proposals have zero authority; normal activation verifies exact current
subordinate human/guest-owner consent; policy exceptions and administrative
revocation require distinct exact signed actions; renewal, expiry, subject
exit, and races are revision-fenced. Immutable first-release storage schema v1
contains the bilateral authority model; current unreleased migration 2 adds only
protected payload-release receipts and does not alter relationship authority.
Unsupported pre-release stores are rejected and no unilateral edge is converted
into consent.

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
