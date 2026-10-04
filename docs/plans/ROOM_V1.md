# Room v1 (`rm1`): guests and agents as room participants

Status: plan against HEAD 3372f48 (v0.6.2), covering D1–D5 and D8. D6 is UI-only. Every `file:line` was re-read at HEAD.

Owner decisions (Sergey, 2026-10-03, via Telegram; all approved, D2 refined, D6 explained and accepted):
- **D1** Groups get guests too: any member can bring a person or an agent in to help; any member can dismiss them.
- **D2** While present, a guest sees the last N messages chosen at invite plus new messages until dismissed. More past history may be shared depending on what the guest should know; alternatively the inviter's own agent can summarize the conversation for the guest.
- **D3** Inviting your own agent needs no extra accept click; other people's agents still need their owner's OK.
- **D4** Agents are full participants: they post as themselves, mention people, and ask or task other agents; a task still needs the target agent owner's approval or a standing grant.
- **D5** Agents receive files sent to them and send files back through a per-run outbox folder (only that folder is ever sent). Skills sent between agents arrive as files; installing one is always the recipient owner's explicit action with a preview.
- **D6** The default interface hides reply-receiver routing and the Message/Question/Task picker; @mention asks, @mention + "Do it" gives a task; answers return to the same chat. The CLI keeps the options.
- **D7** (replaced 2026-10-04) Every interface is a standalone skin package on one core (the daemon and the documented skin contract, host API v1); users can build their own. Comic (the messenger, `internal/ui/web`) is the default built-in package; Classic and Zoom come as standalone packages in a later update on the same contract; the emoji Comic lens of the bundled app is retired, and the bundled app is no longer offered. See docs/DECISIONS.md §9 and docs/UI_SKINS.md.
- **D8** Mesh-ready: a workspace is an organization (membership and permission context), a relay is a transport endpoint; nothing is hard-wired to one relay.
- No release or deploy without the owner's OK. (2026-10-04: OK given for this work: once it is finished and every test passes, publish a release and update the relay and the owner's machines.)
- **D9** (2026-10-04) When one of the owner's own agents asks another of the same person's agents to act, it runs without asking (questions and tasks); agents of other people still need their owner's OK or a standing grant.
- Deferred by the owner (2026-10-04): joiner characters and non-ASCII spaces in names and titles; a workspace switch that blocks outside guests.

**Basis:**
- Wire: from design:minimal. Reuse `HumanTurn`, add one audience value, add no new `Inner` fields.
- Authority: from design:secure.
- Capability implication: from design:general.
- Layered capability check: from review:compat.

**Rejected:**
- A v2 participation domain with `Inner.Room`/`Asker`: it needs strict `HistoryItem` and engine changes, plus a second turn format.
- Roster-bound guests: they break the exact-device rule (`humanturn.go:76-78`).
- Remote hdl1 decisions on conversation items: old hosts ignore them.
- An IndexedDB version bump: not needed.
- Asks parsed from model output: model text would act outside the harness's permissions.

## 1. Model

Membership does not change:
- The DM v2 root stays frozen.
- Group admission stays admin-only (`grouplifecycle.go:180-183`).

"Help" (D1) is a participation (`protocol/participation.go:112`):

| Kind | Role | Audience | Sees |
|---|---|---|---|
| Legacy assistant | `""` | `conversation` | Its grant plus turns addressed to it (unchanged) |
| **Follower agent** | `""` | **`room`** | Its grant (last N) plus every captured turn while active |
| DM person guest | `human` | `conversation` | Unchanged: hgp1 already delivers every turn |
| **Group person guest** | `human` | **`room`** | Its grant plus every captured group turn; host role `visitor` |

**Lifecycle:** unchanged (`client/participation.go:32-39`, `resolve` `:185-323`).

**New predicate `ParticipationInfo.Following()`:** true when the participation is active, `Held==0`, and either `Role==human` or `Audience==room`.

