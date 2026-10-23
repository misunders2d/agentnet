# AgentNet v0.9.3 — release, verification and rollout

[AgentNet v0.9.3](https://github.com/misunders2d/agentnet/releases/tag/v0.9.3)
was published October 23, 2026 at 07:49:58 UTC. Its immutable source is
`3f5bbc4362e1ee6345c676e9000ee742668c64ad`.
The matching [gallery v2026.10.23.1](https://github.com/misunders2d/agentnet-skins/releases/tag/v2026.10.23.1)
was published at 07:50:19 UTC from
`5a12224bbc212db800459a40b8d16d6312f5d618`, pinned to that core commit.
Both resolve as the public Latest release. The existing relay is healthy on
v0.9.3 from 08:54:05 UTC after its verified full backup and both restores.

Sergey explicitly approved publication with the Android first-menu failure,
remaining interaction timings above 200 ms and physical Pixel acceptance left
open. Those remain follow-up work, not passes. The nonshipping Windows
Playwright WebKit media cells are also unexecuted. Play Store/shared push work
stays paused; the owner confirms enrolled phones use preview APKs only.

## Delivered behavior and scope

The [October 22 batch](OCT22_BATCH_HANDOFF.md) and
[acceptance checklist](OCT22_BATCH.md) cover shared linked state/read/reminder
attention, read-aware native hooks, reactions, independent recipient file fanout,
bounded fair Drive work, storage management/errors/exact retry, opt-in media
preparation and original files, quiet Drive refresh/consent and Android attention.
Ordinary clarification uses tagged chat with exact Reply/tag continuation and
saved group-worker rounds. Native permissions and required authorization stay
with the actual host and harness.

Claude reviewed the candidate GUI and significant source changes, rechecked
demonstrated corrections on archived exact candidates, and reviewed the downloaded
draft artifacts. Earlier exploratory dirty-main interactions are excluded.
Corrections include touch-release click suppression, Mac/narrow mouse controls,
first-unread entry, native reading anchors, bounded list/history rendering,
keyboard/Back navigation, responsive editors, topic operations/destination names,
short menus, schedules, current invitations and structured human guest events.
The original thirty-minute historical request was not causally reconstructed.

## Composed qualification

Valid unchanged passes are retained; only affected selections are repeated.
Original aggregate failures, fixture mistakes and explicit skips remain recorded.
No timeout or correctness bound was increased to obtain a pass.

| Gate | Actual evidence and limits |
| --- | --- |
| Vet and Linux race partitions | Final product vet passes. All 18 client selections, remaining race packages and UI0/1/2 are accounted through integrated run39877403457 and its affected reruns. |
| Native Windows client | Six disjoint runners retain their valid results. Partition4 retains183 earlier parent passes and50 explicit skips; the final two parents pass count3 in run39895055440,96.451s. |
| Native macOS/client/UI | Full selections and affected repairs pass, including repaired UI models on all three OSes in run39889553253. |
| Historical itest hooks failure | Both named hook parents pass focused regular/race after delivery waits use inbox peek; full-itest Linux/macOS/Windows and others-race passes at25e remain valid for the unchanged direct hook source/tests. Original9b aggregate FAIL remains historical. |
| Required mixed-version gate | On final3f5 Go against publishedv0.9.2, TestMixedVersion passes72.564s with compatibility required: healthy traffic, suspension, retained sends and update/backlog catch-up. This supersedes the stale86.024s result. |
| Actual desktop interactions | Run39888155855 passes macOS/Linux17 native workloads and applicable browser cases; affected39895057632 and final39908576827 complete the changed cases. Final76da desktop jobs all pass, each with native checks and45 browser reports. Current Go/UI/contract/static production bytes are identical to76da. |
| Windows media | Actual Tauri WebView2 and Chromium/Firefox media pass. Extra Windows WebKit is a nonshipping test port; the same-byte blob/HTTP diagnosis does not establish working playback there. Its unsupported cells stay not executed. |
| Android native and16 workloads | FullAA228 run39912731615 passes signed retained-data upgrade, notification/read-dismissal, indicators/video, denied-notification behavior and16/17 portable workloads. The passive focus/read and exact native staged export-cancel/cleanup regressions pass. Provider writes were not executed. Final app source equalsAA228. |
| Android Actions | Diagnostic run39916719795 preserves the first-menu FAIL.39 samples prove the document timeline/rAF stops advancing for at least2.84s while timers run and the sheet stays at opacity0. Cause unknown; no justified product fix or physical-device pass. |
| Gallery | Complete/affected runtime gates are composed in gallery PROVENANCE. All five final-pin builds/static/parser checks pass; all package digests and20 previews equal qualified76da inputs. Actual preview capture remains76da. |
| Office hardware and timing | CPU-only media/source and actual native/browser workloads were measured. Seventeen portable workloads do not imply every state/role/control has repeated physical measurements. Raw timings above200ms remain open. |

The actual browser advertises 31 capabilities without typing and 32 with typing
at the unchanged maximum32. Do not add another capability without revisiting
that wire bound. Media preparation is an explicit sender choice; exact originals
remain the default.

## Independently verified publication

[Release packaging39918593332](https://github.com/misunders2d/agentnet/actions/runs/39918593332)
passes all five packaging jobs plus draft assembly on the exact immutable tag.
There are 13 assets: 12 payloads and SHA256SUMS, which lists those 12 payloads.

The downloaded draft verifies all checksum/GitHub digest/size pairs, all six Go
programs' exact clean VCS revision and OS/architecture, the v0.9.3 version,
AppImage sidecar/media plugins/scanner, and Android's original preview signer,
nondebuggable package, both ABIs and16KiB alignment. Claude independently checked
inventory, all13 sizes/digests,12 checksums, six Go provenance records, the tag
and signer evidence. AppImage extraction rests on the separate root verifier.

Unauthenticated public download verification passes12/12 payloads and confirms
they are byte-identical to the qualified draft. Public gallery verification
passes all5 packages,129 package files and20 previews through151 HTTPS requests,
using the final core's actual gallery/package parser. Both public Latest lookups
match the published tags. Desktop installers are unsigned.

## Existing-relay rollout

The existing relay activated v0.9.3 at 08:54:05 UTC after a stopped native
backup of all remote-only Drive ciphertext and both old/new offline restores.
The 1,264,958,640-byte archive contains 8,388 files and verifies the exact size/hash
of all 8,380stored ciphertexts. Its SHA256 is
`1bc22bb93249baddb742a2cb3b38ba777cc248d644f3b8ab1e6c8a97583d7c42`.
The private verified archive is retained on the relay, not committed or published.
Operator OAuth configuration is excluded from the portable backup.

Both restores preserve every existing row and non-database durable file and the
Drive account/folder/client binding. Expected restore proof normalization changes
only local_present=1 and state=pending for 8,380Drive rows. The old restore stays
schema24; the new restore appends only the quota column/default0 and reaches25.
Both scratch restore directories were removed after verification. No Drive
disconnect, remigration, quota cleanup or replacement service occurred.

The live container preserves the original realm, volume, environment, HostConfig,
effective configuration except image and neighboring service. It is running with
zero restarts. Its actual executable SHA256 is
`da7c6c6b1315276a334c4983321442ab1adde7ef3953255d1a04af65219e67e9`,
equal to the independently verified public Linux payload, with clean 3f5 source.
Normal-certificate public HTTPS from two locations confirms v0.9.3 and the
original realm. An initial Python probe returned 403; ordinary curl requests from
both locations returned 200 without TLS/proxy/service changes. The failed probe
is retained without a causal-repair claim.

The relay's existing 30-minute grace ends at 09:24:08 UTC. Initial post-cutover
observations show signed authenticated traffic and devices reporting v0.9.3.
Zenbook and Bezos updated; the phone and second laptop last reported v0.9.2 and
need to finish updating before continuing after grace. These are availability
observations, not authority grants. Queued copies for outdated devices stay in
custody. Actual post-grace live enforcement was not yet observed; the required
mixed-version gate covers suspension and catch-up.

Reviewed failure recovery stops and verifies the candidate before reading its
final schema. Changed or unprovable schema requires operator recovery; an old
image is never attempted against migrated data and a stale backup is never
restored over possible new writes. No rollback was required.

## Remaining follow-up

- Android API35 emulator first-menu rendering stall: FAIL; physical cause unknown.
  A cold app/chat/first-actions phone screen recording is the bounded comparator.
- Routine measured controls above200ms remain open; optimistic pending feedback,
  API result and final visibility/paint stay separate.
- The upgraded physical Pixel/channel/launcher check is unverified.
- Extra nonshipping Windows WebKit media cells were not executed.
- Complete field/timing follow-up remains open under the owner publication decision.

All three completed task-owned helper panes were closed after results were
captured. The user-owned Claude session and unrelated panes were preserved.
Public release/rollout documentation is finalized separately from the immutable
release tag; no package bytes or qualification source changed.

No universal no-lag or all-physical-devices acceptance is claimed. The owner-approved
publication exception does not erase these findings. Paused Play work, owner
drafts and the dirty main checkout are preserved.
