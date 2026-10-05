# Skins

AgentNet is the core: the program (the daemon on this computer, or the
browser engine a relay serves) and the skin contract documented here, host
API v1. Every interface is a **skin**: a standalone package that draws the
whole interface (navigation, layout, conversations, dialogs, settings and
flow) on that contract. A skin uses the same data and actions as any other;
it does not change membership, encryption, delivery or permissions.

Anyone can build a skin. The built-in ones are packages too, loaded through
exactly the same path; only trust differs (see [Trust](#trust)).

- **Comic** (`comic`) is AgentNet's own skin and the default: the messenger,
  built from `internal/ui/web` into the package `internal/ui/static/skins/comic/`
  and embedded in the program.
- **Classic** and **Zoom** (`classic`, `zoom`) are standalone reference packages,
  built from their own folders in `internal/ui/skins/` into embedded packages
  under `internal/ui/static/skins/`. Each can be copied and built independently.
- Installed skins: packages placed next to the program (below), and
  browser-local skins imported into one browser.

This is the trusted package boundary for a future skin marketplace. It does
not implement marketplace discovery, downloads, signing, sandboxing or
billing. Distribution is client-owned: a person can install, choose and share
a skin without changing their relay or other clients.

## How a skin is loaded

The UI host (`internal/ui/static/loader.js`) does the same for every skin:

1. reads the catalog (`/assets/skins/index.json`: the built-in packages first,
   then installed ones) and the packages stored in this browser;
2. picks the skin: `?skin=<id>`, else the one chosen before in this browser,
   else Comic. A saved `default` or `classic` (the names before skins were
   packages) opens Comic and is rewritten once; choices made through the package loader
   are marked separately so selecting new Classic survives reload. `?skin=default` names Comic;
3. asks for trust unless the skin is built in;
4. adopts the package's [document rules](#document-rules-fonts) at document level;
5. gives the skin a root in a shadow tree of the page's `#skin` element, links
   the host's base sheet and then the package's stylesheet inside it;
6. imports the entry and calls `mount(root, host)`; on a workspace switch (or
   a rebind) it calls `unmount(root)` and mounts again on a fresh root over a
   host bound to the new membership.

Comic draws its own way to switch (Settings → Appearance → Skin) and its
own workspace menu. Every other skin gets the host's **switcher bar** above
it: one step back to Comic, the skin menu (Comic, then installed and
browser-local skins, with import and removal), and the workspace control
(switch, join, leave, reconnect). The bar sits in the page's flow above the
skin's box, never over it, in its own shadow tree, so no skin stylesheet can
hide or restyle it; notifications the skin does not take wait there with a
button that opens them in Comic. This keeps the way back reachable for a
conforming skin; a skin's code still runs with the page's full trust, so this
is a conformance expectation, not a security guarantee.

### Trust

A built-in skin is trusted by the host's own fixed list of ids (`comic`,
`classic`, `zoom`), never by anything a manifest says. No installed or
browser-local package may use those ids or `default`, or show as a built-in
skin (the name Comic, Classic or Zoom, in any case or spacing): the program
skips such a directory and the browser refuses such a package. Every list of
skins says where each one comes from (built in, installed on this computer,
stored in this browser). The rules live in one browser module,
`/assets/skin-choice.mjs` (which skin opens, its trust, the reserved ids and
names), shared by the host and the browser-local package manager; Go tests
pin it to `static/skins.go`. Any other skin asks first:
it can read your chats and act as you, including sending messages and
approving work. Consent is to the package's exact digest, per browser origin
(including the daemon's port); a changed package asks again.

Installed code has the same access as Comic. This is **not** a sandbox.
Installing on a relay makes the package available to every browser device
using that relay; the relay operator already controls the page served to
those devices. Never install an untrusted package or let another account
write these directories.

## Install and update a skin

### On this computer or a relay

Place a package in `<home>/skins/<id>/` for a laptop daemon, or
`<hub-data>/skins/<id>/` for a relay's browser page, and restart the serving
process. No Go changes, rebuild or changes to embedded files are needed.

On Unix, the skins directory, package directories and files must belong to
the serving user and must not be writable by group or others. Keep the
home/data folder private. On Windows, access control comes from the home
folder's inherited ACL; AgentNet does not inspect Windows skin ACLs
separately.

Choose it in Comic under **Settings → Appearance → Skin**, or open the page
with `?skin=<id>` after authenticating. The first use asks for trust.

**Update:** replace the package's files and restart the serving process.
Packages are immutable snapshots until restart, so replacing files never
swaps code underneath an open page; the new digest asks for trust again.
**Uninstall:** remove the directory and restart; a browser that had chosen it
opens Comic.

The consent digest covers the manifest and every declared file. Requests
cannot read undeclared files, paths outside the package, or symlinks.
Limits: 32 packages, 32 files per package, 4 MiB per file, 16 MiB per
package and 64 MiB for the catalog.

### Import into this browser

In Comic under **Settings → Appearance → Skin**, or from the switcher's
**Import or remove skins…**, choose a package folder, or select its
`skin.json` and declared files. Import validates and stores the package; it
does not execute it or upload anything. Choose it under Skin, review the
trust notice and digest, then choose **Use this skin**. Browser-local IDs
have a `local:` prefix, so a relay package cannot replace a local selection.

**Update:** import the new version the same way; its new digest asks for
trust again. Tabs already using the version they loaded keep it until they
reload. **Remove** deletes every version of that package in this browser.

The browser's CacheStorage keeps immutable package snapshots. Its existing
service worker serves only the reserved `/local-skins/<digest>/` asset path;
chat and API requests keep their normal transport. Relative imports and CSS
remain inside the selected version. The 64 MiB local limit includes retained
versions and manifest bytes. Clearing browser site data or storage eviction
can remove packages; Comic remains. Packages and consent belong to this
browser profile and origin, including the daemon port, not to a workspace or
relay.

This requires a secure context with Service Workers, CacheStorage, Web Locks
and WebCrypto. Unsupported browsers say that skins cannot be stored there.
Choosing a local skin explicitly activates the asset worker; importing alone
does not take over tabs. No marketplace or package download service is
involved.

## Manifest

`skin.json`:

```json
{
  "api": 1,
  "id": "notebook",
  "name": "Notebook example",
  "entry": "entry.mjs",
  "style": "style.css",
  "document": "document.css",
  "files": ["entry.mjs", "style.css", "document.css", "fonts/body.woff2"]
}
```

IDs use lowercase letters, digits and hyphens, starting with a letter;
`comic`, `classic`, `zoom` and `default` are reserved. `files` lists every
file the package uses (at most 32, no duplicates); only declared JS/MJS, CSS,
JSON, PNG, SVG, WebP and WOFF2 files are served. `entry` (JS/MJS) and the
optional `style` and `document` (CSS) must appear in `files`. Unsupported API
versions and invalid packages are omitted. Package-owned imports and assets
use relative paths (resolve them against `import.meta.url` in code, or
relative to the stylesheet in CSS); the one host module a skin may import is
`/assets/typing.mjs` (`mountTyping`, the shared typing presenter). There is
no CDN loading or evaluation of inline code: the page's CSP allows no inline
styles or scripts, so use the stylesheet, never `style` attributes in markup.

### Document rules (fonts)

Browsers ignore `@font-face` and `@property` inside a shadow tree. A package
that needs them declares a `document` stylesheet: the host reads it, keeps
**only** its `@font-face` and `@property` rules, resolves every `url()`
against the file and drops a rule whose URL leaves the package, and adopts
the result at document level before mounting. Anything else in that file is
ignored. Comic declares its Onest and Rubik faces and Tailwind's registered
properties this way.

### The root and its styles

The entry exports `async function mount(root, host)` and optionally
`async function unmount(root)`. The skin owns `root` and nothing outside it:

- `root` lives in a shadow tree of `#skin`, under the host's base sheet
  (`/assets/skin-base.css`: `core.css` in the lowest cascade layer, so the
  package's own rules always win) and the package's stylesheet. Semantic
  colors such as `--surface`, `--text`, `--muted`, `--accent` and `--danger`
  inherit from the page; a package can replace them.
- The skin's box fills the page under the switcher (or the whole page for
  Comic) and scrolls inside itself; the page never scrolls.
- The page is the part of the screen a phone's keyboard leaves visible.
  Android shrinks the page itself (the page's viewport has
  `interactive-widget=resizes-content`). iOS shrinks only the visual
  viewport, so there the host sets two custom properties on `<html>`, which
  inherit into the shadow tree: `--an-viewport-h`, the page's height while
  the keyboard is open, and `--an-keyboard`, how much of the screen's bottom
  it covers. Both are absent while nothing covers the page. Size the skin
  from `root` (`height: 100%` down to the frame), never from `vh`, `dvh` or
  `svh`, so the message box stays above the keyboard; a fixed bottom popup
  (a bottom sheet) sits at `bottom: var(--an-keyboard, 0px)` and is no
  taller than `var(--an-viewport-h, 100dvh)`. Both names are the host's
  own: a skin reads them and never sets them.
- Put theme, tokens and resets on `root` (`:root` and `html`/`body` rules do
  not match in a shadow tree). Render popups (menus, dialogs, sheets) into a
  container inside `root`. Read focus from `root.getRootNode().activeElement`,
  query from `root`, never `document`. Do not write to `document.body`,
  `<html>` (`lang`, classes, styles), the page's adopted style sheets or font
  set, or anything else outside `root`, and do not read the host's own page
  globals.
- A modal dialog is modal inside `root`: mark it `aria-modal="true"`, make
  the rest of the skin behind it `inert`, and keep Tab and Shift+Tab within
  it. Page-level scroll locks and `aria-hidden` on the page's other elements
  (what many dialog libraries do in their fully modal mode) write outside
  `root`. Comic does this for every sheet and confirmation (`owned.tsx`
  `useModal`).
- `unmount` stops what `host.listen` returned, timers, object URLs and every
  `window` or `document` listener the skin added, and leaves `root` empty.
  Use DOM text nodes for message and profile content.

The shadow tree is styling isolation, not a security boundary: the code
still runs with the page's full trust.

## Host API v1

| Method | Contract |
| --- | --- |
| `host.version` | `1`; additive extensions keep this version. |
| `host.platform` | `daemon` or `browser`. |
| `host.api(path, body?)` | Existing JSON `/api/*` read/action surface. Omit body for GET; provide an object for POST. Resolves parsed JSON or rejects with a user-readable error. A skin never fetches `/api` or `/events` itself. |
| `host.listen(fn)` | Push events `{type:"change",seq}`, `{type:"disconnect"}` or `{type:"restart"}`. Returns an unsubscribe function. Reload data on change. Reconnect/reload after interruption; do not poll. |
| `host.onOpen(fn, kinds?)` | Required. Registers notification routing: `fn(target, kind, context)`. Every skin gets `"channel"` (a browser notification's channel: resolve it with GET `/api/notify/resolve?chan=…`, which answers `{conv}` when this device has that conversation; an empty channel means news in more than one) and `"conversation"` (a DM id). Listing `"message"` in `kinds` adds `fn(messageID, "message", {conv?, dir?})` (the conversation and direction the notification names) and `"review"` adds `fn("", "review")`. A destination a skin does not take waits in the switcher with the way to open it in Comic. A missing destination opens the skin's list, never another arbitrary chat. |
| `host.stage(file)` | Prepares a File for sending; resolves an opaque attachment value for `files` in a send. Keep that value only until that send consumes it. Browser encrypts through its engine; daemon stages locally. |
| `host.file(messageID,index,dir)` | Opens an attachment this device holds: a received one, or a copy kept of one it sent. Pass the message's own `dir` (`in` or `out`): a received id is the sender's choice and can equal a sent one here. Resolves `{bytes:Uint8Array}` (browser may add name/size/image). Show Open only where the view says `openable: true`; a sent file without a kept copy says so in `note`. Validate magic bytes before inline display; never execute HTML or SVG. |
| `host.drive` | Optional project-space provider bound to this host's membership: `drive({conv, action, ...})` resolves the existing Drive view/result; `driveUpload(conv, file, confirm)` uploads plaintext to Google only after explicit confirmation. It reuses the core's Drive actions, consent and permissions. Browser providers also expose `prepareGoogle()` and `beginGoogleConsent({conv, full?, confirm_account})`: prepare first, then call begin directly from the confirming click, with no intervening await, to retain the browser user gesture. Never stage this upload through `host.stage`; Google files are outside AgentNet encryption. Switching workspace never retargets a captured provider; retired/disconnected bindings reject new calls. No tokens or raw transport are exposed. Absence/configuration errors must be shown as unavailable. |
| `host.skins` | The catalog: `{api, id, name, digest?, local?, builtin?}` for Comic, the other built-in, installed and browser-local skins. `builtin` is the host's word (its fixed list), never a manifest's. The array is updated in place. |
| `host.onSkinsChange(fn)` | Calls `fn()` when `host.skins` changes (a browser-local skin imported or removed). Returns an unsubscribe function. |
| `host.selectSkin(id)` | Reloads into a skin from `host.skins`. The current skin must preserve or explicitly resolve unsent drafts first. |
| `host.manageLocalSkins(root)` | Optional (present where the browser can store skins). Draws the host's browser-local skin manager (import files or a folder, the stored skins, Remove) into an element the skin owns; returns its teardown, which empties that element. Consent stays the host's: a stored skin asks for trust when chosen. |
| `host.reconnect()` | Optional, present only on this computer's program with workspaces. After the program restarted and retired this membership's handle, binds the same membership again (only when the program still names it with the endpoint, realm, address and key it proved in its own overview) and mounts the skin again over the new binding. Resolves without remounting when the binding still holds. Nothing under way is retargeted or replayed: staged files and sends stay with the old binding, so a skin keeps text drafts and asks for files again. |
| `host.workspace` | The membership this host is bound to: `{id, name, endpoint, address, realm, state}`. A host never changes membership: what a skin holds when an operation starts (a send, a staged file, a file open) stays bound to it. |
| `host.workspaces` | `null` on a program without workspaces. Otherwise `{list(), active(), has(id), select(id), state(id), onChange(fn), join({name, invite, agent}), disconnect(id)}`. `has(id)` is by local registration only, never by anything a message or notification says. Optional, present only on this computer's program (never in a browser enrollment), so check before use: `disconnected()` resolves the memberships disconnected here, each `{id, name, endpoint, address, realm, state}` (`list()` leaves them out); `reconnect(id)` routes one of them again, as the same membership with the same keys and history under a new handle, and refuses one whose identity changed. It does not select it. |

### Workspaces

When the person selects another workspace (in the skin's own menu or the
host's switcher) the host mounts the skin again with a host bound to that
membership: `module.unmount(root)` (optional) is called first, then
`module.mount(newRoot, newHost)`. Keep drafts in
`host.workspaces.state(host.workspace.id)`, keyed there by conversation type
and id (Comic also keeps text drafts per workspace in this browser's
storage, so they survive a reload and a switch of skin). The same
conversation id in two memberships still names separate drafts; shared
labels never merge people or workspaces. Stop what `host.listen` returned in
`unmount`. An operation started before the switch keeps its host: a send
under way goes where it was written, a staged file belongs to the workspace
it was staged in (the host refuses it elsewhere).

Disconnected memberships (BUG-19): `GET /api/workspaces` lists the mounted
memberships only, each with its `handle`. This computer's program lists
every membership with its `state` at `GET /api/workspaces/all` (a
disconnected one has `state: "disconnected"` and no handle) and routes one
again with `POST /api/workspaces/reconnect` and body `{"id": "<its id>"}`
(JSON, same origin, at the page's root, never under a membership's
`/workspaces/<id>/<handle>/` prefix). The answer is the membership bound again
under a new handle (`state: "enrolled"`); 404 says no disconnected
membership has that id. `host.api` is bound to one membership's prefix, so
a skin reaches these two through `host.workspaces.disconnected()` and
`host.workspaces.reconnect(id)` (above), present on this computer's program
only; the host's switcher offers Reconnect over every skin but Comic, which
draws its own workspace menu.

A notification fragment is `#conv=<hash>&workspace=<id>`,
`#msg=<id>[&conv=<hash>][&dir=in|out][&workspace=<id>]` or
`#review[&workspace=<id>]`: the host verifies the workspace id against its
registrations, selects that workspace, then calls the `onOpen` handler; an
unknown id opens nothing (never the current workspace instead).

The host handles the authenticated transport, browser engine and enrollment.
It does not interpret receipt states as task completion, silently approve
anything, or automatically send messages. All effects still require normal
core checks. A skin should use returned actions/capabilities to decide which
controls exist. A browser has no local responder or daemon reminders.

Common JSON routes (see `internal/ui/ui.go` for concrete view types and
`internal/ui/server.go` for request bodies):

- Reads: `/api/overview`, `/api/thread?id=...`, `/api/dm?id=...`.
- Conversations: `/api/send` `{to,kind,body,reply_to,files}`;
  `/api/dm/new`; `/api/dm/send` `{conv,body,reply_to,files}`.
- Decisions: `/api/act`; returned message actions determine availability.
- Standing grants: GET `/api/approvals` lists what `agentnet approvals` does:
  `{questions[{address}], tasks[{address, fingerprint, status}],
  participations[{conv, pid, agent_id?, keys[], tasks_from[], external?}],
  read_only, unresolved?[]}` (`unresolved`: conversations whose agents
  cannot be resolved here now, such as a group whose context is pending;
  their grants are listed once they can be); `POST /api/approvals/revoke` `{kind: "question"|"task",
  address}` or `{kind: "participation", pid}` ends that one grant (as
  `unapprove`, `unapprove --tasks`, `dm dismiss-agent`; an `external` one is
  ended by a member only). A browser keeps none: its list is empty and
  `read_only`, and revoking is refused.
- Identity: `/api/person` creates a person (it does **not** rename);
  `POST /api/person/label` `{label}` changes the existing person's self-claimed
  display name through a signed roster step. Keep person ID, keys, devices,
  routing addresses, history and grants separate from that name. A failed or
  ambiguous confirmation must not show an optimistic rename; refresh the
  person before retrying. First-contact directory trust is not independent
  verification of a real-world owner.
  Device operations:
  `/api/device/service`, `/api/device/link`, `/api/device/decide`,
  `/api/device/remove`.
- Agents: `/api/dm/agent/invite`, `/decide`, `/dismiss`, `/ask` under that prefix.
- Notifications: `/api/notify/enable`, `/disable`, `/mute`, `/allow`, `/seen`,
  plus GET `/api/notify/resolve?chan=...`.
- Files: `/api/upload/discard`, `/api/file/request`; use host stage/file for
  platform differences.
- Message controls: `POST /api/message/react` `{conv?,id,dir,emoji,remove?}`,
  `/api/message/edit` `{conv?,id,dir,text}`, `/api/message/delete` `{conv?,id,dir}`.
  A message view carries what controls did to it: `reactions[]` (`emoji`, `by[]`
  as `{id,label}`, `mine`), `edited`/`revision`/`text` (the text to show; `body`
  stays what was sent), `deleted`, and `can[]` (`react`, `edit`, `delete`): show
  only the actions listed. An edit never reruns anything; a deletion hides text
  and files and recalls nothing already read, saved or given to an agent.
- Reminders: `/api/remind` and existing `/api/remind/{action}`.
- Connecting coding sessions (MEL-528): GET `/api/assistant-setup` lists the
  tools found on this computer `{local, harnesses[{id, label, detected,
  configured, registered, supported, state, note, change?, next?, target?}],
  note?}` (`state`: connected, needs_activation, detected, needs_setup,
  not_detected, unsupported or error: write your own words for each and keep
  `note` and `target` behind details). POST `{action: "review", harnesses}`
  adds `review_id`; POST `{action: "apply", harnesses, review_id}` applies
  exactly that reviewed change and refuses one that changed since. Then each
  chosen tool that `/api/agents` lists as `found` gets its named agent through
  POST `/api/agents` `{action: "create", label, harness, dir}` or `{action:
  "update", id, harness, dir}` (reuse an agent with the same tool, name and
  folder instead of making a second), and one `{action: "publish"}`.
  Read the tool list first: `local: false` (nothing can be installed here)
  means show `note` and skip `/api/agents`. Say an agent is ready only when
  its `responder.ready` says so. The folder is chosen by browsing GET
  `/api/folders?path=` (read-only), never typed; say so when `truncated` is
  set, and when a folder can't be read (deleted, or closed to the person)
  still offer Up and Home, not only a retry. Setup approves nobody, shares
  no history and keeps the default agent.
- Topics (an agent's device threads, docs/plans/TOPICS.md): `overview.threads`
  lists every thread, archived topics too; a skin that pages topics itself
  asks `/api/overview?topics=1` and gets them without archived topics.
  `overview.topics[]` counts each peer's topics (`total`, `archived`,
  `archived_unread`, `latest`) and `overview.topic_list` says the routes
  below exist. A thread summary carries `state` (active, done, archived),
  `done_by` (agent, you), `conclusion` (the final reply's first line: the
  agent's words when `done_by` is agent, label it as theirs; the person's
  own when `done_by` is you), `concluded_by`, `pending`, `renamed`,
  `auto_title`, `quiet_since`; `/api/thread` adds `topic`. GET
  `/api/topics?peer=&state=&q=&before=&limit=` pages `{topics, next,
  matched}`; POST `/api/topic/rename` `{peer,id,title}`, `/api/topic/done`
  and `/api/topic/reopen` `{peer,id,count}` (`count`: the messages shown).
  A name and the Done/Reopen marks are this device's only; say so.
- Headless: a request message may carry `exec` `{state, at, host, stale, attempt?, detail}`,
  the executing host's own signed word (never delivery, presence or a timer);
  show it apart from delivery and say when it is stale. A review notice may carry
  `report` `{host, at, items[{id, from, kind, state, blocker, since, key?, attempt?, excerpt?, actionable, result?}]}`:
  a snapshot at `at`, never a live queue; offer actions only for `actionable`
  items, through `POST /api/operator/decide` `{host, id, key, action, expect,
  attempt, text, report}`; the host's answer lands on the item as `result`.
  The host applies a decision only on a report it sent that operator and still
  holds: once the host has deleted its chat with the operator, decisions on the
  reports in it are refused.
  A notification's `#msg=<id>` (optionally `&conv=…&dir=in|out`) lands on that
  message and does nothing else.
- Storage: GET `/api/storage` (read-only; `local.areas[]` with known usage or an
  `unknown` reason, `local.complete`, `remote.status` available | unsupported |
  unavailable with the Hub's own-usage report). Show unknown as unknown, never
  zero; the Hub's quota is the whole server's, never an allowance.
- Presence refresh: `/api/refresh`, triggered by user navigation; no polling.
- What a conversation view says without its sentences: a DM message's
  `reply_to` names the message as this device shows it (an agent's answer
  names its executor's copy of the request; the view links it to the
  request's own `id`); a participation record carries `event_type` (`invite`,
  `accept`, `decline`, `dismiss` or `scope`) and `event_by` (its author's
  person label as known here, or device address) beside `event`. A DM
  summary carries `guests` (active participations: people and agents),
  `decide` (its requests with actions for this device's person; none on a
  browser) and, when its latest row is a participation record, `last_event`
  `{kind, pid, by}` (a scope counts as its invite). A device thread summary
  carries `agent_id` when one is named.


## Build your own skin

1. Start from `examples/skins/notebook/` (plain modules and CSS, no build
   step) or any toolchain whose output is ES modules and CSS. Comic is built
   with React, Base UI and Tailwind (`internal/ui/web/build.sh`); its package
   is the reference for a bundled skin.
2. Write `skin.json` listing every file. Register `host.onOpen` in `mount`,
   use only the host API above, keep everything inside `root`, and leave
   `root` empty in `unmount`.
3. Try it without installing: import the folder into a browser (Comic,
   Settings → Appearance → Skin). Or install it next to the program and
   restart.
4. Check it (below), on both providers, desktop and phone, light and dark,
   with unread/error/offline states, file safety, drafts, keyboard/focus and
   notification routing. Never use live enrollment codes or real messages as
   a fixture.

## Checks

**Contract only** (`internal/ui/testdata/skin_contract_check.cjs`, Go test
`TestSkinPackagesContractOnly`, opt-in with `AGENTNET_PLAYWRIGHT`): mounts
packages from copied package bytes at an unrelated path, with only a public
host the check implements itself (no loader, no workspace shell, no page
globals), over the demo daemon. It fails on a read of a private page global,
a direct `/api` or `/events` request, a request for an undeclared file (the
documented `/assets/typing.mjs` excepted), any DOM write outside the root
(including the page's adopted style sheets and font set), a `window` or
`document` listener left after `unmount`, errors, or a root left non-empty
after `unmount` (mount/unmount A/B/A). Comic's journey also sends a message
through the host and requires its sheets and confirmations to be modal
inside the root (aria-modal, the app behind them inert, focus kept inside
through 10 Tabs and 10 Shift+Tabs). It runs Comic and the Notebook example
at 1440 and 390 pixels:

```sh
AGENTNET_PLAYWRIGHT=/absolute/path/to/playwright-core go test ./internal/ui -run TestSkinPackagesContractOnly -v
```

**Production host** (`skins_browser_check.cjs`, `TestSkinsRendered`): the real
loader and catalog with Comic as a package (shadow root, fonts and
properties adopted, no switcher), saved choices, an installed skin behind
its trust step under the switcher (layout, keyboard, theme), notification
destinations (a review notice that names only a message opens its
conversation in Comic), a browser-local skin imported, re-trusted after a change and
removed, and the step back to Comic, at 1800×960 light and 390×844 dark.
`workspace_reconnect_browser_check.cjs` covers the switcher's workspace
control.

Digest-bound passing evidence establishes those checks for those bytes, not
a sandbox, safety guarantee or certification of harmlessness. Skins still
execute with the page's full trust.

## Independent example: Notebook

`examples/skins/notebook/` is a small independent skin: a notebook with a
table of contents (people, then devices and services), one conversation page
at a time, a To line and target-named Send button, files added by choosing or
dropping, received and kept files opened as clippings, reactions, edits and
explicit decision buttons, in light and dark, desktop and phone. It imports
nothing of Comic. It is a developer example, not a feature-complete skin: it
says on its page that finding new people, adding devices, settings and
notifications are not in it, and the switcher above it takes you to Comic
for those.

Notebook keeps text, native File objects, send kind and selected conversation
in the host's per-workspace view state. Switching A/B/A restores that
workspace's draft and target, even when another workspace has the same ids.
Sends already started keep their original host and target; returning during a
send does not duplicate it. Accepted sends clear only the text and files they
captured; new edits and other drafts stay.

When the event stream ends (AgentNet restarting, or the connection lost),
Notebook reconnects by itself, with growing pauses: through
`host.reconnect()` where the host has it (the same membership bound again,
Notebook mounted again over it, text drafts kept), then a fresh stream and
refresh. After about a minute without an answer it asks for a reload.

While any workspace has draft text, files or a send in progress, Notebook uses
the browser's native `beforeunload` warning for host escape/reload. Cancel keeps
the page and drafts; confirming leave discards this page's in-memory drafts.
A send already under way may still be accepted after leaving. Clean pages have
no leave listener. This is not crash durability: warning display needs browser
support and user interaction and is unreliable on mobile OS termination. See
[the browser API's limits](https://developer.mozilla.org/en-US/docs/Web/API/Window/beforeunload_event).

Comic keeps text drafts per workspace in this browser; switching skins asks
to send or remove files waiting in a draft first, since files cannot survive
the reload.

### Notebook journey and digest evidence

Use an already installed Playwright Core and Chromium, with a fresh isolated
profile. No live enrollments, real peers or agent execution are used. Example:

```sh
umask 077
mkdir -p /tmp/agentnet-notebook-check/screenshots
AGENTNET_PLAYWRIGHT=/absolute/path/to/installed/playwright-core \
AGENTNET_CHROMIUM=/absolute/path/to/chromium \
AGENTNET_SCREENSHOTS=/tmp/agentnet-notebook-check/screenshots \
node internal/ui/testdata/notebook_conformance_check.cjs \
  --skin-dir examples/skins/notebook \
  > /tmp/agentnet-notebook-check/evidence.json
```

`--skin-dir` defaults to the example directory. The runner snapshots the manifest
and declared files and calls existing `local-skins.prepare()` for the same
package digest used by installation/consent. Its JSON identifies that package,
digest, host version, adapter, browser, viewport journeys and screenshot paths.
The server serves those exact snapshotted bytes throughout the run. Keep evidence
and screenshots private and local unless separately authorized to publish them.

The adapter is `notebook-device-thread-drafts-v1`: Notebook device-thread
navigation, asynchronous sends/refusal/queue, files, A/B/A host-state retention,
and native leave cancel/confirm/clean behavior at 390 and 1280 pixels. It uses a
captured Host API v1 provider and the actual `WorkspaceShell.state`; it does not
certify all providers, DMs, all skin flows, live routing or a mobile OS. Its
controls reproduce host navigation for the native dialog test; this journey
does not replace the separate production-loader tests.

Only a Notebook package declaring `entry.mjs` and `style.css` is supported by
these selectors. Other packages return `status: "unsupported"`, `pass: false`
and exit 2; invalid/failing packages exit nonzero. A Notebook candidate using
`--skin-dir` is tested as Notebook, never described as generic conformance.
An advisory review should name the digest and tested journeys alongside any
unchecked flows. Digest-bound passing evidence establishes those compatibility
checks for those bytes, not a sandbox, safety guarantee or certification of
harmlessness. Skins still execute with the page's full trust.