| Event | Signer |
|---|---|
| invite | Any current member device. In groups it is bound to the author's admission (`bindGroupInvite`, `groupparticipation.go:73`) |
| scope | The invite's author. It is the invite's exact public projection, now also for group room invites |
| accept/decline | The host device only. D3 auto-signs qualifying self-invites (§5) |
| dismiss | Any current member device, or an accepted guest for itself (unchanged) |
| turns | The sending device. Guest turns carry `Human.AuthorPID`. Agent turns come from the host device and are always the output, progress or ask of a **running** job |

**Authority statement (docs and UI must say this):**
- The device key is the authority.
- A run gets `AGENTNET_HOME` (`worker.go:227`), so it could use the CLI as its person. §4.5 only guards against that.
- A grant to a device key therefore covers every agent running on that device.

## 2. Wire

### 2.1 One capability: `rm1`

`protocol.CapRoom="rm1"` and `wire.CapRoom` take slot 16 of 16:
- Go has 15 (`client/conv.go:136`).
- The engine has 14 plus typing (`engine.mjs:3862`).
- The relay only runs `ParseCapsRecord` (`hub/profile.go:25`), so it needs no change.

**Advertising rule:** a binary advertises `rm1` only once it enforces **every** reader rule in §2.2–2.5. Senders can switch features on later, so no later phase needs another capability.

The same release also adds:
- **Implication.** `Profile.Supports` (`person.go:527`) and the engine treat `rm1` as also implying apx1, agi1, hgp1, agr1, prg1, grp1, rcv1 and clr1. Later releases can then drop those tokens; pre-rm1 senders would only hold copies.
- **Parse headroom.** Raise the parse limit to 32 in Go, `wire.mjs:807` and the relay, while devices still advertise at most 16. Old clients ignore records they cannot parse, so a 17-token device would look as if it supports nothing (`person.go:533`).

### 2.2 Participation events

Add the value `protocol.AudienceRoom="room"` to the existing `Audience` field. The domain stays `agentnet-participation-v1`, so legacy bytes do not change.

Changes to `Validate` (`participation.go:149-232`) and `validateEvent` (`wire.mjs:1655`):
- Accept `room` on invites and scopes.
- Allow `Role==human` with `Group` only together with `room`, `HostRole=="visitor"`, no `TaskAdmissions` and no `AgentID`. This relaxes `:168`.
- A room scope may carry `Author.GroupAdmission` and a `Group` with `TaskAdmissions` stripped. Today `:212` refuses both.
- Add an optional `Until int64 json:"until,omitempty"`, only on room invites and scopes. After that time, by the reader's own clock, the participation counts as ended. It never orders anything.

Related changes:
- `ScopeOf` (`:236`) copies `Audience`, `Until` and the stripped `Group`.
- `Projects` (`:243`) and the agreement loop in `resolve` (`client/participation.go:248`) compare all three. Without this, a conversation invite could pair with a room scope (review M2).
- A group scope counts once a scope variant of `verifyInviteEpoch` (`groupparticipation.go:40`) succeeds.

### 2.3 Captured audience: reuse `envelope.HumanTurn`

Changes to `validateHumanInner` (`envelope/human.go:74`) and `parseHumanTurn` (`wire.mjs:1749`):
- **Root:** a two-member DM v2 root **or** a group v3 root. Group turns already carry `Root` (`groupturns.go:262`).
- **Proof scope (`:55`):** `Role==""` is allowed only with `room`.
- **Request (`:95`):** an agent origin is allowed only when the `AuthorPID` proof scope is `Role==""`/room. A human author using an agent origin is refused. `AgentID` stays empty.
- **v3 controls:** a revision or retraction may carry `Human` when it comes from the original turn's author (review X5).
- `MaxHumanAudience=16` counts every follower.

`HistoryItem` already carries `Human` (`history.go:68`).

### 2.4 Group turns and output files

