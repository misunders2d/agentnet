# AgentNet v0.8.2 — release candidate

This version is being verified; v0.8.1 remains the published release.
See the [changelog](../CHANGELOG.md) for release history and the
[installation/update guide](revival/INSTALL.md#updating-and-downgrading)
for desktop, standalone command and relay instructions.

## Changes

- **Bring someone into a group as a guest.** Share selected earlier messages;
  the guest can participate in that conversation until dismissed. Adding a
  permanent member is a separate admin choice. Guests gain no member,
  invitation or agent-execution permissions.
- **Retract or refresh a pending member invitation.** Each refreshed proposal
  needs fresh consent. A stale Join cannot accept a different proposal.
- **OKs shows requests that need a decision.** Running requests have a separate
  Stop action. Opening a request selects its actual topic and preserves your
  unsent text, attachments and reply.
- **Read once across your devices.** Reading a message clears that exact
  message's unread state on your other verified human devices. Newer unseen
  messages stay unread; offline devices catch up when they reconnect.
- Edited and deleted messages update the chat preview. Notification settings
  show each verified person once, with explicit conversation mute overrides.
- Opening a person prefers a populated conversation. Separate conversations
  remain available, with clearer conversation and topic counts.
- Local queued agent work wakes promptly. Ordinary answers keep the topic
  open; explicitly asking to close the topic and manual Done/Reopen remain
  available. Internal review records stay out of messages and topic titles.
- **Update the app and its command together.** An app-managed `agentnet update`
  uses the same whole-app updater as Settings → About. Active agent work blocks
  update preparation, and new work waits during the installation handoff.

## Updating

From v0.8.1, use **Settings → About → Update AgentNet** once to install this
version. v0.8.1's bundled command does not yet initiate whole-app updates.
Once v0.8.2 is installed, its managed command and About button both update the
desktop package and managed command copies together. Custom commands retain
their explicit replacement choice.

A standalone `agentnet` command updates only that command. Relay deployments
remain separate. Preserve existing identities, data, permissions and service
settings; an ordinary upgrade does not require re-enrollment. A separately
managed daemon sharing the app's home must be stopped when idle first.

Update participating devices for group guests, invitation refresh/retraction
and linked-device read state. Mixed-version recipients hold new encrypted
records until their sessions support them; existing history and permissions
remain intact. Read-state records contain encrypted exact message references,
shared only among your current verified human devices.

## Verification and remaining limits

Focused native/browser regressions and rendered Comic, Classic and Zoom
checks have passed. Full candidate race tests, native CI, desktop builds and
release-asset verification are still pending. An isolated three-turn real
Codex check passed: two ordinary replies kept the same topic active, and an
explicit request closed it. No existing device or relay has been upgraded to
this candidate. Interactive Windows/macOS installation and physical-device read
state convergence remain separate live checks.

Private Projects are a [design discussion](DECISIONS.md#10-private-projects-and-selected-guest-context-discussion-oct-6),
not part of this release. The proposed model separates lasting member invites
from temporary guests who receive only selected/approved context.
