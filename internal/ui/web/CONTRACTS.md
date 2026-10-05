# Comic source: contracts between screens

This is the source of **Comic**, AgentNet's default skin: a React app
(`src/`) that `build.sh` builds into the standalone skin package
`../static/skins/comic/` (`skin.json`, `entry.mjs`, `style.css`,
`document.css` with its fonts' faces and Tailwind's registered properties,
and fonts and emoji data in `m/`), embedded in the program and loaded by the
UI host exactly like any other skin (`docs/UI_SKINS.md`). `dev.sh OUT` builds
the same package unminified; `dev-proxy.mjs` serves it in front of a daemon.
It talks to AgentNet only through the host's public API v1, wrapped by
`src/api.ts`, whose request and response types are generated from Go
(`src/api.gen.ts`, see `internal/ui/tsgen_test.go`); `src/host.ts` types the
host, including its documented additions (`onSkinsChange`,
`manageLocalSkins`, `reconnect`, `onOpen` destination kinds).

## Shared foundation (do not change without the integrator)

| File | What it owns |
|---|---|
| `api.ts`, `api.gen.ts` | every route, typed; `errorText` |
| `store.ts` | overview (`/api/overview?topics=1`: archived topics counted, not listed), the open conversation, connection, drafts, toasts, tab, invite sheet, room panel; `store.run(fn, okText)` performs an action and reports failure in words |
| `model.ts` | the `TOPICS` tunables (bar size, chip widths, page sizes, search delay: change them there, never as a setting; `titleMax`/`pageMax` repeat the server's limits, pinned by `TestMessengerTopicLimitsMatchGo`), `Topic`/`topicOf`/`topicMark`/`newestFirst`, names, chat list items (`ChatItem.topics`: an agent's topics not archived; `ChatItem.topicTotal`: all of them; `ChatItem.needsYou`: OKs this device gives in that chat; `ChatItem.held`: questions or tasks held there for you, never counted as OKs), participants, plain-language states, time words, hues, initials; people by device (`personOf`, `isMine`, `nameOf`); conversation items from `overview.needs_you` / `.held` (`Reason`, `decidable`, `chatOf`, `chatName`, `senderOf`, `convTitle`), shared by the chat list's banner and OKs |
| `context.tsx` | `useApp()` (the store), `useAgentNames()`, `useWide()` |
| `owned.tsx` | the skin's own tree: `usePortal()` (the container every Base UI `*.Portal` renders into, `container={portal}`), `useOwned().root`, `focusedIn(root)` (focus from the shadow root), the theme painted on every mounted root (`paintTheme`, `ownTheme`) |
| `host.ts`, `main.tsx` | the host's public API types; `mount`/`unmount` (root setup, notification routing, teardown) |
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
| `features/Conversation.topics.tsx`, `features/Conversation.alltopics.tsx` | `TopicBar({ thread })` (the bar and the open topic's menu), `TopicEnd({ ctx })` (end of a done or archived topic), `TopicMark`, `topicLabel`, `changeTopic`, `barTopics`; `AllTopics(...)` (side panel / full-screen sheet over `/api/topics`) | Conversation, ChatList |
| `features/ChatList.topics.tsx` | `TopicResults({ query, onCount })` (topics in the chat search) | ChatList |
| `features/Composer.tsx`, `features/Emoji.tsx` | `Composer({ dm?: T.DMThread; thread?: T.Thread })`, `EmojiPicker({ onPick, onClose })`; `Composer.focus.ts`: the one rule for putting the cursor in the message field (`useFieldFocus`, `focusComposer(root, conv)` for a Reply chosen outside the composer, `coarse()`) | Conversation, Message (reactions, Reply), Approvals (Reply) |
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
- **A standalone skin:** only the host API; never `fetch('/api…')`,
  `EventSource`, page globals (`window.agentnet…`) or anything outside the
  root: no `document.body`/`documentElement` writes (theme, `lang`, classes,
  styles), no `document.querySelector`/`activeElement` (use `owned.tsx`).
  Every Base UI portal gets `container={usePortal()}`; dialogs use
  `modal="trap-focus"` and spread `useModal(open)` on their `Dialog.Popup`
  (aria-modal, the app behind inert, Tab kept inside), and menus use
  `modal={false}` (a fully modal lock would style the page's body and hide
  the page's other elements). Global listeners are removed on unmount.
  Assets come from the package (`new URL(…, import.meta.url)`);
  `@font-face`/`@property` go to `src/document.css` or are moved there by
  the build. `internal/ui/testdata/skin_contract_check.cjs` checks all of
  this on the built package.
- **CSP:** no `style="..."` in markup strings, no `dangerouslySetInnerHTML`,
  `innerHTML`, `eval`. React's `style` prop is fine (it uses the DOM style
  object). Libraries must not inject `<style>` elements.
- **Mentions:** `[@Name](agentnet:person/ID)` and `agentnet:guest/PID` stay the
  wire format in message text (see `app.js` `mentionRef`, `encodeMentions`).
- **Access:** every target at least 44px; visible focus; labels on icon
  buttons; `role="log"` for the timeline; works at 390px and 1440px, light
  and dark; reduced motion turns loops and slides into short fades.
- **Phone keyboard:** the frame is sized from the root (`h-full`, never
  `h-dvh`), bottom sheets sit on `--an-keyboard` (docs/UI_SKINS.md), and the
  timeline keeps its newest message in view when the keyboard shrinks it.
  On a touch screen a choice made around the message field (an intent, a
  chip's cancel) puts the cursor back only when the person was typing, and
  never with `preventScroll`; Reply always does, inside the tap's own
  handler (iOS opens its keyboard only then). Use `Composer.focus.ts`, not
  a second mechanism.
- **Design:** `docs/plans/MESSENGER_DESIGN.md` (the "Pop Huddle" contract)
  and the mockups it names.