- `ordinaryGroupTurn` (`groupturns.go:149`) accepts `Human` with `PID==AuthorPID`.
- `groupDeliveryRecipient` (`groupdelivery.go:75`) gains a branch for an exact follower host.
- A result with attachments is already a valid envelope. Only `SendConv` refuses it (`conv.go:484-494`).

### 2.5 Gating

**Primary capability stays.** Keep the primary `required_cap` (grp1/hgp1/apx1). About 15 delivery branches switch on it; for example, `groupparticipation.go:126` drops anything that is not grp1.

**`rm1` is an extra check** in delivery (`client.go:576-630`; engine `:1415-1443`). A new `roomCopy(sub, body, human, conv)` sets it from stored columns for:
- room events;
- `Human` on a group root;
- `Human` with a room scope or an agent author.

This follows the pattern where agr1 adds hgp1 (`client.go:593-597`). Also:
- Add `rm1` to the `requireParticipationCaps` allowlist (`agentwire.go:89`).
- `agentRequirement` (Go `:21`, `wire.mjs:1509`) tests room shapes before its hgp1 return (`:25`) and grp1 return (`:35`), and again after the history unwrap.

**Old peers:**
- Copies to them wait as `stateConvWaiting` and are released when their caps change (`client.go:644`).
- A stray copy fails `Validate` or strict decoding and is held, never misread.

**Room invites** need `rm1` on the host and on **every** current member device, browsers included. This mirrors `participation.go:469-476`, so the engine reader must ship in the same release.

## 3. Audience and fan-out

**Plan.** `humanPlan` (Go `humanturn.go:152`, engine `:5188`) iterates over `Following()`.
- A room follower that is held or past `Until` is left out, with a visible notice. It no longer blocks every send as `:170-172` does today (review X5).
- DM guests keep today's rule.

**DM.** `sendHumanTurn` (`humansend.go:41`) seals the same inner bytes for the members and for each audience host. A member-hosted follower already holds the member copy, so it gets no second one.

**Group:**
- `sendGroupTurn` (Go `:163`, engine `:2328`) uses the planner. Each copy carries `Human`, and each external follower host gets one pinned copy.
- The transaction guard re-runs `humanTurnAuthorization`.
- A turn from a guest or a visiting agent goes to every effective member device and to the other followers, reusing `participation_external.go:390-402`.

**Receivers.** These functions swap `externalDM` for "DM, or group with verified context", and `Role==RoleHuman` for `Following()`:
- `humanAuthority` (`:62`), `humanTurnAuthorization`, `mayDeliverHuman`;
- `disclosedHumanEvent`, `discloseHumanConv`;
- `humanEndReader`, `humanEndRecipient`, `assistantHostReader`;
- `inviteParticipation:456`.

Group followers verify membership from the group-context and group-proof carriers. `groupVisitorTargets` (`groupparticipationcarrier.go:20-66`) already selects every external PID, guests included.

**Recent N (D2):**
- The UI turns "last 10" (the default) or a suggested slice into exact `Grant` refs, at most 200.
- Room invites send excerpts through `sendGrantedExcerpts` only **after the host accepts**, generalizing `recoverHumanExcerpts` (`humanturn.go:367`). Today `participation.go:533` sends agent excerpts already at invite time (review M4).

**Agent context.** `agentContext` (`agentjob.go:708`) gets a room case:
- **Contents:** the grant plus the turns whose **stored captured audience lists this PID** (`json_extract` on `inbox.human`/`outbox.human`), never selected by arrival order (review M1). Newest first, within 64 KiB.
- **Labels:** each line carries the author's verified role.
- **Unapproved authors are withheld, with a count (review X2).** For a guest, approval means an `approvals` entry; for an agent, `agent_grants.questions`. A one-time `accept` of the run includes them for that run. Without this, an injected guest turn could ride along on a member's auto-run question.
- **Prompt:** the "no ambient room history" lines (`agentjob.go:402,412`) switch to follower wording.

