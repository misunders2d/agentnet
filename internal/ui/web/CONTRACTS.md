# Messenger source: contracts between screens

The default interface is a React app (`src/`), built by `build.sh` into
`../static/messenger.mjs` and `messenger.css` (plus fonts and emoji data in
`../static/m/`). It talks to AgentNet only through the UI host's API v1
(`docs/UI_SKINS.md`), wrapped by `src/api.ts`, whose request and response
types are generated from Go (`src/api.gen.ts`, see `internal/ui/tsgen_test.go`).

## Shared foundation (do not change without the integrator)

| File | What it owns |
|---|---|
| `api.ts`, `api.gen.ts` | every route, typed; `errorText` |
| `store.ts` | overview, the open conversation, connection, drafts, toasts, tab, invite sheet, room panel; `store.run(fn, okText)` performs an action and reports failure in words |
| `model.ts` | names, chat list items (`ChatItem.needsYou`: OKs this device gives in that chat; `ChatItem.held`: questions or tasks held there for you, never counted as OKs), participants, plain-language states, time words, hues, initials; people by device (`personOf`, `isMine`, `nameOf`); conversation items from `overview.needs_you` / `.held` (`Reason`, `decidable`, `chatOf`, `chatName`, `senderOf`, `convTitle`), shared by the chat list's banner and OKs |
| `context.tsx` | `useApp()` (the store), `useAgentNames()`, `useWide()` |
| `App.tsx` | frame and navigation |
| `ui/Avatar.tsx` | `PersonAvatar`, `AgentAvatar` (moods: neutral, working, done, waiting, asleep), `GroupAvatar`, `Presence` |
| `ui/Button.tsx` | `Button` (variants act, outline, ghost, danger, agent; sizes sm, md, lg), `IconButton` (44px, label, badge) |
| `ui/Tag.tsx` | `Tag` (guest, agent, ok, act, muted, danger) |
| `ui/Sheet.tsx` | `Sheet`: bottom sheet on phones, centred card on desktop |
| `styles.css` | tokens (Tailwind theme: `bg-canvas`, `bg-surface`, `text-ink`, `text-muted`, `bg-mine`, `bg-agent`, `text-agent-ink`, `bg-act`, `text-guest-ink`, `bg-guest-bg`, `text-ok-ink`, `bg-ok-bg`, `text-danger`, `shadow-pop`, `font-display`), `.stroke`, `.dots`, `.press`, `.pop-in`, `.fade-in`, `.working-dot` |

## Screens and their exports

| Owner file(s) | Exports | Used by |
|---|---|---|
| `features/ChatList.tsx`, `features/NewChat.tsx`, `features/WorkspaceSwitcher.tsx` | `ChatList()`, `WorkspaceCoin()`; for Settings: `JoinSheet`, `LeaveSheet`, `workspaceLabel(w)`, `useDisconnected(ws, revision)`, `useReconnect(ws, onDone)` → `{ busy, error, done, reconnect }` (a failure is shown under its row, not as a toast) | App, Settings |
| `features/Conversation.tsx`, `features/Message.tsx`, `features/Markdown.tsx` | `Conversation()` (reads `store.open`, `store.dm`/`store.thread`), `MessageView` | App |
| `features/Composer.tsx`, `features/Emoji.tsx` | `Composer({ dm?: T.DMThread; thread?: T.Thread })`, `EmojiPicker({ onPick, onClose })` | Conversation, Message (reactions) |
| `features/InviteSheet.tsx`, `features/RoomPanel.tsx` | `InviteSheet()` (renders from `store.invite`), `RoomPanel()` (desktop side panel; renders nothing when closed or no conversation), `RoomSheet()` (phone) | App/Conversation |
| `features/Approvals.tsx` (private: `Approvals.card.tsx`, `.conv.tsx`, `.parts.tsx`, `.reports.tsx`, `.sheets.tsx`, `.title.tsx`, `.words.ts`), `features/AgentsView.tsx` | `OksView()`, `needsYouCount(overview)`, `useNeedsYou()` (the one count for the OKs badge, the home banner and the OKs header: decisions this device gives, never held questions or items another device decides), `ApprovalCard({ message, dm?, thread? })`, `AgentsView()` | App, ChatList, Message |
| `features/Settings.tsx` | `Settings()` | App |

A screen may add private helper files named after it (for example
`features/Message.reactions.tsx`). Do not edit another screen's files or the
shared foundation; describe what you need from them in your report.

OKs reads the overview (`needs_you`, `held`, `review`, `group_invitations`,
`links`); `Approvals.chats.ts` (the old per-chat scan) is gone. The one
other read: an invitation card reads its chat once (`/api/dm`) to name what
your agent would see there; when that fails, its button opens the chat to
decide.

## Rules

- **Truth:** show only what the server's views prove. Delivered is not read;
  a request is Working only when its executor says so; never invent "Seen".
  Drive buttons from the server's `can[]`, `actions[]`, `can_*` fields.
- **Authority:** nothing typed, mentioned or reacted grants anything. Asking an
  agent goes through `askAgent({pid, kind: "question"|"task", body})`;
  approvals go through `act` / `decideAgent` / `decideGuest` / `decide`.
- **Words:** plain language (see the design contract). No addresses, keys,
  fingerprints, "responder", "receiver", "participation", "realm", "custody",
  "lens" in the normal flow; put technical details behind a Details toggle.
- **CSP:** no `style="..."` in markup strings, no `dangerouslySetInnerHTML`,
  `innerHTML`, `eval`. React's `style` prop is fine (it uses the DOM style
  object). Libraries must not inject `<style>` elements.
- **Mentions:** `[@Name](agentnet:person/ID)` and `agentnet:guest/PID` stay the
  wire format in message text (see `app.js` `mentionRef`, `encodeMentions`).
- **Access:** every target at least 44px; visible focus; labels on icon
  buttons; `role="log"` for the timeline; works at 390px and 1440px, light
  and dark; reduced motion turns loops and slides into short fades.
- **Design:** `docs/plans/MESSENGER_DESIGN.md` (the "Pop Huddle" contract)
  and the mockups it names.
