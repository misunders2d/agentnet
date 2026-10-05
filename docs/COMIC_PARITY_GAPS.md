# Comic reply continuation gap (MEL-528)

Service role, teams and Google Drive have Comic controls. Reply receiver
selection and continuation status remain available in **Classic** (and Zoom).
The owner must see this remaining gap before Comic replaces those interfaces.
Claude approved deferring this larger port in fixes-1005-p24-codex2.

Classic lets a person do the following, in order, from its composer:

1. Expand **Reply receiver** and keep **Me (human)**, or explicitly
   select this device or another device in their own verified person roster.
   The device address and exact fingerprint must still match that roster.
2. Read that host's enabled, ready managed agents and registered native reply
   sessions. Local sessions name Pi, OMP, Codex or Claude; remote sessions
   omit Claude until its separate remote receipt gate passes. A registered,
   inactive session is labelled as such; registration does not prove it is
   running. Remote lists distinguish pending approval, ready and unavailable.
3. Select a specific managed agent or a specific session handle and harness.
   A previously selected unavailable item stays visible and blocks sending;
   the UI does not silently replace it with a default.
4. For a managed agent, write the original continuation instructions and
   explicitly choose question or task mode. For a Pi/OMP/Codex live session,
   optionally preauthorize a specific managed backup with original instructions
   and mode, to receive replies after the session closes. Claude sessions
   cannot use this backup. The selected backup's exact configuration is bound
   to the choice and must be chosen again if it changes.
5. Send with that explicit reply receiver. Before sending, Classic rereads
   the relevant agent/session catalogs, verifies the same workspace and local
   identity, proves remote host ownership by exact keys again, checks the
   handle's harness and the backup configuration, and rejects stale choices
   while retaining the draft. Remote human routing also requires this proof.
   Automatic guest-room routing keeps its existing gates.
6. Read the latest reply receiver bindings in the composer: original request
   held waiting for receiver approval/Ready, selected receiver state and
   detail, incoming continuation state, native provider state and detail, and
   the preauthorized backup's held/handed-over state. These stay separate from
   message delivery and do not claim that native effects completed.

Comic currently offers none of these selector, backup or binding-status
controls. This is the only deliberately excepted area in `comicParity`:
`12 reply receivers` and `12 reply sessions`. They must not be removed from
Classic while the port remains open. A small dropdown would omit the send-time
identity, session and configuration checks above; the complete port needs
composer/draft integration plus stale-choice and remote-host regressions.

Source: `internal/ui/skins/classic/src/entry.mjs`, functions
`localReceiverCatalog`, `localReplySessions`, `remoteReplySessions`,
`receiverHostProof`, `loadReceiverCatalog`, `prepareReplyReceiverSelection`,
`renderReplyReceiver` and `renderReceiverStatus`. Zoom keeps the same flow.