**Dismissal:**
- The end event goes to the members and to every audience host.
- The planner drops the PID.
- `mayDeliverHuman` and the group guard mark queued copies `not_delivered`.
- `agentStop` halts the running job.
- Copies already delivered cannot be recalled. The UI says so and shows receipts per device.
- `Until` puts a limit on a relay that withholds the end event (review X6).

**Linked devices:**
- Members: unchanged (`Fan`, `forwardStale`).
- Followers: the exact host device only.

**D8 (mesh):**
- One ciphertext copy per device, with no new endpoint and no polling.
- Records name person, roster and key, never a relay or realm.
- Guests are deliberately not realm-checked.
- Caps are per device, so cooperating relays (MEL-436) need no record change.

## 4. Agent turns

### 4.1 Attribution (no wire change)

**Admission check.** In `checkConversationAgent` (`agentwire.go:180`) and in the engine (`engine.mjs:5525`), these turns must come from the participation's exact host address and key:
- every output-shaped PID turn (answer, result, progress);
- every agent-origin turn; one without a participation is refused.

A human-role participation has no agent, so an agent-shaped turn under one is refused even from its host.

The participation must also be active, or the turn historical:
- invited, or with any of its events held here: the turn waits (`proof_pending`) and is retried when the evidence arrives;
- declined or dismissed: refused (`invalid`);
- historical: checked against its original key only; it may outlive the participation.

Before P0a the function returned early when `AgentID==""`, so a member device could post as another member's default agent (review X3). That forgery is now exercised: `TestAgentTurnsComeOnlyFromTheExactHost` and the matching block of `agent_engine_check.mjs` fail when the rule is turned off (the Go test then finds the forged answer stored).

**Known limit (fail closed).** An output still in flight when its participation is declined or dismissed is refused for good on a device that learns of the end first. A device that admitted it before the end keeps it, and a device linked later receives that copy as history (which skips the state check) and shows it. So devices may disagree about such an output. Accepted as is.

**View-model.** It gains a `verified_agent` flag: `ConvMessage.VerifiedAgent` (`verifyAgents`, `agentwire.go:237`), `DMMessage.VerifiedAgent`, and the engine's DM and group views (`verifiedAgent`, `engine.mjs:220`).
- It is computed when the view is built, from the key that verified the turn (the original key for history). It is never stored, and never derived from `origin` or `agent_id`, so rows an older reader admitted are not marked.
- It speaks for the turn as sent. An edit, which any device of the host's person may make, shows as edited and is not the host key's.

**Labels.** UIs must label a turn as an agent's only from `verified_agent`, never from `origin`. Still open (P0a follow-up):
- `app.js` labels from `origin` and `agent_id` (`:1543`, `:2021`, `:2062`);
- the new default UI (`internal/ui/web`) switches to `verified_agent` at integration.

Until both do, a forged row that an older reader admitted still renders as an agent's.

### 4.2 Posts and mentions

- **Posts** reuse the progress shape: a message with `Status=progress` and `ReplyTo` set to the running request. This shape is already valid with `Human` (`envelope.go:503`).
- **Prompt:** PID jobs already get `AGENTNET_REQUEST_ID` (`worker.go:229`). `agentPrompt` adds the instructions for posting.
- **Running check:** `sendConvProgress` (`progress.go:52`) also requires the inbox row to be `stateRunning` (review M6).
- **Mentions** are body links `[@Name](agentnet:person|guest/ID)`, and the prompt lists the tokens. They grant nothing and start nothing.

### 4.3 Agent asks agent (D4)

New command `agentnet room ask --pid PB --kind question|task TEXT`, backed by `SendRoomAsk`.

