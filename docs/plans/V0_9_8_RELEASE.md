# v0.9.8 — October 28 release

Status: app and matching gallery published, public bytes verified, Hub deployed.

Sergey explicitly said “okay, proceed to release” after being told that the
implementation was pushed but unpublished, that exhaustive interaction coverage
was incomplete, and that gallery CI still needed review. This authorizes shipping
the verified candidate with those coverage limits disclosed for this release;
it does not assert completion of the matrix or change the standing policy.

Frozen app source: `f23d04169b5ce6e855cdd053441fbd723c9f39be`.
Matching gallery source: `e0a3a92f8ef2dbebf0204a42525af5dc1d4f89bd`.
Later core main commits contain qualification documentation only.

Scope: MEL-701 unread projection and retained Review, MEL-700 scoped environment
clarification, MEL-692 compact historical Pi notices, MEL-702 readable Drive
export gating, MEL-583 secondary continuation placement, MEL-708 file drops,
and matching reminder-focus preservation in the gallery. Existing execution
authority, encryption and signed synchronization remain unchanged.

Root owns publication, deployment and this checklist. The participating Sol
verifier checks the concrete artifact set after packaging; no additional product
scope or user approval is introduced.

| ID | Target | Acceptance | Owner | State | Evidence |
|---|---|---|---|---|---|
| R1 | Qualified frozen source | Retained source/platform checks, exact-pin gallery CI, explicit coverage limits | root | verified | Retained/affected checks; final gallery CI 40744449492 passes on QA-only cc330d7, with identical product trees to e0a3a92 |
| R2 | Exact release payloads | All checksums/sizes; clean tagged Go stamps; original APK signer/ABIs/alignment; native shell package checks | root + Sol verifier | verified | Draft 427003966: all 12 payloads + SHA256SUMS, sizes/digests, six clean Go stamps, APK signer/ABIs/alignment, AppImage contents |
| R3 | Public app and matching gallery | Exact immutable tags, public Latest endpoints and bytes, actual gallery parser | root + Sol verifier | verified | Both public/Latest and tag pins verify; all 12 payloads + SHA256SUMS match the draft; actual public gallery parser passes 5 packages/129 files/20 previews across 151 unauthenticated requests |
| R4 | Preserve and deploy existing Hub | Verified consistent local recovery copy; same realm/volume/configuration; exact public binary; healthy service and unchanged neighbor | root | deployed | v0.9.8 started 18:47:18 UTC; exact public binary, original realm/volume/settings, unchanged neighbor, zero restarts; consistent local recovery copy verified |
| R5 | Clean handoff | Linear progress, accurate publication/activation limits, clean/current main branches and no stray worktrees | root | verified | Six issues, MEL-642 and project updated; release documentation committed/pushed; both main branches synced and clean with one worktree each |

Known limits: no exhaustive control/platform/history matrix or physical-phone
qualification. Original transient Zoom reload and Mac popup failures remain
unexplained despite unchanged focused passes; raw failures remain recorded.
Native Linux file feedback measured 31 ms in an isolated app test. Pending
presentation and transport custody do not establish remote task completion.

Deployment preserves the existing server and desktop sessions. No user desktop,
native harness or laptop session is restarted by the agent. App publication is
not proof that every installed client has activated the new release.


## Packaging and gallery qualification

Tag v0.9.8 is immutable at the frozen source. Packaging run 40736593338 uses
explicit tag checkout. An initial Go checksum-service HTTP/2 transport failure
was retried without disabling checksum verification; the complete draft passes
independent artifact verification. Exact-tag mixed-version vs v0.9.7 passes in 73.22 s.

Gallery CI 40734770261 exposed a Classic reaction-picker failure. A deterministic
four-case regression proved that same-chat background redraw discarded open
pickers in Classic/Holonet. Current callbacks are rebuilt from current messages;
only open presentation/focus is preserved within the exact host/chat/topic.
Close, Escape, unrelated draft focus and different-chat isolation pass. All 321
local source/browser checks pass, with all 11 affected company-world groups;
Classic motion remains inapplicable. Final gallery source is the revised commit
above; core product source remains unchanged. Original first-tap HTML interception
is not separately proven, and earlier transient failures remain recorded.

CI 40741357820 passes the changed Classic/Holonet groups but fails an unchanged
Zoom long-history bubble's trial tap because its parent intercepts the input.
QA-only commit `cc330d73b200e5ffb6ea15be93de1e877e2dc8f2` adds failure-only geometry
diagnostics and reruns the gate without changing product bytes, timeouts or
assertions. CI 40744449492 passes the complete gate. Local helper checks pass both
geometry/occlusion and mute-wait cases. The unchanged passing rerun does not
establish the earlier Zoom failure's cause.

Final CI executes 173 Node checks with 25 optional skips, zero failures, and all
37 applicable gallery/runtime groups. The separate local browser-enabled result
is 321 passed with zero skips. Zoom's real touch journey passes 28 taps;
Classic/Holonet pass 26 each. No failure-only diagnostics are emitted on this run.

App release 427003966 is public and Latest at 18:35:03 UTC; matching gallery
427044808 follows at 18:35:04 UTC. Unauthenticated Latest metadata independently
confirms both. Public payload and gallery-parser verification remains separate.
The tag-triggered duplicate gallery CI 40746417857 is cancelled after the complete
identical-product run passes; it is not counted as passing evidence.

Independent unauthenticated public verification passes: all twelve payloads and
SHA256SUMS exactly match the qualified draft, GitHub sizes/digests and manifest.
Clean source/OS/architecture stamps, original APK signing identity/version code 9008/
ABIs/ZIP and ELF 16 KiB alignment, and AppImage sidecar/media/scanner all verify.
The actual frozen-core gallery parser verifies five packages, 129 files and
twenty previews across 151 HTTPS requests; both immutable tags and the public
upstream pin match. Linux program SHA256 is
`40eebc279f8251a0efe4f266779e77627e50c25e71043d5b3a102eca94c0a245`.
The 536.5 MB public transfer/check takes 600.821 s; gallery verification takes 57.405 s.
Private machine-readable evidence remains under `/tmp/agentnet-v098-verification/`.

The private Hub staging directory contains the exact Linux payload, matching
Dockerfile, rollback image and unchanged-configuration baseline. An early guard
rejected the incomplete transfer before any build or Hub stop; completed bytes
match the qualified payload.
Preflight also corrected the copied script's public-version assertion from the
old literal to its release-specific new version before any Hub stop.

## Production deployment

Hub v0.9.8 starts at 18:47:18 UTC. The consistent local snapshot captures 17 files
(1,324,992,665 bytes), including the schema 25 database; byte/mode/ownership equality,
SQLite integrity and isolated native maintenance pass. Capture starts 18:46:41 and
the old Hub is online again by 18:47:06 while verification finishes. All 8,940 stored
attachments remain remote Drive references: this is a verified local recovery
copy, not a complete portable attachment backup. No remote ciphertext is fetched
during maintenance. Owned rehearsal files are removed; the recovery copy stays
private under `/opt/agentnet-revived/releases/v0.9.8-f23d0416`.

The running image uses the exact public Linux binary hash above and clean frozen
source. Realm, volume, environment/configuration and neighboring Telegram service
are preserved, with zero new-container restarts. Rollback retains the old image
and restores configuration/image only; it never overwrites live data. No user
desktop, native harness or laptop session was restarted. Installed-client
activation remains separate from publication and Hub deployment.
