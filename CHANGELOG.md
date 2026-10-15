# Changelog

Notable changes to AgentNet, newest first. Dates use `YYYY-MM-DD`.
This record starts with v0.8.1; earlier releases remain on the
[releases page](https://github.com/misunders2d/agentnet/releases).

## Unreleased

- Let Windows AgentNet alerts use the person's configured notification sound
  while continuing to respect Windows quiet time and notification settings.
- Synchronize reads of sent questions and self-agent replies across the owner's
  devices when their exact thread is opened. Unseen threads stay unread;
  reading does not restart or change the result of a request.
- Keep a closed request's complete result in a compact, expandable card with
  its agent and original request. Render safe Markdown and hide recognized
  internal trailers without changing the stored result.
- Keep reaction highlights inside their rounded toolbar, including custom
  skin shapes and narrow screens, with visible keyboard focus.
- Compact long raw links inside Markdown tables while preserving their exact
  destinations, explicit labels and code.
- Show whole portrait and landscape attachment previews at a bounded size,
  with a smaller secondary save control and unchanged original-file actions.

## [0.8.28] — 2026-10-15

- Group agents may delegate a task from an admitted question. The recipient's
  existing task consent and current identity, scope and cancellation checks
  still apply; native tools and permissions remain unchanged.

- Repair selected damaged local skin packages through the existing cache manager,
  preserving app identity, chat data, drafts, queued sends and healthy packages.
  Keep gallery reinstall reachable when its local catalog is unreadable;
  catalog recovery refuses to discard readable unrelated skin metadata.
- Let managers add people to retained legacy lists while preserving their signed
  identity and membership rules; list selection rejects changes during refresh.
- Invite an agent with exactly reviewed messages moved into a topic, including
  their selected files, without changing the originals' execution scope.
  Move notices name Main flow or the actual destination topic.
- Attribute agent-authored requests to the asking agent and hide a local retry
  when its originating run has ended. Explicit handling keeps the request audit
  and clears only that request's archive blocker.
- Show message previews without Markdown formatting markers and running times
  in local time, while preserving explicitly named topics and stored content.
- Show the exact own agent's reported model and reasoning in the picker,
  details and active agent chat, with timestamps and clear missing-report
  explanations. Native settings and permissions stay unchanged.
- Let active helpers see new human messages and verified peer-agent replies
  in their invited DM, group or topic. Preserve selected earlier history,
  dismissal, exact reply binding and existing native execution routes. Recover
  retained native context without treating clock order as access authority;
  unprovable old unindexed incoming turns still require exact selection.

- Allow admitted agents to attach files in their own-device DMs, matching
  existing group and external-agent sends, with current participation checks.
- Copy and open exact chat/topic links across enrolled devices. Refused or
  unavailable destinations preserve drafts and files; organization results
  return their actual stored destination.
- Fix native file saves through the app's page security policy, retaining
  authenticated-host, native-origin and filename checks.
- Choose an exact active request from the ordinary composer for an ordered
  correction, or explicitly start independent work. Native acceptance and queued
  continuation stay distinct; fresh authorized evidence and ready files follow
  the correction without repeating already supplied content.
- Run independent conversations for a registered agent in fresh native sessions,
  retaining exclusion for saved, selected or unverified shared sessions and
  exact cancellation and cleanup ownership.

- Clean up detached scroll callbacks in standalone skins and refuse stale
  Holonet message focus after navigation.

Known issue: MEL-587 remains unfixed. The owner explicitly approved carrying
the Linux WebKit renderer crash into the next release as urgent work; no
crash fix, GPU workaround or renderer-kill behavior is claimed.

## [0.8.27] — 2026-10-14

- Reading an Android chat now clears its exact OS message notifications after
  the read is stored; unrelated pending decisions remain.
- Browser DM agent invitations and dismissals retry a pre-commit storage conflict
  with the same signed event and fresh authority checks, without duplicate sends.

- Restore validated profile pictures along with Hub backups.
- Show locally decrypted Android message previews with truthful agent/device
  attribution. Read existing notification-channel settings and offer their
  direct settings screen when sound, vibration or importance suppress alerts.
- Preserve active download destinations and object-URL lifetime, report actual
  save outcomes, and refuse exhausted filename choices without overwriting files.
- Offer supported Codex native approvals and input on current owner-human
  devices, continuing the same live operation once under unchanged native
  settings. Expired, cancelled or ended callbacks cannot replay work.
- Exclude removed own devices from standing-permission choices while retaining
  their history and current standalone services.
- Let an admitted agent organize exact messages on a verified human request,
  including explicit Main and a grouped Undo that refuses later changes.
  Let agents react to ordinary canonical mentions without a fabricated question.

## [0.8.26] — 2026-10-14

- Allow Android's skin gallery to read its two fixed GitHub sources. Only GET
  subresources are allowed; navigation, native bridge, other hosts/repositories,
  credentials, query strings and unsafe paths remain blocked. This corrects the
  gallery failure in v0.8.25 and requires a new APK release.
- Update the Classic, Holonet and Zoom gallery skins with visible Settings,
  usable phone controls and correct chat navigation. Zoom opens chats from its
  people list and offers Reply in the message view. Asking an agent preserves
  the selected target through sends and retries; own-device requests omit an
  ineligible default reply receiver. Topic selection, chat mute, reminders and
  enabled Project space controls stay reachable in their conversation.

- Keep Zoom’s Write control reachable in a sticky chat header through long DM
  and device histories, including after search and resize. Keyboard focus stays
  below the current header. The corrected collection is `v2026.10.14`.
- Refine Pulp’s selected agent hairstyle and correct standalone permission text
  to describe unchanged native tools and own-device consent.

## [0.8.25] — 2026-10-13

- Add a skin gallery in Settings → Appearance, with verified pictures and
  packages from the latest published AgentNet skin collection. Install keeps
  the skin in this app or browser; using it still requires trusting that exact
  package. Skins requiring a newer AgentNet are marked incompatible.
- Keep Comic as the only built-in skin. Classic and Zoom are available in the
  gallery alongside Holonet and Pulp. A saved Classic or Zoom choice opens
  Comic until the corresponding package is installed again.
- Make Comic a skin kit: overlays can replace its theme, fonts, brand,
  avatars, send button, topic introduction and request frame while retaining
  Comic's features. Pulp is the first published overlay.
- Document the skin contract, host routes and presentation parts against the
  code, and check the complete skin collection against each release candidate.
  Comic uses the host's public native visibility and message interfaces.

Known limitation found after publication: the v0.8.25 Android APK's existing
WebView network policy blocks the skin gallery. Desktop and browser galleries
work. An Android APK correction needs a new patch release; published v0.8.25
assets remain unchanged.

## [0.8.24] — 2026-10-13

- Allow active admitted agents to send ordinary proactive turns under their
  exact current invitation, without requiring a fabricated incoming request (MEL-603).
  Admitted human guests receive ordinary agent messages; linked history retains
  their attribution after dismissal. Bootstrap archives keep all required reader
  capabilities across retries, deletion and expiry without blocking other devices.
- Capture the active visible topic on Bring In, defaulting empty to Main flow
  rather than expanding to all topics (MEL-582).
- Clear stale needs_human blocker phase once a request has a confirmed terminal
  outcome; display settled host snapshots as nothing waiting any more (MEL-605).
- Open chat pictures in a fullscreen viewer with zoom, pinch and pan,
  including a 100% original-size view. On desktop, open the exact image in the
  OS default image app; Download and Use as my picture remain available (MEL-543).
- Keep Comic composer text transparent during selection, with a translucent
  highlight so collective tags do not show overlapping textarea glyphs (MEL-613).
- Correct question approval wording to describe unchanged native tools, skills
  and permissions; remove the false editing-tool exclusion (MEL-597).
- Show same-device agent questions once, clear
  stale “Sending…” previews after delivery, and retain the selected agent for
  new follow-ups. Correct retained history without replaying completed work (MEL-614).
- Record own-agent consent before unrelated invitation fanout. Recover stored
  group acceptances and their exact unsent invitation after restart, without
  another click or another decision.
- Show own-agent invitations as joining, with an explicit setup blocker when
  the local agent is disabled. Keep them out of approval counts and controls;
  show active only after the host's signed acceptance arrives (MEL-525).

## [0.8.23] — 2026-10-12

- Use the same verified human identity across personal devices for own-agent
  invitations, questions, tasks and exact remote Stop. No extra AgentNet
  approval is required; each harness keeps its native tools and permissions.
- Cancel or refresh a pending group invitation from a linked human device while
  its originating device is offline. Cancellation fences only that invitation;
  unrelated invitations remain valid and uncertain publications keep their bytes.
- Preserve chat draft destinations and expose the existing mention picker with
  a persistent @ button. Use terminal request status consistently in topics.
- Query only the selected browser chat and release idle delivery locks. Retry
  transient rate limits with the same saved message instead of failing the send.
- Restore Android message/OK/reminder notifications and show denied system
  permission truthfully. Support exact notification destinations and deduplication.
- Correct historical roster-prefix handling and inert history display after a
  device retires. Keep technical held-copy diagnostics in explicit troubleshooting;
  individual and grouped notice dismissal changes no messages or trust decisions.
- Recover held history copies of locally deleted native messages without
  restoring their text, files or completed work.
- Keep an agent's completed reply deliverable after its parent request finishes,
  while preserving cancellation and current participation checks.
- Preserve native harness launchers, report missing-executable failures back to
  requesters, and distinguish inconclusive setup probes.
  Codex uses its native app-server transport, including native approval refusals,
  across worker modes without injected permission or tool overrides.

## [0.8.22] — 2026-10-11

- Reconnect Android after resume or a default-network change without reopening
  its saved chats. Retire dead HTTP/2 connections after request or stream
  timeouts so sends and incoming messages can recover together. Preserve
  queued message IDs, TLS verification and existing permissions.
- Keep Android's saved-message retry action available inside the open chat.
  Show a short recovery message and put long connection errors in expandable
  details that fit a narrow phone screen.

- Preserve the correct refused/expired device-link outcome when the decision
  races the opening receive stream. Revoked devices remain refused.

## [0.8.20] — 2026-10-11

- Answer questions from your verified personal devices without another AgentNet
  approval. Already-delivered questions that never ran can proceed after update;
  completed work and copied history never run again. Human requesters appear as
  people, and genuine held questions have an **Allow once** action in OKs.

- End undelivered copies addressed to a device or person permanently removed
  from the relay. Keep delivering ordinary messages and replies the relay had
  already accepted from them. Stop recreating background history for removed devices,
  preserve proven receipts and original messages, and refuse unstarted requests
  from known revoked keys. Offline or outdated devices keep their resumable copies.
- Clear a remote running card when the request finishes during a status
  refresh. Keep the existing recipient record until its final snapshot is sent.
- Remove obsolete Pending and Needs-you prompts for permanently removed hosts.
  Show **Host removed** and disable replies/proposal confirmation that cannot
  reach them, while retaining the original records and later accepted answers.
- Allow an admin to remove a whole verified person with `admin revoke --person ID`,
  including current and pending devices and unused invitations. Google-linked
  accounts are also denied re-enrollment through an otherwise allowed domain.
- Wait for group-agent replies through the daemon's existing local change
  notifications instead of polling room state four times a second. Coalesce
  overlapping attention checks from nested runs while retaining new-state wakes.
  Agent output alone no longer repeats permission checks; repeated participants
  in a request chain are resolved once per current-state check.
- Allow inviting an agent into a group topic when an unrelated member's app is
  outdated. Check the selected agent host; keep other devices' exact copies
  waiting independently until they can receive them.
- Keep `@everyone` and shared team mentions as compact tags in drafts and
  messages. Preserve their selected recipients and per-agent outcomes through
  reloads and retries without expanding the text into everyone's name.
- Move passive sync records out of **OKs** into **Settings → About → Technical
  details**. Keep actionable identity warnings visible and retain all records
  and verification checks.
- Show the executing device and its recorded explanation on a remote **Needs
  you** card. Linked-device questions no longer name the viewing computer or
  claim the agent gave no reason when its exact owner report includes one.
- Keep background history counts and progress inside device **Details**. Pending
  transfers no longer claim they need missing context or ask for attention
  without an action the person can take.
- Recover linked history blocked by duplicate deliveries of a membership end.
  Reuse an independently verified copy of the exact signed event for the same
  device; preserve original messages, receipts and admission checks.

## [0.8.19] — 2026-10-11

- Include the native Android preview in tagged releases as a signed APK with
  SHA256SUMS, using the same Comic interface and Go messaging core. Updates keep
  the preview's existing identity and local data.
- Android's Check for updates and Update actions use a native verified download
  and Android's installation confirmation instead of a missing desktop API.
  The APK must match the original signing key, package and exact newer release.

- Keep your follow-up questions and tasks outgoing when their history is copied
  to another personal device. Already-saved copies display correctly after
  updating, preserving their original sender, reply and files.

- Keep a finished request's final review report from being lost when an earlier
  snapshot is still sending. Reuse the existing change event without polling
  or repeating work.

## [0.8.18] — 2026-10-10

### Changed

- Linked-device catch-up transfers recent history in bounded encrypted chunks
  through the existing file service. Older pages import in the background;
  every source contributes the history it holds, with the existing signature,
  membership, deduplication and deletion checks. Interrupted transfers resume.
- Live messages, replies and fresh own-device copies have independent delivery
  from cold history. History upload and import do not hold the live sender or
  receiving stream. Existing queued history retains its original IDs and
  ciphertext when upgraded to background delivery.
- Browser background work queries pending copies through storage indexes,
  avoiding repeated loads of the whole sent-message archive.
- Idle background wakes avoid preparing archive-export and permission queries
  when there is no eligible work; real requests retain the complete checks.
- History progress distinguishes records stored on the destination from those
  waiting at the relay; storage is not presented as completed import.

- Develop an isolated Android preview with the shared Comic interface, embedded Go client and private SQLite storage. Native qualification is tracked separately from the desktop release.

## [0.8.17] — 2026-10-10

### Changed

- **Latest version only.** A relay running a release now serves only devices on that release. After a grace period for updating (30 minutes by default; `hub serve --update-grace` or `AGENTNET_UPDATE_GRACE`, `0` for none), an outdated device is suspended: the relay refuses its new work with an "Update AgentNet to vX to continue" answer and its stream carries only the update notice. It may still acknowledge, look up the records an answer needs, and finish uploading answers and results (with their files) already admitted. Messages for it wait in the relay and arrive once it updates; nothing is lost. Only the relay's own release sets the threshold, so a mistyped recommendation cannot lock anyone out. Development relays keep the policy off. The update recommendation now follows the relay's release automatically.
- **Nobody waits for an outdated device.** Senders skip suspended devices and record why per device ("…'s laptop is suspended until it updates AgentNet"). In the browser and phone app, one device that lacks a capability, never connected, or was revoked no longer stops a whole group message, group change or guest conversation. Delivery headlines ignore your own copies and suspended devices.
- **Automatic updates.** The desktop app, the background program on servers and headless agent hosts, and browser pages update themselves to the release the relay names: downloaded only from the official release page, checked against its checksums, tried against this computer's data first, and switched in only when no agent job is running. Browser pages reload to the new version without losing drafts. Turn it off with `agentnet update --auto off`. Devices on v0.8.16 or older cannot update themselves and must be updated once by hand; a new real-binary release check proves such a device is suspended without slowing anyone else and catches up after its update.
- Topic **Done, Reopen and Archive** now follow you across your own linked devices, like topic names.

### Fixed

- The phone app no longer shows an empty chat list for 20+ seconds while it waits on the relay's notification support; saved chats appear from local storage first. Changes arriving during the first load are no longer lost, and a slow or unavailable skin catalog or font sheet no longer keeps the app on "Opening AgentNet…".
- Receiving on phones and browsers keeps moving: one message that fails unexpectedly is set aside instead of stopping everything behind it, heartbeats are answered immediately, the app reconnects when it comes back to the foreground, and each message now costs work proportional to its conversation instead of all stored history (synthetic desktop measurement: a reply behind 100 sync copies with 5,000 stored messages went from 79 s to about 8 s). History copies are no longer echoed back to the device that sent them.
- Desktop and server programs keep receiving when one message fails admission for a reason other than missing proof; it is held instead of blocking the stream. Held messages are rechecked fairly instead of restarting from the first page on every wake, and presence-only changes no longer trigger full rechecks.
- The relay keeps a busy device's stream open while it acknowledges messages, sends heartbeats during long backlogs, and no longer re-sends an unchanged member list.
- Your own devices no longer receive duplicate history copies of messages they already got directly, and byte-identical copies re-sent by older devices fold into one held record instead of hundreds of notices.
- Held-back notices name the exact cause and who can act ("Your device Bezos re-sent 4 records 866 times · update AgentNet on Bezos"), count records separately from copies, and stop calling your own verified devices "unverified sender". Every copy stays inspectable.
- Requests show "Waiting" only when the executing computer reports active work; otherwise "No result recorded". Executors now report the recorded final state of retained requests after updating, and report requests queued for a busy agent. OKs no longer say "Decide on…" for work that was only last seen running.
- An invitation that lists a device you later removed from your account no longer holds that conversation's agent history forever; the removed device gets no task authority. Edits and deletions of messages written by a removed device of the same person are accepted.
- Deletions and other copies that waited for an older device are released once it updates; history copies waiting on a capability are no longer stuck.
- Own-device sync messages (read marks, invitation sync, topic names and marks), history file requests and conversation deletions are now acknowledged once stored, so they no longer stay at the relay and get pushed again on every reconnect. Re-delivered copies are acknowledged again without being applied twice.
- Sends that v0.8.16 and older marked failed because the relay asked for an update are queued again, unchanged, once that device updates.
- The relay announces a device's session start and end again, so copies waiting for an updated device are released promptly.
- Agent setup shows your default agent and lets you change it in place, explains two agents that run the same program instead of forcing a choice, and a computer with no default agent says exactly how to set one.

## [0.8.16] — 2026-10-10

### Fixed

- Keep fresh browser messages and heartbeat processing moving while older messages wait for missing identity, membership or file evidence. Retain the exact encrypted input for the existing bounded recovery path, recheck current authority before committing, and acknowledge only admitted or explicitly held messages.
- Retain recovery wakes that arrive during an active pass and drain responsive encrypted messages in small pages, including after restart, without waiting for another heartbeat. Stop when evidence is unavailable instead of polling.
- Preserve an ordinary message's first receive time when delayed proof finishes, so recovery does not move it after later arrivals. Historical timestamps and control records keep their existing semantics.
- Open saved chats before restoring additional workspaces. Bound and cancel workspace identity and connection-policy checks without changing realm, consent or origin checks.
- Read only display-related records when building the browser's chat overview, model reports and notification senders, avoiding repeated copies of unrelated encrypted recovery data.
- Keep an acknowledged outbox copy from reverting to queued when an older compatibility check finishes late. In Comic, use the exact verified answer consistently for request status and its reply link, and remove the contradictory Sending clock without inventing delivery receipts for other copies.

## [0.8.15] — 2026-10-09

### Fixed

- Wait for the authenticated local page when reattaching after an independent daemon update, and reload an attached page even when its address is unchanged. A failed connection during restart no longer prevents the new page event from recovering it. Preserve ordinary setup transitions and reconcile the already-approved update without starting it again.
- Keep a failed incoming lookup from dropping or repeatedly blocking the rest of the phone's push stream. Retain the exact encrypted message for bounded recovery, and send a receipt only after admission. Reconnect recovery no longer delays fresh outgoing messages; downloads have a deadline and stop with the workspace.
- Recover older signed conversation participation history whose forwarding device was mistaken for the original author. Recovery still checks the original signature, verified human devices and disclosure scope; it does not accept rejected work or rerun tasks.
- Match direct-conversation answers to their shared request IDs across devices, and reconcile retained answered jobs through signed status updates. Pending lists distinguish missing completion evidence from an actual reported running state.
- Recover completion statuses when their exact request arrives later, including requests received through another personal device or retained history, and repair affected saved statuses after updating. Recovery keeps signature, executor and membership checks, and does not rerun work.
- Keep a saved Stop when a running agent exits with a needs-human response at the same time. The committed cancellation controls the request status and notices; interrupted runs remain uncertain and are never silently repeated.
- Check follow-up ordering only when claiming an actual correction, keeping ordinary worker queries small while preserving transactional ordering and permissions.
- Check the exact remote host's compatibility before marking a waiting request as handled, and explain which computer needs an update.

### Added

- In Comic, send a correction or follow-up to an exact agent request. A running owned Codex group question accepts steering through its existing native bridge; unsupported runs receive a queued follow-up. Uncertain acknowledgements remain explicit and never silently repeat the work.
- In Comic, select messages and move them to a new or existing topic, or merge the reviewed messages of one topic into another. Original messages, replies and files keep their identities, and later unselected messages stay in their original topic.
- In Comic, assign a reviewed message and selected available files to one of your agents. The new task preserves its source attribution and opens in the exact agent's private chat; retries retain the same task and file bytes.
- In Comic, share selected history from a chat into a group as an attributed, inert quotation. Review the destination audience and selected files before sending. Originals stay in their source chat, and imported questions or tasks do not run.
- Support Antigravity's `agy` responder through its native command-line setup, preserving the owner's tools, skills and permissions.
- Show an own agent's last reported model, or an explicit unknown value, across verified personal devices. This reports execution metadata without changing native model settings.

## [0.8.14] — 2026-10-09

### Fixed

- Keep incompatible invitation-sync copies from blocking ordinary questions, files and replies behind them.
- Synchronize older direct-agent conversations across verified personal devices, preserving the original sender, recipient, request, replies and file references. Downloading files still requires a linked source that retains them. Restored messages remain history and never rerun a task; these new copies wait for compatible clients.
- Retain admission receipts for restored history and repair missing receipts from earlier browser versions. Reuse held copies while their evidence is pending, and distinguish queued, stored, admitted and blocked transfers in sync progress.
- Stop unchanged conversation records, invitations, read markers and topic names from generating repeated encrypted copies when a receiving device holds them. New facts and revisions still synchronize normally.
- Avoid repeatedly hashing and probing Codex when no reply input is waiting. Actual inputs retain the same native identity, binding and receipt checks.
- Preserve the verified original author of forwarded participation records during history catch-up, and repair older transfer bookkeeping without accepting invalid records or rerunning work.
- Show repeated background sync failures in one expandable status, grouped by their recorded cause and sender, with bounded bulk archive. Archiving keeps the encrypted message blocked; new or changed reasons remain visible. Older records without a cause are explicitly unknown.
- Keep human invitations reachable directly in the chat at narrow desktop and phone widths, using the same exact Join and No thanks decisions as OKs.
- Bound CLI inbox output and provide exact-message lookup and continuation for large inboxes.
- Keep inherited own-person permissions visible after an individual device grant is removed. Show the actual computer for remote default agents.
- Let a verified own human device explicitly mark an exact remote needs-human request as handled. The host checks the request and attempt, and its confirmation updates the state without rerunning work. Attachment-only requests retain useful context in reports.

- Keep incoming messages and heartbeat acknowledgements moving while delivery receipts are delayed.
- Keep older direct-delivery completions or relay responses from erasing a newer pending delivery receipt, including after restart.
- Use recipient delivery for the chat-list waiting count, matching message ticks; a lagging extra device no longer leaves a delivered conversation marked as unsent.
- Preserve agent authorship in selected conversation history after reinviting an agent. Clarify that admitted action requests use native tools and permissions even when classified as questions.
- Include the owner in named agent labels so same-named agents can be distinguished in mentions and participant cards.
- Record participation device/conversation mismatches as synchronization diagnostics, without implying that a failed check proves signature tampering.

### Added

- In Comic, choose ongoing access to only the selected topic when bringing a person or agent into a chat. The signed scope applies to history, files, future messages and agent context; it does not narrow existing whole-chat access. Topic-only sharing requires compatible participants. Choose Yourself for replies; a selected agent or live-session receiver is refused before sending, preserving the draft.
- In Comic, change a suggested task before sending it to the same agent. Original and revised confirmations share a durable choice across personal devices, so simultaneous approvals and retries cannot create two runs. Revised text keeps its provenance and uses the agent owner's normal task permissions.

## [0.8.13] — 2026-10-09

### Fixed

- Speed up chat history and ordinary sends by indexing repeated delivery copies, reusing loaded messages, and returning topic changes after durable local enqueue. Reopen stays disabled until the refreshed state arrives.
- Keep typing responsive in long Comic conversations without reformatting the retained timeline on each keystroke. Phone catch-up avoids repeated full-store scans while preserving signed, encrypted history admission and background recovery.
- Show a multi-agent send as one human message with separate exact agent outcomes. Compact stopped requests, including older history, into an expandable row without rerunning them.
- Allow two already-running agents to answer each other's exact child questions without deadlocking their worker lanes. Cancellation, current permissions, ordered ordinary work and update fences remain enforced.
- Stop counting terminal local and outgoing requests as pending. Add rename, archive, reopen and exact-message deletion for older Main flows in Comic; replace the phone's ambiguous Back count with a clear Chats label.
- Show received human guest invitations in Comic OKs with the existing Join and No thanks actions.
- Use each agent owner's native tools, skills, configuration and permissions unchanged for questions and accepted tasks. Remove AgentNet's injected tool restrictions and sandbox/approval overrides; native requests for human approval still require attention. Exact recipient bindings and AgentNet's existing task acceptance and grants remain enforced.
- Render Markdown attachment previews with the existing safe renderer while preserving original downloads byte for byte.

## [0.8.12] — 2026-10-09

### Added

- Choose `@everyone` or a shared list such as `@Reviewers` to address people and agents already in the current chat. The composer expands the tag into visible, exact recipients, deduplicates overlapping tags, and preserves each agent's independent send and retry. List managers can select people, agents, or both; everyone can use the tag. Mixed lists require an updated relay.
- Enable automatic tasks from all your verified devices in the receiving computer's Permissions settings. This uses the existing person grant, includes future linked devices, and preserves native permissions and independent device grants. Previously waiting, failed or interrupted tasks are not restarted.
- Open a topic's exact pending requests from its menu to see what keeps it waiting and use the existing actions. Retained history and later replies update this list without rerunning or closing unrelated work.

### Fixed

- Display an available update as a prominent version heading beside Update AgentNet. The previous update result remains secondary while a newer version is available.
- Keep local default and named agents together, label their computers, and explain which requests use the default agent.
- Show an independent trusted-device task permission as inactive after its pinned key changes, matching the existing execution checks.
- Simplify group invitation wording: selected people receive invitations and choose whether to join.

## [0.8.11] — 2026-10-09

### Fixed

- Exclude copies sent from another own device from desktop topic unread badges, matching the chat timeline while retaining genuinely unread incoming messages.
- Offer Mark as handled for ordinary person-to-person requests held in OKs on phones and all skins. Keep the message in its chat and send no reply or agent work. A failed report dismissal no longer shows a success message.
- Reconcile history across verified own human devices, including missing transfers, late arrivals and interrupted catch-up. Recent messages are prioritized for catch-up, with older pages continuing in the background; a chat awaiting proof no longer blocks other chats.
- Retain verified historical agent messages after the agent's host leaves and rejoins a group. Original signed membership evidence travels only as inert encrypted history; current membership, identity and execution checks stay enforced.
- Preserve original message edits during linked-device catch-up, including own edits whose original readers have withdrawn. Historical controls retain exact signature, author, target and admission checks.
- Give requested files a bounded turn during bulk history catch-up, while both file delivery and remaining history continue through their existing permission checks.
- Make context-pending Held back notices archiveable without deleting the retained message or stopping automatic recovery. A changed safety reason brings the notice back.
- Show pending outgoing group invitations on the sender's other verified human devices. Status updates survive offline delivery and reordering; a copy cannot accept an invitation or repeat an action.
- Reconnect the phone's push stream after device linking so approval can finish and history can arrive.
- Synchronize private topic names and resets across verified own human devices, including offline changes and names saved before updating. Renaming a topic does not change anyone else's private label.
- Open chat previews at their exact message and topic, including attachments and linked-device copies. Delayed history keeps the target until the person chooses another destination.
- Mark only the displayed topic's messages as read and refresh topic badges after local or linked-device reads. Opening Main leaves unseen topics unread.

### Improved

- Put frequently used emoji first in reaction pickers and sort human reaction chips by count, keeping stable ties and separate agent marks. The bounded preference is local to this browser and counts successful additions only. Legacy phone pickers retain visible touch targets for every choice.
- Keep history progress unfinished while older or deferred messages remain, and distinguish queued history from confirmed delivery.

## [0.8.10] — 2026-10-08

### Fixed

- Add an explicit Check for updates button in every desktop skin. It checks the published stable release independently of the server's recommendation, shows the installed app version, and distinguishes an available update, current or newer build, and lookup failures. Checking does not install, pause work or poll in the background.
- Hide the phone conversation's stale Bring back action when the exact agent has already rejoined or is awaiting rejoin. A fresh check before opening the invitation also discards results after navigating to another conversation.

## [0.8.9] — 2026-10-08

### Fixed

- Recover retained linked-device group history that older clients rejected after an assistant ended. Recovery is limited to authenticated inert history from current own-human devices; ordinary rejected work stays blocked and current admission checks still apply.
- Let an exact causal group request call back through remote agents to its busy local ancestor, retaining executor ordering, cancellation, authority and update fences.
- Install a Linux AppImage in the user’s application data directory on first opening, then register and update that stable copy. Removing the download no longer removes the installed app; conflicting installations and custom launchers are preserved.
- Shorten long raw link labels in every bundled skin while retaining full targets, original messages, descriptive labels and code.
- Remove the redundant selector when setup already has one exact agent selected, retaining its identity and working folder.
- Describe needs-human outcomes as needing attention and explain when native permissions or environment need repair before a deliberate reply or retry. No native permissions are changed.

## [0.8.8] — 2026-10-08

### Fixed

- Updating checks the current app file before shutdown. A missing or changed
  executable leaves the running app open; losing it afterward reports an honest
  recovery path instead of claiming the previous app is still available.
- Reply on a waiting agent request sends its clarification answer to that exact
  request, locally or from a verified own human device. Retries retain the same
  answer ID; ordinary quoted messages never restart work.
- Multiple selected agents share one visible human message while retaining
  separate requests, files, results and retries. Explicit grouping survives
  reload and linked history; older peers still receive compatible requests.
- Codex can ask another agent in its current group through its native tool
  transport, retaining the question sandbox and the existing room permissions.
- Outdated group invitations can be declined offline. Joining still requires
  current verified consent; declining never grants membership.
- Old agent cards no longer offer Bring back when that exact agent already
  has an active or pending participation. Historical context stays separate.
- Desktop review notification clicks open the installed AgentNet app at the
  exact request and workspace, or the review list for grouped and remote notices.
  Without the app, review uses the existing coding-agent opener instead of an
  unauthenticated browser page. Opening grants no permission and runs no request.
- Confirmed suggested tasks show compact approval, target and current status in
  Comic, Classic and Zoom. The proposal stays linked and exact approved text
  remains available in a disclosure, including when the original is unavailable.
- Authorized requests for distinct local agents start concurrently. Ordinary
  requests to the same selected agent stay ordered; cancellation, exact-parent
  child work and whole-app update checks retain their existing safety gates.

## [0.8.7] — 2026-10-08

### Fixed

- Linked-device history retains previously accepted assistant requests, replies,
  status records and selected context after the assistant leaves. Signed lifecycle
  records keep their original author. Copies remain inert and current device,
  membership, consent, signature and encryption checks stay enforced.
- One human message can select multiple agents. Each exact agent gets its own
  request and result; retrying a failed request does not resend successful ones.
  Repeated mentions are deduplicated. Comic, Classic and Zoom support the flow
  on computers and phones using the existing request and permission paths.
- Held back notices retain safe, specific failure details for new rejections.
  Older records explicitly say when details are unavailable. Archive notice hides
  only that local invalid-message notice; its encrypted copy stays blocked and
  cannot be accepted or executed by archiving.

## [0.8.6] — 2026-10-08

### Fixed

- Browser attachment uploads use the same bounded request timeout as other
  sends, allowing reconnect and queued requests to recover from stalled uploads.
- Deleting a queued question or task stops its remaining local copies, including
  after restart. Delivery already attempted stays explicitly uncertain; confirmed
  delivery and existing agent work are preserved.
- Linked-device group history continues when an original control recipient has
  left. Browser snapshots also supply the existing signed group context, including
  empty groups and recovery of previously completed copies missing that context.
- Approval details retain the agent's full explanation in every skin. Classic
  opens the request's actual topic when it is outside the current conversation view.
- Browser agent setup offers the existing computer installer and device-link flow,
  with approval on the original device before the new computer joins the person.
- Zoom shows signed agent execution progress in direct device threads as well
  as conversation messages.
- Native release links use the app's existing external navigation handler instead
  of being intercepted by an unpermitted plugin command.

## [0.8.5] — 2026-10-07

### Fixed

- Linux AppImage updates preserve host command-search paths when removing the
  old mount's environment. The replacement can find FUSE tools and reopen;
  an immediate exit, including exit status zero, is reported as a restart failure.
- Reopening after a failed restart resumes app and command verification. An
  independently running daemon still waits for active jobs and must confirm
  its new version before the update reports completion.
- Native update controls recognize the app's authenticated session after its
  normal sign-in redirect removes the URL token. The session cookie and bound
  page must still match before an update can run.
- Restarted apps establish their session before opening the main page, avoiding
  an authorization error after the splash screen. Conversation and invitation
  links survive the handoff; the session token leaves the address and history.

### Upgrading from v0.8.3 or v0.8.4

Those AppImages contain the earlier updater. After installing this update,
open AgentNet once from your launcher if it closes without reopening. The new
updater takes effect after that first launch.
If v0.8.4's Update button is unavailable, use `agentnet update v0.8.5` once
for the registered desktop installation, then reopen AgentNet after it closes.

## [0.8.4] — 2026-10-07

### Fixed

- Remind me is available on your own sent messages as well as received messages,
  using the same personal reminder editor and saved message reference.
- Group agents see configured agent names and exact recipient identities.
  Pi and OMP questions can ask another admitted group agent and wait for its
  answer through the existing room tools; selected local child work can run
  while its parent waits. Message details distinguish the selected recipient
  and an existing approval proposal from an ordinary request.
- Comic reminders have separate, labeled Date and Time fields. Typed hour and
  minute changes persist, including when reopening the reminder editor.
- Browser requests time out if their headers or response body stall, allowing
  reconnect and queued sends to resume with their original message IDs.
- Group messages keep an older participant’s encrypted copy waiting for their
  update while current participants can receive the message.
- Linked-device group history preserves signed timestamps for edits, deletions
  and reactions even when local storage finishes in a later second.
- Desktop updates restart from a stable directory and report immediate launch
  failures. About exposes update controls for an independently running daemon;
  replacement waits for accepted work to finish.
- Chats with each person open into a stable Main flow. Earlier conversations
  and their topics appear together in All topics, retaining their messages,
  drafts and original participants.
- Chats notify by default, with one Mute/Unmute choice in each chat. A person's
  mute covers Main and their topics, including later synced conversations.
  Existing quiet choices are preserved; group alerts do not require a separate DM.
- Agent setup uses one flow for supported installed Claude, Codex, Pi and OMP.
  Native hook availability is shown separately from whether an agent can run.
- Codex and Claude no longer appear to need setup merely because another tool
  changed their settings file's formatting.
- OMP can answer questions and run accepted tasks using its own settings,
  skills, extensions and permissions. Questions restrict editing and new approvals.
- Agent startup diagnostics distinguish stored waiting time, process launch,
  first output and launch failure, without exposing message contents.
- Agent attachments offer an explicit download and a bounded plain-text preview.
  HTML and SVG downloads stay files instead of opening inside the app.
- Desktop skin import uses the native folder picker and validates the selected
  package before importing it.
- Message Details identify the device and missing reader support when a
  recipient's copy is waiting for an update.
- Zoom sends retain the selected chat route and keep typed text when that route
  changes while the send dialog is open.
- Incomplete group participation records appear as neutral context, rather than
  an invented assistant invitation waiting for approval.
- Stopped or failed requests to your own agent show their execution state and
  offer an explicit retry, rather than asking for approval again.
- Held-back messages no longer describe every rejected operation as a failed
  identity check. Their contents stay hidden and they cannot start work.

## [0.8.3] — 2026-10-07

### Fixed

- Direct agent/device requests already running or stopping no longer count
  as waiting for your OK. They remain visible as Working, with access to
  their request and Stop control. This completes the conversation-only fix
  shipped in v0.8.2.
- With a desktop app registered, both About and `agentnet update` include
  the app, its private command and verified official terminal copies, including
  an earlier copy on PATH. A closed app opens for the update; an unavailable
  app reports the problem without silently updating only the CLI.
- An already-current app repairs an outdated official command. Update status
  stays pending until the restarted app and all required command copies match;
  failed launches and partial command updates never report success.
- Custom or inaccessible commands identify the exact blocked path. The
  canonical Replace command choice never claims it replaces a different PATH
  entry. Check and status commands remain read-only.
- Recognize an official standalone CLI's module-version metadata before
  checksum verification, so a desktop upgrade adopts it instead of incorrectly
  labeling it a custom build. Custom or modified binaries remain protected.

### Upgrade and verification

From v0.8.1/v0.8.2, use **Settings → About → Update AgentNet** once; the
older standalone command retains its older updater until replaced. v0.8.3
adopts unchanged official commands and routes later updates through the app.
CLI-only installations and relay deployments keep their separate update paths.

Native Linux/Windows/macOS CI, race tests, browser-storage checks, container
checks and desktop builds passed at `5aec1196`. Linux package qualification
replaced an isolated AppImage, restarted the real desktop shell/backend and
verified all managed command versions and bytes plus the completion record.
Direct-device OKs regressions passed in Comic, Classic and Zoom on desktop and
phone fixtures. All 12 release asset digests and 11 package checksums matched.
Interactive Windows/macOS upgrades and a graphical About-button click remain
unverified. No existing installation was changed during qualification.

## [0.8.2] — 2026-10-06

### Fixed

- Group conversations offer scoped human guests by default. Adding a permanent
  member is a separate admin choice. Guests receive selected earlier context
  and new conversation messages until dismissed; they gain no membership or
  invitation rights.
- Edits and deletions reach the guests who received the original message and
  are still participating, including a guest's corrections to their own messages.
- Pending member invitations can be retracted or refreshed with fresh consent.
- Comic keeps running requests out of approval counts, offers Stop separately,
  and opens the request's actual topic without losing an unsent draft.
- Edited or deleted messages update conversation previews.
- Reading a message clears its unread state on your other current, verified
  human devices. Exact message references are encrypted; newer unseen messages
  stay unread, and offline devices catch up after reconnecting.
- Notification settings show one row per verified person, with separate
  conversation mutes that preserve existing choices.
- New Chat prefers a person's populated conversation; conversation and topic
  counts explain their different scopes.
- Local queued agent work wakes promptly. Ordinary answers no longer instruct
  the agent to close an ongoing topic; explicit topic closure stays available.
- Internal review-status records stay out of chat messages and topic titles.

### Updates

- The app-managed `agentnet update` command uses the About page's whole-app
  updater, updating the desktop app and its bundled command together. Active
  jobs block preparation; standalone CLI and relay deployments remain separate.

### Upgrade notes

- From v0.8.1, use **Settings → About → Update AgentNet** once. After installing
  v0.8.2, the app-managed command can also update the whole app. A v0.8.0 desktop
  installation needs the matching desktop package installed manually once.
- Keep your existing identity, data directory and permissions. A separately
  managed daemon sharing the app's home must be stopped when idle first.

### Compatibility

- Update participating devices for group human guests, refreshed/retracted
  invitations and linked-device read-state synchronization. Older readers hold
  the new records for an update; permissions and existing message histories are
  preserved. Read state is shared only among your own verified human devices.

### Documentation

- Reworked the README around company work: topics, guests, existing assistants,
  and talking to agents on your devices. Technical and maintainer guides are
  expandable.
- Clarified desktop, standalone CLI and relay update paths, including the
  one-time desktop-package upgrade from v0.8.0.
- Linked the separate [skin collection](https://github.com/misunders2d/agentnet-skins)
  and its reusable creator and compatibility workflow.

### Verification and remaining limits

Native Linux/Windows/macOS CI, race tests, real browser-storage checks,
container checks and desktop builds passed. A Windows outbox-flush timeout
passed on the unchanged candidate's rerun. All release assets were checksum
verified. An isolated real Codex journey verified ordinary replies keep a
topic open and an explicit close ends it.

No existing installation was upgraded for qualification. Interactive
Windows/macOS installation, physical-device read-state convergence and a live
in-app upgrade remain unverified. The worker-wake fix does not establish that
every startup delay is eliminated. Private Projects remain a design discussion.

An existing official standalone CLI can be misclassified as a custom build and
left at its older version. In v0.8.2, use **Settings → Your agent → Replace
command…** once to adopt the app's copy; later app updates refresh that managed
copy. The automatic recognition correction shipped in v0.8.3.

## [0.8.1] — 2026-10-06

### Added

- Whole-app updates in **Settings → About → Update AgentNet**, with package
  checksum verification, restart and managed AgentNet command/hook refresh.
- Direct chat entry points for this computer's agent and your other devices'
  agents in Comic, even before a conversation exists.
- Synchronization of empty conversations between updated human devices of
  the same verified person.

### Changed

- Comic groups a person's conversations under one chat entry. Separate
  conversations retain their own messages, topics and participants.
- Agent setup separates choosing who answers requests from connecting coding
  tools. It explains working folders and when a new session is needed.
- Profile pictures use drag-and-zoom cropping and remove unsupported metadata
  before upload. Pasted images take precedence over accompanying text, with
  a native desktop clipboard fallback.
- People lists are distinguished from group chats. New groups require a name;
  deleting a list keeps its chats.
- Desktop apps install and refresh the AgentNet command for connected tools.
  Custom command replacements require an explicit choice.

### Fixed

- Linked-device history recovery when an original group author has left.
- Editing or deleting your own messages after everyone else leaves a group.
- Pending group invitations disappearing before acceptance or refusal.
- Named-agent follow-ups losing the selected executor, and local questions
  to your own agent requiring a sender grant.
- Stale tool-connection status when returning to setup.
- Incomplete mention identities and missing own-device agents in invitations.
- Deleted people lists returning from an older cached snapshot.

### Upgrade notes

- v0.8.0 desktop users install the v0.8.1 desktop package once. Later desktop
  updates use the About page. `agentnet update` updates a standalone CLI,
  not a desktop app. Relay upgrades are separate.
- Update both linked endpoints for empty-conversation synchronization.
- Preserve the existing identity, data directory and permissions; ordinary
  upgrades do not require re-enrollment.

### Verification and remaining limits

Native Linux/Windows/macOS CI, race tests, container checks and desktop builds
passed. Release assets were checksum-verified. An existing relay and Linux
AppImage installation were upgraded with identity preserved. Interactive
Windows/macOS installation, native photo/clipboard behavior, physical-phone
history convergence and a future in-app update remain separate live checks.

Known follow-ups include agent startup delays, misleading OKs/navigation,
stale edited-message previews, cross-device unread state, duplicate people in
notification settings and internal status data in some agent topics. See the
[handoff](docs/HANDOFF.md) for current tracking and
[v0.8.1 release notes](https://github.com/misunders2d/agentnet/releases/tag/v0.8.1)
for additional detail.

<details>
<summary>Maintainers and coding agents: keep this record current</summary>

Add notable user-facing changes under **Unreleased** in the same change that
implements them. Describe the resulting behavior in plain English. Use Added,
Changed, Fixed, Removed or Upgrade notes only when needed; documentation-only
changes can use Documentation. Plans and reported bugs are not completed fixes.

At release, move the shipped entries into a version section with the actual
publication date and release link. Keep an empty Unreleased section for the
next work. Record required upgrade steps, compatibility changes and material
known limits. Verify entries against the released commit; do not infer live
platform qualification from builds. Keep detailed commands in the installation
guide and test/rollout evidence in the handoff.

</details>

[0.8.22]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.22
[0.8.4]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.4
[0.8.3]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.3
[0.8.2]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.2
[0.8.1]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.1
