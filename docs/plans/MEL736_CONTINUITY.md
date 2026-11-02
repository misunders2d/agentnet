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

This branch is for review, not deployment. Interaction measurements are in
INTERACTION_COVERAGE.md; qualification evidence follows.


## Verification — November 2–3 review candidate

Counts below are Go **parent tests**, with subtests stated separately. No
failure, timeout, opt-in skip or unstarted test is counted as a pass. The broad
matrix spanned incremental fixes; it is not a green qualification of the final
head. No race-detector warning appeared in the captured runs, which does not
make their timeouts passes. Every package timeout remained 600 seconds.

| Executed command | Result |
|---|---|
| `go vet ./...` | Pass, exit 0. |
| `go test ./internal/client ./internal/ui ./cmd/agentnet -run '^(TestMEL736One\|TestControlMembers\|TestGoogleReviewEmail\|TestGoogleReviewBrowserRetryAndEmailConflict\|TestGroupCurrentForward)' -count=1 -json` | 22 pass, 0 fail, 1 skip (`TestMEL736OneRendered`, browser environment absent in this invocation); 15 subtests pass. |
| `go test ./itest -run '^TestMEL736OneRealBinaries$' -count=1 -json` | 1 pass, 0 fail/skip. Real CLI/daemon processes, synthetic local Google issuer and test Hub; DM/notice/restart. |
| `go test -race ./internal/client -run '^TestMEL736One' -count=1 -timeout 600s -json` | Initial seeded run: 6 pass, 2 fail at unchanged 30-second workflow context. |
| `go test -race ./internal/client -run '^TestMEL736One(HubRecoveryDMAndNoGrantInheritance\|GroupContinuesAfterRecovery)$' -count=1 -timeout 600s -json` | Retry after broad load fell: both pass; retained six unchanged passes complete all eight new recovery regressions. No timeout increase. |
| `go test -race ./internal/client -run '^TestControlMembers(ReadsEachCurrentPersonOnce\|SnapshotKeepsAuthorityFresh)$' -count=1 -timeout 600s -json` | Both pass after fixing the introduced duplicate-read regression; original group roster/admission snapshots preserved. |
| `go test -race ./internal/ui -run '^TestMEL736OneBrowserParity$' -count=1 -timeout 600s -json` | Initial run: 1 fail (fresh device had not published capabilities). After adding the existing-style capability-readiness wait: 1 parent + its real-Hub subtest pass. |
| `go test -race ./internal/ui -run '^(TestMEL736OneBrowserParity\|TestBrowserGroupWarmRecovery)$' -count=1 -timeout 600s -json` | Both parents + Hub subtest pass after fixing notice projection on held groups. Browser probe includes 22 assertions, including ordinary/alias held-group rendering. |
| `bash scripts/race-shard.sh others 18` | Fail: 11 packages pass, 3 fail, 5 have no tests. This non-JSON package run does not provide a trustworthy parent/skip count. `gdrive/TestCiphertextResumable100MiBAndStableID` failed; `itest` and `mobile/core` hit 600 seconds. Mobile also reported network-recovery and linked-notice failures. |
| `bash scripts/race-shard.sh client-K 18`, each literal K=0 through 17 | Every partition failed. Aggregate: 450 pass, 49 fail, 7 skip; 34 started without completion and 985 selected tests never started (1525 selected). |
| `bash scripts/race-shard.sh cmd-0 18` | Pass: 111 parents and 51 subtests, no fail/skip. |
| `bash scripts/race-shard.sh cmd-1 18` | Pass: 108 parents + 51 subtests; 2 parent skips (native catalog bridge and standalone Claude SDK). |
| `bash scripts/race-shard.sh ui-0 18` | Fail/timeout: 47 pass, 13 fail, 8 skip; 1 unfinished, 127 unstarted. |
| `bash scripts/race-shard.sh ui-1 18` | Fail/timeout: 51 pass, 7 fail, 7 skip; 1 unfinished, 132 unstarted. |
| `bash scripts/race-shard.sh ui-2 18` | Fail/timeout: 39 pass, 2 fail, 4 skip; 1 unfinished, 154 unstarted. |

UI shards used exactly
`AGENTNET_PLAYWRIGHT=/tmp/agr/pw/node_modules/playwright AGENTNET_CHROMIUM=/usr/bin/chromium TMPDIR=/tmp/agr`.
`others` stopped before its CLI/UI steps, so those were run independently.
The client skips include opt-in real Codex runs and subprocess helpers. UI
skips include real IndexedDB suites, harness-specific signed-file/receiver
fixtures and backlog measurement. No physical WebView or native device claim.

Rendered command (same browser environment):

```
AGENTNET_PLAYWRIGHT=/tmp/agr/pw/node_modules/playwright AGENTNET_CHROMIUM=/usr/bin/chromium TMPDIR=/tmp/agr AGENTNET_SCREENSHOTS=/tmp/agr/mel736-one-shots go test ./internal/ui -run '^TestMEL736OneRendered$' -count=1 -json
```