**The CLI refuses unless all of these hold:**
1. `AGENTNET_REQUEST_ID` names a `stateRunning` **task** job of a follower PA hosted here (decision 3).
2. PB is another `Claimable` PID in the same conversation.
3. This run has made at most 2 asks.
4. The running request was not itself authored by an agent. This keeps asks to depth 1.

**What it sends:**
- `Target`: PB's host, with an empty `GroupAdmission`.
- `PID=PB`, `Human.AuthorPID=PA` and `Origin agent:<harness>`.
- `ReplyTo`: PA's running request. This records the cause without a new field.

### 4.4 Verdict at PB's host

`agentVerdict` (`agentjob.go:85`) gets a new branch placed **before** `requestEpoch` (`:116`) and the member path (`:119`). A new `roomAgentAuthor(q, r, m)` resolves the stored `human.author_pid` (review M5).

| Check | Outcome |
|---|---|
| PA is `Role==""`/room, active, unheld, at exactly (`r.Sender`, `r.Key`), and not `r.PID` | PA ended → stop; PA held → wait |
| The cause is held here, is a request with `PID==PA`, and was not authored by an agent | Otherwise → ask |
| PA has made more than 4 asks in the last hour, counted in `inbox` (review X9) | Ask |
| Question | Runs only with `agent_grants.questions` for (address, pinned fp, agent_id), or after one `accept` |
| Task | Runs only with `agent_grants.tasks` for that exact key and agent, or after one `accept` |
| Group | `requestEpoch` is replaced by "PA's invite epoch verifies" |

- **Never consulted** for these requests: `TaskKeys`, `r.Local`, `approvals`, `task_grants`, or the member auto-question rule.
- **Grants:** `approve --agent ADDRESS[/AGENT] [--tasks]` writes `agent_grants`. A grant stops holding when the key changes.
- `accept --always` still refuses conversation items (`taskgrant.go:128`).

### 4.5 Needs-you and run guard

**Needs-you:**
- `PageReview` (`uiview.go:240`) adds PID items in `awaiting` or `needs_human`, plus host invites that were not self-consented, each with a reason code. This lifts the limit at `ui/live.go:88`.
- Person turns in `conv_held` are listed separately.
- Decisions go through `/api/act`.
- The browser shows "decide on ‹host device›" as read-only.
- Clicking a notification never accepts anything.

**Run guard** (review X1). This is a guard, not a boundary. Under `AGENTNET_BACKGROUND=1`, the CLI refuses:
- `dm send`, `dm ask-agent`, group send, and device `send`/`ask`/`task`. The only exception is `send --reply-to $AGENTNET_REQUEST_ID`.
- `accept`, `approve`, `decline`, `trust`, and any participation or group change.

`room ask` is the only way a run can send a request. Send commands must never be added to question-mode allow rules.

## 5. Self-invite consent (D3)

`selfConsent(ctx, pid)` in `client/participation.go` signs the ordinary accept through `decide` (`:595`). Old peers see a normal accept, so this needs no wire change and no capability.

**Conditions (all must hold):**
- The invite is in state `invited`, with `Held==0` and `HostHere`.
- `Role==""`, and this device has not decided yet (`ownEvent` makes this idempotent).
- The author, the host and this device's self are the same person, and that person is not conflicted or frozen.
- The author device is in the pinned own chain **and in the local trust set**:
  - The trust set is the host plus own devices the person added, stored under the `config` key `self_consent`.
  - The reason: rosters do not mark browser devices (`person.go:70-79`), and a browser key must not turn relay-served code into execution here (review X4).
- `TaskKeys` is empty or contains only keys from the trust set.
- The AgentID is the default responder or an enabled catalog agent.
- In a group, `inviteEpoch` holds.

**When it runs:**
- after `recordAndSend` in `inviteParticipation`;
- after event admission (`conv.go:948`);
- in a sweep at daemon start.

Each auto-accept shows a local notice with a dismiss button. An invite that fails any condition falls back to the normal accept click.

## 6. Agent files (D5)

