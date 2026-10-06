# AgentNet v0.8.1 — release candidate

## Changes

- One chat per person in Comic. Separate conversations stay inside that chat,
  with their own messages, topics and participants. Existing conversations are
  retained; opening a person selects a populated conversation when available.
- Linked devices recover earlier messages even when an original group author
  has since left. Empty conversations also synchronize between current human
  devices of the same person. Update all devices for empty-conversation sync.
- You can edit or delete your own message after everyone else leaves a group.
  Pending group invitations remain visible until accepted and added, declined
  or revoked. An invited person is not counted as a joined member.
- Profile photos are cropped with drag and zoom controls. Cropped PNGs have
  unsupported metadata removed before upload; the server keeps strict checks.
- Pasted images take priority over accompanying text. The desktop app can read
  an image from its system clipboard when the webview supplies no image file.
- Settings separates who answers requests from connecting AgentNet to your
  tools, explains reconnect requirements and describes the working folder.
- People lists are clearly distinct from group chats. New groups require a
  name. List managers and workspace admins can delete a list without deleting
  its chats; deleted lists do not return from an old cached snapshot.
- The mention picker resolves an incomplete peer view from already verified
  identity data. Bringing in an agent includes your other devices' agents.
- The app installs and refreshes its command for coding tools. Custom builds
  require an explicit replacement choice. Settings shows the command location.
- Update AgentNet downloads and verifies the published package before replacing
  the app, then restarts its daemon and refreshes managed command/hook copies.
  Failed preparation keeps the current app; installer failures use a prepared
  recovery copy. Windows shell asset links remain inside the app.

## Compatibility and verification

Empty-conversation replication uses the explicit `crs1` capability. Older
readers wait for an update. A carrier preserves the original signed root and
creates no message, unread count, notification or agent task. Only current
human devices of the same verified person can send or receive it.

Local browser checks cover Comic, Classic and Zoom at desktop and phone
widths, including photo saving and pending invitations. Native photo saving
and clipboard paste remain unverified: the isolated Linux test window did
not render and was closed. Windows/macOS cross-compilation and mocked
installer recovery do not establish real installation or native UI behavior.

This document describes the candidate, not a published release or completed
rollout. Final race checks, CI and deployment evidence belong in the handoff.