Result: 1 parent + both history subtests pass; all four final screenshots
inspected. Exact timing/fixture limits are in INTERACTION_COVERAGE.md. The same command with `-race -timeout 600s` and screenshots under
`/tmp/agr/mel736-one-race-shots` also passed (1 parent, 2 subtests; zero
fail/skip): cold feedback <=524.5 ms, pending <=92.7 ms. Its four screenshots
were inspected; the exact command and measurements are recorded there as well. An earlier loaded attempt
measured 1062 ms cold feedback and failed; passing measurements do not qualify
arbitrary host saturation. No release authorization is inferred.

Failure classification and retained reruns:

- Untouched `origin/main` (`1a365824`, local source archive) race comparison:
  `go test -race ./internal/client -run '^(TestSentDMShowsPeerCopy|TestAgentOrganizationScopeOnlySelectedGrant|TestReactionOnlyAnswerLocalGroupRequester|TestControlMembersReadsEachCurrentPersonOnce)$' -count=1 -timeout 600s -json`
  gave 2 parent passes / 2 failures. The organization-scope and group-reaction
  deadline failures reproduce; this does **not** classify all other client failures.
- Comic setup parity failed before and after the change with Playwright 1.63:
  `locator.click: Timeout 30000ms exceeded`, waiting for
  `getByRole('button', {name: /^Your agent/})`,
  `comic_setup_parity_check.cjs:164:62`. The requested corrected-environment
  retry failed too. CI 41466686316 on `cfa66090` had passed UI-0..2; a local
  browser mismatch remains a hypothesis, not an established cause.
- Additional untouched-source rendered comparisons reproduced Android Comic
  (`locator.waitFor`, 30000 ms, `android_comic_rendered.cjs:103:114`), Comic
  group parity (`page.waitForRequest`, 20000 ms,
  `comic_group_parity_check.cjs:82:18`), Google browser (`:54:174`, 30000 ms),
  needs-human history (`group_guest_controls_rendered.cjs:873:73`, 8000 ms),
  phone rejoin (`:1154:94`, 8000 ms), compact closed result (`:686:4`) and
  invitation wording (`:828:89`). Together with setup, eight UI failures are
  confirmed baseline, not repaired here.
- Four UI-0 failures were missing existing `esbuild`. `npm ci --ignore-scripts
  --no-audit --no-fund` installed the unchanged lockfile, without adding a
  dependency. Race reruns of `TestCurrentAgentChoices`,
  `TestNormalRowUnreadRendered`, `TestDeviceRemovalRendered` and
  `TestGroupChangesRendered` all pass.
- `TestBrowserGroupWarmRecovery` passes on baseline and exposed an introduced
  notice projection regression. The final focused race run above passes after
  skipping unrelated recovery work and deferring new notices on held context.
- Nine remaining UI-1/2 rendered failures are unclassified: pending-topic
  navigation, Comic people, long link labels, conversation links, stopped-send
  delivery, OKs navigation, request follow-up, selected-message assignment and
  default-agent setup overflow. They remain qualification gaps, not passes.

No external skins collection, mixed-version release gate, physical platform,
real Google account, live Hub, real model or recovered group-admin governance
qualification was performed. Current group messaging is verified; immutable
admin signatures/CAS and native authority are not replaced by an alias. Local
old timelines remain available; fresh devices cannot decrypt lost-key history.
Logs/screenshots and baseline source archives remain under `/tmp/agr` only.


Exact additional focused environment/baseline selections (same browser
variables above, `go test ./internal/ui -count=1 -json -run REGEX` on untouched
source; all failures, no skips, unless stated):

- `^TestAndroidComicRendered$`: 1 fail.
- `^(TestBrowserGroupWarmRecovery|TestComicGroupParityRendered)$`: 1 pass / 1 fail.
- `^(TestGoogleRendered|TestNeedsHumanReportHistoryRendered|TestComicPhoneRejoinRendered|TestComicClosedRequestPresentationRendered|TestComicGuestInvitationInlineRendered)$`: 5 fail.
- Corrected-environment setup retry on candidate:
  `go test ./internal/ui -run '^TestComicSetupParityRendered$' -count=1 -json`:
  1 fail, same as its pre-change result.
- Candidate dependency rerun with `-race -timeout 600s`:
  `^(TestCurrentAgentChoices|TestNormalRowUnreadRendered|TestDeviceRemovalRendered|TestBrowserGroupWarmRecovery)$`:
  3 pass / 1 fail before the held-group fix; its final pass is retained above.
- Candidate `go test -race ./internal/ui -run '^TestGroupChangesRendered$' -count=1 -timeout 600s -json`:
  1 pass, no fail/skip after dependency installation.
