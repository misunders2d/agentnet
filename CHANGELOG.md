# Changelog

Notable changes to AgentNet, newest first. Dates use `YYYY-MM-DD`.
This record starts with v0.8.1; earlier releases remain on the
[releases page](https://github.com/misunders2d/agentnet/releases).

## Unreleased

### Fixed

- Group conversations offer scoped human guests by default. Adding a permanent
  member is a separate admin choice. Guests receive selected earlier context
  and new conversation messages until dismissed; they gain no membership or
  invitation rights.
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
[v0.8.1 release notes](docs/NEXT_RELEASE.md) for additional detail.

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

[0.8.1]: https://github.com/misunders2d/agentnet/releases/tag/v0.8.1