**Run folder.** `<home>/runs/<job>/`, mode 0700, created by the daemon.
- It replaces `opened/.agentnet-apx-*` (`participation_excerpt.go:62`).
- It is removed at job end and swept at startup, like `CleanOpened` (`files.go:569`).

**Inbound (`in/`):**
- It holds the request's files plus files from the selected context, up to 16 files and 200 MiB.
- Files are 0400 and the folder is 0500.
- Files are named `NN-<sha8>-<SafeName>`, so a sender can never plant a `CLAUDE.md` or `AGENTS.md` (review X8). The original name, size and SHA-256 go into the prompt as untrusted data.
- `agentStop` is checked around each copy.

**Outbox (`out/`, task runs only).** Decided substitution (decider, 2026-10-04): P0f ships the outbox for **device-thread** task results (version 1, collected in the worker's `finish` before `SendMessage`); the **conversation-result** outbox described below (`finishAgent`'s `SendConv`) moves to P2. The checks are the same for both.

Files are collected after a `done` run, before `finishAgent`'s `SendConv` (`agentjob.go:476`):
1. Open the run folder with `os.OpenRoot(run)` (Go 1.26). `Lstat` must show `out` as the same directory (device and inode) the daemon created.
2. Open `root.OpenRoot("out")` and read only the top level.
3. Accept only regular files with `nlink==1` owned by the daemon user. Open each with `O_RDONLY|O_NOFOLLOW`, and require `fstat` to equal `Lstat`.
4. Each file must fit `MaxFileSize` (`files.go:34`). At most 8 files (`MaxAttachments`) and 100 MiB in total.
5. Copy each file through `keepSent` (`historyfiles.go:62`) and attach it to the **result**. `SendConv` allows files on a task result when `claim!=nil`.
6. Any violation holds back the whole set, with the reasons in the result and in the job detail.

**Never attached:**
- paths named in the model's output;
- link targets or subfolders;
- anything outside `out/`;
- files from failed, cancelled, held-back or needs-human runs;
- anything on a progress turn.

The docs must say that the outbox does not prevent exfiltration.

**Harness arguments** (`responder.go:44-95`), added only when the run has files:

| Harness | Questions | Tasks |
|---|---|---|
| claude | `--add-dir <run>/in` | `--add-dir <run>/in --add-dir <run>/out` |
| codex | unchanged | `--add-dir <run>/out`. Never `--sandbox` or a bypass; a read-only setup reports "no files produced". A resumed session (`codex exec resume` has no `--add-dir`) gets no outbox and its prompt names none |
| pi | none | none |

Update each harness's `limits` text to match.

## 7. Storage

**Client:** one step, `roomSchema`, in a new `internal/client/room.go`, appended after `convClearSchema` (`store.go:325`):

```sql
CREATE TABLE agent_grants(address TEXT NOT NULL, agent_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL, public TEXT NOT NULL,
  questions INTEGER NOT NULL DEFAULT 0, tasks INTEGER NOT NULL DEFAULT 0,
  added_at INTEGER NOT NULL, PRIMARY KEY(address, agent_id));
```

**No other steps needed:**
- Room events are JSON in `participation_events`.
- The audience reuses `inbox.human` and `outbox.human` (`humanscope.go:10`).
- The trust set lives in `config`.
- Run folders live on disk.

**Hub:** a code change only (the parse limit), with no schema change.

**Browser:** no IndexedDB version bump, because the stores have no indexes (`engine.mjs:63-64`).

## 8. Browser engine parity

The engine reads; it never hosts, decides or runs.

**`wire.mjs`:**
- `CapRoom`, the implication and the parse limit;
- `validateEvent` (room, `Until`, group scope) and `Projects`;
- the human-inner rules;
- `agentRequirement` order;
- byte **and rejection** vectors in `wire_test.go`.

