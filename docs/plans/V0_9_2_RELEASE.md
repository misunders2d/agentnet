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
| R1 | Isolated regular-release candidate preserves parked Play source and includes the approved messenger fixes | Explicit file/hunk inventory, clean candidate tree and preserved owner/checkpoint hashes | root / packaging scout / existing Claude | verified | Frozen source628f30db; Play checkpoint and both owner drafts preserved; clean isolated worktree |
| R2 | All seven locally timing-sensitive cases pass the actual candidate gate | Full candidate race CI, without weakening bounds or assertions; preserve local failures and exact CI proofs | root / CI / Claude | verified | Candidate CI39736942578 plus affected-shard replacements39741160055/39743661026; retained exact coverage below |
| R3 | Release source, all skins and native platforms satisfy the required gates | Final vet, affected local gates, actual mixed-version test against latest published v0.9.1, all-skin candidate conformance, native CI and artifact builds | root / CI / existing Claude | verified | MixedVersion39741924136 all3 OS; Windows original partition0 selection39744304946; gallery39749602975; packaging39751343909 |
| R4 | Published app artifacts and existing-relay cutover match the qualified source | Immutable tag; downloaded checksums/revisions; signed preview continuity; verified stopped-state backup/restore; unchanged realm, volume and environment; post-cutover health and public version | root | verified | Published app/gallery; all13 public files match verified draft; native backup and both restores; public HTTPS same realm; clean628 binary, restart0 and signed traffic |

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

Final source is `628f30dbcbaff103a6edff6657a03dd2b1efbe7e`.
The complete initial candidate run was [39736942578](https://github.com/misunders2d/agentnet/actions/runs/39736942578).
Its unchanged passing selections are retained. Focused replacements cover every
changed selection: client14 in39741160055; client1/4/17 and all three UI groups
in [39743661026](https://github.com/misunders2d/agentnet/actions/runs/39743661026).
All18 client groups and all non-client/non-UI race packages are accounted for;
every parent remains in exactly one partition. The owner explicitly approved
three UI groups and balancing within18 client groups on October22. The600-second
bound, individual deadlines and assertions are unchanged. Three fixture
corrections preserve literal SQLite file identity on macOS, make the legacy-cap
fixture stable against a live daemon, and isolate the idle-traffic observation
from unrelated parallel load. MixedVersion extracts a relative archive path on
Windows instead of letting GNU tar interpret a drive letter as a remote host.

[39741924136](https://github.com/misunders2d/agentnet/actions/runs/39741924136)
passes vet and the real mixed-version journey on Linux, macOS and Windows.
[39744304946](https://github.com/misunders2d/agentnet/actions/runs/39744304946)
passes all231 parents from the original Windows client partition0 at the existing
1800-second bound. Other native, Windows client and desktop successes are
retained from the complete run. A skipped opt-in/platform case is not an
executed-test claim. Local failures and the initially failed CI jobs remain in
private evidence; none is relabelled as a pass.

Gallery [39744986088](https://github.com/misunders2d/agentnet-skins/actions/runs/39744986088)
found a person-card navigation bug in Classic and Zoom while linking a fresh
browser. A deterministic rendered RED reproduced waiting for an unsynced latest
message; ordinary person clicks now use the existing Main-chat route. A second
phone RED pinned Classic's hidden same-chat pane after returning to Chats. Both
fixes pass15 focused rendered checks with zero skips, keeping exact notification
Retry, wrong-target, draft and actual File assertions. Eight affected previews
were regenerated and inspected on core628. The same Claude conversation reviewed
the minimal corrections without a blocker. The integrated collection at45ba32c
passes [39749602975](https://github.com/misunders2d/agentnet-skins/actions/runs/39749602975).

Evidence inventories remain private under `.agentnet/v092/`: the retained race
coverage, exact partition conservation, changed-selection parent accounting,
source equivalence, focused RED/GREEN logs and preview verification. Only tests,
AGENTS and partition metadata changed after the complete candidate's product
source; gallery navigation fixes are confined to agentnet-skins.


Release completed October22, 2026. [App v0.9.2](https://github.com/misunders2d/agentnet/releases/tag/v0.9.2)
was published at09:05:49UTC from immutable core628. [Release packaging39751343909](https://github.com/misunders2d/agentnet/actions/runs/39751343909)
passes Linux, macOS, Windows, six CLI targets and signed Android preview builds.
All12 payloads plus SHA256SUMS were downloaded independently from both draft
and unauthenticated public HTTPS. Every size, GitHub digest and checksum agrees.
All six CLI binaries and the AppImage sidecar report clean628 provenance; Linux
runtime reports v0.9.2. The existing APK verifier confirms the original signer,
preview package, versionCode9002, nondebuggable build, both ABIs and16KiB alignment.
Other installers are checksum-verified; they were not executed on this laptop.

The [matching gallery v2026.10.22.1](https://github.com/misunders2d/agentnet-skins/releases/tag/v2026.10.22.1)
contains the qualified45ba32c packages plus documentation-only childa204d9b.
Its complete CI passes every applicable journey; Node158passed/20optional skips
and the separate focused rendered navigation15/15 are distinguished. The actual
public gallery loader verifies all5 packages,125package files and20previews over
147unauthenticated HTTPS requests, using memory-only install storage.

Existing-relay cutover completed successfully at09:12:51UTC. It retained a native
backup at `/opt/agentnet-revived/deploy/v0.9.2-628f30db-20261022T090930Z/hub-v0.9.1-before-v0.9.2.tgz`
(SHA256 `203483ea35c943e8c941401c1fabd6a368c75d92262eb665bdb30d18921de0f7`).
The archive verified7878files. Previous-binary restore is exact; new-binary restore
preserves all existing rows/files and adds only the2Drive tables, schema23→24.
The same realm, original volume, raw HostConfig, environment and neighboring
service remain. Public TLS `/v1/version` reports v0.9.2; the running binary SHA256
is `36e1f1d9e85f507077a5e1cf61e5dd184c53bedf18f40ed96c02642c3a2a9fcd`,
matching published checksums, with clean628 provenance and zero restarts. Passive
post-cutover observation counted183signed requests; no synthetic user message was
sent. Default30-minute update grace remains, ending09:42:48UTC. Actual post-grace
live enforcement was not observed; the controlled mixed-version gate covers it.

Play Store, shared push, reviewer provisioning and legal publication remain
parked. No live Google consent or attachment migration was performed. Physical
Mac/Windows/Android UI acceptance and unrelated Linear backlog are not completion
claims. Owner drafts and the original Play checkpoint remain preserved.
