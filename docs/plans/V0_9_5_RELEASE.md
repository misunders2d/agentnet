# v0.9.5 historical-device hook parity

Status: published and rolled out; installed laptop hook/app parity verified.
Published v0.9.4 is not retagged or rebuilt.
Tag v0.9.5: `aaa4ff1d4963509c3ee12b8f46c931d21498ec87`.
Matching gallery tag: v2026.10.23.3, source
`c3a82940f1821f17aea8d09d048dc405bc6a51c3`.

Scope: complete the residual T7 case found during v0.9.4 rollout, preserving its
chat fixes. Claude implemented; Codex coordinated independent verification,
artifacts and rollout. No new goal, helper, permission policy or schema change.

## Proven cause and correction

A legacy chat copy from a removed own phone was still offered by the hook although
the app projected it outgoing. Read-only laptop evidence confirmed its verified
author key in earlier pinned self-person roster steps, absent from the current
device table. Device names alone are insufficient.

The correction derives deduplicated address/fingerprint pairs from every pinned
self-person chain step and binds one JSON argument to the existing four attention
query sites. It reuses the historical-key identity rule and preserves deliberate
V2 unread, pending Review, foreign/changed/unverified keys, selected-session replies
and existing visibility/retraction rules. No read marker or user session is edited.

## Qualification

- Source attention.go SHA256 `83fce2ac6a7a8a29246cbf1a6940d01ca12208dc1d7459e82032567f836700cf`;
  test `6c54cd313de5f5b18db06ae870909645dbea327a9111df6bd031dd18d79d11ce`.
  Only these two source/test files and this release record differ from the parent.
- Causal RED on v0.9.4: removed-device copy alone is wrongly listed by all five
  harnesses while the app projects it outgoing. Changed-key and claimed-only
  guards stay attention on both sources. Fixed parent count 3 passes.
- All 41 affected parents pass regular12.5s/race 269s, including query lookup cost.
  Quiet-fixture joins, assertions and deadlines remain intact. Root independently
  passes final vet20.993s, the new parent race21.81s and required real mixed-version
  vs published v0.9.4 in87.89s; source hashes stay unchanged throughout.
- Native Linux/macOS/Windows: both affected query parents count 3 pass in39982395984.
  Other workflow jobs are explicit skips; their valid v0.9.4 evidence is retained.
  No repeat of unrelated full client/UI/platform suites is claimed.
- Before publication, the exact clean CI program ran from a temporary directory
  on the affected laptop. Pi hook IDs equal the live app Unread API's exact three
  IDs; the removed-phone copy and both reported old IDs are absent. No installation,
  acknowledgment, read-state or native-session mutation occurred. An earlier API
  timeout and an unqualified local build's parent VCS stamp remain failed probes.
- Matching gallery full `npm test` CI39983304890 passes on the final pin. Fresh
  browser-enabled Node checks306/306 pass, zero skips. Initial160 passes/23 opt-in
  skips without Chromium remain separate. The automatic duplicate tag CI was
  cancelled after the identical-source branch gate passed.
- Shared UI/protocol/envelope/skin-contract trees are byte-identical to v0.9.4.
  Generated standalone manifests change their version string only; other published
  code, styles and previews match. All five packages rebuild against this exact pin.
- Artifact run39983465523 passes all six jobs. Root verifies all12 payload checksums,
  GitHub size/digest inventory, six clean Go source/OS/arch stamps, original APK
  signer/version/ABIs/16KiB alignment and AppImage sidecar/media scanner/plugins.
  Concrete independent artifact and public verification records follow below.

## Publication and rollout

[App v0.9.5](https://github.com/misunders2d/agentnet/releases/tag/v0.9.5)
was published at 2026-10-23T16:25:42Z; [gallery v2026.10.23.3](https://github.com/misunders2d/agentnet-skins/releases/tag/v2026.10.23.3)
at 2026-10-23T16:25:56Z. Both public Latest endpoints and tags
match. Claude independently accepted the exact draft (HERDR_REVIEW_e29a4c17).
All 12 unauthenticated public payloads equal that draft; the actual tagged gallery
parser verifies 5 packages, 129 files and 20 previews across 151 HTTPS requests.

The relay is healthy from 2026-10-23T16:30:35.787327515Z, zero restarts. Its running binary
matches public Linux SHA256
`53120788df73998034710949e4e9d8cb150b178d50f0c3ff4013e026f9dc6a6b`.
Public HTTPS confirms v0.9.5 and the original realm. Schema 25, volume, environment,
effective configuration, Drive binding and neighboring service remain unchanged.
The native backup snapshot is 16:00:51UTC: 8,606 verified ciphertexts in 8,614 files;
old/new offline restores preserve all existing rows/files and Drive binding.
Only expected restored-cache bookkeeping differs. Live data was never restored
from the older snapshot; verified scratch was removed and the archive retained.
Post-grace live observation remains pending; the real mixed-version gate qualifies
latest-only custody/backlog behavior.

Normal app-aware updates completed on Zenbook and the Linux laptop; Contabo's
actual running headless client reports clean v0.9.5 from the same source. The local
update's first identity-change refusal was retained; a fresh qualified request
completed without interrupting jobs. The installed laptop Pi hook and current app
Unread API return exactly the same three IDs, with all three stale copies absent.
No read acknowledgment or native-session rewrite was performed. Existing Pi sessions
need `/reload`. Phone installation and a full live user-model turn remain unverified. Earlier Pi/OMP native context captures separately qualify historical
metadata filtering without rewriting native history.

Existing owner-approved limits remain open: API35 emulator first Actions frame
stall, measured interactions above 200 ms, physical Pixel qualification and nonshipping
Windows Playwright WebKit media. No unrelated product work or full-suite restart.