**`engine.mjs`:**
- advertise `rm1` with the layered gate (`:1415-1443`, `:1522`);
- follower `humanPlan`, and follower copies in `sendDM`/`sendGroupTurn`;
- `Human` in `admitGroupTurn` (`:2354`);
- the attribution check (done in P0a);
- group `inviteHuman` (`:5419`) and `changeHuman` (`:5389`);
- dismiss by any member;
- follower views, labels for agent-authored requests, read-only needs-you, and result files.

Until visitor-context ingest lands, the engine refuses to accept a group-guest invite (review compat X8).

## 9. Phases (each ships alone, with its tests)

**P0: no capability needed; each item can be vetoed on its own.**
- **a. Attribution (§4.1).** Tests (`TestAgentTurnsComeOnlyFromTheExactHost`, `TestAgentTurnUnderAHumanParticipationIsRefused`, and the matching blocks of `agent_engine_check.mjs`):
  - A member's unnamed PID answer is held.
  - A host output passes.
  - The host's address under another key is held, live and as history, and never marked.
  - An output waits while its participation is invited or has a held event, and is refused after a decline or dismissal.
  - An agent-shaped turn under a human participation is refused, even from its host.
  - History is checked against the original key.
- **b. Run guard.** Every listed command is refused, while progress to the env request still works.
- **c. D3.** Tests:
  - Exactly one auto-accept from the host or a trusted linked device, also across a restart and while offline.
  - No auto-accept from an untrusted own device, another person, a conflicted or frozen person, a human role, foreign TaskKeys or a disabled agent.
  - An old peer resolves it normally.
- **d. Needs-you.** Items appear separately from `conv_held`, and a click never decides.
- **e. D5 inbound.** Tests:
  - File modes are correct, and no name equals `CLAUDE.md`.
  - Cleanup runs after the job and after a crash.
  - Golden argument slices for each harness.
- **f. D5 outbox.** It ships in P0 only if a HEAD receive test plus the opt-in `AGENTNET_COMPAT_TAG` itest (`itest/members_test.go:97`) prove that v0.6.2 readers accept DM result files. Otherwise it moves to P2.
  - **Shipped for device threads (accepted substitution, §6).** Gate evidence, 2026-10-04: the HEAD receive test `TestDeviceTaskOutboxSendsFiles`, and the opt-in itest `TestCLIOutboxCompat` (a v0.6.x requester sends a device-thread task with a file; this build's run copies it into its outbox; the requester receives the result with that file and downloads the same bytes), passing with `AGENTNET_COMPAT_TAG=v0.6.0` (newest local tag) and with `5342aa3` (v0.6.2's commit).
  - **Conversation results** (DM, guest, outside host, group; Go and browser readers) get their outbox in P2, behind the same gate for those readers.
  - Tests:
    - Each is refused: a symlink, a symlinked `out`, a hardlink, a FIFO, a subfolder, an oversized file, a 9th file, a file swapped after `Lstat`.
    - Paths in the output are ignored.
    - Questions get no `out/`.
- **g. D6 and the last-10 preselection** (UI only).

**P1: the `rm1` reader.** Go and engine ship together, and `rm1` is advertised last. Covers §2 plus the receiver half of §3. Tests:
- Events:
  - Room is valid on invites and scopes.
  - Human+group is refused without room or visitor.
  - A scope that disagrees with its invite on `Audience`, `Group` or `Until` is held.
  - Golden v1 hashes are unchanged.
- Turns:
  - A group-root `HumanTurn` is accepted.
  - An agent origin needs an agent author and is refused for a human author.
  - An edit carrying `Human` is accepted only from the original author.
- Delivery:
  - The `rm1` extra check applies while grp1/hgp1 copies still deliver.
  - A device without rm1 waits, then is released.
  - The implication holds, and a 17-token record parses.
  - Go and JS agree both ways.

