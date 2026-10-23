# v0.9.5 historical-device hook parity

Status: source frozen; focused regular/race checks passed; affected native and mixed-version qualification in progress. No tag or release yet.
Scope: complete the one residual T7 case found during v0.9.4 rollout; preserve
all earlier fixes. Claude implements source/tests; Codex coordinates qualification,
artifacts, publication and normal rollout. No new goal or helpers.

## Proven defect and minimal correction

A legacy chat copy from a removed own phone remains in hook unread although the
app projects it outgoing. Read-only laptop evidence confirms a conversation row,
verified author fingerprint and an exact matching public identity in earlier
pinned self-person roster steps, absent from the current device table. Device
names alone are insufficient. Reuse the existing historical-key identity rule;
no schema, native policy, read-marker or user-session mutation.

The correction derives deduplicated address/fingerprint pairs from the pinned
self-person chain and binds one JSON parameter to the existing attention queries.
It preserves deliberate V2 unread, pending Review, foreign/changed/unverified
keys, exact selected-session replies and existing visibility/retraction rules.

## Required focused verification

- Causal removed-device regression, including exact old key versus changed key
  and claimed-only rows; every supported harness remains covered.
- Existing affected hook/read/receiver lookup and query-cost selection regular
  and race; retain quiet-fixture daemon joins and existing bounds.
- Affected native query selection on Linux/macOS/Windows; required final vet and
  real mixed-version against newest published v0.9.4. Keep all unchanged v0.9.4
  integrated race/native/UI/Android evidence, failures and explicit skips.
- Matching gallery final-pin contract equivalence, packages and parser checks;
  no GUI or gallery source correction planned. Never mislabel a skip as a pass.
- Clean tagged-source artifacts, original Android signer, independent Claude
  checkpoint, unauthenticated public download equality, normal rollout and actual
  laptop hook check. Never retag/rebuild published v0.9.4.

Existing owner-approved limits remain open: Android first Actions emulator frame
stall, measured timings above200ms, physical Pixel qualification and nonshipping
Windows Playwright WebKit media. No unrelated product work or full-suite restart.

## Source freeze

Only attention.go and its existing regression changed. Source SHA256
`83fce2ac6a7a8a29246cbf1a6940d01ca12208dc1d7459e82032567f836700cf`;
test SHA256 `6c54cd313de5f5b18db06ae870909645dbea327a9111df6bd031dd18d79d11ce`.
Causal RED on v0.9.4: removed own-device copy alone is wrongly offered by all
five harnesses, while the app projects it outgoing. Changed-key and claimed-only
guards remain attention on both sources. Fixed parent count3 passes; affected
41-parent regular selection passes12.5s and race41/41 passes269s, including query
lookup cost. Quiet-fixture joins, all assertions and deadlines remain intact.
