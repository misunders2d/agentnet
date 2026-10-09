# Changelog

Notable changes to AgentNet, newest first. Dates use `YYYY-MM-DD`.
This record starts with v0.8.1; earlier releases remain on the
[releases page](https://github.com/misunders2d/agentnet/releases).

## Unreleased

### Fixed

- Keep incompatible invitation-sync copies from blocking ordinary questions, files and replies behind them.
- Synchronize older direct-agent conversations across verified personal devices, preserving the original sender, recipient, request, replies and files. Restored messages remain history and never rerun a task; these new copies wait for compatible clients.
- Retain admission receipts for restored history and repair missing receipts from earlier browser versions. Reuse held copies while their evidence is pending, and distinguish queued, stored, admitted and blocked transfers in sync progress.
- Stop unchanged read markers and topic names from generating repeated encrypted copies when a receiving device holds them. New reads and title changes still synchronize normally.
- Preserve the verified original author of forwarded participation records during history catch-up, and repair older transfer bookkeeping without accepting invalid records or rerunning work.
- Show repeated background sync failures in one expandable status, grouped by their recorded cause and sender, with bounded bulk archive. Archiving keeps the encrypted message blocked; new or changed reasons remain visible. Older records without a cause are explicitly unknown.
- Keep human invitations reachable directly in the chat at narrow desktop and phone widths, using the same exact Join and No thanks decisions as OKs.
- Bound CLI inbox output and provide exact-message lookup and continuation for large inboxes.
- Keep inherited own-person permissions visible after an individual device grant is removed. Show the actual computer for remote default agents.
- Let a verified own human device explicitly mark an exact remote needs-human request as handled. The host checks the request and attempt, and its confirmation updates the state without rerunning work. Attachment-only requests retain useful context in reports.

- Keep incoming messages and heartbeat acknowledgements moving while delivery receipts are delayed. Pending invitation synchronization no longer blocks ordinary queued turns.
- Use recipient delivery for the chat-list waiting count, matching message ticks; a lagging extra device no longer leaves a delivered conversation marked as unsent.
- Preserve agent authorship in selected conversation history after reinviting an agent. Clarify that admitted action requests use native tools and permissions even when classified as questions.
- Include the owner in named agent labels so same-named agents can be distinguished in mentions and participant cards.
- Record participation device/conversation mismatches as synchronization diagnostics, without implying that a failed check proves signature tampering.

### Added

- Choose ongoing access to only the selected topic when bringing a person or agent into a chat. The signed scope applies to history, files, future messages and agent context; existing whole-chat invitations stay separate. Topic-only sharing requires compatible participants and uses Yourself for replies when a selected agent or live-session receiver would escape that scope.
- Change a suggested task before sending it to the same agent. Original and revised confirmations share a durable choice across personal devices, so simultaneous approvals and retries cannot create two runs. Revised text keeps its provenance and uses the agent owner's normal task permissions.

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

[0.8.4]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.4
[0.8.3]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.3
[0.8.2]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.2
[0.8.1]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.1
