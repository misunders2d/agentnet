# AgentNet v0.9.2 regular app release

Owner authorization, October 22, 2026: “release the app! only play store later.”
This supersedes the earlier regular-release hold. Publish the normal desktop,
CLI/server and signed Android preview release, and update the existing relay.
Play Store distribution, signing/provisioning, legal publication, reviewer access
and shared-push services remain parked. Preserve the Play checkpoint and both
owner-authored drafts; never reset or discard the shared working tree.

Root is the only checklist/source editor and owns integration, publication and
deployment. Existing GPT-6.1-sol/xhigh workers provide bounded read-only scouts;
the same Claude conversation `c86bd851-a8b5-43cf-b989-27cd6ac81faa` through Herdr
provides the overengineering/release guard. Use one local Go invocation at a
time, GOMAXPROCS=4 and GOFLAGS=-p=1, retaining all existing bounds/assertions.

| ID | Target outcome | Acceptance check | Owner / verifier | State | Evidence |
| --- | --- | --- | --- | --- | --- |
| R1 | Isolated regular-release candidate preserves parked Play source and includes the approved messenger fixes | Explicit file/hunk inventory, clean candidate tree and preserved owner/checkpoint hashes | root / packaging scout / existing Claude | in progress | Current source and prior evidence in OCT21_NON_PLAY.md |
| R2 | All seven locally timing-sensitive cases pass the actual candidate gate | Full candidate race CI, without timeout/fixture changes; preserve local failures and exact CI proofs | root / CI / Claude | awaiting candidate CI | All seven independently verified PASS on prior CI39577187806 at9133893b; current candidate still needs its own run |
| R3 | Release source, all skins and native platforms satisfy the required gates | Final vet, affected local gates, actual mixed-version test against latest published v0.9.1, all-skin candidate conformance, native CI and artifact builds | root / CI / existing Claude | pending | Retain still-valid passing evidence; no skips reported as passes |
| R4 | Published app artifacts and existing-relay cutover match the qualified source | Immutable tag; downloaded checksums/revisions; signed preview continuity; verified stopped-state backup/restore; unchanged realm, volume and environment; post-cutover health and public version | root | pending | Reuse verified v0.9.1 deployment procedure; no reset, relink or live task replay |

Finish cheap focused corrections and integrate them before another costly suite.
No timeout increase, assertion removal, invented pass or baseline-failure waiver.
The same Claude D1 guard found prior real CI passes for all seven, independently
checked by root in `.agentnet/v092/prior-ci-seven-parent-proof.json`. Stop local
timing diagnosis and qualify the isolated candidate once through the established
full go workflow, including all18 race shards, others, native systems and six
Windows client runners. Fix only failures that actually recur on the candidate.
Do not reopen unrelated backlog or build a notification/SQL framework. If a
diagnostic produces no actionable distinction, consult the existing guard before
adding observations or repeating it. A completed test process with failing
assertions does not qualify release.