**P2: DM followers.** The §3 send side for DMs, deferred excerpts, room context and posts, and the conversation-result outbox (§6), shipped once the P0f gate holds for conversation readers. Tests:
- An invite needs rm1 on the host and on every member device.
- A 17th follower is refused.
- Delivery happens only while the follower is active.
- A copy queued before a dismissal ends as `not_delivered`.
- A key change blocks.
- A member-hosted follower gets no duplicate.
- Context holds only listed turns, with unapproved authors withheld.
- Excerpts go out only after accept.
- Progress outside `running` is refused.

**P3: agent asks (D4).** §4.3–4.4, `roomSchema`, `approve --agent`. Tests:
- The asking side refuses asks from question runs, a third ask, and nested asks.
- The target ignores `TaskKeys`, `Local`, `approvals` and `task_grants`, even from a member key.
- Two agents on one device still get "ask".
- A missing or nested cause, or the rate limit, gives "ask".
- An ended PA stops.
- A forged `AuthorPID` is refused.
- A key change demotes the grant.
- A group visitor asker is checked by its invite epoch.

**P4: group guests and followers (D1, D2).** `ordinaryGroupTurn`, `groupDeliveryRecipient`, the group planner and scope counting. Tests:
- A non-admin member can invite; a non-member cannot.
- Any member, or the guest itself, can end the participation.
- An inviter with a stale admission is held.
- A guest's turn reaches all members and followers.
- A held follower is skipped, with a notice.
- The `groupgovernance` tests stay green.

**P5: edits, deletes and `Until` reach followers.**

**itest journeys:**
- **J1 (P4).** A group guest sees the last 10 turns and live turns until dismissed, then nothing.
- **J2 (P0c+P2+P3).**
  1. Alice's own agent joins with no click.
  2. Bob accepts his agent.
  3. Alice's agent sends Bob's agent a task, which waits in Bob's needs-you.
  4. Bob accepts it once.
  5. An outbox file reaches Alice.
- **J3 (P1).** An old member blocks the invite. A later-linked old device waits, then gets its copies after upgrading.
- **J4.** Engine follower fan-out.
- **J5.** The host is offline, self-accepts on restart, and nothing reruns.
- **Opt-in:** `AGENTNET_LIVE=claude` for `--add-dir` and the outbox.

Every phase runs `go vet ./...` and `go test -race -count=1 -timeout 600s ./...`. After v1, any new capability first needs parse support above 16 everywhere.

## 10. Defaults chosen for the open questions (owner may veto any)

1. **D3 trust set.** Chosen: the host plus own native devices the person added. The literal D3 reading ("any linked device") would let a browser key enable execution.
2. **Follower context.** Chosen: an agent run withholds turns from unapproved guests and agents (with a count); people still see everything.
3. **May question runs ask other agents?** Chosen: no.
4. **Own agent asking own agent.** Owner decision D9: runs without asking, for any two agents of the same person.
5. **Outbox review.** Chosen: no extra review for files from an approved task.
6. **`Until` default.** Chosen: none; guests stay until dismissed.
7. **Outside guests.** Chosen: no workspace-wide ban yet. Group guests learn the member list; the invite sheet says so.
8. **Limits (as proposed):**
   - asks: 2 per run, 4 per hour;
   - inbound: 16 files, 200 MiB;
   - outbound: 8 files, 100 MiB;
   - followers: 16.
9. **Docs.** `NEXT_RELEASE.md:118-120` ("not the whole human room") must be revised.

## 11. Not verified

- Whether Claude loads instruction files from `--add-dir` folders. Renaming the files mitigates this.
- What Codex's read-only sandbox can read, and whether `--add-dir` makes a folder writable.
- Pi's file tools.
- Whether v0.6.2 readers accept DM and other conversation result files (the gate for the P2 conversation-result outbox). Device-thread result files are shown accepted by v0.6.0 and v0.6.2 CLI readers (§9 P0f); the browser engine's acceptance is read from its code only.
- Relay withholding: reasoned through, not exercised.